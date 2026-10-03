//go:build !windows

// Tests for the trustvian-run GitHub Action (task 079, run side).
//
// The action's steps are bash scripts. These tests run them exactly as the
// composite action does — same environment variables, same order — against
// fake `trustvian`, `trustvian-local` and `trustvian-collector` executables,
// so every exit code, every input shape and every failure of preservation is
// exercised deterministically and offline. The one path that cannot be faked,
// a real scenario through the real CLI, control plane and Collector, is the
// trustvian-run-action workflow, which runs the action itself.
//
// The workflow tests at the end parse YAML. That is the one import of
// go.yaml.in/yaml/v3 outside `config`, and it is test-only: this package has
// no non-test code, so it adds no package to any binary's dependency graph.
//
// Not on Windows: the action runs on Linux and macOS runners only, and these
// tests signal processes.
package scripts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	actionDir   = "../.github/actions/trustvian-run"
	headSHA     = "1111111111111111111111111111111111111111"
	mergeSHA    = "2222222222222222222222222222222222222222"
	runtimePin  = "5521759bb115744498946bfd03d7dd45f688caac"
	maxResult   = 32*1024*1024 + 64*1024
	fakeAPIPort = "54321"
)

// requireTools skips on a machine without bash or jq, and fails in CI, where
// both are guaranteed and a skip would hide the whole suite.
func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required in CI: %v", tool, err)
			}
			t.Skipf("%s not available: %v", tool, err)
		}
	}
}

// fakeRuntime is a bin directory holding fake runtime executables, plus the
// files they record what happened into.
type fakeRuntime struct {
	bin, args, pwd, cpPID, invoked string
}

