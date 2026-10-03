//go:build !windows

// Structural tests of task 079's security posture, over every place a
// workflow can be copied from: every workflow in .github/workflows, every
// shipped example, every YAML code block in this repository's Markdown, and
// the two actions' own steps.
//
// The guarantee they hold is the job split (ADR 0058 § 1): the job that runs
// the pull request's code holds no write scope, and the job that holds the
// write scope runs no pull request code. These are the tests that fail if
// anyone collapses the two jobs back into one, or lets either drift.
package scripts_test

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	exampleWorkflow  = "../examples/github-actions/behavioral-gate.yml"
	selfTestWorkflow = "../.github/workflows/trustvian-run-action.yml"
	// selfTestCommentJob is the one job allowed to run the comment action as
	// a local action, with the checkout that needs (ADR 0058 § 7).
	selfTestCommentJob = "comment"
	placeholderSHA     = "REPLACE_WITH_A_REVIEWED_TRUSTVIAN_COMMIT_SHA"
)

var (
	usesRunAction     = regexp.MustCompile(`(^|/)\.github/actions/trustvian-run(@|$)`)
	usesCommentAction = regexp.MustCompile(`(^|/)\.github/actions/trustvian-comment(@|$)`)
)

// workflowSource is one parsed workflow, or a workflow-shaped YAML block.
type workflowSource struct {
	name     string
	doc      map[string]any // nil for a fragment
	jobs     map[string]map[string]any
	steps    []map[string]any // a fragment's top-level steps
	shipped  bool             // an example, or a block in documentation
	selfTest bool
}

// sources returns every workflow file, every example, and every YAML block
// in the repository's Markdown that parses as a workflow, a job or a list of
// steps.
func sources(t *testing.T) []workflowSource {
	t.Helper()
	var out []workflowSource
	files, err := filepath.Glob("../.github/workflows/*.y*ml")
	if err != nil {
		t.Fatal(err)
	}
	examples, err := filepath.Glob("../examples/github-actions/*.y*ml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append(files, examples...) {
		src, ok := parseWorkflow(t, path, readFile(t, path))
		if !ok {
			t.Fatalf("%s does not parse as a workflow", path)
		}
		src.shipped = slices.Contains(examples, path)
		src.selfTest = path == selfTestWorkflow
		out = append(out, src)
	}
	if len(examples) == 0 {
		t.Fatal("no shipped example workflow")
	}
	for _, block := range markdownYAMLBlocks(t) {
		if src, ok := parseWorkflow(t, block.name, block.body); ok {
			src.shipped = true
			out = append(out, src)
		}
	}
	return out
}

func parseWorkflow(t *testing.T, name, body string) (workflowSource, bool) {
	t.Helper()
	var raw any
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		return workflowSource{}, false
	}
	src := workflowSource{name: name, jobs: map[string]map[string]any{}}
	switch v := raw.(type) {
	case map[string]any:
		if jobs, ok := v["jobs"].(map[string]any); ok {
			src.doc = v
			for name, job := range jobs {
				m, _ := job.(map[string]any)
				src.jobs[name] = m
			}
			return src, true
		}
		if _, ok := v["steps"]; ok {
			src.jobs["(fragment)"] = v
			return src, true
		}
	case []any:
		for _, item := range v {
			step, ok := item.(map[string]any)
			if !ok || (step["uses"] == nil && step["run"] == nil) {
				return workflowSource{}, false
			}
			src.steps = append(src.steps, step)
		}
		return src, len(src.steps) > 0
	}
	return workflowSource{}, false
}

type yamlBlock struct{ name, body string }

