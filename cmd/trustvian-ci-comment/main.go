// Command trustvian-ci-comment posts a trustvian-ci-render rendering as the
// pull request's one behavioral gate comment, editing it in place (task 079;
// ADR 0058).
//
// It is the only part of Trustvian that writes to GitHub, and it runs in the
// job that holds the write-scoped token — a job that checks out and runs no
// pull request code. It is built from the Go standard library alone and
// imports nothing that can start a process or load code.
//
// The token is read from GITHUB_TOKEN and never from the command line; the
// API base from GITHUB_API_URL, which must be a bare https host.
//
// Exit codes:
//
//	0  the comment was created or updated; or the pull request has a newer
//	   head, so a newer run owns the comment and nothing was written; or the
//	   token may not write here — a fork, or a read-only token — which is
//	   reported as a warning and in the job summary
//	1  the GitHub API failed, after one retry: a GET or PATCH is sent again;
//	   a failed POST is followed by a fresh listing, so a comment that landed
//	   is edited rather than duplicated
//	2  usage: a flag or the environment is missing or malformed
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const usageText = `usage:
  GITHUB_TOKEN=... trustvian-ci-comment --repository <owner/name> --pull-request <n>
      --head-sha <sha> --marker-id <id> --body-file <file>

Posts the rendering in --body-file as the pull request's comment for
--marker-id, editing the existing one in place. GITHUB_API_URL, when set, must
be https://host with no path.

  0  posted, superseded by a newer head, or not permitted (a warning)
  1  the GitHub API failed
  2  usage`

const (
	exitPosted = 0
	exitFailed = 1
	exitUsage  = 2
)

// maxCommentBytes is GitHub's 65,536-character limit on a comment body,
// counted in bytes, which is never fewer.
const maxCommentBytes = 65536

var (
	repositoryText = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	numberText     = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
	// markerIDText is the run action's artifact-name character set: the id
	// sits inside an HTML comment, so it can never close one.
	markerIDText = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	apiURLText   = regexp.MustCompile(`^https://[A-Za-z0-9.-]{1,253}(:[1-9][0-9]{0,4})?$`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr, newHTTPClient(nil), time.Sleep))
}

// newHTTPClient follows no redirect: the token goes to the validated API host
// and nowhere else, whatever a response says. transport is nil in production.
func newHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// run is the command, with its environment and its HTTP client supplied, so
// tests drive it against a fake API.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer,
	httpClient *http.Client, sleep func(time.Duration)) int {
	fs := flag.NewFlagSet("trustvian-ci-comment", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repository := fs.String("repository", "", "owner/name")
	number := fs.String("pull-request", "", "the pull request number")
	head := fs.String("head-sha", "", "the head commit the rendering describes")
	id := fs.String("marker-id", "", "which gate comment this is: the artifact name")
	bodyFile := fs.String("body-file", "", "the rendering")
	usage := func(problem string) int {
		fmt.Fprintf(stderr, "trustvian-ci-comment: %s\n\n%s\n", problem, usageText)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument")
	}
	owner, repo, _ := strings.Cut(*repository, "/")
	switch {
	case !repositoryText.MatchString(*repository) || dotSegment(owner) || dotSegment(repo):
		return usage("--repository must be owner/name")
	case !numberText.MatchString(*number):
		return usage("--pull-request must be a positive number")
	case !shaText.MatchString(*head):
		return usage("--head-sha must be a lowercase hexadecimal commit SHA, 40 or 64 characters")
	case !markerIDText.MatchString(*id):
		return usage("--marker-id must be 1-100 characters of letters, digits, '.', '_' and '-'")
	case *bodyFile == "":
		return usage("--body-file is required")
	}
	token := getenv("GITHUB_TOKEN")
	if token == "" {
		return usage("GITHUB_TOKEN is not set")
	}
	base := getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	if !apiURLText.MatchString(base) {
		return usage("GITHUB_API_URL must be https://host, with no path")
	}
	body, err := readBody(*bodyFile, maxCommentBytes-len(marker(*id))-1)
	if err != nil {
		return usage(err.Error())
	}
	n, _ := strconv.Atoi(*number)

	c := client{http: httpClient, base: base, token: token, sleep: sleep}
	res, err := post(context.Background(), c, postRequest{
		owner: owner, repo: repo, number: n, headSHA: *head, id: *id, body: body,
	})
	if errors.Is(err, errForbidden) {
		// A read refused with 403: the same fork and read-only path.
		res.outcome, err = notPermitted, nil
	}
	if err != nil {
		annotate(stdout, "error", fmt.Sprintf("the gate comment was not posted: %v", err))
		summarize(getenv, stderr, "**Trustvian:** the behavioral gate comment could not be posted: the GitHub API "+
			"failed. The gate's result is the run job's and is unchanged.")
		return exitFailed
	}
	switch res.outcome {
	case created:
		fmt.Fprintf(stdout, "created comment %d for %s\n", res.commentID, *id)
	case updated:
		fmt.Fprintf(stdout, "updated comment %d for %s\n", res.commentID, *id)
	case superseded:
		annotate(stdout, "notice", fmt.Sprintf("superseded by %s: the pull request has a newer head, and the "+
			"run for it owns the comment; nothing was written", shortSHA(res.newerHead)))
	case notPermitted:
		annotate(stdout, "warning", "the gate comment was not posted: the token cannot write to this pull "+
			"request (a fork, or a read-only token). The gate's result is the run job's and is unchanged")
		summarize(getenv, stderr, "**Trustvian:** the behavioral gate comment was not posted, because this "+
			"job's token cannot write to the pull request — expected on a fork. The gate's result is the run "+
			"job's and is unchanged.")
	}
	if res.duplicates > 0 {
		annotate(stdout, "warning", fmt.Sprintf("%d older gate comments for %s carry the same marker; "+
			"only the newest was updated", res.duplicates, *id))
	}
	if res.truncated {
		annotate(stdout, "warning", fmt.Sprintf("the pull request may have more than %d comments; only the "+
			"first %d were searched for the gate comment", maxPages*perPage, maxPages*perPage))
	}
	return exitPosted
}

func dotSegment(s string) bool { return s == "." || s == ".." }

// readBody reads the rendering: valid UTF-8, and small enough that the
// marker and the body together fit GitHub's comment limit.
func readBody(path string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("--body-file cannot be opened")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	switch {
	case err != nil:
		return "", fmt.Errorf("--body-file cannot be read")
	case len(raw) > limit:
		return "", fmt.Errorf("--body-file is over %d bytes, which with the marker exceeds GitHub's comment limit", limit)
	case !utf8.Valid(raw):
		return "", fmt.Errorf("--body-file is not valid UTF-8")
	}
	return string(raw), nil
}

// annotate writes a workflow command. Every message is this command's own
// text, with validated identifiers; '%', '\r' and '\n' are escaped anyway, as
// a workflow command requires.
func annotate(w io.Writer, level, message string) {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	fmt.Fprintf(w, "::%s title=trustvian-comment::%s\n", level, r.Replace(message))
}

// summarize appends a note to the job summary. Without one — outside
// GitHub Actions — it says so on stderr instead.
func summarize(getenv func(string) string, stderr io.Writer, note string) {
	path := getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		fmt.Fprintln(stderr, "trustvian-ci-comment: no GITHUB_STEP_SUMMARY to write the note to")
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = fmt.Fprintf(f, "\n%s\n", note)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "trustvian-ci-comment: the job summary could not be written")
	}
}

// shortSHA is a head GitHub named, already validated as a commit SHA.
func shortSHA(sha string) string { return sha }