func newFakeRuntime(t *testing.T) fakeRuntime {
	t.Helper()
	dir := t.TempDir()
	f := fakeRuntime{
		bin:     filepath.Join(dir, "bin"),
		args:    filepath.Join(dir, "args"),
		pwd:     filepath.Join(dir, "pwd"),
		cpPID:   filepath.Join(dir, "control-plane.pid"),
		invoked: filepath.Join(dir, "invoked"),
	}
	if err := os.Mkdir(f.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The CLI: records its argv NUL-separated and its working directory, then
	// writes FAKE_STDOUT to stdout and exits FAKE_EXIT.
	write(t, filepath.Join(f.bin, "trustvian"), `#!/usr/bin/env bash
touch "$FAKE_INVOKED"
: > "$FAKE_ARGS"
for a in "$@"; do printf '%s\0' "$a" >> "$FAKE_ARGS"; done
pwd -P > "$FAKE_PWD"
[ -z "${FAKE_STDOUT:-}" ] || cat "$FAKE_STDOUT"
echo "fake trustvian progress" >&2
exit "${FAKE_EXIT:-0}"
`)
	// The control plane: publishes a loopback endpoint and runs until stopped.
	write(t, filepath.Join(f.bin, "trustvian-local"), `#!/usr/bin/env bash
state=""
while [ $# -gt 0 ]; do
  case "$1" in --state-dir) state="$2"; shift 2 ;; *) shift ;; esac
done
echo $$ > "$FAKE_CP_PID"
[ "${FAKE_CP_IGNORE_TERM:-}" != 1 ] || trap '' TERM
[ -z "${FAKE_CP_TERM_MARKER:-}" ] || trap 'echo TERM >> "$FAKE_CP_TERM_MARKER"' TERM
url="${FAKE_CP_URL:-http://127.0.0.1:`+fakeAPIPort+`}"
printf '{"version":"1","api_url":"%s"}\n' "$url" > "$state/runtime.json"
while :; do sleep 0.2; done
`)
	write(t, filepath.Join(f.bin, "trustvian-collector"), "#!/bin/sh\nexit 0\n")
	return f
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// actionEnv is a GitHub Actions job environment for one invocation.
type actionEnv struct {
	vars    map[string]string
	temp    string
	output  string
	summary string
}

func newActionEnv(t *testing.T, f fakeRuntime) actionEnv {
	t.Helper()
	dir := t.TempDir()
	e := actionEnv{
		temp:    filepath.Join(dir, "runner-temp"),
		output:  filepath.Join(dir, "github-output"),
		summary: filepath.Join(dir, "step-summary"),
	}
	if err := os.Mkdir(e.temp, 0o755); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(dir, "event.json")
	write(t, event, `{"pull_request":{"number":7,"head":{"sha":"`+headSHA+`"}}}`)
	e.vars = map[string]string{
		"PATH":                         os.Getenv("PATH"),
		"HOME":                         dir,
		"CI":                           "true",
		"RUNNER_TEMP":                  e.temp,
		"GITHUB_OUTPUT":                e.output,
		"GITHUB_STEP_SUMMARY":          e.summary,
		"GITHUB_EVENT_NAME":            "pull_request",
		"GITHUB_EVENT_PATH":            event,
		"GITHUB_SHA":                   mergeSHA,
		"GITHUB_SERVER_URL":            "https://github.com",
		"GITHUB_REPOSITORY":            "trustvian/trustvian",
		"GITHUB_RUN_ID":                "123456",
		"GITHUB_RUN_ATTEMPT":           "1",
		"TRUSTVIAN_RUN_BIN_DIR":        f.bin,
		"TRUSTVIAN_RUN_RUNTIME_COMMIT": runtimePin,
		"TRUSTVIAN_RUN_GO_VERSION":     "go1.27.1",
		"INPUT_SCENARIO":               "scenarios/login.yaml",
		"INPUT_FAIL_FAST":              "false",
		"INPUT_WORKING_DIRECTORY":      ".",
		"INPUT_ARTIFACT_NAME":          "trustvian-run",
		"FAKE_ARGS":                    f.args,
		"FAKE_PWD":                     f.pwd,
		"FAKE_CP_PID":                  f.cpPID,
		"FAKE_INVOKED":                 f.invoked,
	}
	return e
}

type actionResult struct {
	code           int
	stdout, stderr string
	outputs        map[string]string
}

// runActionScript runs one of the action's scripts with the environment and
// returns its exit code, its streams, and the outputs it appended.
func runActionScript(t *testing.T, e actionEnv, script, dir string) actionResult {
	t.Helper()
	return runScriptAt(t, e, filepath.Join(actionDir, script), dir)
}

// runScriptAt runs the script at path — of either action — with the
// environment, in dir.
func runScriptAt(t *testing.T, e actionEnv, path, dir string) actionResult {
	t.Helper()
	script := filepath.Base(path)
	if err := os.WriteFile(e.output, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", abs)
	cmd.Dir = dir
	for k, v := range e.vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", script, err)
	}
	outputs := map[string]string{}
	raw, _ := os.ReadFile(e.output)
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	return actionResult{code: code, stdout: stdout.String(), stderr: stderr.String(), outputs: outputs}
}

// finish runs the finish step as the composite action does, with the run
// step's outputs and an upload outcome.
func finish(t *testing.T, e actionEnv, run actionResult, uploadOutcome, artifactID string) actionResult {
	t.Helper()
	fe := e
	fe.vars = map[string]string{}
	for k, v := range e.vars {
		fe.vars[k] = v
	}
	fe.vars["TRUSTVIAN_RUN_EXIT_CODE"] = run.outputs["exit-code"]
	fe.vars["TRUSTVIAN_RUN_HEAD_SHA"] = run.outputs["head-sha"]
	fe.vars["TRUSTVIAN_RUN_RESULT"] = run.outputs["result"]
	fe.vars["TRUSTVIAN_RUN_UPLOAD_OUTCOME"] = uploadOutcome
	fe.vars["TRUSTVIAN_RUN_ARTIFACT_ID"] = artifactID
	fe.vars["TRUSTVIAN_RUN_ARTIFACT_NAME"] = e.vars["INPUT_ARTIFACT_NAME"]
	return runActionScript(t, fe, "finish.sh", t.TempDir())
}

func stdoutFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type metadata struct {
	Version string `json:"version"`
	Head    struct {
		SHA    string `json:"sha"`
		Source string `json:"source"`
	} `json:"head"`
	Event      string `json:"event"`
	Repository string `json:"repository"`
	Run        struct {
		ID      string `json:"id"`
		Attempt string `json:"attempt"`
	} `json:"run"`
	Mode         string `json:"mode"`
	ControlPlane string `json:"control_plane"`
	CLI          struct {
		ExitCode int `json:"exit_code"`
	} `json:"cli"`
	Result struct {
		Status string `json:"status"`
		File   string `json:"file"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"result"`
	Runtime struct {
		SourceCommit string `json:"source_commit"`
		GoVersion    string `json:"go_version"`
	} `json:"runtime"`
}

func readMetadata(t *testing.T, artifactDir string) metadata {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(artifactDir, "trustvian-run.json"))
	if err != nil {
		t.Fatalf("no execution metadata: %v", err)
	}
	var m metadata
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("metadata %s: %v", raw, err)
	}
	return m
}

func readArgs(t *testing.T, f fakeRuntime) []string {
	t.Helper()
	raw, err := os.ReadFile(f.args)
	if err != nil {
		t.Fatalf("the CLI was not run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

const passDocument = `{"version":"1","scenario":{"name":"login","runs":"5"},"comparison":{"gate":{"verdict":"pass"}}}` + "\n"
const failDocument = `{"version":"1","scenario":{"name":"login","runs":"5"},"comparison":{"gate":{"verdict":"fail"}}}` + "\n"
const suiteDocument = `{"version":"1","complete":true,"members":[],"exit_code":3}` + "\n"

// Every CLI code reaches the finish step's exit status unchanged — 3 is
// never 1, and 1 is never 0 — and the run step itself succeeds once the
// result is preserved, whatever the code was.
func TestRunActionPassesEveryExitCodeThrough(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name   string
		exit   int
		stdout string
		result string
	}{
		{"pass", 0, passDocument, "present"},
		{"gate fail", 1, failDocument, "present"},
		{"usage, no document", 2, "", "absent"},
		{"operational, no document", 3, "", "absent"},
		{"operational suite, with a document", 3, suiteDocument, "present"},
		{"outside the contract", 7, "", "absent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["FAKE_EXIT"] = fmt.Sprint(tt.exit)
			if tt.stdout != "" {
				e.vars["FAKE_STDOUT"] = stdoutFile(t, tt.stdout)
			}
			run := runActionScript(t, e, "run.sh", t.TempDir())
			if run.code != 0 {
				t.Fatalf("run step exited %d, want 0\n%s%s", run.code, run.stdout, run.stderr)
			}
			if got := run.outputs["exit-code"]; got != fmt.Sprint(tt.exit) {
				t.Errorf("exit-code output %q, want %d", got, tt.exit)
			}
			if got := run.outputs["result"]; got != tt.result {
				t.Errorf("result output %q, want %q", got, tt.result)
			}
			m := readMetadata(t, run.outputs["artifact-dir"])
			if m.CLI.ExitCode != tt.exit {
				t.Errorf("metadata exit code %d, want %d", m.CLI.ExitCode, tt.exit)
			}
			done := finish(t, e, run, "success", "99")
			if done.code != tt.exit {
				t.Errorf("finish exited %d, want exactly the CLI's %d\n%s", done.code, tt.exit, done.stdout)
			}
		})
	}
}

// A failed upload is reported and does not change the CLI's code in either
// direction; the upload step's own failure is what fails the action.
func TestRunActionUploadFailureDoesNotMaskTheCode(t *testing.T) {
	requireTools(t)
	for _, code := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["FAKE_EXIT"] = fmt.Sprint(code)
			if code < 2 {
				e.vars["FAKE_STDOUT"] = stdoutFile(t, passDocument)
			}
			run := runActionScript(t, e, "run.sh", t.TempDir())
			done := finish(t, e, run, "failure", "")
			if done.code != code {
				t.Errorf("finish exited %d, want %d", done.code, code)
			}
			if !strings.Contains(done.stdout, "::error title=trustvian-run::the result artifact was not uploaded") {
				t.Errorf("upload failure not reported:\n%s", done.stdout)
			}
			summary := readFile(t, e.summary)
			if !strings.Contains(summary, "**not uploaded**") {
				t.Errorf("summary does not say the artifact is missing:\n%s", summary)
			}
		})
	}
}

