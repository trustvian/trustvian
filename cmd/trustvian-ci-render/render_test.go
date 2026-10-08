package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the current rendering")

// The job identity every artifact in testdata/artifacts was generated under
// (testdata/generate.sh).
const fixtureHead = "0123456789abcdef0123456789abcdef01234567"

func expected(code int) expectedContext {
	return expectedContext{
		headSHA: fixtureHead, repository: "trustvian/trustvian", runID: "1000000001",
		runAttempt: "1", serverURL: "https://github.com", exitCode: &code,
	}
}

const runLink = "[workflow run](https://github.com/trustvian/trustvian/actions/runs/1000000001/attempts/1)"

// artifact copies one real artifact into a fresh directory a test may change.
func artifact(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "artifacts", name)
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeJSON(t *testing.T, path string, doc any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setResult replaces result.json and records its new size and digest in the
// metadata, so a test reaches the result's own validation rather than the
// digest check.
func setResult(t *testing.T, dir string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, resultFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := readJSON(t, filepath.Join(dir, metadataFile))
	sum := sha256.Sum256(raw)
	result := meta["result"].(map[string]any)
	result["bytes"] = len(raw)
	result["sha256"] = hex.EncodeToString(sum[:])
	writeJSON(t, filepath.Join(dir, metadataFile), meta)
}

// mutateResult edits result.json as decoded JSON and keeps the metadata
// consistent with it.
func mutateResult(t *testing.T, dir string, edit func(doc map[string]any)) {
	t.Helper()
	doc := readJSON(t, filepath.Join(dir, resultFile))
	edit(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	setResult(t, dir, raw)
}

func mutateMetadata(t *testing.T, dir string, edit func(doc map[string]any)) {
	t.Helper()
	doc := readJSON(t, filepath.Join(dir, metadataFile))
	edit(doc)
	writeJSON(t, filepath.Join(dir, metadataFile), doc)
}

// path walks decoded JSON: a string key for an object, an int for an array.
func path(doc any, steps ...any) any {
	for _, s := range steps {
		switch k := s.(type) {
		case string:
			doc = doc.(map[string]any)[k]
		case int:
			doc = doc.([]any)[k]
		}
	}
	return doc
}

func obj(doc any, steps ...any) map[string]any { return path(doc, steps...).(map[string]any) }

// The real artifacts, rendered: each case's state, its renderer exit, and
// the exact Markdown, held in testdata/golden. Rendering twice gives the same
// bytes.
func TestRenderRealArtifacts(t *testing.T) {
	tests := []struct {
		name string
		code int
		want state
	}{
		{"pass", 0, stateVerdict},
		{"pass-started", 0, stateVerdict},
		{"pass-reused", 0, stateVerdict},
		{"fail", 1, stateVerdict},
		{"removed", 0, stateVerdict},
		{"single-run", 1, stateVerdict},
		{"operational", 3, stateNoVerdict},
		{"usage", 2, stateNoVerdict},
		{"suite", 1, stateReport},
		{"suite-fail-fast", 1, stateReport},
		{"suite-error", 3, stateReport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join("testdata", "artifacts", tt.name)
			got := render(dir, expected(tt.code), defaultLimits)
			if got.state != tt.want {
				t.Fatalf("state %s, want %s (reason %q)", got.state, tt.want, got.reason)
			}
			if got.rejected != nil {
				t.Fatalf("a real artifact was rejected: %v", got.rejected)
			}
			if again := render(dir, expected(tt.code), defaultLimits); again.markdown != got.markdown {
				t.Fatal("two renderings of one artifact differ")
			}
			assertInert(t, got.markdown)
			golden := filepath.Join("testdata", "golden", tt.name+".md")
			if *update {
				if err := os.WriteFile(golden, []byte(got.markdown), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if got.markdown != string(want) {
				t.Errorf("rendering differs from %s:\n%s", golden, got.markdown)
			}
		})
	}
}

// What a verdict transcribes, asserted on the real documents.
func TestRenderTranscribesTheVerdict(t *testing.T) {
	get := func(name string, code int) string {
		t.Helper()
		out := render(filepath.Join("testdata", "artifacts", name), expected(code), defaultLimits)
		if out.state != stateVerdict {
			t.Fatalf("%s: state %s (%s)", name, out.state, out.reason)
		}
		return out.markdown
	}
	fail := get("fail", 1)
	for _, want := range []string{
		"### Trustvian behavioral gate — FAIL",
		"**Commit** `" + fixtureHead + "` · " + runLink,
		"**Scenario** `render-fail` · **runs** 2 · **execution** `scn-render-fail-",
		"Added: present in at least 2 of 2 candidate runs and at most 0 of 2 reference runs.",
		"| `ai_agent` · `tool` · `export_customer` · `tools.localhost` · `local` | `533ccc6e34dafddc` | 0/2 | 2/2 | **+ added** |",
		"| `ai_agent` · `tool` · `crm_lookup` · `tools.localhost` · `local` | `3e2da711ec218a24` | 2/2 | 2/2 | unclassified |",
		"| repeatedly added behaviors | 1 | at most | 0 | **FAIL** |  |",
		"| reference repetitions completed | 2 | equals | 2 | pass |  |",
		"| worst candidate block decisions per run | 0 | at most | 0 | pass | fresh scope |",
		"| worst candidate critical-risk observations per run | 0 | at most | 0 | pass | fresh scope |",
		"Produced by `trustvian` `v0.9.1-0.20261002183633-3e86f9cead25 3e86f9cead2586e7ee90e14185a8f34074b54240` " +
			"and control plane `(devel) 3e86f9cead2586e7ee90e14185a8f34074b54240`.",
	} {
		if !strings.Contains(fail, want) {
			t.Errorf("FAIL rendering lacks %q:\n%s", want, fail)
		}
	}
	// Every check, passing ones included, in the stable order.
	last := -1
	for _, name := range checkOrder {
		at := strings.Index(fail, "| "+checkLabel[name]+" |")
		if at < 0 || at < last {
			t.Errorf("check %s is missing or out of order", name)
		}
		last = at
	}

	removed := get("removed", 0)
	for _, want := range []string{
		"### Trustvian behavioral gate — PASS",
		"| `ai_agent` · `tool` · `export_customer` · `tools.localhost` · `local` | `533ccc6e34dafddc` | 2/2 | 0/2 | **− removed** |",
	} {
		if !strings.Contains(removed, want) {
			t.Errorf("removed rendering lacks %q:\n%s", want, removed)
		}
	}

	reused := get("pass-reused", 0)
	if !regexp.MustCompile("\\*\\*Reference side\\*\\* reused from execution `scn-render-pass-[0-9T-]+-[0-9a-f]{8}` \\(`--reference last`\\)").
		MatchString(reused) {
		t.Errorf("reference provenance missing:\n%s", reused)
	}
	if strings.Contains(get("pass", 0), "Reference side") {
		t.Error("a run that reused nothing names a reused reference")
	}

	// At N = 1 the control plane marks no check advisory.
	single := get("single-run", 1)
	if strings.Contains(single, "fresh scope") {
		t.Errorf("an N = 1 result shows an advisory it does not carry:\n%s", single)
	}
	if !strings.Contains(single, "| `ai_agent` · `tool` · `export_customer` · `tools.localhost` · `local` | `533ccc6e34dafddc` | 0/1 | 1/1 | **+ added** |") {
		t.Errorf("N = 1 counts are not transcribed:\n%s", single)
	}
}

func TestRenderSuites(t *testing.T) {
	get := func(name string, code int) string {
		t.Helper()
		out := render(filepath.Join("testdata", "artifacts", name), expected(code), defaultLimits)
		if out.state != stateReport {
			t.Fatalf("%s: state %s (%s)", name, out.state, out.reason)
		}
		return out.markdown
	}
	suite := get("suite", 1)
	for _, want := range []string{
		"### Trustvian behavioral suite — CLI exit code 1",
		"A suite has no verdict of its own",
		"**Recorded summary:** passed 2 · failed 1 · errors 0 · skipped 0",
		"| `a-pass.yaml` | `suite-pass` | 2 | PASS | 0 | execution `scn-suite-pass-",
		"| `b-fail.yaml` | `suite-fail` | 2 | FAIL | 1 | execution `scn-suite-fail-",
		"#### `a-pass.yaml` — PASS",
		"#### `b-fail.yaml` — FAIL",
		"#### `c-removed.yaml` — PASS",
		"**− removed**",
		"**+ added**",
	} {
		if !strings.Contains(suite, want) {
			t.Errorf("suite rendering lacks %q:\n%s", want, suite)
		}
	}
	// Each member's own six checks, and no suite-level verdict line.
	if n := strings.Count(suite, "| repeatedly added behaviors |"); n != 3 {
		t.Errorf("%d member gate blocks, want 3", n)
	}
	if regexp.MustCompile(`suite — (PASS|FAIL)`).MatchString(suite) {
		t.Error("the suite was given a verdict of its own")
	}

	failFast := get("suite-fail-fast", 1)
	for _, want := range []string{
		"| `b-pass.yaml` | `ff-pass` | 2 | skipped | — | skipped after a failure (fail-fast) |",
		"**fail-fast** yes",
		"**Recorded summary:** passed 0 · failed 1 · errors 0 · skipped 1",
	} {
		if !strings.Contains(failFast, want) {
			t.Errorf("fail-fast rendering lacks %q:\n%s", want, failFast)
		}
	}
	if strings.Contains(failFast, "#### `b-pass.yaml`") {
		t.Error("a skipped member was given a result section")
	}

	errored := get("suite-error", 3)
	for _, want := range []string{
		"### Trustvian behavioral suite — CLI exit code 3",
		"| `b-operational.yaml` | `err-operational` | 2 | error | 3 | `repetition_failed`: `a repetition failed:",
		"#### `a-pass.yaml` — PASS",
	} {
		if !strings.Contains(errored, want) {
			t.Errorf("error-member rendering lacks %q:\n%s", want, errored)
		}
	}
	if strings.Contains(errored, "#### `b-operational.yaml`") {
		t.Error("an error member was given a result section")
	}
}

// The CLI's own overflow form: complete false, outcomes omitted, exit 3.
func TestRenderIncompleteSuiteIsNoVerdict(t *testing.T) {
	dir := artifact(t, "suite")
	mutateMetadata(t, dir, func(m map[string]any) { obj(m, "cli")["exit_code"] = 3 })
	mutateResult(t, dir, func(doc map[string]any) {
		doc["complete"] = false
		doc["exit_code"] = 3
		doc["summary"] = map[string]any{"passed": 0, "failed": 0, "errors": 0, "skipped": 0}
		for _, m := range doc["members"].([]any) {
			member := m.(map[string]any)
			for k := range member {
				if k != "file" && k != "scenario" {
					delete(member, k)
				}
			}
		}
		doc["error"] = map[string]any{"code": "output_too_large", "message": "over the limit"}
	})
	out := render(dir, expected(3), defaultLimits)
	if out.state != stateNoVerdict || out.rejected != nil {
		t.Fatalf("state %s, rejected %v", out.state, out.rejected)
	}
	assertNoEvidence(t, out.markdown)
	if !strings.Contains(out.reason, "incomplete") {
		t.Errorf("reason %q does not say the suite was incomplete", out.reason)
	}
}

// assertNoEvidence checks a no-verdict rendering carries nothing but the
// commit, the run link and the reason: no table, count, check, verdict or
// zero placeholder.
func assertNoEvidence(t *testing.T, md string) {
	t.Helper()
	if !strings.HasPrefix(md, "### Trustvian behavioral gate — no verdict\n\n**Commit** `"+fixtureHead+"` · "+runLink+"\n\n") {
		t.Errorf("no-verdict rendering does not lead with the commit and run link:\n%s", md)
	}
	at := strings.Index(md, "No verdict exists")
	if at < 0 {
		t.Fatalf("no-verdict rendering lacks its statement:\n%s", md)
	}
	body := md[at:]
	for _, banned := range []string{"|", "PASS", "FAIL", "Gate check", "Behavior", "summary", "/2", "/1",
		"Scenario", "scn-", "Produced by", "](", "<"} {
		if strings.Contains(body, banned) {
			t.Errorf("no-verdict rendering contains %q:\n%s", banned, md)
		}
	}
	// Outside the identity line and the reason — fixed text, which may name
	// an exit status, a field's index or a byte offset — there is no digit.
	body = regexp.MustCompile("(?m)^\\*\\*Reason:\\*\\* `.*`$").ReplaceAllString(body, "")
	if regexp.MustCompile(`[0-9]`).MatchString(body) {
		t.Errorf("no-verdict rendering carries a number:\n%s", md)
	}
}

// Each cause of no verdict, from the expected context and the artifact.
func TestRenderNoVerdict(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
		code     *int
		prepare  func(t *testing.T, dir string)
		reason   string
		rejected bool
	}{
		{name: "the CLI did not run", artifact: "pass", code: nil, reason: "did not run"},
		{name: "exit 2", artifact: "usage", code: ptr(2), reason: "usage error (exit 2)"},
		{name: "exit 3", artifact: "operational", code: ptr(3), reason: "operational error (exit 3)"},
		{name: "artifact directory missing", artifact: "", code: ptr(0), reason: "missing"},
		{name: "metadata missing", artifact: "pass", code: ptr(0), reason: "missing",
			prepare: func(t *testing.T, dir string) { remove(t, dir, metadataFile) }},
		{name: "an exit outside the contract", artifact: "operational", code: ptr(137), reason: "outside its documented codes",
			prepare: func(t *testing.T, dir string) {
				mutateMetadata(t, dir, func(m map[string]any) { obj(m, "cli")["exit_code"] = 137 })
			}},
		{name: "output was oversized", artifact: "operational", code: ptr(3), reason: "larger than",
			prepare: func(t *testing.T, dir string) {
				mutateMetadata(t, dir, func(m map[string]any) { obj(m, "result")["status"] = "oversized" })
			}},
		{name: "output was invalid", artifact: "operational", code: ptr(3), reason: "not one JSON object",
			prepare: func(t *testing.T, dir string) {
				mutateMetadata(t, dir, func(m map[string]any) { obj(m, "result")["status"] = "invalid" })
			}},
		{name: "a verdict exit with no document", artifact: "operational", code: ptr(0), rejected: true,
			reason: "records no result",
			prepare: func(t *testing.T, dir string) {
				mutateMetadata(t, dir, func(m map[string]any) { obj(m, "cli")["exit_code"] = 0 })
			}},
		{name: "a scenario document with an operational exit", artifact: "fail", code: ptr(3), rejected: true,
			reason: "disagrees with the CLI exit code",
			prepare: func(t *testing.T, dir string) {
				mutateMetadata(t, dir, func(m map[string]any) { obj(m, "cli")["exit_code"] = 3 })
			}},
		{name: "result recorded but absent", artifact: "pass", code: ptr(0), rejected: true, reason: "is absent",
			prepare: func(t *testing.T, dir string) { remove(t, dir, resultFile) }},
		{name: "result present but recorded absent", artifact: "operational", code: ptr(3), rejected: true,
			reason: "is present although",
			prepare: func(t *testing.T, dir string) {
				raw, _ := os.ReadFile(filepath.Join("testdata", "artifacts", "pass", resultFile))
				if err := os.WriteFile(filepath.Join(dir, resultFile), raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "absent")
			if tt.artifact != "" {
				dir = artifact(t, tt.artifact)
			}
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}
			want := expected(0)
			want.exitCode = tt.code
			out := render(dir, want, defaultLimits)
			if out.state != stateNoVerdict {
				t.Fatalf("state %s, want no verdict", out.state)
			}
			if (out.rejected != nil) != tt.rejected {
				t.Errorf("rejected = %v, want rejected %v", out.rejected, tt.rejected)
			}
			if out.rejected != nil && !errors.Is(out.rejected, errRejected) {
				t.Errorf("a rejection does not wrap errRejected: %v", out.rejected)
			}
			if !strings.Contains(out.reason, tt.reason) {
				t.Errorf("reason %q lacks %q", out.reason, tt.reason)
			}
			assertNoEvidence(t, out.markdown)
		})
	}
}

func ptr(v int) *int { return &v }

func remove(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

// The metadata must describe this run: every identity checked against the
// expected context.
func TestRenderRejectsMetadataMismatches(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(m map[string]any)
	}{
		{"head commit", "head.sha", func(m map[string]any) {
			obj(m, "head")["sha"] = "1111111111111111111111111111111111111111"
		}},
		{"head commit in upper case", "head.sha", func(m map[string]any) {
			obj(m, "head")["sha"] = strings.ToUpper(fixtureHead)
		}},
		{"head source", "head.source", func(m map[string]any) { obj(m, "head")["source"] = "github.event.after" }},
		{"repository", "repository", func(m map[string]any) { m["repository"] = "someone/else" }},
		{"run id", "run.id", func(m map[string]any) { obj(m, "run")["id"] = "1000000002" }},
		{"run attempt", "run.attempt", func(m map[string]any) { obj(m, "run")["attempt"] = "2" }},
		{"exit code", "cli.exit_code", func(m map[string]any) { obj(m, "cli")["exit_code"] = 1 }},
		{"version", "version", func(m map[string]any) { m["version"] = "2" }},
		{"mode", "mode", func(m map[string]any) { m["mode"] = "suites" }},
		{"mode against the document", "mode", func(m map[string]any) { m["mode"] = "none" }},
		{"suite mode against a scenario document", "complete", func(m map[string]any) { m["mode"] = "suite" }},
		{"control plane", "control_plane", func(m map[string]any) { m["control_plane"] = "remote" }},
		{"result status", "result.status", func(m map[string]any) { obj(m, "result")["status"] = "Present" }},
		{"result file", "result.file", func(m map[string]any) { obj(m, "result")["file"] = "../../etc/passwd" }},
		{"result digest shape", "result.sha256", func(m map[string]any) { obj(m, "result")["sha256"] = "abc" }},
		{"absent runtime", "runtime", func(m map[string]any) { delete(m, "runtime") }},
		{"null exit code", "cli.exit_code", func(m map[string]any) { obj(m, "cli")["exit_code"] = nil }},
		{"exit code as a string", "cli.exit_code", func(m map[string]any) { obj(m, "cli")["exit_code"] = "0" }},
		{"exit code as a fraction", "cli.exit_code", func(m map[string]any) { obj(m, "cli")["exit_code"] = json.Number("0.0") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, "pass")
			mutateMetadata(t, dir, tt.edit)
			out := render(dir, expected(0), defaultLimits)
			if out.state != stateNoVerdict || out.rejected == nil {
				t.Fatalf("state %s, rejected %v: a mismatched artifact was accepted", out.state, out.rejected)
			}
			if !strings.Contains(out.reason, tt.field) {
				t.Errorf("reason %q does not name %s", out.reason, tt.field)
			}
			assertNoEvidence(t, out.markdown)
		})
	}
}

