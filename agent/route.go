package agent

import (
	"fmt"
	"strings"
)

// ParseRoute splits a configured "<route>/<model>" value into its two halves.
//
// The cut is on the first separator, because a model name may itself contain one:
// "openrouter/anthropic/claude-sonnet-4" names the openrouter route and the model
// "anthropic/claude-sonnet-4". A value with no separator is a bare model override
// and keeps the route empty, which means "the session's active provider".
//
// The route half is only split, not validated: parsing cannot know which names are
// configured. The caller resolves it against the registry, and a prefix that names
// no route is an error rather than a silent fallback — the whole point of A5 is
// that the provider half stops being ignored.
func ParseRoute(value string) (route, model string, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", nil
	}
	before, after, found := strings.Cut(value, "/")
	if !found {
		return "", value, nil
	}
	if before == "" {
		return "", "", fmt.Errorf("model %q: the route before %q is empty", value, "/")
	}
	if after == "" {
		return "", "", fmt.Errorf("model %q: the model after %q is empty", value, "/")
	}
	return before, after, nil
}
