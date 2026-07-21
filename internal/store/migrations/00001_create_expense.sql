-- +goose Up
-- Expense table and its list-sort index, per ADR-0003. SQLite has no native
-- boolean or date type, so split is an INTEGER 0/1 with a CHECK constraint and
-- date/timestamps are ISO-8601 TEXT (lexically sortable, no timezone ambiguity).
CREATE TABLE expense (
  id          INTEGER PRIMARY KEY,              -- rowid alias, autoincrements
  date        TEXT    NOT NULL,                 -- expense day, ISO-8601 'YYYY-MM-DD'
  amount      INTEGER NOT NULL,                 -- full paid, EUR minor units (cents)
  description TEXT    NOT NULL,
  split       INTEGER NOT NULL DEFAULT 0 CHECK (split IN (0, 1)),
  account     TEXT,                             -- nullable; blank/omitted = NULL
  raw_text    TEXT    NOT NULL,
  created_at  TEXT    NOT NULL,                 -- ISO-8601 timestamp, UTC
  updated_at  TEXT    NOT NULL
);

CREATE INDEX idx_expense_date ON expense (date DESC);

-- +goose Down
DROP INDEX idx_expense_date;
DROP TABLE expense;
