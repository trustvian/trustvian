package webui

// Structural checks: this package must stay a static transport.
//
// A behavioral test cannot notice a WebUI package that starts holding a
// ControlPlane — it would still serve the right bytes. These scan for the shape
// instead, which is the only thing that catches authority arriving by import.
//
// ADR 0023 observed that each interface is a plausible place to "just compute
// the gate result here". The CLI was protected by the module boundary, where
// importing internal/* across modules is a compile error. This package sits
// *inside* the platform module, where every service is one import away, so the
// protection has to be written down.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// packageSourceFiles returns this package's non-test Go files, parsed.
func packageSourceFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		files[name] = parsed
	}
	if len(files) == 0 {
		t.Fatal("no non-test source files found; the guard would pass vacuously")
	}
	return fset, files
}

// goSourceWithoutComments returns a file's source with every comment blanked.
//
// Needed because this package's comments deliberately name the things the
// guards forbid, to explain why they are not used — handler.go says "a map
// lookup rather than http.FileServer" for exactly that reason. The spec's rule
// applies: when a false positive appears in a comment, strip comments rather
// than weaken the pattern. A guard relaxed to tolerate prose stops catching the
// real thing.
//
// Byte ranges are blanked rather than removed, so reported line numbers still
// match the file.
func goSourceWithoutComments(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, body, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	stripped := []byte(string(body))
	base := fset.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		start := int(group.Pos()) - base
		end := int(group.End()) - base
		for i := start; i < end && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// TestPackageImportsOnlyTheStandardLibrarySubsetItNeeds is the boundary.
//
// An allowlist rather than a denylist: a denylist has to predict what someone
// will reach for, and the whole point is that this package needs almost
// nothing. Anything new has to be added here deliberately, which is the review
// moment the guard exists to create.
func TestPackageImportsOnlyTheStandardLibrarySubsetItNeeds(t *testing.T) {
	allowed := map[string]bool{
		"embed":    true,
		"io/fs":    true,
		"net/http": true,
		"path":     true,
		"sort":     true,
		"strings":  true,
	}

	_, files := packageSourceFiles(t)
	for name, file := range files {
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if !allowed[path] {
				t.Errorf("%s imports %q, which is not on this package's allowlist. "+
					"If it is genuinely needed, add it here and say why in review — "+
					"but note that a platform, store or domain import means the "+
					"WebUI has acquired authority it must not have", name, path)
			}
		}
	}
}

// TestDependencyGraphContainsNoPlatformOrStoreCode checks the transitive graph.
//
// The import allowlist above covers direct imports. This covers the case where
// an allowed package is later swapped for a local one that pulls the platform
// in behind it — go list sees through that, a source scan does not.
func TestDependencyGraphContainsNoPlatformOrStoreCode(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, output)
	}

	forbidden := []string{
		"trustvian-platform",
		"github.com/trustvian/trustvian",
		"database/sql",
		"modernc.org/sqlite",
	}

	for line := range strings.SplitSeq(string(output), "\n") {
		dep := strings.TrimSpace(line)
		if dep == "" || dep == "trustvian-platform/webui" {
			continue
		}
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("dependency graph contains %q; the WebUI package must "+
					"reach neither the platform nor a database", dep)
			}
		}
	}
}

// TestHandlerConstructorTakesNoControlPlane is the signature as boundary.
//
// This is the mutation the spec names first: make NewHandler accept the
// platform service. A package handed a ControlPlane can compute anything the
// control plane can, and no amount of reviewing the body prevents that — so the
// parameter list is what is asserted.
func TestHandlerConstructorTakesNoControlPlane(t *testing.T) {
	_, files := packageSourceFiles(t)

	var found bool
	for name, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "NewHandler" || fn.Recv != nil {
				continue
			}
			found = true
			if count := fn.Type.Params.NumFields(); count != 0 {
				t.Fatalf("%s: NewHandler takes %d parameter(s); it must take none. "+
					"Accepting a ControlPlane, store or option here is how this "+
					"package stops being a static transport", name, count)
			}
		}
	}
	if !found {
		t.Fatal("NewHandler was not found; the guard would pass vacuously")
	}
}

