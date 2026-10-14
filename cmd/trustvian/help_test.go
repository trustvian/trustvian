package main

// Tests for the help text: `trustvian --help`, the eval usage, and
// `trustvian evidence --help` (finding F2 of #175).
//
// The lists these check are read from the source — the dispatcher's switch and
// each subcommand's flag set — rather than restated here, so a subcommand or a
// flag added later cannot be missing from the help without a test failing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dispatchedCases returns every string literal a switch inside funcName in
// file dispatches on.
func dispatchedCases(t *testing.T, file, funcName string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	var cases []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unquoting %s: %v", lit.Value, err)
					}
					cases = append(cases, value)
				}
			}
			return true
		})
	}
	if len(cases) == 0 {
		t.Fatalf("found no dispatched cases in %s.%s; this test is not reading what it thinks",
			file, funcName)
	}
	slices.Sort(cases)
	return cases
}

func topLevelHelp(t *testing.T) string {
	t.Helper()
	stdout, _, code := captureOutput(t, func() int { return run([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("trustvian --help exited %d, want 0", code)
	}
	return stdout
}

func TestTopLevelHelpListsEveryEvalSubcommand(t *testing.T) {
	help := topLevelHelp(t)

	// The eval entry runs from its own line to the evidence entry.
	start := strings.Index(help, "trustvian eval ")
	end := strings.Index(help, "trustvian evidence ")
	if start < 0 || end < start {
		t.Fatalf("the help has no eval entry before the evidence entry:\n%s", help)
	}
	listed := strings.FieldsFunc(strings.TrimPrefix(help[start:end], "trustvian eval "),
		func(r rune) bool { return r == '|' || r == ' ' || r == '\n' })
	slices.Sort(listed)

	if want := dispatchedCases(t, "eval.go", "runEval"); !slices.Equal(listed, want) {
		t.Errorf("trustvian --help lists eval subcommands %v;\nthe dispatcher accepts %v", listed, want)
	}
}

func TestTopLevelHelpStatesExitCodesAndDiscovery(t *testing.T) {
	help := topLevelHelp(t)

	for _, want := range []string{
		// Each meaning of 0 to 3 the platform-cli exit table uses.
		"0 success, 1 command failed, 2 usage",
		"0 success, 2 usage, 3 API or network failure",
		"0 gate PASS, 1 gate FAIL, 2 usage, 3 API or network failure",
		// Exit 1 is a gate FAIL for all three gate commands, not one.
		"Exit 1 means gate FAIL only for eval compare, eval compare-repeated",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not state %q:\n%s", want, help)
		}
	}

	// Discovery, in the order resolveAPIURL applies it.
	at := strings.Index(help, "Control-plane discovery")
	if at < 0 {
		t.Fatalf("the help has no discovery section:\n%s", help)
	}
	section := help[at:]
	last := -1
	for _, step := range []string{
		"--api-url",
		"./.trustvian/runtime.json",
		"~/.trustvian/dev/<hash of this directory>/runtime.json",
	} {
		index := strings.Index(section, step)
		if index < 0 {
			t.Fatalf("the discovery section does not mention %q:\n%s", step, section)
		}
		if index < last {
			t.Errorf("%q is listed out of order in:\n%s", step, section)
		}
		last = index
	}
}

// flagSetsIn returns, for every function in file that builds an eval flag set,
// the command it names and the flags it defines.
//
// The lifecycle commands build theirs from a variable ("eval " + action); they
// are reported as "eval <lifecycle>".
func flagSetsIn(t *testing.T, file string) map[string][]string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	sets := map[string][]string{}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var command string
		var flags []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				switch callee.Name {
				case "newFlagSet":
					command = "eval <lifecycle>"
					if lit, ok := call.Args[0].(*ast.BasicLit); ok {
						command, _ = strconv.Unquote(lit.Value)
					}
				case "registerCommonFlags":
					flags = append(flags, "api-url", "json")
				}
			case *ast.SelectorExpr:
				// fs.String("name", ...), fs.Bool(...), fs.Var(&v, "name", ...):
				// the flag's name is the first string literal argument.
				if x, ok := callee.X.(*ast.Ident); !ok || x.Name != "fs" {
					return true
				}
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						name, _ := strconv.Unquote(lit.Value)
						flags = append(flags, name)
						break
					}
				}
			}
			return true
		})
		if strings.HasPrefix(command, "eval ") {
			sets[command] = flags
		}
	}
	return sets
}

// TestEvalUsageNamesEveryFlagEachSubcommandDefines checks each eval
// subcommand's flags against its own entry in evalUsage.
func TestEvalUsageNamesEveryFlagEachSubcommandDefines(t *testing.T) {
	files, err := filepath.Glob("eval*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		for command, flags := range flagSetsIn(t, file) {
			checked++
			// The subcommand's own entry: from its line to the next entry. The
			// lifecycle commands share one flag set, so theirs are checked
			// against the whole usage.
			entry := evalUsage
			if command != "eval <lifecycle>" {
				name := strings.TrimPrefix(command, "eval ")
				at := -1
				for _, after := range []string{" ", "\n"} {
					if at = strings.Index(evalUsage, "trustvian "+command+after); at >= 0 {
						break
					}
				}
				if at < 0 {
					t.Errorf("evalUsage has no entry for %s", command)
					continue
				}
				entry = evalUsage[at+len("trustvian eval "+name):]
				if next := strings.Index(entry, "trustvian eval "); next >= 0 {
					entry = entry[:next]
				}
			}
			for _, flag := range flags {
				named := false
				for _, after := range []string{" ", "]", "\n", "|"} {
					if strings.Contains(entry, "--"+flag+after) {
						named = true
					}
				}
				if !named {
					t.Errorf("%s defines --%s, which its eval usage entry does not name", command, flag)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no eval flag sets; this test is not reading what it thinks")
	}
}

func TestEvidenceHelpPrintsUsageAndSucceeds(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "help"} {
		t.Run(arg, func(t *testing.T) {
			stdout, stderr, code := captureOutput(t, func() int {
				return run([]string{"evidence", arg})
			})
			if code != 0 {
				t.Fatalf("trustvian evidence %s exited %d, want 0\nstderr: %s", arg, code, stderr)
			}
			if !strings.Contains(stdout, "trustvian evidence behaviors") {
				t.Errorf("stdout does not carry the evidence usage:\n%s", stdout)
			}
			if stderr != "" {
				t.Errorf("help wrote to stderr: %q", stderr)
			}
		})
	}
}
