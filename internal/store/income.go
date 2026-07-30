package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// incomeColumns is the fixed column list every income read selects, in scan
// order. It mirrors the expense columns minus split (an income is never split)
// plus linked_expense_id (the payback discriminator, ADR-0009).
const incomeColumns = "id, date, amount, description, account, raw_text, linked_expense_id, created_at, updated_at"

// CreateIncome inserts i as a new income, assigning its ID and stamping
// CreatedAt and UpdatedAt to the same instant. amount is the positive magnitude
// (the amount > 0 CHECK backs this); a nil Account is written as SQL NULL, and a
// nil LinkedExpenseID as SQL NULL for a standalone income (ADR-0009).
func (s *Store) CreateIncome(ctx context.Context, i *expense.Income) error {
	now := s.now()
	args := append(writeArgsIncome(i), now.Format(tsLayout), now.Format(tsLayout))
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO income (date, amount, description, account, raw_text, linked_expense_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		args...)
	if err != nil {
		return fmt.Errorf("inserting income: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	i.ID = id
	i.CreatedAt = now
	i.UpdatedAt = now
	return s.backupAfterWrite(ctx)
}

// ListIncomes returns every stored income — both linked paybacks and standalone
// credits — newest first, ordered by date descending then id descending, exactly
// as List does for expenses. The store does not split the two kinds; the caller
// inspects LinkedExpenseID per row (nil = standalone, set = payback, ADR-0009).
func (s *Store) ListIncomes(ctx context.Context) ([]expense.Income, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+incomeColumns+` FROM income ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing incomes: %w", err)
	}
	defer rows.Close()

	var out []expense.Income
	for rows.Next() {
		i, err := scanIncome(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing incomes: %w", err)
	}
	return out, nil
}

// GetIncome returns the income with the given id, or ErrNotFound if none exists.
// It mirrors Get for expenses (ADR-0009).
func (s *Store) GetIncome(ctx context.Context, id int64) (expense.Income, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+incomeColumns+` FROM income WHERE id = ?`, id)
	i, err := scanIncome(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Income{}, ErrNotFound
	}
	if err != nil {
		return expense.Income{}, err
	}
	return i, nil
}

// UpdateIncome overwrites the mutable fields of the income identified by i.ID and
// refreshes UpdatedAt to now, leaving its identity (ID, CreatedAt) untouched. It
// deliberately does *not* write linked_expense_id: the parent link is never
// re-parented on edit (ADR-0009), so the stored link is preserved regardless of
// what i carries. It returns ErrNotFound if no row has that id. On success i's
// UpdatedAt is set to the new instant.
func (s *Store) UpdateIncome(ctx context.Context, i *expense.Income) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE income
		 SET date = ?, amount = ?, description = ?, account = ?, raw_text = ?, updated_at = ?
		 WHERE id = ?`,
		i.Date.Format(dateLayout), i.Amount, i.Description,
		accountArg(i.Account), i.RawText, now.Format(tsLayout), i.ID)
	if err != nil {
		return fmt.Errorf("updating income %d: %w", i.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("updating income %d: %w", i.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	i.UpdatedAt = now
	return s.backupAfterWrite(ctx)
}

// DeleteIncome removes the income with the given id, returning ErrNotFound if
// none exists. It targets the income table only; cascade-deleting an expense's
// paybacks is the FK's job on Delete (ADR-0009), not this per-income delete.
func (s *Store) DeleteIncome(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM income WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting income %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting income %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.backupAfterWrite(ctx)
}

// scanIncome reads one row into an Income, translating SQLite's representation
// back to the domain types: a NULL account to a nil pointer, a NULL
// linked_expense_id to a nil *int64 (standalone), and the ISO-8601 TEXT
// date/timestamps to time.Time. It shares the scanner surface with scanExpense.
func scanIncome(sc scanner) (expense.Income, error) {
	var (
		i          expense.Income
		dateStr    string
		createdStr string
		updatedStr string
		account    sql.NullString
		linked     sql.NullInt64
	)
	if err := sc.Scan(&i.ID, &dateStr, &i.Amount, &i.Description,
		&account, &i.RawText, &linked, &createdStr, &updatedStr); err != nil {
		return expense.Income{}, err
	}

	var err error
	if i.Date, err = time.Parse(dateLayout, dateStr); err != nil {
		return expense.Income{}, fmt.Errorf("parsing date %q: %w", dateStr, err)
	}
	if i.CreatedAt, err = time.Parse(tsLayout, createdStr); err != nil {
		return expense.Income{}, fmt.Errorf("parsing created_at %q: %w", createdStr, err)
	}
	if i.UpdatedAt, err = time.Parse(tsLayout, updatedStr); err != nil {
		return expense.Income{}, fmt.Errorf("parsing updated_at %q: %w", updatedStr, err)
	}
	if account.Valid {
		a := account.String
		i.Account = &a
	}
	if linked.Valid {
		id := linked.Int64
		i.LinkedExpenseID = &id
	}
	return i, nil
}

// writeArgsIncome binds the mutable columns of the income INSERT in their
// `date … linked_expense_id` order, so a future UPDATE can share the same
// binding shape (as writeArgs does for expense). The caller appends its own
// trailing arguments (the timestamps). accountArg reuses the nullable-string
// binding; linkedArg does the same for the nullable link.
func writeArgsIncome(i *expense.Income) []any {
	return []any{
		i.Date.Format(dateLayout), i.Amount, i.Description,
		accountArg(i.Account), i.RawText, linkedArg(i.LinkedExpenseID),
	}
}

// linkedArg returns the value bound for the nullable linked_expense_id column:
// the id when set (a payback), or nil so the driver writes SQL NULL (a
// standalone income).
func linkedArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}
