# Life Ledger

Personal expense register. The first slice replaces a manual mobile text note — each note line becomes a stored **Expense** on the server, entered as free text (splittypie-style), listed, edited, and deleted.

## Language

**Expense**:
A single stored spending record — what one line of the mobile note becomes once saved on the server. Carries a date, an amount, a description, and optional markers.
_Avoid_: Transaction, entry, movement

**Amount**:
The full amount actually paid for an expense, held as an integer number of minor units (e.g. cents). The display layer formats it for humans.
_Avoid_: Price, cost, total

**Split** (marker):
A boolean marker on an expense meaning it is also registered in a separate partner-split app, so only half is really the user's. The expense's Amount still records the full amount paid; the halving math is a separate, later effort. Set only by the explicit `*` marker in the entry — never inferred from the Account.
_Avoid_: Shared, halved, half

**Account** (tag):
An optional free-text tag naming which account or card an expense was paid from (e.g. personal, work, common, home). Blank when omitted, in which case "personal" is assumed at display time; may be filled in later when the expense is reviewed. A pure label in this slice — it carries no split or halving behavior.
_Avoid_: Wallet, source, card

**Canonical entry line**:
An expense rendered back into entry syntax from its stored fields — the inverse of parsing. Its date is always an absolute `DD/MM`, never a relative `-N`, so it re-parses to the same expense regardless of when. This is what fills the box when editing (never the original keystrokes), which keeps an edit from silently shifting the date.
_Avoid_: Raw text, verbatim line

**Income**:
A stored record that is structurally an Expense but negative — it subtracts from total expenses. Reuses the expense entry/edit path (the free-text parser, the record shape, the canonical entry line, the edit/delete flow); the one thing it lacks is the `*` split marker (an income is never split). Entered with a leading `+` on the amount (`+30 …`). Lives in its own `income` table.
_Avoid_: Credit, refund, deposit

**Payback**:
An Income linked to a specific Expense (`linked_expense_id`), recording money received back against a fronted group ticket. Carries its own date and own account, both independent of the parent — you can front from one account and be repaid into another, on another day. A standalone income has no such link; a payback is the linked case.
_Avoid_: Repayment, settlement, reimbursement

**Net cost** (derived):
An expense's full Amount minus the sum of its linked paybacks — never stored, computed at display time. May be negative when over-repaid (rendered green). The list surfaces net cost as the headline figure while the stored Amount stays the full amount paid.
_Avoid_: Balance, remainder, owed
