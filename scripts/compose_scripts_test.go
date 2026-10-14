//go:build !windows

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reference deployment's smoke test and recovery drill start with
// `docker compose down -v`. Run as the deployment's own project they deleted
// whatever state a developer's `make compose-up` stack held (finding F15 of
// #175). Each now runs as a Compose project of its own, so every compose
// command it issues — the destructive ones above all — names that project.
//
// docker is a stub that records its arguments and fails `up`, so each script
// stops after its clean-slate step, and nothing here needs Docker.
func TestComposeScriptsTouchOnlyTheirOwnProject(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for script, project := range map[string]string{
		"smoke-test.sh":     "trustvian-smoke",
		"recovery-drill.sh": "trustvian-recovery-drill",
	} {
		t.Run(script, func(t *testing.T) {
			stubs := t.TempDir()
			log := filepath.Join(stubs, "docker.log")
			stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>" + log + "\n" +
				"for a; do [ \"$a\" = up ] && exit 1; done\nexit 0\n"
			if err := os.WriteFile(filepath.Join(stubs, "docker"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join("..", "deployments", "docker-compose", script))
			cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("%s passed with a docker whose `up` fails:\n%s", script, out)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("%s never ran docker: %v", script, err)
			}
			downs := 0
			for _, call := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if !strings.HasPrefix(call, "compose ") {
					continue
				}
				if !strings.HasPrefix(call, "compose --project-name "+project+" ") {
					t.Errorf("%s ran `docker %s`, outside its own project %s", script, call, project)
				}
				if strings.Contains(call, " down -v") {
					downs++
				}
			}
			if downs == 0 {
				t.Errorf("%s never cleaned its own project's slate; docker calls:\n%s", script, raw)
			}
		})
	}
}
