// Command trustvian-ci-render renders a trustvian-run artifact as Markdown for
// a pull request comment or a job summary (task 079; ADR 0057).
//
// It works offline, needs no credential, and treats the artifact as untrusted
// data: it opens only the artifact's two fixed file names, bounds every read,
// validates both documents strictly, and executes nothing. Every number,
// classification, check and verdict it prints is transcribed from the result
// document; it computes none of them. What it cannot render faithfully it
// renders as an explicit "no verdict" for the expected head commit.
//
// Everything that identifies the run comes from the caller — from the event
// and the run job's outputs — and the artifact must agree with it.
//
// Exit codes:
//
//	0  evidence was rendered: a scenario verdict or a suite report
//	1  no verdict: the CLI did not run, exited 2 or 3, or left no result
//	2  usage: the expected context is missing or malformed; nothing rendered
//	3  no verdict: the artifact was rejected by validation
//
// The Markdown is written to stdout in every case but 2; the reason for a
// no-verdict state is also written to stderr.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const usageText = `usage:
  trustvian-ci-render --artifact-dir <dir> --head-sha <sha> --repository <owner/name>
                      --run-id <id> --run-attempt <n> --exit-code <code or empty>
                      [--server-url <https://host>]

Renders a trustvian-run artifact as Markdown on stdout. Every flag but
--server-url is required; --exit-code is the run job's exit-code output, and
an empty value means the CLI did not run.

  0  evidence rendered    1  no verdict
  2  usage                3  no verdict: the artifact was rejected`

const (
	exitRendered  = 0
	exitNoVerdict = 1
	exitUsage     = 2
	exitRejected  = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

var (
	repositoryText = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	runNumberText  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	serverURLText  = regexp.MustCompile(`^https://[A-Za-z0-9.-]{1,253}(:[1-9][0-9]{0,4})?$`)
	exitCodeText   = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})$`)
)

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trustvian-ci-render", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("artifact-dir", "", "the downloaded artifact directory")
	head := fs.String("head-sha", "", "the pull request's head commit, from the event")
	repository := fs.String("repository", "", "owner/name")
	runID := fs.String("run-id", "", "the workflow run id")
	attempt := fs.String("run-attempt", "", "the workflow run attempt")
	exitCode := fs.String("exit-code", "", "the run job's exit-code output")
	serverURL := fs.String("server-url", "https://github.com", "the GitHub server URL")
	usage := func(problem string) int {
		fmt.Fprintf(stderr, "trustvian-ci-render: %s\n\n%s\n", problem, usageText)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, name := range []string{"artifact-dir", "head-sha", "repository", "run-id", "run-attempt", "exit-code"} {
		if !set[name] {
			return usage("--" + name + " is required")
		}
	}

	want := expectedContext{
		headSHA: *head, repository: *repository, runID: *runID, runAttempt: *attempt,
		serverURL: *serverURL,
	}
	switch {
	case *dir == "":
		return usage("--artifact-dir is empty")
	case !shaText.MatchString(want.headSHA):
		return usage("--head-sha must be a lowercase hexadecimal commit SHA, 40 or 64 characters")
	case !repositoryText.MatchString(want.repository) || hasDotSegment(want.repository):
		return usage("--repository must be owner/name")
	case !runNumberText.MatchString(want.runID):
		return usage("--run-id must be a positive decimal number")
	case !runNumberText.MatchString(want.runAttempt):
		return usage("--run-attempt must be a positive decimal number")
	case !serverURLText.MatchString(want.serverURL):
		return usage("--server-url must be https://host, with no path")
	}
	if *exitCode != "" {
		if !exitCodeText.MatchString(*exitCode) {
			return usage("--exit-code must be empty or a decimal exit status")
		}
		code, err := strconv.Atoi(*exitCode)
		if err != nil || code > 255 {
			return usage("--exit-code must be empty or a decimal exit status")
		}
		want.exitCode = &code
	}

	out := render(*dir, want, defaultLimits)
	if _, err := io.WriteString(stdout, out.markdown); err != nil {
		fmt.Fprintf(stderr, "trustvian-ci-render: writing the rendering: %v\n", err)
		return exitRejected
	}
	switch {
	case out.state != stateNoVerdict:
		return exitRendered
	case out.rejected != nil:
		fmt.Fprintf(stderr, "trustvian-ci-render: no verdict: %s\n", out.reason)
		return exitRejected
	default:
		fmt.Fprintf(stderr, "trustvian-ci-render: no verdict: %s\n", out.reason)
		return exitNoVerdict
	}
}

// hasDotSegment refuses "." and ".." as either half of owner/name, which the
// character class allows but which would change the run link's path.
func hasDotSegment(repository string) bool {
	owner, name, _ := strings.Cut(repository, "/")
	return owner == "." || owner == ".." || name == "." || name == ".."
}
