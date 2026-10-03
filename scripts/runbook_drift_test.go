//go:build !windows

// The release runbook (docs/release-runbook.md) and the worked examples of
// releasing with Claude Code (docs/releasing-with-claude-code.md) are
// procedures people and agents follow literally, so they must not drift from
// the code they describe. These tests fail when either names a make target,
// variable, script, script subcommand, slash command, /release form, token
// permission, case id or link that does not exist, or quotes a message the
// scripts no longer print.
package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	runbook         = "../docs/release-runbook.md"
	claudeCodePage  = "../docs/releasing-with-claude-code.md"
	agentsDoc       = "../docs/governance/agents.md"
	releaseCommand  = "../.claude/commands/release.md"
	releaseOperator = "../.claude/agents/release-operator.md"
)

// The procedure documents checked alike.
var releaseDocs = []string{runbook, claudeCodePage}

// The scripts whose messages the documents may quote.
var messageSources = []string{
	"release-preflight.sh",
	"release-version.sh",
	"release.sh",
	"release-prep.sh",
	"release-mode.sh",
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
	shellVariable = regexp.MustCompile(`\$\{[^}]*\}|\$\([^)]*\)|\$[A-Za-z_][A-Za-z0-9_]*|%s|%d`)
	stringLiteral = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	releaseForm   = regexp.MustCompile("`(/release(?: [a-z-]+)+)`")
	formsDecl     = regexp.MustCompile(`<!-- release-forms: ([a-z ]+); flags: ([a-z -]+) -->`)
	caseID        = regexp.MustCompile(`\b([EX](?:[1-9]|1[0-9]))\b`)
	messageQuote  = regexp.MustCompile(`^(release|release preflight|release-prep): `)
	modeLineDecl  = regexp.MustCompile(`(?m)^line "([^"]+)"`)
	concrete      = regexp.MustCompile(`https?://\S+|v\d+\.\d+\.\d+(?:-rc\.\d+)?|\b[0-9a-f]{4}…`)
	versionLine   = regexp.MustCompile(`^release: v\d+\.\d+\.\d+(?:-rc\.\d+)? — (.+)$`)
	permissionRow = regexp.MustCompile(`^\|\s*(?:\*\*)?([A-Z][A-Za-z ,]+?)(?:\*\*)?\s*\|\s*(?:\*\*)?([A-Za-z -]+?)(?:\*\*|[.—(]|\s*\|)`)
)

// GitHub's fine-grained repository permissions the token tables name.
var tokenPermissions = []string{
	"Actions", "Administration", "Contents", "Deployments", "Environments", "Issues",
	"Metadata", "Packages", "Pull requests", "Secrets", "Webhooks", "Workflows",
}

// Every `make <target> VAR=…` the documents show is a Makefile target whose
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
	for _, doc := range releaseDocs {
		found := 0
		for _, m := range makeCall.FindAllStringSubmatch(commandText(readFile(t, doc)), -1) {
			found++
			recipe, ok := recipes[m[1]]
			if !ok {
				t.Errorf("%s runs `make %s`, which the Makefile does not define", doc, m[1])
				continue
			}
			for _, v := range makeVar.FindAllStringSubmatch(m[2], -1) {
				if !strings.Contains(recipe, "$("+v[1]+")") {
					t.Errorf("%s passes %s= to `make %s`, which its recipe ignores", doc, v[1], m[1])
				}
			}
		}
		if found < 5 {
			t.Fatalf("found only %d make commands in %s; the pattern is broken", found, doc)
		}
	}
}

// Every scripts/… path exists, every release-version.sh subcommand is one the
// script accepts, and every slash command has a definition.
func TestRunbookScriptsAndCommandsExist(t *testing.T) {
	versionScript := readFile(t, "release-version.sh")
	for _, doc := range releaseDocs {
		text := readFile(t, doc)
		for _, p := range scriptPath.FindAllString(text, -1) {
			p = strings.TrimRight(p, ".")
			if _, err := os.Stat(filepath.Join("..", p)); err != nil {
				t.Errorf("%s names %s, which does not exist", doc, p)
			}
		}
		for _, m := range versionCmd.FindAllStringSubmatch(text, -1) {
			if !strings.Contains(versionScript, "        "+m[1]+")") {
				t.Errorf("%s runs release-version.sh %s, which the script does not accept", doc, m[1])
			}
		}
		for _, m := range slashCommand.FindAllStringSubmatch(text, -1) {
			if _, err := os.Stat(filepath.Join("..", ".claude", "commands", m[1]+".md")); err != nil {
				t.Errorf("%s names /%s, which has no .claude/commands/%s.md", doc, m[1], m[1])
			}
		}
	}
}

// commandText returns what a document shows as commands: its code spans and
// the lines of its code blocks, so prose ("make this a minor release") is not
// mistaken for one.
func commandText(doc string) string {
	var out, prose []string
	inBlock := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock {
			out = append(out, line)
		} else {
			prose = append(prose, line)
		}
	}
	// Code spans from the prose only, line by line: a fence's backticks, or
	// a span broken across lines, would otherwise pair with the next span's
	// and turn prose into a "span".
	for _, line := range prose {
		for _, m := range backticked.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return strings.Join(out, "\n")
}

