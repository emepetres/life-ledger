package expense_test

import (
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

var sharedToday = time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)

// A line entered in the Shared tab becomes a Shared expense paid by whoever
// typed it.
func TestAddSharedMakesSharedExpensePaidByTyper(t *testing.T) {
	for _, payer := range []expense.Payer{expense.Javi, expense.Carla} {
		sub := expense.Submit(expense.AddShared(payer), "30 cena", sharedToday)
		if !sub.OK() || sub.Expense == nil {
			t.Fatalf("payer %s: want an expense, got %+v", payer, sub)
		}
		if !sub.Expense.Shared || sub.Expense.PaidBy != payer || sub.Expense.Settlement {
			t.Errorf("payer %s: got Shared=%v PaidBy=%s Settlement=%v", payer,
				sub.Expense.Shared, sub.Expense.PaidBy, sub.Expense.Settlement)
		}
		if sub.Expense.Amount != 3000 {
			t.Errorf("Amount = %d, want 3000", sub.Expense.Amount)
		}
	}
}

// A plain (ordinary-tab) add is never shared and is paid by Javi, the only
// user of that tab.
func TestPlainAddIsOrdinaryPaidByJavi(t *testing.T) {
	sub := expense.Submit(expense.Add(), "30 cena", sharedToday)
	if sub.Expense == nil || sub.Expense.Shared || sub.Expense.PaidBy != expense.Javi {
		t.Fatalf("got %+v, want an ordinary expense paid by javi", sub.Expense)
	}
}

// The '*' marker is gone: it is just a word of the description now.
func TestStarIsNoLongerAMarker(t *testing.T) {
	sub := expense.Submit(expense.Add(), "30 cena *", sharedToday)
	if sub.Expense == nil || sub.Expense.Shared {
		t.Fatalf("got %+v, want an ordinary expense", sub.Expense)
	}
	if sub.Expense.Description != "cena *" {
		t.Errorf("Description = %q, want %q", sub.Expense.Description, "cena *")
	}
}

// A '=' line in the Shared tab is a Settlement paid by the typist: Javi's is an
// expense in his real totals, Carla's an income. Neither is Shared (not halved).
func TestAddSharedSettlementByPayer(t *testing.T) {
	javi := expense.Submit(expense.AddShared(expense.Javi), "=40 devolución", sharedToday)
	if javi.Expense == nil || javi.Income != nil || !javi.Expense.Settlement || javi.Expense.Shared || javi.Expense.PaidBy != expense.Javi {
		t.Errorf("javi: got Expense %+v Income %+v, want a non-shared settlement expense paid by javi", javi.Expense, javi.Income)
	}
	carla := expense.Submit(expense.AddShared(expense.Carla), "=40 devolución", sharedToday)
	if carla.Income == nil || carla.Expense != nil || !carla.Income.Settlement || carla.Income.LinkedExpenseID != nil {
		t.Errorf("carla: got Expense %+v Income %+v, want a settlement income", carla.Expense, carla.Income)
	}
}

// '+' incomes don't exist in the Shared tab, and '=' settlements don't exist in
// the ordinary one.
func TestTabsRefuseTheOtherTabsSigils(t *testing.T) {
	inc := expense.Submit(expense.AddShared(expense.Javi), "+30 reembolso", sharedToday)
	if inc.OK() || len(inc.Parsed.Errors) != 1 || inc.Parsed.Errors[0] != expense.ErrIncomeInShared {
		t.Errorf("+ in shared: errors = %v, want [ErrIncomeInShared]", inc.Parsed.Errors)
	}
	set := expense.Submit(expense.Add(), "=30 devolución", sharedToday)
	if set.OK() || len(set.Parsed.Errors) != 1 || set.Parsed.Errors[0] != expense.ErrSettlementOutsideShared {
		t.Errorf("= in ordinary: errors = %v, want [ErrSettlementOutsideShared]", set.Parsed.Errors)
	}
}