// The digest proves the two files agree: a changed byte, or a changed size,
// is refused even though the result still parses.
func TestRenderRejectsDigestAndSizeMismatches(t *testing.T) {
	t.Run("a changed byte", func(t *testing.T) {
		dir := artifact(t, "pass")
		raw, _ := os.ReadFile(filepath.Join(dir, resultFile))
		raw = bytes.Replace(raw, []byte(`"render-pass"`), []byte(`"render-pasS"`), 2)
		if err := os.WriteFile(filepath.Join(dir, resultFile), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		out := render(dir, expected(0), defaultLimits)
		if out.rejected == nil || !strings.Contains(out.reason, "SHA-256") {
			t.Fatalf("a modified result was accepted: %s", out.reason)
		}
	})
	t.Run("a changed size", func(t *testing.T) {
		dir := artifact(t, "pass")
		raw, _ := os.ReadFile(filepath.Join(dir, resultFile))
		if err := os.WriteFile(filepath.Join(dir, resultFile), append(raw, ' '), 0o644); err != nil {
			t.Fatal(err)
		}
		out := render(dir, expected(0), defaultLimits)
		if out.rejected == nil || !strings.Contains(out.reason, "size") {
			t.Fatalf("a resized result was accepted: %s", out.reason)
		}
	})
	t.Run("a result over the bound", func(t *testing.T) {
		dir := artifact(t, "pass")
		f, err := os.Create(filepath.Join(dir, resultFile))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxResultBytes + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()
		out := render(dir, expected(0), defaultLimits)
		if out.rejected == nil || !strings.Contains(out.reason, "larger than") {
			t.Fatalf("an oversized result was read: %s", out.reason)
		}
	})
	t.Run("metadata over the bound", func(t *testing.T) {
		dir := artifact(t, "pass")
		mutateMetadata(t, dir, func(m map[string]any) { m["padding"] = strings.Repeat("x", maxMetadataBytes) })
		out := render(dir, expected(0), defaultLimits)
		if out.rejected == nil || !strings.Contains(out.reason, "larger than") {
			t.Fatalf("oversized metadata was read: %s", out.reason)
		}
	})
}

// Fixed names only, and never a link: a symbolic link is a path the artifact
// chose.
func TestRenderRefusesLinksAndNonFiles(t *testing.T) {
	for _, name := range []string{resultFile, metadataFile} {
		t.Run("a linked "+name, func(t *testing.T) {
			dir := artifact(t, "pass")
			target := filepath.Join(t.TempDir(), name)
			raw, _ := os.ReadFile(filepath.Join(dir, name))
			if err := os.WriteFile(target, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			remove(t, dir, name)
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			out := render(dir, expected(0), defaultLimits)
			if out.rejected == nil || !strings.Contains(out.reason, "not a regular file") {
				t.Fatalf("a symbolic link was followed: %s", out.reason)
			}
		})
	}
	t.Run("a directory named result.json", func(t *testing.T) {
		dir := artifact(t, "pass")
		remove(t, dir, resultFile)
		if err := os.Mkdir(filepath.Join(dir, resultFile), 0o755); err != nil {
			t.Fatal(err)
		}
		out := render(dir, expected(0), defaultLimits)
		if out.rejected == nil {
			t.Fatal("a directory was read as the result")
		}
	})
}

// Malformed JSON, in either file, is refused before any field is read.
func TestRenderRejectsMalformedJSON(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "artifacts", "pass", resultFile))
	if err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat(`{"a":`, maxJSONDepth+2) + "1" + strings.Repeat("}", maxJSONDepth+2)
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"truncated", valid[:len(valid)/2], "malformed"},
		{"a trailing document", append(append([]byte{}, valid...), []byte(` {}`)...), "data after the document"},
		{"two documents on two lines", append(append([]byte{}, valid...), []byte("\n"+string(valid))...), "data after the document"},
		{"a duplicate top-level key", bytes.Replace(valid, []byte(`{"version":"1",`), []byte(`{"version":"1","version":"1",`), 1), "duplicate"},
		{"a duplicate verdict", bytes.Replace(valid, []byte(`"verdict":"pass"`), []byte(`"verdict":"pass","verdict":"fail"`), 1), "duplicate"},
		{"a duplicate key in an unknown field", bytes.Replace(valid, []byte(`{"version":"1",`), []byte(`{"x":{"y":1,"y":2},"version":"1",`), 1), "duplicate"},
		{"invalid UTF-8", bytes.Replace(valid, []byte(`render-pass`), []byte("render-\xffass"), 1), "UTF-8"},
		{"nested too deep", []byte(deep), "nested deeper"},
		{"too many values", []byte("[" + strings.Repeat("1,", maxJSONNodes) + "1]"), "more than"},
		{"an array, not an object", []byte(`[]`), "not an object"},
		{"an empty file, refused by its recorded size", []byte(``), "result.bytes"},
		{"whitespace only", []byte("  \n "), "malformed"},
		{"a byte-order mark", append([]byte("\uFEFF"), valid...), "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, "pass")
			setResult(t, dir, tt.raw)
			out := render(dir, expected(0), defaultLimits)
			if out.rejected == nil {
				t.Fatalf("malformed JSON was accepted (state %s)", out.state)
			}
			if !strings.Contains(out.reason, tt.want) {
				t.Errorf("reason %q lacks %q", out.reason, tt.want)
			}
			assertNoEvidence(t, out.markdown)
		})
	}
	t.Run("a duplicate key in the metadata", func(t *testing.T) {
		dir := artifact(t, "pass")
		raw, _ := os.ReadFile(filepath.Join(dir, metadataFile))
		raw = bytes.Replace(raw, []byte(`"exit_code": 0`), []byte(`"exit_code": 1, "exit_code": 0`), 1)
		if err := os.WriteFile(filepath.Join(dir, metadataFile), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if out := render(dir, expected(0), defaultLimits); out.rejected == nil || !strings.Contains(out.reason, "duplicate") {
			t.Fatalf("a duplicated metadata key was accepted: %s", out.reason)
		}
	})
}

