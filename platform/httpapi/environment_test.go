package httpapi_test

// The environment routes.
//
// Six routes, one of them the platform API's first collection. What the
// collection cases are really testing is the bound: a page is never larger
// than the limit whatever the project holds, and a caller can enumerate a
// project that migration left far above the creation cap.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // the same pure-Go driver the store uses
)

// environmentBody decodes one environment response.
type environmentBody struct {
	Version   string  `json:"version"`
	ProjectID string  `json:"project_id"`
	Ref       string  `json:"ref"`
	Name      string  `json:"name"`
	Rank      *uint16 `json:"rank"`
	Status    string  `json:"status"`
	Revision  uint64  `json:"revision"`
}

type environmentListBody struct {
	Version      string            `json:"version"`
	ProjectID    string            `json:"project_id"`
	Environments []environmentBody `json:"environments"`
	NextAfter    string            `json:"next_after"`
}

func decodeEnvironment(t *testing.T, raw []byte) environmentBody {
	t.Helper()
	var body environmentBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding environment response: %v (%s)", err, raw)
	}
	return body
}

func decodeEnvironmentList(t *testing.T, raw []byte) environmentListBody {
	t.Helper()
	var body environmentListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding environment list: %v (%s)", err, raw)
	}
	return body
}

func TestEnvironmentRouteRoundTrip(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy() // creates proj-1 and its "staging" environment

	// Read one back.
	r := a.do("GET", "/v1/projects/proj-1/environments/staging", nil)
	a.mustStatus(r, 200, "get environment")
	env := decodeEnvironment(t, r.Body.Bytes())
	if env.Version != "1" || env.Ref != "staging" || env.ProjectID != "proj-1" {
		t.Errorf("environment = %+v, want version 1, staging in proj-1", env)
	}
	if env.Status != "active" || env.Revision != 1 {
		t.Errorf("status/revision = %s/%d, want active/1", env.Status, env.Revision)
	}
	if env.Rank != nil {
		t.Errorf("rank = %d, want the field omitted for an unranked environment", *env.Rank)
	}

	// Create a ranked one: still revision 1, and the rank is present.
	r = a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "production", "name": "Production", "rank": 20})
	a.mustStatus(r, 201, "create ranked environment")
	created := decodeEnvironment(t, r.Body.Bytes())
	if created.Rank == nil || *created.Rank != 20 || created.Revision != 1 {
		t.Errorf("created = %+v, want rank 20 at revision 1", created)
	}

	// Rank 0 is a rank, and survives the wire as one.
	r = a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "zero", "name": "Zero", "rank": 0})
	a.mustStatus(r, 201, "create rank 0")
	if zero := decodeEnvironment(t, r.Body.Bytes()); zero.Rank == nil || *zero.Rank != 0 {
		t.Errorf("rank 0 came back as %v, want a present zero", zero.Rank)
	}

	// The same ref in another project is another environment.
	a.mustStatus(a.do("POST", "/v1/projects", map[string]string{
		"id": "proj-2", "name": "Other"}), 201, "create second project")
	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-2", "ref": "staging", "name": "Other staging"}), 201,
		"same ref in another project")

	// Duplicate identity.
	r = a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "staging", "name": "Again"})
	a.mustStatus(r, 409, "duplicate")
	if code := errorCode(t, r); code != "already_exists" {
		t.Errorf("duplicate code = %q, want already_exists", code)
	}

	// Missing project and missing environment.
	a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
		"project_id": "absent", "ref": "staging", "name": "X"}), 404, "missing project")
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/environments/absent", nil), 404, "missing env")
}

func TestEnvironmentConfigureRequiresARevision(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	// Missing revision is a 400, not an unconditional write.
	r := a.do("POST", "/v1/projects/proj-1/environments/staging/configure",
		map[string]any{"name": "Renamed"})
	a.mustStatus(r, 400, "configure without a revision")
	if code := errorCode(t, r); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}

	// A correct revision renames and advances exactly one.
	r = a.do("POST", "/v1/projects/proj-1/environments/staging/configure",
		map[string]any{"revision": 1, "name": "Staging EU", "rank": 20})
	a.mustStatus(r, 200, "configure")
	updated := decodeEnvironment(t, r.Body.Bytes())
	if updated.Name != "Staging EU" || updated.Revision != 2 {
		t.Errorf("updated = %+v, want Staging EU at revision 2", updated)
	}
	if updated.Rank == nil || *updated.Rank != 20 {
		t.Errorf("rank = %v, want 20", updated.Rank)
	}

	// Replaying the same revision is a conflict.
	r = a.do("POST", "/v1/projects/proj-1/environments/staging/configure",
		map[string]any{"revision": 1, "name": "Third"})
	a.mustStatus(r, 409, "stale revision")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("stale code = %q, want conflict", code)
	}

	// rank and clear_rank are opposite intentions.
	r = a.do("POST", "/v1/projects/proj-1/environments/staging/configure",
		map[string]any{"revision": 2, "rank": 30, "clear_rank": true})
	a.mustStatus(r, 400, "rank with clear_rank")

	// clear_rank alone removes it.
	r = a.do("POST", "/v1/projects/proj-1/environments/staging/configure",
		map[string]any{"revision": 2, "clear_rank": true})
	a.mustStatus(r, 200, "clear rank")
	if cleared := decodeEnvironment(t, r.Body.Bytes()); cleared.Rank != nil {
		t.Errorf("rank = %d after clear_rank, want it omitted", *cleared.Rank)
	}
}

