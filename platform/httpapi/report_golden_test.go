package httpapi_test

// The golden version 2 documents (task 107-B): each is the report route's
// output for one fixture execution recorded through the real routes, written
// to cmd/trustvian-ci-render/testdata/golden, where the shared renderer's
// tests render it to the golden Markdown beside it. Nothing in those
// documents is written by hand. Set TRUSTVIAN_UPDATE_GOLDEN=1 to rewrite them.
//
// With TRUSTVIAN_TEST_POSTGRES_DSN set, every fixture is also recorded on
// PostgreSQL and its report must be byte-identical to SQLite's — and so,
// since the Markdown is a function of those bytes, is its rendering.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/trustvian/trustvian/event"

	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// goldenDir is the shared renderer's golden directory.
var goldenDir = filepath.Join("..", "..", "cmd", "trustvian-ci-render", "testdata", "golden")

// goldenProducerVersion replaces the build's control plane version, so a
// golden document does not change with the commit it was built from.
const goldenProducerVersion = "v0.13.0"

// goldenCLIVersion is the version every golden execution's runner records.
const goldenCLIVersion = "v0.13.0 3e86f9cead2586e7ee90e14185a8f34074b54240"

// goldenPricing prices the two fixture models.
func goldenPricing(t *testing.T) platform.Pricing {
	t.Helper()
	pricing, err := platform.NewPricing("golden-2026-10", "golden fixture prices, not real ones", "USD",
		"sha256:"+strings.Repeat("cd", 32), map[string]platform.ModelPrice{
			"gemma3:4b":  {InputMicrosPerMillion: 100000, OutputMicrosPerMillion: 200000},
			"qwen2.5:3b": {InputMicrosPerMillion: 80000, OutputMicrosPerMillion: 160000},
		})
	if err != nil {
		t.Fatal(err)
	}
	return pricing
}

// goldenAPI serves one store, with or without pricing.
func goldenAPI(t *testing.T, store platform.Store, pricing *platform.Pricing) *api {
	t.Helper()
	var options []platform.ControlPlaneOption
	if pricing != nil {
		options = append(options, platform.WithPricing(*pricing))
	}
	plane, err := platform.NewControlPlane(store, store, store, options...)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(plane, httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		t.Fatal(err)
	}
	return &api{t: t, server: handler}
}

// The steps the fixtures are built from, shaped like the demo agent's
// telemetry: a model call named by the GenAI conventions, the HTTP request
// beneath it, and named tool calls.
func modelCall(model, input, output, duration string) reportStep {
	return reportStep{op: model, category: event.OperationCategoryExternal, target: "ollama",
		duration: duration, status: event.StatusOK, envelope: map[string]string{
			"fidelity": "semantic", "behavior_layer": "model", "tokens_input": input, "tokens_output": output}}
}

func modelCallWithoutUsage(model, duration string) reportStep {
	return reportStep{op: model, category: event.OperationCategoryExternal, target: "ollama",
		duration: duration, status: event.StatusOK,
		envelope: map[string]string{"fidelity": "semantic", "behavior_layer": "model"}}
}

func ollamaPost(status, duration string) reportStep {
	spanStatus := event.StatusOK
	if status != "200" {
		spanStatus = event.StatusError
	}
	return reportStep{op: "POST", category: event.OperationCategoryHTTP, target: "ollama.localhost",
		duration: duration, status: spanStatus, envelope: map[string]string{
			"fidelity": "transport", "behavior_layer": "transport", "http_status_code": status}}
}

// toolCall is a tool the telemetry named (semantic), or the same tool's
// behavior proved only by transport, which classifies its layer as transport.
func toolCall(name, fidelity string) reportStep {
	layer := "tool"
	if fidelity == "transport" {
		layer = "transport"
	}
	return reportStep{op: name, category: event.OperationCategoryTool, noTarget: true, duration: "2000000",
		status: event.StatusOK, envelope: map[string]string{"fidelity": fidelity, "behavior_layer": layer}}
}

