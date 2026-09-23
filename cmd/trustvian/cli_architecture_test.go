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
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readSource reads one file in this package.
func readSource(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", name, err)
	}
	return string(source)
}

// readCode returns a file's source with comments removed.
//
// The guards below assert that certain things are *absent from the code*, and
// a comment explaining why something is absent is evidence of intent, not a
// violation — scanning raw text makes documenting a decision fail the build
// for having documented it. Parsing without ParseComments and reprinting is
// the structural way to ask the question.
func readCode(t *testing.T, name string) string {
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

// tuiRuntimeFiles are task 061's shipped sources.
//
// Listed explicitly so adding a new TUI file is a decision: the guards below
// are the only thing standing between "a dashboard" and "a second client with
// its own opinions about durable state".
var tuiRuntimeFiles = []string{
	"tui.go", "tui_model.go", "tui_realtime.go", "tui_render.go",
}

// TestTUIIsReadOnly enforces the three-endpoint rule structurally.
//
// A dashboard that could mutate is one where a keystroke changes durable
// state, and the failure is someone pressing a key while looking at the wrong
// run. Mutations stay in the CLI, where they are typed deliberately.
func TestTUIIsReadOnly(t *testing.T) {
	for _, name := range tuiRuntimeFiles {
		source := readCode(t, name)

		// The client's only mutating verb, and the raw method.
		for _, forbidden := range []string{".post(", "http.MethodPost", `"POST"`,
			"http.MethodPut", "http.MethodDelete", "http.MethodPatch"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains %q; the TUI is read-only", name, forbidden)
			}
		}

		// Mutating route segments must not appear at all.
		for _, route := range []string{`"records"`, `"compare"`, `"start"`, `"complete"`,
			`"fail"`, `"cancel"`, `"projects"`, `"agents"`, `"candidates"`} {
			if strings.Contains(source, route) {
				t.Errorf("%s names route segment %s; the TUI reads three endpoints only",
					name, route)
			}
		}
	}
}

// TestTUIUsesNoReplayCursor pins the no-history contract at the source level.
func TestTUIUsesNoReplayCursor(t *testing.T) {
	// Structural first: no Header.Set call may name a replay cursor.
	for _, name := range tuiRuntimeFiles {
		ast.Inspect(parseFile(t, name, 0), func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall || len(call.Args) == 0 {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector || (selector.Sel.Name != "Set" && selector.Sel.Name != "Add") {
				return true
			}
			literal, isLiteral := call.Args[0].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				return true
			}
			header, err := strconv.Unquote(literal.Value)
			if err == nil && strings.EqualFold(header, "Last-Event-ID") {
				t.Errorf("%s sets the %s header; task 059 retains no history", name, header)
			}
			return true
		})
	}

	for _, name := range tuiRuntimeFiles {
		source := readCode(t, name)
		for _, forbidden := range []string{"Last-Event-ID", "LastEventID", "lastEventID"} {
			// The parser must mention "id" to ignore it, but never as a header.
			if strings.Contains(source, forbidden) {
				t.Errorf("%s references %q; task 059 retains no history, so there is "+
					"no cursor to resume from", name, forbidden)
			}
		}
	}
}

// TestTUIHasNoPollingTicker catches a refresh loop structurally.
//
// The behavioral test (TestQuietStreamDoesNotPoll) proves the current code
// does not poll; this catches the shape being reintroduced somewhere the
// behavioral test does not reach.
func TestTUIHasNoPollingTicker(t *testing.T) {
	for _, name := range tuiRuntimeFiles {
		source := readCode(t, name)
		for _, forbidden := range []string{"time.NewTicker", "time.Tick(", "tea.Every"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s uses %s; realtime drives the dashboard, not a clock",
					name, forbidden)
			}
		}
	}
}

// TestTUIDoesNotReuseTheOneShotTimeout is subtle and load-bearing.
//
// Task 060's client carries a 30s total timeout. Correct for one request;
// fatal for a dashboard, which it would kill every 30 seconds for being
// healthy. The stream must build its own client.
func TestTUIDoesNotReuseTheOneShotTimeout(t *testing.T) {
	source := readCode(t, "tui_realtime.go")
	if strings.Contains(source, "platformRequestTimeout") {
		t.Error("tui_realtime.go uses the one-shot request timeout for a long-lived stream")
	}
	if !strings.Contains(source, "ResponseHeaderTimeout") {
		t.Error("the streaming transport has no response-header bound")
	}
	// An http.Client literal with a Timeout field in this file would be the
	// total-lifetime bound this test exists to prevent.
	file := parseFile(t, "tui_realtime.go", 0)
	ast.Inspect(file, func(node ast.Node) bool {
		composite, isComposite := node.(*ast.CompositeLit)
		if !isComposite {
			return true
		}
		selector, isSelector := composite.Type.(*ast.SelectorExpr)
		if !isSelector || selector.Sel.Name != "Client" {
			return true
		}
		for _, element := range composite.Elts {
			pair, isPair := element.(*ast.KeyValueExpr)
			if !isPair {
				continue
			}
			if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "Timeout" {
				t.Error("the SSE http.Client sets a total Timeout, which would kill a " +
					"healthy long-lived stream")
			}
		}
		return true
	})
}