func TestEnvironmentArchiveAndActivateRoutes(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	for _, missing := range []string{"archive", "activate"} {
		a.mustStatus(a.do("POST",
			"/v1/projects/proj-1/environments/staging/"+missing, map[string]any{}),
			400, missing+" without a revision")
	}

	r := a.do("POST", "/v1/projects/proj-1/environments/staging/archive",
		map[string]any{"revision": 1})
	a.mustStatus(r, 200, "archive")
	if archived := decodeEnvironment(t, r.Body.Bytes()); archived.Status != "archived" {
		t.Errorf("status = %q, want archived", archived.Status)
	}

	// An archived environment refuses a new run — the create-time rule, seen
	// from the transport.
	r = a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-archived", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile})
	a.mustStatus(r, 409, "run against an archived environment")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}

	r = a.do("POST", "/v1/projects/proj-1/environments/staging/activate",
		map[string]any{"revision": 2})
	a.mustStatus(r, 200, "activate")
	if active := decodeEnvironment(t, r.Body.Bytes()); active.Status != "active" {
		t.Errorf("status = %q, want active", active.Status)
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-after", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile}), 201,
		"run after reactivation")
}

// A run naming an environment that was never registered is refused, which is
// the typo case the registry exists for.
func TestRunAgainstAnUnregisteredEnvironmentIsRefused(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	r := a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-typo", "candidate_id": "cand-1",
		"environment": "stagin", "behavioral_profile": testProfile})
	a.mustStatus(r, 404, "run against an unregistered environment")

	a.mustStatus(a.do("GET", "/v1/evaluation-runs/run-typo", nil), 404, "the run was not written")
}

func TestEnvironmentValidationOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	tests := []struct {
		name string
		body map[string]any
	}{
		{"empty ref", map[string]any{"project_id": "proj-1", "ref": "", "name": "X"}},
		{"empty name", map[string]any{"project_id": "proj-1", "ref": "x", "name": ""}},
		{"ref with a control character", map[string]any{
			"project_id": "proj-1", "ref": "a\nb", "name": "X"}},
		{"ref with trailing space", map[string]any{
			"project_id": "proj-1", "ref": "trailing ", "name": "X"}},
		{"over-length ref", map[string]any{
			"project_id": "proj-1", "ref": strings.Repeat("r", 257), "name": "X"}},
		{"rank above the bound", map[string]any{
			"project_id": "proj-1", "ref": "big", "name": "X", "rank": 10000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := a.do("POST", "/v1/environments", tt.body)
			a.mustStatus(r, 400, tt.name)
			if code := errorCode(t, r); code != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", code)
			}
		})
	}

	// A path parameter faces the same rules.
	a.mustStatus(a.do("GET",
		"/v1/projects/proj-1/environments/"+strings.Repeat("r", 257), nil),
		400, "over-length ref in the path")
}

// ---------------------------------------------------------------------
// The collection
// ---------------------------------------------------------------------

func TestEnvironmentListPagination(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	// Fill the project to the creation cap: 63 more beside the fixture's own
	// "staging", which is every environment this API can create. A project
	// larger than one *page* only arises through migration, and that case is
	// enumerated in the platform package's migration tests, which can build a
	// schema-2 database; here the page size is lowered instead, which
	// exercises the same cursor over more pages.
	total := 63
	a.seedEnvironments("proj-1", total)

	seen := make([]string, 0, total+1)
	after := ""
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("pagination did not terminate")
		}
		path := "/v1/projects/proj-1/environments?limit=10"
		if after != "" {
			path += "&after=" + after
		}
		r := a.do("GET", path, nil)
		a.mustStatus(r, 200, "list page")
		body := decodeEnvironmentList(t, r.Body.Bytes())

		if len(body.Environments) > 10 {
			t.Fatalf("page carried %d rows, want at most the requested 10",
				len(body.Environments))
		}
		for _, env := range body.Environments {
			seen = append(seen, env.Ref)
		}
		if body.NextAfter == "" {
			break
		}
		if body.NextAfter != body.Environments[len(body.Environments)-1].Ref {
			t.Fatalf("next_after = %q, want the page's last ref", body.NextAfter)
		}
		after = body.NextAfter
	}

	if len(seen) != total+1 { // +1 for the fixture's own "staging"
		t.Fatalf("enumerated %d environments, want %d", len(seen), total+1)
	}
	unique := map[string]int{}
	for _, ref := range seen {
		unique[ref]++
	}
	for ref, count := range unique {
		if count != 1 {
			t.Errorf("%s appeared %d times in a traversal", ref, count)
		}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Fatalf("traversal is not ascending: %q then %q", seen[i-1], seen[i])
		}
	}
}

