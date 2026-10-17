package webui

// Task 109's static checks over the Live view's sources.

import (
	"regexp"
	"strings"
	"testing"
)

// TestLiveKeysSourcesByTheStatedActor: the source is the actor the frame
// states, read and never derived, and the actor never joins the scope key —
// it is display metadata, not identity (task 109).
func TestLiveKeysSourcesByTheStatedActor(t *testing.T) {
	model := stripJSNoise(readAsset(t, "model.js"))
	for _, field := range []string{"observation.actor_id", "observation.actor_id_state", "observation.actor_type"} {
		if !strings.Contains(model, field) {
			t.Errorf("model.js never reads %s", field)
		}
	}
	if !regexp.MustCompile(`edgeKey\(sourceID, fingerprintID\)`).MatchString(model) {
		t.Error("an edge is not keyed by (actor, fingerprint)")
	}
	scope := regexp.MustCompile(`(?s)export function scopeKey\(scope\) \{.*?\n\}`).FindString(model)
	if scope == "" || strings.Contains(scope, "actor") {
		t.Errorf("scopeKey must not include the actor:\n%s", scope)
	}
}
