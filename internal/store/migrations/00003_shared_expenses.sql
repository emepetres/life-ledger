-- +goose Up
-- Shared tab, per ADR-0015. The existing `split` boolean is reused as the
-- "shared" flag (no data migration), so the only new facts are who paid and
-- whether a record is a settlement between the two users.
--
-- paid_by defaults to 'javi', so every expense that predates the Shared tab reads
-- as paid by Javi. A settlement is Javi -> Carla (an expense row) or Carla -> Javi
-- (an income row); neither is shared, so neither is halved.
ALTER TABLE expense ADD COLUMN paid_by    TEXT    NOT NULL DEFAULT 'javi';
ALTER TABLE expense ADD COLUMN settlement INTEGER NOT NULL DEFAULT 0;
ALTER TABLE income  ADD COLUMN settlement INTEGER NOT NULL DEFAULT 0;

-- The '*' marker is gone from the entry syntax: drop it from the retained raw
-- text of the expenses that carried it, so a future re-parse doesn't read it as
-- part of the description.
UPDATE expense
   SET raw_text = trim(replace(' ' || raw_text || ' ', ' * ', ' '))
 WHERE split = 1;

-- +goose Down
ALTER TABLE income  DROP COLUMN settlement;
ALTER TABLE expense DROP COLUMN settlement;
ALTER TABLE expense DROP COLUMN paid_by;
