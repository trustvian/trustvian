package main

// The boundary guard.
//
// Every rule in docs/adr/0033-developer-cli-is-a-thin-http-adapter.md is a
// statement about what this package must *not* contain, and prose does not
// fail a build. The violations are all one import or one call away, and each
// would look like a simplification to whoever made it: importing the platform
// removes serialization, touching SQLite removes a hop, recomputing the gate
// removes a round trip.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImports must never appear in the root module's CLI.
var forbiddenImports = []string{
	// The module edge. ADR 0022 points one way; this would close the loop.
	"trustvian-platform",
	// Direct persistence. The platform database's invariants live in
	// ControlPlane, not in the file.
	"database/sql",
	"modernc.org/sqlite",
	// Identity generation. ADR 0025 puts IDs in the caller's hands.
	"github.com/google/uuid",
}

// forbiddenIdentifiers are platform-side concepts that would indicate the CLI
// had started doing the server's job.
var forbiddenIdentifiers = []string{
	"SQLiteStore",
	"ControlPlane",
	"ControlStore",
	"EvaluationStore",
	"EvaluationIngestStore",
	"EvaluationGatePolicy",
	"EvaluateEvaluationGate",
	"BehaviorDiff",
	"CompareBehaviorSnapshots",
	"EvaluationScorecard",
	"NewEvaluationScorecard",
	"BehaviorCollector",
}

// TestCLIImportsNothingFromThePlatform walks the package's own source.
func TestCLIImportsNothingFromThePlatform(t *testing.T) {
	for _, name := range goFiles(t, false) {
		file := parseFile(t, name, parser.ImportsOnly)
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", name, spec.Path.Value, err)
			}
			for _, forbidden := range forbiddenImports {
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Errorf("%s imports %q; the CLI's only edge onto the platform is HTTP /v1",
						name, path)
				}
			}
		}
	}
}

// goFiles lists this package's sources. parser.ParseDir would be shorter, but
// it is deprecated in favour of a module the root go.mod must not take on.
func goFiles(t *testing.T, includeTests bool) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	return names
}

func parseFile(t *testing.T, name string, mode parser.Mode) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, nil, mode)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return file
}

// TestCLIDoesNotNamePlatformInternals is deliberately textual.
//
// An import check alone would miss a copied implementation: someone who
// reimplements the gate in this package imports nothing new. A name like
// EvaluateEvaluationGate appearing here means the server's job has started
// moving client-side, whatever the import list says.
func TestCLIDoesNotNamePlatformInternals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// This file names them all, on purpose.
		if name == "cli_architecture_test.go" {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for _, forbidden := range forbiddenIdentifiers {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s mentions %q; that is a control-plane concept, "+
					"and the CLI must consume its result rather than reproduce it",
					name, forbidden)
			}
		}
	}
}

// TestRootModuleDoesNotDependOnThePlatform guards the other half.
//
// A stray `go mod tidy` after an accidental import would add the requirement
// quietly, and go.mod is not something a reviewer reads line by line.
func TestRootModuleDoesNotDependOnThePlatform(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum"} {
		path := filepath.Join("..", "..", name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		if strings.Contains(string(content), "trustvian-platform") {
			t.Errorf("root %s references trustvian-platform; "+
				"the dependency direction must stay one-way (ADR 0022)", name)
		}
	}
}

// TestCLIStartsNoListener keeps a client from becoming a server.
func TestCLIStartsNoListener(t *testing.T) {
	forbidden := []string{
		"ListenAndServe", "net.Listen", "http.Server{", "httptest.NewServer",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// Tests may serve; the shipped binary may not.
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for _, pattern := range forbidden {
			if strings.Contains(string(source), pattern) {
				t.Errorf("%s contains %q; task 062 owns the local runtime, not the CLI",
					name, pattern)
			}
		}
	}
}

// TestPlatformCommandsDeclareNoTokenFlag keeps a speculative auth contract out.
//
// Task 070 owns authentication. A --token flag now would freeze a credential
// shape before there is anything to authenticate against, and a half-designed
// auth surface is far harder to remove than to add.
func TestPlatformCommandsDeclareNoTokenFlag(t *testing.T) {
	forbidden := map[string]bool{
		"token": true, "api-key": true, "apikey": true,
		"password": true, "credential": true, "bearer": true,
		// Task 062 owns local startup; these would imply the CLI runs one.
		"db": true, "database": true, "listen": true, "port": true,
	}

	for _, name := range goFiles(t, false) {
		ast.Inspect(parseFile(t, name, 0), func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall || len(call.Args) == 0 {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			switch selector.Sel.Name {
			case "String", "Bool", "Var", "Int", "Uint64", "Duration":
			default:
				return true
			}
			// flag.FlagSet registration functions take the name first,
			// except Var, where it is second.
			nameArg := call.Args[0]
			if selector.Sel.Name == "Var" {
				if len(call.Args) < 2 {
					return true
				}
				nameArg = call.Args[1]
			}
			literal, isLiteral := nameArg.(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				return true
			}
			flagName, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			if forbidden[flagName] {
				t.Errorf("%s declares a --%s flag; that surface belongs to a later task",
					name, flagName)
			}
			return true
		})
	}
}
