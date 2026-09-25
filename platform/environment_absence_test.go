package platform_test

// What task 065 must NOT do.
//
// A behavioral test cannot notice most of these: an environment that grew a
// `URL` field, a control plane that started dialling one, or a second answer
// to "which environment is next" would all still pass every test that only
// checks results. These scan for the shape and the absence instead, and each
// fails loudly the moment a later change crosses the line.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	platform "trustvian-platform"
)

// environmentSourceFiles are the task's own non-test sources. Named
// explicitly rather than globbed: a check that silently scanned nothing would
// pass forever.
var environmentSourceFiles = []string{"environment.go", "environment_store.go"}

func parseEnvironmentSources(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(environmentSourceFiles))
	for _, name := range environmentSourceFiles {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s is gone; this check would pass vacuously: %v", name, err)
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return fset, files
}

// An environment is a reference with a name, a rank and a status. It is not a
// deployment target, so it holds nothing that could address, authenticate
// against or execute anything — and nothing that would duplicate a field
// another entity owns.
func TestEnvironmentDeclaresNoDeploymentOrSecretField(t *testing.T) {
	forbidden := []string{
		// A deployment target, which an environment is not.
		"URL", "Endpoint", "Host", "Address", "Command", "Script", "Webhook",
		// A credential, which nothing in this repository stores.
		"Credentials", "Credential", "Secret", "Token", "TokenRef", "APIKey", "Password",
		// Matched case-insensitively, so each name appears once.
		// A second identity, or a field another entity already owns.
		"EnvironmentID", "Description", "Labels", "Annotations", "Metadata",
		"PolicyRef", "BehavioralProfileRef", "CreatedAt", "UpdatedAt",
	}

	fset, files := parseEnvironmentSources(t)
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					for _, bad := range forbidden {
						if strings.EqualFold(name.Name, bad) {
							t.Errorf("a struct at %s declares %q; an environment is a "+
								"reference, not a deployment target",
								fset.Position(name.Pos()), name.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// No environment source reaches the network or the shell. The registry
// records what a project calls its environments; it does not go and do
// anything to them.
func TestEnvironmentSourcesImportNoNetworkOrProcess(t *testing.T) {
	forbidden := []string{
		"net", "net/http", "net/url", "os/exec", "net/smtp", "crypto/tls",
	}

	fset, files := parseEnvironmentSources(t)
	for _, file := range files {
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			for _, bad := range forbidden {
				if path == bad {
					t.Errorf("%s imports %q; an environment operation reaches nothing "+
						"outside this process", fset.Position(spec.Pos()), path)
				}
			}
		}
	}
}

// failingTransport fails the test if anything dials.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.t.Errorf("an environment operation issued an outbound request to %s", request.URL)
	return nil, net.ErrClosed
}

// And the dynamic half: every environment operation, plus the run creation
// that now resolves one, with an HTTP transport wired in that fails the test
// if it is ever used.
//
// The static import check above proves the task's own sources cannot dial.
// This proves the path a caller actually takes does not either — through the
// control plane, the store, and the migration the store opens on the way.
func TestEnvironmentOperationsMakeNoOutboundRequest(t *testing.T) {
	transport := failingTransport{t: t}
	previousDefault := http.DefaultTransport
	previousClient := http.DefaultClient.Transport
	http.DefaultTransport = transport
	http.DefaultClient.Transport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previousDefault
		http.DefaultClient.Transport = previousClient
	})

	f := newFixture(t)
	seedProjectOnly(t, f, "proj-1")

	env := createEnvironment(t, f, "proj-1", "staging", "Staging")
	if _, err := f.plane.Environment(t.Context(), "proj-1", "staging"); err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if _, err := f.plane.ProjectEnvironments(
		t.Context(), "proj-1", "", platform.MaxEnvironmentPage); err != nil {
		t.Fatalf("ProjectEnvironments() error = %v", err)
	}
	rank := uint16(10)
	configured, err := f.plane.ConfigureEnvironment(t.Context(), "proj-1", "staging",
		platform.ConfigureEnvironmentRequest{Revision: env.Revision(), Rank: &rank})
	if err != nil {
		t.Fatalf("ConfigureEnvironment() error = %v", err)
	}
	archived, err := f.plane.ArchiveEnvironment(
		t.Context(), "proj-1", "staging", configured.Revision())
	if err != nil {
		t.Fatalf("ArchiveEnvironment() error = %v", err)
	}
	if _, err := f.plane.ActivateEnvironment(
		t.Context(), "proj-1", "staging", archived.Revision()); err != nil {
		t.Fatalf("ActivateEnvironment() error = %v", err)
	}
	if _, err := f.plane.PromotionOrder(t.Context(), "proj-1", "staging", "staging"); err != nil {
		t.Fatalf("PromotionOrder() error = %v", err)
	}
}

// Ordering has one implementation, and CanPromote is it.
//
// A second rank comparison anywhere in this package would be a second answer
// to "which environment is next", which is how two callers end up disagreeing
// about a promotion. The domain's own bound checks are not that: they compare
// a rank against a constant, never two environments against each other.
func TestCanPromoteIsTheOnlyRankComparison(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var scanned int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if decl.Name.Name == "CanPromote" {
				// The one place the comparison belongs.
				return false
			}
			ast.Inspect(decl, func(inner ast.Node) bool {
				binary, ok := inner.(*ast.BinaryExpr)
				if !ok {
					return true
				}
				if binary.Op != token.LSS && binary.Op != token.GTR &&
					binary.Op != token.LEQ && binary.Op != token.GEQ {
					return true
				}
				// A comparison of two rank *selectors* — env.rank vs other.rank
				// — is precedence. A rank against a literal or a constant is a
				// bound check.
				if isRankSelector(binary.X) && isRankSelector(binary.Y) {
					t.Errorf("%s compares two ranks at %s; CanPromote is the only "+
						"implementation of promotion precedence",
						decl.Name.Name, fset.Position(binary.Pos()))
				}
				return true
			})
			return false
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the check would pass vacuously")
	}
}

