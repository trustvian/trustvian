package httpapi_test

// The four hierarchy collection routes task 074 adds.
//
// They share one contract with the environment and promotion collections, so
// most of what is asserted here is that the shape really is the same one and
// not a fourth dialect of paging: parent-scoped except the root, id byte
// order, exclusive `after`, limit 1..64 defaulting to 64, `next_after` present
// exactly when another row follows, 404 for a missing parent and 200 with an
// empty array for an empty one.
//
// The continuation proof is the one that needed a seam rather than a
// black-box request: a `limit+1` lookahead and a second bounded probe produce
// identical responses, and only one of them keeps the store's public range
// honest at 64. So a recording store sits between the handler and SQLite and
// the test asserts on the limits the handler actually asked for.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	httpapi "trustvian-platform/httpapi"

	platform "trustvian-platform"
)

// recordingStore wraps a real backend and records every collection limit.
//
// Embedded rather than reimplemented: every other method is the SQLite store's
// own, so the handler runs against real persistence and only the four
// collections are observed.
type recordingStore struct {
	*platform.SQLiteStore

	mu     sync.Mutex
	limits []int
	calls  []string
}

func (s *recordingStore) record(name string, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	s.limits = append(s.limits, limit)
}

func (s *recordingStore) observed() ([]string, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...), append([]int(nil), s.limits...)
}

func (s *recordingStore) Projects(
	ctx context.Context, after platform.ProjectID, limit int,
) ([]platform.Project, error) {
	s.record("Projects", limit)
	return s.SQLiteStore.Projects(ctx, after, limit)
}

func (s *recordingStore) ProjectAgents(
	ctx context.Context, projectID platform.ProjectID, after platform.AgentID, limit int,
) ([]platform.Agent, error) {
	s.record("ProjectAgents", limit)
	return s.SQLiteStore.ProjectAgents(ctx, projectID, after, limit)
}

func (s *recordingStore) AgentCandidates(
	ctx context.Context, agentID platform.AgentID, after platform.CandidateID, limit int,
) ([]platform.Candidate, error) {
	s.record("AgentCandidates", limit)
	return s.SQLiteStore.AgentCandidates(ctx, agentID, after, limit)
}

func (s *recordingStore) CandidateEvaluationRuns(
	ctx context.Context, candidateID platform.CandidateID, after platform.EvaluationRunID, limit int,
) ([]platform.EvaluationRun, error) {
	s.record("CandidateEvaluationRuns", limit)
	return s.SQLiteStore.CandidateEvaluationRuns(ctx, candidateID, after, limit)
}

// collectionAPI is an api whose store records collection limits.
type collectionAPI struct {
	*api
	store *recordingStore
}

func newCollectionAPI(t *testing.T) *collectionAPI {
	t.Helper()
	backend, err := platform.OpenSQLiteStore(t.Context(),
		filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { backend.Close() })

	store := &recordingStore{SQLiteStore: backend}
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	handler, err := httpapi.NewHandler(plane,
		httpapi.WithClock(func() time.Time { return testEpoch }))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return &collectionAPI{api: &api{t: t, server: handler}, store: store}
}

// collectionPage is the shape every list response shares.
type collectionPage struct {
	Version     string            `json:"version"`
	ProjectID   string            `json:"project_id"`
	AgentID     string            `json:"agent_id"`
	CandidateID string            `json:"candidate_id"`
	Projects    []json.RawMessage `json:"projects"`
	Agents      []json.RawMessage `json:"agents"`
	Candidates  []json.RawMessage `json:"candidates"`
	Runs        []json.RawMessage `json:"evaluation_runs"`
	NextAfter   string            `json:"next_after"`
}

func decodeCollection(t *testing.T, r *httptest.ResponseRecorder) collectionPage {
	t.Helper()
	var page collectionPage
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode collection: %v (%s)", err, r.Body.String())
	}
	if page.Version != httpapi.WireVersion {
		t.Errorf("version = %q, want %q", page.Version, httpapi.WireVersion)
	}
	return page
}

func idsIn(t *testing.T, rows []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var entity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(row, &entity); err != nil {
			t.Fatalf("decode collection element: %v", err)
		}
		out = append(out, entity.ID)
	}
	return out
}

// seedAgents fills one project with count agents.
func (a *collectionAPI) seedAgents(count int) {
	a.t.Helper()
	for i := range count {
		a.mustStatus(a.do("POST", "/v1/agents", map[string]string{
			"id": fmt.Sprintf("agent-%03d", i), "project_id": "proj-1", "name": "A",
		}), 201, "create agent")
	}
}

