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

// Every job runs in the release environment, and the order is preflight →
// gates → build and image → verify → publish.
func TestReleaseJobOrder(t *testing.T) {
	_, jobs := releaseJobs(t)
	want := map[string][]string{
		"preflight": nil,
		"gates":     {"preflight"},
		"build":     {"gates"},
		"image":     {"gates"},
		"verify":    {"build", "image"},
		"publish":   {"preflight", "gates", "build", "image", "verify"},
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
		if jobs[name]["environment"] != "release" {
			t.Errorf("%s runs in environment %v, want release", name, jobs[name]["environment"])
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

// The image job pushes by digest and nothing else; tags are created only in
// publish, from that digest. No job pushes git, and no job creates a git ref
// or tag object: the release tag is a human Organization Admin's, made by
// scripts/release.sh, and publish only reads it.
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
			if refCreation.MatchString(run) && (strings.Contains(run, "-X POST") || strings.Contains(run, " -f ")) {
				t.Errorf("%s step %q writes a git ref or tag", name, s["name"])
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

// publish waits for and checks the human's tag before anything else, creates
// the release only against that existing tag, makes a leftover draft exactly
// this release, never reads a failed image lookup as "absent", and moves the
// floating tags only for the newest stable release.
func TestPublishIsIdempotentAndCannotMoveATag(t *testing.T) {
	_, jobs := releaseJobs(t)
	waitAt, wait := publishStep(t, jobs, "1. Wait for the release tag")
	releaseAt, release := publishStep(t, jobs, "2. GitHub Release")
	_, image := publishStep(t, jobs, "3. Image version tag")
	_, floating := publishStep(t, jobs, "4. Floating tags")
	if waitAt > releaseAt {
		t.Error("publish creates the release before checking the tag")
	}
	for _, want := range []string{`type" != tag`, `"commit $COMMIT"`} {
		if !strings.Contains(stepRun(wait), want) {
			t.Errorf("the tag wait does not check %s", want)
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

// release.sh waits for jobs by their display names; they must be the
// workflow's, or the script would wait forever. Its confirmation reads a
// terminal and cannot be bypassed by an environment variable.
func TestReleaseScriptMatchesTheWorkflow(t *testing.T) {
	_, jobs := releaseJobs(t)
	names := map[string]bool{}
	for _, job := range jobs {
		name, _ := job["name"].(string)
		if strings.Contains(name, "${{ matrix.os }}") {
			for _, os := range []string{"ubuntu-latest", "macos-latest"} {
				names[strings.ReplaceAll(name, "${{ matrix.os }}", os)] = true
			}
			continue
		}
		names[name] = true
	}
	script := readFile(t, "release.sh")
	m := regexp.MustCompile(`(?m)^readonly VERIFIED_JOBS='(\[.*\])'$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("release.sh has no VERIFIED_JOBS")
	}
	listed := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1)
	for _, l := range listed {
		if !names[l[1]] {
			t.Errorf("release.sh waits for %q, which is not a job in release.yml", l[1])
		}
	}
	if !strings.Contains(script, `[ "$ok" = `+fmt.Sprint(len(listed))+` ]`) {
		t.Errorf("release.sh does not wait for all %d listed jobs", len(listed))
	}
	if strings.Contains(script, "RELEASE_CONFIRM") || !strings.Contains(script, "</dev/tty") {
		t.Error("release.sh's confirmation can be skipped or does not read the terminal")
	}
}
