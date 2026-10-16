//go:build !windows

// This file is the QuickStart check: docs/quickstart.md's own commands, parsed
// from the document and run in order in one shell, against freshly built
// binaries in a temporary directory.
//
// A walkthrough rots in a way no other test notices. A flag is renamed, an
// output line changes, the example's scenario gains a field, and the page
// still renders. Parsing the commands from the page, rather than keeping a
// second copy of them here, is what makes the check and the page unable to
// drift apart: change one command and the check runs the changed command.
//
// The document marks every shell block with an HTML comment on the line before
// it, which GitHub does not render:
//
//	<!-- quickstart-check: run -->          run it; it must exit 0
//	<!-- quickstart-check: background -->   run it, then wait for the control
//	                                        plane's discovery file
//	<!-- quickstart-check: skip <reason> --> do not run it, and say why
//	<!-- quickstart-check: expect -->       before a text block: every line of
//	                                        it, after normalizing the values
//	                                        that change per run, is a line of
//	                                        the previous command's output.
//	                                        Lines shortened with … are skipped
//	<!-- quickstart-check: file <name> -->  before a block: it is that file of
//	                                        examples/quickstart, byte for byte
//
// Running needs Python 3, git, curl, tar and network access for pip, so it is
// opt-in: `make quickstart-check` sets TRUSTVIAN_QUICKSTART_CHECK=1. Parsing,
// the file and download-list checks and the normalization run on every
// `go test ./scripts`.
//
// Five values in the commands differ on every machine, and are rewritten
// before the block runs, the way a reader adapts them:
//
//   - the release download URL becomes a file:// URL of an archive built from
//     this checkout, with its checksums.txt — unless
//     TRUSTVIAN_QUICKSTART_RELEASE=1, which downloads the published release;
//   - the raw example URL becomes a file:// URL of examples/quickstart;
//   - the archive name A= gets this machine's OS and architecture;
//   - the control plane's http://127.0.0.1:<port> becomes the one the
//     background block started;
//   - the execution id becomes the one `eval run` printed.
package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	quickstartDocPath = "../docs/quickstart.md"
	quickstartAgent   = "../examples/quickstart"
)

var (
	quickstartMarker = regexp.MustCompile(`^<!-- quickstart-check: (run|background|skip|expect|file)(?: (.+?))? -->$`)
	quickstartFence  = regexp.MustCompile("^```([a-z]*)$")

	quickstartReleaseURL = regexp.MustCompile(`https://github\.com/trustvian/trustvian/releases/download`)
	quickstartRawURL     = regexp.MustCompile(`https://raw\.githubusercontent\.com/trustvian/trustvian/[^/\s]+/examples/quickstart`)
	quickstartArchive    = regexp.MustCompile(`(?m)^A=trustvian_\$\{V\}_[a-z0-9]+_[a-z0-9]+`)
	quickstartVersion    = regexp.MustCompile(`(?m)^V=(v\d+\.\d+\.\d+)\b`)
	quickstartAPI        = regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	quickstartExecution  = regexp.MustCompile(`scn-quickstart-\d{8}T\d{6}-[0-9a-f]{8}`)
	quickstartDownloads  = regexp.MustCompile(`(?m)^for f in ([^;]+); do curl`)
)

