-- +goose Up
-- Forgiveness table, per ADR-0016: Javi waiving part or all of what Carla owes
-- him on the Shared balance, without money moving. It is its own table (not an
-- expense or income row) because those feed Javi's totals, and the forgiven money
-- is already inside them. The amount is stored because the balance is derived and
-- a Forgiveness must remember what was waived at that moment. Date is ISO-8601
-- TEXT, like every other date.
CREATE TABLE forgiveness (
  id         INTEGER PRIMARY KEY,                 -- rowid alias, autoincrements
  date       TEXT    NOT NULL,                    -- forgiven day, ISO-8601 'YYYY-MM-DD'
  amount     INTEGER NOT NULL CHECK (amount > 0), -- positive magnitude, EUR minor units (cents)
  created_at TEXT    NOT NULL                     -- ISO-8601 timestamp, UTC
);

CREATE INDEX idx_forgiveness_date ON forgiveness (date DESC);

-- +goose Down
DROP INDEX idx_forgiveness_date;
DROP TABLE forgiveness;
