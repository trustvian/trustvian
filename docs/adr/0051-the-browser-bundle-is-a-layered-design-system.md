# 0051 — The browser bundle is a layered design system, not a directory of scripts

**Status:** Accepted

## Context

[ADR 0050](0050-the-browser-surface-is-a-record-first-admin-console.md) made
the browser surface a console of tables. It changed the information
architecture and left the *bundle* as it was: fifteen modules in one flat
directory, one 1,400-line stylesheet, and a set of conventions held together
by the fact that one person had written all of them.

That shape had three specific costs, and each shows up as something a reader
can see:

**Values were named wherever they were used.** `0.86rem`, `#fbfbfa` and
`border-radius: 6px` appeared dozens of times. A colour's dark-mode
counterpart was a hundred lines away from it, so the two drifted, and nothing
could tell you whether a grey was *the* muted grey or a near miss.

**Loading and empty rendered identically.** Both were one line of italic text,
so a table that was still fetching and a table with nothing in it looked the
same. A reader could not tell "wait" from "there is nothing here", and the
page was silent between a click and its answer.

**Severity had to be read.** A `block`/`critical` observation was the same
weight as an `allow`/`low` one, in a monospace column of identical rows.
Finding the observation that matters — which is the entire job — meant
reading every line.

Underneath all three: there was no layer boundary to violate, so nothing
stopped a presentational helper from reaching a route, and no guard could say
it had.

## Decision

### Five layers, and dependencies only point down

```text
core/   → nothing              DOM construction and value formatting.
v1/     → core                 The control-plane contract: routes, allowlists, renderers.
ui/     → core                 The design system. Reaches no API.
live/   → live                 The realtime observatory.
views/  → core, v1, ui, views  One module per destination.
app.js  → anything             The composition root, and the only one.
styles/ → tokens, base, layout, components, views
```

`ui/` not being allowed to import `v1/` is the boundary that matters. The
moment a component can read a route, "presentation" stops being a layer and
the design system starts carrying domain knowledge — which is how a table
ends up knowing what a gate verdict is.

`app.js` may import anything because that is what a composition root is, and
there is exactly one of them.

### Only `tokens.css` names a raw value

Every colour, size, space, radius, duration and layout dimension is declared
once, with its dark-mode counterpart beside it, and read everywhere else as
`var(--…)`. A hex literal, `rgb(` or `hsl(` in any other sheet fails a test.

This is what makes the palette a palette rather than a hundred independent
choices, and it is why light and dark cannot drift apart: they are two values
in one declaration block, not two files edited at different times.

The accent is blue by elimination, not by taste. Green, amber and red carry
verdict and risk; violet carries "new". An accent sharing any of those hues
would make *selected* read as *severe*.

There is still no web font. `font-src 'none'` is only an honest policy
because nothing asks for one, so the type system's distinction is prose
versus record — labels in the UI sans, every value that is a record in mono
with tabular figures.

### Loading, empty, failed and populated are four different answers

A surface is always in exactly one, and each says something different. A
table that is fetching draws a skeleton in the shape of the rows that are
coming; an empty one states the absence and names the next action; a failed
one shows the server's refusal as the refusal it is.

The skeleton is **static**. The obvious one shimmers, and this console has a
standing rule that nothing loops: a page with something perpetually moving on
it teaches a reader to ignore movement, which is the one signal this product
has. `aria-busy` tells a screen reader the same thing without the animation.

### Severity is three carriers, and the word is the one that counts

A decision shows as a gutter down the row's leading edge, a tint behind the
row, and a mark beside the word in its own column. Remove the gutter, the
tint and the mark and the table is still correct — that is the test for
whether any of them was allowed to be added.

The word is always the one the server returned, spelled its way. Nothing here
decides what a decision means; the grouping into three severities is
presentational, and it is declared as a table that is iterated rather than as
a condition somebody can extend by accident.

### Icons are built, never parsed, and never a font

Inline SVG through `createElementNS`. A sprite string would be the one place
markup entered a bundle whose central safety property is that it parses none,
and an icon font is not available under the policy. They are decoration
beside a word and are marked `aria-hidden`, so nothing is read twice.

## Alternatives considered

**Adopt a CSS framework or a component library.** Would have supplied all of
this. It also needs a build step, a package manager and a lockfile, all three
of which this repository refuses by test and by ADR 0036. The design system
is roughly 700 lines of CSS and 400 of JavaScript; a toolchain to avoid
writing it is not a trade.

**Keep one stylesheet and add tokens at the top.** Half the benefit for none
of the cost, and it was the tempting option. It does not survive growth: the
reason the values drifted was not that they were in one file but that nothing
distinguished a token from a use, and `@layer` would have expressed that
without making anything findable.

**Split by feature rather than by layer** — `runs/`, `compare/`, `evidence/`
each holding their own markup, styles and script. The usual advice, and wrong
here: the thing being shared is the *presentation*, and a feature-first tree
gives every feature its own private table, so within two changes there are
three tables that differ.

**Animate the skeleton.** Rejected against the standing no-loop rule rather
than on its own merits, which is the right way round: a rule that bends for
the first pleasant exception is not a rule.

## Consequences

Four guards now hold the system together, and each was verified to fail when
the property it names is broken:

- only `tokens.css` names a raw value, and it defines both schemes;
- the layers depend only downward, and every declared layer is non-empty;
- every icon a view references is declared, hidden from assistive technology,
  and built rather than parsed;
- `dataTable` checks loading before empty (the reverse always wins, since a
  loading table has no rows), every collection has a loading flag, and every
  flag is cleared in a `finally` so a failed request cannot leave a permanent
  skeleton.

Test guards that named a file now name a module: `readAsset` resolves a base
name to its path, with a companion test that base names stay unique. The
guards that are about "the styles" read every sheet rather than whichever
file a rule used to live in, which is strictly stronger than before.

Two test harnesses that copied assets to a temporary directory reproduce the
directory tree instead of flattening it, because a flattened copy runs
modules whose relative imports mean something other than what ships.

Nothing about the bounds, the privacy allowlists or the Content-Security-Policy
changed. The page still parses no markup, stores nothing, follows no cursor on
its own, and loads no font.
