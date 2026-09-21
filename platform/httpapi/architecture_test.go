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
