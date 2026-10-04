//go:build !windows

package scripts_test

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/trustvian/trustvian/internal/store/postgres"
)

// The reference deployment's smoke test checks that a fresh database is
// created at the core baseline schema version. Its expectation is a literal
// in a shell script that only Nightly runs, so a schema bump left it stale
// from 2026-09-20 (#81, version 2) until this test. It now fails on the pull
// request that bumps postgres.SchemaVersion without the smoke test.
func TestSmokeTestExpectsTheCurrentSchemaVersion(t *testing.T) {
	script := readFile(t, "../deployments/docker-compose/smoke-test.sh")
	m := regexp.MustCompile(`(?m)^EXPECTED_SCHEMA_VERSION=(\d+)$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("smoke-test.sh does not define EXPECTED_SCHEMA_VERSION")
	}
	got, _ := strconv.Atoi(m[1])
	if got != postgres.SchemaVersion {
		t.Errorf("smoke-test.sh expects schema version %d, internal/store/postgres.SchemaVersion is %d", got, postgres.SchemaVersion)
	}
	if !regexp.MustCompile(`\[ "\$version" = "\$EXPECTED_SCHEMA_VERSION" \] \|\| fail`).MatchString(script) {
		t.Error("smoke-test.sh no longer compares the live schema version against EXPECTED_SCHEMA_VERSION")
	}
}
