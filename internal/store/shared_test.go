package store_test

import (
	"context"
	"testing"

	"github.com/emepetres/life-ledger/internal/expense"
)

// A Shared expense keeps who paid it across Create/Get/List/Update.
func TestSharedExpenseRoundTripsPayer(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	e := expense.NewExpense(baseTime, 3000, "cena", nil, true, "30 cena")
	e.PaidBy = expense.Carla
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Shared || got.PaidBy != expense.Carla || got.Settlement {
		t.Errorf("got Shared=%v PaidBy=%q Settlement=%v, want shared, carla, no settlement", got.Shared, got.PaidBy, got.Settlement)
	}

	got.Amount = 3500
	if err := s.Update(ctx, &got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].PaidBy != expense.Carla || !list[0].Shared || list[0].Amount != 3500 {
		t.Errorf("after update: %+v", list)
	}
}

// A Settlement round-trips on both tables.
func TestSettlementRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	e := expense.NewExpense(baseTime, 4000, "devolución", nil, false, "=40 devolución")
	e.Settlement = true
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	i := expense.NewIncome(baseTime, 2500, "devolución", nil, nil, "=25 devolución")
	i.Settlement = true
	if err := s.CreateIncome(ctx, i); err != nil {
		t.Fatalf("CreateIncome: %v", err)
	}

	gotE, _ := s.Get(ctx, e.ID)
	if !gotE.Settlement || gotE.Shared || gotE.PaidBy != expense.Javi {
		t.Errorf("expense: %+v", gotE)
	}
	gotI, _ := s.GetIncome(ctx, i.ID)
	if !gotI.Settlement {
		t.Errorf("income: %+v", gotI)
	}
	gotI.Amount = 3000
	if err := s.UpdateIncome(ctx, &gotI); err != nil {
		t.Fatalf("UpdateIncome: %v", err)
	}
	again, _ := s.GetIncome(ctx, i.ID)
	if !again.Settlement || again.Amount != 3000 {
		t.Errorf("income after update: %+v", again)
	}
	incomes, _ := s.ListIncomes(ctx)
	if len(incomes) != 1 || !incomes[0].Settlement {
		t.Errorf("ListIncomes: %+v", incomes)
	}
}
