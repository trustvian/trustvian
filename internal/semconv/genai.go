package semconv

// OpenTelemetry GenAI semantic conventions.
//
// Verified against open-telemetry/semantic-conventions-genai at commit
// e57c543b4889 (2026-09-24), reading model/gen-ai/registry.yaml and
// docs/gen-ai/gen-ai-spans.md.
//
// **Every attribute in that convention is stability `development`.** There is no
// stable GenAI attribute as of that commit. Combined with the fact that these
// keys appeared in the Go semconv package around v1.39.0 and were gone by
// v1.42.0, that is the whole argument for the degradation rules below: this is
// not a contract, it is a moving target that happens to be worth reading.
const (
	// AttrGenAIOperationName is Required in the convention, and is the
	// discriminator for the whole GenAI branch.
	AttrGenAIOperationName = "gen_ai.operation.name"

	AttrGenAIToolName     = "gen_ai.tool.name"
	AttrGenAIAgentName    = "gen_ai.agent.name"
	AttrGenAIAgentID      = "gen_ai.agent.id"
	AttrGenAIRequestModel = "gen_ai.request.model"
	AttrGenAIDataSourceID = "gen_ai.data_source.id"

	// AttrGenAIProviderName is Required, and replaced gen_ai.system.
	AttrGenAIProviderName = "gen_ai.provider.name"

	// AttrGenAISystemLegacy is read only as an alias for the provider, and only
	// because producers lag the specification.
	//
	// gen_ai.system appears **nowhere** in the convention as of the commit
	// above — not in the registry, and not in a deprecated registry, because
	// that repository has no deprecated registry at all. It is read because an
	// SDK pinned to a 2025 convention still emits it, and version tolerance is
	// a requirement of task 075. It is not read because the specification lists
	// both, and this comment exists so nobody later "fixes" the table by
	// promoting it to a current attribute.
	//
	// Do not confuse it with gen_ai.system_instructions, which is a different
	// attribute carrying the system prompt. That one is content and is on the
	// deny-list; reading it here would record a prompt as a provider name.
	AttrGenAISystemLegacy = "gen_ai.system"

	AttrGenAIConversationID = "gen_ai.conversation.id"
)

// GenAI operation names this table maps.
//
// The convention's enum has eighteen members as of the verified commit. Six
// classes are mapped below; every other value — including a nineteenth added
// upstream tomorrow — falls through to "convention absent", which is the version
// tolerance the task requires.
const (
	opExecuteTool     = "execute_tool"
	opInvokeAgent     = "invoke_agent"
	opCreateAgent     = "create_agent"
	opInvokeWorkflow  = "invoke_workflow"
	opPlan            = "plan"
	opChat            = "chat"
	opTextCompletion  = "text_completion"
	opGenerateContent = "generate_content"
	opEmbeddings      = "embeddings"
	opRetrieval       = "retrieval"
)

// normalizeGenAI reads the GenAI convention, if the span carries it.
//
// The bool result distinguishes "this convention matched" from "it produced
// nothing useful". Both leave the caller with today's mapping, but only the first
// stops Normalize from trying OpenInference — a span that declares
// gen_ai.operation.name has declared which convention it speaks.
func normalizeGenAI(s Span) (Normalized, bool) {
	operation := stringAttr(s.Attributes, AttrGenAIOperationName)
	if operation == "" {
		return Normalized{}, false
	}

	n := Normalized{
		SessionID: stringAttr(s.Attributes, AttrGenAIConversationID),
		ActorType: genAIActorType(s.Attributes),
		Fidelity:  FidelityTransport,
	}

	provider := firstString(s.Attributes, AttrGenAIProviderName, AttrGenAISystemLegacy)

	switch operation {
	case opExecuteTool:
		n.OperationCategory = categoryTool
		n.OperationName = stringAttr(s.Attributes, AttrGenAIToolName)
		n.Layer = LayerTool

	case opInvokeAgent, opCreateAgent:
		n.OperationCategory = categoryTool
		n.OperationName = stringAttr(s.Attributes, AttrGenAIAgentName)
		n.Layer = LayerTool

	case opInvokeWorkflow, opPlan:
		// A workflow or a planning step is the agent acting, so it is the same
		// category as a tool call. The agent name is the only identity the
		// convention offers for it; falling back to the operation name is
		// legitimate here and nowhere else, because `invoke_workflow` and
		// `plan` *are* the semantic names — unlike `execute_tool`, which names
		// a class of action and needs the tool to say what happened.
		n.OperationCategory = categoryTool
		n.OperationName = stringAttr(s.Attributes, AttrGenAIAgentName)
		n.Layer = LayerTool
		if n.OperationName == "" {
			n.OperationName = operation
		}

	case opChat, opTextCompletion, opGenerateContent, opEmbeddings:
		// External, not tool: a model call leaves the process to a third party,
		// which is what `external` already means. Using `tool` would make "a
		// tool was used" and "a model was consulted" indistinguishable, and
		// telling them apart is the point of the task.
		n.OperationCategory = categoryExternal
		n.OperationName = stringAttr(s.Attributes, AttrGenAIRequestModel)
		n.TargetName = provider
		n.Layer = LayerModel

	case opRetrieval:
		n.OperationCategory = categoryExternal
		n.Layer = LayerRetrieval
		source := stringAttr(s.Attributes, AttrGenAIDataSourceID)
		n.OperationName = source
		if n.OperationName == "" {
			// Same reasoning as the workflow branch: `retrieval` is itself the
			// semantic name, and a retrieval with no named source is still
			// meaningfully "this actor retrieved something".
			n.OperationName = operation
		}
		n.TargetName = source

	default:
		// A mapped-nothing match. The convention is present and spoke, but not
		// about anything this table reads — so the span keeps today's mapping,
		// and OpenInference is not consulted for it.
		return Normalized{ActorType: n.ActorType, SessionID: n.SessionID}, true
	}

	if n.OperationName == "" {
		// The category matched but the identity attribute was missing or not a
		// string. Emitting the category alone would render as "tool · POST" —
		// a semantic category wearing a transport name, which is exactly the
		// fabrication task 075's acceptance criterion 4 forbids. So the row does
		// not fire: correlation and actor evidence are kept, identity is not.
		return Normalized{ActorType: n.ActorType, SessionID: n.SessionID}, true
	}

	n.Fidelity = FidelitySemantic
	return n, true
}

// genAIActorType decides whether the producer established an agent identity.
//
// Only an explicit agent-identity attribute counts. The presence of
// gen_ai.operation.name does **not**: a plain backend service calling an LLM
// through an instrumented client library emits `gen_ai.operation.name=chat` and
// is not an AI agent.
//
// The cost of getting this wrong is not cosmetic. ActorType is one of the six
// StableFeatures dimensions, so flipping it changes every fingerprint for that
// actor and discards its learned baseline. A rule that fired on a client
// library's presence would silently reset baselines the first time a service
// adopted one.
//
// A producer that knows it is an agent and emits no agent-name attribute can
// still say so with trustvian.actor.type, which the adapters apply after this and
// which always wins.
func genAIActorType(attrs map[string]any) string {
	if firstString(attrs, AttrGenAIAgentName, AttrGenAIAgentID) != "" {
		return actorTypeAIAgent
	}
	return ""
}