// TestEnvironmentListPageBoundIsExactlySixtyFour is the contract the store
// and the route share.
//
// The route once asked the store for limit+1 rows so it could tell a full
// last page from a truncated one, which forced the store's public range up to
// 65. It now asks a second bounded question instead, so all three cases below
// go through a store that accepts nothing above 64.
func TestEnvironmentListPageBoundIsExactlySixtyFour(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	// 63 beside the fixture's own "staging": the creation cap exactly.
	a.seedEnvironments("proj-1", 63)

	// A full page that is also the last page must not claim a continuation.
	r := a.do("GET", "/v1/projects/proj-1/environments?limit=64", nil)
	a.mustStatus(r, 200, "limit=64")
	body := decodeEnvironmentList(t, r.Body.Bytes())
	if len(body.Environments) != 64 {
		t.Fatalf("limit=64 returned %d rows, want all 64", len(body.Environments))
	}
	if body.NextAfter != "" {
		t.Errorf("next_after = %q on a complete collection; a full page is not "+
			"evidence of another one", body.NextAfter)
	}

	// A full page with a row after it must claim one, and the cursor is that
	// page's last ref.
	r = a.do("GET", "/v1/projects/proj-1/environments?limit=63", nil)
	a.mustStatus(r, 200, "limit=63")
	first := decodeEnvironmentList(t, r.Body.Bytes())
	if len(first.Environments) != 63 {
		t.Fatalf("limit=63 returned %d rows, want 63", len(first.Environments))
	}
	lastRef := first.Environments[len(first.Environments)-1].Ref
	if first.NextAfter != lastRef {
		t.Fatalf("next_after = %q, want the page's last ref %q", first.NextAfter, lastRef)
	}

	// And following it lands on the remaining row with no further cursor.
	r = a.do("GET", "/v1/projects/proj-1/environments?limit=63&after="+first.NextAfter, nil)
	a.mustStatus(r, 200, "second page")
	second := decodeEnvironmentList(t, r.Body.Bytes())
	if len(second.Environments) != 1 || second.NextAfter != "" {
		t.Errorf("second page = %d rows, next_after %q; want the last row and no cursor",
			len(second.Environments), second.NextAfter)
	}
}

// TestEnvironmentListOfAMigratedProject is the case the creation cap cannot
// reach and the page bound exists for.
//
// A project may hold more environments than may now be created, because
// schema 2 had no registry and the v2 → v3 migration preserves everything its
// runs referenced. Those rows are written here the way migration writes them
// — straight into the table, past the cap — because the API deliberately has
// no way to create them.
func TestEnvironmentListOfAMigratedProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	a := openAPI(t, path)
	a.seedHierarchy()

	// 130 beyond the fixture's own "staging": two full pages and change.
	insertMigratedEnvironments(t, path, "proj-1", 130)

	// A full page at the maximum still reports the continuation correctly,
	// which is the case that would silently break if the route went back to
	// borrowing a 65th row from the store.
	r := a.do("GET", "/v1/projects/proj-1/environments?limit=64", nil)
	a.mustStatus(r, 200, "first page of a migrated project")
	body := decodeEnvironmentList(t, r.Body.Bytes())
	if len(body.Environments) != 64 {
		t.Fatalf("page carried %d rows, want exactly 64", len(body.Environments))
	}
	if body.NextAfter != body.Environments[63].Ref {
		t.Fatalf("next_after = %q, want the page's last ref %q",
			body.NextAfter, body.Environments[63].Ref)
	}

	// And the whole collection enumerates, in ref byte order, once each.
	var seen []string
	after := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination did not terminate")
		}
		query := "/v1/projects/proj-1/environments?limit=64"
		if after != "" {
			query += "&after=" + after
		}
		r := a.do("GET", query, nil)
		a.mustStatus(r, 200, "page")
		got := decodeEnvironmentList(t, r.Body.Bytes())
		if len(got.Environments) > 64 {
			t.Fatalf("page carried %d rows, want at most 64", len(got.Environments))
		}
		for _, env := range got.Environments {
			seen = append(seen, env.Ref)
		}
		if got.NextAfter == "" {
			break
		}
		after = got.NextAfter
	}

	if len(seen) != 131 {
		t.Fatalf("enumerated %d environments, want 131", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Fatalf("traversal is not ref-ascending: %q then %q", seen[i-1], seen[i])
		}
	}
}

