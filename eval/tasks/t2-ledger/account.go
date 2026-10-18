package task

// Account holds a balance in whole units.
type Account struct {
	balance int
}

// NewAccount opens an account with the given balance.
func NewAccount(opening int) *Account {
	return &Account{balance: opening}
}

// Balance reports the current balance.
func (a *Account) Balance() int { return a.balance }

// Deposit adds n to the balance. A negative n subtracts, which is how a
// withdrawal is expressed today.
func (a *Account) Deposit(n int) { a.balance += n }
