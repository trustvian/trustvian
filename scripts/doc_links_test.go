// This file checks that every relative link and anchor in the current
// documentation resolves.
//
// A renamed heading or a moved file breaks a link silently: the Markdown still
// renders, and the reader finds out by clicking. The current documentation is
// the set a reader starts from; the historical record (docs/adr, docs/archive,
// docs/tasks, docs/results, CHANGELOG.md) is not checked as a source, because it
// is immutable and describes what was true when it was written. A current
// document may still link into it, and those links are checked.
package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// currentDocGlobs is the current documentation, relative to the repository root.
var currentDocGlobs = []string{
	"README.md",
	"CONTRIBUTING.md",
	"docs/*.md",
	"docs/governance/*.md",
	"processor/README.md",
	"deployments/docker-compose/README.md",
	"examples/README.md",
	"examples/*/README.md",
	".github/actions/*/README.md",
	".github/SECURITY.md",
}

var (
	// inlineLink matches [text](target) and ![alt](target). The target stops
	// at whitespace, so an optional "title" is left out.
	inlineLink = regexp.MustCompile(`!?\[[^\]]*\]\(\s*(<[^>]*>|[^)\s]+)[^)]*\)`)
	// referenceDef matches a reference-style definition: [label]: target
	referenceDef = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*(\S+)`)
	// htmlHref matches an href in raw HTML.
	htmlHref = regexp.MustCompile(`<a\s[^>]*href="([^"]+)"`)
	// htmlAnchor matches an explicit anchor in raw HTML.
	htmlAnchor = regexp.MustCompile(`<a\s[^>]*(?:id|name)="([^"]+)"`)
	// inlineCode matches a code span, which holds no link.
	inlineCode = regexp.MustCompile("`+[^`]*`+")
	// lineAnchor matches a GitHub line anchor such as #L12 or #L12-L20.
	lineAnchor = regexp.MustCompile(`^L\d+(-L\d+)?$`)
)

func TestCurrentDocumentationLinksResolve(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	docs := currentDocs(t, root)
	if len(docs) == 0 {
		t.Fatal("no current documentation found; the globs no longer match the repository")
	}
	for _, broken := range brokenLinks(t, root, docs) {
		t.Error(broken)
	}
}

func TestBrokenLinksFindsMissingFilesAndAnchors(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("target.md", "# Target\n\n## `--config <path>`\n\n<a id=\"explicit\"></a>\n")
	doc := write("doc.md", strings.Join([]string{
		"[ok](target.md#--config-path) [ok](target.md#explicit) [ok](#local)",
		"[ok](https://example.com/missing.md) [ok](target.md#L12)",
		"`[ignored](missing.md)`",
		"```",
		"[ignored](missing.md)",
		"```",
		"## Local",
		"[missing file](missing.md)",
		"[missing anchor](target.md#nope)",
		"[root-relative](/docs/x.md)",
	}, "\n"))

	got := brokenLinks(t, root, []string{doc})
	want := []string{
		`doc.md:8: "missing.md": no such file or directory`,
		`doc.md:9: "target.md#nope": no heading or anchor #nope`,
		`doc.md:10: "/docs/x.md" is root-relative; GitHub resolves it against the site, not the repository`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("brokenLinks:\ngot  %q\nwant %q", got, want)
	}
}

func TestHeadingSlugMatchesGitHub(t *testing.T) {
	tests := []struct {
		heading, want string
	}{
		{"Behavioral scenarios (`trustvian eval run`)", "behavioral-scenarios-trustvian-eval-run"},
		{"A suite of scenarios (`--suite`)", "a-suite-of-scenarios---suite"},
		{"`--config <path>`", "--config-path"},
		{"Calibrating `N`, `k` and `j`", "calibrating-n-k-and-j"},
		{"Cold start: two numbers, not one", "cold-start-two-numbers-not-one"},
		{"Step 4 — **Done** <sup>1</sup>", "step-4--done-1"},
		{"See [the guide](guide.md) ##", "see-the-guide"},
		{"max_lost_behaviors", "max_lost_behaviors"},
	}
	for _, tt := range tests {
		t.Run(tt.heading, func(t *testing.T) {
			if got := headingSlug(tt.heading); got != tt.want {
				t.Errorf("headingSlug(%q) = %q, want %q", tt.heading, got, tt.want)
			}
		})
	}
}

