package main

// Rendering an artifact: what the caller independently knows about the run,
// and the artifact directory, in; Markdown out — the same Markdown for a pull
// request comment and a job summary. This file reads and validates the
// artifact; the document inside it is rendered by internal/cirender, the one
// renderer `trustvian eval report --format md` also calls.

import (
	"errors"
	"fmt"

	"github.com/trustvian/trustvian/internal/cirender"
)

// render reads the artifact directory and renders it. It never fails: what
// cannot be rendered as evidence is rendered as no verdict.
func render(dir string, want cirender.Frame, lim cirender.Limits) cirender.Rendering {
	if want.ExitCode == nil {
		return cirender.NoVerdict(want, "the run job reported no CLI exit code, so the CLI did not run")
	}
	rawMeta, err := readBounded(dir, metadataFile, maxMetadataBytes)
	if errors.Is(err, errMissing) {
		return cirender.NoVerdict(want, "the trustvian-run artifact is missing, so no result was preserved")
	}
	if err != nil {
		return cirender.Rejected(want, err)
	}
	meta, err := parseMetadata(rawMeta, want)
	if err != nil {
		return cirender.Rejected(want, fmt.Errorf("%s: %w", metadataFile, err))
	}

	code := meta.exitCode
	if code > 3 {
		return cirender.NoVerdict(want, fmt.Sprintf("the CLI exited %d, outside its documented codes 0 to 3; "+
			"no verdict is read from it", code))
	}
	switch meta.resultStatus {
	case statusOversized:
		return cirender.NoVerdict(want, "the CLI's output was larger than the run action preserves, "+
			"so no result document was kept")
	case statusInvalid:
		return cirender.NoVerdict(want, "the CLI's output was not one JSON object, so no result document was kept")
	case statusAbsent:
		if exists(dir, resultFile) {
			return cirender.Rejected(want, cirender.SchemaErr(resultFile, "is present although trustvian-run.json records none"))
		}
		switch code {
		case 2:
			return cirender.NoVerdict(want, "the CLI reported a usage error (exit 2): the scenario or its "+
				"configuration was refused before anything ran")
		case 3:
			return cirender.NoVerdict(want, "the CLI reported an operational error (exit 3): the control plane, "+
				"the network, the reference or a repetition failed")
		}
		// A PASS or FAIL always comes with its document.
		return cirender.Rejected(want, cirender.SchemaErr(metadataFile, "records no result for a CLI exit that always has one"))
	}

	rawResult, err := readBounded(dir, resultFile, maxResultBytes)
	if errors.Is(err, errMissing) {
		return cirender.Rejected(want, cirender.SchemaErr(resultFile, "is absent although trustvian-run.json records it"))
	}
	if err != nil {
		return cirender.Rejected(want, err)
	}
	if err := checkDigest(rawResult, meta); err != nil {
		return cirender.Rejected(want, err)
	}
	doc, err := cirender.Parse(rawResult)
	if err != nil {
		return cirender.Rejected(want, fmt.Errorf("%s: %w", resultFile, err))
	}

	switch meta.mode {
	case modeScenario:
		result, err := cirender.ParseScenario(doc)
		if err != nil {
			return cirender.Rejected(want, fmt.Errorf("%s: %w", resultFile, err))
		}
		// The CLI's exit code is the stored verdict mapped through its
		// contract, so the two must say the same thing.
		if !(code == 0 && result.Verdict() == cirender.VerdictPass) &&
			!(code == 1 && result.Verdict() == cirender.VerdictFail) {
			return cirender.Rejected(want, cirender.SchemaErr("comparison.gate.verdict", "disagrees with the CLI exit code"))
		}
		return cirender.RenderScenario(want, lim, result)
	case modeSuite:
		suite, err := cirender.ParseSuite(doc)
		if err != nil {
			return cirender.Rejected(want, fmt.Errorf("%s: %w", resultFile, err))
		}
		if suite.ExitCode() != code {
			return cirender.Rejected(want, cirender.SchemaErr("exit_code", "disagrees with the CLI exit code"))
		}
		if !suite.Complete() {
			return cirender.NoVerdict(want, "the suite result is incomplete: it was larger than the CLI's "+
				"output limit, so the CLI omitted every member outcome")
		}
		return cirender.RenderSuite(want, lim, suite)
	}
	// "both" and "none" are refused by the CLI before it writes anything.
	return cirender.Rejected(want, cirender.SchemaErr("mode", "names no result document, but one was preserved"))
}