// A required field is required: absent and null are refused, while a valid
// zero, false or empty array is a value.
func TestRenderDistinguishesAbsentFromZeroAndFalse(t *testing.T) {
	refused := []struct {
		name, field string
		edit        func(doc map[string]any)
	}{
		{"absent passed", "comparison.gate.checks[0].passed", func(d map[string]any) {
			delete(obj(d, "comparison", "gate", "checks", 0), "passed")
		}},
		{"null passed", "comparison.gate.checks[0].passed", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 0)["passed"] = nil
		}},
		{"passed as a string", "comparison.gate.checks[0].passed", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 0)["passed"] = "true"
		}},
		{"absent actual", "comparison.gate.checks[2].actual", func(d map[string]any) {
			delete(obj(d, "comparison", "gate", "checks", 2), "actual")
		}},
		{"an actual as a number", "comparison.gate.checks[2].actual", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 2)["actual"] = 0
		}},
		{"a count with a leading zero", "comparison.behaviors[0].reference_runs_present", func(d map[string]any) {
			obj(d, "comparison", "behaviors", 0)["reference_runs_present"] = "02"
		}},
		{"a negative count", "comparison.behaviors[0].candidate_runs_present", func(d map[string]any) {
			obj(d, "comparison", "behaviors", 0)["candidate_runs_present"] = "-1"
		}},
		{"absent k", "comparison.gate_limits.added_candidate_presence_minimum", func(d map[string]any) {
			delete(obj(d, "comparison", "gate_limits"), "added_candidate_presence_minimum")
		}},
		{"absent behaviors", "comparison.behaviors", func(d map[string]any) {
			delete(obj(d, "comparison"), "behaviors")
		}},
		{"null behaviors", "comparison.behaviors", func(d map[string]any) {
			obj(d, "comparison")["behaviors"] = nil
		}},
		{"absent verdict", "comparison.gate.verdict", func(d map[string]any) {
			delete(obj(d, "comparison", "gate"), "verdict")
		}},
		{"absent runs", "scenario.runs", func(d map[string]any) { delete(obj(d, "scenario"), "runs") }},
		{"zero runs", "scenario.runs", func(d map[string]any) { obj(d, "scenario")["runs"] = 0 }},
		{"absent producers", "producers", func(d map[string]any) { delete(d, "producers") }},
		{"absent control-plane version", "producers.control_plane_version", func(d map[string]any) {
			delete(obj(d, "producers"), "control_plane_version")
		}},
		{"absent descriptor actor", "comparison.behaviors[1].behavior.actor_type", func(d map[string]any) {
			delete(obj(d, "comparison", "behaviors", 1, "behavior"), "actor_type")
		}},
		{"null optional descriptor field", "comparison.behaviors[1].behavior.target_name", func(d map[string]any) {
			obj(d, "comparison", "behaviors", 1, "behavior")["target_name"] = nil
		}},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, "pass")
			mutateResult(t, dir, tt.edit)
			out := render(dir, expected(0), defaultLimits)
			if out.rejected == nil {
				t.Fatalf("accepted (state %s)", out.state)
			}
			if !strings.Contains(out.reason, tt.field) {
				t.Errorf("reason %q does not name %s", out.reason, tt.field)
			}
		})
	}

	accepted := []struct {
		name, artifact string
		code           int
		edit           func(doc map[string]any)
		want           string
	}{
		{"a false passed on a check", "pass", 0, func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 2)["passed"] = false
		}, "| repetitions failing minimum evidence | 0 | at most | 0 | **FAIL** |"},
		{"no behaviors at all", "pass", 0, func(d map[string]any) {
			obj(d, "comparison")["behaviors"] = []any{}
		}, "*(the result lists no behaviors)*"},
		{"an absent optional descriptor field", "pass", 0, func(d map[string]any) {
			delete(obj(d, "comparison", "behaviors", 0, "behavior"), "target_name")
		}, "| `ai_agent` · `tool` · `crm_lookup` · `local` |"},
		{"a zero summary and false fail_fast", "suite-error", 3, nil, "passed 1 · failed 0 · errors 1 · skipped 0"},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, tt.artifact)
			if tt.edit != nil {
				mutateResult(t, dir, tt.edit)
			}
			out := render(dir, expected(tt.code), defaultLimits)
			if out.state == stateNoVerdict {
				t.Fatalf("a valid value was refused: %s", out.reason)
			}
			if !strings.Contains(out.markdown, tt.want) {
				t.Errorf("rendering lacks %q:\n%s", tt.want, out.markdown)
			}
		})
	}

	t.Run("an absent fail_fast", func(t *testing.T) {
		dir := artifact(t, "suite-error")
		mutateResult(t, dir, func(d map[string]any) { delete(obj(d, "options"), "fail_fast") })
		if out := render(dir, expected(3), defaultLimits); out.rejected == nil ||
			!strings.Contains(out.reason, "options.fail_fast") {
			t.Fatalf("an absent false was accepted: %s", out.reason)
		}
	})
	t.Run("an absent zero exit code", func(t *testing.T) {
		dir := artifact(t, "suite-error")
		mutateResult(t, dir, func(d map[string]any) { delete(obj(d, "members", 0), "exit_code") })
		if out := render(dir, expected(3), defaultLimits); out.rejected == nil ||
			!strings.Contains(out.reason, "members[0].exit_code") {
			t.Fatalf("an absent zero was accepted: %s", out.reason)
		}
	})
}

