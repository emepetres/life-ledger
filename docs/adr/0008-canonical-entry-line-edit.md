# Editing re-renders a canonical entry line; edits capped at 12 months

Editing an expense used to reload its verbatim `raw_text` into the quick-add box and re-parse it against *now* on save. For a line carrying a relative (`-N`) or year-less absolute (`DD/MM`) date token, that re-resolves the date against the day of the *edit*, so an edit touching only the amount could silently walk the stored date ([issue #47](https://github.com/emepetres/life-ledger/issues/47)).

We fix this by editing a **canonical entry line** instead of the original keystrokes:

- On edit, the box is filled from the stored fields, rendered back to entry syntax ([ADR 0002](./0002-free-text-entry-syntax.md)) as `<amount> <description> <DD/MM> [@account] [*]` — the amount trims a `.00` fraction for whole euros, and **the date is always emitted as an absolute `DD/MM`**, never `-N` and never omitted.
- The edit path is then byte-for-byte identical to the add path: parse the box against `now()`. Because the date in the box is already absolute, the parse reference no longer affects it, so nothing has to thread a special "reference date" through the parse/preview machinery.
- On save, `raw_text` is rewritten to the edited canonical line. This **amends [ADR 0001](./0001-stored-expense-record-shape.md)**: after an edit, `raw_text` is a canonicalized-but-equivalent entry line rather than the original verbatim transcript. A freshly added expense still stores its verbatim line; only editing canonicalizes. This is if anything more robust for the "re-parse if the syntax evolves" goal — an absolute date survives re-parsing, a stale `-N` would not.

A year-less `DD/MM` only re-parses back to its own date while that date is **strictly less than a year old** (it resolves to "the most recent occurrence at or before today", so a date exactly one year ago resolves to *this* year's occurrence and drifts). So editing is **capped at one year**: an expense is editable iff its `date` is strictly after the same calendar day one year before today (`dateOnly(date).After(dateOnly(now).AddDate(-1,0,0))`). This cutoff *is* the `DD/MM` round-trip boundary — set deliberately so that every editable item renders drift-free and the box never needs the longer `DD/MM/YYYY` form. The list omits the Edit affordance for older rows, and both `/edit/{id}` handlers (GET and POST) independently return a bare `403` for an out-of-range id; `404` still means unknown id.

The canonical renderer and the editable predicate both live in the `expense` package beside `Parse`, so the parse↔render round-trip and the cutoff boundary are testable as pure black boxes, and the coupling between the 12-month cap and the `DD/MM` round-trip stays documented in one place — if the cap is ever loosened, it sits right next to the renderer that would then start drifting.

## Considered alternatives

- **Anchor the parse to the original entry day** (feed `created_at`'s calendar day as the "today" for the edit parse). Reproduces the original resolution for an untouched token without changing the box or `raw_text`, but a *deliberately* changed relative token then resolves against the original entry day rather than now — a second surprising rule — and it keeps two divergent parse references alive. Rejected in favour of "edit exactly like add".
- **`DD/MM/YYYY` in the canonical line** (full year, always). Removes the 12-month cap entirely and needs no guard. Rejected because normal use never edits year-old items; the shorter `DD/MM` is preferred in the box, and the cap is wanted as an explicit product limit anyway.
- **No guard, accept the >12-month drift.** Simplest, but reintroduces the exact silent date-shift this ADR closes, just more rarely. Rejected.

Ubiquitous language in [CONTEXT.md](../../CONTEXT.md).
