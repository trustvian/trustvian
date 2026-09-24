package trustvianprocessor_test

// The boundary guard.
//
// ADR 0022 points the platform at the core, and ADR 0033 keeps the CLI from
// closing that loop. Task 073 puts the same pressure on this module: the
// processor now talks to the control plane, and importing trustvian-platform
// would remove a serialization hop and look like a simplification while
// dragging the platform's SQLite driver, schema and domain types into every
// Collector distribution built from this component.
//
// Prose does not fail a build. This does.

import (
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImports must never appear in this module, in source or in tests.
//
// Tests are included deliberately: go.mod does not distinguish a test-only
// dependency, so an import in a _test.go file reverses the module edge just
// as thoroughly as one in shipped code.
var forbiddenImports = []string{
	"trustvian-platform",
	// Direct persistence. The platform database's invariants live in
	// ControlPlane, and a second writer reaching past them is how a store
	// acquires states its own code believes impossible.
	"database/sql",
	"modernc.org/sqlite",
}

// forbiddenIdentifiers are platform-side concepts whose appearance would mean
// this module had started doing the control plane's job.
var forbiddenIdentifiers = []string{
	"ControlPlane",
	"SQLiteStore",
	"ControlStore",
	"EvaluationStore",
	"EvaluationIngestStore",
	"EvaluateEvaluationGate",
	"CompareBehaviorSnapshots",
	"NewEvaluationScorecard",
	"BehaviorCollector",
	"EvaluationScorecard",
}

// goFiles returns every Go file in this module, tests included.
func goFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no Go files; the guard would pass vacuously")
	}
	return files
}

// codeWithoutComments returns a file's source with comments removed.
//
// The guards assert that certain things are absent from the *code*. A comment
// explaining why something is absent is evidence of intent, not a violation,
// and scanning raw text would fail the build for having documented a
// decision.
func codeWithoutComments(t *testing.T, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	var out strings.Builder
	if err := printer.Fprint(&out, fset, file); err != nil {
		t.Fatalf("printing %s: %v", name, err)
	}
	return out.String()
}

func TestProcessorImportsNothingFromThePlatform(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range goFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquoting import in %s: %v", name, err)
			}
			for _, forbidden := range forbiddenImports {
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Errorf("%s imports %q; the processor reaches the control plane "+
						"over /v1 only (ADR 0022, ADR 0033)", name, path)
				}
			}
		}
	}
}

func TestProcessorNamesNoPlatformConcept(t *testing.T) {
	for _, name := range goFiles(t) {
		if name == "architecture_test.go" {
			// This file names them all, on purpose.
			continue
		}
		code := codeWithoutComments(t, name)
		for _, identifier := range forbiddenIdentifiers {
			if strings.Contains(code, identifier) {
				t.Errorf("%s names %q; control-plane logic belongs to the control plane",
					name, identifier)
			}
		}
	}
}

// TestModuleDoesNotRequireThePlatform asks the module files rather than the
// source, because a dependency can arrive without an import line appearing in
// the package a reader happens to open.
func TestModuleDoesNotRequireThePlatform(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(content), "trustvian-platform") {
			t.Errorf("%s names trustvian-platform; the module edge must stay absent", name)
		}
	}
}