// TestCollectionRoutesShareOnePagingContract covers every route's happy path
// and its parameter handling in one table, because the point is that they do
// not differ.
func TestCollectionRoutesShareOnePagingContract(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")

	routes := []struct {
		name  string
		path  string
		rows  func(collectionPage) []json.RawMessage
		scope func(collectionPage) string
		want  string
	}{
		{"projects", "/v1/projects",
			func(p collectionPage) []json.RawMessage { return p.Projects },
			func(collectionPage) string { return "" }, ""},
		{"agents", "/v1/projects/proj-1/agents",
			func(p collectionPage) []json.RawMessage { return p.Agents },
			func(p collectionPage) string { return p.ProjectID }, "proj-1"},
		{"candidates", "/v1/agents/agent-1/candidates",
			func(p collectionPage) []json.RawMessage { return p.Candidates },
			func(p collectionPage) string { return p.AgentID }, "agent-1"},
		{"runs", "/v1/candidates/cand-1/evaluation-runs",
			func(p collectionPage) []json.RawMessage { return p.Runs },
			func(p collectionPage) string { return p.CandidateID }, "cand-1"},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			// Default limit, one row, no continuation.
			r := a.do("GET", route.path, nil)
			a.mustStatus(r, 200, "GET "+route.path)
			page := decodeCollection(t, r)
			if got := len(route.rows(page)); got != 1 {
				t.Errorf("rows = %d, want 1", got)
			}
			if page.NextAfter != "" {
				t.Errorf("next_after = %q on a complete collection", page.NextAfter)
			}
			if got := route.scope(page); got != route.want {
				t.Errorf("scope = %q, want %q", got, route.want)
			}

			// An explicit limit is honoured.
			a.mustStatus(a.do("GET", route.path+"?limit=1", nil), 200, "limit=1")

			// Out of range is refused, never clamped: a caller asking for 200
			// has a belief about the response, and quietly returning 64 lets
			// that belief survive.
			for _, limit := range []string{"0", "-1", "65", "1000", "abc", ""} {
				query := route.path + "?limit=" + limit
				if limit == "" {
					// An empty limit is "absent", which means the default.
					a.mustStatus(a.do("GET", query, nil), 200, "empty limit")
					continue
				}
				rr := a.do("GET", query, nil)
				a.mustStatus(rr, 400, "limit="+limit)
				if code := errorCode(t, rr); code != "invalid_request" {
					t.Errorf("limit=%s code = %q, want invalid_request", limit, code)
				}
			}
		})
	}
}

// TestCollectionRootCarriesNoScopeField: /v1/projects has no parent, so it
// publishes no scope key rather than an empty one.
func TestCollectionRootCarriesNoScopeField(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()

	r := a.do("GET", "/v1/projects", nil)
	a.mustStatus(r, 200, "GET /v1/projects")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, absent := range []string{"project_id", "agent_id", "candidate_id"} {
		if _, present := raw[absent]; present {
			t.Errorf("the root collection publishes %q; it has no parent to echo", absent)
		}
	}
	if _, present := raw["projects"]; !present {
		t.Error("the root collection has no projects key")
	}
	// Nothing task 074 did not define.
	for _, absent := range []string{"total_count", "page_number", "offset", "sort"} {
		if _, present := raw[absent]; present {
			t.Errorf("the collection publishes %q, which this contract does not define", absent)
		}
	}
}