// When the CLI never ran there is no code to pass through, and the action
// must not look as though it passed.
func TestRunActionFinishWithoutTheCLIFails(t *testing.T) {
	requireTools(t)
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	done := finish(t, e, actionResult{outputs: map[string]string{}}, "skipped", "")
	if done.code == 0 {
		t.Fatal("finish exited 0 although trustvian never ran")
	}
	if !strings.Contains(readFile(t, e.summary), "none — it did not run") {
		t.Errorf("summary does not say the CLI did not run:\n%s", readFile(t, e.summary))
	}
}

// Every input reaches the CLI as exactly one argument, attached to its flag,
// and nothing is evaluated by a shell along the way.
func TestRunActionPassesArgumentsSafely(t *testing.T) {
	requireTools(t)
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	work := t.TempDir()
	canary := filepath.Join(work, "pwned")
	hostile := map[string]string{
		"INPUT_SCENARIO":         "scen ario/$(touch " + canary + ")`touch " + canary + "`;*.yaml",
		"INPUT_SUITE":            "-rf --json",
		"INPUT_REFERENCE":        "last\nsecond line",
		"INPUT_SCENARIO_TIMEOUT": "'10m\" $HOME",
		"INPUT_FAIL_FAST":        "true",
		"INPUT_API_URL":          "http://example.invalid --api-url=http://evil.invalid",
	}
	for k, v := range hostile {
		e.vars[k] = v
	}
	e.vars["FAKE_EXIT"] = "2"
	run := runActionScript(t, e, "run.sh", work)
	if run.code != 0 {
		t.Fatalf("run step exited %d\n%s%s", run.code, run.stdout, run.stderr)
	}
	want := []string{
		"eval", "run", "--json", "--collector-bin=" + filepath.Join(f.bin, "trustvian-collector"),
		"--scenario=" + hostile["INPUT_SCENARIO"],
		"--suite=" + hostile["INPUT_SUITE"],
		"--reference=" + hostile["INPUT_REFERENCE"],
		"--scenario-timeout=" + hostile["INPUT_SCENARIO_TIMEOUT"],
		"--fail-fast",
		"--api-url=" + hostile["INPUT_API_URL"],
	}
	if got := readArgs(t, f); !slices.Equal(got, want) {
		t.Errorf("argv\n got %q\nwant %q", got, want)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("an input was evaluated by a shell")
	}
	// An attached control plane: the action started none.
	if _, err := os.Stat(f.cpPID); err == nil {
		t.Error("a control plane was started although api-url was given")
	}
	if m := readMetadata(t, run.outputs["artifact-dir"]); m.Mode != "both" || m.ControlPlane != "attached" {
		t.Errorf("metadata mode %q control plane %q", m.Mode, m.ControlPlane)
	}
}

