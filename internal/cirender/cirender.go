// Package cirender is the one renderer of Trustvian's result documents (task
// 079; task 107-B; ADR 0057): a validated `trustvian eval run --json` or
// report document in, Markdown out. cmd/trustvian-ci-render calls it for the
// pull request comment and the job summary.
//
// It works offline, needs no credential, executes nothing and imports only
// the standard library. Every number, classification, check and verdict it
// prints is transcribed from the document; it computes none of them. What it
// cannot render faithfully it renders as an explicit "no verdict".
package cirender

// The exported surface: what a caller that has read a document — and, for the
// comment, validated the artifact around it — needs to render it. Everything
// else stays unexported, as it was inside the command.

// Node is one strictly parsed JSON value.
type Node = node

// Parse parses exactly one JSON value, refusing duplicate keys, trailing data,
// invalid UTF-8 and documents beyond the depth and size bounds.
func Parse(raw []byte) (*Node, error) { return parseStrict(raw) }

// IsObject reports whether the value is a JSON object.
func (n *node) IsObject() bool { return n.kind == kindObject }

// Has reports whether an object carries a member, of any kind, null included.
func (n *node) Has(name string) bool {
	_, ok := n.fields[name]
	return ok
}

// The schema errors, and the field readers a caller validating its own
// document — the comment artifact's metadata — shares with this package.
var (
	ErrMalformed = errMalformed
	ErrSchema    = errSchema
)

func SchemaErr(path, problem string) error { return schemaErr(path, problem) }
func Join(path, name string) string        { return join(path, name) }
func Object(obj *Node, path, name string) (*Node, error) {
	return object(obj, path, name)
}
func Str(obj *Node, path, name string) (string, error) { return str(obj, path, name) }
func Enum(obj *Node, path, name string, allowed ...string) (string, error) {
	return enum(obj, path, name, allowed...)
}
func EqualText(obj *Node, path, name, want string) error { return equalText(obj, path, name, want) }
func Integer(obj *Node, path, name string, lo, hi int64) (int64, error) {
	return integer(obj, path, name, lo, hi)
}

// The stored verdicts.
const (
	VerdictPass = verdictPass
	VerdictFail = verdictFail
)

// Scenario is a validated single-scenario result document.
type Scenario struct{ r scenarioResult }

// ParseScenario validates a single-scenario result document.
func ParseScenario(doc *Node) (Scenario, error) {
	r, err := parseScenarioResult(doc, "")
	return Scenario{r}, err
}

// Verdict is the stored verdict, pass or fail.
func (s Scenario) Verdict() string { return s.r.verdict }

// Suite is a validated suite result document.
type Suite struct{ r suiteResult }

// ParseSuite validates a suite result document.
func ParseSuite(doc *Node) (Suite, error) {
	r, err := parseSuiteResult(doc)
	return Suite{r}, err
}

// ExitCode is the exit code the suite document records.
func (s Suite) ExitCode() int64 { return s.r.exitCode }

// Complete is false for the CLI's output-overflow form, which carries no
// member outcome.
func (s Suite) Complete() bool { return s.r.complete }

// RenderScenario renders a scenario's verdict within the limits: in full, then
// with only the evidence the verdict rests on, and otherwise as no verdict.
func RenderScenario(want Frame, lim Limits, s Scenario) Rendering {
	return fit(want, lim, StateVerdict, func(level int) string {
		return renderScenario(want, lim, s.r, level)
	})
}

// RenderSuite renders a suite's report within the limits, as RenderScenario.
func RenderSuite(want Frame, lim Limits, s Suite) Rendering {
	return fit(want, lim, StateReport, func(level int) string {
		return renderSuite(want, lim, s.r, level)
	})
}

// NoVerdict renders the no-verdict state from the frame alone.
func NoVerdict(want Frame, reason string) Rendering { return noVerdict(want, reason, nil) }

// Rejected renders the no-verdict state for a document or artifact that
// failed validation; the rendering's Rejected wraps ErrRejected and err.
func Rejected(want Frame, err error) Rendering { return rejected(want, err) }

// The parser's bounds and the truncation marker, for callers' tests.
const (
	MaxJSONDepth    = maxJSONDepth
	MaxJSONNodes    = maxJSONNodes
	TruncatedMarker = truncatedMarker
)

// RenderDocument renders a bare result document — a scenario's, a report, or
// a suite's — with no run around it: the rendering `trustvian eval report
// --format md` prints, and `trustvian-ci-render --document` prints for the
// same bytes. A comment for the same document is this rendering with the
// commit, the run link and the CLI exit code added after its heading.
//
// A document that fails validation renders the no-verdict state, with
// Rejected set, exactly as an artifact that fails validation does.
func RenderDocument(raw []byte, lim Limits) Rendering {
	var bare Frame
	doc, err := parseStrict(raw)
	if err != nil {
		return rejected(bare, err)
	}
	if doc.kind == kindObject && doc.Has("members") {
		suite, err := ParseSuite(doc)
		if err != nil {
			return rejected(bare, err)
		}
		if !suite.Complete() {
			return noVerdict(bare, "the suite result is incomplete: it was larger than the CLI's "+
				"output limit, so the CLI omitted every member outcome", nil)
		}
		return RenderSuite(bare, lim, suite)
	}
	scenario, err := ParseScenario(doc)
	if err != nil {
		return rejected(bare, err)
	}
	return RenderScenario(bare, lim, scenario)
}