// Editing a Shared expense keeps it Shared and keeps who paid it, whoever edits.
func TestEditSharedExpenseKeepsSharedAndPayer(t *testing.T) {
	existing := expense.NewExpense(sharedToday, 3000, "cena", nil, true, "30 cena")
	existing.ID = 5
	existing.PaidBy = expense.Carla
	in, ok := expense.EditExpense(*existing, sharedToday)
	if !ok {
		t.Fatal("EditExpense refused a recent expense")
	}
	sub := expense.Submit(in, "35 cena 20/7", sharedToday)
	if sub.Expense == nil || !sub.Expense.Shared || sub.Expense.PaidBy != expense.Carla || sub.Expense.ID != 5 {
		t.Fatalf("got %+v, want shared, paid by carla, id 5", sub.Expense)
	}
}

// An edit keeps the record's kind: a settlement stays a settlement (and its
// payer), a shared expense can't turn into one.
func TestEditSettlementKeepsKind(t *testing.T) {
	s := expense.NewExpense(sharedToday, 4000, "devolución", nil, false, "=40 devolución")
	s.ID = 9
	s.Settlement = true
	in, _ := expense.EditExpense(*s, sharedToday)

	ok := expense.Submit(in, "=45 devolución", sharedToday)
	if ok.Expense == nil || !ok.Expense.Settlement || ok.Expense.PaidBy != expense.Javi || ok.Expense.Amount != 4500 {
		t.Errorf("edit settlement: got %+v", ok.Expense)
	}
	bad := expense.Submit(in, "45 devolución", sharedToday)
	if bad.OK() || bad.Parsed.Errors[0] != expense.ErrMustBeSettlement {
		t.Errorf("settlement edited to a plain line: errors = %v, want [ErrMustBeSettlement]", bad.Parsed.Errors)
	}

	incS := expense.NewIncome(sharedToday, 4000, "devolución", nil, nil, "=40 devolución")
	incS.ID = 10
	incS.Settlement = true
	inI, _ := expense.EditIncome(*incS, sharedToday)
	res := expense.Submit(inI, "=50 devolución", sharedToday)
	if res.Income == nil || !res.Income.Settlement || res.Income.ID != 10 {
		t.Errorf("edit settlement income: got %+v", res.Income)
	}
}

// An ordinary expense stays ordinary when edited.
func TestEditOrdinaryExpenseStaysOrdinary(t *testing.T) {
	existing := expense.NewExpense(sharedToday, 3000, "cena", nil, false, "30 cena")
	existing.ID = 5
	in, _ := expense.EditExpense(*existing, sharedToday)
	sub := expense.Submit(in, "35 cena", sharedToday)
	if sub.Expense == nil || sub.Expense.Shared || sub.Expense.PaidBy != expense.Javi {
		t.Fatalf("got %+v, want ordinary paid by javi", sub.Expense)
	}
}

// ---- Shared balance ----------------------------------------------------------

func sharedExp(id int64, amount int, by expense.Payer) expense.Expense {
	e := expense.NewExpense(sharedToday, amount, "x", nil, true, "")
	e.ID, e.PaidBy = id, by
	return *e
}

func payback(amount int, parent int64) expense.Income {
	i := expense.NewIncome(sharedToday, amount, "p", nil, &parent, "")
	return *i
}

