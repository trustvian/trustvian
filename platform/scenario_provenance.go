package platform

// Scenario provenance on persisted executions (task 086).
//
// Each side of an execution records what it ran: the scenario definition's
// digest, the declared inputs' digest, and the model and prompt reference it
// declared. A comparison reads them to say whether both sides ran the same
// scenario over the same inputs with the same model and prompt.
//
// The CLI computes both digests, because it reads the files, and sends them
// when it begins the execution. The control plane validates their format and
// stores them; it cannot recompute them. A digest is therefore exactly as
// trustworthy as the client that sent it — the trust level every
// CandidateMetadata field already has.
//
// Nothing here is content and nothing here is identity. A model is an
// identifier and a prompt reference is a name and a digest, each without
// whitespace, so neither can hold prompt text; none of it reaches
// StableFeatures, a fingerprint or a baseline key.

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// The formats, restated from config's: the control plane validates whatever
// any client sends.
var (
	provenanceDigestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	provenanceIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)
)

// PromptRef identifies a prompt: a name from the producer's own prompt store
// and the digest of the prompt's bytes. It is never prompt text. Trustvian
// does not store, fetch or render prompt content, and no field named for
// prompt text will be added beside this one.
type PromptRef struct {
	Name   string
	Digest string
}

// Stated reports whether a prompt reference was declared.
func (p PromptRef) Stated() bool { return p != PromptRef{} }

// SideDeclaration is what one side declared about how its workload ran.
// Either field may be empty: not declared.
type SideDeclaration struct {
	Model     string
	PromptRef PromptRef
}

// SideProvenance is what an execution recorded for one of its sides.
//
// ScenarioDigest empty means the scenario was not recorded — an execution
// begun before schema 12, or by a client that sent no provenance — and then
// nothing about inputs is recorded either. With a scenario digest recorded,
// InputsDeclared says whether the scenario listed inputs, and InputDigest is
// their digest exactly when it did: no inputs is absence, never the digest of
// an empty set.
type SideProvenance struct {
	ScenarioDigest string
	InputsDeclared bool
	InputDigest    string
	SideDeclaration
}

// ScenarioRecorded reports whether the scenario and inputs were recorded.
func (p SideProvenance) ScenarioRecorded() bool { return p.ScenarioDigest != "" }

// ExecutionProvenance is what a client sends when it begins an execution:
// the digests of the scenario it is running, and each side's declarations.
//
// With a recorded reference, Reference must be empty: the reference side was
// run by another execution, and its provenance is that execution's.
type ExecutionProvenance struct {
	ScenarioDigest string
	InputsDeclared bool
	InputDigest    string
	Reference      SideDeclaration
	Candidate      SideDeclaration
}

// side is this request's provenance for one side it runs.
func (p ExecutionProvenance) side(d SideDeclaration) SideProvenance {
	return SideProvenance{
		ScenarioDigest: p.ScenarioDigest, InputsDeclared: p.InputsDeclared,
		InputDigest: p.InputDigest, SideDeclaration: d,
	}
}

func (p ExecutionProvenance) validate() error {
	for _, side := range []SideProvenance{p.side(p.Reference), p.side(p.Candidate)} {
		if err := side.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validate checks every format and the states' consistency.
func (p SideProvenance) validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: provenance: "+format, append([]any{ErrInvalidRepeatedRequest}, args...)...)
	}
	switch {
	case p.ScenarioDigest == "":
		if p.InputsDeclared || p.InputDigest != "" {
			return invalid("inputs are recorded only with a scenario digest")
		}
	case !provenanceDigestPattern.MatchString(p.ScenarioDigest):
		return invalid("scenario_digest must be sha256: followed by 64 lowercase hex digits")
	}
	if p.InputsDeclared != (p.InputDigest != "") {
		return invalid("input_digest is present exactly when inputs are declared")
	}
	if p.InputDigest != "" && !provenanceDigestPattern.MatchString(p.InputDigest) {
		return invalid("input_digest must be sha256: followed by 64 lowercase hex digits")
	}
	if p.Model != "" && !provenanceIdentifierPattern.MatchString(p.Model) {
		return invalid("model must be 1..128 of [A-Za-z0-9._:/@+-], starting alphanumeric")
	}
	if p.PromptRef.Stated() {
		if !provenanceIdentifierPattern.MatchString(p.PromptRef.Name) {
			return invalid("prompt_ref.name must be 1..128 of [A-Za-z0-9._:/@+-], starting alphanumeric")
		}
		if !provenanceDigestPattern.MatchString(p.PromptRef.Digest) {
			return invalid("prompt_ref.digest must be sha256: followed by 64 lowercase hex digits")
		}
	}
	return nil
}

// schemaVersionV11 is task 087's schema, the last version without task 086's
// scenario provenance columns. v12 adds columns only, so v11 and v12 hold the
// same tables and are told apart by the stamp.
const schemaVersionV11 = 11

// Schema 12's columns, five per side. Every one is nullable and NULL means not
// recorded, which is what an execution migrated from schema 11 holds: the step
// invents nothing about executions it did not see (ADR 0054).
var scenarioProvenanceColumnNames = []string{
	"scenario_digest", "input_digest", "model", "prompt_ref_name", "prompt_ref_digest",
}

// scenarioProvenanceColumns lists the ten columns, reference side first.
func scenarioProvenanceColumns() []string {
	out := make([]string, 0, 2*len(scenarioProvenanceColumnNames))
	for _, side := range []ComparisonSide{SideReference, SideCandidate} {
		for _, name := range scenarioProvenanceColumnNames {
			out = append(out, string(side)+"_"+name)
		}
	}
	return out
}

// scenarioProvenanceColumnList is the ten columns, comma-separated, in
// scenarioProvenanceColumns' order.
var scenarioProvenanceColumnList = strings.Join(scenarioProvenanceColumns(), ", ")

// scenarioProvenanceSchemaStatements is schema 12's whole change, in either
// dialect: ten nullable TEXT columns, no backfill. Not identifiers and never
// ordered, so plain TEXT on PostgreSQL too.
func scenarioProvenanceSchemaStatements() []string {
	out := make([]string, 0, 10)
	for _, column := range scenarioProvenanceColumns() {
		out = append(out, `ALTER TABLE `+tableScenarioExecutions+` ADD COLUMN `+column+` TEXT`)
	}
	return out
}

// provenanceColumnValues is one side's five column values, NULL for empty.
func provenanceColumnValues(p SideProvenance) []any {
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return []any{null(p.ScenarioDigest), null(p.InputDigest), null(p.Model),
		null(p.PromptRef.Name), null(p.PromptRef.Digest)}
}

// sideProvenanceFromColumns rebuilds one side from its five scanned columns.
// InputsDeclared is derived: with a scenario digest recorded, a NULL input
// digest means no inputs were declared. Validation is
// restoreScenarioExecution's, so an empty or malformed stored value is
// refused as corrupt rather than read as a digest.
func sideProvenanceFromColumns(c []sql.NullString) SideProvenance {
	return SideProvenance{
		ScenarioDigest: c[0].String, InputDigest: c[1].String, InputsDeclared: c[1].Valid,
		SideDeclaration: SideDeclaration{
			Model: c[2].String, PromptRef: PromptRef{Name: c[3].String, Digest: c[4].String},
		},
	}
}