// Closed vocabularies and shapes: an unknown value, a different version or an
// outcome whose fields do not belong to it is refused.
func TestRenderRejectsOutOfContractValues(t *testing.T) {
	scenario := []struct {
		name, field string
		edit        func(doc map[string]any)
	}{
		{"document version 2", "version", func(d map[string]any) { d["version"] = "2" }},
		{"comparison version 2", "comparison.version", func(d map[string]any) { obj(d, "comparison")["version"] = "2" }},
		{"an unknown verdict", "comparison.gate.verdict", func(d map[string]any) { obj(d, "comparison", "gate")["verdict"] = "PASS" }},
		{"an unknown classification", "comparison.behaviors[0].classification", func(d map[string]any) {
			obj(d, "comparison", "behaviors", 0)["classification"] = "suspicious"
		}},
		{"an unknown rule", "comparison.gate.checks[0].rule", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 0)["rule"] = "at_least"
		}},
		{"an empty advisory", "comparison.gate.checks[4].advisory", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 4)["advisory"] = ""
		}},
		{"an unknown advisory", "comparison.gate.checks[4].advisory", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 4)["advisory"] = "ignore_me"
		}},
		{"checks out of order", "comparison.gate.checks[0].name", func(d map[string]any) {
			checks := path(d, "comparison", "gate", "checks").([]any)
			checks[0], checks[1] = checks[1], checks[0]
		}},
		{"a check missing", "comparison.gate.checks", func(d map[string]any) {
			g := obj(d, "comparison", "gate")
			g["checks"] = g["checks"].([]any)[:5]
		}},
		{"a seventh check", "comparison.gate.checks", func(d map[string]any) {
			g := obj(d, "comparison", "gate")
			g["checks"] = append(g["checks"].([]any), g["checks"].([]any)[0])
		}},
		{"an unknown check", "comparison.gate.checks[5].name", func(d map[string]any) {
			obj(d, "comparison", "gate", "checks", 5)["name"] = "always_passes"
		}},
		{"an unknown reference mode", "reference.mode", func(d map[string]any) {
			d["reference"] = map[string]any{"mode": "previous", "execution_id": "x"}
		}},
		{"a reference without its execution", "reference.execution_id", func(d map[string]any) {
			d["reference"] = map[string]any{"mode": "last"}
		}},
		{"runs that disagree", "comparison.runs", func(d map[string]any) { obj(d, "comparison")["runs"] = 3 }},
		{"producers that disagree", "comparison.producer.control_plane_version", func(d map[string]any) {
			obj(d, "comparison", "producer")["control_plane_version"] = "other"
		}},
		{"runs over the scenario bound", "scenario.runs", func(d map[string]any) { obj(d, "scenario")["runs"] = 65 }},
		{"a verdict that disagrees with exit 0", "comparison.gate.verdict", func(d map[string]any) {
			obj(d, "comparison", "gate")["verdict"] = "fail"
		}},
	}
	for _, tt := range scenario {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, "pass")
			mutateResult(t, dir, tt.edit)
			out := render(dir, expected(0), defaultLimits)
			if out.rejected == nil {
				t.Fatalf("accepted (state %s)", out.state)
			}
			if !strings.Contains(out.reason, tt.field) {
				t.Errorf("reason %q does not name %s", out.reason, tt.field)
			}
			assertNoEvidence(t, out.markdown)
		})
	}

	suite := []struct {
		name, field string
		edit        func(doc map[string]any)
	}{
		{"an unknown outcome", "members[0].outcome", func(d map[string]any) { obj(d, "members", 0)["outcome"] = "flaky" }},
		{"a pass with an error", "members[0].error", func(d map[string]any) {
			obj(d, "members", 0)["error"] = map[string]any{"code": "x", "message": "y"}
		}},
		{"a pass with exit 1", "members[0].exit_code", func(d map[string]any) { obj(d, "members", 0)["exit_code"] = 1 }},
		{"a pass without its result", "members[0].result", func(d map[string]any) { delete(obj(d, "members", 0), "result") }},
		{"a pass whose result fails", "members[0].result.comparison.gate.verdict", func(d map[string]any) {
			obj(d, "members", 0, "result", "comparison", "gate")["verdict"] = "fail"
		}},
		{"a result for another scenario", "members[0].result.scenario", func(d map[string]any) {
			obj(d, "members", 0, "result", "scenario")["name"] = "another"
		}},
		{"a result for another execution", "members[0].result.execution_id", func(d map[string]any) {
			obj(d, "members", 0, "result")["execution_id"] = "scn-other"
		}},
		{"an error with a result", "members[1].result", func(d map[string]any) {
			obj(d, "members", 1)["result"] = obj(d, "members", 0)["result"]
		}},
		{"an error without its error", "members[1].error", func(d map[string]any) { delete(obj(d, "members", 1), "error") }},
		{"an error with exit 0", "members[1].exit_code", func(d map[string]any) { obj(d, "members", 1)["exit_code"] = 0 }},
		{"a skipped member with an exit code", "members[1].exit_code", func(d map[string]any) {
			m := obj(d, "members", 1)
			for _, k := range []string{"error", "execution_id"} {
				delete(m, k)
			}
			m["outcome"], m["skipped_reason"] = "skipped", "cancelled"
		}},
		{"an unknown skipped reason", "members[1].skipped_reason", func(d map[string]any) {
			m := obj(d, "members", 1)
			for _, k := range []string{"error", "execution_id", "exit_code"} {
				delete(m, k)
			}
			m["outcome"], m["skipped_reason"] = "skipped", "bored"
		}},
		{"a missing member", "members", func(d map[string]any) { d["members"] = d["members"].([]any)[:1] }},
		{"an exit code that disagrees", "exit_code", func(d map[string]any) { d["exit_code"] = 1 }},
		{"a complete document with an error", "error", func(d map[string]any) {
			d["error"] = map[string]any{"code": "output_too_large", "message": "x"}
		}},
		{"an incomplete document without its error", "error", func(d map[string]any) { d["complete"] = false }},
		{"an unknown suite reference", "options.reference", func(d map[string]any) { obj(d, "options")["reference"] = "first" }},
		{"suite version 2", "version", func(d map[string]any) { d["version"] = "2" }},
	}
	for _, tt := range suite {
		t.Run("suite/"+tt.name, func(t *testing.T) {
			dir := artifact(t, "suite-error")
			mutateResult(t, dir, tt.edit)
			out := render(dir, expected(3), defaultLimits)
			if out.rejected == nil {
				t.Fatalf("accepted (state %s)", out.state)
			}
			if !strings.Contains(out.reason, tt.field) {
				t.Errorf("reason %q does not name %s", out.reason, tt.field)
			}
			assertNoEvidence(t, out.markdown)
		})
	}
}

