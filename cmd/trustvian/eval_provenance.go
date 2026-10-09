package main

// Task 086: what `eval run` records about the scenario it runs, and how it
// prints what the control plane recorded and compared.
//
// The CLI computes the digests because it reads the files. That is provenance
// reporting, like the CLI version the result document carries, not evaluation:
// sameness is the control plane's answer, and this file only transcribes it.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/trustvian/trustvian/config"
)

type promptRefBody struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type sideDeclarationBody struct {
	Model     string         `json:"model,omitempty"`
	PromptRef *promptRefBody `json:"prompt_ref,omitempty"`
}

// executionProvenanceBody is the begin request's provenance.
type executionProvenanceBody struct {
	ScenarioDigest string              `json:"scenario_digest"`
	InputsDeclared bool                `json:"inputs_declared,omitempty"`
	InputDigest    string              `json:"input_digest,omitempty"`
	Reference      sideDeclarationBody `json:"reference"`
	Candidate      sideDeclarationBody `json:"candidate"`
}

// scenarioProvenance computes what an execution of the scenario at path
// records: both digests, and each side's declarations read from the
// environment its workload will run with. reused is true when the reference
// side is a recorded execution's, whose declarations that execution already
// holds, so none is sent for it. Every failure is a usage error, before
// anything runs.
func scenarioProvenance(scenario config.ScenarioConfig, path, collectorBin string, reused bool,
) (*executionProvenanceBody, error) {
	inputs, err := scenario.InputDigest(path)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}
	body := &executionProvenanceBody{
		ScenarioDigest: scenario.Digest(), InputsDeclared: inputs.Declared, InputDigest: inputs.Digest,
	}
	for _, side := range []struct {
		name string
		spec config.ScenarioSide
		into *sideDeclarationBody
	}{{"reference", scenario.Reference, &body.Reference}, {"candidate", scenario.Candidate, &body.Candidate}} {
		if reused && side.name == "reference" {
			continue
		}
		environment := repetitionConfig(scenario, side.spec, "", collectorBin, "", "", nil).inheritedEnvironment()
		declared, err := side.spec.Provenance(side.name, environment.Inherited)
		if err != nil {
			return nil, usageErrorf("%v", err)
		}
		side.into.Model = declared.Model
		if declared.PromptRef != nil {
			side.into.PromptRef = &promptRefBody{Name: declared.PromptRef.Name, Digest: declared.PromptRef.Digest}
		}
	}
	return body, nil
}

// inputsLine says what the inputs digest recorded, for the progress line.
func inputsLine(p *executionProvenanceBody) string {
	if !p.InputsDeclared {
		return "no inputs declared"
	}
	return "input_digest " + p.InputDigest
}

// sideProvenanceDTO is one side as the control plane recorded or compared it.
type sideProvenanceDTO struct {
	ScenarioDigest string         `json:"scenario_digest"`
	Inputs         string         `json:"inputs"`
	InputDigest    string         `json:"input_digest"`
	Model          string         `json:"model"`
	PromptRef      *promptRefBody `json:"prompt_ref"`
}

type executionProvenanceDTO struct {
	Reference sideProvenanceDTO `json:"reference"`
	Candidate sideProvenanceDTO `json:"candidate"`
}

// samenessDTO decodes the comparison's sameness and warnings for the human
// rendering; --json carries the server's body untouched.
type samenessDTO struct {
	SameScenario  string            `json:"same_scenario"`
	SameInputs    string            `json:"same_inputs"`
	SameModel     string            `json:"same_model"`
	SamePromptRef string            `json:"same_prompt_ref"`
	Reference     sideProvenanceDTO `json:"reference"`
	Candidate     sideProvenanceDTO `json:"candidate"`
}

type warningDTO struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// describeSide renders one side's recorded values on one line, naming what
// was not recorded rather than leaving a blank.
func describeSide(p sideProvenanceDTO) string {
	or := func(v, absent string) string {
		if v == "" {
			return absent
		}
		return v
	}
	inputs := or(p.Inputs, "not_recorded")
	if p.InputDigest != "" {
		inputs = p.InputDigest
	}
	prompt := "not_recorded"
	if p.PromptRef != nil {
		prompt = p.PromptRef.Name + "@" + p.PromptRef.Digest
	}
	return fmt.Sprintf("scenario %s   inputs %s   model %s   prompt_ref %s",
		or(p.ScenarioDigest, "not_recorded"), inputs, or(p.Model, "not_recorded"), prompt)
}

// renderRecordedProvenance prints what the execution recorded, from the
// control plane's begin response.
func renderRecordedProvenance(w io.Writer, raw json.RawMessage) error {
	if len(raw) == 0 {
		// A control plane that predates task 086 records nothing.
		return nil
	}
	var p executionProvenanceDTO
	if err := json.Unmarshal(raw, &p); err != nil {
		return operationalErrorf("decoding the recorded provenance: %v", err)
	}
	fmt.Fprintln(w, "Recorded")
	fmt.Fprintf(w, "  reference: %s\n", describeSide(p.Reference))
	fmt.Fprintf(w, "  candidate: %s\n", describeSide(p.Candidate))
	return nil
}

// renderSameness transcribes the comparison's sameness and warnings; absent
// from a control plane that predates task 086.
func renderSameness(w io.Writer, s *samenessDTO, warnings []warningDTO) {
	if s == nil {
		return
	}
	fmt.Fprintln(w, "\nSameness")
	for _, answer := range []struct{ name, state string }{
		{"same_scenario", s.SameScenario}, {"same_inputs", s.SameInputs},
		{"same_model", s.SameModel}, {"same_prompt_ref", s.SamePromptRef},
	} {
		fmt.Fprintf(w, "  %-16s %s\n", answer.name, answer.state)
	}
	fmt.Fprintf(w, "  reference: %s\n", describeSide(s.Reference))
	fmt.Fprintf(w, "  candidate: %s\n", describeSide(s.Candidate))
	for _, warning := range warnings {
		fmt.Fprintf(w, "  warning %s: %s\n", warning.Code, strings.TrimSpace(warning.Text))
	}
}
