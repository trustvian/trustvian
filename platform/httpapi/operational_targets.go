package httpapi

// Task 087's per-target operational read.

import (
	"net/http"

	platform "trustvian-platform"
)

// ---------------------------------------------------------------------
// GET /v1/evaluations/operational
// ---------------------------------------------------------------------

type operationalTargetDTO struct {
	TargetName            string            `json:"target_name"`
	TargetCategory        string            `json:"target_category"`
	ReferenceObservations string            `json:"reference_observations"`
	CandidateObservations string            `json:"candidate_observations"`
	Latency               latencySectionDTO `json:"latency"`
	Errors                errorsSectionDTO  `json:"errors"`
	Tokens                tokensSectionDTO  `json:"tokens"`
}

// operationalTargetsResponse is one bounded page of per-target rows. Ordered by
// target (category, then name, byte order); next_after is present exactly when
// another row follows.
type operationalTargetsResponse struct {
	Version         string                 `json:"version"`
	ReferenceRunIDs []string               `json:"reference_run_ids"`
	CandidateRunIDs []string               `json:"candidate_run_ids"`
	Targets         []operationalTargetDTO `json:"targets"`
	NextAfter       string                 `json:"next_after,omitempty"`
}

// operationalByTarget serves GET /v1/evaluations/operational.
//
// reference_run_id and candidate_run_id each repeat once per run: one of each
// is a pairwise read, N of each is a repeated comparison's sides. The page
// contract is the collections' own — exclusive `after`, `limit` 1..64
// defaulting to 64, `next_after` only when another row follows.
func (h *Handler) operationalByTarget(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimitParam(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	query := r.URL.Query()
	request := platform.OperationalTargetRequest{}
	for _, id := range query["reference_run_id"] {
		request.ReferenceRunIDs = append(request.ReferenceRunIDs, platform.EvaluationRunID(id))
	}
	for _, id := range query["candidate_run_id"] {
		request.CandidateRunIDs = append(request.CandidateRunIDs, platform.EvaluationRunID(id))
	}
	page, err := h.controlPlane.OperationalByTarget(r.Context(), request, query.Get("after"), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	response := operationalTargetsResponse{
		Version:         WireVersion,
		ReferenceRunIDs: query["reference_run_id"],
		CandidateRunIDs: query["candidate_run_id"],
		Targets:         make([]operationalTargetDTO, 0, len(page.Rows)),
	}
	for _, row := range page.Rows {
		response.Targets = append(response.Targets, operationalTargetDTO{
			TargetName:            row.Target.Name,
			TargetCategory:        row.Target.Category,
			ReferenceObservations: u64(row.ReferenceObservations),
			CandidateObservations: u64(row.CandidateObservations),
			Latency:               newLatencySectionDTO(row.Sections.Latency),
			Errors:                newErrorsSectionDTO(row.Sections.Errors),
			Tokens:                newTokensSectionDTO(row.Sections.Tokens),
		})
	}
	if page.More {
		response.NextAfter = page.Rows[len(page.Rows)-1].Target.Cursor()
	}
	writeJSON(w, http.StatusOK, response)
}
