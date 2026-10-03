//go:build !windows

// Tests of the trustvian-release GitHub App's confinement (ADR 0060 § 3):
// only the approved publish job can mint its token, every tag and release is
// written with it, nothing else references its key, the rulesets
// release-setup.sh applies have exactly the documented shape, and the release
// audit runs with exactly the documented triggers and permissions.
package scripts_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	appSecret     = "RELEASE_APP_PRIVATE_KEY"
	appTokenStep  = "actions/create-github-app-token@"
	auditWorkflow = "../.github/workflows/release-audit.yml"
)

func jobText(job map[string]any) string {
	var b strings.Builder
	for k, v := range job {
		fmt.Fprintf(&b, "%s: %v\n", k, v)
	}
	return b.String()
}

// Only publish references the App's key and mints its token; preflight may
// read the App's id, to fail early. The tag and every `gh release` call use the
// App's token, and no job holds contents: write.
func TestOnlyPublishHoldsTheReleaseApp(t *testing.T) {
	doc, jobs := releaseJobs(t)
	if strings.Contains(fmt.Sprint(doc["env"]), appSecret) || strings.Contains(fmt.Sprint(doc["env"]), "RELEASE_APP") {
		t.Error("the workflow-level env references the release App")
	}
	for name, job := range jobs {
		text := jobText(job)
		holds := strings.Contains(text, appSecret) || strings.Contains(text, appTokenStep)
		if holds != (name == "publish") {
			t.Errorf("%s: references the App key or token step = %v", name, holds)
		}
		if strings.Contains(text, "vars.RELEASE_APP_ID") && name != "publish" && name != "preflight" {
			t.Errorf("%s reads the App's id", name)
		}
		if fmt.Sprint(jobPermissions(job)["contents"]) == "write" {
			t.Errorf("%s holds contents: write; the App's token writes tags and releases", name)
		}
	}

	var mint map[string]any
	for _, s := range stepsOf(jobs["publish"]) {
		if use, _ := s["uses"].(string); strings.HasPrefix(use, appTokenStep) {
			mint = s
		}
	}
	if mint == nil {
		t.Fatal("publish does not mint the App's token")
	}
	with, _ := mint["with"].(map[string]any)
	for key, want := range map[string]string{
		"app-id":              "${{ vars.RELEASE_APP_ID }}",
		"private-key":         "${{ secrets.RELEASE_APP_PRIVATE_KEY }}",
		"owner":               "${{ github.repository_owner }}",
		"repositories":        "${{ github.event.repository.name }}",
		"permission-contents": "write",
	} {
		if fmt.Sprint(with[key]) != want {
			t.Errorf("the token step's %s = %v, want %s", key, with[key], want)
		}
	}
	if len(with) != 5 {
		t.Errorf("the token step has inputs %v; any other permission widens the token", with)
	}

	tagAt, tag := publishStep(t, jobs, "1. Create the release tag")
	_, release := publishStep(t, jobs, "2. GitHub Release")
	for name, step := range map[string]map[string]any{"tag": tag, "release": release} {
		env, _ := step["env"].(map[string]any)
		if env["GH_TOKEN"] != "${{ steps.app.outputs.token }}" {
			t.Errorf("the %s step uses GH_TOKEN %v, want the App's token", name, env["GH_TOKEN"])
		}
	}
	// A release keeps its author when a draft is published, so publish finishes
	// only the App's own draft and replaces anyone else's.
	for _, want := range []string{
		`.author.login // ""' <<<"$state")" != "trustvian-release[bot]" ]`,
		`gh release delete "$VERSION" --yes`,
	} {
		if !strings.Contains(stepRun(release), want) {
			t.Errorf("the release step lacks %s; it would publish a draft another actor authored", want)
		}
	}
	if strings.Contains(stepRun(release), "--cleanup-tag") {
		t.Error("the release step deletes a tag; tags are immutable")
	}
	mintAt := slices.IndexFunc(stepsOf(jobs["publish"]), func(s map[string]any) bool { return s["id"] == "app" })
	if mintAt < 0 || mintAt > tagAt {
		t.Error("publish creates the tag before minting the App's token")
	}
	for _, s := range stepsOf(jobs["publish"]) {
		env, _ := s["env"].(map[string]any)
		if strings.Contains(stepRun(s), "gh release ") && env["GH_TOKEN"] != "${{ steps.app.outputs.token }}" &&
			!strings.Contains(stepRun(s), `GH_TOKEN="$APP_TOKEN" gh release`) {
			t.Errorf("publish step %q calls gh release without the App's token", s["name"])
		}
	}
}

