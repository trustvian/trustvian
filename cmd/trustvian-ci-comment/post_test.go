package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pull request every test posts to.
const (
	owner     = "trustvian"
	repo      = "trustvian"
	number    = 7
	headSHA   = "0123456789abcdef0123456789abcdef01234567"
	testToken = "ghs_TESTTOKENthatmustneverbeprinted"
)

// fakeGitHub is the four REST endpoints the poster uses, holding one pull
// request's head and issue comments. Responses can be overridden per route,
// one queued status at a time.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	head     string
	comments []fakeComment
	nextID   int64
	// failures maps a route ("GET pulls", "GET comments", "POST comments",
	// "PATCH comment") to statuses served, in order, before the real answer.
	failures map[string][]failure
	requests []string
	auths    []string
	// landThenFail makes the first POST store its comment and answer 502,
	// as GitHub sometimes does.
	landThenFail bool
}

type fakeComment struct {
	ID    int64
	Login string
	Body  string
}

type failure struct {
	status int
	header map[string]string
}

func newFake(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{t: t, head: headSHA, nextID: 1000, failures: map[string][]failure{}}
	server := httptest.NewTLSServer(f)
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeGitHub) add(login, body string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.comments = append(f.comments, fakeComment{ID: f.nextID, Login: login, Body: body})
	return f.nextID
}

func (f *fakeGitHub) fail(route string, failures ...failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[route] = append(f.failures[route], failures...)
}

func (f *fakeGitHub) snapshot() []fakeComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeComment(nil), f.comments...)
}

func (f *fakeGitHub) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, "POST") || strings.HasPrefix(r, "PATCH") {
			n++
		}
	}
	return n
}