// TestCollectionElementsAreTheDetailDTO proves a listing publishes no field a
// by-id read does not.
//
// The privacy consequence is the reason: if a collection could carry its own
// element shape, a list would be a place for "just one more useful field" to
// appear without facing the review a detail route's fields faced.
func TestCollectionElementsAreTheDetailDTO(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")

	cases := []struct {
		list, detail, key string
	}{
		{"/v1/projects", "/v1/projects/proj-1", "projects"},
		{"/v1/projects/proj-1/agents", "/v1/agents/agent-1", "agents"},
		{"/v1/agents/agent-1/candidates", "/v1/candidates/cand-1", "candidates"},
		{"/v1/candidates/cand-1/evaluation-runs", "/v1/evaluation-runs/run-1", "evaluation_runs"},
	}
	for _, tt := range cases {
		t.Run(tt.key, func(t *testing.T) {
			listResp := a.do("GET", tt.list, nil)
			a.mustStatus(listResp, 200, "GET "+tt.list)
			var envelope map[string][]map[string]json.RawMessage
			if err := json.Unmarshal(listResp.Body.Bytes(), &envelope); err != nil {
				// The envelope also holds scalars; decode just the rows.
				var loose map[string]json.RawMessage
				if err := json.Unmarshal(listResp.Body.Bytes(), &loose); err != nil {
					t.Fatalf("decode list: %v", err)
				}
				var rows []map[string]json.RawMessage
				if err := json.Unmarshal(loose[tt.key], &rows); err != nil {
					t.Fatalf("decode rows: %v", err)
				}
				envelope = map[string][]map[string]json.RawMessage{tt.key: rows}
			}
			rows := envelope[tt.key]
			if len(rows) != 1 {
				t.Fatalf("list returned %d rows, want 1", len(rows))
			}

			detailResp := a.do("GET", tt.detail, nil)
			a.mustStatus(detailResp, 200, "GET "+tt.detail)
			var detail map[string]json.RawMessage
			if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
				t.Fatalf("decode detail: %v", err)
			}

			for field, value := range rows[0] {
				want, present := detail[field]
				if !present {
					t.Errorf("the collection element publishes %q, which the detail "+
						"route does not", field)
					continue
				}
				if string(value) != string(want) {
					t.Errorf("%s: list = %s, detail = %s", field, value, want)
				}
			}
			for field := range detail {
				if _, present := rows[0][field]; !present {
					t.Errorf("the collection element omits %q, which the detail route "+
						"publishes; a list element is the detail DTO", field)
				}
			}
		})
	}
}

// TestCollectionContinuationUsesASecondBoundedProbe is the regression this
// route family most needs.
//
// A `limit+1` lookahead and a bounded probe are indistinguishable from the
// response, so this asserts on the limits the handler asked the store for. The
// store's public range is 1..64; a transport that asks for 65 has widened a
// public contract so one caller can look ahead, which is exactly what task 066
// had to correct.
func TestCollectionContinuationUsesASecondBoundedProbe(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	// 65 agents: one more than a full page, so the first page fills and a
	// continuation genuinely exists. seedHierarchy created agent-1 already.
	a.seedAgents(64)

	r := a.do("GET", "/v1/projects/proj-1/agents", nil)
	a.mustStatus(r, 200, "list agents")
	page := decodeCollection(t, r)

	if len(page.Agents) != 64 {
		t.Fatalf("page returned %d agents, want 64", len(page.Agents))
	}
	if page.NextAfter == "" {
		t.Fatal("next_after is absent with another row available")
	}
	ids := idsIn(t, page.Agents)
	if page.NextAfter != ids[len(ids)-1] {
		t.Errorf("next_after = %q, want the page's last id %q", page.NextAfter, ids[len(ids)-1])
	}

	calls, limits := a.store.observed()
	if len(limits) != 2 {
		t.Fatalf("handler made %d store calls (%v), want 2: the page and one probe",
			len(limits), calls)
	}
	if limits[0] != 64 {
		t.Errorf("page limit = %d, want 64", limits[0])
	}
	if limits[1] != 1 {
		t.Errorf("probe limit = %d, want 1", limits[1])
	}
	for i, limit := range limits {
		if limit > platform.MaxListPage {
			t.Errorf("store call %d asked for limit %d, over the public bound of %d; "+
				"a transport lookahead must not widen the store's range",
				i, limit, platform.MaxListPage)
		}
	}
}

// TestCollectionFinalPageOmitsNextAfter covers the exactly-full-page case,
// where a short page cannot signal the end.
func TestCollectionFinalPageOmitsNextAfter(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	// seedHierarchy made agent-1; three more gives exactly four.
	a.seedAgents(3)

	r := a.do("GET", "/v1/projects/proj-1/agents?limit=4", nil)
	a.mustStatus(r, 200, "exactly a full page")
	page := decodeCollection(t, r)
	if len(page.Agents) != 4 {
		t.Fatalf("rows = %d, want 4", len(page.Agents))
	}
	if page.NextAfter != "" {
		t.Errorf("next_after = %q on a full page with nothing after it; a full page "+
			"is not evidence of another one", page.NextAfter)
	}

	// The probe still happened — that is how the handler knows.
	_, limits := a.store.observed()
	if len(limits) != 2 || limits[1] != 1 {
		t.Errorf("limits = %v, want a page read followed by a limit-1 probe", limits)
	}
}

