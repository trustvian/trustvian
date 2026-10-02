//go:build !windows

// Regression tests for the trustvian-run action's isolation from the job it
// runs in: the consumer's Go and Git environment, CDPATH and odd directory
// names, cancellation signals, and an unwritable job summary. Each reproduces
// a finding from the review of pull request #138.
package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func libPath(t *testing.T) string {
	t.Helper()
	lib, err := filepath.Abs(filepath.Join(actionDir, "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func requireGitAndGo(t *testing.T) string {
	t.Helper()
	requireTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	// The real toolchain binary, not a symlink to it: go_isolated derives
	// GOROOT from the binary's location.
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go is required: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "bin", "go")
}

// gitCmd runs git for a test fixture, with the test's own identity and no
// inherited configuration.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// consumerRepo is a repository standing in for the workload's checkout:
// tracked files, an untracked file, and one commit.
func consumerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	write(t, filepath.Join(dir, "app.py"), "print('the application')\n")
	write(t, filepath.Join(dir, "README.md"), "consumer\n")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-q", "-m", "consumer")
	write(t, filepath.Join(dir, "untracked.txt"), "not committed\n")
	return dir
}

// snapshot records every file under dir, .git included, by path, mode and
// content digest. Two equal snapshots mean nothing was written there.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			files[rel] = "dir " + info.Mode().String()
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		files[rel] = info.Mode().String() + " " + hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertSnapshotUnchanged(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for path, was := range before {
		if now, ok := after[path]; !ok {
			t.Errorf("%s: %s was removed", what, path)
		} else if now != was {
			t.Errorf("%s: %s changed", what, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s: %s was added", what, path)
		}
	}
}

// hostileGitEnv points every repository and configuration override Git
// honours at the consumer, and adds a rewrite that would send a fetch
// nowhere and a hook that would leave a canary.
func hostileGitEnv(t *testing.T, consumer, origin, canary string) []string {
	t.Helper()
	hooks := t.TempDir()
	write(t, filepath.Join(hooks, "post-checkout"), "#!/bin/sh\ntouch '"+canary+"'\n")
	home := t.TempDir()
	write(t, filepath.Join(home, ".gitconfig"),
		"[url \"/nonexistent/home-rewrite\"]\n\tinsteadOf = "+origin+"\n[core]\n\thooksPath = "+hooks+"\n")
	cdpath := t.TempDir()
	return []string{
		"GIT_DIR=" + filepath.Join(consumer, ".git"),
		"GIT_WORK_TREE=" + consumer,
		"GIT_INDEX_FILE=" + filepath.Join(consumer, ".git", "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(consumer, ".git", "objects"),
		"GIT_COMMON_DIR=" + filepath.Join(consumer, ".git"),
		"GIT_CONFIG_PARAMETERS='url./nonexistent/param-rewrite.insteadof'='" + origin + "'",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + hooks,
		"GIT_TEMPLATE_DIR=" + hooks,
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"CDPATH=" + cdpath,
	}
}

// Finding 2: a GIT_DIR or GIT_WORK_TREE the job set must not redirect the
// runtime's source checkout into the consumer's repository, and no inherited
// Git configuration may change what is fetched or run a hook.
func TestFetchSourceIsConfinedToItsCheckout(t *testing.T) {
	requireGitAndGo(t)
	origin := t.TempDir()
	gitCmd(t, origin, "init", "-q")
	write(t, filepath.Join(origin, "go.mod"), "module example.com/pinned\n")
	gitCmd(t, origin, "add", ".")
	gitCmd(t, origin, "commit", "-q", "-m", "pinned")
	pinned := gitCmd(t, origin, "rev-parse", "HEAD")
	write(t, filepath.Join(origin, "go.mod"), "module example.com/later\n")
	gitCmd(t, origin, "commit", "-q", "-am", "later")
	originURL := "file://" + origin

	consumer := consumerRepo(t)
	before := snapshot(t, consumer)
	canary := filepath.Join(t.TempDir(), "hook-ran")
	dest := filepath.Join(t.TempDir(), "source-"+pinned)

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$1"
fetch_source "$2" "$3" "$4"
source_is_pinned "$4" "$3"`, "bash", libPath(t), originURL, pinned, dest)
	cmd.Dir = consumer // the worst case: the step runs in the consumer's checkout
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, hostileGitEnv(t, consumer, originURL, canary)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch under a hostile Git environment: %v\n%s", err, out)
	}

	if got := gitCmd(t, dest, "rev-parse", "HEAD"); got != pinned {
		t.Errorf("checkout is at %s, want %s", got, pinned)
	}
	if got := readFile(t, filepath.Join(dest, "go.mod")); got != "module example.com/pinned\n" {
		t.Errorf("checkout holds %q", got)
	}
	assertSnapshotUnchanged(t, "consumer repository", before, snapshot(t, consumer))
	if _, err := os.Stat(canary); err == nil {
		t.Error("an inherited hook ran")
	}

	// And a checkout that is not exactly the pin is never accepted: a
	// modified file, and an untracked one.
	write(t, filepath.Join(dest, "go.mod"), "module example.com/tampered\n")
	if out, err := exec.Command("bash", "-c", `. "$1"; source_is_pinned "$2" "$3"`,
		"bash", libPath(t), dest, pinned).CombinedOutput(); err == nil {
		t.Errorf("a modified checkout was accepted as pinned: %s", out)
	}
	gitCmd(t, dest, "checkout", "-q", "--", "go.mod")
	write(t, filepath.Join(dest, "extra.go"), "package extra\n")
	if out, err := exec.Command("bash", "-c", `. "$1"; source_is_pinned "$2" "$3"`,
		"bash", libPath(t), dest, pinned).CombinedOutput(); err == nil {
		t.Errorf("a checkout with an untracked file was accepted as pinned: %s", out)
	}
}

// Finding 1: every Go invocation ignores the job's Go configuration —
// including GOTOOLCHAIN=<other>+path, which made even `go env` look for a
// toolchain that was not there — while the build still records genuine VCS
// information.
func TestGoIsolatedIgnoresTheJobsGoEnvironment(t *testing.T) {
	goBin := requireGitAndGo(t)
	tooling := t.TempDir()
	if err := os.Mkdir(filepath.Join(tooling, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := exec.Command(goBin, "env", "GOVERSION")
	clean.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GOTOOLCHAIN=local", "GOENV=off"}
	versionOut, err := clean.Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(versionOut))

	// A hostile working directory: a go.mod and go.work demanding a toolchain
	// that does not exist.
	hostileCwd := t.TempDir()
	write(t, filepath.Join(hostileCwd, "go.mod"), "module hostile\n\ngo 1.99.0\n\ntoolchain go1.99.0\n")
	write(t, filepath.Join(hostileCwd, "go.work"), "go 1.99.0\n\nuse .\n")
	goenv := filepath.Join(t.TempDir(), "go.env")
	write(t, goenv, "GOTOOLCHAIN=go1.99.0+path\nGOFLAGS=-toolexec=/bin/false\n")
	consumer := consumerRepo(t)
	hostile := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOTOOLCHAIN=go1.99.0+path",
		"GOFLAGS=-mod=vendor -toolexec=/bin/false",
		"GOENV=" + goenv,
		"GOROOT=/nonexistent/goroot",
		"GOPATH=/nonexistent/gopath",
		"GOWORK=" + filepath.Join(hostileCwd, "go.work"),
		"GOPROXY=off",
		"GOSUMDB=off",
		"GONOSUMDB=*",
		"GOINSECURE=*",
		"GOEXPERIMENT=nosuchexperiment",
		"CGO_ENABLED=1",
		"CDPATH=" + t.TempDir(),
		"GIT_DIR=" + filepath.Join(consumer, ".git"),
		"GIT_WORK_TREE=" + consumer,
	}
	goIsolated := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{"-c", `set -euo pipefail; . "$1"
GO_ISOLATED_BIN="$2" GO_ISOLATED_TOOLING="$3"
dir="$4"; shift 4
go_isolated "$dir" "$@"`, "bash", libPath(t), goBin, tooling, dir}, args...)...)
		cmd.Dir = hostileCwd
		cmd.Env = hostile
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	got, err := goIsolated(tooling, "env", "GOVERSION", "GOTOOLCHAIN", "GOFLAGS", "GOWORK",
		"GOPROXY", "GOSUMDB", "GONOSUMDB", "GOINSECURE", "GOENV", "CGO_ENABLED", "GOEXPERIMENT")
	if err != nil {
		t.Fatalf("go env under a hostile environment: %v\n%s", err, got)
	}
	// GOENV=off reports as empty; GOEXPERIMENT is unset.
	want := strings.TrimSpace(strings.Join([]string{version, "local", "-trimpath -mod=readonly", "off",
		"https://proxy.golang.org,direct", "sum.golang.org", "", "", "", "0", ""}, "\n"))
	if got != want {
		t.Errorf("go env\n got %q\nwant %q", got, want)
	}

	// A build from a pinned checkout, under the same environment, records
	// that checkout's commit — not the consumer's, which GIT_DIR names.
	module := t.TempDir()
	gitCmd(t, module, "init", "-q")
	write(t, filepath.Join(module, "go.mod"), "module example.com/pinned\n\ngo 1.21\n")
	write(t, filepath.Join(module, "main.go"), "package main\n\nfunc main() {}\n")
	gitCmd(t, module, "add", ".")
	gitCmd(t, module, "commit", "-q", "-m", "pinned")
	commit := gitCmd(t, module, "rev-parse", "HEAD")
	binary := filepath.Join(t.TempDir(), "pinned")
	if out, err := goIsolated(module, "build", "-o", binary, "."); err != nil {
		t.Fatalf("build under a hostile environment: %v\n%s", err, out)
	}
	info, err := goIsolated(tooling, "version", "-m", binary)
	if err != nil {
		t.Fatalf("version -m: %v\n%s", err, info)
	}
	for _, line := range []string{"vcs.revision=" + commit, "vcs.modified=false", "-trimpath=true", "CGO_ENABLED=0"} {
		if !strings.Contains(info, line) {
			t.Errorf("build information lacks %s:\n%s", line, info)
		}
	}
}

// Every Go and Git command setup.sh runs goes through the isolated helpers.
func TestSetupRunsNoUnisolatedGoOrGit(t *testing.T) {
	body := readFile(t, filepath.Join(actionDir, "setup.sh"))
	// A command in command position: at the start of a line, or after ;, &,
	// |, (, ! or $( — not a word such as `for tool in git jq`.
	direct := regexp.MustCompile(`(?m)(^[ \t]*|[;&|(][ \t]*|![ \t]+|\$\([ \t]*)(git|"\$GO_ISOLATED_BIN"|"\$go_bin"|go)[ \t]+[a-z-]`)
	for _, m := range direct.FindAllString(body, -1) {
		t.Errorf("setup.sh runs a command outside go_isolated/git_isolated: %q", strings.TrimSpace(m))
	}
	for _, helper := range []string{"go_isolated", "fetch_source", "source_is_pinned"} {
		if !strings.Contains(body, helper) {
			t.Errorf("setup.sh does not use %s", helper)
		}
	}
}

// Finding 3: working-directory is a literal path. A directory named -P is a
// directory, CDPATH is never consulted, spaces survive, and nothing a cd
// prints reaches the captured result.
func TestRunActionWorkingDirectoryIsLiteral(t *testing.T) {
	requireTools(t)
	tests := []struct {
		name, input string
		absolute    bool
	}{
		{"a directory named -P", "-P", false},
		{"a directory named --", "--", false},
		{"a relative directory shadowed under CDPATH", "app", false},
		{"a relative path with spaces", "my workloads/support agent", false},
		{"an absolute path with spaces", "abs dir/with spaces", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			work := t.TempDir()
			target := filepath.Join(work, tt.input)
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			// A same-named directory elsewhere, offered through CDPATH.
			cdpath := t.TempDir()
			if err := os.MkdirAll(filepath.Join(cdpath, tt.input), 0o755); err != nil {
				t.Fatal(err)
			}
			e.vars["CDPATH"] = cdpath
			e.vars["INPUT_WORKING_DIRECTORY"] = tt.input
			if tt.absolute {
				e.vars["INPUT_WORKING_DIRECTORY"] = target
			}
			e.vars["FAKE_STDOUT"] = stdoutFile(t, passDocument)
			run := runActionScript(t, e, "run.sh", work)
			if run.code != 0 {
				t.Fatalf("run step exited %d\n%s%s", run.code, run.stdout, run.stderr)
			}
			want, err := filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSuffix(readFile(t, f.pwd), "\n"); got != want {
				t.Errorf("the CLI ran in %q, want %q", got, want)
			}
			if got := readFile(t, filepath.Join(run.outputs["artifact-dir"], "result.json")); got != passDocument {
				t.Errorf("result.json is %q, want only the CLI's output", got)
			}
		})
	}
}

// signalAwareCLI replaces the fake CLI with one that records the signals it
// receives, writes a readiness marker once its traps are installed, and exits
// 3 — as a cancelled suite does — after recording one.
func signalAwareCLI(t *testing.T, f fakeRuntime) {
	t.Helper()
	write(t, filepath.Join(f.bin, "trustvian"), `#!/usr/bin/env bash
echo $$ > "$FAKE_CLI_PID"
trap 'echo INT >> "$FAKE_SIGNALS"; exit 3' INT
trap 'echo TERM >> "$FAKE_SIGNALS"; exit 3' TERM
touch "$FAKE_READY"
while :; do sleep 0.05; done
`)
}

// stepWrapper writes the script the runner executes for the action's run
// step — that step's own `run:` text, read from action.yml — so the test
// exercises the real wrapper structure.
func stepWrapper(t *testing.T) string {
	t.Helper()
	var action struct {
		Runs struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(actionDir, "action.yml"))), &action); err != nil {
		t.Fatal(err)
	}
	for _, step := range action.Runs.Steps {
		if step.ID == "run" {
			path := filepath.Join(t.TempDir(), "step.sh")
			write(t, path, step.Run+"\n")
			return path
		}
	}
	t.Fatal("action.yml has no run step")
	return ""
}

func waitForFile(t *testing.T, path string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s", path, limit)
}

// Finding 4: SIGINT and SIGTERM reach the CLI promptly — sent to the script
// directly, or to the step's own process as the runner sends them — and the
// step then stops the control plane it started, exits with the signal's
// status, and leaves every process it did not start running.
func TestRunActionForwardsCancellation(t *testing.T) {
	requireTools(t)
	type launch struct {
		name string
		argv func(t *testing.T) []string
	}
	direct := launch{"direct", func(t *testing.T) []string {
		abs, _ := filepath.Abs(filepath.Join(actionDir, "run.sh"))
		return []string{"bash", abs}
	}}
	// How GitHub runs a composite `shell: bash` step.
	wrapper := launch{"step wrapper", func(t *testing.T) []string {
		return []string{"bash", "--noprofile", "--norc", "-eo", "pipefail", stepWrapper(t)}
	}}
	tests := []struct {
		launch   launch
		signal   syscall.Signal
		name     string
		want     int
		attached bool
	}{
		{direct, syscall.SIGINT, "INT", 130, false},
		{direct, syscall.SIGTERM, "TERM", 143, false},
		{wrapper, syscall.SIGINT, "INT", 130, false},
		{wrapper, syscall.SIGTERM, "TERM", 143, false},
		{wrapper, syscall.SIGINT, "INT", 130, true},
		{wrapper, syscall.SIGTERM, "TERM", 143, true},
	}
	for _, tt := range tests {
		mode := "started control plane"
		if tt.attached {
			mode = "attached control plane"
		}
		t.Run(tt.launch.name+"/SIG"+tt.name+"/"+mode, func(t *testing.T) {
			decoy := exec.Command("sleep", "60")
			if err := decoy.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = decoy.Process.Kill(); _, _ = decoy.Process.Wait() })

			f := newFakeRuntime(t)
			signalAwareCLI(t, f)
			e := newActionEnv(t, f)
			dir := t.TempDir()
			cliPID := filepath.Join(dir, "cli.pid")
			signals := filepath.Join(dir, "signals")
			ready := filepath.Join(dir, "ready")
			e.vars["FAKE_CLI_PID"] = cliPID
			e.vars["FAKE_SIGNALS"] = signals
			e.vars["FAKE_READY"] = ready
			abs, _ := filepath.Abs(actionDir)
			e.vars["GITHUB_ACTION_PATH"] = abs

			var external *exec.Cmd
			if tt.attached {
				// A control plane someone else runs: the action must not touch it.
				external = exec.Command("sleep", "60")
				if err := external.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = external.Process.Kill(); _, _ = external.Process.Wait() })
				e.vars["INPUT_API_URL"] = "http://127.0.0.1:1"
			}

			if err := os.WriteFile(e.output, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			argv := tt.launch.argv(t)
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = t.TempDir()
			for k, v := range e.vars {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })

			waitForFile(t, ready, 20*time.Second)
			if err := cmd.Process.Signal(tt.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-time.After(20 * time.Second):
				t.Fatalf("the step did not exit within 20s of SIG%s", tt.name)
			}
			if got := cmd.ProcessState.ExitCode(); got != tt.want {
				t.Errorf("step exited %d, want %d (SIG%s)", got, tt.want, tt.name)
			}
			if got := readFile(t, signals); got != tt.name+"\n" {
				t.Errorf("the CLI received %q, want exactly one SIG%s", got, tt.name)
			}
			if pid := readPID(t, cliPID); alive(pid) {
				t.Errorf("the CLI (%d) is still running", pid)
			}
			if tt.attached {
				if _, err := os.Stat(f.cpPID); err == nil {
					t.Error("a control plane was started although api-url was given")
				}
				if !alive(external.Process.Pid) {
					t.Error("the externally managed control plane was stopped")
				}
			} else if pid := readPID(t, f.cpPID); alive(pid) {
				t.Errorf("the control plane the step started (%d) is still running", pid)
			}
			if !alive(decoy.Process.Pid) {
				t.Error("an unrelated process was stopped")
			}
			if strings.Contains(readFile(t, e.output), "exit-code=") {
				t.Error("a cancelled run reported a CLI exit code")
			}
		})
	}
}

// A cancellation that arrives after the CLI has finished, while the step is
// stopping its control plane, neither abandons nor restarts that shutdown: the
// control plane gets SIGTERM once, then SIGKILL at the deadline, and is gone
// before the step exits with the first signal's status. A second signal
// changes none of that, and nothing the step did not start is touched.
func TestRunActionCancellationDuringShutdownStillStopsTheControlPlane(t *testing.T) {
	requireTools(t)
	if testing.Short() {
		t.Skip("includes the 10s SIGTERM grace")
	}
	tests := []struct {
		name    string
		signals []syscall.Signal
		want    int
	}{
		{"SIGINT", []syscall.Signal{syscall.SIGINT}, 130},
		{"SIGTERM", []syscall.Signal{syscall.SIGTERM}, 143},
		{"SIGINT twice", []syscall.Signal{syscall.SIGINT, syscall.SIGINT}, 130},
		{"SIGTERM twice", []syscall.Signal{syscall.SIGTERM, syscall.SIGTERM}, 143},
		{"SIGINT then SIGTERM", []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}, 130},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // each case spends the 10s grace
			decoy := exec.Command("sleep", "60")
			if err := decoy.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = decoy.Process.Kill(); _, _ = decoy.Process.Wait() })

			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			// The CLI finishes normally, with a valid document; the control
			// plane records SIGTERM and keeps running.
			e.vars["FAKE_STDOUT"] = stdoutFile(t, passDocument)
			termMarker := filepath.Join(t.TempDir(), "control-plane-term")
			e.vars["FAKE_CP_TERM_MARKER"] = termMarker
			abs, _ := filepath.Abs(actionDir)
			e.vars["GITHUB_ACTION_PATH"] = abs
			if err := os.WriteFile(e.output, nil, 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", stepWrapper(t))
			cmd.Dir = t.TempDir()
			for k, v := range e.vars {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			logPath := filepath.Join(t.TempDir(), "step.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			cmd.Stdout = logFile
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			t.Cleanup(func() {
				// Never leave the control plane behind, whatever the outcome.
				var pid int
				if raw, err := os.ReadFile(f.cpPID); err == nil {
					if _, err := fmt.Sscan(string(raw), &pid); err == nil && pid > 0 {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})

			// The step is now inside its SIGTERM grace, waiting for a control
			// plane that will not stop on its own.
			waitForFile(t, termMarker, 20*time.Second)
			cpPID := readPID(t, f.cpPID)
			// Each signal is sent once the step has acknowledged the one
			// before, so a repeated signal is two deliveries, not one.
			for i, sig := range tt.signals {
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				waitForLog(t, logPath, "while the control plane is stopping", i+1, 20*time.Second)
			}
			select {
			case <-exited:
			case <-time.After(30 * time.Second):
				t.Fatal("the step did not exit within 30s of the cancellation")
			}

			log := readFile(t, logPath)
			if got := cmd.ProcessState.ExitCode(); got != tt.want {
				t.Errorf("step exited %d, want %d\n%s", got, tt.want, log)
			}
			if got := readFile(t, termMarker); got != "TERM\n" {
				t.Errorf("the control plane received %q, want exactly one SIGTERM", got)
			}
			if !strings.Contains(log, "did not stop within 10s of SIGTERM; killing it") {
				t.Errorf("the SIGKILL fallback did not run:\n%s", log)
			}
			// Gone, not a zombie: the step reaped it. Its exit was waited on
			// above, so this is bounded only by the kernel's own bookkeeping.
			deadline := time.Now().Add(5 * time.Second)
			for syscall.Kill(cpPID, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if err := syscall.Kill(cpPID, 0); err == nil {
				t.Errorf("the control plane the step started (%d) is still running or unreaped", cpPID)
			}
			if !alive(decoy.Process.Pid) {
				t.Error("an unrelated process was stopped")
			}
			if strings.Contains(readFile(t, e.output), "exit-code=") {
				t.Error("a cancelled run reported a CLI exit code")
			}
		})
	}
}

// waitForLog waits until path holds at least n occurrences of text.
func waitForLog(t *testing.T, path, text string, n int, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.Count(string(raw), text) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not log %q %d times within %s:\n%s", path, text, n, limit, readFile(t, path))
}

// Finding 5: an unwritable job summary is reported, and the CLI's code —
// every one of them — still passes through. A CLI that never ran still fails.
func TestRunActionFinishSurvivesAnUnwritableSummary(t *testing.T) {
	requireTools(t)
	for _, code := range []string{"0", "1", "2", "3", ""} {
		name := "exit " + code
		if code == "" {
			name = "the CLI did not run"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t)
			e := newActionEnv(t, f)
			e.vars["GITHUB_STEP_SUMMARY"] = t.TempDir() // a directory: every write fails
			run := actionResult{outputs: map[string]string{"exit-code": code, "head-sha": headSHA, "result": "present"}}
			done := finish(t, e, run, "success", "9")
			want := 1
			if code != "" {
				fmt.Sscan(code, &want)
			}
			if done.code != want {
				t.Errorf("finish exited %d, want %d\n%s%s", done.code, want, done.stdout, done.stderr)
			}
			if !strings.Contains(done.stdout, "::warning title=trustvian-run::the job summary could not be written") {
				t.Errorf("the summary failure was not reported:\n%s", done.stdout)
			}
		})
	}
	// An upload failure stays visible when the summary also fails.
	f := newFakeRuntime(t)
	e := newActionEnv(t, f)
	e.vars["GITHUB_STEP_SUMMARY"] = t.TempDir()
	done := finish(t, e, actionResult{outputs: map[string]string{"exit-code": "3", "head-sha": headSHA}}, "failure", "")
	if done.code != 3 || !strings.Contains(done.stdout, "::error title=trustvian-run::the result artifact was not uploaded") {
		t.Errorf("exit %d; upload failure not reported:\n%s", done.code, done.stdout)
	}
}
