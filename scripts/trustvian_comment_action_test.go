//go:build !windows

// Tests for the trustvian-comment GitHub Action (task 079, comment side;
// ADR 0058).
//
// The action's scripts run here exactly as the composite action runs them —
// same environment variables, same order. The render step drives the real
// trustvian-ci-render, built from this checkout, against the real artifacts
// in cmd/trustvian-ci-render/testdata, so what an empty download directory or
// a rejected artifact becomes is the renderer's own answer. The post step
// drives a fake poster, because the real one only talks to an https API; the
// lines post.sh reads from it are checked against the poster's source below.
// The real comment against GitHub's API is the trustvian-run-action
// workflow's comment job.
package scripts_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	commentActionDir = "../.github/actions/trustvian-comment"
	// fixtureHead is the head commit every renderer test artifact was
	// generated under (cmd/trustvian-ci-render/testdata/generate.sh).
	fixtureHead = "0123456789abcdef0123456789abcdef01234567"
)

// commentEnv is the job environment of one comment action invocation, at the
// identity the renderer's fixtures were generated under.
func commentEnv(t *testing.T) actionEnv {
	t.Helper()
	e := newActionEnv(t, newFakeRuntime(t))
	event := filepath.Join(t.TempDir(), "event.json")
	write(t, event, `{"pull_request":{"number":7,"head":{"sha":"`+fixtureHead+`"}}}`)
	e.vars["GITHUB_EVENT_PATH"] = event
	e.vars["GITHUB_RUN_ID"] = "1000000001"
	e.vars["GITHUB_RUN_ATTEMPT"] = "1"
	e.vars["TRUSTVIAN_COMMENT_HEAD_SHA"] = fixtureHead
	return e
}

func runCommentScript(t *testing.T, e actionEnv, script string) actionResult {
	t.Helper()
	return runScriptAt(t, e, filepath.Join(commentActionDir, script), t.TempDir())
}

// buildRenderer builds trustvian-ci-render from this checkout into a bin
// directory of its own.
func buildRenderer(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, "trustvian-ci-render"), "./cmd/trustvian-ci-render")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the renderer: %v\n%s", err, out)
	}
	return bin
}

// copyArtifact copies one of the renderer's real test artifacts into a fresh
// directory, as actions/download-artifact would leave it.
func copyArtifact(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("..", "cmd", "trustvian-ci-render", "testdata", "artifacts", name)
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// The render step posts whatever the renderer renders — evidence, no verdict,
// or a rejected artifact as no verdict — and writes the same rendering to the
// job summary. An empty download directory, which is what a missing artifact
// leaves, is the renderer's "artifact is missing" no verdict, not a failure.
func TestCommentActionRendersEveryPostableState(t *testing.T) {
	requireTools(t)
	bin := buildRenderer(t)
	tests := []struct {
		name     string
		artifact func(t *testing.T) string
		exitCode string
		want     string
		contains string
	}{
		{"evidence", func(t *testing.T) string { return copyArtifact(t, "pass") }, "0", "0", "— PASS"},
		{"missing artifact", func(t *testing.T) string { return t.TempDir() }, "0", "1", "artifact is missing"},
		{"the CLI did not run", func(t *testing.T) string { return t.TempDir() }, "", "1", "the CLI did not run"},
		{"operational, no document", func(t *testing.T) string { return copyArtifact(t, "operational") }, "3", "1", "no verdict"},
		{"rejected artifact", func(t *testing.T) string {
			dir := copyArtifact(t, "pass")
			write(t, filepath.Join(dir, "trustvian-run.json"), `{"version":"1","version":"1"}`)
			return dir
		}, "0", "3", "no verdict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := commentEnv(t)
			e.vars["TRUSTVIAN_COMMENT_BIN_DIR"] = bin
			e.vars["TRUSTVIAN_COMMENT_ARTIFACT_DIR"] = tt.artifact(t)
			e.vars["INPUT_EXIT_CODE"] = tt.exitCode
			res := runCommentScript(t, e, "render.sh")
			if res.code != 0 {
				t.Fatalf("render step failed (%d):\n%s%s", res.code, res.stdout, res.stderr)
			}
			if res.outputs["renderer-exit"] != tt.want {
				t.Errorf("renderer-exit %q, want %q", res.outputs["renderer-exit"], tt.want)
			}
			body := readFile(t, res.outputs["body-file"])
			if !strings.Contains(body, tt.contains) || !strings.Contains(body, fixtureHead) {
				t.Errorf("rendering lacks %q or the head commit:\n%s", tt.contains, body)
			}
			if tt.want != "0" && strings.Contains(body, "— PASS") {
				t.Errorf("a no-verdict rendering carries a verdict:\n%s", body)
			}
			if summary := readFile(t, e.summary); summary != body {
				t.Errorf("the job summary is not the rendering:\n%s", summary)
			}
		})
	}
}

