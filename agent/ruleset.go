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

// TranslateToolLists converts the legacy per-agent AllowedTools/DeniedTools
// lists into permission rules on top of base.
//
// Every built-in agent ships a Ruleset and the ruleset takes precedence over
// the two lists, so without this translation a configured tool list would be
// silently ignored. A non-empty allowed list acts as a whitelist (deny
// everything, then allow the listed tools). The base ruleset's *named* deny
// rules are re-applied on top of the whitelist, so a configured whitelist can
// never silently re-grant a tool the agent explicitly denies (for example
// `task` for the general sub-agent). An empty-but-non-nil allowed list means
// "no override" rather than "deny everything". Denied names are appended last,
// so a tool named in both lists ends up denied.
func TranslateToolLists(base Ruleset, allowed, denied []string) Ruleset {
	rules := append(Ruleset(nil), base...)

	if len(allowed) > 0 {
		whitelist := Ruleset{{Action: "*", Resource: "*", Effect: EffectDeny}}
		for _, name := range allowed {
			whitelist = append(whitelist, Rule{Action: name, Resource: "*", Effect: EffectAllow})
		}
		for _, r := range base {
			// Only named denies are preserved: a blanket "*" deny is what the
			// whitelist is meant to replace.
			if r.Effect == EffectDeny && r.Action != "*" {
				whitelist = append(whitelist, r)
			}
		}
		rules = whitelist
	}

	for _, name := range denied {
		rules = append(rules, Rule{Action: name, Resource: "*", Effect: EffectDeny})
	}
	return rules
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
