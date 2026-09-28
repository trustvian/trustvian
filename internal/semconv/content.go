package semconv

// The content attributes this package refuses to read.
//
// Not used by Normalize — it reads only the identity keys named in genai.go and
// openinference.go, so this list changes no behavior. It exists because "we do
// not read content" is a claim a reader should be able to *check*, and because a
// test feeds every key below through the whole pipeline and asserts its value
// reaches no enforcing layer. A deny-list nobody tests is a comment.
//
// The distinction being drawn: a tool *name* is what the agent did; a tool
// *argument* is what it said, and routinely carries customer data. The first is a
// behavioral dimension, the second is content. Every key here is the second.
//
// Each GenAI entry carries an explicit sensitivity warning in the convention's
// own registry — "Likely to contain sensitive information including user/PII
// data" for the message attributes — so this is upstream's assessment, not
// Trustvian's guess.
var (
	// GenAIContentAttributes are the Opt-In content attributes of the GenAI
	// convention, at the commit genai.go records.
	GenAIContentAttributes = []string{
		"gen_ai.input.messages",
		"gen_ai.output.messages",
		"gen_ai.tool.call.arguments",
		"gen_ai.tool.call.result",
		"gen_ai.tool.definitions",
		"gen_ai.system_instructions",
	}

	// OpenInferenceContentAttributes are the content attributes of the
	// OpenInference convention, at the commit openinference.go records.
	//
	// Three entries are worth justifying because they are not obviously payload:
	//
	//   user.id        an identifier, but a *human's*. Actor.ID is the service;
	//                  a person is not a behavioral dimension of it.
	//   metadata       a free-form JSON blob, so content by construction.
	//   *.mime_type    describes content and nothing else; carrying it would
	//                  leak the shape of what was sent for no behavioral gain.
	OpenInferenceContentAttributes = []string{
		"input.value",
		"output.value",
		"input.mime_type",
		"output.mime_type",
		"llm.input_messages",
		"llm.output_messages",
		"llm.prompt_template.template",
		"llm.prompt_template.variables",
		"retrieval.documents",
		"reranker.input_documents",
		"reranker.output_documents",
		"tool.parameters",
		"input.images",
		"output.images",
		"metadata",
		"user.id",
	}
)

// ContentAttributes is every content key from both conventions.
//
// Returned as a fresh slice so a caller cannot mutate the package's own lists.
func ContentAttributes() []string {
	all := make([]string, 0, len(GenAIContentAttributes)+len(OpenInferenceContentAttributes))
	all = append(all, GenAIContentAttributes...)
	all = append(all, OpenInferenceContentAttributes...)
	return all
}