// insertMigratedEnvironments writes rows the way the v2 → v3 migration does:
// active, unranked, revision 1, named after the ref, and past the creation
// cap. It writes through the same schema the store just created rather than
// building one, so a column change fails this loudly instead of drifting.
func insertMigratedEnvironments(t *testing.T, path, projectID string, count int) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	for i := range count {
		ref := fmt.Sprintf("migrated-%03d", i)
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO platform_environments
			 (project_id, ref, name, rank, status, revision)
			 VALUES (?, ?, ?, NULL, 'active', 1)`, projectID, ref, ref); err != nil {
			t.Fatalf("insert migrated environment %s: %v", ref, err)
		}
	}
}

func TestEnvironmentListParameters(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	// The default limit needs no parameter, and an empty project is an empty
	// array rather than a 404.
	a.mustStatus(a.do("POST", "/v1/projects", map[string]string{
		"id": "proj-empty", "name": "Empty"}), 201, "create project")
	r := a.do("GET", "/v1/projects/proj-empty/environments", nil)
	a.mustStatus(r, 200, "list an empty project")
	body := decodeEnvironmentList(t, r.Body.Bytes())
	if len(body.Environments) != 0 || body.NextAfter != "" {
		t.Errorf("empty project list = %+v, want no rows and no cursor", body)
	}
	if body.ProjectID != "proj-empty" {
		t.Errorf("project_id = %q, want proj-empty", body.ProjectID)
	}

	// A project that does not exist is a 404.
	a.mustStatus(a.do("GET", "/v1/projects/absent/environments", nil), 404, "missing project")

	// Bad parameters are refused rather than clamped.
	for _, query := range []string{"?limit=65", "?limit=0", "?limit=-1", "?limit=abc",
		"?after=" + strings.Repeat("a", 257), "?after=with%20space%20"} {
		r := a.do("GET", "/v1/projects/proj-1/environments"+query, nil)
		a.mustStatus(r, 400, "list"+query)
		if code := errorCode(t, r); code != "invalid_request" {
			t.Errorf("%s code = %q, want invalid_request", query, code)
		}
	}

	// A cursor naming no row is a position, not an error.
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/environments?after=aaa", nil),
		200, "cursor naming no row")
}

// seedEnvironments creates count environments through the API itself, which
// is the only way rows are created — and which the creation cap bounds.
func (a *api) seedEnvironments(projectID string, count int) {
	a.t.Helper()
	for i := range count {
		ref := fmt.Sprintf("env-%03d", i)
		a.mustStatus(a.do("POST", "/v1/environments", map[string]any{
			"project_id": projectID, "ref": ref, "name": fmt.Sprintf("Env %d", i),
		}), 201, "seed "+ref)
	}
}

// The creation cap is enforced through the API too, and reported as a
// conflict rather than mistaken for a duplicate.
func TestEnvironmentCreationCapOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	a.seedEnvironments("proj-1", 63) // 63 + the fixture's "staging" = the cap

	r := a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "one-too-many", "name": "X"})
	a.mustStatus(r, 409, "create past the cap")
	if code := errorCode(t, r); code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}

	// Identity still precedes the cap: re-creating a ref the project already
	// has is a duplicate, not a limit.
	r = a.do("POST", "/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": "staging", "name": "X"})
	a.mustStatus(r, 409, "duplicate at the cap")
	if code := errorCode(t, r); code != "already_exists" {
		t.Errorf("duplicate at the cap code = %q, want already_exists", code)
	}
}

// TestNoPromotionRouteExists is the other half of the absence.
//
// Task 065 answers an ordering question inside the process; it moves nothing.
// A route that promoted a candidate would be task 066 arriving early, without
// the record, the evidence rule or the approval it owes.
func TestNoPromotionRouteExists(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()

	for _, path := range []string{
		"/v1/projects/proj-1/environments/staging/promote",
		"/v1/projects/proj-1/environments/staging/promotions",
		"/v1/promotions",
		"/v1/candidates/cand-1/promote",
	} {
		for _, method := range []string{"GET", "POST"} {
			r := a.do(method, path, map[string]string{})
			if r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want no such route; promotion is task 066's",
					method, path, r.Code)
			}
		}
	}
}
