-- +goose Up
-- Income table, per ADR-0009. An income is structurally an expense but negative:
-- a "money in" credit that subtracts from total expenses. It is a *separate*
-- table so the expense record stays pristine and literally split-free — reuse
-- lives at the code level (shared parser, shared struct base), not the schema.
-- As with expense (00001), SQLite has no native date type, so date/timestamps
-- are ISO-8601 TEXT (lexically sortable, no timezone ambiguity).
--
-- The table itself is the income discriminator; the presence of a non-NULL
-- linked_expense_id is the payback discriminator (NULL = standalone income,
-- set = a payback netting down that expense). There is deliberately no split
-- column — an income can never be split. foreign_keys is ON on every connection
-- (see store.dsn), so the linked_expense_id reference and its ON DELETE CASCADE
-- are enforced: deleting a fronted expense deletes its paybacks with it.
CREATE TABLE income (
  id                INTEGER PRIMARY KEY,              -- rowid alias, autoincrements
  date              TEXT    NOT NULL,                 -- credit day, ISO-8601 'YYYY-MM-DD'; its own date
  amount            INTEGER NOT NULL CHECK (amount > 0), -- positive magnitude, EUR minor units (cents)
  description       TEXT    NOT NULL,                 -- required (shared ErrEmptyDescription save gate)
  account           TEXT,                             -- nullable; blank/omitted = NULL; its own account
  raw_text          TEXT    NOT NULL,                 -- verbatim entry line, retained for re-parse
  linked_expense_id INTEGER REFERENCES expense(id) ON DELETE CASCADE, -- NULL = standalone, set = payback
  created_at        TEXT    NOT NULL,                 -- ISO-8601 timestamp, UTC
  updated_at        TEXT    NOT NULL
);

-- Newest-first listing (mirrors idx_expense_date): the flat ListIncomes reads
-- ORDER BY date DESC.
CREATE INDEX idx_income_date ON income (date DESC);
-- Bucketing paybacks by their parent expense (the server stitches netted feeds
-- keyed on linked_expense_id).
CREATE INDEX idx_income_linked ON income (linked_expense_id);

-- +goose Down
DROP INDEX idx_income_linked;
DROP INDEX idx_income_date;
DROP TABLE income;