// messageLiterals returns every double-quoted string in the message sources'
// code (comment lines skipped, read line by line so an apostrophe in prose
// cannot swallow what follows), with
// shell variables and printf verbs replaced by "…" and escaped quotes
// unescaped: how the documents write a message that names a version, commit
// or link.
func messageLiterals(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range messageSources {
		for _, line := range strings.Split(readFile(t, f), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, m := range stringLiteral.FindAllStringSubmatch(line, -1) {
				out = appendLiteral(out, m[1])
			}
		}
	}
	return out
}

// appendLiteral normalizes one literal and keeps it if it is long enough to
// be a message.
func appendLiteral(out []string, lit string) []string {
	lit = strings.ReplaceAll(lit, `\"`, `"`)
	lit = strings.ReplaceAll(lit, "\\`", "`")
	lit = shellVariable.ReplaceAllString(lit, "…")
	if len(lit) >= 12 {
		out = append(out, lit)
	}
	return out
}

// wildcard compiles s into a regexp in which every "…" matches anything.
func wildcard(s string) *regexp.Regexp {
	var parts []string
	for _, frag := range strings.Split(s, "…") {
		parts = append(parts, regexp.QuoteMeta(frag))
	}
	return regexp.MustCompile(strings.Join(parts, ".*"))
}

// printed reports whether quote is a message some script prints. Either the
// quote, with its "…", is a fragment of a script's literal (the runbook's F1
// table), or a script's literal, with its variables, matches inside the quote
// once the quote's concrete versions, links and abbreviated SHAs are
// abstracted (a transcript line).
func printed(quote string, literals []string) bool {
	// The version line is composed of two parts: release.sh prints
	// "release: <version> — <derivation>", and the derivation is
	// release-version.sh's. Check the derivation on its own.
	if m := versionLine.FindStringSubmatch(quote); m != nil {
		how, _, _ := strings.Cut(m[1], "; newest stable tag: ")
		how = concrete.ReplaceAllString(how, "…")
		for _, lit := range literals {
			if regexp.MustCompile("^" + wildcard(lit).String() + "$").MatchString(how) {
				return true
			}
		}
		return false
	}
	stripped := messageQuote.ReplaceAllString(quote, "")
	pattern := wildcard(stripped)
	abstract := concrete.ReplaceAllString(quote, "…")
	for _, lit := range literals {
		if pattern.MatchString(lit) {
			return true
		}
		// A literal too generic to identify a message ("release: …", what
		// die prints) would match anything; it needs some fixed text.
		if len(strings.ReplaceAll(lit, "…", "")) >= 15 && wildcard(lit).MatchString(abstract) {
			return true
		}
	}
	return false
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
	literals := messageLiterals(t)
	quoted := 0
	for _, row := range strings.Split(text[start:end], "\n") {
		if !strings.HasPrefix(row, "| `") {
			continue
		}
		cell := strings.SplitN(row, "|", 3)[1]
		for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
			quoted++
			if !printed(m[1], literals) {
				t.Errorf("F1 quotes %q, which no release script prints", m[1])
			}
		}
	}
	if quoted < 10 {
		t.Fatalf("found only %d quoted messages in F1; the table or the pattern changed", quoted)
	}
}

func runbookText(t *testing.T) string {
	t.Helper()
	return readFile(t, runbook)
}

