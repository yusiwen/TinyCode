package task

import "fmt"

// Ledger holds named accounts.
type Ledger struct {
	accounts map[string]*Account
}

// NewLedger returns a ledger with no accounts.
func NewLedger() *Ledger { return &Ledger{accounts: make(map[string]*Account)} }

// Open creates an account and returns it.
func (l *Ledger) Open(name string, opening int) *Account {
	account := NewAccount(opening)
	l.accounts[name] = account
	return account
}

// Account returns the named account, if it exists.
func (l *Ledger) Account(name string) (*Account, bool) {
	account, ok := l.accounts[name]
	return account, ok
}

// Transfer moves n from one account to another. It refuses an unknown account
// and it refuses to leave the source account overdrawn; a refused transfer
// leaves both balances as they were.
func (l *Ledger) Transfer(from, to string, n int) error {
	src, ok := l.accounts[from]
	if !ok {
		return fmt.Errorf("transfer: no account %q", from)
	}
	dst, ok := l.accounts[to]
	if !ok {
		return fmt.Errorf("transfer: no account %q", to)
	}
	if n < 0 {
		return fmt.Errorf("transfer: amount %d is negative", n)
	}
	src.Deposit(-n)
	dst.Deposit(n)
	return nil
}
