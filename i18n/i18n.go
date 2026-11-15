// Package i18n holds the interface language and the catalogue of translated
// messages.
//
// The English source text doubles as the message id, in the gettext tradition:
// a string with no catalogue entry degrades to English rather than to a key
// name, so a half-written translation is a half-translated screen and not a
// screen full of `tui.status.no_agent`. It also means adding a message costs one
// catalogue entry instead of a message, an id, and a lookup.
//
// Two boundaries are deliberate and documented in docs/i18n.md: text generated
// by cobra (the `--help` output and flag descriptions) is printed before the
// configured language is known, and text aimed at the model (system prompts,
// tool descriptions, tool results) is not interface text at all — translating it
// would change model behaviour, not what a person reads.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Language is one of the languages the interface can be written in.
type Language string

const (
	// English is the source language. Every message id is its English text.
	English Language = "en"
	// Chinese is the translated language, written in Simplified Chinese.
	Chinese Language = "zh"
)

// Languages lists the supported languages in the order the documentation
// presents them.
var Languages = []Language{English, Chinese}

// current holds the language of every T call that follows. It is atomic because
// the process reads it from more than one goroutine: the TUI renders the status
// bar and messages while the program that chose the language has already moved
// on.
var current atomic.Value

func init() { current.Store(English) }

// SetLanguage sets the language used by T from now on. A value that parses to no
// supported language leaves the process in English, which is the language the
// source text is written in.
func SetLanguage(l Language) {
	if parsed, ok := Parse(string(l)); ok {
		current.Store(parsed)
		return
	}
	current.Store(English)
}

// Current reports the language T renders in.
func Current() Language {
	l, ok := current.Load().(Language)
	if !ok {
		return English
	}
	return l
}

// Parse turns a language or locale name into a supported language. It accepts
// the short codes ("en", "zh"), the POSIX locale form ("zh_CN.UTF-8"), the BCP 47
// form ("zh-Hans-CN") and the English names of the two languages. "C" and
// "POSIX" mean English: they are how a shell says "no translations", and the
// source language is what that resolves to here.
//
// ok is false for a value that names no supported language, which is what lets
// Detect tell "not set" from "set to something nobody supports".
func Parse(s string) (Language, bool) {
	base := strings.ToLower(strings.TrimSpace(s))
	if base == "" {
		return "", false
	}
	if base == "c" || base == "posix" {
		return English, true
	}
	// Drop the codeset and modifier ("zh_CN.UTF-8@euro"), then the territory
	// ("zh_CN", "zh-Hans-CN"). What is left is the language subtag.
	if i := strings.IndexAny(base, ".@"); i >= 0 {
		base = base[:i]
	}
	if i := strings.IndexAny(base, "_-"); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "en", "english":
		return English, true
	case "zh", "chinese":
		return Chinese, true
	}
	return "", false
}

// Detect resolves the interface language. The first supported value wins, in
// this order: the configured value (--lang, then config.json), TINYCODE_LANG, and
// then the POSIX locale variables — LC_ALL, LC_MESSAGES, LANG. A value nobody
// supports is skipped rather than fatal, so a typo in one variable cannot lock a
// person out of the language their locale asks for.
//
// getenv is passed in so the order can be tested without a process environment.
func Detect(configured string, getenv func(string) string) Language {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, candidate := range []string{
		configured,
		getenv("TINYCODE_LANG"),
		getenv("LC_ALL"),
		getenv("LC_MESSAGES"),
		getenv("LANG"),
	} {
		if l, ok := Parse(candidate); ok {
			return l
		}
	}
	return English
}

// T returns msgid in the current language, formatted with args.
//
// Formatting happens only when args are present: a message that legitimately
// contains a percent sign ("100% local") must survive being looked up, and a
// fmt.Sprintf with no arguments would turn it into "%!d(MISSING)". Messages that
// take arguments always pass them, and the catalogue gate keeps the two
// translations agreeing on how many verbs they have.
func T(msgid string, args ...any) string {
	msg := Lookup(Current(), msgid)
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Translation returns the catalogue entry for msgid in l, and whether one
// exists. English has no entries: its translation is the message id.
func Translation(l Language, msgid string) (string, bool) {
	if l == English || l == "" {
		return "", false
	}
	tr, ok := catalogue[l][msgid]
	return tr, ok && tr != ""
}

// Lookup returns the translation of msgid for l, falling back to msgid itself.
// English is the source language, so its translation is the message id.
func Lookup(l Language, msgid string) string {
	if tr, ok := Translation(l, msgid); ok {
		return tr
	}
	return msgid
}

// Messages returns a copy of the catalogue for l, keyed by the English source
// text. The copy means a caller cannot add a translation at run time, which is
// the point: the catalogue is data that ships with the binary, and the gate in
// catalogue_test.go is what keeps it complete.
func Messages(l Language) map[string]string {
	src := catalogue[l]
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