// quotedMessages returns every `release…: …` line the document quotes, in a
// code span or a code block, with "# " comment markers removed.
func quotedMessages(doc string) []string {
	var out []string
	for _, line := range strings.Split(commandText(doc), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if messageQuote.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// Every release message either document quotes is one a script prints; every
// mode line is exactly one of the four scripts/test-release-mode.sh asserts.
func TestDocsQuoteRealReleaseMessages(t *testing.T) {
	literals := messageLiterals(t)
	var modeLines []string
	for _, m := range modeLineDecl.FindAllStringSubmatch(readFile(t, "test-release-mode.sh"), -1) {
		modeLines = append(modeLines, m[1])
	}
	if len(modeLines) != 4 {
		t.Fatalf("test-release-mode.sh asserts %d mode lines, want 4", len(modeLines))
	}
	for _, doc := range releaseDocs {
		quotes := quotedMessages(readFile(t, doc))
		if len(quotes) < 5 {
			t.Fatalf("%s quotes only %d release messages; the pattern is broken", doc, len(quotes))
		}
		for _, q := range quotes {
			if strings.HasPrefix(q, "release: mode ") {
				if !slices.Contains(modeLines, q) {
					t.Errorf("%s quotes the mode line %q, which release.sh never prints", doc, q)
				}
				continue
			}
			if !printed(q, literals) {
				t.Errorf("%s quotes %q, which no release script prints", doc, q)
			}
		}
	}
}

// /release accepts exactly minor, patch, rc and stable, each optionally with
// --dry-run; every form the page shows is one of those.
func TestReleaseCommandFormsMatchThePage(t *testing.T) {
	command := readFile(t, releaseCommand)
	decl := formsDecl.FindStringSubmatch(command)
	if decl == nil {
		t.Fatal(".claude/commands/release.md declares no release-forms")
	}
	words := strings.Fields(decl[1])
	flags := strings.Fields(decl[2])
	if got := strings.Join(words, " ") + "; " + strings.Join(flags, " "); got != "minor patch rc stable; --dry-run" {
		t.Errorf("/release declares %q, want exactly minor patch rc stable; --dry-run", got)
	}
	accepted := map[string]bool{}
	for _, w := range words {
		accepted["/release "+w] = true
		for _, f := range flags {
			accepted["/release "+w+" "+f] = true
		}
	}
	page := readFile(t, claudeCodePage)
	shown := 0
	for _, section := range []string{"## How to ask", "## Quick reference"} {
		start := strings.Index(page, section)
		if start < 0 {
			t.Fatalf("the page has no %q section", section)
		}
		end := strings.Index(page[start+len(section):], "\n## ")
		body := page[start:]
		if end >= 0 {
			body = page[start : start+len(section)+end]
		}
		for _, m := range releaseForm.FindAllStringSubmatch(body, -1) {
			shown++
			if !accepted[m[1]] {
				t.Errorf("%s shows `%s`, which /release does not accept", section, m[1])
			}
		}
	}
	for _, m := range releaseForm.FindAllStringSubmatch(page, -1) {
		if !accepted[m[1]] {
			t.Errorf("the page shows `%s`, which /release does not accept", m[1])
		}
	}
	if shown < 6 {
		t.Fatalf("found only %d /release forms in How to ask and Quick reference", shown)
	}
	for _, text := range []string{command, readFile(t, releaseOperator)} {
		for _, m := range backticked.FindAllStringSubmatch(text, -1) {
			cmd := m[1]
			if (strings.HasPrefix(cmd, "make release ") || strings.HasPrefix(cmd, "make release-prep ")) &&
				strings.Contains(cmd, "=") && !strings.Contains(cmd, "MODE=agent") {
				t.Errorf("%q runs without MODE=agent", cmd)
			}
		}
	}
}

// permissions reads a token table: permission → "rw", "r" or "none".
func permissions(t *testing.T, doc string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(readFile(t, doc), "\n") {
		m := permissionRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level := strings.ToLower(strings.TrimSpace(m[2]))
		switch {
		case strings.HasPrefix(level, "read and write"):
			level = "rw"
		case strings.HasPrefix(level, "read"):
			level = "r"
		case strings.HasPrefix(level, "no access"), strings.HasPrefix(level, "none"):
			level = "none"
		default:
			continue
		}
		for _, name := range strings.Split(m[1], ",") {
			name = strings.TrimSpace(name)
			if slices.Contains(tokenPermissions, name) {
				out[name] = level
			}
		}
	}
	return out
}

// The token's permissions are the same in the page, the runbook and
// agents.md, and every one of GitHub's permissions the tables name is set.
func TestTokenTablesAgree(t *testing.T) {
	want := permissions(t, agentsDoc)
	if len(want) != len(tokenPermissions) {
		var got []string
		for k := range want {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Fatalf("agents.md's token table names %v, want all of %v", got, tokenPermissions)
	}
	for _, doc := range releaseDocs {
		got := permissions(t, doc)
		for _, name := range tokenPermissions {
			if got[name] != want[name] {
				t.Errorf("%s gives %s %q, agents.md %q", doc, name, got[name], want[name])
			}
		}
	}
	if want["Deployments"] != "none" || want["Workflows"] != "none" || want["Administration"] != "none" || want["Environments"] != "none" {
		t.Errorf("the token may hold Deployments, Workflows, Administration or Environments: %v", want)
	}
}

// Every E/X case the page, runbook, operator or command refers to is a case
// the page has.
func TestCaseIDsExist(t *testing.T) {
	page := readFile(t, claudeCodePage)
	have := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^### ([EX]\d+)\.`).FindAllStringSubmatch(page, -1) {
		have[m[1]] = true
	}
	if len(have) != 26 {
		t.Fatalf("the page has %d cases, want E1–E8 and X1–X18", len(have))
	}
	for _, f := range []string{claudeCodePage, runbook, releaseOperator, releaseCommand, agentsDoc} {
		for _, m := range caseID.FindAllStringSubmatch(readFile(t, f), -1) {
			if !have[m[1]] {
				t.Errorf("%s refers to %s, which the page does not have", f, m[1])
			}
		}
	}
}

// Every relative link resolves to a file, and every #anchor into a Markdown
// file to one of its headings.
func TestRunbookLinksResolve(t *testing.T) {
	for _, doc := range releaseDocs {
		text := readFile(t, doc)
		dir := filepath.Dir(doc)
		for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
			link := m[1]
			if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
				continue
			}
			path, anchor, _ := strings.Cut(link, "#")
			target := doc
			if path != "" {
				target = filepath.Join(dir, path)
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links %s, which does not exist", doc, link)
				continue
			}
			if anchor != "" && strings.HasSuffix(target, ".md") && !hasAnchor(readFile(t, target), anchor) {
				t.Errorf("%s links %s, but %s has no such heading", doc, link, target)
			}
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