// Renderer exit 2 is this job's own malformed context: nothing is rendered,
// nothing reaches the summary, nothing is handed to the post step, and the
// step fails.
func TestCommentActionPostsNothingOnRendererUsage(t *testing.T) {
	requireTools(t)
	e := commentEnv(t)
	e.vars["TRUSTVIAN_COMMENT_BIN_DIR"] = buildRenderer(t)
	e.vars["TRUSTVIAN_COMMENT_ARTIFACT_DIR"] = copyArtifact(t, "pass")
	e.vars["TRUSTVIAN_COMMENT_HEAD_SHA"] = "not-a-commit"
	e.vars["INPUT_EXIT_CODE"] = "0"
	res := runCommentScript(t, e, "render.sh")
	if res.code == 0 || !strings.Contains(res.stdout, "::error title=trustvian-comment::trustvian-ci-render refused") {
		t.Fatalf("exit %d:\n%s%s", res.code, res.stdout, res.stderr)
	}
	if res.outputs["renderer-exit"] != "2" {
		t.Errorf("renderer-exit %q, want 2", res.outputs["renderer-exit"])
	}
	if _, ok := res.outputs["body-file"]; ok {
		t.Error("a body file was handed on after exit 2")
	}
	if summary, err := os.ReadFile(e.summary); err == nil && len(summary) > 0 {
		t.Errorf("the summary was written: %q", summary)
	}
}

