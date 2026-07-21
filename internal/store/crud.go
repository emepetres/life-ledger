package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// ErrNotFound is returned by Get, Update, and Delete when no expense has the
// requested id.
var ErrNotFound = errors.New("expense not found")

const (
	// dateLayout is the ISO-8601 calendar-day format for the date column.
	dateLayout = "2006-01-02"
	// tsLayout is the ISO-8601 timestamp format for created_at / updated_at. The
	// stamped values are UTC, so they serialise with a trailing 'Z'.
	tsLayout = time.RFC3339
)

// columns is the fixed column list every read selects, in scan order.
const columns = "id, date, amount, description, split, account, raw_text, created_at, updated_at"

// Create inserts e as a new expense, assigning its ID and stamping CreatedAt and
// UpdatedAt to the same instant. The full Amount is stored regardless of Split
// (ADR-0001); a nil Account is written as SQL NULL, never an empty string.
func (s *Store) Create(ctx context.Context, e *expense.Expense) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO expense (date, amount, description, split, account, raw_text, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Date.Format(dateLayout), e.Amount, e.Description, boolToInt(e.Split),
		accountArg(e.Account), e.RawText, now.Format(tsLayout), now.Format(tsLayout))
	if err != nil {
		return fmt.Errorf("inserting expense: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	e.ID = id
	e.CreatedAt = now
	e.UpdatedAt = now
	return nil
}

// List returns every stored expense, newest first — ordered by date descending
// then id descending, so entries within a day come back most-recently-added
// first. This is the natural order for the day-grouped list view (ADR-0003).
func (s *Store) List(ctx context.Context) ([]expense.Expense, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+columns+` FROM expense ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing expenses: %w", err)
	}
	defer rows.Close()

	var out []expense.Expense
	for rows.Next() {
		e, err := scanExpense(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing expenses: %w", err)
	}
	return out, nil
}

// Get returns the expense with the given id, or ErrNotFound if none exists.
func (s *Store) Get(ctx context.Context, id int64) (expense.Expense, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM expense WHERE id = ?`, id)
	e, err := scanExpense(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Expense{}, ErrNotFound
	}
	if err != nil {
		return expense.Expense{}, err
	}
	return e, nil
}

// Update overwrites the mutable fields of the expense identified by e.ID and
// refreshes UpdatedAt to now, leaving its identity (ID, CreatedAt) untouched
// (ADR-0001). It returns ErrNotFound if no row has that id. On success e's
// UpdatedAt is set to the new instant.
func (s *Store) Update(ctx context.Context, e *expense.Expense) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE expense
		 SET date = ?, amount = ?, description = ?, split = ?, account = ?, raw_text = ?, updated_at = ?
		 WHERE id = ?`,
		e.Date.Format(dateLayout), e.Amount, e.Description, boolToInt(e.Split),
		accountArg(e.Account), e.RawText, now.Format(tsLayout), e.ID)
	if err != nil {
		return fmt.Errorf("updating expense %d: %w", e.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("updating expense %d: %w", e.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	e.UpdatedAt = now
	return nil
}

// Delete removes the expense with the given id, returning ErrNotFound if none
// exists.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM expense WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting expense %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting expense %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner is the shared Scan surface of *sql.Row and *sql.Rows, so a single
// scanExpense serves both the single-row and list reads.
type scanner interface {
	Scan(dest ...any) error
}

// scanExpense reads one row into an Expense, translating SQLite's representation
// back to the domain types: split 0/1 to bool, a NULL account to a nil pointer,
// and the ISO-8601 TEXT date/timestamps to time.Time.
func scanExpense(sc scanner) (expense.Expense, error) {
	var (
		e          expense.Expense
		dateStr    string
		createdStr string
		updatedStr string
		splitInt   int
		account    sql.NullString
	)
	if err := sc.Scan(&e.ID, &dateStr, &e.Amount, &e.Description, &splitInt,
		&account, &e.RawText, &createdStr, &updatedStr); err != nil {
		return expense.Expense{}, err
	}

	var err error
	if e.Date, err = time.Parse(dateLayout, dateStr); err != nil {
		return expense.Expense{}, fmt.Errorf("parsing date %q: %w", dateStr, err)
	}
	if e.CreatedAt, err = time.Parse(tsLayout, createdStr); err != nil {
		return expense.Expense{}, fmt.Errorf("parsing created_at %q: %w", createdStr, err)
	}
	if e.UpdatedAt, err = time.Parse(tsLayout, updatedStr); err != nil {
		return expense.Expense{}, fmt.Errorf("parsing updated_at %q: %w", updatedStr, err)
	}
	e.Split = splitInt != 0
	if account.Valid {
		a := account.String
		e.Account = &a
	}
	return e, nil
}

// boolToInt maps a Go bool to the 0/1 integer SQLite stores for split.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// accountArg returns the value bound for the nullable account column: the string
// value when set, or nil so the driver writes SQL NULL.
func accountArg(a *string) any {
	if a == nil {
		return nil
	}
	return *a
}
