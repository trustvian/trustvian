package webui

// The behavioral layer in the browser (task 083).
//
// Source assertions, like the rest of this package's suite: the shipped assets
// are read and checked for the properties that matter, because there is no
// JavaScript runtime in this test binary.

import (
	"regexp"
	"strings"
	"testing"
)

// TestInspectorStatesTheBehaviorLayer requires each of the four layers, and both
// non-layer states, to have a sentence a reader can act on.
func TestInspectorStatesTheBehaviorLayer(t *testing.T) {
	source := readAsset(t, "inspector.js")

	// The value is read from the server, never derived in the browser.
	if !strings.Contains(source, "edge.behaviorLayer") {
		t.Error("inspector.js does not read edge.behaviorLayer")
	}

	for _, want := range []string{
		"Model call", "Tool call", "Retrieval", "Outbound request",
		"Layer not classified",
		"does not recognize",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("inspector.js states no case for %q", want)
		}
	}

	// A closed map rather than interpolation: an unrecognized value from a newer
	// server must not be rendered as if the browser understood it.
	if !strings.Contains(source, "LAYER_SENTENCES") {
		t.Error("inspector.js does not use a closed layer map")
	}
}

// TestLiveViewCarriesTheLayerWithoutDefaulting is the difference from fidelity,
// asserted in the browser too: fidelity defaults an absent value to "transport",
// and the layer must not, because "not classified" is a real state.
func TestLiveViewCarriesTheLayerWithoutDefaulting(t *testing.T) {
	source := readAsset(t, "live.js")

	if !strings.Contains(source, `observation.behavior_layer || ""`) {
		t.Error("live.js does not read behavior_layer, or defaults it to something")
	}
	if strings.Contains(source, `observation.behavior_layer || "transport"`) {
		t.Error("live.js defaults an absent layer to transport, which asserts an " +
			"identity source the server did not state")
	}
}

// TestBehaviorLabelHasNoDanglingSeparator is 083's rendering criterion.
//
// A tool behavior carries no target, so any renderer that appends a separator
// unconditionally prints `export_customer →` with nothing after it. Every label
// builder in the shipped assets must filter empty parts.
func TestBehaviorLabelHasNoDanglingSeparator(t *testing.T) {
	graph := readAsset(t, "graph.js")

	// operationLabel joins only non-empty parts.
	if !strings.Contains(graph, `.filter((part) => part !== "")`) {
		t.Error("graph.js operationLabel does not filter empty parts, so a " +
			"target-less or name-less behavior would render a dangling separator")
	}

	// And no asset builds a label by unconditional concatenation with a
	// separator. This catches the shape rather than one instance of it.
	for _, name := range []string{"graph.js", "live.js", "inspector.js", "render.js"} {
		source := readAsset(t, name)
		// ` → ` or ` · ` immediately followed by a bare interpolation of a
		// possibly-empty descriptor field.
		bad := regexp.MustCompile(`[\x{2192}\x{00b7}]\s*\$\{\s*(edge|behavior)\.(targetName|target_name|operationName|operation_name)\s*\}`)
		if bad.MatchString(source) {
			t.Errorf("%s concatenates a separator with a descriptor field that may be "+
				"empty; filter the parts instead", name)
		}
	}
}

// TestInspectorRendersMissingTargetAsWords keeps the existing honest empty state,
// which is the other half of the same criterion.
func TestInspectorRendersMissingTargetAsWords(t *testing.T) {
	source := readAsset(t, "inspector.js")
	if !strings.Contains(source, "(no target recorded)") {
		t.Error("inspector.js no longer states a missing target in words")
	}
}