// Empty optional inputs become no flag at all, and the CLI runs in the
// working directory with the control plane the action started.
func TestRunActionOmitsEmptyInputsAndRunsInTheWorkingDirectory(t *testing.T) {
	requireTools(t)
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "workload"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.vars["INPUT_WORKING_DIRECTORY"] = "workload"
	e.vars["FAKE_STDOUT"] = stdoutFile(t, passDocument)
	run := runActionScript(t, e, "run.sh", work)
	if run.code != 0 {
		t.Fatalf("run step exited %d\n%s%s", run.code, run.stdout, run.stderr)
	}
	want := []string{
		"eval", "run", "--json", "--collector-bin=" + filepath.Join(f.bin, "trustvian-collector"),
		"--scenario=scenarios/login.yaml", "--api-url=http://127.0.0.1:" + fakeAPIPort,
	}
	if got := readArgs(t, f); !slices.Equal(got, want) {
		t.Errorf("argv\n got %q\nwant %q", got, want)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(work, "workload"))
	if got := strings.TrimSpace(readFile(t, f.pwd)); got != wantDir {
		t.Errorf("the CLI ran in %s, want %s", got, wantDir)
	}
}

func TestRunActionRefusesBadActionInputs(t *testing.T) {
	requireTools(t)
	tests := map[string]map[string]string{
		"fail-fast not a boolean":    {"INPUT_FAIL_FAST": "yes"},
		"artifact name with a slash": {"INPUT_ARTIFACT_NAME": "../escape"},
		"artifact name with a space": {"INPUT_ARTIFACT_NAME": "a b"},
		"missing working directory":  {"INPUT_WORKING_DIRECTORY": "does-not-exist"},
	}
	for name, vars := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			for k, v := range vars {
				e.vars[k] = v
			}
			run := runActionScript(t, e, "run.sh", t.TempDir())
			if run.code == 0 || !strings.Contains(run.stdout, "::error title=trustvian-run::input") {
				t.Fatalf("exit %d, want an input error\n%s", run.code, run.stdout)
			}
			if _, err := os.Stat(f.invoked); err == nil {
				t.Error("the CLI ran despite an invalid action input")
			}
			if run.outputs["exit-code"] != "" {
				t.Errorf("exit-code output %q although the CLI did not run", run.outputs["exit-code"])
			}
		})
	}
}

// The result is preserved byte for byte after a FAIL and after an operational
// error that produced a document; an operational error without one leaves the
// metadata, saying so.
func TestRunActionPreservesTheResultAfterFailures(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name   string
		exit   int
		stdout string
	}{
		{"gate fail", 1, failDocument},
		{"operational suite", 3, suiteDocument},
		{"operational, no document", 3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["FAKE_EXIT"] = fmt.Sprint(tt.exit)
			if tt.stdout != "" {
				e.vars["FAKE_STDOUT"] = stdoutFile(t, tt.stdout)
			}
			run := runActionScript(t, e, "run.sh", t.TempDir())
			if run.code != 0 {
				t.Fatalf("run step exited %d\n%s", run.code, run.stdout)
			}
			dir := run.outputs["artifact-dir"]
			temp, err := filepath.EvalSymlinks(e.temp)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(dir, temp+string(filepath.Separator)) {
				t.Errorf("artifact %s is not under RUNNER_TEMP", dir)
			}
			m := readMetadata(t, dir)
			preserved, err := os.ReadFile(filepath.Join(dir, "result.json"))
			if tt.stdout == "" {
				if err == nil {
					t.Error("result.json exists although the CLI wrote nothing")
				}
				if m.Result.Status != "absent" || m.Result.File != "" {
					t.Errorf("metadata result %+v, want absent", m.Result)
				}
				return
			}
			if string(preserved) != tt.stdout {
				t.Errorf("result.json %q, want the CLI's stdout byte for byte %q", preserved, tt.stdout)
			}
			sum := sha256.Sum256([]byte(tt.stdout))
			if m.Result.Status != "present" || m.Result.File != "result.json" ||
				m.Result.Bytes != len(tt.stdout) || m.Result.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("metadata result %+v", m.Result)
			}
			if m.Repository != "trustvian/trustvian" || m.Run.ID != "123456" || m.Run.Attempt != "1" {
				t.Errorf("metadata run %+v in %q", m.Run, m.Repository)
			}
			if m.Version != "1" || m.Runtime.SourceCommit != runtimePin || m.Runtime.GoVersion != "go1.27.1" ||
				m.Mode != "scenario" || m.ControlPlane != "started" || m.Event != "pull_request" {
				t.Errorf("metadata %+v", m)
			}
		})
	}
}

