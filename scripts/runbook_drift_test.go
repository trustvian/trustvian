//go:build !windows

// The release runbook (docs/release-runbook.md) is a procedure people and
// agents follow literally, so it must not drift from the code it describes.
// These tests fail when it names a make target, variable, script, script
// subcommand, slash command or link that does not exist, or quotes a refusal
// message the scripts no longer print.
package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const runbook = "../docs/release-runbook.md"

// The scripts whose messages the runbook may quote.
var messageSources = []string{
	"release-preflight.sh",
	"release-version.sh",
	"release.sh",
	"release-prep.sh",
}

var (
	makeCall      = regexp.MustCompile("make ([a-z][a-z0-9-]*)((?: +[A-Z_]+=(?:\"[^\"]*\"|[^ `]*))*)")
	makeVar       = regexp.MustCompile(`([A-Z_]+)=`)
	makeTarget    = regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*):`)
	scriptPath    = regexp.MustCompile(`scripts/[A-Za-z0-9_.-]+`)
	versionCmd    = regexp.MustCompile(`release-version\.sh ([a-z-]+)`)
	slashCommand  = regexp.MustCompile("`/([a-z-]+)[ `]")
	mdLink        = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	backticked    = regexp.MustCompile("`([^`]+)`")
	shellVariable = regexp.MustCompile(`\$\{[^}]*\}|\$\([^)]*\)|\$[A-Za-z_][A-Za-z0-9_]*`)
)

func runbookText(t *testing.T) string {
	t.Helper()
	return readFile(t, runbook)
}

// Every `make <target> VAR=…` the runbook shows is a Makefile target whose
// recipe passes each variable on.
func TestRunbookMakeTargetsExist(t *testing.T) {
	makefile := readFile(t, "../Makefile")
	recipes := map[string]string{}
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		if m := makeTarget.FindStringSubmatch(line); m != nil {
			var body []string
			for j := i + 1; j < len(lines) && strings.HasPrefix(lines[j], "\t"); j++ {
				body = append(body, lines[j])
			}
			recipes[m[1]] = strings.Join(body, "\n")
		}
	}
	found := 0
	for _, m := range makeCall.FindAllStringSubmatch(runbookText(t), -1) {
		found++
		recipe, ok := recipes[m[1]]
		if !ok {
			t.Errorf("the runbook runs `make %s`, which the Makefile does not define", m[1])
			continue
		}
		for _, v := range makeVar.FindAllStringSubmatch(m[2], -1) {
			if !strings.Contains(recipe, "$("+v[1]+")") {
				t.Errorf("the runbook passes %s= to `make %s`, which its recipe ignores", v[1], m[1])
			}
		}
	}
	if found < 5 {
		t.Fatalf("found only %d make commands in the runbook; the pattern is broken", found)
	}
}

// Every scripts/… path exists, every release-version.sh subcommand is one the
// script accepts, and every slash command has a definition.
func TestRunbookScriptsAndCommandsExist(t *testing.T) {
	text := runbookText(t)
	for _, p := range scriptPath.FindAllString(text, -1) {
		p = strings.TrimRight(p, ".")
		if _, err := os.Stat(filepath.Join("..", p)); err != nil {
			t.Errorf("the runbook names %s, which does not exist", p)
		}
	}
	versionScript := readFile(t, "release-version.sh")
	for _, m := range versionCmd.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(versionScript, "        "+m[1]+")") {
			t.Errorf("the runbook runs release-version.sh %s, which the script does not accept", m[1])
		}
	}
	for _, m := range slashCommand.FindAllStringSubmatch(text, -1) {
		if _, err := os.Stat(filepath.Join("..", ".claude", "commands", m[1]+".md")); err != nil {
			t.Errorf("the runbook names /%s, which has no .claude/commands/%s.md", m[1], m[1])
		}
	}
}

// normalizedMessages returns every line of the message sources with shell
// variables replaced by "…" and escaped quotes unescaped, which is how the
// runbook writes a message that names a version, commit or workflow.
func normalizedMessages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range messageSources {
		for _, line := range strings.Split(readFile(t, f), "\n") {
			line = strings.ReplaceAll(line, `\"`, `"`)
			out = append(out, shellVariable.ReplaceAllString(line, "…"))
		}
	}
	return out
}

// Every message the F1 tables quote is one a script prints, with "…" standing
// for whatever the script fills in.
func TestRunbookQuotesRealPreflightMessages(t *testing.T) {
	text := runbookText(t)
	start := strings.Index(text, "### F1.")
	end := strings.Index(text, "### F2.")
	if start < 0 || end < start {
		t.Fatal("the runbook has no F1 section")
	}
	sources := normalizedMessages(t)
	quoted := 0
	for _, row := range strings.Split(text[start:end], "\n") {
		if !strings.HasPrefix(row, "| `") {
			continue
		}
		cell := strings.SplitN(row, "|", 3)[1]
		for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
			quoted++
			var parts []string
			for _, frag := range strings.Split(m[1], "…") {
				parts = append(parts, regexp.QuoteMeta(frag))
			}
			pattern := regexp.MustCompile(strings.Join(parts, ".*"))
			matched := false
			for _, line := range sources {
				if pattern.MatchString(line) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("F1 quotes %q, which no release script prints", m[1])
			}
		}
	}
	if quoted < 10 {
		t.Fatalf("found only %d quoted messages in F1; the table or the pattern changed", quoted)
	}
}

// Every relative link resolves to a file, and every #anchor into a Markdown
// file to one of its headings.
func TestRunbookLinksResolve(t *testing.T) {
	text := runbookText(t)
	dir := filepath.Dir(runbook)
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		link := m[1]
		if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
			continue
		}
		path, anchor, _ := strings.Cut(link, "#")
		target := runbook
		if path != "" {
			target = filepath.Join(dir, path)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("the runbook links %s, which does not exist", link)
			continue
		}
		if anchor != "" && strings.HasSuffix(target, ".md") && !hasAnchor(readFile(t, target), anchor) {
			t.Errorf("the runbook links %s, but %s has no such heading", link, target)
		}
	}
}

// hasAnchor reports whether a Markdown document has a heading whose
// GitHub-style anchor is anchor.
func hasAnchor(doc, anchor string) bool {
	drop := regexp.MustCompile(`[^\p{L}\p{N} _-]`)
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		h := strings.TrimSpace(strings.TrimLeft(line, "#"))
		slug := strings.ReplaceAll(drop.ReplaceAllString(strings.ToLower(h), ""), " ", "-")
		if slug == anchor {
			return true
		}
	}
	return false
}