// No script, and no other workflow, references the App's key. release-setup.sh
// may name it only to check, names only, that the `release` environment has it.
func TestNothingElseReferencesTheAppKey(t *testing.T) {
	scripts, _ := filepath.Glob("*.sh")
	workflows, _ := filepath.Glob("../.github/workflows/*.y*ml")
	for _, f := range append(scripts, workflows...) {
		text := readFile(t, f)
		if !strings.Contains(text, appSecret) {
			continue
		}
		switch filepath.Base(f) {
		case "release.yml":
			// Checked job by job above.
		case "release-setup.sh":
			code := stripShellComments(text)
			if strings.Contains(code, "secrets.") || strings.Contains(code, "gh secret") ||
				!strings.Contains(code, "environments/release/secrets") {
				t.Errorf("release-setup.sh does more with %s than check its name", appSecret)
			}
		default:
			t.Errorf("%s references %s", f, appSecret)
		}
	}
}

// release-setup.sh's two ruleset payloads have exactly the shape ADR 0060
// records: every tag; creation restricted with the App as the only bypass;
// update, deletion and force-move restricted with no bypass at all.
func TestReleaseSetupRulesetPayloads(t *testing.T) {
	payload := func(fn string, args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command("bash", "-c", `set -euo pipefail; source scripts/release-setup.sh; "$@"`, "bash", fn)
		cmd.Args = append(cmd.Args, args...)
		cmd.Dir = ".."
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", fn, err, out)
		}
		return v
	}
	check := func(v map[string]any, name string, rules []string, bypass string) {
		t.Helper()
		if v["name"] != name || v["target"] != "tag" || v["enforcement"] != "active" {
			t.Errorf("%s: name/target/enforcement = %v/%v/%v", name, v["name"], v["target"], v["enforcement"])
		}
		cond := fmt.Sprint(v["conditions"])
		if cond != "map[ref_name:map[exclude:[] include:[refs/tags/*]]]" {
			t.Errorf("%s: conditions = %s, want every tag", name, cond)
		}
		var got []string
		for _, r := range v["rules"].([]any) {
			got = append(got, fmt.Sprint(r.(map[string]any)["type"]))
		}
		slices.Sort(got)
		if !slices.Equal(got, rules) {
			t.Errorf("%s: rules = %v, want %v", name, got, rules)
		}
		if b := fmt.Sprint(v["bypass_actors"]); b != bypass {
			t.Errorf("%s: bypass_actors = %s, want %s", name, b, bypass)
		}
	}
	check(payload("creation_ruleset_json", "123456"), "Release tags: creation", []string{"creation"},
		"[map[actor_id:123456 actor_type:Integration bypass_mode:always]]")
	check(payload("immutable_ruleset_json"), "Release tags: immutable",
		[]string{"deletion", "non_fast_forward", "update"}, "[]")

	cmd := exec.Command("bash", "-c", `source scripts/release-setup.sh; creation_ruleset_json "" || creation_ruleset_json abc`)
	cmd.Dir = ".."
	if out, err := cmd.Output(); err == nil {
		t.Errorf("creation_ruleset_json accepts a missing or non-numeric App id:\n%s", out)
	}
}

// The release audit runs on a release being published, created or edited, and
// weekly; it holds exactly contents: read, attestations: read and issues:
// write; it pins the signer workflow and exempts exactly the releases up to
// v0.9.0.
func TestReleaseAuditShape(t *testing.T) {
	doc := loadYAML(t, auditWorkflow)
	on, _ := doc["on"].(map[string]any)
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range on {
			if !yield(k) {
				return
			}
		}
	})
	if !slices.Equal(keys, []string{"release", "schedule"}) {
		t.Errorf("triggers = %v, want release and schedule", keys)
	}
	rel, _ := on["release"].(map[string]any)
	if fmt.Sprint(rel["types"]) != "[published created edited]" {
		t.Errorf("release types = %v", rel["types"])
	}
	if perms := fmt.Sprint(doc["permissions"]); perms != "map[contents:read]" {
		t.Errorf("workflow permissions = %s, want contents: read", perms)
	}
	jobs, _ := doc["jobs"].(map[string]any)
	if len(jobs) != 1 {
		t.Fatalf("the audit has %d jobs, want 1", len(jobs))
	}
	for _, j := range jobs {
		job, _ := j.(map[string]any)
		if got := fmt.Sprint(job["permissions"]); got != "map[attestations:read contents:read issues:write]" {
			t.Errorf("job permissions = %s", got)
		}
		run := ""
		for _, s := range stepsOf(job) {
			run += stepRun(s)
		}
		for _, want := range []string{
			`APP_LOGIN="trustvian-release[bot]"`,
			`--signer-workflow "$SIGNER"`,
			`SIGNER="$GITHUB_REPOSITORY/.github/workflows/release.yml"`,
			`!= tag ]`,
			"compare/main...$commit",
			"--label release-audit",
			`exempt=" v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.6.0 v0.7.0 v0.8.0 v0.9.0-rc.1 v0.9.0-rc.2 v0.9.0-rc.3 v0.9.0 "`,
		} {
			if !strings.Contains(run, want) {
				t.Errorf("the audit lacks %s", want)
			}
		}
	}
}
