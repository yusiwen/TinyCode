package i18n

import "testing"

// TestParseAcceptsEverySpellingOfA language pins the forms a person can write in
// config.json, on the command line or in a locale variable.
func TestParseAcceptsEverySpellingOfALanguage(t *testing.T) {
	cases := []struct {
		in   string
		want Language
		ok   bool
	}{
		{"", "", false},
		{"en", English, true},
		{"EN", English, true},
		{"en_US.UTF-8", English, true},
		{"en-US", English, true},
		{"english", English, true},
		{"zh", Chinese, true},
		{"zh_CN.UTF-8", Chinese, true},
		{"zh-CN", Chinese, true},
		{"zh-Hans-CN", Chinese, true},
		{"zh_TW", Chinese, true},
		{"chinese", Chinese, true},
		{" C ", English, true},
		{"POSIX", English, true},
		{"fr", "", false},
		{"fr_FR.UTF-8", "", false},
		{"zhx", "", false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestDetectResolvesInTheDocumentedOrder is the precedence table docs/i18n.md
// states: the configured value, then TINYCODE_LANG, then the locale variables.
func TestDetectResolvesInTheDocumentedOrder(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		name       string
		configured string
		values     map[string]string
		want       Language
	}{
		{"nothing set", "", nil, English},
		{"configured wins over everything", "zh", map[string]string{
			"TINYCODE_LANG": "en", "LC_ALL": "en_US.UTF-8", "LANG": "en_US.UTF-8",
		}, Chinese},
		{"TINYCODE_LANG beats the locale", "", map[string]string{
			"TINYCODE_LANG": "zh", "LC_ALL": "en_US.UTF-8", "LANG": "en_US.UTF-8",
		}, Chinese},
		{"LC_ALL beats LC_MESSAGES and LANG", "", map[string]string{
			"LC_ALL": "zh_CN.UTF-8", "LC_MESSAGES": "en_US.UTF-8", "LANG": "en_US.UTF-8",
		}, Chinese},
		{"LC_MESSAGES beats LANG", "", map[string]string{
			"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "en_US.UTF-8",
		}, Chinese},
		{"LANG is the last resort", "", map[string]string{"LANG": "zh_CN.UTF-8"}, Chinese},
		{"an unsupported value is skipped, not fatal", "fr", map[string]string{
			"LANG": "zh_CN.UTF-8",
		}, Chinese},
		{"an unsupported value with nothing behind it is English", "fr", nil, English},
		{"a C locale is an explicit English", "", map[string]string{"LC_ALL": "C"}, English},
	}
	for _, c := range cases {
		if got := Detect(c.configured, env(c.values)); got != c.want {
			t.Errorf("%s: Detect(%q, env) = %q, want %q", c.name, c.configured, got, c.want)
		}
	}
}

// TestTFormatsOnlyWithArguments keeps a percent sign in a message that takes no
// arguments out of fmt.Sprintf's reach.
func TestTFormatsOnlyWithArguments(t *testing.T) {
	reset(t)
	if got := T("100% local"); got != "100% local" {
		t.Errorf("T without arguments changed the message: %q", got)
	}
	if got := T("%d files", 3); got != "3 files" {
		t.Errorf("T with arguments = %q, want %q", got, "3 files")
	}
}

// TestLookupFallsBackToTheSourceText is the property that makes a missing
// translation a readable screen: English, never a blank or a key name.
func TestLookupFallsBackToTheSourceText(t *testing.T) {
	reset(t)
	SetLanguage(Chinese)
	if got := Lookup(Chinese, "a message nobody has translated"); got != "a message nobody has translated" {
		t.Errorf("missing entry = %q, want the English text", got)
	}
	if got := Lookup(English, "anything"); got != "anything" {
		t.Errorf("English lookup = %q, want the message id", got)
	}
}

// TestTranslationsFormatThroughTheSwitch proves the catalogue is actually
// consulted by T, not only by Lookup.
func TestTranslationsFormatThroughTheSwitch(t *testing.T) {
	reset(t)
	const msg = "msgid only the test knows %d"
	chinese[msg] = "只有测试知道的消息 %d"
	t.Cleanup(func() { delete(chinese, msg) })

	SetLanguage(Chinese)
	if got := T(msg, 7); got != "只有测试知道的消息 7" {
		t.Errorf("T in Chinese = %q, want the translated, formatted text", got)
	}
	SetLanguage(English)
	if got := T(msg, 7); got != "msgid only the test knows 7" {
		t.Errorf("T in English = %q, want the source text", got)
	}
}

// TestSetLanguageRejectsWhatItCannotRender keeps a typo in one variable from
// leaving the interface in a language whose catalogue does not exist.
func TestSetLanguageRejectsWhatItCannotRender(t *testing.T) {
	reset(t)
	SetLanguage("klingon")
	if got := Current(); got != English {
		t.Errorf("Current() = %q after an unsupported language, want %q", got, English)
	}
	SetLanguage(Chinese)
	if got := Current(); got != Chinese {
		t.Errorf("Current() = %q, want %q", got, Chinese)
	}
}

// TestMessagesIsACopy keeps the shipped catalogue immutable at run time.
func TestMessagesIsACopy(t *testing.T) {
	reset(t)
	m := Messages(Chinese)
	m["a message nobody has translated"] = "tampered"
	if got := Lookup(Chinese, "a message nobody has translated"); got != "a message nobody has translated" {
		t.Errorf("Messages returned the live map: %q", got)
	}
}

// reset puts the package back in English and restores it after the test. The
// language is process state, so a test that leaves it elsewhere changes every
// test that runs after it.
func reset(t *testing.T) {
	t.Helper()
	before := Current()
	SetLanguage(English)
	t.Cleanup(func() { SetLanguage(before) })
}
