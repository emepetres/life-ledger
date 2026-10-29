# Forgiveness of the Shared balance

**Status:** accepted; extends [ADR-0015](0015-shared-tab-and-two-users.md).

Sometimes Carla owes Javi and Javi decides not to collect. The **Shared balance** must go to zero, and the app must keep a visible trace of what was forgiven. Vocabulary in [CONTEXT.md](../../CONTEXT.md) (**Forgiveness**, **Shared balance**, **Settlement**).

## Decision

| Aspect | Decision |
| --- | --- |
| Who and how much | Only Javi, only while Carla owes him. The amount defaults to the whole balance and may be lowered, never above the balance at that moment (`0 < amount ≤ balance`). A partial Forgiveness must not be entered as a Carla → Javi Settlement: that would store an income that never arrived and falsify Javi's totals. |
| Storage | Own table `forgiveness` (date, amount in minor units), one additive migration (`00004`). Not an `expense` or `income` row. |
| Totals | Not counted. Javi's totals already include the forgiven money: he paid the full Amount of each Shared expense and never got Carla's half back. |
| Balance | A Forgiveness comes off the balance *as shown* (already rounded to the cent), so forgiving the whole displayed balance always leaves it at zero, even on an odd cent. Derived as before; nothing is frozen. Editing or deleting an older Shared record after forgiving can make the balance non-zero again. |
| Entry | A "Forgive" button beside the balance, visible only to Javi while Carla owes him, that opens a small form (a plain POST, not htmx-boosted: htmx does not swap a 422) with an amount field (the whole balance by default) and a date field (today by default). Opening it is the confirmation step. No new text syntax. |
| Refusals | `POST /forgive` answers 404 to Carla, and 422 (back on the Shared tab with a note, nothing stored) when Carla owes nothing or Javi is the debtor, the amount is unreadable, zero or above the balance, or the date is malformed or in the future. |
| Listing | Shared tab only, visible to both Users, neutral colour, outside the day total ("Javi forgave €23.40"). |
| Undo | Deletable by Javi only (`POST /delete/forgiveness/{id}`; Carla gets a 404, since the act was his); never editable, since its amount is the balance at that instant. |

## Why this and not the alternatives

- **Own table** over a third `settlement`-style flag on `expense`: expense and income rows feed Javi's totals, and a forgiveness row there would count the same money twice. A separate table keeps "real money moved" and "what Carla owes, given up" apart.
- **Not a new expense** over recording the loss as spending: the loss is already inside the totals. Showing it as a loss in a report can be derived later from the Forgiveness records without storing anything new.
- **Optional amount, whole balance by default** over whole-balance-only: the common case stays one tap, and a partial Forgiveness needs no money to move first. Reusing a Settlement for it is wrong, since a Settlement implies a real transfer and counts in Javi's totals.

## Consequences

- `expense.SharedBalance` takes the Forgiveness records as a third input.
- Forgiving is an action on the balance, not an entry line, so ADR-0008's single text entry gate is untouched.