// goldenCase is one golden document: the fixture, whether a pricing table is
// configured, and anything done to the stored execution after it completed.
type goldenCase struct {
	name    string
	pricing bool
	// record records the execution(s) and returns the one reported.
	record func(a *api) string
	// afterwards changes the stored execution, given one statement runner.
	afterwards func(t *testing.T, exec func(string) error, id string)
}

func withLimits(k, j string, extra map[string]any) map[string]any {
	limits := repeatedLimitsBody(k, j)
	for name, value := range extra {
		limits[name] = value
	}
	return limits
}

func modelBegin(reference, candidate string) map[string]any {
	prompt := map[string]any{"name": "support-prompt", "digest": sha("e")}
	return map[string]any{
		"cli_version": goldenCLIVersion,
		"provenance": map[string]any{
			"scenario_digest": sha("a"), "inputs_declared": true, "input_digest": sha("b"),
			"reference": map[string]any{"model": reference, "prompt_ref": prompt},
			"candidate": map[string]any{"model": candidate, "prompt_ref": prompt},
		},
	}
}

var goldenCases = []goldenCase{
	{
		// The reference runs gemma3:4b, the candidate qwen2.5:3b. The model
		// behavior changes with it, the candidate adds a tool, the Ollama
		// endpoint answers one 429, and a pricing table prices both models.
		name: "v2-model-change", pricing: true,
		record: func(a *api) string {
			reference := func(seconds string) []reportStep {
				return []reportStep{
					modelCall("gemma3:4b", "1210", "305", seconds+"00000000"),
					ollamaPost("200", seconds+"10000000"),
					toolCall("lookup_customer", "semantic"), toolCall("send_reply", "semantic"),
					modelCall("gemma3:4b", "1490", "212", seconds+"00000000"),
					ollamaPost("200", seconds+"10000000"),
				}
			}
			candidate := func(seconds, status string) []reportStep {
				return []reportStep{
					modelCall("qwen2.5:3b", "1388", "402", seconds+"00000000"),
					ollamaPost(status, seconds+"20000000"),
					toolCall("lookup_customer", "semantic"), toolCall("export_customer", "semantic"),
					modelCall("qwen2.5:3b", "1602", "377", seconds+"00000000"),
					ollamaPost("200", seconds+"20000000"),
				}
			}
			a.recordExecution(reportFixture{
				id: "golden-model-change", begin: modelBegin("gemma3:4b", "qwen2.5:3b"),
				reference: [][]reportStep{reference("8"), reference("9"), reference("7")},
				candidate: [][]reportStep{candidate("12", "200"), candidate("14", "429"), candidate("11", "200")},
				limits: withLimits("2", "0", map[string]any{
					"max_lost_behaviors":    "0",
					"max_calls_per_run":     []map[string]string{{"target": "ollama.localhost", "max": "4"}},
					"max_llm_calls_per_run": "3",
				}),
			})
			return "golden-model-change"
		},
	},
	{
		// The same scenario and model on recorded inputs that changed: the
		// reference side is reused (`last`) from an execution that ran other
		// inputs, so sameness says the inputs differ, and warns.
		name: "v2-input-change",
		record: func(a *api) string {
			steps := func(extra ...reportStep) []reportStep {
				return append([]reportStep{
					modelCall("gemma3:4b", "1100", "250", "800000000"), ollamaPost("200", "810000000"),
					toolCall("lookup_customer", "semantic"),
				}, extra...)
			}
			first := modelBegin("gemma3:4b", "gemma3:4b")
			a.recordExecution(reportFixture{
				id: "golden-inputs-before", begin: first,
				reference: [][]reportStep{steps(), steps()}, candidate: [][]reportStep{steps(), steps()},
				limits: withLimits("2", "0", nil),
			})
			second := modelBegin("gemma3:4b", "gemma3:4b")
			second["provenance"].(map[string]any)["input_digest"] = sha("c")
			delete(second["provenance"].(map[string]any), "reference")
			second["reference"] = map[string]string{"mode": "last"}
			a.recordExecution(reportFixture{
				id: "golden-inputs-after", begin: second,
				candidate: [][]reportStep{steps(toolCall("lookup_invoice", "semantic")),
					steps(toolCall("lookup_invoice", "semantic"))},
				limits: withLimits("2", "0", nil),
			})
			return "golden-inputs-after"
		},
	},
	{
		// A producer whose model spans carry no usage attributes: tokens are
		// not available, and so, although a pricing table is configured, is
		// the cost.
		name: "v2-no-usage", pricing: true,
		record: func(a *api) string {
			steps := func() []reportStep {
				return []reportStep{
					modelCallWithoutUsage("gemma3:4b", "900000000"), ollamaPost("200", "910000000"),
					toolCall("lookup_customer", "semantic"),
				}
			}
			a.recordExecution(reportFixture{
				id: "golden-no-usage", begin: modelBegin("gemma3:4b", "gemma3:4b"),
				reference: [][]reportStep{steps(), steps()}, candidate: [][]reportStep{steps(), steps()},
				limits: withLimits("2", "0", nil),
			})
			return "golden-no-usage"
		},
	},
	{
		// One behavior the telemetry named in some observations and only
		// transport proved in others: its fidelity is mixed, with counts.
		name: "v2-mixed-fidelity",
		record: func(a *api) string {
			reference := []reportStep{toolCall("lookup_customer", "semantic"), toolCall("lookup_customer", "semantic")}
			candidate := []reportStep{toolCall("lookup_customer", "semantic"), toolCall("lookup_customer", "transport")}
			a.recordExecution(reportFixture{
				id: "golden-mixed-fidelity", begin: map[string]any{"cli_version": goldenCLIVersion},
				reference: [][]reportStep{reference, reference}, candidate: [][]reportStep{candidate, reference},
				limits: withLimits("2", "0", nil),
			})
			return "golden-mixed-fidelity"
		},
	},
	{
		// max_llm_calls_per_run set on a run with no model-layer evidence: the
		// check is deferred, with the evidence it is missing (task 081, D9).
		name: "v2-deferred-gate",
		record: func(a *api) string {
			steps := []reportStep{
				{op: "POST", category: event.OperationCategoryHTTP, target: "api.model.example",
					duration: "700000000", status: event.StatusOK, envelope: map[string]string{"http_status_code": "200"}},
			}
			a.recordExecution(reportFixture{
				id: "golden-deferred", begin: map[string]any{"cli_version": goldenCLIVersion},
				reference: [][]reportStep{steps, steps}, candidate: [][]reportStep{steps, steps},
				limits: withLimits("2", "0", map[string]any{"max_llm_calls_per_run": "2"}),
			})
			return "golden-deferred"
		},
	},
	{
		// An execution completed before schema 14: its gate snapshot is NULL,
		// so the report says the gate was not recorded and derives none.
		name: "v2-gate-not-recorded",
		record: func(a *api) string {
			steps := []reportStep{toolCall("lookup_customer", "semantic"), ollamaPost("200", "500000000")}
			a.recordExecution(reportFixture{
				id:        "golden-pre-v14",
				reference: [][]reportStep{steps, steps},
				candidate: [][]reportStep{append(steps, toolCall("export_customer", "semantic")), steps},
				limits:    withLimits("2", "0", nil),
			})
			return "golden-pre-v14"
		},
		afterwards: func(t *testing.T, exec func(string) error, id string) {
			if err := exec(`UPDATE platform_scenario_executions SET gate_snapshot = NULL WHERE id = '` + id + `'`); err != nil {
				t.Fatal(err)
			}
		},
	},
}