// quickstartNormalizers rewrite the values that legitimately differ between
// the run the page was written from and any other: identifiers derived from a
// clock or a commit, ports, versions, timestamps and the dev state directory.
// Everything else — behaviors, counts, digests of the scenario file,
// fingerprints, verdicts — must match exactly.
var quickstartNormalizers = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{quickstartExecution, "<execution>"},
	{regexp.MustCompile(`dev-git-[0-9a-f]{7}-\d{8}T\d{6}\.\d{3}Z`), "<run>"},
	{regexp.MustCompile(`git:[0-9a-f]{7}`), "git:<sha>"},
	{regexp.MustCompile(`127\.0\.0\.1:\d+`), "127.0.0.1:<port>"},
	{regexp.MustCompile(`\S*\.trustvian/dev/[0-9a-f]+`), "<state>"},
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`), "<time>"},
	{regexp.MustCompile(`(v\d+\.\d+\.\d+|\(devel\))[^\s` + "`" + `]*( [0-9a-f]{40}[^\s` + "`" + `]*)?`), "<version>"},
}

type quickstartBlock struct {
	line   int    // the opening fence's line number, 1-based
	lang   string // the fence's info string
	mode   string // the marker before it, or "" when unmarked
	arg    string // the marker's argument
	body   string
	expect []quickstartBlock // the expect blocks that follow a run block
}

func parseQuickstart(doc string) ([]quickstartBlock, error) {
	var (
		blocks  []quickstartBlock
		pending *quickstartBlock
		current *quickstartBlock
		body    []string
	)
	for i, line := range strings.Split(doc, "\n") {
		if current != nil {
			if line == "```" {
				current.body = strings.Join(body, "\n") + "\n"
				blocks = append(blocks, *current)
				current, body = nil, nil
				continue
			}
			body = append(body, line)
			continue
		}
		if m := quickstartMarker.FindStringSubmatch(line); m != nil {
			if pending != nil {
				return nil, fmt.Errorf("line %d: marker %q has no block before the next marker", pending.line, pending.mode)
			}
			pending = &quickstartBlock{line: i + 1, mode: m[1], arg: m[2]}
			continue
		}
		if m := quickstartFence.FindStringSubmatch(line); m != nil {
			current = &quickstartBlock{line: i + 1, lang: m[1]}
			if pending != nil {
				current.mode, current.arg = pending.mode, pending.arg
				pending = nil
			}
			continue
		}
		if pending != nil && strings.TrimSpace(line) != "" {
			return nil, fmt.Errorf("line %d: marker %q is not directly before a block", pending.line, pending.mode)
		}
	}
	if current != nil {
		return nil, fmt.Errorf("line %d: unterminated code block", current.line)
	}
	if pending != nil {
		return nil, fmt.Errorf("line %d: marker %q has no block", pending.line, pending.mode)
	}

	// Attach each expect block to the command block before it.
	var out []quickstartBlock
	for _, b := range blocks {
		switch {
		case b.mode == "expect":
			if len(out) == 0 || (out[len(out)-1].mode != "run" && out[len(out)-1].mode != "background") {
				return nil, fmt.Errorf("line %d: an expect block must follow a run or background block", b.line)
			}
			out[len(out)-1].expect = append(out[len(out)-1].expect, b)
		case b.lang == "bash" && b.mode == "":
			return nil, fmt.Errorf("line %d: a bash block has no quickstart-check marker; mark it run, background or skip", b.line)
		case b.mode == "skip" && b.arg == "":
			return nil, fmt.Errorf("line %d: a skipped block must say why", b.line)
		case b.mode == "file" && b.arg == "":
			return nil, fmt.Errorf("line %d: a file block must name its file", b.line)
		default:
			out = append(out, b)
		}
	}
	return out, nil
}

func loadQuickstart(t *testing.T) (string, []quickstartBlock) {
	t.Helper()
	raw, err := os.ReadFile(quickstartDocPath)
	if err != nil {
		t.Fatalf("reading the QuickStart: %v", err)
	}
	blocks, err := parseQuickstart(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", quickstartDocPath, err)
	}
	return string(raw), blocks
}

func normalizeQuickstart(line string) string {
	for _, n := range quickstartNormalizers {
		line = n.pattern.ReplaceAllString(line, n.replace)
	}
	return strings.TrimRight(line, " \t")
}

// missingQuickstartLines returns the lines of expected that are not lines of
// actual, after normalization. Empty lines and lines shortened with … are not
// compared.
func missingQuickstartLines(expected, actual string) []string {
	have := map[string]bool{}
	for line := range strings.SplitSeq(actual, "\n") {
		have[normalizeQuickstart(line)] = true
	}
	var missing []string
	for line := range strings.SplitSeq(expected, "\n") {
		if strings.TrimSpace(line) == "" || strings.Contains(line, "…") {
			continue
		}
		if !have[normalizeQuickstart(line)] {
			missing = append(missing, line)
		}
	}
	return missing
}