// An unwritable job summary is reported, and the comment is still posted.
func TestCommentActionSurvivesAnUnwritableSummary(t *testing.T) {
	requireTools(t)
	e := commentEnv(t)
	e.vars["TRUSTVIAN_COMMENT_BIN_DIR"] = buildRenderer(t)
	e.vars["TRUSTVIAN_COMMENT_ARTIFACT_DIR"] = copyArtifact(t, "pass")
	e.vars["INPUT_EXIT_CODE"] = "0"
	e.vars["GITHUB_STEP_SUMMARY"] = filepath.Join(t.TempDir(), "missing", "summary")
	res := runCommentScript(t, e, "render.sh")
	if res.code != 0 || res.outputs["body-file"] == "" {
		t.Fatalf("exit %d:\n%s%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "::warning title=trustvian-comment::the job summary could not be written") {
		t.Errorf("no warning:\n%s", res.stdout)
	}
}

// fakePoster records its arguments and the token it was given, prints
// FAKE_POSTER_STDOUT, and exits FAKE_POSTER_EXIT.
func fakePoster(t *testing.T) (bin, args, token string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	args, token = filepath.Join(dir, "args"), filepath.Join(dir, "token")
	write(t, filepath.Join(bin, "trustvian-ci-comment"), `#!/usr/bin/env bash
: > "$FAKE_ARGS"
for a in "$@"; do printf '%s\0' "$a" >> "$FAKE_ARGS"; done
printf '%s' "${GITHUB_TOKEN:-}" > "$FAKE_TOKEN"
printf '%s' "${FAKE_POSTER_STDOUT:-}"
exit "${FAKE_POSTER_EXIT:-0}"
`)
	return bin, args, token
}

func postEnv(t *testing.T) (actionEnv, string, string) {
	t.Helper()
	e := commentEnv(t)
	bin, args, token := fakePoster(t)
	body := filepath.Join(t.TempDir(), "body.md")
	write(t, body, "### Trustvian behavioral gate — PASS\n")
	e.vars["TRUSTVIAN_COMMENT_BIN_DIR"] = bin
	e.vars["TRUSTVIAN_COMMENT_PULL_REQUEST"] = "7"
	e.vars["TRUSTVIAN_COMMENT_MARKER_ID"] = "trustvian-run"
	e.vars["TRUSTVIAN_COMMENT_BODY_FILE"] = body
	e.vars["GITHUB_TOKEN"] = "ghs_the-job-token"
	e.vars["FAKE_ARGS"] = args
	e.vars["FAKE_TOKEN"] = token
	return e, args, token
}

// Poster exit 0 succeeds for every outcome — created, updated, superseded and
// not permitted — and only a created or updated comment counts as posted.
// Exit 1 and 2 fail the job, after the poster's own annotations reach the log.
func TestCommentActionPostsAndReportsTheOutcome(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name, stdout     string
		exit             string
		wantCode         int
		posted, outcome  string
		wantOutputsUnset bool
	}{
		{"created", "created comment 42 for trustvian-run\n", "0", 0, "true", "created", false},
		{"updated", "updated comment 42 for trustvian-run\n", "0", 0, "true", "updated", false},
		{"superseded", "::notice title=trustvian-comment::superseded by fedcba987654: the pull request has a newer head\n", "0", 0, "false", "superseded", false},
		{"not permitted", "::warning title=trustvian-comment::the gate comment was not posted: the token cannot write to this pull request\n", "0", 0, "false", "not-permitted", false},
		{"api failure", "::error title=trustvian-comment::the gate comment was not posted: boom\n", "1", 1, "", "", true},
		{"usage", "", "2", 1, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, args, token := postEnv(t)
			e.vars["FAKE_POSTER_STDOUT"] = tt.stdout
			e.vars["FAKE_POSTER_EXIT"] = tt.exit
			res := runCommentScript(t, e, "post.sh")
			if res.code != tt.wantCode {
				t.Fatalf("exit %d, want %d:\n%s%s", res.code, tt.wantCode, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stdout, tt.stdout) {
				t.Errorf("the poster's output did not reach the log:\n%s", res.stdout)
			}
			if tt.wantOutputsUnset {
				if _, ok := res.outputs["posted"]; ok {
					t.Errorf("posted was written on a failure: %v", res.outputs)
				}
				if !strings.Contains(res.stdout, "::error title=trustvian-comment::") {
					t.Errorf("a failure is not annotated:\n%s", res.stdout)
				}
			} else if res.outputs["posted"] != tt.posted || res.outputs["outcome"] != tt.outcome {
				t.Errorf("posted %q outcome %q, want %q %q", res.outputs["posted"], res.outputs["outcome"], tt.posted, tt.outcome)
			}
			want := []string{"--repository=trustvian/trustvian", "--pull-request=7", "--head-sha=" + fixtureHead,
				"--marker-id=trustvian-run", "--body-file=" + e.vars["TRUSTVIAN_COMMENT_BODY_FILE"]}
			if got := strings.Split(strings.TrimSuffix(readFile(t, args), "\x00"), "\x00"); !slices.Equal(got, want) {
				t.Errorf("poster arguments %q, want %q", got, want)
			}
			if got := readFile(t, token); got != "ghs_the-job-token" {
				t.Errorf("the poster got token %q", got)
			}
		})
	}
}

// post.sh reads the poster's outcome from lines the poster writes. Each one
// must still be a line the poster's source writes, or a renamed message would
// silently turn every outcome into "".
func TestCommentActionOutcomeLinesMatchThePoster(t *testing.T) {
	poster := readFile(t, "../cmd/trustvian-ci-comment/main.go")
	for _, line := range []string{
		`"created comment %d for %s\n"`,
		`"updated comment %d for %s\n"`,
		`"::%s title=trustvian-comment::%s\n"`,
		`"superseded by %s: the pull request has a newer head`,
		`"the gate comment was not posted: the token cannot write to this pull "`,
	} {
		if !strings.Contains(poster, line) {
			t.Errorf("the poster no longer writes %s", line)
		}
	}
}

