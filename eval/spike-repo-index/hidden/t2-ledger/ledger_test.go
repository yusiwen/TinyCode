package task

import "testing"

func TestTransferMovesFunds(t *testing.T) {
	ledger := NewLedger()
	ledger.Open("checking", 100)
	ledger.Open("savings", 0)

	if err := ledger.Transfer("checking", "savings", 30); err != nil {
		t.Fatalf("Transfer = %v, want nil", err)
	}
	checking, _ := ledger.Account("checking")
	savings, _ := ledger.Account("savings")
	if checking.Balance() != 70 || savings.Balance() != 30 {
		t.Fatalf("balances = %d/%d, want 70/30", checking.Balance(), savings.Balance())
	}
}

func TestTransferRefusesAnOverdraft(t *testing.T) {
	ledger := NewLedger()
	ledger.Open("checking", 100)
	ledger.Open("savings", 0)

	if err := ledger.Transfer("checking", "savings", 1000); err == nil {
		t.Fatal("Transfer accepted an overdraft")
	}
	checking, _ := ledger.Account("checking")
	savings, _ := ledger.Account("savings")
	if checking.Balance() != 100 {
		t.Errorf("a refused transfer moved the source: balance = %d, want 100", checking.Balance())
	}
	if savings.Balance() != 0 {
		t.Errorf("a refused transfer moved the target: balance = %d, want 0", savings.Balance())
	}
}

func TestTransferRefusesUnknownAccounts(t *testing.T) {
	ledger := NewLedger()
	ledger.Open("checking", 10)
	if err := ledger.Transfer("checking", "nowhere", 1); err == nil {
		t.Fatal("Transfer to an unknown account succeeded")
	}
	if err := ledger.Transfer("nowhere", "checking", 1); err == nil {
		t.Fatal("Transfer from an unknown account succeeded")
	}
}
