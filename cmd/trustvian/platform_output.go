package main

// Output and flag plumbing shared by the platform command families.
//
// Two rules shape everything here.
//
// Results go to stdout, diagnostics go to stderr, always. A shell pipeline
// that captures stdout must get evidence or nothing — never an error message
// where JSON was expected.
//
// --json forwards the server's own response body. Decoding into a CLI struct
// and re-encoding would create a second field namespace with its own
// versioning problem, and would silently drop any field a newer server added.
// The DTOs below exist only to render human output.

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strconv"
)

// streams is where a command writes. Injected so tests observe the
// stdout/stderr split directly rather than through process redirection.
type streams struct {
	out io.Writer
	err io.Writer
}

// commonFlags are the two every platform command accepts.
//
// The flag set is retained so presence can be recovered after parsing. An
// empty string is not the same as an absent flag: `--api-url ""` means the
// caller chose an endpoint and got it wrong, which must stay a usage error
// rather than quietly becoming "use whatever is in this directory".
type commonFlags struct {
	fs     *flag.FlagSet
	apiURL *string
	json   *bool
}

func registerCommonFlags(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		fs: fs,
		apiURL: fs.String("api-url", "",
			"base URL of the control-plane API (default: the local runtime in ./.trustvian)"),
		json: fs.Bool("json", false, "write the API's JSON response to stdout"),
	}
}

// apiURLNote is the discovery footer every platform usage block ends with.
//
// Shared rather than repeated so the four command families cannot drift into
// describing the same resolution rule three different ways.
const apiURLNote = "\n\n" + `--api-url is optional: a runtime started with ` + "`make local`" + ` is found
through ./.trustvian/runtime.json in the current directory.`

// apiURLSet reports whether --api-url appeared on the command line.
//
// fs.Visit walks only the flags actually set, which is the one thing the
// parsed value cannot tell us. Kept here so no leaf command has to remember
// the distinction.
func (c commonFlags) apiURLSet() bool { return flagWasSet(c.fs, "api-url") }