// brokenLinks returns one message per relative link in docs whose file or
// anchor does not resolve.
func brokenLinks(t *testing.T, root string, docs []string) []string {
	t.Helper()
	anchors := map[string]map[string]bool{}
	anchorsOf := func(path string) map[string]bool {
		if a, ok := anchors[path]; ok {
			return a
		}
		a := markdownAnchors(t, path)
		anchors[path] = a
		return a
	}

	var broken []string
	for _, doc := range docs {
		rel, _ := filepath.Rel(root, doc)
		rel = filepath.ToSlash(rel)
		for _, l := range markdownLinks(t, doc) {
			if isExternal(l.target) {
				continue
			}
			pathPart, anchor, _ := strings.Cut(l.target, "#")
			target := doc
			if pathPart != "" {
				if strings.HasPrefix(pathPart, "/") {
					broken = append(broken, fmt.Sprintf("%s:%d: %q is root-relative; GitHub resolves it against the site, not the repository", rel, l.line, l.target))
					continue
				}
				target = filepath.Join(filepath.Dir(doc), filepath.FromSlash(pathPart))
				if _, err := os.Stat(target); err != nil {
					broken = append(broken, fmt.Sprintf("%s:%d: %q: no such file or directory", rel, l.line, l.target))
					continue
				}
			}
			if anchor == "" || !strings.EqualFold(filepath.Ext(target), ".md") || lineAnchor.MatchString(anchor) {
				continue
			}
			if !anchorsOf(target)[strings.ToLower(anchor)] {
				broken = append(broken, fmt.Sprintf("%s:%d: %q: no heading or anchor #%s", rel, l.line, l.target, anchor))
			}
		}
	}
	return broken
}

func currentDocs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var docs []string
	for _, g := range currentDocGlobs {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(g)))
		if err != nil {
			t.Fatalf("glob %q: %v", g, err)
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				docs = append(docs, m)
			}
		}
	}
	sort.Strings(docs)
	return docs
}

type link struct {
	target string
	line   int
}

// markdownLinks returns every link target outside a fenced code block.
func markdownLinks(t *testing.T, path string) []link {
	t.Helper()
	var links []link
	forEachProseLine(t, path, func(n int, line string) {
		line = inlineCode.ReplaceAllString(line, "")
		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			links = append(links, link{strings.Trim(m[1], "<>"), n})
		}
		if m := referenceDef.FindStringSubmatch(line); m != nil {
			links = append(links, link{strings.Trim(m[1], "<>"), n})
		}
		for _, m := range htmlHref.FindAllStringSubmatch(line, -1) {
			links = append(links, link{m[1], n})
		}
	})
	return links
}

// markdownAnchors returns the anchors GitHub generates for a file's headings,
// plus any explicit HTML anchor.
func markdownAnchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	anchors := map[string]bool{}
	counts := map[string]int{}
	forEachProseLine(t, path, func(_ int, line string) {
		for _, m := range htmlAnchor.FindAllStringSubmatch(line, -1) {
			anchors[strings.ToLower(m[1])] = true
		}
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "#") {
			return
		}
		text := strings.TrimLeft(trimmed, "#")
		if text != "" && text[0] != ' ' && text[0] != '\t' {
			return // "#tag", not a heading
		}
		slug := headingSlug(text)
		if n := counts[slug]; n > 0 {
			anchors[slug+"-"+strconv.Itoa(n)] = true
		} else {
			anchors[slug] = true
		}
		counts[slug]++
	})
	return anchors
}

var (
	headingLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	headingHTML    = regexp.MustCompile(`<[^>]+>`)
	closingHashes  = regexp.MustCompile(`\s+#+\s*$`)
	notSlugCharset = regexp.MustCompile(`[^\p{L}\p{N}\p{M} _-]`)
)

// headingSlug reproduces GitHub's heading anchor: the rendered text,
// lower-cased, with punctuation other than '-' and '_' removed and each space
// replaced by '-'.
func headingSlug(text string) string {
	text = strings.TrimSpace(text)
	text = closingHashes.ReplaceAllString(text, "")
	text = headingLink.ReplaceAllString(text, "$1")
	// Raw HTML is dropped from the rendered text, but "<path>" inside a code
	// span is text.
	var rendered strings.Builder
	for {
		loc := inlineCode.FindStringIndex(text)
		if loc == nil {
			rendered.WriteString(headingHTML.ReplaceAllString(text, ""))
			break
		}
		rendered.WriteString(headingHTML.ReplaceAllString(text[:loc[0]], ""))
		rendered.WriteString(strings.Trim(text[loc[0]:loc[1]], "`"))
		text = text[loc[1]:]
	}
	text = strings.ReplaceAll(rendered.String(), "*", "")
	text = strings.ToLower(strings.TrimSpace(text))
	text = notSlugCharset.ReplaceAllString(text, "")
	return strings.ReplaceAll(text, " ", "-")
}

// forEachProseLine calls fn for every line outside a fenced code block.
func forEachProseLine(t *testing.T, path string, fn func(n int, line string)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	fence := ""
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if fence == "" {
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				fence = trimmed[:3]
				continue
			}
			fn(i+1, line)
			continue
		}
		if strings.HasPrefix(trimmed, fence) {
			fence = ""
		}
	}
}

func isExternal(target string) bool {
	if i := strings.Index(target, ":"); i > 0 && !strings.ContainsAny(target[:i], "/#.") {
		return true // a scheme: https:, mailto:, …
	}
	return strings.HasPrefix(target, "//")
}
