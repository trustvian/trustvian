package trustvian

import "github.com/trustvian/trustvian/event"

// StableFeatures are the dimensions of an Event that identify *what
// kind of behavior* it is, as opposed to how a particular occurrence of
// it performed. They are what a Fingerprint is derived from, and they
// are what a Baseline accumulates history against: two events with
// identical stable features are, to Trustvian, the same behavior
// happening twice.
//
// This is a read-only view handed to a WithContextRisk callback, not a
// value callers construct — the same relationship Result has to
// Engine.Analyze. Every field is a value type, so a callback cannot
// affect the analysis by modifying what it was given.
//
// JSON tags are present because this type is projected into
// DecisionRecord, which is a serializable public boundary; without them
// the record's own snake_case field names would sit beside Go field names
// in the same document.
//
// Deliberately narrow: it carries the stable dimensions and nothing
// else. Volatile, per-occurrence data — latency, timestamps, error
// state, session or delegation context — is not here, because a context
// risk penalty is a statement about a *kind* of operation ("writing to
// the secrets manager is inherently sensitive"), not about one instance
// of it. Scoring how unusual a particular occurrence is already has a
// mechanism, and it is the anomaly stage.
type StableFeatures struct {
	// ActorType is the kind of actor that performed the operation.
	ActorType event.ActorType `json:"actor_type"`

	// OperationCategory is the kind of operation performed.
	OperationCategory event.OperationCategory `json:"operation_category"`

	// OperationName is the specific operation, such as an HTTP route
	// or a tool name. Empty when the source did not supply one.
	OperationName string `json:"operation_name,omitempty"`

	// TargetName is what the operation acted on, such as a database or
	// an external host. Empty when the source did not supply one.
	TargetName string `json:"target_name,omitempty"`

	// TargetCategory is the kind of target acted on. Empty when the
	// source did not supply one.
	TargetCategory event.TargetCategory `json:"target_category,omitempty"`

	// Environment is the deployment environment the behavior occurred
	// in, such as "production". Empty when the source did not supply
	// one.
	Environment string `json:"environment,omitempty"`
}
