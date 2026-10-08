package httpapi

// Task 086's scenario provenance on /v1: what a client sends when it begins an
// execution, and how each side's recorded provenance is rendered.

import platform "trustvian-platform"

// Input states on a rendered side. Three, because "no inputs were declared"
// and "nothing was recorded" are different facts.
const (
	inputsNotRecorded  = "not_recorded"
	inputsNotDeclared  = "not_declared"
	inputsDeclaredText = "declared"
)

type promptRefDTO struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// sideDeclarationDTO is one side's declared model and prompt reference; each
// is omitted when not declared.
type sideDeclarationDTO struct {
	Model     string        `json:"model,omitempty"`
	PromptRef *promptRefDTO `json:"prompt_ref,omitempty"`
}

func (d sideDeclarationDTO) decode() platform.SideDeclaration {
	out := platform.SideDeclaration{Model: d.Model}
	if d.PromptRef != nil {
		out.PromptRef = platform.PromptRef{Name: d.PromptRef.Name, Digest: d.PromptRef.Digest}
	}
	return out
}

// executionProvenanceDTO is the begin request's provenance. Optional as a
// whole: a client that predates task 086 sends none, and nothing is recorded.
type executionProvenanceDTO struct {
	ScenarioDigest string             `json:"scenario_digest,omitempty"`
	InputsDeclared bool               `json:"inputs_declared,omitempty"`
	InputDigest    string             `json:"input_digest,omitempty"`
	Reference      sideDeclarationDTO `json:"reference"`
	Candidate      sideDeclarationDTO `json:"candidate"`
}

func (p *executionProvenanceDTO) decode() platform.ExecutionProvenance {
	if p == nil {
		return platform.ExecutionProvenance{}
	}
	return platform.ExecutionProvenance{
		ScenarioDigest: p.ScenarioDigest, InputsDeclared: p.InputsDeclared, InputDigest: p.InputDigest,
		Reference: p.Reference.decode(), Candidate: p.Candidate.decode(),
	}
}

// sideProvenanceDTO is one side as recorded. scenario_digest and input_digest
// are absent when not recorded or not declared, and inputs says which.
type sideProvenanceDTO struct {
	ScenarioDigest string        `json:"scenario_digest,omitempty"`
	Inputs         string        `json:"inputs"`
	InputDigest    string        `json:"input_digest,omitempty"`
	Model          string        `json:"model,omitempty"`
	PromptRef      *promptRefDTO `json:"prompt_ref,omitempty"`
}

func newSideProvenanceDTO(p platform.SideProvenance) sideProvenanceDTO {
	dto := sideProvenanceDTO{
		ScenarioDigest: p.ScenarioDigest, Inputs: inputsNotRecorded, InputDigest: p.InputDigest, Model: p.Model,
	}
	switch {
	case p.InputsDeclared:
		dto.Inputs = inputsDeclaredText
	case p.ScenarioRecorded():
		dto.Inputs = inputsNotDeclared
	}
	if p.PromptRef.Stated() {
		dto.PromptRef = &promptRefDTO{Name: p.PromptRef.Name, Digest: p.PromptRef.Digest}
	}
	return dto
}

// executionProvenanceResponseDTO is both sides of an execution as recorded.
type executionProvenanceResponseDTO struct {
	Reference sideProvenanceDTO `json:"reference"`
	Candidate sideProvenanceDTO `json:"candidate"`
}

func newExecutionProvenanceDTO(e platform.ScenarioExecution) executionProvenanceResponseDTO {
	return executionProvenanceResponseDTO{
		Reference: newSideProvenanceDTO(e.Provenance(platform.SideReference)),
		Candidate: newSideProvenanceDTO(e.Provenance(platform.SideCandidate)),
	}
}
