# Life Ledger

Personal expense register. The first slice replaces a manual mobile text note — each note line becomes a stored **Expense** on the server, entered as free text (splittypie-style), listed, edited, and deleted.

## Language

**Expense**:
A single stored spending record — what one line of the mobile note becomes once saved on the server. Carries a date, an amount, a description, a **Payer**, and optional markers.
_Avoid_: Transaction, entry, movement

**Amount**:
The full amount actually paid for an expense, held as an integer number of minor units (e.g. cents). The display layer formats it for humans.
_Avoid_: Price, cost, total

**Shared** (flag):
A boolean on an expense meaning it is split 50/50 between the two **Users**, whatever the **Payer**. Amount still records the full amount paid; each User's half is derived at display time. Set by registering the line in the Shared tab — there is no `*` marker. Shared expenses are listed only in the Shared tab; ordinary ones only in the main tab.
_Avoid_: Split, halved, half

**User**:
One of the two people the app knows: **Javi** (the owner, sees both tabs) and **Carla** (the partner, sees only the Shared tab). Identified by which password was entered at login, carried in the signed session cookie.
_Avoid_: Account (taken), member, partner

**Payer**:
The User who paid a shared expense (`paid_by`): whoever registered it, correctable on edit. Blank-defaults to Javi, so every expense that predates the Shared tab reads as paid by Javi.
_Avoid_: Owner, author

**Shared balance** (derived):
Who owes whom, and how much, shown at the top of the Shared tab ("Javi owes Carla 23,40 €", or "Estáis a mano"). Never stored: for each shared expense, the Payer is owed half its **Net cost** by the other User; **Settlements** then move it back. Paybacks on a shared expense are assumed received by its Payer.
_Avoid_: Debt, total owed

**Settlement**:
A transfer of money between the two Users to settle the Shared balance, entered in the Shared tab with a leading `=` (`=40 devolución`), paid by whoever types it. Not a Shared expense and not halved: it only moves the balance. Javi → Carla is stored as an expense, Carla → Javi as an income, and both count in Javi's real totals but are listed only in the Shared tab.
_Avoid_: Repayment, payback (that is linked to an Expense)

**Account** (tag):
An optional free-text tag naming which account or card an expense was paid from (e.g. personal, work, common, home). Blank when omitted, in which case "personal" is assumed at display time; may be filled in later when the expense is reviewed. A pure label in this slice — it carries no split or halving behavior.
_Avoid_: Wallet, source, card

**Canonical entry line**:
An expense rendered back into entry syntax from its stored fields — the inverse of parsing. Its date is always an absolute `DD/MM`, never a relative `-N`, so it re-parses to the same expense regardless of when. This is what fills the box when editing (never the original keystrokes), which keeps an edit from silently shifting the date.
_Avoid_: Raw text, verbatim line

**Income**:
A stored record that is structurally an Expense but negative — it subtracts from total expenses. Reuses the expense entry/edit path (the free-text parser, the record shape, the canonical entry line, the edit/delete flow); the one thing it lacks is the Shared flag (an income is never shared; only a Carla → Javi **Settlement** is an income tied to the Shared tab). Entered with a leading `+` on the amount (`+30 …`). Lives in its own `income` table. A **standalone** income (not linked to an expense — see Payback) displays **blue with no leading `−`**: money-in that nets against nothing on screen reads apart from a payback (ADR-0009 [#59](https://github.com/emepetres/life-ledger/issues/59) amendment).
_Avoid_: Credit, refund, deposit

**Payback**:
An Income linked to a specific Expense (`linked_expense_id`), recording money received back against a fronted group ticket. Carries its own date and own account, both independent of the parent — you can front from one account and be repaid into another, on another day. A standalone income has no such link; a payback is the linked case. A payback displays **green with a leading `−`** (it claws back part of a specific expense), unlike a standalone income's blue no-`−` treatment.
_Avoid_: Repayment, settlement, reimbursement

**Intent**:
What the user is doing with an entry line: adding a new record, adding a **Payback** to a specific Expense, or editing a specific existing Expense or Income. The intent decides which kind of record the line may become. A plain add accepts either kind (the `+` decides). A Payback must be an Income. An edit keeps the record's kind, so a line whose `+` disagrees with it is refused rather than converted, and an edited Payback stays linked to the same Expense. Only records still within the edit window (see Canonical entry line) can be the target of an edit intent.
_Avoid_: Mode, action, context

**Net cost** (derived):
An expense's full Amount minus the sum of its linked paybacks — never stored, computed at display time. May be negative when over-repaid (rendered green). The list surfaces net cost as the headline figure while the stored Amount stays the full amount paid.
_Avoid_: Balance, remainder, owed
