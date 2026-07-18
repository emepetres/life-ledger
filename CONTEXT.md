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
