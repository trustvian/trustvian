# Go SDK Guide

```bash
go get github.com/trustvian/trustvian
```

## The `Event` type

`event.Event` (package `github.com/trustvian/trustvian/event`) is the
one type every caller constructs. It's a generic shape that covers
HTTP calls, service-to-service RPC, database operations, external
destinations, and AI-agent tool calls uniformly — see
[Use Cases](use-cases.md) for one worked example of each.

```go
type Event struct {
	ID         string
	Timestamp  time.Time
	Actor      Actor
	Operation  Operation
	Target     Target          // optional
	Attributes map[string]any  // optional
	Context    Context         // optional
}

type Actor struct {
	ID                 string
	Type               ActorType // service | user | service_account | ai_agent | device | unknown
	IdentityConfidence float64   // [0,1] — an input Trustvian trusts, not something it computes
}

type Operation struct {
	Category  OperationCategory  // http | db | rpc | tool | external
	Name      string             // e.g. "POST /payment", "SELECT accounts", "search_customer"
	Direction OperationDirection // inbound | outbound | "" (optional)
}

type Target struct {
	Name     string         // the destination: a service, database, or host
	Category TargetCategory // internal | external | database | "" (optional)
}

type Context struct {
	Environment string
	TraceID     string
	SpanID      string
}
```

