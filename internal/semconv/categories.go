package semconv

// The core vocabulary values this table produces, as plain strings.
//
// Literals rather than event.OperationCategory constants, and not because of an
// import cycle — there is none, event imports nothing from here. It is so this
// package's signature carries no opinion about which core types exist: it emits
// strings, the two adapters cast them, and a core rename is caught by the
// adapters' own compile rather than silently accepted here.
//
// A test asserts each one equals its event counterpart, so the decoupling cannot
// drift into a disagreement.
const (
	categoryTool     = "tool"
	categoryExternal = "external"

	actorTypeAIAgent = "ai_agent"
)
