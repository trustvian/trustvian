# Policy Guide

`internal/policy` is the last pipeline stage: it turns a `Trust` result
and an event's stable features into a `Decision`. A `Policy` is a plain
Go value — an ordered list of rules plus a mandatory default — not
code, so it can eventually be built from a YAML/file loader without
touching the evaluator.

> `Policy`/`Rule`/`Condition` live under `internal/` and stay there —
> so this guide's code, written as a Go literal, is the way
> `cmd/trustvian` itself builds its policy today, usable directly only
> by code inside this repository. A caller outside this module should
> use the `config` package instead — see [§ Configuring a Policy from
> outside this module](#configuring-a-policy-from-outside-this-module)
> below — rather than needing this internal literal syntax at all. See
> also [Go SDK Guide § the public/internal boundary today](sdk-guide.md#configuring-from-outside-the-module).

## The types

```go
type Decision string
const (
	DecisionAllow           Decision = "allow"
	DecisionObserveOnly     Decision = "observe_only"
	DecisionAlert           Decision = "alert"
	DecisionChallenge       Decision = "challenge"
	DecisionRequireApproval Decision = "require_approval"
	DecisionBlock           Decision = "block"
)

type Condition struct {
	ActorType         event.ActorType         // "" = any
	OperationCategory event.OperationCategory // "" = any
	TargetName        string                  // "" = any
	Environment       string                  // "" = any
	MinRiskLevel      trust.RiskLevel         // "" = any; else Trust.Risk must be >= this
	Attributes        map[string]string       // nil/empty = any; else every key must match (see below)
	ApprovalStatus    event.ApprovalStatus    // "" = any; else Input.ApprovalStatus must equal this exactly
}

type Rule struct {
	Name   string
	When   Condition
	Unless *Condition // if it also matches, the rule is suppressed
	Action Decision
	Reason string
}

type Policy struct {
	Rules         []Rule
	DefaultAction Decision
	DefaultReason string
}
```

Every `Condition` field is optional — its zero value means "don't
care." A `Condition{}` matches every event. `MinRiskLevel` uses an
ordering (`RiskLow < RiskMedium < RiskHigh < RiskCritical`), so
`MinRiskLevel: trust.RiskMedium` matches `MEDIUM`, `HIGH`, and
`CRITICAL`. `Attributes`, if non-empty, requires every configured key
to be present in `Input.Attributes` with a value whose string form
(via `fmt.Sprint`, so `true`/`42`/etc. all compare as their natural
string) equals the configured value — see [Example: matching
`Event.Attributes`](#example-matching-eventattributes) below. There
are no comparison operators beyond string equality and no AND/OR/NOT
combinators between `Condition`s — `Rule.When`/`Unless` stay single
flat matchers by design.

## Evaluation: first match wins

```go
result := myPolicy.Evaluate(policy.Input{
	Stable:         features.Stable, // from a Result, or built directly
	Trust:          trust,
	Attributes:     event.Attributes,        // the raw Event.Attributes, for Condition.Attributes matching
	ApprovalStatus: event.Context.ApprovalStatus, // for Condition.ApprovalStatus matching (task 030)
})
// result.Decision, result.Explanation.{RuleName, Reason, MatchedDefault}
```

Rules are checked in order. The first `Rule` whose `When` matches
(and whose `Unless`, if set, does *not* match) wins — its `Action`
becomes `Decision`, its `Reason` becomes `Explanation.Reason`. If no
rule matches, `DefaultAction`/`DefaultReason` apply.

## Fail-closed, not fail-open

```go
var p policy.Policy // zero value: no rules, no default configured
result := p.Evaluate(in)
// result.Decision == policy.DecisionBlock
// result.Explanation.Reason == "no policy default configured; failing closed to BLOCK"
```

An unconfigured or misconfigured `Policy` — empty/invalid
`DefaultAction`, or `DefaultAction` set but `DefaultReason` left empty
— never falls through to `ALLOW` by accident. It fails closed to
`BLOCK`. There is no code path from "the config is wrong" to a silent
allow.

## Example: the CLI's starter policy

From [`cmd/trustvian/policy.go`](../cmd/trustvian/policy.go), real code
in this repository:

```go
policy.Policy{
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
```

This is what produces every `Decision` you see in
[Use Cases](use-cases.md) and the [CLI Guide](cli-guide.md) examples.

## Example: rule specificity (first match wins)

A more specific rule ahead of a catch-all:

```go
policy.Policy{
	Rules: []policy.Rule{
		{
			Name:   "block-secrets-access",
			When:   policy.Condition{TargetName: "secrets-manager"},
			Action: policy.DecisionBlock,
			Reason: "secrets access is always blocked, regardless of risk score",
		},
		{
			Name:   "block-high-risk",
			When:   policy.Condition{MinRiskLevel: trust.RiskHigh},
			Action: policy.DecisionBlock,
			Reason: "trust score indicates high or critical risk",
		},
	},
	DefaultAction: policy.DecisionAllow,
	DefaultReason: "risk within tolerance",
}
```

An event with `TargetName: "secrets-manager"` matches the first rule
and gets its specific reason, even if it would *also* have matched the
second, more general rule — order matters, and the first match wins.

## Example: `Unless` — an exception to a rule

```go
policy.Rule{
	Name:   "alert-external-calls",
	When:   policy.Condition{OperationCategory: event.OperationCategoryExternal},
	Unless: &policy.Condition{TargetName: "partner-api"}, // known, approved destination
	Action: policy.DecisionAlert,
	Reason: "external calls are unexpected for this actor",
}
```

An external call to `partner-api` is exempted (falls through to
whatever rule/default comes next); an external call to anything else
triggers the alert.

## Example: an operation that requires approval

[Task 030](archive/tasks/v0.7/030-approval-aware-policy-semantics.md) reuses this
exact `Unless` mechanism — no new Rule-level primitive — to express
"this operation requires approval":

```go
policy.Rule{
	Name:   "shell-execute-requires-approval",
	When:   policy.Condition{OperationCategory: event.OperationCategoryTool, TargetName: "shell.execute"},
	Unless: &policy.Condition{ApprovalStatus: event.ApprovalApproved},
	Action: policy.DecisionBlock,
	Reason: "shell.execute requires approval; approval evidence was not Approved",
}
```

The rule fires (blocking) for every `ApprovalStatus` value except
`Approved` — including `Denied`, `Required`, `Unspecified`, and,
deliberately, `NotRequired`: an event self-declaring
`ApprovalNotRequired` does not exempt it, because **Policy, not the
event, decides whether an operation requires approval.** Whether this
rule exists at all is the requirement; `ApprovalStatus` is only ever
evidence. See [ADR
0015](adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md)
for the full reasoning, and
[docs/SECURITY.md § AI Agent behavioral security](SECURITY.md) for why
`ApprovalStatus` must be treated as untrusted, self-reported input —
this `Unless` pattern only decides what Policy *does* with the
evidence; it does not, by itself, verify where that evidence came
from.

This `Condition` field is a plain equality match on
`Input.ApprovalStatus`, identical in kind to every other field above —
nothing here is specific to AI agents. The same rule gates a `service`
or `user` actor's identical operation exactly the same way, since
`When` never inspects `Actor.Type`.

## Example: matching `Event.Attributes`

The project spec's own AI-agent policy example — "block AI agents from
touching secrets-category tools" — needs to match on a specific
`Event.Attributes` key/value pair, not just the fields on
`features.StableFeatures`. `Condition.Attributes` closes exactly that
gap, real code from
[`internal/policy/policy_test.go`](../internal/policy/policy_test.go):

```go
policy.Policy{
	Rules: []policy.Rule{
		{
			Name: "block-ai-agent-secrets-access",
			When: policy.Condition{
				ActorType:  event.ActorTypeAIAgent,
				Attributes: map[string]string{"tool.category": "secrets"},
			},
			Action: policy.DecisionBlock,
			Reason: "AI agents may not access secrets-category tools",
		},
	},
	DefaultAction: policy.DecisionObserveOnly,
	DefaultReason: "no matching rule",
}
```

An `ai_agent`-typed actor whose event carries
`Attributes: map[string]any{"tool.category": "secrets"}` is blocked;
any other `tool.category` value, a missing key, or a non-`ai_agent`
actor falls through. `Condition.Attributes` only ever does string
equality per key (event attribute values are stringified with
`fmt.Sprint`) — there is deliberately no `>`/`<`/regex matching, and no
AND/OR/NOT composition between `Condition`s. That would be a real
policy language, which this project's roadmap explicitly defers (see
[`docs/archive/tasks/v0.1/006-policy.md`](archive/tasks/v0.1/006-policy.md#non-goals)).

## Testing a policy

`Policy.Evaluate` is a pure function — table-driven tests are the
natural fit. See
[`internal/policy/policy_test.go`](../internal/policy/policy_test.go)
for the full pattern this codebase uses (rule ordering, `Unless`
override, all three fail-closed scenarios, and a check that every
`Result` across several policies has a non-empty `Explanation.Reason`).

## Configuring a Policy from outside this module

Everything above is written the way code *inside* this module builds
a `Policy` — a Go struct literal naming `policy.Rule`/`Condition`/
`Decision` directly, which only works for code that can import
`internal/policy`. A caller outside this module (the CLI conceptually
could, but doesn't need to, since it's in-module; a standalone
deployment or the OTel Collector processor integration
([task 022](archive/tasks/v0.5/022-collector-config-integration.md)) genuinely
does) uses the public `config` package instead
([task 019](archive/tasks/v0.5/019-policy-config-model.md),
[ADR 0008](adr/0008-policy-config-boundary.md)):

```go
cfg := config.PolicyConfig{
	Version:         config.SchemaVersionV1,
	DefaultDecision: "observe_only",
	DefaultReason:   "no policy rules configured; observing by default",
	Rules: []config.PolicyRule{
		{
			Name: "block-agent-secrets",
			When: config.PolicyCondition{
				ActorType:  "ai_agent",
				Attributes: map[string]string{"tool.category": "secrets"},
			},
			Decision: "block",
			Reason:   "AI agent secret access is blocked by configured policy",
		},
	},
}

p, err := config.CompilePolicy(cfg)
if err != nil {
	log.Fatal(err)
}

engine := trustvian.NewEngine(trustvian.WithPolicy(p))
```

Note `p`'s static type is `policy.Policy` — the same internal type
this guide's earlier examples construct directly — but this code never
names that type or imports `internal/policy`: `p` is received from
`CompilePolicy` and passed straight into `trustvian.WithPolicy` via
Go's normal type inference. This is not a coincidence or a loophole;
it is the deliberate design [ADR 0008](adr/0008-policy-config-boundary.md)
records, verified empirically there.

`config.PolicyCondition`'s fields mirror `policy.Condition`'s exactly —
`ActorType`, `OperationCategory`, `TargetName`, `Environment`,
`MinRiskLevel`, `Attributes`, `ApprovalStatus` — as plain strings/maps, validated against
the real enum values by `CompilePolicy` (which calls `Validate()`
internally regardless of whether the caller already did). A typo like
`ActorType: "srevice"` fails compilation with an actionable error
identifying exactly which field was wrong, rather than silently
compiling into a condition that simply never matches — see
[SECURITY.md § Configuration-input
validation](SECURITY.md#configuration-input-validation) for why this
matters as a security property, not just an ergonomics one.

## Loading a Policy from a YAML file

[Task 020](archive/tasks/v0.5/020-policy-config-loader.md) added a loader that
decodes a YAML file directly into the same `PolicyConfig` shown above —
there is no separate, file-specific config type:

```yaml
# trustvian.yaml
version: v1
default_decision: observe_only
default_reason: no policy rules configured; observing by default
rules:
  - name: block-agent-secrets
    when:
      actor_type: ai_agent
      attributes:
        tool.category: secrets
    unless:
      approval_status: approved
    decision: block
    reason: AI agent secret access requires approval
```

`unless.approval_status: approved` is [task
030](archive/tasks/v0.7/030-approval-aware-policy-semantics.md)'s typed field —
before it existed, the only way to express this was the ad hoc
`attributes: {approval: human}` convention (still valid syntax, since
`Condition.Attributes` is unchanged, but with no defined meaning
Trustvian itself understands); `approval_status` gives the identical
intent a real, validated home instead.

```go
cfg, err := config.LoadFile("trustvian.yaml")
if err != nil {
	log.Fatal(err)
}
p, err := config.CompilePolicy(cfg)
if err != nil {
	log.Fatal(err)
}
engine := trustvian.NewEngine(trustvian.WithPolicy(p))
```

`config.Load([]byte)` is the byte-oriented equivalent, for a config
already held in memory (fetched from a secrets store, embedded via
`go:embed`, received over a network call) rather than read from a
local path — `LoadFile` is a thin wrapper around it plus a bounded
file read.

**Decoding is strict.** A field name typo anywhere in the document —
`default_decison` instead of `default_decision`, `min_risk_leve`
instead of `min_risk_level` — fails loudly with an error naming the
unrecognized field, rather than being silently ignored and falling
through to some other value. Duplicate keys in the same YAML mapping
(two `decision:` entries in one rule) are rejected too. Both are
verified by test against the real decoder, not assumed of the
library — see [SECURITY.md § Configuration-input
validation](SECURITY.md#configuration-input-validation) for why a
silently-ignored field is a security property here, not just an
ergonomics one.

`LoadFile` bounds its read to 1 MiB — far beyond what any real policy
file needs, small enough to bound a pathological input to a fixed,
cheap read. `path` is caller-controlled: `LoadFile` does not scan
directories or auto-discover a config file; you name the exact file.

The CLI's `trustvian analyze --config <path>`/`trustvian baseline
build --config <path>` (see [CLI Guide § --config](cli-guide.md#--config-path))
call this exact `LoadFile` — no parallel loader of their own. The OTel
Collector processor ([`processor/`](../processor/README.md#configuration))
also consumes this same schema (a `policy:` block matching
`PolicyConfig`'s fields exactly), but not through `LoadFile` — Collector
already parses its own YAML before the processor ever sees it, so the
processor decodes that already-parsed structure directly into a
`PolicyConfig` value (see [task
022](archive/tasks/v0.5/022-collector-config-integration.md) for why).
