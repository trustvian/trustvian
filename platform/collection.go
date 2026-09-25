package platform

// The bounded hierarchy collections task 074 adds, and the one page bound they
// share with task 065's and task 066's.
//
// Task 063 shipped no collection route on purpose: pagination, ordering,
// cursor semantics and scoping were undesigned, and a `/v1` route shape is a
// stable contract once published. Task 065 designed all four for one entity.
// What was missing was a consumer for the rest of the hierarchy, and task 074
// is it — a browser that reloads with no live traffic has no other honest way
// to find what already exists. Browser storage is not authority, a static
// same-origin client must not be given a database, realtime publishes
// replay_available: false, and polling is not a design.
//
// See docs/tasks/v1.0/074-zero-input-live-behavior-webui.md and
// docs/adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md.

import "fmt"

// MaxListPage bounds one page of any platform collection.
//
// One number for every collection in this API, not one per entity. Task 065
// chose 64 for environments and task 066 restated it for promotions with the
// comment "the same number, for the same reason"; a third and fourth copy
// would be where they finally disagree.
//
// MaxEnvironmentPage and MaxPromotionPage remain exported and remain 64. They
// are published compatibility surface, and deduplicating a constant is not a
// reason to remove a symbol a caller may name — they are defined in terms of
// this one instead, so there is exactly one value and three names for it.
//
// This is the whole range at the store edge, not a default a privileged caller
// may exceed. Task 066 corrected precisely that: a transport that wanted
// limit+1 for lookahead had widened the store's public contract to 65. A
// caller that needs to know whether another page follows asks a second bounded
// question instead.
const MaxListPage = 64

// validateListPage applies the page rules every collection shares.
//
// kind names the collection in the error, because "page limit 0 is outside
// 1..64" with no subject is a worse diagnostic than the caller deserves. An
// empty cursor starts at the beginning; a non-empty one is an identifier and
// faces the identifier rules, because that is what it is.
func validateListPage(kind, after string, limit int) error {
	if limit < 1 || limit > MaxListPage {
		return fmt.Errorf("%w: %s page limit %d is outside 1..%d",
			ErrInvalidID, kind, limit, MaxListPage)
	}
	if after != "" {
		if err := validateID(kind+" page cursor", after); err != nil {
			return err
		}
	}
	return nil
}
