package task

import "fmt"

// Evaluate computes the value of an arithmetic expression over integers.
//
// Supported: + - * /, parentheses, unary minus, and spaces anywhere. *
// and / bind tighter than + and -, and both are left-associative. Division
// truncates towards zero. Anything malformed — an empty expression, a dangling
// operator, an unbalanced parenthesis, a division by zero — is an error.
func Evaluate(expr string) (int, error) {
	return 0, fmt.Errorf("evaluate: not implemented")
}
