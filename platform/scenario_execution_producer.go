package platform

// What a scenario execution's runner said about itself when it began (task
// 107, D14, schema 15): the CLI version that ran it, and — when it reused a
// recorded reference side — how it asked for that reference.
//
// Both are members result document version 1 already carried
// (producers.cli_version, reference.mode). Until schema 15 nothing persisted
// them, so a report read for the same execution said not_recorded where the
// runner's own document said the value. They are recorded facts, not
// evidence: nothing compares them, nothing gates on them, and neither enters
// a fingerprint, a baseline key or a comparison.

import (
	"fmt"
	"strings"
)

// ScenarioReferenceMode is how an execution asked for a reused reference
// side: by naming one execution, or by asking for the latest.
type ScenarioReferenceMode string

const (
	ScenarioReferenceModeExecution ScenarioReferenceMode = "execution"
	ScenarioReferenceModeLast      ScenarioReferenceMode = "last"
)

func (m ScenarioReferenceMode) valid() bool {
	return m == ScenarioReferenceModeExecution || m == ScenarioReferenceModeLast
}

// mode is the reference mode a begin request asked for, or "" when it runs its
// own reference side.
func (r ScenarioReference) mode() ScenarioReferenceMode {
	switch {
	case r.Last:
		return ScenarioReferenceModeLast
	case r.ExecutionID != "":
		return ScenarioReferenceModeExecution
	}
	return ""
}

// Schema 15's columns on the scenario execution table.
const (
	columnCLIVersion    = "cli_version"
	columnReferenceMode = "reference_mode"
)

// schemaVersionV14 is task 107-A's schema, the last version without the
// runner's own facts. v15 adds columns only, so v14 and v15 hold the same
// tables and are told apart by the stamp.
const schemaVersionV14 = 14

// scenarioProducerSchemaStatements is schema 15's whole change, in either
// dialect: two nullable columns, no backfill. NULL is "not recorded", which is
// what every execution begun before schema 15 holds. Not identifiers and never
// ordered, so plain TEXT on PostgreSQL too.
func scenarioProducerSchemaStatements() []string {
	return []string{
		`ALTER TABLE ` + tableScenarioExecutions + ` ADD COLUMN ` + columnCLIVersion + ` TEXT`,
		`ALTER TABLE ` + tableScenarioExecutions + ` ADD COLUMN ` + columnReferenceMode + ` TEXT`,
	}
}

// validateCLIVersion checks a recorded CLI version: within the bounds every
// caller-supplied string shares, and not blank. Empty means not recorded and
// is valid. The text is otherwise opaque — "v0.12.0 3e86f9c" and "(devel)"
// are both versions a build reports.
func validateCLIVersion(version string) error {
	if version == "" {
		return nil
	}
	if err := validateText(ErrInvalidRepeatedRequest, "scenario execution cli version", version); err != nil {
		return err
	}
	if strings.TrimSpace(version) != version || strings.TrimSpace(version) == "" {
		return fmt.Errorf("%w: scenario execution cli version is blank or padded", ErrInvalidRepeatedRequest)
	}
	return nil
}

// validateProducer holds the two facts to each other and to the execution:
// a reference mode exists exactly when a reference does, except that an
// execution begun before schema 15 has a reference and no mode.
func (e ScenarioExecution) validateProducer() error {
	if err := validateCLIVersion(e.cliVersion); err != nil {
		return err
	}
	if e.referenceMode == "" {
		return nil
	}
	if !e.referenceMode.valid() {
		return fmt.Errorf("%w: scenario execution reference mode %q", ErrInvalidRepeatedRequest, e.referenceMode)
	}
	if e.reference == "" {
		return fmt.Errorf("%w: scenario execution records reference mode %s without a reference",
			ErrInvalidRepeatedRequest, e.referenceMode)
	}
	return nil
}

// withProducer records the runner's facts on a new execution.
func (e ScenarioExecution) withProducer(cliVersion string, mode ScenarioReferenceMode) (ScenarioExecution, error) {
	next := e
	next.cliVersion, next.referenceMode = cliVersion, mode
	if err := next.validate(); err != nil {
		return ScenarioExecution{}, err
	}
	return next, nil
}

// CLIVersion is the version of the CLI that began the execution, or "" when
// it was not recorded: an older client sent none, or the execution began
// before schema 15.
func (e ScenarioExecution) CLIVersion() string { return e.cliVersion }

// ReferenceMode is how the execution asked for its reused reference side, or
// "" when it ran its own reference side or began before schema 15.
func (e ScenarioExecution) ReferenceMode() ScenarioReferenceMode { return e.referenceMode }

// producerColumnValues is the two column values, NULL for not recorded.
func (e ScenarioExecution) producerColumnValues() []any {
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return []any{null(e.cliVersion), null(string(e.referenceMode))}
}