// Every step refuses the events that run with base-repository privileges,
// and setup refuses anything that is not a pull request, or a pull request it
// cannot name — before anything is fetched or built.
func TestCommentActionRefusesWhatItCannotComment(t *testing.T) {
	requireTools(t)
	for _, script := range []string{"setup.sh", "render.sh", "post.sh"} {
		for _, event := range []string{"pull_request_target", "workflow_run"} {
			t.Run(script+"/"+event, func(t *testing.T) {
				e := commentEnv(t)
				e.vars["GITHUB_EVENT_NAME"] = event
				res := runCommentScript(t, e, script)
				if res.code == 0 || !strings.Contains(res.stdout, "refusing to run on "+event) ||
					!strings.Contains(res.stdout, "title=trustvian-comment::") {
					t.Errorf("exit %d:\n%s", res.code, res.stdout)
				}
			})
		}
	}
	tests := []struct {
		name  string
		event string
		vars  map[string]string
		want  string
	}{
		{"push", "", map[string]string{"GITHUB_EVENT_NAME": "push"}, "runs on pull_request events only"},
		{"no number", `{"pull_request":{"head":{"sha":"` + fixtureHead + `"}}}`, nil, "event.pull_request.number"},
		{"string number", `{"pull_request":{"number":"7","head":{"sha":"` + fixtureHead + `"}}}`, nil, "event.pull_request.number"},
		{"zero", `{"pull_request":{"number":0,"head":{"sha":"` + fixtureHead + `"}}}`, nil, "event.pull_request.number"},
		{"fraction", `{"pull_request":{"number":7.5,"head":{"sha":"` + fixtureHead + `"}}}`, nil, "event.pull_request.number"},
		{"no head", `{"pull_request":{"number":7}}`, nil, "cannot name the head commit"},
		{"bad artifact name", "", map[string]string{"INPUT_ARTIFACT_NAME": "../escape"}, "input artifact-name"},
		{"bad marker", "", map[string]string{"INPUT_MARKER_ID": "x --> <b>"}, "input marker-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := commentEnv(t)
			if tt.event != "" {
				write(t, e.vars["GITHUB_EVENT_PATH"], tt.event)
			}
			maps.Copy(e.vars, tt.vars)
			res := runCommentScript(t, e, "setup.sh")
			if res.code == 0 || !strings.Contains(res.stdout, tt.want) {
				t.Errorf("exit %d, want a failure naming %q:\n%s%s", res.code, tt.want, res.stdout, res.stderr)
			}
			if strings.Contains(res.stdout, "Downloading") || strings.Contains(res.stdout, "Fetching") {
				t.Error("setup fetched before validating its context")
			}
		})
	}
}

