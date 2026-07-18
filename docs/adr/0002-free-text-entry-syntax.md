# Free-text entry syntax and parse rules

For the minimal expense-register slice, an **Expense** is entered as a single free-text line (splittypie-style quick-add). This ADR fixes that syntax and the rules for parsing it into the stored record shape ([ADR 0001](./0001-stored-expense-record-shape.md)).

## Syntax

Shape: `<amount> <description + floating markers, in any order>`

| Element | Required | Syntax | Parses to |
| --- | --- | --- | --- |
| Amount | yes | first bare positive number; `.` or `,` decimal (`12.50` = `12,50`) | `amount` (minor units, implied EUR) |
| Description | yes | all text left after amount and markers are removed; must be non-empty | `description` |
| Date (relative) | no | `-N` = N days before today (`-1` = yesterday) | `date` |
| Date (absolute) | no | `DD/MM` (no leading zeros); `DD/MM/YYYY` to override | `date` |
| Account | no | `@tag`, a single token (`@credit-card` for two words) | `account` |
| Split | no | standalone `*` | `split = true` |
| Date default | — | omitted → today | `date = today` |

The whole verbatim line is retained as `raw_text` so records can be re-parsed if the syntax evolves.

## Parse rules

- **Amount is the first bare positive number.** No currency token (implied EUR), no thousands separator. A leading `-` marks a date offset, never a negative amount.
- **Markers float** — `@account`, `*`, and the date token may appear anywhere after the amount; their order is irrelevant. Whatever remains is the description.
- **`*` sets `split` only** — never inferred from the account (see ADR 0001). Full amount is still stored; halving math is out of scope.
- **Account is a free-text tag**; blank when omitted, with "personal" applied only at display time.
- **Date resolution:**
  - `-N` → today minus N calendar days.
  - `DD/MM` → the **most recent occurrence at or before today** (so `24/12` typed in Jan 2026 → 24 Dec 2025; today's own `DD/MM` → today). `DD/MM/YYYY` overrides, and is the only way to name a future or >1-year-old date.
- **Multiple bare numbers** → first is the amount, the rest stay in the description (lenient; the preview shows the result).

## Live preview

Rendered server-side (Go `html/template` + htmx, keyup → parse endpoint — no duplicate client parser). Echoes the **resolved** fields as chips: formatted amount (`€12.50`), resolved date (`Fri 17 Jul`, not `-1`), description, account (`@work` or *personal (default)*), and a `½ split` badge shown only when `*` is present.

## Save gate

Refuse to save (button disabled with a fix message; server re-validates on submit so a no-JS post is also rejected) when:

- no amount, or
- empty description, or
- two date tokens (`-1 17/7`), or
- two `@account` tokens.

Everything else parses leniently. The philosophy: parse leniently, refuse to save only when genuinely ambiguous or missing a required field, and let the preview show exactly what will be stored.

## Deviations worth recording

- **Amount-first, not description-first** — splittypie leads with the name (it needs the label prominent for splitting); this is a money-ledger, so the amount — the one token that must be machine-read exactly — leads and is trivially locatable.
- **Sigil (`@`) for account, not a trailing comma** — a comma collides with commas in natural descriptions and forces positional ordering; a sigil is unambiguous, order-independent, and highlightable in the preview.
- **`-N` relative dates over words or repeated glyphs** — the common non-today case is one or two days back; `-N` is fast to type, self-documenting, and scales to the occasional older entry, while the preview resolves it to a real date so counting is never trusted blind.
- **`DD/MM` infers the latest past year** — matches the "logged a few days late" reality and effectively retires the `DD/MM/YYYY` form for normal use.
- **Description required** — an expense with no information is not worth storing; amount-only entry is rejected.

Decided in [issue #4](https://github.com/emepetres/life-ledger/issues/4); domain fields in [ADR 0001](./0001-stored-expense-record-shape.md); ubiquitous language in [CONTEXT.md](../../CONTEXT.md).
