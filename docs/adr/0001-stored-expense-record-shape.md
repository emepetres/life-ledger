# Stored Expense record shape

For the minimal expense-register slice, a stored **Expense** has: `id` (autoincrement integer PK), `date` (calendar day, the expense date), `amount` (integer minor units of a single implied currency, EUR), `description` (text), `split` (boolean), `account` (nullable free-text tag), `raw_text` (verbatim entry line), and `created_at` / `updated_at` timestamps.

Several field choices are deliberate deviations worth recording:

- **`amount` as integer minor units**, not a float or decimal — exact arithmetic, no rounding-mode decisions, keeps the future half-accounting math clean.
- **`amount` is the full amount paid**, and **`split` is a plain boolean**, not a share fraction or a stored half. The `*` marker only records intent ("half is really mine, via a separate partner-split app"); the halving math is out of scope for this effort, so storing the full amount loses nothing and defers the rounding rules.
- **`split` is independent of `account`** — it is set only by the explicit `*` marker, never inferred from a "shared" account. Inferring it would be the shared-account-halving behavior the effort defers, and would force accounts to become managed data now.
- **`account` is an optional free-text tag**, not an enum, and is **blank when omitted** ("personal" is a display-time default, not written into the record). Chosen to keep the stored record a faithful transcript and to avoid an account-management surface; promoting to a controlled set is a clean later migration once real usage is seen. Account may be filled in later during review.
- **Single implied currency (EUR)**, not stored per record — a per-record currency is speculative for euro-only accounts; adding a column with an `EUR` backfill later is non-destructive.
- **`raw_text` is retained** so the entry syntax can be refined later and stored records re-parsed, rather than losing information to an early parsing decision.

Decided in [issue #3](https://github.com/emepetres/life-ledger/issues/3); ubiquitous language in [CONTEXT.md](../../CONTEXT.md).
