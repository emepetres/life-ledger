package server

import (
	"log"
	"net/http"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// forgiveDateLayout is the value an HTML date input posts.
const forgiveDateLayout = "2006-01-02"

// handleForgive records a Forgiveness: Javi waiving what Carla owes him on the
// Shared balance, without money moving (ADR-0016). Carla gets a 404, as for any
// route outside her scope. Anything Javi cannot forgive — nothing owed, or Javi is
// the debtor, a bad amount (zero, unreadable, above the balance) or a bad or
// future date — is a 422 back on the Shared tab with a note, storing nothing. The
// amount defaults to the whole balance and the date to today.
func (s *Server) handleForgive(w http.ResponseWriter, r *http.Request) {
	if isCarla(r) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	expenses, err := s.store.List(ctx)
	if err != nil {
		log.Printf("listing expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	incomes, err := s.store.ListIncomes(ctx)
	if err != nil {
		log.Printf("listing incomes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	forgivenesses, err := s.store.ListForgivenesses(ctx)
	if err != nil {
		log.Printf("listing forgivenesses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	balance := expense.SharedBalance(expenses, incomes, forgivenesses)

	f, refusal := s.newForgiveness(r, balance)
	if refusal != "" {
		s.renderHome(w, r, http.StatusUnprocessableEntity, homeView{
			Intent: expense.AddShared(userPayer(r)),
			Notice: refusal,
		})
		return
	}
	if err := s.store.CreateForgiveness(ctx, f); err != nil {
		log.Printf("creating forgiveness: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, sharedPath, http.StatusSeeOther)
}

// newForgiveness builds the Forgiveness the form asks for against the current
// balance, or returns the reason it cannot be made.
func (s *Server) newForgiveness(r *http.Request, balance expense.Balance) (*expense.Forgiveness, string) {
	if balance.Amount == 0 || balance.Debtor != expense.Carla {
		return nil, "Carla doesn't owe you anything, so there is nothing to forgive."
	}

	amount := balance.Amount
	if raw := r.PostFormValue("amount"); raw != "" {
		var ok bool
		amount, ok = expense.ParseAmount(raw)
		if !ok || amount == 0 {
			return nil, "The amount to forgive must be a number above zero."
		}
		if amount > balance.Amount {
			return nil, "You can't forgive more than Carla owes you (" + formatEuro(balance.Amount) + ")."
		}
	}

	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	date := today
	if raw := r.PostFormValue("date"); raw != "" {
		var err error
		date, err = time.ParseInLocation(forgiveDateLayout, raw, now.Location())
		if err != nil {
			return nil, "That date isn't valid."
		}
		if date.After(today) {
			return nil, "A forgiveness can't be dated in the future."
		}
	}
	return expense.NewForgiveness(date, amount), ""
}

// handleDeleteForgiveness undoes a Forgiveness, bringing the debt back. Only Javi
// made it, so only Javi can undo it: Carla gets a 404 like any route outside her
// scope. An unknown id is a 404 too.
func (s *Server) handleDeleteForgiveness(w http.ResponseWriter, r *http.Request) {
	if isCarla(r) {
		http.NotFound(w, r)
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if respondStoreErr(w, r, "deleting forgiveness", id, s.store.DeleteForgiveness(r.Context(), id)) {
		return
	}
	http.Redirect(w, r, sharedPath, http.StatusSeeOther)
}