func TestQuickstartDocumentMarksEveryCommand(t *testing.T) {
	_, blocks := loadQuickstart(t)
	count := map[string]int{}
	for _, b := range blocks {
		count[b.mode]++
		if b.mode == "file" {
			want, err := os.ReadFile(filepath.Join(quickstartAgent, b.arg))
			if err != nil {
				t.Errorf("line %d: %v", b.line, err)
				continue
			}
			if b.body != string(want) {
				t.Errorf("line %d: the block is not examples/quickstart/%s as it is in the repository", b.line, b.arg)
			}
		}
	}
	if count["run"] == 0 {
		t.Error("the QuickStart has no block marked run")
	}
	if count["background"] != 1 {
		t.Errorf("the QuickStart has %d background blocks; the check expects exactly one, the control plane", count["background"])
	}
}

// Every file the agent needs is in the page's download loop, and nothing that
// is not in examples/quickstart is downloaded. The tests, the README and the
// unpinned requirements input are not needed to run the agent.
func TestQuickstartDownloadsEveryAgentFile(t *testing.T) {
	doc, _ := loadQuickstart(t)
	m := quickstartDownloads.FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("the QuickStart has no `for f in …; do curl` download loop")
	}
	downloaded := strings.Fields(m[1])
	slices.Sort(downloaded)

	out, err := exec.Command("git", "ls-files", "--", quickstartAgent).Output()
	if err != nil {
		t.Fatalf("listing examples/quickstart: %v", err)
	}
	var needed []string
	for path := range strings.FieldsSeq(string(out)) {
		name := filepath.Base(path)
		if name == "README.md" || name == "requirements.in" || strings.HasPrefix(name, "test_") {
			continue
		}
		needed = append(needed, name)
	}
	slices.Sort(needed)
	if !slices.Equal(downloaded, needed) {
		t.Errorf("the QuickStart downloads %q; examples/quickstart needs %q", downloaded, needed)
	}
}