func flagWasSet(fs *flag.FlagSet, name string) bool {
	var found bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// emitSuccess writes one successful result.
//
// In --json mode the server's bytes pass through untouched apart from a
// trailing newline, so an additive field the CLI has never heard of still
// reaches the caller.
func emitSuccess(s streams, asJSON bool, body []byte, render func(io.Writer) error) error {
	if asJSON {
		trimmed := bytes.TrimRight(body, "\n")
		if _, err := s.out.Write(trimmed); err != nil {
			return operationalErrorf("writing output: %v", err)
		}
		if _, err := io.WriteString(s.out, "\n"); err != nil {
			return operationalErrorf("writing output: %v", err)
		}
		return nil
	}
	return render(s.out)
}

// emitError writes a diagnostic to stderr and returns the exit code.
//
// Never to stdout. A caller redirecting stdout to a file is collecting
// evidence, and an error written there is indistinguishable from a result
// until something downstream fails to parse it.
func emitError(s streams, asJSON bool, err error) int {
	var perr *platformError
	if asJSON && asPlatformError(err, &perr) && len(perr.envelope) > 0 {
		// The server's envelope verbatim: the CLI defines no second error
		// schema, so a tool that already parses API errors parses this one.
		trimmed := bytes.TrimRight(perr.envelope, "\n")
		fmt.Fprintf(s.err, "%s\n", trimmed)
		return perr.code
	}
	fmt.Fprintf(s.err, "trustvian: %v\n", err)
	return exitCodeFor(err)
}

func asPlatformError(err error, target **platformError) bool {
	perr, ok := err.(*platformError)
	if ok {
		*target = perr
	}
	return ok
}

// usageFailure prints a flag set's usage and returns exit 2.
func usageFailure(s streams, usage string, err error) int {
	if err != nil {
		fmt.Fprintf(s.err, "trustvian: %v\n", err)
	}
	fmt.Fprintln(s.err, usage)
	return exitUsage
}

// requireFlag rejects an empty required value.
//
// Empty is never silently filled in. For identifiers in particular that would
// mean generating one, which ADR 0025 forbids: identity belongs to the caller,
// who usually already has a name for the thing from CI.
func requireFlag(name, value string) error {
	if value == "" {
		return usageErrorf("--%s is required", name)
	}
	return nil
}

// optionalUint64 distinguishes an omitted flag from an explicit zero.
//
// This is why it is a flag.Value rather than a uint64 with a default: zero is
// the strictest possible gate limit, so treating "omitted" as zero would apply
// maximum strictness to a caller who simply forgot a flag — failing their
// build for a policy they never chose.
type optionalUint64 struct {
	value uint64
	raw   string
	set   bool
}

func (o *optionalUint64) String() string {
	if o == nil {
		return ""
	}
	return o.raw
}

// Set parses exact uint64 in canonical form.
//
// Canonical because the value is forwarded to the server as text: "007" and
// "+1" parse to numbers a round trip would not reproduce, and MaxUint64 has to
// survive as an exact integer, which a JSON double would not.
func (o *optionalUint64) Set(raw string) error {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != raw {
		return fmt.Errorf("must be a canonical unsigned decimal integer")
	}
	o.value, o.raw, o.set = parsed, raw, true
	return nil
}

// canonical returns the text form sent on the wire.
func (o *optionalUint64) canonical() string { return strconv.FormatUint(o.value, 10) }

// ---------------------------------------------------------------------
// Rendering DTOs
// ---------------------------------------------------------------------
//
// These mirror the JSON nouns of the /v1 contract. They are not the platform's
// Go domain types and must never become them: the CLI decodes only what it
// prints, and an unknown field is ignored rather than rejected.

type projectDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type agentDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

// candidateMetadataDTO is both the request and response shape.
//
// omitempty so an unset flag is absent rather than an explicit empty string:
// the two mean the same thing to the server today, and sending the difference
// would be claiming a distinction that does not exist.
type candidateMetadataDTO struct {
	Label          string `json:"label,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	ArtifactDigest string `json:"artifact_digest,omitempty"`
	Model          string `json:"model,omitempty"`
	ToolsetDigest  string `json:"toolset_digest,omitempty"`
	ConfigDigest   string `json:"config_digest,omitempty"`
}

type candidateDTO struct {
	ID       string               `json:"id"`
	AgentID  string               `json:"agent_id"`
	Metadata candidateMetadataDTO `json:"metadata"`
}

type evaluationRunDTO struct {
	ID                string `json:"id"`
	CandidateID       string `json:"candidate_id"`
	Environment       string `json:"environment"`
	BehavioralProfile string `json:"behavioral_profile"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	FailureReason     string `json:"failure_reason"`
}

type progressDTO struct {
	RunID                    string `json:"run_id"`
	Status                   string `json:"status"`
	RecordCount              string `json:"record_count"`
	BehaviorObservationCount string `json:"behavior_observation_count"`
	DistinctBehaviorCount    int    `json:"distinct_behavior_count"`
	BehaviorComplete         bool   `json:"behavior_complete"`
	NextIngestSequence       string `json:"next_ingest_sequence"`
}

type ingestStateDTO struct {
	RunID        string `json:"run_id"`
	NextSequence string `json:"next_sequence"`
}

type ingestResultDTO struct {
	Disposition      string `json:"disposition"`
	NextSequence     string `json:"next_sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`
}

type maximumGateDTO struct {
	Actual  string `json:"actual"`
	Maximum string `json:"maximum"`
	Passed  bool   `json:"passed"`
}

type minimumGateDTO struct {
	Actual  string `json:"actual"`
	Minimum string `json:"minimum"`
	Passed  bool   `json:"passed"`
}

type gateDTO struct {
	ReferenceEvidence        minimumGateDTO `json:"reference_evidence"`
	CandidateEvidence        minimumGateDTO `json:"candidate_evidence"`
	AddedBehaviors           maximumGateDTO `json:"added_behaviors"`
	BlockDecisions           maximumGateDTO `json:"block_decisions"`
	CriticalRiskObservations maximumGateDTO `json:"critical_risk_observations"`
	Verdict                  string         `json:"verdict"`
}

type behaviorDiffDTO struct {
	AddedCount   int `json:"added_count"`
	RemovedCount int `json:"removed_count"`
	SharedCount  int `json:"shared_count"`
}

type compareDTO struct {
	ReferenceRunID string          `json:"reference_run_id"`
	CandidateRunID string          `json:"candidate_run_id"`
	Diff           behaviorDiffDTO `json:"behavior_diff"`
	Gate           gateDTO         `json:"gate"`
}

// ---------------------------------------------------------------------
// Human rendering
// ---------------------------------------------------------------------
//
// Observational output. It prints what the server returned and derives
// nothing: every number here is a field, never a computation.

func renderProject(w io.Writer, p projectDTO) error {
	fmt.Fprintf(w, "Project %s\n", p.ID)
	fmt.Fprintf(w, "Name: %s\n", p.Name)
	return nil
}

func renderAgent(w io.Writer, a agentDTO) error {
	fmt.Fprintf(w, "Agent %s\n", a.ID)
	fmt.Fprintf(w, "Project: %s\n", a.ProjectID)
	fmt.Fprintf(w, "Name: %s\n", a.Name)
	return nil
}

func renderCandidate(w io.Writer, c candidateDTO) error {
	fmt.Fprintf(w, "Candidate %s\n", c.ID)
	fmt.Fprintf(w, "Agent: %s\n", c.AgentID)
	for _, field := range []struct{ label, value string }{
		{"Label", c.Metadata.Label},
		{"Source ref", c.Metadata.SourceRef},
		{"Artifact digest", c.Metadata.ArtifactDigest},
		{"Model", c.Metadata.Model},
		{"Toolset digest", c.Metadata.ToolsetDigest},
		{"Config digest", c.Metadata.ConfigDigest},
	} {
		// Omitted rather than printed empty: metadata is descriptive, and a
		// blank line implies a field was set to nothing.
		if field.value != "" {
			fmt.Fprintf(w, "%s: %s\n", field.label, field.value)
		}
	}
	return nil
}

func renderEvaluationRun(w io.Writer, r evaluationRunDTO) error {
	fmt.Fprintf(w, "Evaluation %s\n", r.ID)
	fmt.Fprintf(w, "Candidate: %s\n", r.CandidateID)
	fmt.Fprintf(w, "Environment: %s\n", r.Environment)
	fmt.Fprintf(w, "Behavioral profile: %s\n", r.BehavioralProfile)
	fmt.Fprintf(w, "Status: %s\n", r.Status)
	fmt.Fprintf(w, "Created: %s\n", r.CreatedAt)
	if r.StartedAt != "" {
		fmt.Fprintf(w, "Started: %s\n", r.StartedAt)
	}
	if r.FinishedAt != "" {
		fmt.Fprintf(w, "Finished: %s\n", r.FinishedAt)
	}
	if r.FailureReason != "" {
		fmt.Fprintf(w, "Failure reason: %s\n", r.FailureReason)
	}
	return nil
}

// renderProgress stays factual.
//
// No pass, safe or promotable line: a running evaluation has progress, and a
// verdict-shaped field here would be read as an answer the data cannot give.
func renderProgress(w io.Writer, p progressDTO) error {
	fmt.Fprintf(w, "Evaluation %s\n", p.RunID)
	fmt.Fprintf(w, "Status: %s\n", p.Status)
	fmt.Fprintf(w, "Records: %s\n", p.RecordCount)
	fmt.Fprintf(w, "Behavior observations: %s\n", p.BehaviorObservationCount)
	fmt.Fprintf(w, "Distinct behaviors: %d\n", p.DistinctBehaviorCount)
	fmt.Fprintf(w, "Behavior evidence: %s\n", completeness(p.BehaviorComplete))
	fmt.Fprintf(w, "Next sequence: %s\n", p.NextIngestSequence)
	return nil
}

func completeness(complete bool) string {
	if complete {
		return "complete"
	}
	return "incomplete"
}

func renderIngestState(w io.Writer, s ingestStateDTO) error {
	fmt.Fprintf(w, "Evaluation %s\n", s.RunID)
	fmt.Fprintf(w, "Next sequence: %s\n", s.NextSequence)
	return nil
}

func renderIngestResult(w io.Writer, r ingestResultDTO) error {
	fmt.Fprintf(w, "Disposition: %s\n", r.Disposition)
	fmt.Fprintf(w, "Records: %s\n", r.RecordCount)
	fmt.Fprintf(w, "Behavior evidence: %s\n", completeness(r.BehaviorComplete))
	fmt.Fprintf(w, "Next sequence: %s\n", r.NextSequence)
	return nil
}

// renderComparison prints the server's evidence and its verdict.
//
// Every value is echoed, never recomputed — including the case where the
// counts and the verdict look inconsistent to a reader. The server owns that
// judgement; a CLI that "corrected" it would be a second gate implementation,
// and the dangerous direction of a second implementation is agreement for a
// year followed by a quiet divergence toward a false PASS.
func renderComparison(w io.Writer, c compareDTO) error {
	fmt.Fprintln(w, "Evaluation comparison")
	fmt.Fprintf(w, "Reference: %s\n", c.ReferenceRunID)
	fmt.Fprintf(w, "Candidate: %s\n", c.CandidateRunID)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Behaviors: +%d / -%d / %d shared\n",
		c.Diff.AddedCount, c.Diff.RemovedCount, c.Diff.SharedCount)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Gate checks:")
	renderMinimum(w, "Reference evidence", c.Gate.ReferenceEvidence)
	renderMinimum(w, "Candidate evidence", c.Gate.CandidateEvidence)
	renderMaximum(w, "Added behaviors", c.Gate.AddedBehaviors)
	renderMaximum(w, "Block decisions", c.Gate.BlockDecisions)
	renderMaximum(w, "Critical risk observations", c.Gate.CriticalRiskObservations)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Gate: %s\n", verdictLabel(c.Gate.Verdict))
	return nil
}

func renderMinimum(w io.Writer, label string, gate minimumGateDTO) {
	fmt.Fprintf(w, "  %s %s: %s (minimum %s)\n",
		checkMark(gate.Passed), label, gate.Actual, gate.Minimum)
}

func renderMaximum(w io.Writer, label string, gate maximumGateDTO) {
	fmt.Fprintf(w, "  %s %s: %s (maximum %s)\n",
		checkMark(gate.Passed), label, gate.Actual, gate.Maximum)
}

func checkMark(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

// verdictLabel echoes the server's verdict without interpreting it.
//
// An unrecognised verdict is printed as-is rather than mapped to a default:
// guessing would hide a server the CLI no longer understands, and the exit
// code path treats anything that is not "pass" as a failure anyway.
func verdictLabel(verdict string) string {
	switch verdict {
	case gateVerdictPass:
		return "PASS"
	case gateVerdictFail:
		return "FAIL"
	default:
		return verdict
	}
}
