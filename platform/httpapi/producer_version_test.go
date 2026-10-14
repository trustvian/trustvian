package httpapi_test

import (
	"strings"
	"testing"

	"trustvian-platform/httpapi"
)

// A release build reports the release's version, not Go's "(devel)": the
// platform module's path is not under the repository's, so Go cannot stamp it
// from a tag, and the release build supplies it instead (finding F7 of #175).
func TestBuildVersionReportsTheReleaseVersion(t *testing.T) {
	tests := []struct {
		name    string
		release string
		want    string
	}{
		{"release build", "v0.12.1", "v0.12.1"},
		{"development build", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := httpapi.BuildVersion(tt.release)
			first, _, _ := strings.Cut(got, " ")
			if tt.want != "" && first != tt.want {
				t.Errorf("BuildVersion(%q) = %q, want it to start with %q", tt.release, got, tt.want)
			}
			if tt.want == "" && (first == "" || strings.HasPrefix(first, "v0.12.1")) {
				t.Errorf("BuildVersion(%q) = %q, want Go's own build information", tt.release, got)
			}
		})
	}
}