// TestCollectionCursorIsExclusiveAndByteOrdered covers ordering and the cursor
// together, with identifiers whose byte order differs from a locale's.
func TestCollectionCursorIsExclusiveAndByteOrdered(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	for _, id := range []string{"Zulu", "alpha", "_under", "beta"} {
		a.mustStatus(a.do("POST", "/v1/agents", map[string]string{
			"id": id, "project_id": "proj-1", "name": "A",
		}), 201, "create "+id)
	}

	r := a.do("GET", "/v1/projects/proj-1/agents", nil)
	a.mustStatus(r, 200, "list")
	got := idsIn(t, decodeCollection(t, r).Agents)
	want := []string{"Zulu", "_under", "agent-1", "alpha", "beta"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v (byte order)", got, want)
	}

	r = a.do("GET", "/v1/projects/proj-1/agents?after=agent-1", nil)
	a.mustStatus(r, 200, "after")
	got = idsIn(t, decodeCollection(t, r).Agents)
	if fmt.Sprint(got) != fmt.Sprint([]string{"alpha", "beta"}) {
		t.Errorf("after agent-1 = %v, want [alpha beta]; the cursor is exclusive", got)
	}
}

// TestCollectionMissingParentIsNotFound keeps "there is nothing here" and
// "there is no here" distinguishable.
func TestCollectionMissingParentIsNotFound(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()

	for _, path := range []string{
		"/v1/projects/nope/agents",
		"/v1/agents/nope/candidates",
		"/v1/candidates/nope/evaluation-runs",
	} {
		r := a.do("GET", path, nil)
		a.mustStatus(r, 404, "GET "+path)
		if code := errorCode(t, r); code != "not_found" {
			t.Errorf("GET %s code = %q, want not_found", path, code)
		}
	}

	// An existing parent with no children is 200 and an empty array.
	a.mustStatus(a.do("POST", "/v1/agents", map[string]string{
		"id": "agent-empty", "project_id": "proj-1", "name": "A",
	}), 201, "create empty agent")
	r := a.do("GET", "/v1/agents/agent-empty/candidates", nil)
	a.mustStatus(r, 200, "empty collection")
	page := decodeCollection(t, r)
	if page.Candidates == nil {
		t.Error("an empty collection returned null rather than []")
	}
	if len(page.Candidates) != 0 {
		t.Errorf("rows = %d, want 0", len(page.Candidates))
	}
}

// TestCollectionMalformedCursorIsRejected: a cursor is an identifier and faces
// the identifier rules, because that is what it is.
//
// Percent-encoded, so the URL itself parses and the decoded value is what
// reaches validateID. The cases are the ones validateID actually refuses —
// control characters and surrounding whitespace. An interior space is *not*
// among them: it is a legal identifier in this domain, so a cursor carrying
// one is legal too, and asserting otherwise would be inventing a rule the
// cursor does not share with the ids it indexes.
func TestCollectionMalformedCursorIsRejected(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()

	for _, bad := range []string{"tab%09here", "new%0Aline", "%20leading", "trailing%20"} {
		r := a.do("GET", "/v1/projects/proj-1/agents?after="+bad, nil)
		if r.Code != http.StatusBadRequest {
			t.Errorf("cursor %q status = %d, want 400 (body %s)",
				bad, r.Code, r.Body.String())
		}
	}

	// And a legal-but-absent cursor is not an error: the cursor is a position,
	// and it need not name a row that exists.
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/agents?after=with%20space", nil),
		200, "an interior space is a legal identifier")
}

// TestCollectionTraversesAProjectLargerThanOnePage walks 130 agents the way a
// client does, and counts the requests it took.
func TestCollectionTraversesAProjectLargerThanOnePage(t *testing.T) {
	a := newCollectionAPI(t)
	a.seedHierarchy()
	a.seedAgents(129) // plus seedHierarchy's agent-1 makes 130

	seen := map[string]bool{}
	after := ""
	requests := 0
	for {
		requests++
		if requests > 10 {
			t.Fatal("traversal did not terminate")
		}
		path := "/v1/projects/proj-1/agents"
		if after != "" {
			path += "?after=" + after
		}
		r := a.do("GET", path, nil)
		a.mustStatus(r, 200, path)
		page := decodeCollection(t, r)
		if len(page.Agents) > platform.MaxListPage {
			t.Fatalf("page returned %d rows, over the bound", len(page.Agents))
		}
		for _, id := range idsIn(t, page.Agents) {
			if seen[id] {
				t.Errorf("%s appeared twice", id)
			}
			seen[id] = true
		}
		if page.NextAfter == "" {
			break
		}
		after = page.NextAfter
	}

	if len(seen) != 130 {
		t.Errorf("enumerated %d agents, want 130", len(seen))
	}
	if requests != 3 {
		t.Errorf("traversal took %d requests, want 3", requests)
	}
}
