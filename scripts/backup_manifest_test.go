//go:build !windows

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The MANIFEST records the bare pg_dump version. A Debian build — the
// postgres:17-bookworm image the recovery drill runs the tools from —
// prints `pg_dump (PostgreSQL) 17.6 (Debian 17.6-1.pgdg120+1)`, and taking the
// last field wrote `pg_dump_version=17.6-1.pgdg120+1)` (finding F15 of #175).
// The client tools are stubs here, so this runs without PostgreSQL.
func TestBackupManifestRecordsTheBarePgDumpVersion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	stubs := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("psql", `for a; do q="$a"; done
case "$q" in
  "SHOW server_version") echo 17.6 ;;
  *to_regclass*) echo 2 ;;
  *"count(*)"*) echo 1 ;;
  *) echo 2 ;;
esac
`)
	write("pg_dump", `for a; do
  case "$a" in
    --version) echo "pg_dump (PostgreSQL) 17.6 (Debian 17.6-1.pgdg120+1)"; exit 0 ;;
    --file=*) printf 'PGDMP' >"${a#--file=}" ;;
  esac
done
`)
	write("pg_restore", `printf '%s\n' \
  "1; 1259 1 TABLE public trustvian_baseline trustvian" \
  "2; 1259 2 TABLE public trustvian_schema_version trustvian" \
  "3; 0 1 TABLE DATA public trustvian_baseline trustvian" \
  "4; 0 2 TABLE DATA public trustvian_schema_version trustvian"
`)
	output := filepath.Join(t.TempDir(), "backup")
	cmd := exec.Command("bash", "backup-postgres.sh", "--output", output)
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"), "PGDATABASE=trustvian")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backup-postgres.sh: %v\n%s", err, out)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "\npg_dump_version=17.6\n") {
		t.Errorf("MANIFEST does not record pg_dump_version=17.6:\n%s", manifest)
	}
}