func isRankSelector(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.SelectorExpr:
		return node.Sel.Name == "rank" || node.Sel.Name == "Rank"
	case *ast.CallExpr:
		selector, ok := node.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == "Rank"
	}
	return false
}

// Task 066 now models promotion, and this asserts the shape it was allowed to
// take rather than its absence.
//
// The evolved form of task 065's "no Promotion* type" check. That rule existed
// so 065 could not quietly acquire a workflow it had not designed; 066
// designed one, so the question becomes which promotion types exist. The
// forbidden alternatives are still forbidden, and they are the ones that would
// turn a recorded decision into a deployment.
func TestPromotionTypesAreTheOnesTask066Specified(t *testing.T) {
	// Exactly the promotion vocabulary task 066 specifies.
	allowed := map[string]bool{
		"PromotionID":       true,
		"PromotionOutcome":  true,
		"Promotion":         true,
		"PromotionDecision": true,
		"PromotionRequest":  true,
	}

	// Names that would mean Trustvian had started claiming it deploys, moves,
	// releases or authorizes something — or had grown a second engine.
	forbidden := []string{
		"PromotionEngine", "PromotionPolicy", "PromotionWorkflow", "PromotionStatus",
		"PromotionState", "PromotionApproval", "PromotionTrigger",
		"Deployment", "DeploymentTarget", "Release", "ReleaseChannel", "Rollback",
	}

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	declared := map[string]bool{}
	var scanned int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			declared[spec.Name.Name] = true
			for _, bad := range forbidden {
				if spec.Name.Name == bad {
					t.Errorf("%s declares type %s at %s; a promotion is a recorded "+
						"decision, not a deployment, a workflow or a lifecycle",
						name, spec.Name.Name, fset.Position(spec.Pos()))
				}
			}
			if strings.HasPrefix(spec.Name.Name, "Promotion") && !allowed[spec.Name.Name] {
				t.Errorf("%s declares unexpected promotion type %s at %s; task 066's "+
					"vocabulary is closed", name, spec.Name.Name, fset.Position(spec.Pos()))
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the check would pass vacuously")
	}

	// And the ones it does specify are actually there, so this cannot pass by
	// the whole feature having been deleted.
	for name := range allowed {
		if !declared[name] {
			t.Errorf("task 066 specifies type %s and it is not declared", name)
		}
	}
}

// A promotion models no deployment, no residence and no approval.
//
// The field-level counterpart to the type check above. These are the names
// that would appear first if somebody started making Trustvian claim it knows
// where a candidate is running.
func TestPromotionDeclaresNoDeploymentOrApprovalField(t *testing.T) {
	forbidden := []string{
		"CurrentEnvironment", "DeployedCandidate", "DeployedAt", "Deployment",
		"URL", "Endpoint", "Host", "Address", "Command", "Webhook",
		"Credentials", "Credential", "Secret", "Token", "APIKey", "Password",
		"ApprovedBy", "Approver", "ApproverRole", "RequestedBy", "Actor",
		"Status", "State", "Phase", "Reason", "Notes", "Message", "Metadata",
		"RolledBack", "RollbackOf",
	}

	fset := token.NewFileSet()
	for _, name := range []string{"promotion.go", "promotion_store.go"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s is gone; this check would pass vacuously: %v", name, err)
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, ident := range field.Names {
					for _, bad := range forbidden {
						if strings.EqualFold(ident.Name, bad) {
							t.Errorf("a struct at %s declares %q; a promotion records a "+
								"decision and models no deployment, residence or approval",
								fset.Position(ident.Pos()), ident.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// The restore path is not an evaluator.
//
// Task 066's historical-evidence rule, asserted structurally: a build that
// recomputed a Passed flag or a verdict on read would hand back a value that
// existed at no point in time. The behavioural proof lives in the store tests;
// this is the one that fails the moment somebody reaches for the helpers.
func TestGateRestoreEvaluatesNothing(t *testing.T) {
	forbidden := []string{"minimumGate", "maximumGate", "EvaluateEvaluationGate"}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "gate.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse gate.go: %v", err)
	}

	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.FuncDecl)
		if !ok || decl.Name.Name != "restoreEvaluationGateResult" {
			return true
		}
		found = true
		ast.Inspect(decl, func(inner ast.Node) bool {
			ident, ok := inner.(*ast.Ident)
			if !ok {
				return true
			}
			for _, bad := range forbidden {
				if ident.Name == bad {
					t.Errorf("restoreEvaluationGateResult calls %s at %s; the restore "+
						"path returns what was stored and derives nothing",
						bad, fset.Position(ident.Pos()))
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("restoreEvaluationGateResult not found; this check would pass vacuously")
	}
}

// The wire shape belongs to the DTOs, here as everywhere else.
func TestEnvironmentCarriesNoJSONTags(t *testing.T) {
	typ := reflect.TypeOf(platform.Environment{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		if tag, present := field.Tag.Lookup("json"); present {
			t.Errorf("Environment.%s carries a json tag %q; wire shape belongs to the DTOs",
				field.Name, tag)
		}
	}
}
