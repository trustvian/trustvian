package webui_test

// Change Impact against a real browser (task 107-C): the page is served with a
// /v1 that answers the report route with a golden version 2 document — the
// report route's own output for a fixture execution, from
// cmd/trustvian-ci-render/testdata/golden — and the rendered DOM is walked
// against that document.
//
// Needs a Chrome or Chromium. TRUSTVIAN_TEST_CHROME names one; otherwise the
// usual names on PATH and the macOS application are tried, and without one
// the test skips, as the PostgreSQL tests do without a database.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"trustvian-platform/webui"
)

func chromePath(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("TRUSTVIAN_TEST_CHROME"); path != "" {
		return path
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	const mac = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(mac); err == nil {
		return mac
	}
	t.Skip("no Chrome or Chromium found; set TRUSTVIAN_TEST_CHROME to run the browser check")
	return ""
}

// goldenReport is one golden version 2 document, raw and decoded.
func goldenReport(t *testing.T, name string) ([]byte, any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "trustvian-ci-render", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// renderedChangeImpact serves the WebUI with a /v1 that knows one report,
// opens Change Impact on it through the fragment, and returns the report
// host's serialized DOM.
func renderedChangeImpact(t *testing.T, id string, report []byte) string {
	t.Helper()
	chrome := chromePath(t)
	ui, err := webui.NewHandler()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/scenario-executions/"+id+"/report" {
			_, _ = w.Write(report)
			return
		}
		// Everything else is absent: the page must still render the report.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"version":"1","error":{"code":"not_found","message":"not in this test"}}`))
	})
	mux.Handle("/", ui)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox",
		"--no-first-run", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=5000", "--dump-dom",
		server.URL+"/#execution="+id)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	dom := string(out)
	start := strings.Index(dom, `id="impact-report"`)
	if start < 0 {
		t.Fatalf("the page has no report host:\n%.2000s", dom)
	}
	end := strings.Index(dom[start:], `<h2 class="region-title">Or compare two runs</h2>`)
	if end < 0 {
		t.Fatal("the report host is not followed by the run comparison")
	}
	return dom[start : start+end]
}

var (
	sourceSpan = regexp.MustCompile(`<span class="src[^"]*" data-source="([^"]+)">([^<]*)</span>`)
	vocabSpan  = regexp.MustCompile(`<span class="vocab" data-vocab="">[^<]*</span>`)
	localNote  = regexp.MustCompile(`<p class="panel-meta" data-local="">[^<]*</p>`)
	labels     = regexp.MustCompile(`(?s)<(th|caption)[^>]*>.*?</(th|caption)>`)
	tags       = regexp.MustCompile(`<[^>]*>`)
	pathStep   = regexp.MustCompile(`([^.\[\]]+)|\[(\d+)\]`)
)

// valueAt resolves a data-source path in the decoded document.
func valueAt(doc any, path string) (string, bool) {
	for _, step := range pathStep.FindAllStringSubmatch(path, -1) {
		switch {
		case step[1] != "":
			m, ok := doc.(map[string]any)
			if !ok {
				return "", false
			}
			if doc, ok = m[step[1]]; !ok {
				return "", false
			}
		default:
			a, ok := doc.([]any)
			i, _ := strconv.Atoi(step[2])
			if !ok || i >= len(a) {
				return "", false
			}
			doc = a[i]
		}
	}
	switch v := doc.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	}
	return "", false
}

