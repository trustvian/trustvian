package main

// trustvian eval report, task 107: one GET, the server's body printed
// unchanged in both output modes, and the eval family's exit codes — a FAIL
// verdict in the report is not a failed command.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/internal/cirender"
)

const reportBody = `{"version":"2","scenario":{"name":"support","runs":3},"execution_id":"exec/1",` +
	`"comparison":{"gate":{"verdict":"fail","state":"recorded"}},"extra":"kept"}`

func TestEvalReportPrintsTheDocumentUnchanged(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "report", "exec/1"},
		{"eval", "report", "exec/1", "--json"},
		{"eval", "report", "--json", "exec/1"},
	} {
		api := newFakeAPI(t)
		api.reply(200, reportBody)
		result := runPlatformCLI(t, append(args, "--api-url", api.url())...)
		result.mustExit(t, exitOK, strings.Join(args, " "))
		request := api.only()
		if request.method != "GET" || request.escapedPath != "/v1/scenario-executions/exec%2F1/report" {
			t.Fatalf("request = %s %s", request.method, request.escapedPath)
		}
		if result.stdout != reportBody+"\n" {
			t.Fatalf("stdout %q is not the server's body", result.stdout)
		}
	}
}

func TestEvalReportExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"no id", []string{"eval", "report"}, exitUsage},
		{"two ids", []string{"eval", "report", "a", "b"}, exitUsage},
		{"unknown flag", []string{"eval", "report", "a", "--style", "md"}, exitUsage},
		{"unknown format", []string{"eval", "report", "a", "--format", "html"}, exitUsage},
		{"two outputs", []string{"eval", "report", "a", "--format", "md", "--json"}, exitUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := runPlatformCLI(t, append(tt.args, "--api-url", "http://127.0.0.1:1")...)
			result.mustExit(t, tt.want, tt.name)
		})
	}
	for status, body := range map[int]string{
		404: `{"error":{"code":"not_found","message":"no such execution"}}`,
		409: `{"error":{"code":"conflict","message":"execution is running"}}`,
	} {
		api := newFakeAPI(t)
		api.reply(status, body)
		result := runPlatformCLI(t, "eval", "report", "e1", "--api-url", api.url())
		result.mustExit(t, exitOperational, "API refusal")
		if result.stdout != "" {
			t.Fatalf("a refusal printed %q", result.stdout)
		}
	}
}

// --format md is the one renderer's bytes for the served document, and a
// document the renderer refuses is an operational failure, its no-verdict
// rendering still printed.
func TestEvalReportFormatMarkdown(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "trustvian-ci-render", "testdata", "golden", "v2-model-change.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := newFakeAPI(t)
	api.reply(200, string(raw))
	result := runPlatformCLI(t, "eval", "report", "golden-model-change", "--format", "md", "--api-url", api.url())
	result.mustExit(t, exitOK, "--format md")
	if want := cirender.RenderDocument(raw, cirender.DefaultLimits).Markdown; result.stdout != want {
		t.Fatalf("stdout is not the renderer's bytes:\n%s", result.stdout)
	}

	api = newFakeAPI(t)
	api.reply(200, reportBody)
	result = runPlatformCLI(t, "eval", "report", "exec/1", "--format", "md", "--api-url", api.url())
	result.mustExit(t, exitOperational, "a document the renderer refuses")
	if !strings.HasPrefix(result.stdout, "### Trustvian behavioral gate — no verdict") ||
		!strings.Contains(result.stderr, "the report could not be rendered") {
		t.Fatalf("stdout %q\nstderr %q", result.stdout, result.stderr)
	}
}

// The task's acceptance test: for every golden document, version 1 and
// version 2, `trustvian eval report --format md` prints exactly the bytes
// `trustvian-ci-render --document` prints for the same document.
func TestEvalReportMarkdownIsTheCIRenderersForEveryGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("builds trustvian-ci-render")
	}
	bin := filepath.Join(t.TempDir(), "trustvian-ci-render")
	if out, err := exec.Command("go", "build", "-o", bin, "../trustvian-ci-render").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	testdata := filepath.Join("..", "trustvian-ci-render", "testdata")
	documents, err := filepath.Glob(filepath.Join(testdata, "golden", "v2-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := filepath.Glob(filepath.Join(testdata, "artifacts", "*", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	documents = append(documents, artifacts...)
	if len(documents) < 6+8 {
		t.Fatalf("only %d golden documents: %v", len(documents), documents)
	}
	for _, document := range documents {
		t.Run(filepath.Base(filepath.Dir(document))+"/"+filepath.Base(document), func(t *testing.T) {
			raw, err := os.ReadFile(document)
			if err != nil {
				t.Fatal(err)
			}
			api := newFakeAPI(t)
			api.reply(200, string(raw))
			cli := runPlatformCLI(t, "eval", "report", "e1", "--format", "md", "--api-url", api.url())
			cli.mustExit(t, exitOK, document)

			cmd := exec.Command(bin, "--document", document)
			var stdout strings.Builder
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				t.Fatalf("trustvian-ci-render --document %s: %v", document, err)
			}
			if cli.stdout != stdout.String() {
				t.Fatalf("eval report --format md and trustvian-ci-render --document differ:\n%s\n---\n%s",
					cli.stdout, stdout.String())
			}
		})
	}
}
