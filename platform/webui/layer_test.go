package webui

// The behavioral layer in the browser (task 083).
//
// Source assertions, like the rest of this package's suite: the shipped assets
// are read and checked for the properties that matter, because there is no
// JavaScript runtime in this test binary.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// directionWords are the words a layer sentence may never contain.
//
// The layer says which instrumentation layer supplied an operation's identity. It
// says nothing about which way the call went: `transport` covers inbound SERVER
// and CONSUMER spans, outbound CLIENT and PRODUCER spans, and spans whose kind
// establishes no direction at all. A sentence claiming one of these would be
// asserting something the value does not carry — which is the defect these tests
// exist to prevent recurring.
var directionWords = []string{
	"outbound", "inbound", "incoming", "outgoing",
	"egress", "ingress", "client-side", "server-side",
}

// layerCases is every input layerSentence must answer for, and the leading words
// its answer must start with. The full sentence is asserted in the behavioral
// test below; here the prefix is what pins which branch fired.
var layerCases = []struct {
	layer      string
	wantPrefix string
}{
	{"model", "Model call:"},
	{"tool", "Tool call:"},
	{"retrieval", "Retrieval:"},
	{"transport", "Transport operation:"},
	{"", "Layer not classified:"},
	{"chain", "Layer reported as a value this page does not recognize"},
	{"Transport", "Layer reported as a value this page does not recognize"},
	{"http", "Layer reported as a value this page does not recognize"},
}

// TestInspectorStatesTheBehaviorLayer checks the structure of the layer
// rendering, without node, so this coverage exists on any machine.
//
// The load-bearing assertion is the *absence* property: no sentence anywhere in
// the layer map or its two fallbacks may contain a direction word. That is
// stronger than checking a replacement phrase is present, because it fails for
// any future wording that reintroduces the claim rather than only for the one
// phrase that did.
func TestInspectorStatesTheBehaviorLayer(t *testing.T) {
	source := readAsset(t, "inspector.js")

	// The value is read from the server, never derived in the browser.
	if !strings.Contains(source, "edge.behaviorLayer") {
		t.Error("inspector.js does not read edge.behaviorLayer")
	}

	// A closed map rather than interpolation: an unrecognized value from a newer
	// server must not be rendered as if the browser understood it.
	if !strings.Contains(source, "LAYER_SENTENCES") {
		t.Error("inspector.js does not use a closed layer map")
	}

	for _, sentence := range layerSentenceLiterals(t, source) {
		lower := strings.ToLower(sentence)
		for _, word := range directionWords {
			if strings.Contains(lower, word) {
				t.Errorf("a layer sentence claims a direction (%q):\n  %s\n"+
					"The layer does not establish direction: transport covers inbound "+
					"SERVER and CONSUMER spans as well as outbound CLIENT and PRODUCER "+
					"spans, and spans with no direction at all.", word, sentence)
			}
		}
	}
}

// layerSentenceLiterals extracts every string literal the layer rendering can
// return: the closed map's values and the two fallbacks.
//
// Source extraction rather than a phrase match, so the assertion covers wording
// that does not exist yet.
func layerSentenceLiterals(t *testing.T, source string) []string {
	t.Helper()

	start := strings.Index(source, "const LAYER_SENTENCES = {")
	if start < 0 {
		t.Fatal("LAYER_SENTENCES is not declared in inspector.js")
	}
	end := strings.Index(source[start:], "\n};")
	if end < 0 {
		t.Fatal("LAYER_SENTENCES is not terminated")
	}
	block := source[start : start+end]

	fnStart := strings.Index(source, "export function layerSentence(")
	if fnStart < 0 {
		t.Fatal("layerSentence is not exported from inspector.js")
	}
	fnEnd := strings.Index(source[fnStart:], "\n}")
	if fnEnd < 0 {
		t.Fatal("layerSentence is not terminated")
	}
	body := source[fnStart : fnStart+fnEnd]

	literal := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	var out []string
	for _, region := range []string{block, body} {
		for _, m := range literal.FindAllStringSubmatch(region, -1) {
			if len(m[1]) > 20 { // skip keys and short tokens
				out = append(out, m[1])
			}
		}
	}
	if len(out) < 6 {
		t.Fatalf("expected at least 6 layer sentences, extracted %d: %q", len(out), out)
	}
	return out
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

// ---------------------------------------------------------------------
// Behavioral: the real function, executed
// ---------------------------------------------------------------------

// TestLayerSentenceBehavior calls the shipped layerSentence and checks what it
// returns, rather than checking what its source looks like.
//
// This repository ships no JavaScript toolchain — no package.json, no runner, no
// dependency — and this test adds none. It runs the asset under `node` when one
// is on PATH and skips otherwise, the same arrangement the end-to-end tests use
// for `go` and `git`. The structural assertions above are what hold on a machine
// without node, so skipping here never leaves the behavior unchecked.
func TestLayerSentenceBehavior(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the structural layer assertions still ran")
	}

	dir := t.TempDir()
	// The asset is embedded, so it and its one import are written out to be
	// imported as real modules rather than re-implemented here.
	for _, name := range []string{"inspector.js", "graph.js", "render.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(readAsset(t, name)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	const driver = `
import { layerSentence } from "./inspector.js";
const out = {};
for (const layer of JSON.parse(process.argv[2])) {
  out[layer] = layerSentence(layer);
}
process.stdout.write(JSON.stringify(out));
`
	if err := os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(driver), 0o600); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	inputs := make([]string, 0, len(layerCases))
	for _, c := range layerCases {
		inputs = append(inputs, c.layer)
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		t.Fatalf("encode inputs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, "driver.mjs", string(encoded))
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running layerSentence under node: %v\n%s", err, stderr.String())
	}

	var got map[string]string
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("decode driver output: %v\n%s", err, stdout)
	}

	for _, c := range layerCases {
		sentence, ok := got[c.layer]
		if !ok {
			t.Errorf("layerSentence(%q) returned nothing", c.layer)
			continue
		}
		if !strings.HasPrefix(sentence, c.wantPrefix) {
			t.Errorf("layerSentence(%q) = %q, want it to start with %q",
				c.layer, sentence, c.wantPrefix)
		}
		if strings.TrimSpace(sentence) == "" {
			t.Errorf("layerSentence(%q) returned an empty sentence", c.layer)
		}
		// The property that motivated this test: no answer, for any input, may
		// claim a direction.
		lower := strings.ToLower(sentence)
		for _, word := range directionWords {
			if strings.Contains(lower, word) {
				t.Errorf("layerSentence(%q) claims a direction (%q): %q",
					c.layer, word, sentence)
			}
		}
	}

	// And the specific regression: transport is described as a transport mapping,
	// not as a request that went one particular way.
	transport := got["transport"]
	if !strings.Contains(transport, "transport mapping") {
		t.Errorf("layerSentence(\"transport\") no longer says the behavior uses the "+
			"transport mapping: %q", transport)
	}

	// Distinct inputs that are real layers must produce distinct sentences, so a
	// reader can actually tell a model call from a tool call.
	seen := map[string]string{}
	for _, layer := range []string{"model", "tool", "retrieval", "transport"} {
		if prev, clash := seen[got[layer]]; clash {
			t.Errorf("layers %q and %q render identically: %q", prev, layer, got[layer])
		}
		seen[got[layer]] = layer
	}
}
