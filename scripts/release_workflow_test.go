//go:build !windows

// Structural tests of release.yml's shape (ADR 0059): dispatched from main,
// verified before anything is published, and published only by the one job
// that may. These are the properties that make "Re-run failed jobs" safe and a
// dry run harmless, so they are asserted rather than left to review.
package scripts_test

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const releaseWorkflow = "../.github/workflows/release.yml"

var (
	releasePinnedUse = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	writeScope       = regexp.MustCompile(`^write$`)
	tagFlag          = regexp.MustCompile(`(^|\s)(-t|--tag)\s`)
	gitPush          = regexp.MustCompile(`\bgit\s+(-c\s+\S+\s+)*push\b`)
	refCreation      = regexp.MustCompile(`git/(refs|tags)\b`)
)

func releaseJobs(t *testing.T) (map[string]any, map[string]map[string]any) {
	t.Helper()
	doc := loadYAML(t, releaseWorkflow)
	raw, _ := doc["jobs"].(map[string]any)
	jobs := map[string]map[string]any{}
	for name, j := range raw {
		jobs[name], _ = j.(map[string]any)
	}
	return doc, jobs
}

func jobNeeds(job map[string]any) []string {
	switch n := job["needs"].(type) {
	case string:
		return []string{n}
	case []any:
		var out []string
		for _, v := range n {
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	return nil
}

func jobPermissions(job map[string]any) map[string]string {
	out := map[string]string{}
	perms, _ := job["permissions"].(map[string]any)
	for k, v := range perms {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func stepRun(step map[string]any) string {
	run, _ := step["run"].(string)
	return run
}

// The one trigger is a dispatch with the three documented inputs, from main,
// never overlapping and never cancelled.
func TestReleaseIsDispatchedOnly(t *testing.T) {
	doc, jobs := releaseJobs(t)
	if got := triggers(doc); !slices.Equal(got, []string{"workflow_dispatch"}) {
		t.Fatalf("triggers = %v, want only workflow_dispatch", got)
	}
	on, _ := doc["on"].(map[string]any)
	dispatch, _ := on["workflow_dispatch"].(map[string]any)
	inputs, _ := dispatch["inputs"].(map[string]any)
	for name, want := range map[string]string{"version": "true", "commit": "true"} {
		in, _ := inputs[name].(map[string]any)
		if fmt.Sprint(in["required"]) != want {
			t.Errorf("input %s: required = %v, want %s", name, in["required"], want)
		}
	}
	dry, _ := inputs["dry_run"].(map[string]any)
	if dry["type"] != "boolean" || fmt.Sprint(dry["default"]) != "false" {
		t.Errorf("input dry_run = %v, want a boolean defaulting to false", dry)
	}

	conc, _ := doc["concurrency"].(map[string]any)
	if conc["group"] != "release" || fmt.Sprint(conc["cancel-in-progress"]) != "false" {
		t.Errorf("concurrency = %v, want group release, never cancelled", conc)
	}

	preflight := strings.Join(slices.Collect(func(yield func(string) bool) {
		for _, s := range stepsOf(jobs["preflight"]) {
			if !yield(stepRun(s)) {
				return
			}
		}
	}), "\n")
	for _, want := range []string{`"$GITHUB_REF" != "refs/heads/main"`, `"$COMMIT" != "$GITHUB_SHA"`, "release-preflight.sh"} {
		if !strings.Contains(preflight, want) {
			t.Errorf("preflight does not check %s", want)
		}
	}
}

// The order is preflight → gates → build and image → verify → summary →
// publish. Only publish runs in the `release` environment, whose required
// reviewers are the human gate; every other job runs in `release-build`.
func TestReleaseJobOrder(t *testing.T) {
	_, jobs := releaseJobs(t)
	want := map[string][]string{
		"preflight": nil,
		"gates":     {"preflight"},
		"build":     {"gates"},
		"image":     {"gates"},
		"verify":    {"build", "image"},
		"summary":   {"preflight", "gates", "build", "image", "verify"},
		"publish":   {"preflight", "gates", "build", "image", "verify", "summary"},
	}
	if got := slices.Sorted(maps.Keys(jobs)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("jobs = %v, want %v", got, slices.Sorted(maps.Keys(want)))
	}
	for name, needs := range want {
		got := jobNeeds(jobs[name])
		slices.Sort(got)
		slices.Sort(needs)
		if !slices.Equal(got, needs) {
			t.Errorf("%s needs %v, want %v", name, got, needs)
		}
		wantEnv := "release-build"
		if name == "publish" {
			wantEnv = "release"
		}
		if jobs[name]["environment"] != wantEnv {
			t.Errorf("%s runs in environment %v, want %s", name, jobs[name]["environment"], wantEnv)
		}
	}
	matrix, _ := jobs["verify"]["strategy"].(map[string]any)["matrix"].(map[string]any)
	if got := fmt.Sprint(matrix["os"]); got != "[ubuntu-latest macos-latest]" {
		t.Errorf("verify runs on %s, want ubuntu-latest and macos-latest", got)
	}
}

// Nothing before publish can publish: only publish holds contents: write,
// packages: write is held only by image (an untagged digest) and publish, and
// the workflow level grants no write at all. A dry run skips publish.
func TestOnlyPublishCanPublish(t *testing.T) {
	doc, jobs := releaseJobs(t)
	if perms, _ := doc["permissions"].(map[string]any); fmt.Sprint(perms) != "map[contents:read]" {
		t.Errorf("workflow-level permissions = %v, want contents: read only", perms)
	}
	writers := map[string][]string{
		"contents": {"publish"},
		"packages": {"image", "publish"},
	}
	for name, job := range jobs {
		for scope, level := range jobPermissions(job) {
			if !writeScope.MatchString(level) {
				continue
			}
			allowed, ok := writers[scope]
			if !ok {
				switch scope {
				case "id-token", "attestations":
					// Signing, not publishing: build attests, image signs.
					if name == "build" || name == "image" {
						continue
					}
				}
				t.Errorf("%s holds %s: write", name, scope)
				continue
			}
			if !slices.Contains(allowed, name) {
				t.Errorf("%s holds %s: write; only %v may", name, scope, allowed)
			}
		}
	}
	if cond := fmt.Sprint(jobs["publish"]["if"]); !strings.Contains(strings.ReplaceAll(cond, " ", ""), "!inputs.dry_run") {
		t.Errorf("publish if = %q, want it skipped for a dry run", cond)
	}
}

// The image job pushes by digest and nothing else; image tags are created only
// in publish, from that digest. No job pushes git, and only publish, after
// approval, creates a git ref or tag object: the release tag.
func TestImageIsPushedByDigestAndNoJobMintsATag(t *testing.T) {
	_, jobs := releaseJobs(t)
	pushes := 0
	for _, s := range stepsOf(jobs["image"]) {
		run := stepRun(s)
		if strings.Contains(run, "imagetools create") {
			t.Errorf("image retags (%q); only publish may", s["name"])
		}
		if !strings.Contains(run, "--push") && !strings.Contains(run, "push=true") {
			continue
		}
		pushes++
		if !strings.Contains(run, "push-by-digest=true") {
			t.Errorf("image step %q pushes without push-by-digest", s["name"])
		}
		if tagFlag.MatchString(run) {
			t.Errorf("image step %q pushes with a tag", s["name"])
		}
	}
	if pushes != 1 {
		t.Errorf("image has %d pushing steps, want exactly 1", pushes)
	}

	for name, job := range jobs {
		for _, s := range stepsOf(job) {
			run := stepRun(s)
			if gitPush.MatchString(run) {
				t.Errorf("%s step %q pushes git", name, s["name"])
			}
			if strings.Contains(run, "imagetools create") && name != "publish" {
				t.Errorf("%s step %q moves an image tag", name, s["name"])
			}
			// publish reads git/ref and git/tags to check the human's tag;
			// nothing may write them.
			if refCreation.MatchString(run) && (strings.Contains(run, "-X POST") || strings.Contains(run, " -f ")) && name != "publish" {
				t.Errorf("%s step %q writes a git ref or tag; only publish may", name, s["name"])
			}
		}
	}
}

// Every action is pinned to a full commit SHA.
func TestReleaseActionsArePinned(t *testing.T) {
	_, jobs := releaseJobs(t)
	for name, job := range jobs {
		for _, s := range stepsOf(job) {
			use, ok := s["uses"].(string)
			if ok && !releasePinnedUse.MatchString(use) {
				t.Errorf("%s uses %s, not pinned to a commit", name, use)
			}
		}
	}
}

// verify checks the image with the exact identity this workflow signs with.
func TestVerifyPinsTheExactSigningIdentity(t *testing.T) {
	_, jobs := releaseJobs(t)
	var cosign string
	for _, s := range stepsOf(jobs["verify"]) {
		if strings.Contains(stepRun(s), "cosign verify") {
			cosign = stepRun(s)
		}
	}
	for _, want := range []string{
		`--certificate-identity "https://github.com/$GITHUB_REPOSITORY/.github/workflows/release.yml@refs/heads/main"`,
		"--certificate-oidc-issuer https://token.actions.githubusercontent.com",
		`--certificate-github-workflow-repository "$GITHUB_REPOSITORY"`,
		"--certificate-github-workflow-trigger workflow_dispatch",
		`--certificate-github-workflow-sha "$COMMIT"`,
	} {
		if !strings.Contains(cosign, want) {
			t.Errorf("verify's cosign verify lacks %s", want)
		}
	}
}

func publishStep(t *testing.T, jobs map[string]map[string]any, prefix string) (int, map[string]any) {
	t.Helper()
	for i, s := range stepsOf(jobs["publish"]) {
		if name, _ := s["name"].(string); strings.HasPrefix(name, prefix) {
			return i, s
		}
	}
	t.Fatalf("publish has no step %q", prefix)
	return -1, nil
}

// publish creates the annotated tag at $COMMIT before anything else (and
// accepts an existing tag only if it is that same tag), creates the release
// only against that tag, makes a leftover draft exactly this release, never
// reads a failed image lookup as "absent", and moves the floating tags only
// for the newest stable release.
func TestPublishIsIdempotentAndCannotMoveATag(t *testing.T) {
	_, jobs := releaseJobs(t)
	tagAt, tag := publishStep(t, jobs, "1. Create the release tag")
	releaseAt, release := publishStep(t, jobs, "2. GitHub Release")
	_, image := publishStep(t, jobs, "3. Image version tag")
	_, floating := publishStep(t, jobs, "4. Floating tags")
	if tagAt > releaseAt {
		t.Error("publish creates the release before the tag")
	}
	for _, want := range []string{`!= tag ]`, `"commit $COMMIT"`, "git/tags", "-f object=\"$COMMIT\" -f type=commit", "git/refs"} {
		if !strings.Contains(stepRun(tag), want) {
			t.Errorf("the tag step lacks %s", want)
		}
	}
	for _, s := range stepsOf(jobs["publish"]) {
		if strings.Contains(stepRun(s), "sleep") {
			t.Errorf("publish step %q waits; the approval is the environment's, not a polling loop", s["name"])
		}
	}
	for _, want := range []string{"--verify-tag", "delete-asset", "--notes-file release-notes.md"} {
		if !strings.Contains(stepRun(release), want) {
			t.Errorf("the release step lacks %s", want)
		}
	}
	if run := stepRun(image); !strings.Contains(run, "not found") || !strings.Contains(run, "already points at") {
		t.Error("the image tag step does not distinguish an existing tag, a missing one and a failed lookup")
	}
	if cond := fmt.Sprint(floating["if"]); !strings.Contains(cond, "steps.newest.outputs.newest") {
		t.Errorf("floating tags move under %q, want only for the newest stable release", cond)
	}
}

// release.sh is safe for an agent to run: it creates no tag, pushes nothing,
// creates no release and never approves a deployment, and it tells the user
// where to approve. Its watch loop reads the run's status, which needs no
// Deployments permission.
func TestReleaseScriptCannotTagOrApprove(t *testing.T) {
	script := readFile(t, "release.sh")
	code := stripShellComments(script)
	for _, forbidden := range []string{"pending_deployments", "git/tags", "git/refs", "git tag", "git push", "release create", "RELEASE_CONFIRM"} {
		if strings.Contains(code, forbidden) {
			t.Errorf("release.sh runs %q", forbidden)
		}
	}
	for _, want := range []string{"approve at $run_url (GitHub web or mobile)", "waiting)", "--no-wait"} {
		if !strings.Contains(script, want) {
			t.Errorf("release.sh lacks %q", want)
		}
	}
}

// Only publish uses the release environment, and the summary the approver
// reads exists before it: version, commit, derivation, every check's result,
// and the CHANGELOG section.
func TestApproverReadsASummaryFirst(t *testing.T) {
	_, jobs := releaseJobs(t)
	for name, job := range jobs {
		if job["environment"] == "release" && name != "publish" {
			t.Errorf("%s runs in the release environment; only publish may", name)
		}
	}
	var run string
	for _, s := range stepsOf(jobs["summary"]) {
		run += stepRun(s)
	}
	for _, want := range []string{"GITHUB_STEP_SUMMARY", "$VERSION", "$COMMIT", "DERIVATION", "actions/runs/$GITHUB_RUN_ID", "CHANGELOG.md"} {
		if !strings.Contains(run, want) {
			t.Errorf("the approval summary lacks %s", want)
		}
	}
}

// stripShellComments drops comment lines and trailing comments, roughly:
// enough that prose explaining what a script never does is not mistaken for
// doing it.
func stripShellComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if i := strings.Index(line, " # "); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
