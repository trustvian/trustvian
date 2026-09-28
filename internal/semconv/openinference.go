package semconv

// OpenInference semantic conventions.
//
// Verified against Arize-ai/openinference at commit 300bba9191bf (2026-09-25),
// reading spec/semantic_conventions.md.
//
// Read because it is what the agent frameworks developers actually run emit
// today. Read as a *convention*, never as a framework integration: no framework
// is named here, in any identifier, branch or comment, and none ever may be.
const (
	// AttrOISpanKind is required on every OpenInference span by that spec, and
	// is the discriminator for this branch.
	AttrOISpanKind = "openinference.span.kind"

	// AttrOIToolName is the invoked tool — but only on a TOOL span.
	//
	// The same key appears under llm.tools.<index>.tool.name as an *advertised
	// tool definition* on an LLM span. Reading it without checking the kind
	// would let a model span that merely lists the tools available to it be
	// recorded as having used one, which is a fabricated behavior and the
	// subtlest trap in either convention. Hence the kind check in the table
	// below, and a test named for this specific mistake.
	AttrOIToolName = "tool.name"

	AttrOIModelName    = "llm.model_name"
	AttrOIProvider     = "llm.provider"
	AttrOISystem       = "llm.system"
	AttrOISessionID    = "session.id"
	AttrOIAgentName    = "agent.name"
	AttrOIRerankerName = "reranker.model_name"
)

// OpenInference span kinds this table maps.
//
// The spec defines ten. Six are mapped; the other four are not, for two
// different reasons recorded at their branch.
const (
	kindTool      = "TOOL"
	kindAgent     = "AGENT"
	kindLLM       = "LLM"
	kindEmbedding = "EMBEDDING"
	kindRetriever = "RETRIEVER"
	kindReranker  = "RERANKER"
)

// normalizeOpenInference reads the OpenInference convention, if present.
func normalizeOpenInference(s Span) (Normalized, bool) {
	kind := stringAttr(s.Attributes, AttrOISpanKind)
	if kind == "" {
		return Normalized{}, false
	}

	n := Normalized{
		SessionID: stringAttr(s.Attributes, AttrOISessionID),
		ActorType: openInferenceActorType(s.Attributes),
		Fidelity:  FidelityTransport,
	}

	provider := firstString(s.Attributes, AttrOIProvider, AttrOISystem)

	switch kind {
	case kindTool:
		// The kind check that makes reading tool.name safe — see AttrOIToolName.
		n.OperationCategory = categoryTool
		n.OperationName = stringAttr(s.Attributes, AttrOIToolName)

	case kindAgent:
		n.OperationCategory = categoryTool
		n.OperationName = stringAttr(s.Attributes, AttrOIAgentName)

	case kindLLM, kindEmbedding:
		n.OperationCategory = categoryExternal
		n.OperationName = stringAttr(s.Attributes, AttrOIModelName)
		n.TargetName = provider

	case kindRetriever:
		// The spec defines no identity attribute for a retriever — no
		// retriever.name, no data source id. So the kind itself is the only
		// semantic fact available, and it is a real one: "this actor retrieved
		// something" is more than "this actor made a POST". The target stays
		// whatever the transport mapping found.
		n.OperationCategory = categoryExternal
		n.OperationName = "retriever"

	case kindReranker:
		n.OperationCategory = categoryExternal
		n.OperationName = stringAttr(s.Attributes, AttrOIRerankerName)

	default:
		// CHAIN, GUARDRAIL, EVALUATOR, PROMPT and anything unknown.
		//
		// CHAIN is unmapped on purpose rather than for want of an attribute: its
		// own spec calls it "a starting point or a link between different LLM
		// application steps … the glue code". It names no operation the actor
		// performed, so mapping it would manufacture a behavior out of control
		// flow.
		//
		// GUARDRAIL, EVALUATOR and PROMPT are unmapped for the narrower reason
		// that the spec defines no identity attribute for any of them. If one
		// gains one, a row is added then — not a guess now.
		//
		// An unknown kind takes this branch too, and does nothing: no category,
		// no name, no fidelity upgrade, no error and no log line. It is the same
		// code path as a span carrying no kind at all, which is what "an unknown
		// span kind behaves as if the convention were absent" means.
		return Normalized{ActorType: n.ActorType, SessionID: n.SessionID}, true
	}

	if n.OperationName == "" {
		// Same rule as the GenAI branch: a category with no name would render as
		// "tool · POST". Identity is dropped, correlation is kept.
		return Normalized{ActorType: n.ActorType, SessionID: n.SessionID}, true
	}

	n.Fidelity = FidelitySemantic
	return n, true
}

// openInferenceActorType mirrors genAIActorType's conservatism.
//
// agent.name is the only agent-identity attribute this convention defines, and
// the span kind is not evidence: an AGENT span says what kind of operation was
// traced, not what kind of actor performed it. See genAIActorType for why a wrong
// upgrade here costs a learned baseline.
func openInferenceActorType(attrs map[string]any) string {
	if stringAttr(attrs, AttrOIAgentName) != "" {
		return actorTypeAIAgent
	}
	return ""
}