// goldenReport records one case on a store and returns its report, with the
// producer version replaced and indented as the golden file holds it.
func goldenReport(t *testing.T, c goldenCase, store platform.Store, exec func(string) error) []byte {
	t.Helper()
	var pricing *platform.Pricing
	if c.pricing {
		p := goldenPricing(t)
		pricing = &p
	}
	a := goldenAPI(t, store, pricing)
	id := c.record(a)
	if c.afterwards != nil {
		c.afterwards(t, exec, id)
	}
	raw := a.report(id)
	var doc struct {
		Producers struct {
			Version string `json:"control_plane_version"`
		} `json:"producers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte(`"control_plane_version":"`+doc.Producers.Version+`"`),
		[]byte(`"control_plane_version":"`+goldenProducerVersion+`"`))
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	indented.WriteByte('\n')
	return indented.Bytes()
}

func TestGoldenReportDocuments(t *testing.T) {
	dsn := os.Getenv("TRUSTVIAN_TEST_POSTGRES_DSN")
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "platform.db")
			sqlite, err := platform.OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlite.Close() })
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { raw.Close() })
			got := goldenReport(t, c, sqlite, func(statement string) error {
				_, err := raw.Exec(statement)
				return err
			})

			golden := filepath.Join(goldenDir, c.name+".json")
			if os.Getenv("TRUSTVIAN_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (set TRUSTVIAN_UPDATE_GOLDEN=1 to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("the report differs from %s:\n%s", golden, got)
			}

			if strings.TrimSpace(dsn) == "" {
				return
			}
			schema := isolatedSchema(t, dsn)
			postgres, err := platform.OpenPostgresStore(t.Context(), platform.PostgresConfig{DSN: schema})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { postgres.Close() })
			pool, err := pgxpool.New(context.Background(), schema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			onPostgres := goldenReport(t, c, postgres, func(statement string) error {
				_, err := pool.Exec(context.Background(), statement)
				return err
			})
			if !bytes.Equal(onPostgres, got) {
				t.Fatalf("PostgreSQL and SQLite disagree on %s:\n%s", c.name, onPostgres)
			}
		})
	}
}

// Each golden document is what its name says, checked on the document itself
// so a fixture change cannot silently stop exercising its case.
func TestGoldenReportDocumentsShowTheirCase(t *testing.T) {
	read := func(name string) map[string]any {
		raw, err := os.ReadFile(filepath.Join(goldenDir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return decodeAny(t, raw)
	}
	get := func(doc any, steps ...any) any {
		for _, s := range steps {
			switch k := s.(type) {
			case string:
				doc = doc.(map[string]any)[k]
			case int:
				doc = doc.([]any)[k]
			}
		}
		return doc
	}
	for _, tt := range []struct {
		name  string
		steps []any
		want  any
	}{
		{"v2-model-change", []any{"comparison", "sameness", "same_model"}, "false"},
		{"v2-model-change", []any{"comparison", "operational", "cost", "comparable"}, true},
		{"v2-model-change", []any{"comparison", "gate", "frequency_checks", 3, "state"}, "evaluated"},
		{"v2-input-change", []any{"comparison", "sameness", "same_inputs"}, "false"},
		{"v2-input-change", []any{"reference", "mode"}, "last"},
		{"v2-no-usage", []any{"comparison", "operational", "tokens", "reason"}, "both_unavailable"},
		{"v2-deferred-gate", []any{"comparison", "gate", "frequency_checks", 3, "state"}, "deferred"},
		{"v2-deferred-gate", []any{"comparison", "operational", "cost_reason"}, "pricing_not_configured"},
		{"v2-gate-not-recorded", []any{"comparison", "gate", "state"}, "not_recorded"},
		{"v2-gate-not-recorded", []any{"comparison", "behaviors", 0, "classification"}, "not_recorded"},
	} {
		if got := get(read(tt.name), tt.steps...); got != tt.want {
			t.Errorf("%s %v = %v, want %v", tt.name, tt.steps, got, tt.want)
		}
	}
	mixed := false
	for _, b := range get(read("v2-mixed-fidelity"), "comparison", "behaviors").([]any) {
		if f, ok := b.(map[string]any)["candidate_fidelity"].(map[string]any); ok && f["mixed"] == true {
			mixed = true
		}
	}
	if !mixed {
		t.Error("v2-mixed-fidelity has no mixed-fidelity behavior")
	}
}
