package httpapi_test

// Structural checks: the adapter must stay an adapter, and domain values must
// stay off the wire.
//
// A behavioral test cannot notice a handler that starts computing a
// comparison — it would still return the right answer. These scan for the
// shape instead.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// The evaluation chain lives in the control plane, once. A handler calling
// these directly is the duplication ADR 0031 forbids — four calls in a fixed
// order with preconditions between them, which is exactly the shape someone
// reimplements "just for this endpoint".
func TestHTTPAdapterDoesNotComputeEvaluationLogic(t *testing.T) {
	forbidden := []string{
		"CompareBehaviorSnapshots",
		"NewEvaluationScorecard",
		"EvaluateEvaluationGate",
		"NewEvaluationAggregate",
		"NewBehaviorCollector",
		"OpenSQLiteStore",
		"SQLiteStore",

		// Realtime: the adapter subscribes, translates and streams. A
		// transport able to publish could fabricate state a client would
		// believe, so it must not reach the publisher side at all.
		"RealtimePublisher",
		"WithRealtimePublisher",
		"NewInMemoryRealtimeBus",
		"InMemoryRealtimeBus",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	var scanned int
	for _, entry := range entries {
		name := entry.Name()
		// Non-test source only: the tests legitimately build stores and drive
		// the whole chain to prove it works.
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, bad := range forbidden {
				if ident.Name == bad {
					t.Errorf("%s references %q at %s; evaluation logic belongs in the control plane",
						name, bad, fset.Position(ident.Pos()))
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the check would pass vacuously")
	}

	realtimeMappingReadsOnlyTheEvent(t)
}

// realtimeMappingReadsOnlyTheEvent: a realtime frame is a rendering of the
// event the control plane published, and nothing else (task 109).
//
// The frame's actor fields could otherwise be "completed" here — a lookup of
// the observation, or of the run, to fill in what the event did not carry —
// and the frame would then describe a state read at a different moment from
// the one it was published at. So the mapping takes the event as its only
// parameter and calls nothing but the pure helpers listed below.
func realtimeMappingReadsOnlyTheEvent(t *testing.T) {
	t.Helper()
	const name = "newRealtimeEventPayload"
	allowed := map[string]bool{
		"u64": true, "string": true, "formatTime": true, "OrTransport": true,
		"newRealtimeScopeDTO": true, "newBehaviorDescriptorDTO": true,
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mapping.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mapping.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == name {
			fn = f
		}
	}
	if fn == nil {
		t.Fatalf("mapping.go has no %s; the check would pass vacuously", name)
	}
	if params := fn.Type.Params.List; len(params) != 1 || len(params[0].Names) != 1 {
		t.Fatalf("%s must take exactly one parameter, the published event", name)
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var callee string
		switch f := call.Fun.(type) {
		case *ast.Ident:
			callee = f.Name
		case *ast.SelectorExpr:
			callee = f.Sel.Name
		default:
			callee = "<expression>"
		}
		if !allowed[callee] {
			t.Errorf("%s calls %s at %s; a frame is filled from the published event only",
				name, callee, fset.Position(call.Pos()))
		}
		return true
	})
}

// Task 052 made these unexported-field domain values on purpose. A JSON tag
// would make the wire format a property of the domain type: renaming a field
// would silently break clients, and adding one would silently appear on the
// wire. The DTOs own the wire.
func TestDomainTypesCarryNoJSONTags(t *testing.T) {
	for _, value := range []any{
		platform.Project{},
		platform.Agent{},
		platform.Candidate{},
		platform.CandidateMetadata{},
		platform.Environment{},
		platform.EvaluationRun{},
		platform.EvaluationAggregate{},
		platform.BehaviorSnapshot{},
		platform.EvaluationScorecard{},
		platform.EvaluationGateResult{},
		platform.EvaluationGateLimits{},
	} {
		typ := reflect.TypeOf(value)
		t.Run(typ.Name(), func(t *testing.T) {
			for i := range typ.NumField() {
				field := typ.Field(i)
				if tag, present := field.Tag.Lookup("json"); present {
					t.Errorf("%s.%s carries a json tag %q; wire shape belongs to the DTOs",
						typ.Name(), field.Name, tag)
				}
			}
		})
	}
}

// The adapter must not import a database driver or database/sql: persistence
// is the control plane's concern, reached only through its methods.
func TestHTTPAdapterImportsNoDatabase(t *testing.T) {
	forbidden := []string{"database/sql", "modernc.org/sqlite"}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	var scanned int
	for _, entry := range entries {
		name := entry.Name()
		// Non-test source only: a test may legitimately open a store to build
		// a fixture, and one does.
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			for _, bad := range forbidden {
				if path == bad {
					t.Errorf("%s imports %q; persistence belongs behind the control plane", name, bad)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the check would pass vacuously")
	}
}

// The publisher capability must not be reachable from a Handler at all — not
// as a field, not as an option.
func TestHandlerCannotPublish(t *testing.T) {
	handlerType := reflect.TypeOf(httpapi.Handler{})
	publisherType := reflect.TypeOf((*platform.RealtimePublisher)(nil)).Elem()

	for i := range handlerType.NumField() {
		field := handlerType.Field(i)
		if field.Type == publisherType || field.Type.Implements(publisherType) {
			t.Errorf("Handler.%s can publish realtime events; a transport must only subscribe",
				field.Name)
		}
	}
}
