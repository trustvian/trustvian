package platform

// Tests for the shared read seam.

import "testing"

// TestRebindPositionalRewritesPlaceholdersInOrder pins the one piece of
// dialect translation the shared queries depend on.
//
// The shared restore functions are written with `?` because that is what they
// already used; PostgreSQL needs `$1`, `$2`, … in argument order. Getting the
// numbering wrong produces SQL the server accepts and that means something
// else, which is why this is tested rather than eyeballed.
func TestRebindPositionalRewritesPlaceholdersInOrder(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"none", "SELECT 1", "SELECT 1"},
		{"one", "WHERE id = ?", "WHERE id = $1"},
		{"several", "SET a = ?, b = ? WHERE id = ?", "SET a = $1, b = $2 WHERE id = $3"},
		{"ten crosses a digit", "?,?,?,?,?,?,?,?,?,?,?",
			"$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11"},

		// A `?` inside a string literal is data, not a placeholder. No shared
		// query contains one today; the rule is here so adding one later cannot
		// silently shift every number after it.
		{"literal is left alone", "WHERE a = '?' AND b = ?", "WHERE a = '?' AND b = $1"},
		{"escaped quote inside a literal", "WHERE a = 'it''s ?' AND b = ?",
			"WHERE a = 'it''s ?' AND b = $1"},
		{"literal after a placeholder", "WHERE a = ? AND b = '?'", "WHERE a = $1 AND b = '?'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rebindPositional(tt.query); got != tt.want {
				t.Errorf("rebindPositional(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestSQLiteQuerierRebindIsIdentity keeps the SQLite path free of translation.
func TestSQLiteQuerierRebindIsIdentity(t *testing.T) {
	const query = "SELECT a FROM t WHERE id = ? AND status = ?"
	if got := (sqlQuerier{}).rebind(query); got != query {
		t.Errorf("rebind() = %q, want the query unchanged", got)
	}
}