func TestParseQuickstartRefusesUnmarkedCommands(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		{"unmarked bash", "```bash\nls\n```\n", "has no quickstart-check marker"},
		{"skip without reason", "<!-- quickstart-check: skip -->\n```bash\nls\n```\n", "must say why"},
		{"expect first", "<!-- quickstart-check: expect -->\n```text\nx\n```\n", "must follow a run"},
		{"marker then prose", "<!-- quickstart-check: run -->\nprose\n```bash\nls\n```\n", "not directly before a block"},
		{"dangling marker", "<!-- quickstart-check: run -->\n", "has no block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseQuickstart(tt.doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseQuickstart() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestMissingQuickstartLines(t *testing.T) {
	tests := []struct {
		name, expected, actual string
		want                   []string
	}{
		{"identical", "Gate: FAIL\n", "Gate: FAIL\n", nil},
		{"order is free", "b\na\n", "a\nb\n", nil},
		{"a changed line", "Gate: FAIL\n", "Gate: PASS\n", []string{"Gate: FAIL"}},
		{"shortened lines are skipped", "…\nRun … complete.\n", "other\n", nil},
		{"ids, ports and times", "  Run         dev-git-9094f2f-20261015T232908.484Z\nAPI:   http://127.0.0.1:57534\nquickstart   runs 3   (execution scn-quickstart-20261015T232916-89e3486e)\n",
			"  Run         dev-git-0123abc-20270101T000000.000Z\nAPI:   http://127.0.0.1:1234\nquickstart   runs 3   (execution scn-quickstart-20270101T000000-0123abcd)\n", nil},
		{"a source build's versions",
			"Produced by `trustvian` `v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991` and control plane `v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991`.\n",
			"Produced by `trustvian` `v0.12.1+dirty 5728e8afdd194bc8a05c87f8797b5368a75dc991` and control plane `(devel) 5728e8afdd194bc8a05c87f8797b5368a75dc991+dirty`.\n", nil},
		{"a digest is compared", "scenario sha256:81f1\n", "scenario sha256:0000\n", []string{"scenario sha256:81f1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingQuickstartLines(tt.expected, tt.actual); !slices.Equal(got, tt.want) {
				t.Errorf("missingQuickstartLines() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestQuickstartRunsAsDocumented runs the page. See the file comment.
func TestQuickstartRunsAsDocumented(t *testing.T) {
	if os.Getenv("TRUSTVIAN_QUICKSTART_CHECK") != "1" {
		t.Skip("set TRUSTVIAN_QUICKSTART_CHECK=1, or run `make quickstart-check`")
	}
	doc, blocks := loadQuickstart(t)
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartVersion.FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("the QuickStart sets no V=v<version>")
	}
	version := m[1]
	platform := runtime.GOOS + "_" + runtime.GOARCH
	archive := "trustvian_" + version + "_" + platform

	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	home := filepath.Join(root, "home")
	outDir := filepath.Join(root, "out")
	for _, dir := range []string{home, outDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if saved := os.Getenv("TRUSTVIAN_QUICKSTART_OUT"); saved != "" {
		outDir = saved
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	release := os.Getenv("TRUSTVIAN_QUICKSTART_RELEASE") == "1"
	if !release {
		buildQuickstartArchive(t, repo, filepath.Join(dist, version), archive)
	}

	script := quickstartScript(blocks, quickstartRewrite{
		release:  release,
		dist:     dist,
		example:  filepath.Join(repo, "examples", "quickstart"),
		platform: platform,
	}, outDir)
	if err := os.WriteFile(filepath.Join(outDir, "quickstart.sh"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", filepath.Join(outDir, "quickstart.sh"))
	cmd.Dir = home
	cmd.Env = quickstartEnv(home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The control plane is a background job of this shell; whatever happens,
	// nothing the page started outlives the test.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	})
	started := time.Now()
	output, err := cmd.CombinedOutput()
	t.Logf("the QuickStart's commands took %s", time.Since(started).Round(time.Second))
	if err != nil {
		current, _ := os.ReadFile(filepath.Join(outDir, "current"))
		block, _ := os.ReadFile(filepath.Join(outDir, strings.TrimSpace(string(current))+".out"))
		t.Fatalf("the QuickStart's commands stopped: %v\n%s\n%s", err, output, block)
	}

	for i, b := range blocks {
		if b.mode != "run" && b.mode != "background" {
			continue
		}
		got, err := os.ReadFile(filepath.Join(outDir, strconv.Itoa(i)+".out"))
		if err != nil {
			t.Fatalf("line %d: no output recorded: %v", b.line, err)
		}
		for _, e := range b.expect {
			missing := missingQuickstartLines(e.body, string(got))
			if len(missing) > 0 {
				t.Errorf("line %d: the page shows %d line(s) the command did not print:\n  %s\nIt printed:\n%s",
					e.line, len(missing), strings.Join(missing, "\n  "), got)
			}
		}
	}
}

type quickstartRewrite struct {
	release  bool
	dist     string
	example  string
	platform string
}

// quickstartScript is one shell session running every run and background block
// in order, so a cd, an export or a background job carries over as it does in
// a reader's terminal. Each block's output goes to its own file. Every command
// must exit 0, except one whose status the page's next command prints with $?:
// that next command is the one checked.
func quickstartScript(blocks []quickstartBlock, rw quickstartRewrite, outDir string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "QS_OUT=%q\nQS_API=\nQS_EXECUTION=\n", outDir)
	for i, b := range blocks {
		if b.mode != "run" && b.mode != "background" {
			continue
		}
		body := b.body
		if !rw.release {
			body = quickstartReleaseURL.ReplaceAllLiteralString(body, "file://"+rw.dist)
		}
		body = quickstartRawURL.ReplaceAllLiteralString(body, "file://"+rw.example)
		body = quickstartArchive.ReplaceAllLiteralString(body, "A=trustvian_${V}_"+rw.platform)
		body = quickstartAPI.ReplaceAllLiteralString(body, "$QS_API")
		body = quickstartExecution.ReplaceAllLiteralString(body, "$QS_EXECUTION")

		out := fmt.Sprintf("$QS_OUT/%d.out", i)
		fmt.Fprintf(&s, "\n# docs/quickstart.md:%d\necho %d > \"$QS_OUT/current\"\n{\n", b.line, i)
		commands := quickstartCommands(body)
		for j, command := range commands {
			s.WriteString(command)
			s.WriteByte('\n')
			if j+1 < len(commands) && strings.Contains(commands[j+1], "$?") {
				continue
			}
			fmt.Fprintf(&s, "qs_rc=$?; if [ $qs_rc -ne 0 ]; then echo \"docs/quickstart.md:%d: exit $qs_rc from: %s\"; exit 1; fi\n",
				b.line, strings.ReplaceAll(strings.ReplaceAll(command, `"`, `'`), "$", `\$`))
		}
		fmt.Fprintf(&s, "} > %q 2>&1\n", out)
		if b.mode == "background" {
			fmt.Fprintf(&s, `for _ in $(seq 1 300); do [ -s .trustvian/runtime.json ] && break; sleep 0.1; done
QS_API=$(sed -n 's/.*"api_url":"\([^"]*\)".*/\1/p' .trustvian/runtime.json)
if [ -z "$QS_API" ]; then echo "docs/quickstart.md:%d: the control plane wrote no discovery file" >> %q; exit 1; fi
`, b.line, out)
		}
		fmt.Fprintf(&s, "[ -n \"$QS_EXECUTION\" ] || QS_EXECUTION=$(grep -o 'scn-quickstart-[0-9]\\{8\\}T[0-9]\\{6\\}-[0-9a-f]\\{8\\}' %q | head -n 1)\n", out)
	}
	s.WriteString("echo done > \"$QS_OUT/current\"\n")
	return s.String()
}

// quickstartCommands splits a block into its commands: one per line, with
// backslash-continued lines joined, and blank and comment lines dropped.
func quickstartCommands(body string) []string {
	var (
		commands []string
		current  string
	)
	for line := range strings.SplitSeq(body, "\n") {
		if current == "" && (strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#")) {
			continue
		}
		if cont, ok := strings.CutSuffix(line, "\\"); ok {
			current += cont
			continue
		}
		commands = append(commands, current+line)
		current = ""
	}
	if current != "" {
		commands = append(commands, current)
	}
	return commands
}

// quickstartEnv is a clean environment: a fresh HOME, the system PATH, and a
// git identity, which a fresh HOME lacks. Nothing of Trustvian's or
// OpenTelemetry's is inherited.
func quickstartEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"GIT_AUTHOR_NAME=QuickStart",
		"GIT_AUTHOR_EMAIL=quickstart@example.invalid",
		"GIT_COMMITTER_NAME=QuickStart",
		"GIT_COMMITTER_EMAIL=quickstart@example.invalid",
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return env
}

// buildQuickstartArchive builds the three binaries from this checkout and
// packs them the way the release does: <archive>/ in <archive>.tar.gz, with a
// checksums.txt beside it.
func buildQuickstartArchive(t *testing.T, repo, dir, archive string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), archive)
	builds := []struct{ module, pkg, name string }{
		{".", "./cmd/trustvian", "trustvian"},
		{"platform", "./cmd/trustvian-local", "trustvian-local"},
		{"processor", "./cmd/trustvian-collector", "trustvian-collector"},
	}
	for _, b := range builds {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, b.name), b.pkg)
		cmd.Dir = filepath.Join(repo, b.module)
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", b.name, err, out)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	for _, b := range builds {
		data, err := os.ReadFile(filepath.Join(bin, b.name))
		if err != nil {
			t.Fatal(err)
		}
		hdr := &tar.Header{Name: archive + "/" + b.name, Mode: 0o755, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	name := archive + ".tar.gz"
	if err := os.WriteFile(filepath.Join(dir, name), packed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(packed.Bytes())
	checksums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
}
