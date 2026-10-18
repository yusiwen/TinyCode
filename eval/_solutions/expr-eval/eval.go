package task

import "fmt"

// Evaluate computes the value of an arithmetic expression over integers.
//
// A recursive-descent parser: expr := term (('+'|'-') term)*,
// term := unary (('*'|'/') unary)*, unary := '-' unary | primary,
// primary := number | '(' expr ')'.
func Evaluate(expr string) (int, error) {
	p := &parser{src: []rune(expr)}
	value, err := p.expr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if !p.done() {
		return 0, fmt.Errorf("evaluate: unexpected %q at %d", string(p.peek()), p.at)
	}
	return value, nil
}

type parser struct {
	src []rune
	at  int
}

func (p *parser) done() bool { return p.at >= len(p.src) }

func (p *parser) peek() rune {
	if p.done() {
		return 0
	}
	return p.src[p.at]
}

func (p *parser) skipSpace() {
	for !p.done() && (p.src[p.at] == ' ' || p.src[p.at] == '\t') {
		p.at++
	}
}

func (p *parser) expr() (int, error) {
	value, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.done() {
			return value, nil
		}
		op := p.peek()
		if op != '+' && op != '-' {
			return value, nil
		}
		p.at++
		right, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			value += right
		} else {
			value -= right
		}
	}
}

func (p *parser) term() (int, error) {
	value, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.done() {
			return value, nil
		}
		op := p.peek()
		if op != '*' && op != '/' {
			return value, nil
		}
		p.at++
		right, err := p.unary()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			value *= right
			continue
		}
		if right == 0 {
			return 0, fmt.Errorf("evaluate: division by zero")
		}
		value /= right
	}
}

func (p *parser) unary() (int, error) {
	p.skipSpace()
	if p.done() {
		return 0, fmt.Errorf("evaluate: unexpected end of expression")
	}
	if p.peek() == '-' {
		p.at++
		value, err := p.unary()
		if err != nil {
			return 0, err
		}
		return -value, nil
	}
	return p.primary()
}

func (p *parser) primary() (int, error) {
	p.skipSpace()
	if p.done() {
		return 0, fmt.Errorf("evaluate: unexpected end of expression")
	}
	if p.peek() == '(' {
		p.at++
		value, err := p.expr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.done() || p.peek() != ')' {
			return 0, fmt.Errorf("evaluate: missing )")
		}
		p.at++
		return value, nil
	}
	start := p.at
	for !p.done() && p.peek() >= '0' && p.peek() <= '9' {
		p.at++
	}
	if start == p.at {
		return 0, fmt.Errorf("evaluate: expected a number at %d", start)
	}
	value := 0
	for _, digit := range p.src[start:p.at] {
		value = value*10 + int(digit-'0')
	}
	return value, nil
}