func TestSharedBalance(t *testing.T) {
	settleJavi := expense.NewExpense(sharedToday, 1000, "s", nil, false, "")
	settleJavi.Settlement = true
	settleCarla := expense.NewIncome(sharedToday, 500, "s", nil, nil, "")
	settleCarla.Settlement = true
	ordinary := expense.NewExpense(sharedToday, 99999, "solo mío", nil, false, "")
	standalone := expense.NewIncome(sharedToday, 99999, "nómina", nil, nil, "")

	tests := []struct {
		name       string
		expenses      []expense.Expense
		incomes       []expense.Income
		forgivenesses []expense.Forgiveness
		wantDebtor    expense.Payer
		wantAmount    int
	}{
		{name: "nothing: even", wantAmount: 0},
		{
			name:          "forgiving all of what carla owes: javi paid 100, forgave 50 => even",
			expenses:      []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			forgivenesses: []expense.Forgiveness{*expense.NewForgiveness(sharedToday, 5000)},
		},
		{
			name:          "forgiving the whole displayed balance of an odd cent leaves it even: javi paid 0.01 => carla owes 0.01, forgave 0.01",
			expenses:      []expense.Expense{sharedExp(1, 1, expense.Javi)},
			forgivenesses: []expense.Forgiveness{*expense.NewForgiveness(sharedToday, 1)},
		},
		{
			name:          "forgiving the whole displayed balance of 1.01 leaves it even: carla owes 0.51, forgave 0.51",
			expenses:      []expense.Expense{sharedExp(1, 101, expense.Javi)},
			forgivenesses: []expense.Forgiveness{*expense.NewForgiveness(sharedToday, 51)},
		},
		{
			name:          "partial forgiveness: javi paid 100, forgave 20 => carla owes 30",
			expenses:      []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			forgivenesses: []expense.Forgiveness{*expense.NewForgiveness(sharedToday, 2000)},
			wantDebtor:    expense.Carla, wantAmount: 3000,
		},
		{
			name:          "forgiveness on top of a carla settlement: javi paid 100, carla settled 5, forgave 45 => even",
			expenses:      []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			incomes:       []expense.Income{*settleCarla},
			forgivenesses: []expense.Forgiveness{*expense.NewForgiveness(sharedToday, 4500)},
		},
		{
			name:       "javi paid 100: carla owes 50",
			expenses:   []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			wantDebtor: expense.Carla, wantAmount: 5000,
		},
		{
			name:       "carla paid 100: javi owes 50",
			expenses:   []expense.Expense{sharedExp(1, 10000, expense.Carla)},
			wantDebtor: expense.Javi, wantAmount: 5000,
		},
		{
			name:     "each paid 100: even",
			expenses: []expense.Expense{sharedExp(1, 10000, expense.Javi), sharedExp(2, 10000, expense.Carla)},
		},
		{
			name:       "paybacks reduce the payer's net cost: 100 paid, 40 back => carla owes 30",
			expenses:   []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			incomes:    []expense.Income{payback(4000, 1)},
			wantDebtor: expense.Carla, wantAmount: 3000,
		},
		{
			name:       "javi's settlement of 10 reduces what javi owes: carla paid 100 => javi owes 40",
			expenses:   []expense.Expense{sharedExp(1, 10000, expense.Carla), *settleJavi},
			wantDebtor: expense.Javi, wantAmount: 4000,
		},
		{
			name:       "carla's settlement of 5 reduces what carla owes: javi paid 100 => carla owes 45",
			expenses:   []expense.Expense{sharedExp(1, 10000, expense.Javi)},
			incomes:    []expense.Income{*settleCarla},
			wantDebtor: expense.Carla, wantAmount: 4500,
		},
		{
			name:       "overpaying a settlement flips the debt: javi paid 10 owing nothing => carla owes... javi is owed back",
			expenses:   []expense.Expense{*settleJavi},
			wantDebtor: expense.Carla, wantAmount: 1000,
		},
		{
			name:       "ordinary expenses and standalone incomes are ignored",
			expenses:   []expense.Expense{*ordinary, sharedExp(1, 10000, expense.Javi)},
			incomes:    []expense.Income{*standalone},
			wantDebtor: expense.Carla, wantAmount: 5000,
		},
		{
			name:       "an odd cent rounds to the nearest cent (half up): 1.01 paid => other owes 0.51",
			expenses:   []expense.Expense{sharedExp(1, 101, expense.Javi)},
			wantDebtor: expense.Carla, wantAmount: 51,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expense.SharedBalance(tt.expenses, tt.incomes, tt.forgivenesses)
			if got.Amount != tt.wantAmount || (tt.wantAmount != 0 && got.Debtor != tt.wantDebtor) {
				t.Errorf("SharedBalance = {debtor %q, amount %d}, want {debtor %q, amount %d}",
					got.Debtor, got.Amount, tt.wantDebtor, tt.wantAmount)
			}
		})
	}
}
