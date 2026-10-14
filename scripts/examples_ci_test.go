//go:build !windows

package scripts_test

import (
	"regexp"
	"strings"
	"testing"
)

// The examples' READMEs quote each program's output, so CI runs every
// program, not only builds and tests the module, and `make examples` runs
// them the way CI resolves modules: with GOWORK=off, since a developer's
// go.work would resolve the root differently from an external consumer
// (finding F19 of #175).
func TestExamplesRunInCIWithoutAWorkspace(t *testing.T) {
	makefile := readFile(t, "../Makefile")
	recipe := regexp.MustCompile(`(?ms)^examples:[^\n]*\n((?:\t[^\n]*\n)+)`).FindStringSubmatch(makefile)
	if recipe == nil {
		t.Fatal("the Makefile has no examples target")
	}
	if !regexp.MustCompile(`GOWORK=off\s+(\$\(GO\)|go)\s+run\b`).MatchString(recipe[1]) {
		t.Errorf("make examples does not run the programs with GOWORK=off:\n%s", recipe[1])
	}

	doc := loadYAML(t, "../.github/workflows/ci.yml")
	jobs, _ := doc["jobs"].(map[string]any)
	job, _ := jobs["examples"].(map[string]any)
	steps, _ := job["steps"].([]any)
	ran := false
	for _, s := range steps {
		step, _ := s.(map[string]any)
		run, _ := step["run"].(string)
		dir, _ := step["working-directory"].(string)
		if strings.TrimSpace(run) == "make examples" && (dir == "" || dir == ".") {
			ran = true
		}
	}
	if !ran {
		t.Error("ci.yml's examples job does not run `make examples` from the repository root")
	}
}