// Missing, malformed and oversized output is never preserved as a result,
// never truncated, and never allowed to stand as a successful run.
func TestRunActionRefusesMissingInvalidAndOversizedResults(t *testing.T) {
	requireTools(t)
	oversized := `{"padding":"` + strings.Repeat("x", maxResult) + `"}`
	exactlyAtLimit := `{"padding":"` + strings.Repeat("x", maxResult-len(`{"padding":""}`)) + `"}`
	tests := []struct {
		name, stdout string
		exit         int
		result       string
		stepFails    bool
	}{
		{"PASS with no document", "", 0, "absent", true},
		{"FAIL with no document", "", 1, "absent", true},
		{"truncated document", passDocument[:40], 0, "invalid", true},
		{"two documents", passDocument + passDocument, 0, "invalid", true},
		{"an array", `[]`, 0, "invalid", true},
		{"not JSON", "PASS\n", 0, "invalid", true},
		{"invalid after an operational error", "{", 3, "invalid", true},
		{"oversized", oversized, 0, "oversized", true},
		{"oversized after a FAIL", oversized, 1, "oversized", true},
		{"exactly at the bound", exactlyAtLimit, 0, "present", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["FAKE_EXIT"] = fmt.Sprint(tt.exit)
			if tt.stdout != "" {
				e.vars["FAKE_STDOUT"] = stdoutFile(t, tt.stdout)
			}
			run := runActionScript(t, e, "run.sh", t.TempDir())
			if got := run.code != 0; got != tt.stepFails {
				t.Fatalf("run step exited %d, want failure %v\n%s", run.code, tt.stepFails, run.stdout)
			}
			if run.outputs["result"] != tt.result || run.outputs["exit-code"] != fmt.Sprint(tt.exit) {
				t.Errorf("outputs %v", run.outputs)
			}
			dir := run.outputs["artifact-dir"]
			m := readMetadata(t, dir)
			if m.Result.Status != tt.result || m.CLI.ExitCode != tt.exit {
				t.Errorf("metadata %+v", m)
			}
			_, err := os.Stat(filepath.Join(dir, "result.json"))
			if (err == nil) != (tt.result == "present") {
				t.Errorf("result.json present %v, want %v", err == nil, tt.result == "present")
			}
			if tt.stepFails && !strings.Contains(run.stdout, "::error title=trustvian-run::") {
				t.Errorf("failure not reported:\n%s", run.stdout)
			}
			// The CLI's code still passes through exactly.
			if done := finish(t, e, run, "success", "1"); done.code != tt.exit {
				t.Errorf("finish exited %d, want %d", done.code, tt.exit)
			}
		})
	}
}

