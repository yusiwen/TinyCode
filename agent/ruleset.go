package agent

// Effect is the result of a permission rule evaluation.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

// Rule defines a permission rule: an action on a resource produces an effect.
type Rule struct {
	Action   string // tool name or "*" wildcard
	Resource string // resource pattern or "*" wildcard
	Effect   Effect // allow or deny
}

// Ruleset is an ordered list of rules evaluated last-match-wins.
type Ruleset []Rule

// Evaluate applies last-match-wins over the given rules.
// Unmatched rules default to EffectAllow.
func Evaluate(action, resource string, rules ...Rule) Effect {
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		if wildcardMatch(r.Action, action) && wildcardMatch(r.Resource, resource) {
			return r.Effect
		}
	}
	return EffectAllow
}

// FilterTools returns only the tools whose names are not denied by the ruleset.
func FilterTools(allTools []string, rules ...Rule) []string {
	var out []string
	for _, name := range allTools {
		if Evaluate(name, "*", rules...) != EffectDeny {
			out = append(out, name)
		}
	}
	return out
}

// wildcardMatch reports whether the pattern matches the value.
// Pattern supports '*' (any sequence) and '?' (any single char).
// Multi-segment patterns like "bash:ls *" match against action:resource.
// The function is simple — it does not handle escaping or complex patterns.
func wildcardMatch(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == value {
		return true
	}
	// Simple glob matching with * and ?
	pi, vi := 0, 0
	nextPi, nextVi := -1, -1
	for vi < len(value) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == value[vi]) {
			pi++
			vi++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			nextPi = pi + 1
			nextVi = vi
			pi++
			continue
		}
		if nextPi >= 0 {
			pi = nextPi
			vi = nextVi
			nextVi++
			continue
		}
		return false
	}
	// Skip trailing *
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi >= len(pattern)
}
