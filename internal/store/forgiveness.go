package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// CreateForgiveness inserts f, assigning its ID and stamping CreatedAt. amount is
// the positive forgiven magnitude (the amount > 0 CHECK backs this). A
// Forgiveness is never edited, so there is no update (ADR-0016).
func (s *Store) CreateForgiveness(ctx context.Context, f *expense.Forgiveness) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO forgiveness (date, amount, created_at) VALUES (?, ?, ?)`,
		f.Date.Format(dateLayout), f.Amount, now.Format(tsLayout))
	if err != nil {
		return fmt.Errorf("inserting forgiveness: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	f.ID = id
	f.CreatedAt = now
	return nil
}

// ListForgivenesses returns every stored Forgiveness, newest first (date
// descending, then id descending), as List does for expenses.
func (s *Store) ListForgivenesses(ctx context.Context) ([]expense.Forgiveness, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, date, amount, created_at FROM forgiveness ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing forgivenesses: %w", err)
	}
	defer rows.Close()

	var out []expense.Forgiveness
	for rows.Next() {
		f, err := scanForgiveness(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing forgivenesses: %w", err)
	}
	return out, nil
}

// GetForgiveness returns the Forgiveness with the given id, or ErrNotFound.
func (s *Store) GetForgiveness(ctx context.Context, id int64) (expense.Forgiveness, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, date, amount, created_at FROM forgiveness WHERE id = ?`, id)
	f, err := scanForgiveness(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Forgiveness{}, ErrNotFound
	}
	return f, err
}

// DeleteForgiveness removes the Forgiveness with the given id, or returns
// ErrNotFound if none exists.
func (s *Store) DeleteForgiveness(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM forgiveness WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting forgiveness %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting forgiveness %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanForgiveness reads one row, translating the ISO-8601 TEXT date and
// timestamp back to time.Time.
func scanForgiveness(sc scanner) (expense.Forgiveness, error) {
	var (
		f          expense.Forgiveness
		dateStr    string
		createdStr string
	)
	if err := sc.Scan(&f.ID, &dateStr, &f.Amount, &createdStr); err != nil {
		return expense.Forgiveness{}, err
	}
	var err error
	if f.Date, err = time.Parse(dateLayout, dateStr); err != nil {
		return expense.Forgiveness{}, fmt.Errorf("parsing date %q: %w", dateStr, err)
	}
	if f.CreatedAt, err = time.Parse(tsLayout, createdStr); err != nil {
		return expense.Forgiveness{}, fmt.Errorf("parsing created_at %q: %w", createdStr, err)
	}
	return f, nil
}
