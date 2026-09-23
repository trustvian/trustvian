package localruntime_test

// Backend selection lives here and nowhere else.
//
// Task 064's whole shape is one persistence contract with two implementations,
// and the thing that ruins it is a backend conditional appearing above the
// composition root. `if postgres { A } else { B }` in a service is two
// behaviours, and the one a user believes is whichever deployment they opened —
// the failure ADR 0023 named and ADR 0037 restates.
//
// A behavioural test cannot catch that: both branches would return correct
// answers. This scans for the shape instead.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backendVocabulary is what a layer would have to name to branch on a backend.
var backendVocabulary = []string{
	"BackendPostgres",
	"BackendSQLite",
	"PostgresStore",
	"OpenPostgresStore",
	"PostgresConfig",
	"PostgresOptions",
	"SQLiteStore",
	"OpenSQLiteStore",
	"pgx",
	"pgxpool",
	"pgconn",
	"modernc.org/sqlite",
}

// TestBackendSelectionDoesNotLeakAboveComposition scans every package that must
// stay backend-agnostic.
//
// The composition root is deliberately excluded: choosing is its job. The
// exclusion is stated as a single directory rather than a pattern, so adding a
// package cannot quietly inherit the exemption.
func TestBackendSelectionDoesNotLeakAboveComposition(t *testing.T) {
	agnostic := []struct {
		name string
		dir  string
	}{
		{"httpapi", filepath.Join("..", "httpapi")},
		{"webui", filepath.Join("..", "webui")},
	}

	for _, target := range agnostic {
		t.Run(target.name, func(t *testing.T) {
			files := goSourceFiles(t, target.dir)
			if len(files) == 0 {
				t.Fatalf("no source files found under %s; the guard would pass vacuously", target.dir)
			}
			for name, source := range files {
				for _, word := range backendVocabulary {
					if strings.Contains(source, word) {
						t.Errorf("%s names %q. Backend selection belongs to the "+
							"composition root; a conditional here is two behaviours "+
							"with one contract.", name, word)
					}
				}
			}
		})
	}
}

// TestControlPlaneAndServicesAreBackendAgnostic covers the platform package's
// own service layer.
//
// sqlite.go, postgres.go and their schema files are the implementations and are
// excluded by name. Everything else in that package — the control plane, the
// domain, the gate, the diff, the scorecard, the aggregate, ingest, realtime —
// must not know a backend exists.
func TestControlPlaneAndServicesAreBackendAgnostic(t *testing.T) {
	implementations := map[string]bool{
		"sqlite.go":          true,
		"postgres.go":        true,
		"postgres_schema.go": true,
		// store.go declares the interfaces and the composite, and names neither
		// backend beyond one compile-time assertion per implementation.
		"store.go": true,
		// querier.go carries the shared read seam and names the adapter types.
		"querier.go": true,
	}

	files := goSourceFiles(t, "..")
	if len(files) == 0 {
		t.Fatal("no source files found in the platform package")
	}

	var scanned int
	for name, source := range files {
		if implementations[filepath.Base(name)] {
			continue
		}
		scanned++
		for _, word := range backendVocabulary {
			if strings.Contains(source, word) {
				t.Errorf("%s names %q. Evaluation, diff, scorecard, gate and ingest "+
					"semantics must not depend on which database is underneath.",
					name, word)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("every file was excluded; the guard would pass vacuously")
	}
}

// TestCompositionRootIsTheOnlyPlaceThatChooses states the positive half.
//
// Without it, deleting the selection entirely would satisfy every guard above.
func TestCompositionRootIsTheOnlyPlaceThatChooses(t *testing.T) {
	source := goSourceWithoutComments(t, "runtime.go")

	for _, required := range []string{"BackendSQLite", "BackendPostgres", "openBackend", "resolveBackend"} {
		if !strings.Contains(source, required) {
			t.Errorf("runtime.go no longer names %q; backend selection has moved "+
				"or been removed", required)
		}
	}
	// And it must still refuse rather than default.
	if !strings.Contains(source, "ErrBackendConfiguration") {
		t.Error("runtime.go does not refuse an invalid backend selection")
	}
}

// goSourceFiles reads every non-test .go file in one directory, comments
// stripped.
//
// Comments are removed because these files legitimately discuss the backends in
// prose — ADR references, rationale, the reason a boundary exists. Matching on
// prose would make the guard fire on its own documentation, and loosening the
// pattern to tolerate that is how it stops catching real code.
func goSourceFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	sources := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources[filepath.Join(dir, name)] = goSourceWithoutComments(t, filepath.Join(dir, name))
	}
	return sources
}

// goSourceWithoutComments blanks every comment, preserving offsets.
func goSourceWithoutComments(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, body, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	stripped := []byte(string(body))
	base := fset.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for i := int(group.Pos()) - base; i < int(group.End())-base && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}
