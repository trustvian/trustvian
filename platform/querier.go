package platform

// The read surface the restore functions share between backends.
//
// Task 064 adds a second persistence backend, and the expensive part of doing
// that badly is duplication: the evaluation aggregate alone has fifty columns,
// and a second copy of that list — in a SELECT, in an INSERT, and in the scan
// targets between them — is where the two backends would silently drift apart.
// A differential test finds drift after it happens; sharing one definition
// means there is nothing to drift.
//
// So `loadRun`, `loadAggregate`, `loadSnapshot`, `loadBehaviorEntries`,
// `evidenceRowsPresent` and `ingestState` are written once against these
// interfaces, and each backend supplies a thin adapter. What differs between
// the drivers is exactly three things, and each has one method here: how a row
// is scanned, how "no rows" is spelled, and how a placeholder is written.
//
// This is deliberately not a `Database` abstraction. It carries no Exec, no
// transaction control, and no query building — writes stay in each backend's
// own file where the dialect differences are visible. See
// docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// rowScanner is one result row, from either driver.
type rowScanner interface {
	Scan(dest ...any) error
}

// rowIterator is a multi-row result.
//
// Close returns nothing, because the two drivers disagree and neither
// disagreement matters to a caller that already checks Err.
type rowIterator interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// rowQuerier is the single-row read surface.
//
// rebind lets the shared SQL be written with one placeholder style. The queries
// below use `?` because that is what they already used; PostgreSQL's adapter
// rewrites them. Keeping one style in the shared text means a column list is
// never copied to change its punctuation.
type rowQuerier interface {
	queryRow(ctx context.Context, query string, args ...any) rowScanner
	// noRows reports whether err is this driver's empty-result sentinel.
	noRows(err error) bool
	rebind(query string) string
}

// evidenceQuerier adds the multi-row read behaviour entries need.
type evidenceQuerier interface {
	rowQuerier
	query(ctx context.Context, query string, args ...any) (rowIterator, error)
}

// ---------------------------------------------------------------------
// database/sql adapter — SQLite
// ---------------------------------------------------------------------

// sqlQuerier adapts anything database/sql-shaped: *sql.DB or *sql.Tx.
type sqlQuerier struct {
	db interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	}
}

func (q sqlQuerier) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	return q.db.QueryRowContext(ctx, query, args...)
}

func (q sqlQuerier) noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// rebind is the identity: `?` is already SQLite's placeholder.
func (q sqlQuerier) rebind(query string) string { return query }

func (q sqlQuerier) query(ctx context.Context, query string, args ...any) (rowIterator, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlRows{rows}, nil
}

// sqlRows drops Close's error, which the caller already learns from Err.
type sqlRows struct{ *sql.Rows }

func (r sqlRows) Close() { _ = r.Rows.Close() }

// ---------------------------------------------------------------------
// Placeholder rewriting
// ---------------------------------------------------------------------

// rebindPositional rewrites `?` placeholders as `$1`, `$2`, … in order.
//
// Only outside single-quoted string literals, so a `?` inside a literal is left
// alone. No shared query contains one today; the scan is there so adding one
// later cannot quietly corrupt the statement.
//
// Deliberately not a regexp: a pattern cannot tell a `?` inside a literal from
// a placeholder, and being wrong in either direction produces SQL that is
// accepted and means something else.
func rebindPositional(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)

	next := 1
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			// '' inside a literal is an escaped quote, not a close.
			if inLiteral && i+1 < len(query) && query[i+1] == '\'' {
				out.WriteString("''")
				i++
				continue
			}
			inLiteral = !inLiteral
			out.WriteByte(c)
		case c == '?' && !inLiteral:
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(next))
			next++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