// TestSourceNamesNoPlatformAuthority catches a reference before it is wired.
//
// Identifiers, not imports: a dot-import, a type alias or a stringly-typed
// reflection hop would satisfy the import allowlist while naming the thing. If
// any of these words appear in this package's non-test source, something has
// gone wrong regardless of how it got there.
func TestSourceNamesNoPlatformAuthority(t *testing.T) {
	forbidden := []string{
		"ControlPlane",
		"SQLiteStore",
		"OpenSQLiteStore",
		"EvaluationGatePolicy",
		"EvaluateEvaluationGate",
		"EvaluationScorecard",
		"NewEvaluationScorecard",
		"BehaviorDiff",
		"CompareBehaviorSnapshots",
		"BehaviorCollector",
		"NewBehaviorCollector",
		"EvaluationAggregate",
		"RealtimePublisher",
		"RealtimeBus",
		"DecisionRecord",
	}

	fset, files := packageSourceFiles(t)
	for name, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			for _, bad := range forbidden {
				if ident.Name == bad {
					t.Errorf("%s:%d names %s; the WebUI package must not reference "+
						"platform authority", name,
						fset.Position(ident.Pos()).Line, bad)
				}
			}
			return true
		})
	}
	// Also catch it inside string literals and comments, where an Ident scan
	// would not look — a handler reaching the plane through a registry lookup
	// would still have to name it.
	for name := range files {
		source := goSourceWithoutComments(t, name)
		for _, bad := range []string{"OpenSQLiteStore", "EvaluateEvaluationGate"} {
			if strings.Contains(source, bad) {
				t.Errorf("%s references %s outside a comment", name, bad)
			}
		}
	}
}

// TestNoFilesystemWebRoot keeps assets embedded.
//
// A disk web root reintroduces every path-traversal question embed removes, and
// makes the release artifact depend on files beside it.
func TestNoFilesystemWebRoot(t *testing.T) {
	forbidden := []string{
		"os.Open",
		"os.ReadFile",
		"os.DirFS",
		"http.Dir",
		"http.FileServer",
		"filepath.Join",
	}

	_, files := packageSourceFiles(t)
	for name := range files {
		// Comments stripped: handler.go explains at length why it does not use
		// http.FileServer, and that explanation must not trip the check that
		// it does not.
		source := goSourceWithoutComments(t, name)
		for _, bad := range forbidden {
			if strings.Contains(source, bad) {
				t.Errorf("%s uses %s; assets are embedded and paths are map keys, "+
					"so nothing here should touch a filesystem or serve a directory",
					name, bad)
			}
		}
	}
}

// TestAssetsDirectoryContainsOnlyShippableKinds keeps build output and
// dependency manifests out of the binary.
//
// A lockfile or a node_modules tree appearing here would mean a frontend
// toolchain arrived, which ADR 0036 rules out — and would ship with the binary.
func TestAssetsDirectoryContainsOnlyShippableKinds(t *testing.T) {
	allowedExt := map[string]bool{".html": true, ".js": true, ".css": true}
	forbiddenNames := map[string]bool{
		"package.json":      true,
		"package-lock.json": true,
		"pnpm-lock.yaml":    true,
		"yarn.lock":         true,
		"bun.lockb":         true,
		"node_modules":      true,
		"tsconfig.json":     true,
		"vite.config.js":    true,
		"webpack.config.js": true,
	}

	err := filepath.WalkDir("assets", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if forbiddenNames[base] {
			t.Errorf("%s exists; Task 063 ships no frontend toolchain", path)
		}
		if entry.IsDir() {
			return nil
		}
		if ext := filepath.Ext(base); !allowedExt[ext] {
			t.Errorf("%s has extension %q, which is not a shippable asset kind", path, ext)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}
}
