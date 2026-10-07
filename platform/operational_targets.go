package platform

// Per-target operational evidence (task 087).
//
// The scorecard's three sections are run-level and fixed shape. "Which target
// got slower, which started returning 429s" needs one row per target, which is
// not fixed shape, so it is a separate, paged read rather than part of the
// scorecard. A target is the pair (target name, target category) the behaviors
// reaching it share; a row is the sum of those behaviors' per-behavior
// summaries, computed here on read and never stored or computed by an adapter.
//
// Either side may name one run or several. Several is a repeated comparison
// (078): each side is summed over its runs, and every section of every row
// says on how many runs that target carried the section's evidence, so a
// target seen in 3 of 10 runs is never read as if it were seen in all of them.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trustvian/trustvian/event"
)

// ErrInvalidOperationalRequest reports a per-target read that could never be
// answered as asked: a side with no runs or too many, unequal sides, or a run
// named twice.
var ErrInvalidOperationalRequest = errors.New("platform: invalid operational read")

// OperationalTarget identifies a target by the two behavior dimensions that
// name it.
type OperationalTarget struct {
	Name     string
	Category string
}

// Cursor is the page key: the category, a slash, then the name. The category
// vocabulary contains no slash, so the first slash is always the separator,
// whatever the name holds.
func (t OperationalTarget) Cursor() string { return t.Category + "/" + t.Name }

func (t OperationalTarget) compare(o OperationalTarget) int {
	return cmp.Or(cmp.Compare(t.Category, o.Category), cmp.Compare(t.Name, o.Name))
}

// ParseOperationalCursor reads a cursor Cursor produced. The empty string is
// the first page.
func ParseOperationalCursor(s string) (OperationalTarget, bool, error) {
	if s == "" {
		return OperationalTarget{}, false, nil
	}
	category, name, ok := strings.Cut(s, "/")
	if !ok || !validTargetCategory(event.TargetCategory(category)) {
		return OperationalTarget{}, false, fmt.Errorf("%w: %q is not a target cursor",
			ErrInvalidOperationalRequest, preview(s))
	}
	return OperationalTarget{Name: name, Category: category}, true, nil
}

// OperationalTargetRow is one target's sections.
type OperationalTargetRow struct {
	Target OperationalTarget

	// Observations each side made of this target, summed over its runs.
	ReferenceObservations uint64
	CandidateObservations uint64

	Sections OperationalComparison
}

// OperationalTargetRequest names the runs on each side.
type OperationalTargetRequest struct {
	ReferenceRunIDs []EvaluationRunID
	CandidateRunIDs []EvaluationRunID
}