`Event` carries `json` struct tags (snake_case field names) so it can
be read from a file — this is what the CLI does; see
[CLI Guide](cli-guide.md#event-json-format).

Two attribute keys have special meaning if present in `Attributes`,
picked up by `internal/features`:

| Key | Type | Meaning |
|---|---|---|
| `duration_ms` | `float64`/`int`/`int64` | operation latency, feeds the anomaly latency signal |
| `error` | `bool` | whether the operation errored, feeds the anomaly error signal |

Call `ev.Validate()` (or just call `Analyze`, which does this for you)
to check required fields are set — `ID`, `Timestamp`,
`Actor.ID`/`Type`/`IdentityConfidence`, `Operation.Category`/`Name`.
`Target`, `Attributes`, and `Context` are optional.

`Timestamp` is also checked for encodability: a year outside `[0,9999]` or a
zone offset of 24 hours or more is constructible in Go but cannot be written
as RFC 3339, and would produce a decision record `json.Marshal` refuses. Both
return `event.ErrInvalidTimestamp`. Any timestamp from `time.Now()` or a
parsed RFC 3339 string is fine.

## Constructing an `Engine`

```go
engine := trustvian.NewEngine()
```

With no options, you get: an in-memory `Store` (baselines don't
survive a process restart), a `Policy` with no rules and an
`OBSERVE_ONLY` default (never blocks, never silently allows), and
default anomaly/trust thresholds. This is enough to call `Analyze` and
`Observe` and get real, differentiated `Anomaly`/`Trust` output — you
just won't get a differentiated `Decision` until you configure a
`Policy` (see [Policy Guide](policy-guide.md)).

## Analyze

```go
result, err := engine.Analyze(ctx, ev)
```

Runs `ev` through the full pipeline. Strictly read-only — it never
writes to the `Store`, no matter how many times or in what order you
call it. Returns an error if `ev.Validate()` fails.

## `Result`

```go
type Result struct {
	Event       event.Event
	Features    features.Features   // internal type, fields readable
	Fingerprint fingerprint.Fingerprint
	BaselineKey baseline.Key
	Anomaly     anomaly.Anomaly     // Score, Confidence, Contributors []Signal
	Trust       trust.Trust         // Score, Risk, plus every input retained
	Decision    policy.Decision     // "allow" | "observe_only" | "alert" | "challenge" | "require_approval" | "block"
	Explanation policy.Explanation  // RuleName, Reason, MatchedDefault
}
```

Every field is readable from outside the module even though several of
the field *types* live under `internal/` — see
[Architecture § package boundaries](ARCHITECTURE.md#package-boundaries)
for why that's fine. In practice:

```go
fmt.Println(result.Trust.Score, result.Trust.Risk, result.Decision)
for _, signal := range result.Anomaly.Contributors {
	fmt.Println(signal.Name, signal.Value, signal.Detail)
}
```

`result.Trust.Explain() string` renders `Trust`'s retained fields
(`Score`, `Risk`, `IdentityConfidence`, `AnomalyScore`,
`AnomalyConfidence`, `ContextRisk`) as one human-readable sentence —
pure formatting, no extra computation — for logging or display without
hand-assembling the components yourself:

```go
fmt.Println(result.Trust.Explain())
// "trust 0.35 (high): identity confidence 0.97, anomaly 0.91 at full confidence, context risk 0.10"
```

`result.Explain() string` renders the *whole* decision — not just
`Trust` — as a multi-line, human-readable summary: the `Decision`,
`Trust.Explain()`'s sentence, the anomaly score and confidence, every
contributing signal (name, value, detail) if any fired, and which
policy rule or default produced the outcome. It's pure formatting over
`Result`'s existing fields, reusing `Trust.Explain()` internally:

```go
fmt.Print(result.Explain())
// Decision: observe_only
// trust 1.00 (low): identity confidence 1.00, anomaly 1.00 at 0% confidence, context risk 0.00
// Anomaly score: 1.00 (confidence 0.00)
// Detected:
//   - categorical_novelty: 1.00 (fingerprint never observed for this actor)
// Policy: default action (no policy rules configured; observing by default)
```

## Observe and learning

```go
learned, err := engine.Observe(ctx, result)
```

Feeds `result` back into the `Baseline` — but only if `result.Decision`
is one where the action actually proceeded (`ALLOW`, `OBSERVE_ONLY`,
`ALERT`); anything held or stopped (`CHALLENGE`, `REQUIRE_APPROVAL`,
`BLOCK`) is silently skipped (`learned` comes back `false`, `err` is
`nil`). This is what makes it safe to call `Observe` after *every*
`Analyze` unconditionally — you never need to check the decision
yourself first.

## Watching trust mature

The following program calls `Analyze`+`Observe` in a loop for the same
actor doing the same operation 25 times, then analyzes one clearly
suspicious, unrelated event. It's a genuine external-module program —
compiled and run against this repository with no code living inside
it — output included verbatim, not hand-written:

```go
package main

import (
	"context"
	"fmt"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func paymentEvent(id string) event.Event {
	return event.Event{
		ID:        id,
		Timestamp: time.Now(),
		Actor: event.Actor{
			ID:                 "order-service",
			Type:               event.ActorTypeService,
			IdentityConfidence: 0.97,
		},
		Operation: event.Operation{Category: event.OperationCategoryDB, Name: "SELECT orders"},
		Target:    event.Target{Name: "orders-db"},
		Context:   event.Context{Environment: "production"},
		Attributes: map[string]any{"duration_ms": 12},
	}
}

func main() {
	engine := trustvian.NewEngine()
	ctx := context.Background()

	for i := 1; i <= 25; i++ {
		result, _ := engine.Analyze(ctx, paymentEvent(fmt.Sprintf("warm-up-%d", i)))
		learned, _ := engine.Observe(ctx, result)
		if i == 1 || i == 10 || i == 20 || i == 25 {
			fmt.Printf("event %2d: confidence=%.2f trust=%.2f decision=%s learned=%v\n",
				i, result.Anomaly.Confidence, result.Trust.Score, result.Decision, learned)
		}
	}

	suspicious := paymentEvent("attack")
	suspicious.Actor.IdentityConfidence = 0.2
	suspicious.Operation = event.Operation{Category: event.OperationCategoryExternal, Name: "POST /exfil"}
	suspicious.Target = event.Target{Name: "unknown-host"}

	result, _ := engine.Analyze(ctx, suspicious)
	fmt.Printf("suspicious event: confidence=%.2f trust=%.2f decision=%s\n",
		result.Anomaly.Confidence, result.Trust.Score, result.Decision)
}
```

Output:

```
event  1: confidence=0.00 trust=0.97 decision=observe_only learned=true
event 10: confidence=0.45 trust=0.73 decision=observe_only learned=true
event 20: confidence=0.95 trust=0.92 decision=observe_only learned=true
event 25: confidence=1.00 trust=0.97 decision=observe_only learned=true
suspicious event: confidence=0.00 trust=0.20 decision=observe_only
```

Notice: **confidence climbs from 0 to 1** as the fingerprint matures
(20 observations is `anomaly.DefaultConfig().MinObservations`), and
**trust dips mid-ramp (0.73 at event 10) before recovering to 0.97** —
that dip is the categorical-novelty signal still partially
contributing while the fingerprint is only half-mature, exactly the
behavior [`internal/anomaly`](../internal/anomaly/anomaly.go) is
designed to produce. The suspicious event's `confidence=0.00` shows
it's being scored as a completely unrelated, unfamiliar fingerprint —
none of the 25 warm-up observations transferred to it, because they
were for a different `(Actor, Operation, Target)` shape entirely.

Every `decision` above is `observe_only` because this program used
`NewEngine()` with no `Policy` configured — see the next section for
why, and [Policy Guide](policy-guide.md) for how to get `ALLOW`/`BLOCK`
differentiation like the CLI examples in [Use Cases](use-cases.md).

## Options

```go
trustvian.NewEngine(
	trustvian.WithStore(store),              // config.CompileStorage
	trustvian.WithPolicy(policy),            // config.CompilePolicy
	trustvian.WithAnomalyConfig(anomalyCfg), // config.CompileAnomaly
	trustvian.WithTrustConfig(trustCfg),     // config.CompileTrust
	trustvian.WithContextRisk(contextRisk),  // a func(trustvian.StableFeatures) float64
	trustvian.WithLearningScope("reference"),// partitions learned state
)
```

### `WithLearningScope`

By default every Engine shares one learned history per actor and
environment. `WithLearningScope` partitions it:

```go
a := trustvian.NewEngine(trustvian.WithStore(store), trustvian.WithLearningScope("candidate-a"))
b := trustvian.NewEngine(trustvian.WithStore(store), trustvian.WithLearningScope("candidate-b"))
```

Over one store, `a` and `b` never share a `Baseline` — even for the same
actor in the same environment. Each accumulates its own history and each
starts cold. Two engines given the *same* scope share one profile; the scope
is the identity, not the Engine instance.

Use it wherever learned history must stay separate under one actor identity:
replaying a corpus without disturbing production learning, holding a
reference profile beside a live one, or evaluating two builds of the same
agent.

The default is `""`. Omitting the option changes nothing — that is the scope
every Engine used before this option existed, and where every baseline
persisted before it already lives.

Three properties are worth knowing:

- **A scope is opaque.** Trustvian never parses it. `"candidate/9f3c1a"` is
  fine, and means nothing to the engine.
- **It is not behavioral identity.** The same event under two scopes
  produces the same `Fingerprint.ID` and the same `StableFeatures`. Only the
  learned evidence differs.
- **It cannot come from an event.** Scope is Engine configuration and is
  never derived from `SessionID`, `TraceID`, or `Attributes`, so an event
  producer cannot choose which profile it trains. See
  [SECURITY.md § learning-scope selection](SECURITY.md#learning-scope-selection).

Each scoped baseline carries its own independent fingerprint capacity:
filling one scope does not consume another's.

### Configuring from outside the module

Every option is configurable from a third-party module. Four take a
value produced by the public `config` package; the fifth takes a
callback over `trustvian.StableFeatures`, a public type.

```go
policy, err := config.CompilePolicy(config.PolicyConfig{
	Version:         config.SchemaVersionV1,
	DefaultDecision: "observe_only",
	DefaultReason:   "no rule matched",
})

store, err := config.CompileStorage(config.StorageConfig{
	Version: config.StorageSchemaVersionV1,
	Type:    config.StorageTypeFile,
	File:    &config.FileStorageConfig{Path: "baseline.json"},
})

anomalyCfg, err := config.CompileAnomaly(config.AnomalyConfig{
	Version: config.AnomalySchemaVersionV1,
})

high := 0.4 // escalate to High risk earlier than the default 0.5
trustCfg, err := config.CompileTrust(config.TrustConfig{
	Version:       config.TrustSchemaVersionV1,
	HighThreshold: &high,
})
```

A context-risk callback states that some kinds of operation are
inherently sensitive however familiar they become — the one behavioral
input Trustvian does not learn:

```go
func contextRisk(sf trustvian.StableFeatures) float64 {
	if sf.TargetName == "secrets-manager" {
		return 0.6
	}
	return 0
}
```

`StableFeatures` carries the dimensions that identify *what kind of
behavior* an event is — actor type, operation category and name, target
name and category, environment — and nothing per-occurrence. How
unusual one occurrence is already has a mechanism, and it is the
anomaly stage.

Notice what none of this does: it never names the types the options
accept. Each compiled value is received and passed straight on. Those
types live under `internal/` so the engine stays free to evolve them,
while the `config` package gives callers a complete configuration path.
The same arrangement lets you read every field of `Result` without
importing anything beyond the root package.

[`examples/configured-engine`](../examples/configured-engine/) is this
whole path as a runnable program, and — because `examples/` is a
separate module — it is also the proof that it works from outside.

### Getting a serializable record

`Result` is the engine's rich in-process output. When you need to persist a
decision, send it over an API, or stream it to a dashboard, project it:

```go
record := result.DecisionRecord()

raw, err := json.Marshal(record)
```

`DecisionRecord` is a public type with explicit JSON field names, so a
consumer can declare, store, and round-trip one without importing anything
beyond this package. It carries the evidence behind the decision — behavioral
shape, fingerprint, anomaly score and contributors, trust and risk, the policy
rule and reason, and correlation identifiers such as trace, session, and
delegation.

It deliberately carries **no raw event payload**. `Event.Attributes`, tool
arguments, prompts, and completions have no field on the record and cannot
appear in its JSON. If you need raw history, store it yourself as an explicit
choice — the record is security evidence, not an event archive.

### Persistence is selected, not implemented

Supplying a custom `Store` implementation is **not** a supported
extension point. `store.Store`'s methods reference internal types, so
an external module cannot implement it, and that is deliberate: it
keeps `Baseline` free to evolve. Choose a shipped backend through
`config.StorageConfig` — memory, file, or PostgreSQL.

### You own what you compile

A store compiled from configuration belongs to the caller. An `Engine`
never closes a store it was handed, because it did not open one. When
the backend holds resources — a PostgreSQL pool, an open file — close
it yourself:

```go
store, err := config.CompileStorage(cfg)
if err != nil {
	return err
}
if c, ok := store.(io.Closer); ok {
	defer c.Close()
}
```

There is no `Engine.Close`: an engine closing a resource it does not
own is how double-closes happen.

In practice, today, this is how the CLI itself configures a policy and
anomaly scoring (from
[`cmd/trustvian/policy.go`](../cmd/trustvian/policy.go) — real code in
this repository, not a hypothetical):

```go
func defaultPolicy() policy.Policy {
	return policy.Policy{
		Rules: []policy.Rule{
			{
				Name:   "block-high-risk",
				When:   policy.Condition{MinRiskLevel: trust.RiskHigh},
				Action: policy.DecisionBlock,
				Reason: "trust score indicates high or critical risk",
			},
			{
				Name:   "alert-medium-risk",
				When:   policy.Condition{MinRiskLevel: trust.RiskMedium},
				Action: policy.DecisionAlert,
				Reason: "trust score indicates elevated risk",
			},
		},
		DefaultAction: policy.DecisionAllow,
		DefaultReason: "risk within tolerance",
	}
}

func newEngine() *trustvian.Engine {
	return trustvian.NewEngine(trustvian.WithPolicy(defaultPolicy()))
}
```

This is exactly the `Decision` differentiation you see in every
[Use Cases](use-cases.md) example — those all go through the CLI's
`newEngine()`, i.e. this exact code.

See [Policy Guide](policy-guide.md) for the full `Rule`/`Condition`
reference and more examples.
