# 0061 — The theme preference is the one value the browser stores

**Status:** Accepted. Amends [ADR 0036](0036-webui-is-a-same-origin-adapter-over-v1.md) § 10.

## Context

[ADR 0036](0036-webui-is-a-same-origin-adapter-over-v1.md) § 10 says the
browser stores no platform state: no `localStorage`, `sessionStorage`,
`IndexedDB` or cookies. A test enforced it as a blanket ban on the APIs.

[Task 097](../tasks/v0.11/097-theme-preference.md) adds Light, Dark and System
themes, and requires that an explicit choice survives a reload. That fact has
to live somewhere:

- **On the server** it would be the first per-viewer setting in a control plane
  that has no notion of a viewer. The local runtime is unauthenticated (task
  070), so "whose preference" has no answer, and every browser on the machine
  would share one.
- **In the URL** it would be lost on every navigation that does not carry it,
  and would leak a presentation choice into links people paste into issues.
- **In browser storage** it is exactly what the API is for: a per-browser,
  per-origin presentation preference.

What § 10 protects is *platform* state: identifiers, records, pages, cursors —
anything a reader could mistake for what the control plane holds. A colour
scheme is none of those.

## Decision

1. **One key, two values.** `trustvian.theme` holds `light` or `dark`.
   *System* is the absence of the key, so a first visit writes nothing and a
   reader who never chose keeps following the operating system.
2. **Two files.** `core/theme.js` reads and writes it; `core/theme-boot.js`, a
   classic script loaded in `<head>` before the stylesheets, only reads it, so a
   dark choice applies before first paint. No other asset may name a storage
   API, and the guard says so per file rather than being deleted.
3. **Every access is guarded.** Storage that is absent, blocked or throwing
   renders the system scheme; a failed write still applies the choice for the
   session, and the switcher says it will not survive a reload.
4. **Nothing else rides along.** No identifier, selection, page, cursor or
   timestamp is stored, now or by extension of this key. A future per-viewer
   convenience needs its own decision.
5. **No CSP change.** The pre-paint script is external, under
   `script-src 'self'`.

## Alternatives considered

**A module script with `prefers-color-scheme` only until it loads.** Modules
are deferred; a reader who chose Dark on a light system would see a light
frame on every load. Rejected.

**A cookie.** Sent with every `/v1` request for no reason, and visible to the
server, which has no use for it. Rejected.

**Keep the blanket ban and not remember the choice.** Fails the task's
acceptance criterion, and makes the switcher a per-page toggle that resets on
every reload. Rejected.

## Consequences

- § 10 of ADR 0036 now reads "the browser stores no platform state, and one
  presentation preference". Its rationale — the database is the only source of
  truth — is unchanged.
- `TestNoBrowserPersistenceOfPlatformState` and
  `TestLiveStoresNothingInTheBrowser` allow `localStorage` in exactly the two
  theme files; `TestThemeStorageHoldsOneKeyAndOnlyTheseValues` pins the key, the
  values and that the boot script never writes.
- The dark palette is declared twice — for `data-theme="dark"` and for the
  system preference — and a test keeps the two bodies identical.
