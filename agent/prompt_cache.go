package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// PromptCacheMode is how a route is told to cache its prompt prefix.
//
// The default providers (DeepSeek, OpenAI, Gemini) cache an unchanged prefix
// automatically; there is no request field to add for them, and this mode only
// decides which hints TinyCode sends. Anthropic-style routes (including
// Anthropic models routed through OpenRouter), Alibaba and Gemini-over-OpenRouter
// cache only content whose block carries a cache_control breakpoint, so they need
// "explicit".
type PromptCacheMode string

const (
	// PromptCacheOff sends no hints. It does not turn off a provider's own
	// automatic caching, because none of them exposes a per-request switch for it.
	PromptCacheOff PromptCacheMode = "off"
	// PromptCacheAuto sends a stable prompt_cache_key, which OpenAI uses to improve
	// cache routing before GPT-5.6 and OpenRouter uses as its sticky-routing key.
	// The provider's automatic caching does the actual work.
	PromptCacheAuto PromptCacheMode = "auto"
	// PromptCacheExplicit sends the key plus a cache_control breakpoint after the
	// leading system message, which is the end of the tools + system prefix.
	PromptCacheExplicit PromptCacheMode = "explicit"
)

// PromptCachePolicy is a resolved prompt_cache configuration entry.
//
// TTL is empty for the provider default (five minutes for Anthropic) and "1h"
// only when the user asked for the longer lifetime. The zero value means off, so
// a provider built without a policy sends nothing.
type PromptCachePolicy struct {
	Mode PromptCacheMode
	TTL  string
}

// ResolvePromptCache validates a prompt_cache configuration entry.
//
// A value that cannot be honoured is an error rather than a warning: a mode that
// silently means "off" leaves the user believing tokens are being cached. The
// message names the accepted values, because the caller prefixes the route.
func ResolvePromptCache(mode, ttl string) (PromptCachePolicy, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	t := strings.ToLower(strings.TrimSpace(ttl))

	switch m {
	case "", "off":
		if t != "" {
			return PromptCachePolicy{}, fmt.Errorf(`prompt_cache: ttl %q needs mode "explicit"`, ttl)
		}
		return PromptCachePolicy{Mode: PromptCacheOff}, nil
	case "auto":
		if t != "" {
			return PromptCachePolicy{}, fmt.Errorf(`prompt_cache: ttl %q needs mode "explicit"`, ttl)
		}
		return PromptCachePolicy{Mode: PromptCacheAuto}, nil
	case "explicit":
		switch t {
		case "", "5m":
			// "5m" is Anthropic's default, so it is the same request as an
			// omitted ttl.
			return PromptCachePolicy{Mode: PromptCacheExplicit}, nil
		case "1h":
			return PromptCachePolicy{Mode: PromptCacheExplicit, TTL: "1h"}, nil
		default:
			return PromptCachePolicy{}, fmt.Errorf(`prompt_cache: unknown ttl %q (want "5m" or "1h")`, ttl)
		}
	default:
		return PromptCachePolicy{}, fmt.Errorf(`prompt_cache: unknown mode %q (want "off", "auto" or "explicit")`, mode)
	}
}

// UsesKey reports whether the policy sends a prompt_cache_key.
func (p PromptCachePolicy) UsesKey() bool {
	return p.Mode == PromptCacheAuto || p.Mode == PromptCacheExplicit
}

// PlacesBreakpoints reports whether the policy marks content with cache_control.
func (p PromptCachePolicy) PlacesBreakpoints() bool {
	return p.Mode == PromptCacheExplicit
}

// cacheControl is the marker placed after the stable prefix.
func (p PromptCachePolicy) cacheControl() map[string]any {
	c := map[string]any{"type": "ephemeral"}
	if p.TTL != "" {
		c["ttl"] = p.TTL
	}
	return c
}

// cacheKeyPrefix is the stable part of a request: the model, the leading system
// messages and the tool definitions.
//
// The conversation is deliberately absent. A key that changed with every user
// prompt would tell the provider nothing about which requests share a prefix,
// and the whole point is to group the requests that reuse the tools + system
// block.
type cacheKeyPrefix struct {
	Model  string          `json:"model"`
	System []string        `json:"system"`
	Tools  []types.ToolDef `json:"tools"`
}

// promptCacheKey derives the routing key for one request.
//
// It is deterministic across steps, runs and processes: json.Marshal sorts the
// keys of every parameters map, so the same tool schema always hashes the same
// way. Changing a tool description or one character of the system prompt changes
// the prefix and therefore the key, which is the property that keeps a route from
// reusing a cache written for a different prefix.
func promptCacheKey(model string, msgs []types.Message, tools []types.ToolDef) string {
	prefix := cacheKeyPrefix{Model: model, System: leadingSystemMessages(msgs), Tools: tools}
	raw, err := json.Marshal(prefix)
	if err != nil {
		// The value is built from strings, maps and slices, all of which marshal.
		// Falling back to the model keeps the field well-formed if that ever stops
		// being true.
		return "tinycode-" + model
	}
	sum := sha256.Sum256(raw)
	return "tinycode-" + hex.EncodeToString(sum[:])[:32]
}

// leadingSystemMessages returns the initial run of system messages. Anything
// after the first non-system message is conversation (an injected memory block,
// for example) and is not part of the cacheable prefix.
func leadingSystemMessages(msgs []types.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != types.RoleSystem {
			break
		}
		out = append(out, m.Content)
	}
	return out
}

// cacheBreakpointIndex returns the message that ends the tools + system prefix,
// or -1 when the request has no leading system message to mark.
//
// The *last* leading system message is chosen so a request with several of them
// caches all of them; the cache order is tools → system → messages, so one
// breakpoint here covers the tool schemas too.
func cacheBreakpointIndex(msgs []types.Message) int {
	idx := -1
	for i, m := range msgs {
		if m.Role != types.RoleSystem {
			break
		}
		if m.Content != "" {
			idx = i
		}
	}
	return idx
}
