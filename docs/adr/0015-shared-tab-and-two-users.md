# Shared tab, payer, and two users

**Status:** accepted; supersedes the *Credential* row of [ADR-0004](0004-access-model-auth.md) (single shared password) and drops the splittypie export and the `*` marker of [ADR-0001](0001-stored-expense-record-shape.md) / [ADR-0002](0002-free-text-entry-syntax.md).

Javi shared expenses with Carla in a separate app (splittypie) and retyped them by hand. This ADR moves that into Life Ledger: expenses split 50/50 with Carla are registered in their own **Shared tab**, either of the two can register them, and the app tells who owes whom. Vocabulary in [CONTEXT.md](../../CONTEXT.md) (**Shared**, **User**, **Payer**, **Shared balance**, **Settlement**).

## Decision

| Aspect | Decision |
| --- | --- |
| Tabs | `/` lists ordinary expenses and incomes; `/shared` lists shared expenses, settlements, and the paybacks of a shared expense. The `*` marker is gone from the entry syntax: the tab a line is typed in decides what it becomes. |
| Existing column | `expense.split` is **reused as "shared"** — no data migration. Every row that was split reads as shared. |
| New columns | `expense.paid_by` (`'javi'`/`'carla'`, default `'javi'`, so legacy rows read as paid by Javi), `expense.settlement` and `income.settlement` (0/1). One additive migration (`00003`), which also strips the `*` from the retained `raw_text` of split rows. |
| Split ratio | Always 50/50. The full amount stays stored; the half is derived. No percentages, no "only for one of us". |
| Balance | Derived, never stored (`expense.SharedBalance`): per shared expense the payer is owed half its **net cost** (paybacks are assumed received by the payer); settlements move it back. Odd cents round half up. |
| Settlement | A `=40 …` line in the Shared tab, paid by whoever types it. Javi → Carla is stored as an **expense** row, Carla → Javi as an **income** row, both flagged `settlement`, neither shared. They count in Javi's real totals but are listed only in the Shared tab. |
| Users | Two passwords (`LIFELEDGER_PASSWORD_HASH` for Javi, optional `LIFELEDGER_CARLA_PASSWORD_HASH`), **no selector**: which password is typed decides who logs in, carried as the signed cookie's payload. Old cookies (payload `"1"`) read as Javi so the deploy logs nobody out. |
| Carla's scope | Only the Shared tab. `/` redirects to `/shared`; edit, delete and payback on any record outside it answer 404 as if it did not exist. Her adds are always shared and paid by Carla, whatever the form posts. |
| Editing | Either user may edit or delete any shared record. An edit keeps the record's payer and kind. |
| Splittypie export | Removed — the data now lives here. |

## Why this and not the alternatives

- **Reuse `split`, add `paid_by`** over a `kind` enum or a separate `shared_expense` table: the smallest schema change that keeps every legacy row valid and every existing path (paybacks, edit window, canonical line) working. The cost is a column named `split` meaning "shared"; renaming it would need a table rebuild in SQLite for no behavioural gain.
- **Two passwords over one password plus a selector:** with a selector Carla could pick Javi and read his private ledger, so her restriction would be cosmetic. A second hash costs the same code and makes the identity unforgeable. This supersedes ADR-0004's single-credential row; the rest of that ADR (bcrypt, signed stateless cookie, rate limit, 30-day sliding expiry) is unchanged.
- **Settlement stored as expense/income** over a dedicated table: it is real money leaving or entering Javi's accounts, so it reuses the existing record kinds, edit path and totals. The price is a `settlement` flag on both tables and a payer implied by the table (income ⇒ Carla).
- **Payer set by whoever types, kept on edit** over a payer picker: no new form control, and the text-line entry stays the single gate. A wrong payer is fixed by deleting and re-adding the line; a picker can be added later without schema change.

## Consequences

- The tab is a property of the **Intent** (`expense.AddShared`, `Intent.IsShared`), so preview, add, and edit cannot disagree about it.
- A payback's tab follows its parent expense; settlement incomes have none.
- `Down` of migration `00003` drops the three columns but cannot restore the stripped `*`.
