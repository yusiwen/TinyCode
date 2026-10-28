package task

import "testing"

func TestReserveAndAvailable(t *testing.T) {
	stock := NewStock(10)
	if err := stock.Reserve(4); err != nil {
		t.Fatalf("Reserve(4) = %v", err)
	}
	if got := stock.Available(); got != 6 {
		t.Errorf("Available = %d, want 6", got)
	}
	if err := stock.Reserve(7); err == nil {
		t.Error("a reservation larger than the stock succeeded")
	}
	if got := stock.Available(); got != 6 {
		t.Errorf("a refused reservation changed Available to %d, want 6", got)
	}
}

func TestOrderWrapsTheRefusal(t *testing.T) {
	order := NewOrder(NewStock(2))
	if err := order.Place(3); err == nil {
		t.Fatal("Place accepted an oversized order")
	}
	if err := order.Place(2); err != nil {
		t.Fatalf("Place(2) after a refused Place(3) = %v", err)
	}
}