// Validate refuses a request that could never be answered, before any run is
// loaded: 1..MaxRepetitions runs a side, equal sides, no run named twice on one
// side. The same run on both sides is a self-comparison, which compare allows.
func (r OperationalTargetRequest) Validate() error {
	n := len(r.ReferenceRunIDs)
	if n < 1 || n > MaxRepetitions {
		return fmt.Errorf("%w: %d reference runs; a side names 1..%d", ErrInvalidOperationalRequest, n, MaxRepetitions)
	}
	if len(r.CandidateRunIDs) != n {
		return fmt.Errorf("%w: %d reference and %d candidate runs; both sides name the same number",
			ErrInvalidOperationalRequest, n, len(r.CandidateRunIDs))
	}
	for _, side := range [][]EvaluationRunID{r.ReferenceRunIDs, r.CandidateRunIDs} {
		seen := make(map[EvaluationRunID]struct{}, n)
		for _, id := range side {
			if err := validateID("operational run id", string(id)); err != nil {
				return err
			}
			if _, dup := seen[id]; dup {
				return fmt.Errorf("%w: run %s is named twice on one side; its evidence would count twice",
					ErrInvalidOperationalRequest, preview(string(id)))
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

// OperationalTargetPage is one bounded page of rows, ordered by target.
type OperationalTargetPage struct {
	Rows []OperationalTargetRow

	// More is true when another row follows the last one returned.
	More bool
}

// OperationalByTarget returns one page of per-target rows for the runs named.
//
// Every run must be completed, in one environment and one project, with a
// complete behavior snapshot — the same evidence a comparison requires, refused
// the same way. after is exclusive; limit is 1..MaxListPage.
func (c *ControlPlane) OperationalByTarget(
	ctx context.Context, request OperationalTargetRequest, after string, limit int,
) (OperationalTargetPage, error) {
	if err := request.Validate(); err != nil {
		return OperationalTargetPage{}, err
	}
	if limit < 1 || limit > MaxListPage {
		return OperationalTargetPage{}, fmt.Errorf("%w: limit %d is outside 1..%d",
			ErrInvalidOperationalRequest, limit, MaxListPage)
	}
	cursor, hasCursor, err := ParseOperationalCursor(after)
	if err != nil {
		return OperationalTargetPage{}, err
	}

	type side struct {
		name ComparisonSide
		ids  []EvaluationRunID
	}
	type accumulated struct {
		reference, candidate  operationalSide
		referenceObs, candObs uint64
	}
	rows := map[OperationalTarget]*accumulated{}
	var first EvaluationRun
	for _, s := range []side{{SideReference, request.ReferenceRunIDs}, {SideCandidate, request.CandidateRunIDs}} {
		for _, id := range s.ids {
			run, err := c.completedRun(ctx, string(s.name), id)
			if err != nil {
				return OperationalTargetPage{}, err
			}
			if first.ID() == "" {
				first = run
			} else {
				if run.Environment() != first.Environment() {
					return OperationalTargetPage{}, fmt.Errorf(
						"%w: run %s is in environment %s, run %s in %s",
						ErrBehaviorEnvironmentMismatch, preview(string(first.ID())),
						preview(string(first.Environment())), preview(string(id)),
						preview(string(run.Environment())))
				}
				if err := c.requireSameProject(ctx, first, run); err != nil {
					return OperationalTargetPage{}, err
				}
			}
			_, snapshot, err := c.comparisonEvidence(ctx, run)
			if err != nil {
				return OperationalTargetPage{}, err
			}
			if !snapshot.Complete() {
				return OperationalTargetPage{}, fmt.Errorf(
					"%w: run %s saturated its behavior snapshot", ErrIncompleteSnapshot, preview(string(id)))
			}
			// One run's entries, grouped by target, then folded into each
			// target's side as one run — which is what makes
			// runs_with_evidence count runs rather than behaviors.
			byTarget := map[OperationalTarget][]BehaviorEntry{}
			for _, e := range snapshot.Entries() {
				t := OperationalTarget{Name: e.Behavior.TargetName, Category: string(e.Behavior.TargetCategory)}
				byTarget[t] = append(byTarget[t], e)
			}
			for t, entries := range byTarget {
				row := rows[t]
				if row == nil {
					row = &accumulated{}
					rows[t] = row
				}
				target, observations := &row.candidate, &row.candObs
				if s.name == SideReference {
					target, observations = &row.reference, &row.referenceObs
				}
				if err := target.addRun(entries); err != nil {
					return OperationalTargetPage{}, err
				}
				for _, e := range entries {
					if err := addCount(observations, e.Observations, "target observations"); err != nil {
						return OperationalTargetPage{}, err
					}
				}
			}
		}
	}

	targets := make([]OperationalTarget, 0, len(rows))
	for t := range rows {
		if hasCursor && t.compare(cursor) <= 0 {
			continue
		}
		targets = append(targets, t)
	}
	slices.SortFunc(targets, OperationalTarget.compare)

	page := OperationalTargetPage{More: len(targets) > limit}
	for _, t := range targets[:min(limit, len(targets))] {
		row := rows[t]
		page.Rows = append(page.Rows, OperationalTargetRow{
			Target:                t,
			ReferenceObservations: row.referenceObs,
			CandidateObservations: row.candObs,
			Sections:              compareOperational(row.reference, row.candidate),
		})
	}
	return page, nil
}
