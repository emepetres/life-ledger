# Income and paybacks: record shape, entry syntax, and net cost

Group payment — I front a group ticket (typically a work meal), the rest of the
group pay me back their share — is the settlement effort [ADR 0001](./0001-stored-expense-record-shape.md)
and [CONTEXT.md](../../CONTEXT.md) deferred. The stored `Expense` was shaped to
make it clean: exact integer minor units, and the **full amount actually paid**
always stored ([ADR 0001](./0001-stored-expense-record-shape.md)). This ADR pins
the domain model that nets a fronted expense back down as paybacks arrive.

It **extends** ADR-0001 (record shape), ADR-0002 (entry syntax), and ADR-0008
(canonical-entry-line edit); it **amends none of them**. In particular the stored
`expense` table, the `*` split marker, and the full-amount-paid rule are all
untouched.

Assembled from the locked decisions of wayfinder map
[#48](https://github.com/emepetres/life-ledger/issues/48) — tickets
[#49](https://github.com/emepetres/life-ledger/issues/49) (record shape),
[#50](https://github.com/emepetres/life-ledger/issues/50) (entry & display), and
[#52](https://github.com/emepetres/life-ledger/issues/52) (store query shape).
Origin: [#46](https://github.com/emepetres/life-ledger/issues/46).

## The model: Income, of which Payback is the linked case

The effort is built on a first-class **Income**: structurally an expense but
**negative** — it subtracts from total expenses — reusing almost all expense
code (the free-text parser, the record shape, the canonical entry line, the
edit/delete flow). The one thing an income drops is the `*` **split** marker: an
income is never split.

A **Payback** is simply an income **linked** to a specific expense. Standalone
incomes (no link) are in scope and behave like any other income; a payback is the
linked special case, distinguished solely by a non-NULL `linked_expense_id`.

**Net cost** of an expense is *derived*, never stored: `paid − Σ linked
paybacks`. It may go **negative** when you were over-repaid.

## Record shape — the `income` table

A **separate `income` table**, not a negative row in `expense`. This keeps
`expense` pristine and literally split-free (ADR-0001 faithful); reuse is at the
*code* level — a shared parser and a shared struct base — not the table.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | INTEGER PRIMARY KEY | rowid alias, autoincrements |
| `date` | TEXT NOT NULL | ISO-8601 `YYYY-MM-DD`; its **own** date, independent of any parent |
| `amount` | INTEGER NOT NULL `CHECK (amount > 0)` | **positive magnitude**, EUR minor units (cents) |
| `description` | TEXT NOT NULL | **required** — reuses the expense `ErrEmptyDescription` save-gate |
| `account` | TEXT (nullable) | NULL = omitted ("personal" is a display-time default); its **own** account |
| `raw_text` | TEXT NOT NULL | retained — re-parse if the syntax evolves (as expense) |
| `linked_expense_id` | INTEGER (nullable) `REFERENCES expense(id) ON DELETE CASCADE` | **NULL = standalone income; set = payback** |
| `created_at` / `updated_at` | TEXT NOT NULL | store-owned UTC ISO-8601 |

**No `split` column.** The table *is* the income discriminator; the presence of
`linked_expense_id` is the payback discriminator. Field choices otherwise track
ADR-0001 verbatim — integer minor units, nullable free-text account, retained
`raw_text`, store-owned timestamps.

### Decisions

1. **Storage — separate table.** `expense` stays split-free and unchanged; the
   feature is purely additive. No existing row or column is touched.
2. **Amount — positive magnitude** (`CHECK (amount > 0)`), exactly like expense.
   The "subtracts" meaning lives in the table and the derivation, not the sign,
   so the expense amount validation/formatting is reused verbatim.
3. **Link delete — `ON DELETE CASCADE`.** Paybacks are subitems of the ticket;
   deleting the parent expense deletes them. The expense-delete confirm flow
   ([#45](https://github.com/emepetres/life-ledger/issues/45)) is where the user
   is told they go too.
4. **Bounds — ungated.** `Σ paybacks` may exceed the parent, so derived net cost
   may go negative (over-repaid). Only the per-row `amount > 0` rule applies.
   Gating would reintroduce the balance-tracking sub-ledger this effort rules out.
5. **Mutability — editable + deletable exactly like an expense**, including the
   canonical-entry-line re-parse and the one-year `Editable` cutoff
   ([ADR 0008](./0008-canonical-entry-line-edit.md)). `linked_expense_id` is
   **not** expressible in the entry line and is preserved **out-of-band** across
   edits: an edit never re-parents a payback.
6. **No separate note field.** `description` carries who/what (e.g. "Bob's lunch
   share").

### Derived, never stored

- Total expenses = `SUM(expense.amount) − SUM(income.amount)`.
- Net cost of an expense `e` = `e.amount − Σ(income.amount WHERE linked_expense_id = e.id)`
  — may be negative (ungated).

## Entry syntax — the leading `+` sigil

An income reuses the expense free-text entry/edit path
([ADR 0002](./0002-free-text-entry-syntax.md)); this ADR adds **one** parse rule.

- An income is typed as `+<amount> <description + markers>`, e.g.
  `+30 Bob's share @bbva`. A `+` **immediately before the first bare number**
  marks the record as an income — amount-first, mirroring the existing `-N`
  date-offset intuition, reading as "money in".
- **Everything else in ADR-0002 is unchanged**: floating `@account`, the date
  token, description-is-the-remainder, lenient parse, live preview. A leading `-`
  still marks a date offset, never a negative amount.
- **`*` is rejected on an income** — an income can't be split. This is a new
  save-gate violation, handled by the existing lenient-parse / gate-on-save
  machinery (no new control flow).
- The **preview** echoes the income amount as a green `−€30.00` credit chip (it
  subtracts) plus the resolved date/description/account chips, exactly like an
  expense. *(Amended [#59](https://github.com/emepetres/life-ledger/issues/59):
  a standalone `+…` line — no active payback link — previews **blue with no `−`**;
  only a payback previews green with the `−`. See the amendment below.)*
- The **parent link is set out-of-band, never typed.** A per-row `+ payback`
  action on an expense opens the quick-add pre-set to income and pre-linked
  (shown as a non-editable `↩ payback → <expense>` chip with a Cancel
  affordance). A standalone income is just a `+…` line typed with no active link.

## Display — net cost is the headline, storage stays full

The list surfaces **net cost as the headline figure** while storage remains the
faithful full-amount transcript. This reconciliation is the crux worth stating
plainly:

> **Net-first is a display choice only.** The stored `expense.amount` remains the
> full amount actually paid (ADR-0001 untouched). Net cost is computed at render
> time from `paid − Σ paybacks` and is never written to any row.

- A fronted expense renders **net cost** as the primary amount, with the full
  paid amount shown **struck-through** beneath it. A `−€60.00 from N paybacks ▾`
  summary expands to each payback's amount, description, its own `@account`, and
  its own date.
- **Over-repaid (negative net)** renders **green**, consistent with the ungated
  derivation.
- **Standalone incomes** render as their own green credit rows, interleaved by
  their own date into the existing day groups. *(Amended
  [#59](https://github.com/emepetres/life-ledger/issues/59): a standalone income
  renders **blue with no leading `−`**, not green — see the amendment below.
  Over-repaid net and individual paybacks keep green with the `−`.)*
- Rows with **no paybacks** are visually unchanged from today.
- **Day total** = the sum of **net costs of expenses only**. Standalone incomes
  render as green rows but do **not** move the day total, so it keeps meaning
  "what the day cost me" (paybacks already netted in) and can't be flipped
  negative by a one-off credit.

## Amendment ([#59](https://github.com/emepetres/life-ledger/issues/59)) — standalone-income display: blue, no leading `−`

The original Display decisions above rendered **every** income green with a
leading `−`. In use the `−` reads wrong on a **standalone income** (no
`linked_expense_id`): the `−` says "this claws back a specific expense", but a
standalone income nets against nothing on screen — it does not even move a day
total. So #59 revises the display treatment. This is **display only** — the
record shape, the `amount > 0` rule, and the `Total expenses = ΣExpense −
ΣIncome` math are all untouched:

- A **standalone income** (`linked_expense_id IS NULL`) renders **blue, with no
  leading `−`** — both its list-row headline amount and its live `+…` preview
  chip. New palette tokens `--income: #1d4ed8` (text) and `--income-soft:
  #dbeafe` (preview-chip background), kept visibly distinct from the indigo
  `--acct` account chip.
- Everything that genuinely nets **against an expense keeps green with the `−`**:
  each **payback** in the disclosure drawer, the `−€X from N paybacks` summary,
  and an **over-repaid** expense's negative net headline.
- The preview therefore colours by **link state**: a `+…` line typed with **no
  active payback link** is a standalone income → blue / no-`−`; the same line in
  **payback mode** (an active `linked_expense_id`) is a payback → green / `−`.

### Payback entry must stay an income (new save-gate rule)

Also from #59, extending the entry-syntax save gates (§ "Entry syntax — the
leading `+` sigil"): when the quick-add box is in **payback mode** (a
`linked_expense_id` is present, set out-of-band by the `+ payback` action), the
line **must remain an income** — its leading `+` cannot be removed to turn the
entry into an expense. A non-income line in payback mode is a new save-gate
violation: Save is disabled in the live preview, and the server `/add` handler
mirrors the refusal for the no-JS path. It is handled by the existing
lenient-parse / gate-on-save machinery — no new control flow, exactly like
`*`-on-income.

## Store query shape

The store stays a **thin per-table repository**; the server assembles the netted,
interleaved feed (net cost and "feeds" are display concepts, not storage).

- `store.List(ctx)` — unchanged.
- **new** `store.ListIncomes(ctx)` — **all** incomes (linked paybacks *and*
  standalone) in one flat `SELECT … FROM income ORDER BY date DESC, id DESC`. The
  store does not split the two; the server inspects `linked_expense_id` per row.
- The server buckets incomes into `map[int64][]Income` keyed by
  `linked_expense_id`, attaches each bucket to its expense, and treats NULL-link
  incomes as standalone feed entries. **Two queries total, regardless of row
  count** — N+1 buys nothing, since the whole ledger renders every time.
- Netting + interleaving live in `internal/server/view.go` beside `groupByDay`.
  What crosses into `html/template` is **one unified discriminated `rowView`**:
  a kind flag, net-cost display fields (net headline, struck-through paid,
  over-repaid flag), and an optional nested `Paybacks []paybackView`. The template
  ranges once with an `{{if .IsCredit}}…{{else}}…{{end}}` branch (`html/template`
  has no type switch — a discriminator field is the idiom).

## Go representation

`internal/expense` gains an **unexported embedded base `entry`** — `ID, Date,
Amount, Description, Account, RawText, CreatedAt, UpdatedAt` — carrying the fields
common to both records. `Expense` embeds `entry` + `Split bool`; `Income` embeds
`entry` + `LinkedExpenseID *int64`. Neither carries the other's field, so each
type stays honest about what it holds; the base stays an implementation detail so
callers always hold an `Expense` or an `Income`.

Both are built from the **same parser result**. The parser's `ParsedExpense` is
renamed **`ParsedEntry`** (it now represents an expense *or* an income) and gains
`IsIncome bool`; the server branches on it to build the right record. `Editable`
stays a shared free function; `EntryLine()` is per-type (expense appends `*`;
income prepends `+`, never `*`), sharing a helper for the common middle.

## Considered alternatives

- **Negative row in the `expense` table** (a signed amount, no second table).
  Rejected: it makes `expense` non-pristine, forces `split` to be meaningful-or-
  meaningless per row, and the "full amount paid" invariant of ADR-0001 stops
  being literally true. A separate table keeps the expense record a faithful
  transcript and confines the new concept.
- **Storing net cost** (a computed column or a maintained running total).
  Rejected: it duplicates derivable state, must be re-derived on every payback
  add/edit/delete, and re-opens the exact rounding/consistency questions the
  full-amount-paid rule avoids. Net cost is cheap to compute at render time.
- **Gating over-repayment** (refuse or cap `Σ paybacks > paid`). Rejected: it
  reintroduces balance tracking — "you can't be repaid more than you're owed" is
  an expected-share concept — which is explicitly out of scope. Over-repay just
  renders green.
- **Reusing the `*` marker to mean "payback"** or otherwise unifying the two.
  Rejected: it reopens a locked ADR (0001/0002) for no gain; `*` and paybacks are
  orthogonal and stay so.
- **Eager JOIN of expenses to paybacks in the store** (one row-multiplying
  query, or N+1 per-expense fetches). Rejected in favour of two bulk reads +
  in-memory stitch: no row multiplication, no per-row round-trips, and the store
  stays free of display concepts.
- **One `Expense` struct reused for both**, or **two flat sibling structs with no
  shared base**. Rejected in favour of the shared unexported `entry` base: reuse
  lives in one place, yet each public type carries only its own fields.

## Out of scope

- **Debt / expected-share tracking** — "who owes what", per-person shares,
  outstanding balances. The Splitwise-style effort; ruled out (see gating, above).
- **Reporting / analysis** over net cost (totals, summaries) — a separate effort.
- **Multi-currency paybacks** — a payback in a different currency than its
  expense. EUR-only stays.
- **Any redefinition of the `*` split marker** — ADR-0001 / ADR-0002 stay locked.

Decided across issues [#49](https://github.com/emepetres/life-ledger/issues/49),
[#50](https://github.com/emepetres/life-ledger/issues/50),
[#52](https://github.com/emepetres/life-ledger/issues/52), and composed in
[#51](https://github.com/emepetres/life-ledger/issues/51); ubiquitous language in
[CONTEXT.md](../../CONTEXT.md).