// The head commit is the pull request's, from the event payload — never
// github.sha, which on pull_request is the synthetic merge commit.
func TestRunActionNamesTheHeadFromTheEvent(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name, event, payload, sha string
		wantSHA, wantSource       string
	}{
		{"pull request head, not the merge commit", "pull_request",
			`{"pull_request":{"head":{"sha":"` + headSHA + `"},"merge_commit_sha":"` + mergeSHA + `"}}`,
			mergeSHA, headSHA, "event.pull_request.head.sha"},
		{"push uses github.sha", "push", `{"after":"` + headSHA + `"}`, mergeSHA, mergeSHA, "github.sha"},
		{"pull request without a head", "pull_request", `{"pull_request":{}}`, mergeSHA, "", ""},
		{"pull request with a short head", "pull_request", `{"pull_request":{"head":{"sha":"abc123"}}}`, mergeSHA, "", ""},
		{"pull request with a non-string head", "pull_request", `{"pull_request":{"head":{"sha":7}}}`, mergeSHA, "", ""},
		{"pull request with an injected head", "pull_request",
			`{"pull_request":{"head":{"sha":"` + headSHA + `\nresult=present"}}}`, mergeSHA, "", ""},
		{"push without github.sha", "push", `{}`, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["GITHUB_EVENT_NAME"] = tt.event
			write(t, e.vars["GITHUB_EVENT_PATH"], tt.payload)
			e.vars["GITHUB_SHA"] = tt.sha
			e.vars["FAKE_STDOUT"] = stdoutFile(t, passDocument)
			run := runActionScript(t, e, "run.sh", t.TempDir())
			if tt.wantSHA == "" {
				if run.code == 0 || !strings.Contains(run.stdout, "cannot name the head commit") {
					t.Fatalf("exit %d, want a head error\n%s", run.code, run.stdout)
				}
				if _, err := os.Stat(f.invoked); err == nil {
					t.Error("the CLI ran without a head commit")
				}
				return
			}
			if run.code != 0 {
				t.Fatalf("run step exited %d\n%s", run.code, run.stdout)
			}
			if run.outputs["head-sha"] != tt.wantSHA {
				t.Errorf("head-sha %q, want %q", run.outputs["head-sha"], tt.wantSHA)
			}
			m := readMetadata(t, run.outputs["artifact-dir"])
			if m.Head.SHA != tt.wantSHA || m.Head.Source != tt.wantSource || m.Event != tt.event {
				t.Errorf("metadata head %+v event %q", m.Head, m.Event)
			}
			done := finish(t, e, run, "success", "5")
			summary := readFile(t, e.summary)
			if !strings.Contains(summary, "`"+tt.wantSHA+"`") || strings.Contains(summary, mergeSHA) && tt.wantSHA != mergeSHA {
				t.Errorf("summary does not name exactly the head commit:\n%s", summary)
			}
			if done.code != 0 {
				t.Errorf("finish exited %d", done.code)
			}
		})
	}
}

// The action executes the workload, so it refuses the events that run with
// base-repository privileges, in every step that could run anything.
func TestRunActionRefusesPrivilegedEvents(t *testing.T) {
	requireTools(t)
	for _, event := range []string{"pull_request_target", "workflow_run"} {
		for _, script := range []string{"setup.sh", "run.sh"} {
			t.Run(event+"/"+script, func(t *testing.T) {
				f := newFakeRuntime(t)
				e := newActionEnv(t, f)
				e.vars["GITHUB_EVENT_NAME"] = event
				run := runActionScript(t, e, script, t.TempDir())
				if run.code == 0 || !strings.Contains(run.stdout, "refusing to run on "+event) {
					t.Fatalf("exit %d, want a refusal\n%s", run.code, run.stdout)
				}
				if _, err := os.Stat(f.invoked); err == nil {
					t.Error("the CLI ran")
				}
				entries, _ := os.ReadDir(e.temp)
				if len(entries) != 0 {
					t.Errorf("%s wrote %d entries to RUNNER_TEMP before refusing", script, len(entries))
				}
			})
		}
	}
}

// The control plane the action started is stopped — after a PASS, after a
// failure of the action itself, and when it ignores SIGTERM — and no other
// process is touched.
func TestRunActionStopsOnlyTheControlPlaneItStarted(t *testing.T) {
	requireTools(t)
	if testing.Short() {
		t.Skip("includes the 10s SIGTERM grace")
	}
	tests := []struct {
		name     string
		vars     map[string]string
		stepFail bool
	}{
		{"after a run", map[string]string{"FAKE_STDOUT": "pass"}, false},
		{"after a run with no document", map[string]string{"FAKE_EXIT": "0"}, true},
		{"when it publishes a non-loopback endpoint", map[string]string{"FAKE_CP_URL": "http://10.0.0.1:80"}, true},
		{"when it ignores SIGTERM", map[string]string{"FAKE_STDOUT": "pass", "FAKE_CP_IGNORE_TERM": "1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A process the action did not start, which must survive it.
			decoy := exec.Command("sleep", "60")
			if err := decoy.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = decoy.Process.Kill(); _, _ = decoy.Process.Wait() })

			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			for k, v := range tt.vars {
				if k == "FAKE_STDOUT" {
					v = stdoutFile(t, passDocument)
				}
				e.vars[k] = v
			}
			start := time.Now()
			done := make(chan actionResult, 1)
			go func() { done <- runActionScript(t, e, "run.sh", t.TempDir()) }()
			var run actionResult
			select {
			case run = <-done:
			case <-time.After(90 * time.Second):
				t.Fatal("run step did not finish")
			}
			if (run.code != 0) != tt.stepFail {
				t.Fatalf("run step exited %d after %s\n%s", run.code, time.Since(start), run.stdout)
			}
			pid := readPID(t, f.cpPID)
			if alive(pid) {
				t.Errorf("control plane %d is still running", pid)
			}
			if !alive(decoy.Process.Pid) {
				t.Error("a process the action did not start was stopped")
			}
		})
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	if _, err := fmt.Sscan(readFile(t, path), &pid); err != nil {
		t.Fatalf("no control plane pid: %v", err)
	}
	return pid
}