// Unknown fields are tolerated, as the compatibility contract requires, and
// none is ever rendered.
func TestRenderToleratesButNeverRendersUnknownFields(t *testing.T) {
	const canary = "UNKNOWN-FIELD-CANARY"
	for _, name := range []string{"fail", "suite"} {
		t.Run(name, func(t *testing.T) {
			dir := artifact(t, name)
			code := 1
			plant := func(m map[string]any) {
				m["x_"+canary] = canary
				m["note"] = map[string]any{"text": canary, "verdict": "pass"}
			}
			mutateMetadata(t, dir, func(m map[string]any) { plant(m); plant(obj(m, "result")) })
			mutateResult(t, dir, func(d map[string]any) {
				plant(d)
				var scenarios []map[string]any
				if name == "fail" {
					scenarios = []map[string]any{d}
				} else {
					plant(obj(d, "summary"))
					for i := range d["members"].([]any) {
						plant(obj(d, "members", i))
						scenarios = append(scenarios, obj(d, "members", i, "result"))
					}
				}
				for _, s := range scenarios {
					plant(obj(s, "comparison"))
					plant(obj(s, "comparison", "gate"))
					plant(obj(s, "comparison", "gate", "checks", 3))
					plant(obj(s, "comparison", "behaviors", 0))
					plant(obj(s, "comparison", "behaviors", 0, "behavior"))
				}
			})
			out := render(dir, expected(code), defaultLimits)
			if out.state == stateNoVerdict {
				t.Fatalf("unknown fields were refused: %s", out.reason)
			}
			if strings.Contains(out.markdown, canary) {
				t.Errorf("an unknown field was rendered:\n%s", out.markdown)
			}
			if out.markdown != render(filepath.Join("testdata", "artifacts", name), expected(code), defaultLimits).markdown {
				t.Error("unknown fields changed the rendering")
			}
		})
	}
}

