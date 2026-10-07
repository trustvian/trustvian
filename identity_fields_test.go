package trustvian_test

// Behavioral identity is exactly what it was (task 106's constraint, and every
// v0.12.0 task's): nothing new enters StableFeatures — and so no fingerprint —
// or a baseline key. Frequency evidence, operational evidence and the gates
// over them are computed beside identity, never inside it. A field added to
// either type fails here, next to the change that added it, rather than as a
// baseline reset in someone's deployment.

import (
	"reflect"
	"slices"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/internal/baseline"
)

func fieldNames(v any) []string {
	t := reflect.TypeOf(v)
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		names = append(names, t.Field(i).Name)
	}
	return names
}

func TestBehavioralIdentityHasNotGrown(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  []string
		want []string
	}{
		{"StableFeatures", fieldNames(trustvian.StableFeatures{}),
			[]string{"ActorType", "OperationCategory", "OperationName", "TargetName", "TargetCategory", "Environment"}},
		{"baseline.Key", fieldNames(baseline.Key{}), []string{"Scope", "ActorID", "Environment"}},
	} {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s fields = %v, want %v — a change to behavioral identity needs its own task "+
				"and a baseline migration plan", tt.name, tt.got, tt.want)
		}
	}
}
