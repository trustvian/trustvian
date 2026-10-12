package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trustvian/trustvian/internal/cirender"
)

// Every measured report published under docs/results is the renderer's own
// output for the report beside it: report.md is what `trustvian eval report
// --format md` and the pull request comment's renderer print for report.json,
// byte for byte, so a published report cannot drift from its evidence.
func TestPublishedReportsAreTheRenderersOutput(t *testing.T) {
	documents, err := filepath.Glob(filepath.Join("..", "..", "docs", "results", "*", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) == 0 {
		t.Fatal("no published report under docs/results")
	}
	for _, document := range documents {
		markdown := filepath.Join(filepath.Dir(document), "report.md")
		if _, err := os.Stat(markdown); err != nil {
			continue
		}
		t.Run(filepath.Base(filepath.Dir(document)), func(t *testing.T) {
			raw, err := os.ReadFile(document)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(markdown)
			if err != nil {
				t.Fatal(err)
			}
			out := cirender.RenderDocument(raw, cirender.DefaultLimits)
			if out.State != cirender.StateVerdict || out.Rejected != nil {
				t.Fatalf("the published report does not render: %s", out.Reason)
			}
			if out.Markdown != string(want) {
				t.Errorf("%s is not the renderer's output for %s", markdown, document)
			}
		})
	}
}