// task087Operational is a compare-repeated "operational" object exactly as the
// control plane publishes it since task 087, cost section included.
const task087Operational = `{
  "latency": {"comparable": true,
    "reference": {"runs_with_evidence": "3", "buckets": [{"bound": "le_1ms", "count": "0"},
      {"bound": "gt_10000ms", "count": "1"}], "observed": "4", "unobserved": "0",
      "sum_nanos": "16421647000", "min_nanos": "1200000", "max_nanos": "12000000000"},
    "candidate": {"runs_with_evidence": "3", "buckets": [], "observed": "4", "unobserved": "0",
      "sum_nanos": "15971278000", "min_nanos": "1100000", "max_nanos": "11000000000"},
    "delta": {"buckets": [], "observed": "0", "unobserved": "0", "sum_nanos": "-450369000",
      "min_nanos": "-100000", "max_nanos": "-1000000000"}},
  "errors": {"comparable": false, "reason": "candidate_unavailable",
    "reference": {"runs_with_evidence": "1", "span_status": {"unavailable": "0", "unset": "0", "ok": "4", "error": "0"},
      "http_status": {"1xx": "0", "2xx": "4", "3xx": "0", "4xx": "0", "5xx": "0", "unavailable": "0"}, "http_429": "0"},
    "candidate": {"runs_with_evidence": "0", "span_status": {"unavailable": "4", "unset": "0", "ok": "0", "error": "0"},
      "http_status": {"1xx": "0", "2xx": "0", "3xx": "0", "4xx": "0", "5xx": "0", "unavailable": "4"}, "http_429": "0"}},
  "tokens": {"comparable": true,
    "reference": {"runs_with_evidence": "3", "input": "1380", "output": "239", "unsplit": "0", "observed": "4", "unobserved": "11"},
    "candidate": {"runs_with_evidence": "3", "input": "1356", "output": "217", "unsplit": "0", "observed": "4", "unobserved": "11"},
    "delta": {"input": "-24", "output": "-22", "unsplit": "0", "observed": "0", "unobserved": "0"}},
  "cost": {"pricing_version": "team-2026-10", "pricing_digest": "sha256:4006213e9a5580ea06ab750a1ebb1bb0a96787be63bac430b529aa561cddbafb",
    "source": "fixture", "currency": "USD", "comparable": true,
    "reference": {"runs_with_evidence": "3", "cost_micros": "233", "priced_tokens": "1619", "unpriced_tokens": "0"},
    "candidate": {"runs_with_evidence": "3", "cost_micros": "222", "priced_tokens": "1573", "unpriced_tokens": "0"},
    "delta": {"cost_micros": "-11"}}
}`

// TestRenderAcceptsTask087Sections: the 079 renderer keeps rendering a result
// document whose comparison carries task 087's operational sections, byte for
// byte as it rendered the document without them. They are additive fields it
// neither refuses nor renders.
func TestRenderAcceptsTask087Sections(t *testing.T) {
	var operational map[string]any
	if err := json.Unmarshal([]byte(task087Operational), &operational); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		code int
	}{{"fail", 1}, {"pass", 0}, {"suite", 1}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, tt.name)
			mutateResult(t, dir, func(d map[string]any) {
				if members, ok := d["members"].([]any); ok {
					for i := range members {
						obj(d, "members", i, "result", "comparison")["operational"] = operational
					}
					return
				}
				obj(d, "comparison")["operational"] = operational
			})
			out := render(dir, expected(tt.code), defaultLimits)
			if out.state == stateNoVerdict {
				t.Fatalf("a document with task 087's sections was refused: %s", out.reason)
			}
			if out.markdown != render(filepath.Join("testdata", "artifacts", tt.name), expected(tt.code), defaultLimits).markdown {
				t.Error("task 087's sections changed the rendering")
			}
		})
	}
}

// TestRenderAcceptsTask106Fields: the 079 renderer keeps rendering a result
// document carrying task 106's frequency evidence — per-behavior frequency and
// lost, per-target frequency, the frequency checks, the extra gate limits and
// the comparison suggestions — byte for byte as it rendered it without them.
// None changes the six checks, the classification vocabulary or the verdict.
func TestRenderAcceptsTask106Fields(t *testing.T) {
	frequency := map[string]any{"runs": "3", "calls_total": "9", "calls_per_run_min": "3",
		"calls_per_run_max": "3", "calls_per_run_mean_milli": "3000"}
	for _, tt := range []struct {
		name string
		code int
	}{{"fail", 1}, {"pass", 0}, {"suite", 1}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, tt.name)
			plant := func(c map[string]any) {
				limits := obj(c, "gate_limits")
				limits["min_candidate_frequency"] = "1"
				limits["max_lost_behaviors"] = "0"
				limits["max_calls_per_run"] = []any{map[string]any{"target": "crm.internal", "max": "6"}}
				for i := range c["behaviors"].([]any) {
					b := obj(c, "behaviors", i)
					b["reference_frequency"], b["candidate_frequency"], b["lost"] = frequency, frequency, false
				}
				c["targets"] = []any{map[string]any{"target_name": "crm.internal", "target_category": "internal",
					"reference_frequency": frequency, "candidate_frequency": frequency,
					"call_ratio_available": true, "call_ratio_permille": "1000"}}
				c["lost_transitions"] = "not_recorded"
				obj(c, "gate")["frequency_checks"] = []any{
					map[string]any{"name": "min_candidate_frequency", "state": "evaluated", "rule": "at_least",
						"actual": "3", "bound": "1", "passed": true},
					map[string]any{"name": "max_lost_behaviors", "state": "not_evaluated"},
					map[string]any{"name": "max_calls_per_run", "state": "deferred",
						"missing_evidence": "no completed candidate repetition"},
				}
				c["suggestions"] = []any{map[string]any{"rule": "compare.lost_in_half", "rule_version": 1,
					"evidence": []any{map[string]any{"name": "runs", "value": "3"}}, "text": "SUGGESTION-CANARY"}}
				c["suggestions_truncated"] = false
			}
			mutateResult(t, dir, func(d map[string]any) {
				if members, ok := d["members"].([]any); ok {
					for i := range members {
						plant(obj(d, "members", i, "result", "comparison"))
					}
					return
				}
				plant(obj(d, "comparison"))
			})
			out := render(dir, expected(tt.code), defaultLimits)
			if out.state == stateNoVerdict {
				t.Fatalf("a document with task 106's fields was refused: %s", out.reason)
			}
			if strings.Contains(out.markdown, "SUGGESTION-CANARY") {
				t.Error("a suggestion was rendered; the renderer renders none")
			}
			if out.markdown != render(filepath.Join("testdata", "artifacts", tt.name), expected(tt.code), defaultLimits).markdown {
				t.Error("task 106's fields changed the rendering")
			}
		})
	}
}