// The comment action, structurally: four steps in order, the one tolerated
// failure is the download, every action pinned, no expression spliced into a
// script, the token in the post step's environment and nowhere else, and the
// download writing only into the setup step's fresh directory.
func TestCommentActionDefinition(t *testing.T) {
	action := loadYAML(t, filepath.Join(commentActionDir, "action.yml"))
	runs, _ := action["runs"].(map[string]any)
	if runs["using"] != "composite" {
		t.Fatalf("runs.using %v, want composite", runs["using"])
	}
	steps, _ := runs["steps"].([]any)
	var ids []string
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		id := step["id"].(string)
		ids = append(ids, id)
		if coe, ok := step["continue-on-error"]; ok && (id != "download" || coe != true) {
			t.Errorf("step %s sets continue-on-error %v", id, coe)
		}
		if use, ok := step["uses"].(string); ok && !pinnedUse.MatchString(use) {
			t.Errorf("step %s uses %q, not pinned to a full commit", id, use)
		}
		if script, ok := step["run"].(string); ok && strings.Contains(script, "${{") {
			t.Errorf("step %s splices an expression into its script: %q", id, script)
		}
		env, _ := step["env"].(map[string]any)
		for k, v := range env {
			if strings.Contains(v.(string), "github-token") && !(id == "post" && k == "GITHUB_TOKEN") {
				t.Errorf("step %s passes the token as %s", id, k)
			}
		}
		if _, ok := env["GITHUB_TOKEN"]; ok != (id == "post") {
			t.Errorf("step %s: GITHUB_TOKEN in env is %v", id, ok)
		}
		with, _ := step["with"].(map[string]any)
		if id == "download" {
			if step["continue-on-error"] != true {
				t.Error("the download may not fail without failing the action; a missing artifact must render")
			}
			want := map[string]any{"name": "${{ inputs.artifact-name }}", "path": "${{ steps.setup.outputs.artifact-dir }}"}
			if len(with) != len(want) || with["name"] != want["name"] || with["path"] != want["path"] {
				t.Errorf("download with %v, want %v", with, want)
			}
		}
	}
	if want := []string{"setup", "download", "render", "post"}; !slices.Equal(ids, want) {
		t.Errorf("steps %v, want %v", ids, want)
	}
	inputs, _ := action["inputs"].(map[string]any)
	if got := slices.Sorted(maps.Keys(inputs)); !slices.Equal(got, []string{"artifact-name", "exit-code", "github-token", "marker-id"}) {
		t.Errorf("inputs %v", got)
	}
	if exit, _ := inputs["exit-code"].(map[string]any); exit["required"] != true {
		t.Error("exit-code is not required")
	}
	if tok, _ := inputs["github-token"].(map[string]any); tok["default"] != "${{ github.token }}" {
		t.Errorf("github-token default %v", tok["default"])
	}
	outputs, _ := action["outputs"].(map[string]any)
	for _, name := range []string{"renderer-exit", "posted", "outcome"} {
		if _, ok := outputs[name]; !ok {
			t.Errorf("no %s output", name)
		}
	}
}

// The comment action reads nothing from the job's workspace — not the
// variable, not the expression — in any file it runs, including the run
// action's lib.sh, which it sources. ADR 0058 § 2.
func TestCommentActionNeverReadsTheWorkspace(t *testing.T) {
	files := []string{
		filepath.Join(commentActionDir, "action.yml"),
		filepath.Join(commentActionDir, "setup.sh"),
		filepath.Join(commentActionDir, "render.sh"),
		filepath.Join(commentActionDir, "post.sh"),
		filepath.Join(actionDir, "lib.sh"),
	}
	workspace := regexp.MustCompile(`GITHUB_WORKSPACE|github\.workspace`)
	for _, path := range files {
		if m := workspace.FindString(readFile(t, path)); m != "" {
			t.Errorf("%s references %s", path, m)
		}
	}
	// Every script starts in $RUNNER_TEMP, so not even a relative path can
	// resolve inside the workspace the job step starts in.
	for _, script := range files[1:4] {
		if !strings.Contains(readFile(t, script), `cd -P -- "$RUNNER_TEMP"`) {
			t.Errorf("%s does not leave the workspace for $RUNNER_TEMP", script)
		}
	}
	// It builds exactly the two commands, from the pinned source.
	setup := readFile(t, filepath.Join(commentActionDir, "setup.sh"))
	builds := regexp.MustCompile(`(?m)^build_pinned (\S+) (\S+) `).FindAllStringSubmatch(setup, -1)
	var pkgs []string
	for _, b := range builds {
		pkgs = append(pkgs, b[1]+" "+b[2])
	}
	if want := []string{". ./cmd/trustvian-ci-render", ". ./cmd/trustvian-ci-comment"}; !slices.Equal(pkgs, want) {
		t.Errorf("builds %v, want %v", pkgs, want)
	}
	for _, helper := range []string{"refuse_privileged_event", "provide_pinned_source", "verify_pinned_build", "resolve_head"} {
		if !strings.Contains(setup, helper) {
			t.Errorf("setup.sh does not use %s", helper)
		}
	}
}
