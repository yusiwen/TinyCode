package task

import "fmt"

// Order reserves units on behalf of a customer.
type Order struct {
	stock *Stock
}

// NewOrder returns an order book over one stock.
func NewOrder(stock *Stock) *Order { return &Order{stock: stock} }

// Place reserves n units and reports a failure with the order's size in it.
func (o *Order) Place(n int) error {
	if err := o.stock.Reserve(n); err != nil {
		return fmt.Errorf("order %d: %w", n, err)
	}
	return nil
}