// markdownYAMLBlocks returns every ```yaml block in the repository's
// Markdown, outside .git and build output.
func markdownYAMLBlocks(t *testing.T) []yamlBlock {
	t.Helper()
	fence := regexp.MustCompile("(?s)```ya?ml\n(.*?)```")
	var blocks []yamlBlock
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "bin", "node_modules", ".remember":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, m := range fence.FindAllStringSubmatch(string(body), -1) {
			blocks = append(blocks, yamlBlock{fmt.Sprintf("%s#yaml%d", path, i+1), m[1]})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

func stepsOf(job map[string]any) []map[string]any {
	raw, _ := job["steps"].([]any)
	var steps []map[string]any
	for _, s := range raw {
		if m, ok := s.(map[string]any); ok {
			steps = append(steps, m)
		}
	}
	return steps
}

func jobUses(job map[string]any, pattern *regexp.Regexp) bool {
	for _, step := range stepsOf(job) {
		if use, _ := step["uses"].(string); pattern.MatchString(use) {
			return true
		}
	}
	return false
}

// No `on:` anywhere names an event that runs with base-repository
// privileges. Prose explaining the refusal is expected and required; the
// check is on triggers, not on the string.
func TestNoWorkflowRunsOnAPrivilegedEvent(t *testing.T) {
	for _, src := range sources(t) {
		if src.doc == nil {
			continue
		}
		for _, trigger := range triggers(src.doc) {
			if trigger == "pull_request_target" || trigger == "workflow_run" {
				t.Errorf("%s is triggered by %s", src.name, trigger)
			}
		}
	}
	for _, path := range []string{
		"../docs/ci-github-action.md",
		"../.github/actions/trustvian-run/README.md",
		"../.github/actions/trustvian-comment/README.md",
		exampleWorkflow,
	} {
		if !strings.Contains(readFile(t, path), "pull_request_target") {
			t.Errorf("%s does not explain the pull_request_target refusal", path)
		}
	}
}

// The job that holds the write scope runs no pull request code: exactly
// pull-requests: write, no contents scope, no checkout, no local action, and
// no step of its own — only the comment action, pinned. The self-test's
// comment job is the single, documented exception (ADR 0058 § 7).
func TestCommentJobsRunNoPullRequestCode(t *testing.T) {
	found, exceptions := 0, 0
	for _, src := range sources(t) {
		for name, job := range src.jobs {
			if !jobUses(job, usesCommentAction) {
				continue
			}
			found++
			where := src.name + ": job " + name
			if jobUses(job, usesRunAction) {
				t.Errorf("%s runs the workload and holds the comment action: the two jobs were merged", where)
			}
			if src.selfTest && name == selfTestCommentJob {
				exceptions++
				checkSelfTestCommentJob(t, where, job)
				continue
			}
			perms, ok := job["permissions"].(map[string]any)
			if !ok || len(perms) != 1 || perms["pull-requests"] != "write" {
				t.Errorf("%s permissions %v, want exactly pull-requests: write", where, job["permissions"])
			}
			for _, step := range stepsOf(job) {
				use, _ := step["uses"].(string)
				switch {
				case step["run"] != nil:
					t.Errorf("%s has a run step, which would execute in the workspace", where)
				case strings.HasPrefix(use, "./"):
					t.Errorf("%s uses a local action %q, which is the pull request's code", where, use)
				case strings.HasPrefix(use, "actions/checkout@"):
					t.Errorf("%s checks out the repository", where)
				case !usesCommentAction.MatchString(use):
					t.Errorf("%s uses %q; the comment job runs the comment action alone", where, use)
				case !strings.HasSuffix(use, "@"+placeholderSHA) && !pinnedUse.MatchString(use):
					t.Errorf("%s uses %q, not pinned to a reviewed commit", where, use)
				}
				if _, ok := step["working-directory"]; ok {
					t.Errorf("%s sets a working directory", where)
				}
			}
			if src.doc != nil {
				if needs := fmt.Sprint(job["needs"]); !strings.Contains(needs, "run") {
					t.Errorf("%s does not need the run job (needs %v)", where, job["needs"])
				}
				if cond := fmt.Sprint(job["if"]); !strings.Contains(cond, "!cancelled()") {
					t.Errorf("%s runs under %q, want !cancelled(): a cancelled run makes no claim", where, cond)
				}
			}
		}
	}
	if found == 0 {
		t.Error("no comment job found anywhere; the scan is not looking where the workflows are")
	}
	if exceptions != 1 {
		t.Errorf("%d self-test exceptions, want exactly 1", exceptions)
	}
}

// checkSelfTestCommentJob: the exception is exactly the shape ADR 0058 § 7
// justifies — the local action needs a checkout, the checkout needs contents:
// read, and nothing more is granted; the checkout keeps no credential, and the
// job runs on pull requests only.
func checkSelfTestCommentJob(t *testing.T, where string, job map[string]any) {
	t.Helper()
	perms, _ := job["permissions"].(map[string]any)
	if len(perms) != 2 || perms["contents"] != "read" || perms["pull-requests"] != "write" {
		t.Errorf("%s permissions %v, want contents: read and pull-requests: write", where, perms)
	}
	if cond := fmt.Sprint(job["if"]); !strings.Contains(cond, "!cancelled()") ||
		!strings.Contains(cond, "github.event_name == 'pull_request'") {
		t.Errorf("%s runs under %q", where, cond)
	}
	for _, step := range stepsOf(job) {
		use, _ := step["uses"].(string)
		if strings.HasPrefix(use, "./") && use != "./.github/actions/trustvian-comment" {
			t.Errorf("%s uses local action %q", where, use)
		}
		if strings.HasPrefix(use, "actions/checkout@") {
			with, _ := step["with"].(map[string]any)
			if len(with) != 1 {
				t.Errorf("%s checks out with %v; only persist-credentials: false is expected", where, with)
			}
		}
	}
	if !strings.Contains(readFile(t, selfTestWorkflow), "ADR 0058 § 7") {
		t.Errorf("%s does not cite its justification", selfTestWorkflow)
	}
}

// The job that runs the workload holds no write scope, and says so
// explicitly rather than inheriting the repository's default.
func TestRunJobsHoldNoWriteScope(t *testing.T) {
	found := 0
	for _, src := range sources(t) {
		for name, job := range src.jobs {
			if !jobUses(job, usesRunAction) {
				continue
			}
			found++
			where := src.name + ": job " + name
			perms, ok := job["permissions"].(map[string]any)
			if !ok {
				t.Errorf("%s states no permissions %v; it would inherit the default token", where, job["permissions"])
				continue
			}
			for scope, level := range perms {
				if level == "write" {
					t.Errorf("%s holds %s: write next to the workload", where, scope)
				}
			}
		}
	}
	if found == 0 {
		t.Error("no run job found anywhere")
	}
}

// No shipped workflow — the examples, every workflow block in the
// documentation — and not this repository's own action test grants
// permissions at workflow level, which would reach the run job.
func TestShippedWorkflowsGrantNoWorkflowLevelPermissions(t *testing.T) {
	for _, src := range sources(t) {
		if src.doc == nil || !(src.shipped || src.selfTest) {
			continue
		}
		if _, ok := src.doc["permissions"]; ok {
			t.Errorf("%s grants permissions at workflow level", src.name)
		}
	}
}

// Every checkout — in every workflow, example, documentation block and
// action — sets persist-credentials: false. actions/checkout defaults to
// true, which writes the job's token into .git/config.
func TestEveryCheckoutKeepsNoCredential(t *testing.T) {
	var all []struct {
		where string
		step  map[string]any
	}
	add := func(where string, steps []map[string]any) {
		for _, s := range steps {
			all = append(all, struct {
				where string
				step  map[string]any
			}{where, s})
		}
	}
	for _, src := range sources(t) {
		add(src.name, src.steps)
		for name, job := range src.jobs {
			add(src.name+": job "+name, stepsOf(job))
		}
	}
	for _, dir := range []string{actionDir, commentActionDir} {
		action := loadYAML(t, filepath.Join(dir, "action.yml"))
		runs, _ := action["runs"].(map[string]any)
		add(dir, stepsOf(runs))
	}
	checkouts := 0
	for _, s := range all {
		use, _ := s.step["uses"].(string)
		if !strings.HasPrefix(use, "actions/checkout@") {
			continue
		}
		checkouts++
		with, _ := s.step["with"].(map[string]any)
		if v, ok := with["persist-credentials"]; !ok || fmt.Sprint(v) != "false" {
			t.Errorf("%s checks out with persist-credentials %v, want false", s.where, v)
		}
	}
	if checkouts == 0 {
		t.Error("no checkout found anywhere")
	}
}

// The shipped example is the two-job workflow, end to end: the run job hands
// its exit code on, the comment job consumes it after anything but a
// cancellation, both actions are pinned by the same placeholder, newer pushes
// cancel older runs, and nothing suppresses a failure or reads a secret.
func TestExampleIsTheTwoJobWorkflow(t *testing.T) {
	doc := loadYAML(t, exampleWorkflow)
	jobs, _ := doc["jobs"].(map[string]any)
	if got := slices.Sorted(maps.Keys(jobs)); !slices.Equal(got, []string{"comment", "run"}) {
		t.Fatalf("jobs %v, want run and comment", got)
	}
	run, _ := jobs["run"].(map[string]any)
	comment, _ := jobs["comment"].(map[string]any)
	if out, _ := run["outputs"].(map[string]any); out["exit-code"] != "${{ steps.gate.outputs.exit-code }}" {
		t.Errorf("run job outputs %v", run["outputs"])
	}
	if comment["needs"] != "run" || comment["if"] != "${{ !cancelled() }}" {
		t.Errorf("comment job needs %v if %v", comment["needs"], comment["if"])
	}
	steps := stepsOf(comment)
	if len(steps) != 1 {
		t.Fatalf("comment job has %d steps, want the action alone", len(steps))
	}
	with, _ := steps[0]["with"].(map[string]any)
	if with["exit-code"] != "${{ needs.run.outputs.exit-code }}" {
		t.Errorf("comment action with %v", with)
	}
	for _, job := range []map[string]any{run, comment} {
		for _, step := range stepsOf(job) {
			use, _ := step["uses"].(string)
			if (usesRunAction.MatchString(use) || usesCommentAction.MatchString(use)) &&
				!strings.HasSuffix(use, "@"+placeholderSHA) {
				t.Errorf("the example uses %q, not the placeholder", use)
			}
		}
	}
	conc, _ := doc["concurrency"].(map[string]any)
	if !strings.Contains(fmt.Sprint(conc["group"]), "github.event.pull_request.number") || conc["cancel-in-progress"] != true {
		t.Errorf("concurrency %v, want a group per pull request with cancel-in-progress", conc)
	}
	raw := readFile(t, exampleWorkflow)
	for _, forbidden := range []string{"continue-on-error", "|| true", "write-all", "${{ secrets.", "issues:", "contents: write"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the example contains %q", forbidden)
		}
	}
}
