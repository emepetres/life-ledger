package expense

// Balance is the Shared balance between the two Users (CONTEXT.md): who owes the
// other, and how much. A zero Amount means they are even, and Debtor is then
// empty.
type Balance struct {
	// Debtor is the User who owes the other; empty when Amount is zero.
	Debtor Payer
	// Amount is what the Debtor owes, in minor units, never negative.
	Amount int
}

// SharedBalance derives the Shared balance from every stored expense and income —
// it never reads anything else and stores nothing. Each Shared expense costs both
// Users half of its Net cost (the full Amount less its linked paybacks, which are
// assumed received by the Payer), so the other User owes the Payer that half;
// Settlements then move the balance back: Javi's (an expense) reduces what Javi
// owes Carla, Carla's (an income) reduces what Carla owes Javi.
//
// The arithmetic runs in doubled units so an odd cent never truncates; the
// result is rounded to the nearest cent, half away from zero.
func SharedBalance(expenses []Expense, incomes []Income) Balance {
	paybacks := make(map[int64]int)
	for _, i := range incomes {
		if i.LinkedExpenseID != nil {
			paybacks[*i.LinkedExpenseID] += i.Amount
		}
	}

	// owedByJavi2 is what Javi owes Carla, doubled (negative: Carla owes Javi).
	owedByJavi2 := 0
	for _, e := range expenses {
		switch {
		case e.Settlement:
			owedByJavi2 -= 2 * e.Amount
		case e.Shared:
			net := e.Amount - paybacks[e.ID]
			if e.PaidBy == Carla {
				owedByJavi2 += net
			} else {
				owedByJavi2 -= net
			}
		}
	}
	for _, i := range incomes {
		if i.Settlement {
			owedByJavi2 += 2 * i.Amount
		}
	}

	debtor, magnitude := Javi, owedByJavi2
	if owedByJavi2 < 0 {
		debtor, magnitude = Carla, -owedByJavi2
	}
	amount := (magnitude + 1) / 2
	if amount == 0 {
		return Balance{}
	}
	return Balance{Debtor: debtor, Amount: amount}
}