// TestRenderAcceptsTask086Fields: the 079 renderer keeps rendering a result
// document carrying task 086's sameness block and warnings — a difference in
// every answer included — byte for byte as it rendered it without them.
// Neither is a check, so neither reaches the verdict.
func TestRenderAcceptsTask086Fields(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	for _, tt := range []struct {
		name string
		code int
	}{{"fail", 1}, {"pass", 0}, {"suite", 1}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := artifact(t, tt.name)
			plant := func(c map[string]any) {
				c["sameness"] = map[string]any{
					"same_scenario": "false", "same_inputs": "not_recorded", "same_model": "false",
					"same_prompt_ref": "true",
					"reference": map[string]any{"scenario_digest": digest, "inputs": "declared",
						"input_digest": digest, "model": "llama3.2",
						"prompt_ref": map[string]any{"name": "sys@v14", "digest": digest}},
					"candidate": map[string]any{"scenario_digest": other, "inputs": "not_recorded",
						"model": "gemma3:4b", "prompt_ref": map[string]any{"name": "sys@v14", "digest": digest}},
				}
				c["warnings"] = []any{
					map[string]any{"code": "scenario_differs", "text": "WARNING-CANARY"},
					map[string]any{"code": "model_differs", "text": "WARNING-CANARY"},
				}
			}
			mutateResult(t, dir, func(d map[string]any) {
				if members, ok := d["members"].([]any); ok {
					for i := range members {
						plant(obj(d, "members", i, "result", "comparison"))
					}
					return
				}
				plant(obj(d, "comparison"))
			})
			out := render(dir, expected(tt.code), defaultLimits)
			if out.state == stateNoVerdict {
				t.Fatalf("a document with task 086's fields was refused: %s", out.reason)
			}
			if out.markdown != render(filepath.Join("testdata", "artifacts", tt.name), expected(tt.code), defaultLimits).markdown {
				t.Error("task 086's fields changed the rendering")
			}
		})
	}
}

// The renderer transcribes; it never decides. A document whose stored
// classification, check outcome, verdict and suite summary contradict its own
// counts is rendered exactly as stored — the control plane owns all of them.
func TestRenderNeverRecomputes(t *testing.T) {
	dir := artifact(t, "fail")
	mutateMetadata(t, dir, func(m map[string]any) { obj(m, "cli")["exit_code"] = 0 })
	mutateResult(t, dir, func(d map[string]any) {
		obj(d, "comparison", "behaviors", 1)["classification"] = "neither" // 0/2 → 2/2
		obj(d, "comparison", "behaviors", 0)["classification"] = "removed" // 2/2 → 2/2
		check := obj(d, "comparison", "gate", "checks", 3)
		check["passed"] = true // actual 1, at most 0
		obj(d, "comparison", "gate")["verdict"] = "pass"
	})
	out := render(dir, expected(0), defaultLimits)
	if out.state != stateVerdict {
		t.Fatalf("state %s: %s", out.state, out.reason)
	}
	for _, want := range []string{
		"### Trustvian behavioral gate — PASS",
		"`export_customer` · `tools.localhost` · `local` | `533ccc6e34dafddc` | 0/2 | 2/2 | unclassified |",
		"`crm_lookup` · `tools.localhost` · `local` | `3e2da711ec218a24` | 2/2 | 2/2 | **− removed** |",
		"| repeatedly added behaviors | 1 | at most | 0 | pass |",
	} {
		if !strings.Contains(out.markdown, want) {
			t.Errorf("rendering lacks the stored value %q:\n%s", want, out.markdown)
		}
	}

	suite := artifact(t, "suite")
	mutateResult(t, suite, func(d map[string]any) {
		d["summary"] = map[string]any{"passed": 7, "failed": 0, "errors": 5, "skipped": 9}
	})
	report := render(suite, expected(1), defaultLimits)
	if !strings.Contains(report.markdown, "**Recorded summary:** passed 7 · failed 0 · errors 5 · skipped 9") {
		t.Errorf("the recorded summary was not transcribed as recorded:\n%s", report.markdown)
	}
}

// hostile is every string shape a pull request's workload can put into a
// behavior descriptor or a scenario name.
var hostile = []string{
	"[click here](http://evil.example)",
	"<img src=x onerror=alert(1)>",
	"<script>alert(1)</script>",
	"@trustvian/security-team",
	"Fixes #1234 and trustvian/trustvian#5",
	"| 0/2 | 0/2 | pass |",
	`\| 0/2 \\| pass \|`,
	"x\n| repeatedly added behaviors | 0 | at most | 0 | pass |  |",
	"line\r\nbreak\u2028sep\u2029para\u0085nel",
	"`` ` code ``` `",
	"ends with a backtick`",
	"\u202Egnp.exe\u202C bidi \u200B zero width",
	"![image](http://evil.example/x.png)",
	"<!-- hidden -->",
	"https://evil.example/autolink www.evil.example",
	"*emphasis* _under_ ~~strike~~ \\ backslash",
	"\x1b[31mterminal escape\x1b[0m \x00 nul",
	"## a heading",
	"> a quote",
}

func TestRenderMakesHostileStringsInert(t *testing.T) {
	dir := artifact(t, "fail")
	long := strings.Repeat("é", 6000) // 12,000 bytes, two-byte runes
	mutateResult(t, dir, func(d map[string]any) {
		behaviors := path(d, "comparison", "behaviors").([]any)
		template := behaviors[1].(map[string]any)
		var all []any
		for i, h := range append(append([]string{}, hostile...), long) {
			b := map[string]any{}
			for k, v := range template {
				b[k] = v
			}
			b["fingerprint_id"] = h
			b["behavior"] = map[string]any{"actor_type": h, "operation_category": "tool", "operation_name": h,
				"target_name": h, "environment": h}
			if i%2 == 0 {
				b["classification"] = "neither"
			}
			all = append(all, b)
		}
		obj(d, "comparison")["behaviors"] = all
		obj(d, "scenario")["name"] = hostile[0]
		d["execution_id"] = hostile[6]
		obj(d, "producers")["cli_version"] = hostile[3]
		obj(d, "producers")["control_plane_version"] = hostile[1]
		obj(d, "comparison", "producer")["control_plane_version"] = hostile[1]
	})
	out := render(dir, expected(1), defaultLimits)
	if out.state != stateVerdict {
		t.Fatalf("state %s: %s", out.state, out.reason)
	}
	assertInert(t, out.markdown)

	// One table row per behavior, and the six gate rows, exactly: no value
	// added a row or a line.
	rows := 0
	for _, line := range strings.Split(out.markdown, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| Behavior") && !strings.HasPrefix(line, "| Gate check") {
			rows++
		}
	}
	if want := len(hostile) + 1 + 6; rows != want {
		t.Errorf("%d table rows, want %d:\n%s", rows, want, out.markdown)
	}
	// The forged gate row did not land as a row of its own.
	if strings.Contains(out.markdown, "\n| repeatedly added behaviors | 0 |") {
		t.Errorf("a forged gate row was rendered:\n%s", out.markdown)
	}
	if !strings.Contains(out.markdown, "| repeatedly added behaviors | 1 | at most | 0 | **FAIL** |") {
		t.Error("the real failing check is missing")
	}
	// The 12,000-byte value is cut, at a rune boundary, within the limit
	// with its ellipsis, and marked.
	kept := (defaultLimits.stringBytes - len("…")) / 2
	if !strings.Contains(out.markdown, "`"+strings.Repeat("é", kept)+"…`"+truncatedMarker) {
		t.Errorf("the long value was not truncated visibly at a rune boundary")
	}
	if strings.Contains(out.markdown, strings.Repeat("é", kept+1)) {
		t.Error("the long value exceeds the per-string limit")
	}
}