// alive reports whether a process exists and has not been reaped or become a
// zombie of an unrelated parent.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The summary carries the head commit, the run link, the exit code and the
// artifact's availability — and no verdict.
func TestRunActionSummaryIsMinimal(t *testing.T) {
	requireTools(t)
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	e.vars["FAKE_EXIT"] = "1"
	e.vars["FAKE_STDOUT"] = stdoutFile(t, failDocument)
	run := runActionScript(t, e, "run.sh", t.TempDir())
	finish(t, e, run, "success", "42")
	summary := readFile(t, e.summary)
	for _, want := range []string{
		"`" + headSHA + "`",
		"[123456](https://github.com/trustvian/trustvian/actions/runs/123456)",
		"| `trustvian` exit code | `1` |",
		"uploaded as `trustvian-run` — result document and execution metadata",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	for _, verdict := range []string{"PASS", "FAIL", "pass", "fail", "verdict\":", "login"} {
		if strings.Contains(summary, verdict) {
			t.Errorf("summary carries %q, which is the renderer's to publish:\n%s", verdict, summary)
		}
	}
}

// Values the finish step did not validate never reach the summary.
func TestRunActionSummaryRefusesMalformedValues(t *testing.T) {
	requireTools(t)
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	e.vars["GITHUB_SERVER_URL"] = "javascript:alert(1)//"
	e.vars["GITHUB_RUN_ID"] = "1) [x](http://evil"
	e.vars["INPUT_ARTIFACT_NAME"] = "x`|<img src=x>@team"
	run := actionResult{outputs: map[string]string{
		"exit-code": "1", "head-sha": "abc | forged | row", "result": "present\n| pass |",
	}}
	finish(t, e, run, "success", "7")
	summary := readFile(t, e.summary)
	for _, bad := range []string{"javascript", "evil", "<img", "@team", "forged", "| pass |"} {
		if strings.Contains(summary, bad) {
			t.Errorf("summary carries %q:\n%s", bad, summary)
		}
	}
	if strings.Count(summary, "\n|") != 6 {
		t.Errorf("summary table has an unexpected number of rows:\n%s", summary)
	}
}

// md_inert leaves nothing active in a hostile string, and bounds it.
func TestMarkdownInert(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name, in string
		absent   []string
	}{
		{"link", "[click](http://evil.example)", []string{"[click]", "](http"}},
		{"mention", "@org/security-team", []string{" @org", "\x00"}},
		// An escaped "\<" renders a literal "<", so no tag can open.
		{"html", "<img src=x onerror=alert(1)>", []string{" <img", "\x00"}},
		{"code span", "a`b`c", []string{"a`b"}},
		{"table row", "x | 0/5 | 5/5 | pass |\n| forged | row |", []string{"\n", " | "}},
		{"control characters", "a\x1b[31mb\rc\td", []string{"\x1b", "\r", "\t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mdInert(t, tt.in, 256)
			for _, s := range tt.absent {
				if strings.Contains(got, s) {
					t.Errorf("md_inert(%q) = %q still contains %q", tt.in, got, s)
				}
			}
			// Every ASCII punctuation character is escaped.
			if regexp.MustCompile(`(^|[^\\])[][!"#$%&'()*+,./:;<=>?@^_{|}~` + "`" + `-]`).MatchString(
				strings.ReplaceAll(got, `\\`, "")) {
				t.Errorf("md_inert(%q) = %q has unescaped punctuation", tt.in, got)
			}
		})
	}

	long := mdInert(t, strings.Repeat("é", 5000), 256)
	if !strings.HasSuffix(long, `… \(truncated\)`) {
		t.Errorf("truncation is not stated: %q", long[len(long)-40:])
	}
	if body := strings.TrimSuffix(long, ` … \(truncated\)`); len(body) > 256 || !utf8Valid(body) {
		t.Errorf("truncated to %d bytes (valid UTF-8 %v), want at most 256 and valid", len(body), utf8Valid(body))
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }

func mdInert(t *testing.T, value string, max int) string {
	t.Helper()
	lib, err := filepath.Abs(filepath.Join(actionDir, "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1"; md_inert "$2" "$3"`, "bash", lib, value, fmt.Sprint(max))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("md_inert: %v", err)
	}
	return string(out)
}

// The pin the action builds is well-formed, and malformed pins are refused
// before anything is fetched.
func TestRunActionRuntimePin(t *testing.T) {
	requireTools(t)
	lib, err := filepath.Abs(filepath.Join(actionDir, "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(path string) (string, error) {
		out, err := exec.Command("bash", "-c",
			`set -e; . "$1"; load_runtime_pin "$2"; echo "$TRUSTVIAN_SOURCE_COMMIT $GO_VERSION"`,
			"bash", lib, path).CombinedOutput()
		return string(out), err
	}
	pin, err := filepath.Abs(filepath.Join(actionDir, "runtime.env"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := load(pin); err != nil || strings.TrimSpace(out) != runtimePin+" 1.27.1" {
		t.Fatalf("the shipped pin: %q %v", out, err)
	}
	good := readFile(t, pin)
	tests := map[string]string{
		"short commit":     strings.Replace(good, runtimePin, "5521759", 1),
		"branch name":      strings.Replace(good, runtimePin, "main", 1),
		"floating go":      strings.Replace(good, "GO_VERSION=1.27.1", "GO_VERSION=1.27", 1),
		"unknown key":      good + "GOFLAGS=-insecure\n",
		"duplicate key":    good + "GO_VERSION=1.27.1\n",
		"missing digest":   regexp.MustCompile(`(?m)^GO_SHA256_LINUX_AMD64=.*\n`).ReplaceAllString(good, ""),
		"non-github":       strings.Replace(good, "https://github.com/trustvian/trustvian.git", "http://evil.example/t.git", 1),
		"shell expression": strings.Replace(good, runtimePin, "$(touch /tmp/x)", 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.env")
			write(t, path, body)
			if out, err := load(path); err == nil || !strings.Contains(out, "runtime pin") {
				t.Errorf("accepted: %q %v", out, err)
			}
		})
	}
}

// --- The action and its workflows, structurally ---------------------------

var pinnedUse = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)

func loadYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc
}

func TestRunActionDefinition(t *testing.T) {
	action := loadYAML(t, filepath.Join(actionDir, "action.yml"))
	runs, _ := action["runs"].(map[string]any)
	if runs["using"] != "composite" {
		t.Fatalf("runs.using %v, want composite", runs["using"])
	}
	steps, _ := runs["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("no steps")
	}
	var ids []string
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		ids = append(ids, fmt.Sprint(step["id"]))
		if _, ok := step["continue-on-error"]; ok {
			t.Errorf("step %v sets continue-on-error", step["id"])
		}
		if use, ok := step["uses"].(string); ok && !pinnedUse.MatchString(use) {
			t.Errorf("step %v uses %q, not pinned to a full commit", step["id"], use)
		}
		if script, ok := step["run"].(string); ok && strings.Contains(script, "${{") {
			t.Errorf("step %v splices an expression into its script: %q", step["id"], script)
		}
	}
	if want := []string{"setup", "run", "upload", "finish"}; !slices.Equal(ids, want) {
		t.Errorf("steps %v, want %v", ids, want)
	}
	inputs, _ := action["inputs"].(map[string]any)
	for name, raw := range inputs {
		input, _ := raw.(map[string]any)
		if input["required"] == true {
			t.Errorf("input %s is required; the CLI, not the action, decides what is missing", name)
		}
		for _, forbidden := range []string{"max", "limit", "threshold", "presence", "verdict", "github-token", "token"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("input %s looks like gate configuration or a credential", name)
			}
		}
	}
}

// The action computes nothing: no script reads a verdict, a count, or a
// limit out of the result document.
func TestRunActionComputesNothing(t *testing.T) {
	for _, name := range []string{"lib.sh", "setup.sh", "run.sh", "finish.sh"} {
		body := readFile(t, filepath.Join(actionDir, name))
		for _, forbidden := range []string{
			".verdict", ".gate", ".comparison", ".behaviors", ".checks", "runs_present",
			"max_repeated", "classification", ".members", ".summary",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s reads %q from the result document", name, forbidden)
			}
		}
	}
}

// triggers returns the event names a workflow's `on:` names.
func triggers(doc map[string]any) []string {
	// yaml.v3 decodes a bare `on` key as the string "on", not the boolean.
	switch on := doc["on"].(type) {
	case string:
		return []string{on}
	case []any:
		var names []string
		for _, n := range on {
			names = append(names, fmt.Sprint(n))
		}
		return names
	case map[string]any:
		var names []string
		for n := range on {
			names = append(names, n)
		}
		return names
	}
	return nil
}

// The workflow, example and documentation scans live in
// trustvian_ci_workflows_test.go, which covers both actions.