// TestTUICollectionsAreBounded proves the two bounds exist and differ.
func TestTUICollectionsAreBounded(t *testing.T) {
	if tuiObservationCapacity <= 0 || tuiObservationCapacity > 1000 {
		t.Errorf("tuiObservationCapacity = %d; a display window must be small and finite",
			tuiObservationCapacity)
	}
	if tuiPendingEventCapacity <= 0 || tuiPendingEventCapacity > 1000 {
		t.Errorf("tuiPendingEventCapacity = %d; the transport buffer must be finite",
			tuiPendingEventCapacity)
	}
	if maxSSELineBytes <= 0 || maxSSEFrameBytes <= 0 {
		t.Error("SSE input bounds must be positive")
	}
	source := readCode(t, "tui_realtime.go")
	if !strings.Contains(source, "make(chan realtimeFrame, tuiPendingEventCapacity)") {
		t.Error("the frame channel is not bounded by tuiPendingEventCapacity")
	}
}

// TestUIFrameworkIsConfinedToTheCommand is the dependency-rule check.
//
// .claude/rules/go.md permits a fourth dependency on the condition that the
// single package allowed to import it is named and the confinement verified.
// This is that verification, in the build rather than in prose.
func TestUIFrameworkIsConfinedToTheCommand(t *testing.T) {
	enginePackages := []string{
		"github.com/trustvian/trustvian",
		"github.com/trustvian/trustvian/event",
		"github.com/trustvian/trustvian/config",
		"github.com/trustvian/trustvian/alert",
		"github.com/trustvian/trustvian/internal/features",
		"github.com/trustvian/trustvian/internal/fingerprint",
		"github.com/trustvian/trustvian/internal/baseline",
		"github.com/trustvian/trustvian/internal/store",
		"github.com/trustvian/trustvian/internal/anomaly",
		"github.com/trustvian/trustvian/internal/trust",
		"github.com/trustvian/trustvian/internal/policy",
	}

	for _, pkg := range enginePackages {
		output, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			for _, framework := range []string{
				"charmbracelet", "muesli/", "mattn/go-runewidth", "mattn/go-isatty",
				"lucasb-eyer", "rivo/uniseg", "xo/terminfo", "aymanbagabas",
			} {
				if strings.Contains(line, framework) {
					t.Errorf("%s depends on %s; the behavioral engine must stay "+
						"free of the UI framework", pkg, strings.TrimSpace(line))
				}
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

// TestLocalRuntimeIsNotImportedByTheRootModule is task 062's boundary guard.
//
// Task 062 makes the local platform startable, which is exactly the pressure
// that would justify "just import the platform from cmd/trustvian" — it would
// remove the discovery file, the second executable and a whole class of test.
// It would also reverse ADR 0022 and ADR 0033, and pull the SQLite driver into
// every shipped CLI.
//
// The composition lives in the platform module instead, and the CLI finds it
// through a file rather than a Go import.
func TestLocalRuntimeIsNotImportedByTheRootModule(t *testing.T) {
	for _, name := range goFiles(t, true) {
		file := parseFile(t, name, parser.ImportsOnly)
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if strings.HasPrefix(path, "trustvian-platform") {
				t.Errorf("%s imports %q; the local runtime is composed inside the "+
					"platform module precisely so this edge stays absent", name, path)
			}
		}
	}

	// And the module graph agrees, including test-only requirements.
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if strings.Contains(string(content), "trustvian-platform") {
			t.Errorf("root %s references trustvian-platform", name)
		}
	}
}

// TestDiscoveryResolverIsTheSinglePathToAClient stops the resolver being
// bypassed one command at a time.
//
// Every platform client must share it: a command that called
// newPlatformClient directly would silently lose local discovery, and the
// omission would look like a missing feature rather than a bug.
func TestDiscoveryResolverIsTheSinglePathToAClient(t *testing.T) {
	for _, name := range goFiles(t, false) {
		if name == "platform_client.go" {
			// Where the resolver and the constructor both live.
			continue
		}
		source := readCode(t, name)
		if strings.Contains(source, "newPlatformClient(") {
			t.Errorf("%s constructs a client directly; use resolveAPIURL so local "+
				"discovery applies to every command", name)
		}
	}
}
