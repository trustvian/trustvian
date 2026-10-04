# 097 — Theme Preference: Light, Dark and System

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [096](../v1.0/096-record-first-admin-console.md) (the token sheet
this extends)
Decision record: [ADR 0061](../../adr/0061-the-theme-preference-is-the-one-value-the-browser-stores.md)

## Problem

The console already has a dark palette, applied by
`@media (prefers-color-scheme: dark)` and nothing else. A developer whose
operating system is light and who wants a dark console at night — or the
reverse, reading a projected screen — has no way to choose. The palette is also
only half a theme: native form controls and scrollbars do not know which scheme
is active, because nothing declares `color-scheme`.

## Scope

- Three preferences: **Light**, **Dark** and **System**. System follows
  `prefers-color-scheme` and keeps following it when the OS changes.
- First use follows the operating system. An explicit choice is remembered for
  the next visit; choosing System forgets it.
- An accessible switcher, reachable from every destination.
- The chosen theme applies **before first paint**, so a dark preference never
  flashes a light page.
- One set of design tokens drives navigation, cards, forms, tables, charts,
  timelines, drawers and interaction states in both themes.

## Technical requirements

1. Dark values are declared once per scheme trigger: under
   `:root[data-theme="dark"]`, and under `prefers-color-scheme: dark` for
   `:root:not([data-theme="light"])`. A test asserts the two blocks are
   identical, so they cannot drift.
2. `color-scheme` is declared for each scheme so native controls follow.
3. The preference is the **only** value the bundle writes to browser storage:
   one key, two values, read and written by one module and the pre-paint
   script, each access wrapped so a blocked or throwing storage still renders.
   The persistence guard is narrowed to exactly that, not removed (ADR 0061).
4. The pre-paint script is an external classic script under `script-src 'self'`.
   No inline script, no `'unsafe-inline'`, no CSP change.
5. The switcher is a native radio group with a visible label and focus ring.

## Tests

- Preference resolution: stored value → applied theme for every combination of
  stored value (none, light, dark, garbage) and system scheme.
- Storage failure (throwing getter, throwing setter, absent storage) renders
  System without an error.
- The pre-paint script and the module agree on the key and the accepted values.
- The two dark token blocks are identical; `color-scheme` is declared.
- Only the two theme files reference browser storage; no other key is written.

## Acceptance criteria

1. Light, Dark and System are selectable from every destination with a pointer
   and with the keyboard alone, and the current choice is announced.
2. A first visit renders the operating system's scheme. An explicit choice
   survives a reload; System survives a reload by storing nothing.
3. A dark preference does not flash light on load.
4. Navigation, tables, forms, strips, charts, timelines, drawers, focus rings
   and hover/selected states are legible in both themes. Body text meets WCAG
   AA (4.5:1) and large text and UI boundaries 3:1, checked in both.
5. With storage blocked the page still renders and the switcher still works for
   the session.
