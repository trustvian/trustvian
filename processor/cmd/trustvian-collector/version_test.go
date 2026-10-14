package main

import (
	"strings"
	"testing"
)

// A release build reports the release version, not a hardcoded "0.0.0-dev":
// the processor module's path is not under the repository's root module, so Go
// cannot stamp it from a tag, and the release writes it into main.version
// (finding F7 of #175). Any other build reports Go's build information.
func TestCollectorVersion(t *testing.T) {
	for _, tt := range []struct{ stamped, want string }{
		{"v0.12.1", "v0.12.1"},
		{"", "(devel)"},
	} {
		version = tt.stamped
		if got := moduleVersion(); got != tt.want {
			t.Errorf("stamped %q: moduleVersion() = %q, want %q", tt.stamped, got, tt.want)
		}
		if got := versionLine(); !strings.HasPrefix(got, "trustvian-collector "+tt.want) {
			t.Errorf("stamped %q: --version prints %q, want it to start with %q", tt.stamped, got, "trustvian-collector "+tt.want)
		}
	}
	version = ""

	_, showVersion, err := parseArgs([]string{"--version"})
	if err != nil || !showVersion {
		t.Errorf("parseArgs(--version) = %t, %v, want true, nil", showVersion, err)
	}
}