// TestChangeImpactShowsOnlyTheReportsFigures walks every figure Change Impact
// renders for a golden report: each sits in an element naming the member it
// came from, and its text is that member's value exactly. Once those, the
// closed-vocabulary labels and the page's own read time are removed, no digit
// is left: the browser formatted no number of its own.
func TestChangeImpactShowsOnlyTheReportsFigures(t *testing.T) {
	for _, name := range []string{"v2-model-change", "v2-gate-not-recorded", "v2-deferred-gate"} {
		t.Run(name, func(t *testing.T) {
			raw, doc := goldenReport(t, name)
			id, _ := valueAt(doc, "execution_id")
			host := renderedChangeImpact(t, id, raw)

			matches := sourceSpan.FindAllStringSubmatch(host, -1)
			if len(matches) < 20 {
				t.Fatalf("only %d sourced figures rendered:\n%.3000s", len(matches), host)
			}
			for _, m := range matches {
				want, ok := valueAt(doc, m[1])
				if !ok {
					t.Errorf("a figure names %s, which the document does not hold", m[1])
					continue
				}
				if got := html.UnescapeString(m[2]); got != want {
					t.Errorf("%s renders %q; the document says %q", m[1], got, want)
				}
			}

			rest := sourceSpan.ReplaceAllString(host, "")
			rest = vocabSpan.ReplaceAllString(rest, "")
			rest = localNote.ReplaceAllString(rest, "")
			rest = labels.ReplaceAllString(rest, "")
			text := html.UnescapeString(tags.ReplaceAllString(rest, " "))
			// The one fixed sentence that names a schema version.
			text = strings.ReplaceAll(text, "(schema 14)", "")
			if digit := regexp.MustCompile(`\S*[0-9]\S*`).FindString(text); digit != "" {
				t.Errorf("Change Impact renders %q outside any document field", digit)
			}
		})
	}
}

// The report's sections are all on the page, and a gate that was not
// recorded shows no check and no verdict.
func TestChangeImpactRendersEverySection(t *testing.T) {
	raw, _ := goldenReport(t, "v2-model-change")
	host := renderedChangeImpact(t, "golden-model-change", raw)
	for _, section := range []string{"Sameness", "Behaviors", "Targets", "Tokens and cost", "Gate", "Suggestions"} {
		if !strings.Contains(host, fmt.Sprintf(`<h3 class="impact-section">%s</h3>`, section)) {
			t.Errorf("the %s section is missing", section)
		}
	}
	for _, want := range []string{"Verdict: ", "Observations of ", "priced by "} {
		if !strings.Contains(html.UnescapeString(host), want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	raw, _ = goldenReport(t, "v2-gate-not-recorded")
	host = renderedChangeImpact(t, "golden-pre-v14", raw)
	if !strings.Contains(host, "Gate: not recorded.") || strings.Contains(host, "Verdict: ") ||
		strings.Contains(host, "comparison.gate.checks") {
		t.Errorf("a gate not recorded renders a check or a verdict:\n%.3000s", host)
	}
}

// TestChangeImpactComputesNothing is the static half of the browser check:
// the module formats no number and compares no count. The arithmetic and
// numeric formatting a page would need to derive a figure are absent.
func TestChangeImpactComputesNothing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("assets", "views", "change-impact.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []*regexp.Regexp{
		regexp.MustCompile(`Math\.`),
		regexp.MustCompile(`\.toFixed\(|\.toPrecision\(|toLocaleString\(|Intl\.`),
		regexp.MustCompile(`\bparseInt\(|\bparseFloat\(|\bNumber\(|\bBigInt\(`),
		regexp.MustCompile(`\.(actual|bound|max|count|calls_total|http_429|cost_micros|input|output|observed|unobserved)\s*[-+*/<>]`),
		regexp.MustCompile(`[-+*/<>]=?\s*\w+\.(actual|bound|max|count|calls_total|http_429|cost_micros)\b`),
		regexp.MustCompile(`\bfetch\(|/v1/|setTimeout\(|setInterval\(`),
	} {
		if match := forbidden.FindString(source); match != "" {
			t.Errorf("change-impact.js contains %q; every figure is the report's own", match)
		}
	}
	// It reads the report's display strings rather than deriving them.
	for _, member := range []string{"reference_presence", "candidate_presence", "_calls_per_run_mean", "call_ratio",
		"frequency_checks", "missing_evidence", "present_in", "pricing_digest", "cost_reason", "same_model"} {
		if !strings.Contains(source, member) {
			t.Errorf("change-impact.js never reads %s", member)
		}
	}
}