var (
	pullsPath    = fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	commentsPath = fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	commentPath  = regexp.MustCompile(fmt.Sprintf(`^/repos/%s/%s/issues/comments/([0-9]+)$`, owner, repo))
)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.auths = append(f.auths, r.Header.Get("Authorization"))

	var route string
	switch {
	case r.Method == "GET" && r.URL.Path == pullsPath:
		route = "GET pulls"
	case r.Method == "GET" && r.URL.Path == commentsPath:
		route = "GET comments"
	case r.Method == "POST" && r.URL.Path == commentsPath:
		route = "POST comments"
	case r.Method == "PATCH" && commentPath.MatchString(r.URL.Path):
		route = "PATCH comment"
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if queue := f.failures[route]; len(queue) > 0 {
		f.failures[route] = queue[1:]
		for k, v := range queue[0].header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(queue[0].status)
		_, _ = io.WriteString(w, `{"message":"`+testToken+` must never be echoed"}`)
		return
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	asJSON := func(c fakeComment) map[string]any {
		return map[string]any{"id": c.ID, "body": c.Body, "user": map[string]any{"login": c.Login}}
	}

	switch route {
	case "GET pulls":
		reply(200, map[string]any{"number": number, "head": map[string]any{"sha": f.head}})
	case "GET comments":
		if r.URL.Query().Get("per_page") != "100" {
			f.t.Errorf("listed with per_page=%q", r.URL.Query().Get("per_page"))
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		out := []map[string]any{}
		for i := (page - 1) * 100; i < len(f.comments) && i < page*100; i++ {
			out = append(out, asJSON(f.comments[i]))
		}
		reply(200, out)
	case "POST comments":
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.nextID++
		c := fakeComment{ID: f.nextID, Login: botLogin, Body: in.Body}
		f.comments = append(f.comments, c)
		if f.landThenFail {
			f.landThenFail = false
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		reply(201, asJSON(c))
	case "PATCH comment":
		id, _ := strconv.ParseInt(commentPath.FindStringSubmatch(r.URL.Path)[1], 10, 64)
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		for i := range f.comments {
			if f.comments[i].ID == id {
				f.comments[i].Body = in.Body
				reply(200, asJSON(f.comments[i]))
				return
			}
		}
		reply(404, map[string]any{"message": "Not Found"})
	}
}

type result struct {
	code           int
	stdout, stderr string
	summary        string
	slept          []time.Duration
}

// postBody runs the poster against the fake with body as the rendering.
func postBody(t *testing.T, server *httptest.Server, body string, extra ...string) result {
	t.Helper()
	return postWith(t, server, server.Client(), body, extra...)
}

func postWith(t *testing.T, server *httptest.Server, client *http.Client, body string, extra ...string) result {
	t.Helper()
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(dir, "summary.md")
	env := map[string]string{
		"GITHUB_TOKEN":        testToken,
		"GITHUB_API_URL":      server.URL,
		"GITHUB_STEP_SUMMARY": summary,
	}
	args := append([]string{"--repository", owner + "/" + repo, "--pull-request", strconv.Itoa(number),
		"--head-sha", headSHA, "--marker-id", "trustvian-run", "--body-file", bodyFile}, extra...)
	var stdout, stderr bytes.Buffer
	var slept []time.Duration
	code := run(args, func(k string) string { return env[k] }, &stdout, &stderr, client,
		func(d time.Duration) { slept = append(slept, d) })
	raw, _ := os.ReadFile(summary)
	out := result{code: code, stdout: stdout.String(), stderr: stderr.String(), summary: string(raw), slept: slept}
	if strings.Contains(out.stdout+out.stderr+out.summary, testToken) {
		t.Fatal("the testToken reached the output")
	}
	return out
}

const ourMarker = "<!-- trustvian-behavioral-gate:trustvian-run -->"

func TestPostCreatesThenEditsOneComment(t *testing.T) {
	f, server := newFake(t)
	first := postBody(t, server, "first rendering\n")
	if first.code != exitPosted || !strings.Contains(first.stdout, "created comment") {
		t.Fatalf("first run: exit %d\n%s%s", first.code, first.stdout, first.stderr)
	}
	second := postBody(t, server, "second rendering\n")
	if second.code != exitPosted || !strings.Contains(second.stdout, "updated comment") {
		t.Fatalf("second run: exit %d\n%s%s", second.code, second.stdout, second.stderr)
	}
	comments := f.snapshot()
	if len(comments) != 1 {
		t.Fatalf("%d comments after two runs, want 1", len(comments))
	}
	if want := ourMarker + "\nsecond rendering\n"; comments[0].Body != want {
		t.Errorf("comment body %q, want %q", comments[0].Body, want)
	}
	for _, auth := range f.auths {
		if auth != "Bearer "+testToken {
			t.Errorf("a request carried Authorization %q", auth)
		}
	}
}

// A marker is ours only on the first line of a github-actions[bot] comment.
func TestPostNeverEditsACommentItDoesNotOwn(t *testing.T) {
	f, server := newFake(t)
	human := f.add("octocat", ourMarker+"\nI copied the marker on purpose.")
	quoted := f.add(botLogin, "Another workflow quoted it: `"+ourMarker+"`")
	later := f.add(botLogin, "first line\n"+ourMarker)
	otherID := f.add(botLogin, "<!-- trustvian-behavioral-gate:another-suite -->\nanother suite")
	prefixed := f.add(botLogin, ourMarker+"x\nlonger marker")
	res := postBody(t, server, "ours\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, "created comment") {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
	for _, c := range f.snapshot() {
		switch c.ID {
		case human, quoted, later, otherID, prefixed:
			if strings.Contains(c.Body, "ours") {
				t.Errorf("comment %d, which the poster does not own, was edited", c.ID)
			}
		}
	}
	if n := len(f.snapshot()); n != 6 {
		t.Errorf("%d comments, want the five others and one new", n)
	}
}

func TestPostUpdatesTheNewestOwnedAndWarnsOfDuplicates(t *testing.T) {
	f, server := newFake(t)
	older := f.add(botLogin, ourMarker+"\nolder")
	newer := f.add(botLogin, ourMarker+"\nnewer")
	res := postBody(t, server, "current\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, fmt.Sprintf("updated comment %d", newer)) {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
	if !strings.Contains(res.stdout, "::warning title=trustvian-comment::1 older gate comments") {
		t.Errorf("duplicates were not reported:\n%s", res.stdout)
	}
	for _, c := range f.snapshot() {
		if c.ID == older && c.Body != ourMarker+"\nolder" {
			t.Error("the older duplicate was edited")
		}
	}
}

// The search reaches every page up to its bound.
func TestPostFindsItsCommentOnALaterPage(t *testing.T) {
	f, server := newFake(t)
	for range 250 {
		f.add("someone", "chatter")
	}
	ours := f.add(botLogin, ourMarker+"\nstale")
	res := postBody(t, server, "fresh\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, fmt.Sprintf("updated comment %d", ours)) {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
	pages := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, "GET "+commentsPath) {
			pages++
		}
	}
	if pages != 3 {
		t.Errorf("listed %d pages, want 3", pages)
	}
}

func TestPostStopsSearchingAtItsBound(t *testing.T) {
	f, server := newFake(t)
	for range maxPages*perPage + 5 {
		f.add("someone", "chatter")
	}
	res := postBody(t, server, "fresh\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, "more than 3000 comments") {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
	for page := 1; page <= maxPages; page++ {
		want := fmt.Sprintf("GET %s?per_page=100&page=%d", commentsPath, page)
		if !slices.Contains(f.requests, want) {
			t.Errorf("page %d was not searched", page)
		}
	}
	for _, r := range f.requests {
		if strings.Contains(r, fmt.Sprintf("page=%d", maxPages+1)) {
			t.Errorf("searched past the bound: %s", r)
		}
	}
}

// A newer head means a newer run owns the comment: write nothing.
func TestPostWritesNothingWhenSuperseded(t *testing.T) {
	f, server := newFake(t)
	f.head = "fedcba9876543210fedcba9876543210fedcba98"
	stale := f.add(botLogin, ourMarker+"\nthe newer run's verdict")
	res := postBody(t, server, "an older run's rendering\n")
	if res.code != exitPosted {
		t.Fatalf("exit %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "::notice title=trustvian-comment::superseded by "+f.head) {
		t.Errorf("no superseded notice:\n%s", res.stdout)
	}
	if f.writes() != 0 {
		t.Errorf("a superseded run wrote: %v", f.requests)
	}
	if got := f.snapshot()[0]; got.ID != stale || got.Body != ourMarker+"\nthe newer run's verdict" {
		t.Error("the newer run's comment was changed")
	}
}

// The fork path: a read-only testToken is a warning and a job summary note, and
// the job succeeds.
func TestPostDegradesWhenNotPermitted(t *testing.T) {
	for _, tt := range []struct{ name, route string }{
		{"403 creating", "POST comments"},
		{"404 creating", "POST comments"},
		{"403 updating", "PATCH comment"},
		{"403 reading the pull request", "GET pulls"},
		{"403 listing comments", "GET comments"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, server := newFake(t)
			if tt.route == "PATCH comment" {
				f.add(botLogin, ourMarker+"\nstale")
			}
			status := http.StatusForbidden
			if strings.HasPrefix(tt.name, "404") {
				status = http.StatusNotFound
			}
			f.fail(tt.route, failure{status: status})
			res := postBody(t, server, "rendering\n")
			if res.code != exitPosted {
				t.Fatalf("exit %d, want 0\n%s%s", res.code, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stdout, "::warning title=trustvian-comment::the gate comment was not posted") {
				t.Errorf("no warning:\n%s", res.stdout)
			}
			if !strings.Contains(res.summary, "expected on a fork") {
				t.Errorf("no job summary note: %q", res.summary)
			}
			if len(res.slept) != 0 {
				t.Error("a refusal was retried")
			}
		})
	}
}

// 429 and 5xx are retried once, then fail the job — visibly.
func TestPostRetriesOnceThenFails(t *testing.T) {
	t.Run("5xx twice fails", func(t *testing.T) {
		f, server := newFake(t)
		f.fail("POST comments", failure{status: 502}, failure{status: 503})
		res := postBody(t, server, "rendering\n")
		if res.code != exitFailed {
			t.Fatalf("exit %d, want %d", res.code, exitFailed)
		}
		if !strings.Contains(res.stdout, "::error title=trustvian-comment::") ||
			!strings.Contains(res.stdout, "after one retry") {
			t.Errorf("the failure is not reported:\n%s", res.stdout)
		}
		if !strings.Contains(res.summary, "could not be posted") {
			t.Errorf("no job summary note: %q", res.summary)
		}
		if len(res.slept) != 1 {
			t.Errorf("slept %d times, want exactly one retry", len(res.slept))
		}
		if f.writes() != 2 {
			t.Errorf("%d write attempts, want 2", f.writes())
		}
	})
	t.Run("one 5xx then success posts", func(t *testing.T) {
		f, server := newFake(t)
		f.fail("GET comments", failure{status: 500})
		res := postBody(t, server, "rendering\n")
		if res.code != exitPosted || len(f.snapshot()) != 1 {
			t.Fatalf("exit %d, %d comments\n%s", res.code, len(f.snapshot()), res.stdout)
		}
	})
	t.Run("429 waits as asked, within the cap", func(t *testing.T) {
		f, server := newFake(t)
		f.fail("GET pulls", failure{status: 429, header: map[string]string{"Retry-After": "600"}})
		res := postBody(t, server, "rendering\n")
		if res.code != exitPosted || len(res.slept) != 1 || res.slept[0] != maxRetryWait {
			t.Fatalf("exit %d, slept %v", res.code, res.slept)
		}
	})
	t.Run("a rate-limiting 403 is retried, not degraded", func(t *testing.T) {
		f, server := newFake(t)
		f.fail("POST comments", failure{status: 403, header: map[string]string{"X-RateLimit-Remaining": "0"}})
		res := postBody(t, server, "rendering\n")
		if res.code != exitPosted || len(f.snapshot()) != 1 || strings.Contains(res.stdout, "::warning") {
			t.Fatalf("exit %d\n%s", res.code, res.stdout)
		}
	})
	t.Run("an unexpected status is not retried", func(t *testing.T) {
		f, server := newFake(t)
		f.fail("POST comments", failure{status: 422})
		res := postBody(t, server, "rendering\n")
		if res.code != exitFailed || len(res.slept) != 0 {
			t.Fatalf("exit %d, slept %v", res.code, res.slept)
		}
	})
}

// --- The renderer's no-verdict bodies, posted over a stored PASS -------------

var (
	rendererOnce sync.Once
	rendererBin  string
	rendererErr  error
)

// renderer builds the real cmd/trustvian-ci-render once per test binary.
func renderer(t *testing.T) string {
	t.Helper()
	rendererOnce.Do(func() {
		dir, err := os.MkdirTemp("", "trustvian-ci-render")
		if err != nil {
			rendererErr = err
			return
		}
		rendererBin = filepath.Join(dir, "trustvian-ci-render")
		out, err := exec.Command("go", "build", "-o", rendererBin, "../trustvian-ci-render").CombinedOutput()
		if err != nil {
			rendererErr = fmt.Errorf("%v\n%s", err, out)
		}
	})
	if rendererErr != nil {
		t.Fatalf("building the renderer: %v", rendererErr)
	}
	return rendererBin
}

const fixtures = "../trustvian-ci-render/testdata/artifacts"

// render runs the real renderer on an artifact directory with the identity
// its fixtures were generated under.
func render(t *testing.T, dir, exitCode string) (string, int) {
	t.Helper()
	cmd := exec.Command(renderer(t), "--artifact-dir", dir, "--head-sha", headSHA, "--repository",
		owner+"/"+repo, "--run-id", "1000000001", "--run-attempt", "1", "--exit-code", exitCode)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), code
}

// withoutVerdict copies the real PASS artifact with comparison.gate.verdict
// removed, the metadata's size and digest kept consistent.
func withoutVerdict(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(fixtures, "pass", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["comparison"].(map[string]any)["gate"].(map[string]any), "verdict")
	result, _ := json.Marshal(doc)
	var meta map[string]any
	rawMeta, _ := os.ReadFile(filepath.Join(fixtures, "pass", "trustvian-run.json"))
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(result)
	meta["result"].(map[string]any)["bytes"] = len(result)
	meta["result"].(map[string]any)["sha256"] = hex.EncodeToString(sum[:])
	metaOut, _ := json.Marshal(meta)
	for name, content := range map[string][]byte{"result.json": result, "trustvian-run.json": metaOut} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A stored PASS is replaced, never left standing, when the next run has no
// verdict — for each cause task 079 names.
func TestPostOverwritesAStalePassWithNoVerdict(t *testing.T) {
	pass, code := render(t, filepath.Join(fixtures, "pass"), "0")
	if code != 0 || !strings.Contains(pass, "— PASS") {
		t.Fatalf("the PASS rendering: exit %d\n%s", code, pass)
	}
	tests := []struct {
		name, dir, exitCode, reason string
		renderExit                  int
	}{
		{"a missing artifact", filepath.Join(t.TempDir(), "absent"), "0", "artifact is missing", 1},
		{"exit 3", filepath.Join(fixtures, "operational"), "3", "operational error", 1},
		{"exit 2", filepath.Join(fixtures, "usage"), "2", "usage error", 1},
		{"a missing required field", "", "0", "comparison.gate.verdict: required field is absent", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir
			if dir == "" {
				dir = withoutVerdict(t)
			}
			noVerdict, code := render(t, dir, tt.exitCode)
			if code != tt.renderExit {
				t.Fatalf("the renderer exited %d, want %d\n%s", code, tt.renderExit, noVerdict)
			}
			f, server := newFake(t)
			if res := postBody(t, server, pass); res.code != exitPosted {
				t.Fatalf("posting the PASS: exit %d", res.code)
			}
			if res := postBody(t, server, noVerdict); res.code != exitPosted ||
				!strings.Contains(res.stdout, "updated comment") {
				t.Fatalf("posting the no-verdict: exit %d\n%s", res.code, res.stdout)
			}
			comments := f.snapshot()
			if len(comments) != 1 {
				t.Fatalf("%d comments, want the one comment, edited", len(comments))
			}
			body := comments[0].Body
			if strings.Contains(body, "PASS") {
				t.Errorf("the stale PASS is still standing:\n%s", body)
			}
			if !strings.Contains(body, tt.reason) {
				t.Errorf("the comment does not give the reason %q:\n%s", tt.reason, body)
			}
			assertNoVerdictBody(t, body)
		})
	}
}

// assertNoVerdictBody: the marker, the head commit, and no number but the
// commit, the run link and the reason's own fixed text.
func assertNoVerdictBody(t *testing.T, body string) {
	t.Helper()
	want := ourMarker + "\n### Trustvian behavioral gate — no verdict\n\n**Commit** `" + headSHA + "`"
	if !strings.HasPrefix(body, want) {
		t.Errorf("the no-verdict comment does not lead with the marker and the head commit:\n%s", body)
	}
	at := strings.Index(body, "No verdict exists")
	if at < 0 {
		t.Fatalf("no no-verdict statement:\n%s", body)
	}
	rest := regexp.MustCompile("(?m)^\\*\\*Reason:\\*\\* `.*`$").ReplaceAllString(body[at:], "")
	if regexp.MustCompile(`[0-9]`).MatchString(rest) {
		t.Errorf("the no-verdict comment carries a number:\n%s", body)
	}
	for _, banned := range []string{"|", "Gate check", "Behavior", "/2", "Produced by", "scn-"} {
		if strings.Contains(body, banned) {
			t.Errorf("the no-verdict comment contains %q:\n%s", banned, body)
		}
	}
}

// A 404 to a read is a wrong repository or number — even a fork's read-only
// token can read the pull request — so it fails rather than degrading.
func TestPostFailsOnANotFoundRead(t *testing.T) {
	for _, route := range []string{"GET pulls", "GET comments"} {
		t.Run(route, func(t *testing.T) {
			f, server := newFake(t)
			f.fail(route, failure{status: 404})
			res := postBody(t, server, "rendering\n")
			if res.code != exitFailed || strings.Contains(res.stdout, "::warning") {
				t.Fatalf("exit %d\n%s", res.code, res.stdout)
			}
			if f.writes() != 0 {
				t.Error("wrote after a failed read")
			}
		})
	}
}

// A POST that failed may have landed. The poster lists again before its one
// retry, and edits the comment that landed instead of duplicating it.
func TestPostNeverDuplicatesAfterALandedButFailedCreate(t *testing.T) {
	f, server := newFake(t)
	f.mu.Lock()
	f.landThenFail = true
	f.mu.Unlock()
	res := postBody(t, server, "rendering\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, "updated comment") {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
	if n := len(f.snapshot()); n != 1 {
		t.Errorf("%d comments, want the one that landed", n)
	}
	if len(res.slept) != 1 {
		t.Errorf("slept %d times, want one wait before the retry", len(res.slept))
	}
}

// A comment deleted between listing and editing is posted afresh.
func TestPostRecreatesACommentDeletedWhileEditing(t *testing.T) {
	f, server := newFake(t)
	f.add(botLogin, ourMarker+"\nstale")
	f.fail("PATCH comment", failure{status: 404})
	res := postBody(t, server, "fresh\n")
	if res.code != exitPosted || !strings.Contains(res.stdout, "created comment") {
		t.Fatalf("exit %d\n%s", res.code, res.stdout)
	}
}

// A pull request GitHub returns without a head commit is an API failure, never
// a reason to write nothing and succeed.
func TestPostFailsWhenTheHeadIsNotACommit(t *testing.T) {
	for _, head := range []string{"", "not-a-sha", strings.ToUpper(headSHA)} {
		t.Run(fmt.Sprintf("%q", head), func(t *testing.T) {
			f, server := newFake(t)
			f.head = head
			res := postBody(t, server, "rendering\n")
			if res.code != exitFailed || strings.Contains(res.stdout, "superseded") {
				t.Fatalf("exit %d\n%s", res.code, res.stdout)
			}
		})
	}
}

// A transport failure is retried once.
func TestPostRetriesATransportFailure(t *testing.T) {
	f, server := newFake(t)
	client := server.Client()
	base := client.Transport
	failed := false
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !failed && req.Method == "GET" {
			failed = true
			return nil, fmt.Errorf("connection reset")
		}
		return base.RoundTrip(req)
	})
	res := postWith(t, server, client, "rendering\n")
	if res.code != exitPosted || len(f.snapshot()) != 1 || len(res.slept) != 1 {
		t.Fatalf("exit %d, slept %v\n%s", res.code, res.slept, res.stdout)
	}
}

// No redirect is followed: the token never leaves the API host.
func TestPostFollowsNoRedirect(t *testing.T) {
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a redirect was followed to %s, carrying %q", r.URL, r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirecting.Close()
	// The production client, with the test server's certificate trusted.
	client := newHTTPClient(redirecting.Client().Transport)
	res := postWith(t, redirecting, client, "rendering\n")
	if res.code != exitFailed {
		t.Fatalf("exit %d, want %d\n%s", res.code, exitFailed, res.stdout)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