// assertInert checks a rendering's structure: every code span closes on its
// own line, every table row has its table's column count, and outside code
// spans there is no HTML, mention, issue reference, image, autolink or link
// but the run link.
func assertInert(t *testing.T, md string) {
	t.Helper()
	columns := 0
	for _, line := range strings.Split(md, "\n") {
		outside, ok := stripCodeSpans(line)
		if !ok {
			t.Errorf("an unclosed code span: %q", line)
			continue
		}
		outside = strings.Replace(outside, runLink, "", 1)
		for _, banned := range []string{"<", ">", "@", "](", "![", "http", "www.", "~~", "\\"} {
			if strings.Contains(outside, banned) && !(banned == ">" && strings.HasPrefix(line, "> **Shortened:**")) {
				t.Errorf("%q outside a code span: %q", banned, line)
			}
		}
		if regexp.MustCompile(`#[0-9]`).MatchString(outside) {
			t.Errorf("an issue reference outside a code span: %q", line)
		}
		if strings.HasPrefix(line, "#") && !regexp.MustCompile(`^#{3,4} `).MatchString(line) {
			t.Errorf("a heading this renderer does not write: %q", line)
		}
		if strings.HasPrefix(line, "|") {
			cells := len(regexp.MustCompile(`(^|[^\\])\|`).FindAllString(line, -1)) - 1
			if strings.HasPrefix(line, "|---") {
				columns = cells
			} else if columns != 0 && cells != columns {
				t.Errorf("a row with %d cells in a %d-column table: %q", cells, columns, line)
			}
		} else {
			columns = 0
		}
	}
}

// stripCodeSpans removes every code span from a line, as GitHub would find
// them, and reports whether every span closed.
func stripCodeSpans(line string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			out.WriteByte(line[i])
			i++
			continue
		}
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		fence := strings.Repeat("`", n)
		rest := line[i+n:]
		end := -1
		for j := 0; j+n <= len(rest); j++ {
			if rest[j:j+n] == fence && (j+n == len(rest) || rest[j+n] != '`') && (j == 0 || rest[j-1] != '`') {
				end = j
				break
			}
		}
		if end < 0 {
			return "", false
		}
		out.WriteString("CODE")
		i += n + end + n
	}
	return out.String(), true
}

func TestInert(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "`plain`"},
		{"", "*(empty)*"},
		{"a|b", "`a\uFF5Cb`"},
		{`a\|b\`, "`a\\\uFF5Cb\\`"},
		{"a`b", "``a`b``"},
		{"`edge`", "`` `edge` ``"},
		{"a\nb", "`a\uFFFDb`"},
		{"\u202Eevil", "`\uFFFDevil`"},
		{"bad\xffbyte", "`bad\uFFFDbyte`"},
		{"   ", "*(blank)*"},
		{"\u3164", "`\uFFFD`"},
		{"a\u2800b", "`a\uFFFDb`"},
	}
	for _, tt := range tests {
		if got := inert(tt.in, 64); got != tt.want {
			t.Errorf("inert(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := inert("abcdefgh", 6); got != "`abc…`"+truncatedMarker {
		t.Errorf("truncation: %q", got)
	}
	if got := inert("ééé", 4); got != "`…`"+truncatedMarker { // é would make 5 bytes
		t.Errorf("truncation splits a rune or exceeds the limit: %q", got)
	}
	if got := inert("éééé", 7); got != "`éé…`"+truncatedMarker {
		t.Errorf("truncation: %q", got)
	}
}

// The total limit: unclassified behaviors give way first, visibly; when the
// evidence a verdict rests on cannot fit, there is no verdict at all.
func TestRenderOutputLimits(t *testing.T) {
	dir := artifact(t, "fail")
	mutateResult(t, dir, func(d map[string]any) {
		behaviors := path(d, "comparison", "behaviors").([]any)
		for i := range 200 {
			b := map[string]any{}
			for k, v := range behaviors[0].(map[string]any) {
				b[k] = v
			}
			b["fingerprint_id"] = strings.Repeat("f", 200) + string(rune('a'+i%26))
			behaviors = append(behaviors, b)
		}
		obj(d, "comparison")["behaviors"] = behaviors
	})
	full := render(dir, expected(1), limits{stringBytes: 256, totalBytes: 1 << 20})
	if full.state != stateVerdict || strings.Contains(full.markdown, "Shortened") {
		t.Fatalf("an unbounded rendering was shortened: %s", full.reason)
	}

	short := render(dir, expected(1), defaultLimits)
	if short.state != stateVerdict {
		t.Fatalf("state %s: %s", short.state, short.reason)
	}
	if len(short.markdown) > defaultLimits.totalBytes {
		t.Errorf("%d bytes, over the %d limit", len(short.markdown), defaultLimits.totalBytes)
	}
	for _, want := range []string{"> **Shortened:**", "**+ added**", "| repeatedly added behaviors | 1 |",
		"| worst candidate critical-risk observations per run |", "Produced by"} {
		if !strings.Contains(short.markdown, want) {
			t.Errorf("shortened rendering lacks %q", want)
		}
	}
	if strings.Contains(short.markdown, "unclassified |") {
		t.Error("a shortened rendering kept unclassified rows")
	}

	tiny := render(dir, expected(1), limits{stringBytes: 256, totalBytes: 1500})
	if tiny.state != stateNoVerdict || tiny.rejected != nil || !strings.Contains(tiny.reason, "does not fit") {
		t.Fatalf("evidence that cannot fit was rendered partially: %s %s", tiny.state, tiny.reason)
	}
	assertNoEvidence(t, tiny.markdown)

	suite := render(filepath.Join("testdata", "artifacts", "suite"), expected(1), limits{stringBytes: 256, totalBytes: 2000})
	if suite.state != stateNoVerdict || !strings.Contains(suite.reason, "does not fit") {
		t.Fatalf("a suite that cannot fit was rendered partially: %s", suite.state)
	}
}

// A no-verdict rendering keeps nothing from an earlier one: each rendering is
// built from its own inputs alone.
func TestRenderNoVerdictRetainsNothing(t *testing.T) {
	first := render(filepath.Join("testdata", "artifacts", "fail"), expected(1), defaultLimits)
	if first.state != stateVerdict {
		t.Fatal(first.reason)
	}
	dir := artifact(t, "fail")
	remove(t, dir, resultFile)
	second := render(dir, expected(1), defaultLimits)
	if second.state != stateNoVerdict {
		t.Fatalf("state %s", second.state)
	}
	for _, s := range []string{"render-fail", "export_customer", "FAIL", "scn-"} {
		if strings.Contains(second.markdown, s) {
			t.Errorf("the no-verdict rendering carries %q from an earlier one", s)
		}
	}
	assertNoEvidence(t, second.markdown)
}

// A repository in the SHA-256 object format has 64-character commits, which
// the run action records; the renderer accepts them on both sides.
func TestRenderAcceptsASHA256Head(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir := artifact(t, "pass")
	mutateMetadata(t, dir, func(m map[string]any) { obj(m, "head")["sha"] = head })
	want := expected(0)
	want.headSHA = head
	out := render(dir, want, defaultLimits)
	if out.state != stateVerdict || !strings.Contains(out.markdown, "**Commit** `"+head+"`") {
		t.Fatalf("state %s: %s", out.state, out.reason)
	}
	var stdout bytes.Buffer
	if got := run(replaceArg(cliArgs(dir, "0"), "--head-sha", head), &stdout, &bytes.Buffer{}); got != exitRendered {
		t.Errorf("exit %d for a SHA-256 head", got)
	}
}
