package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/i18n"
)

// TestTheTranslatedTablesHaveChineseEntries covers the messages that reach T
// through a variable: the command palette, the welcome rows, the key hints and
// the permission answers are tables, so the AST gate in the i18n package cannot
// see them and this test enumerates the real tables instead of a copy of them.
func TestTheTranslatedTablesHaveChineseEntries(t *testing.T) {
	i18n.SetLanguage(i18n.English)
	t.Cleanup(func() { i18n.SetLanguage(i18n.English) })

	missing := func(what, msgid string) {
		t.Helper()
		if _, ok := i18n.Translation(i18n.Chinese, msgid); !ok {
			t.Errorf("%s translates %q at run time but the catalogue has no entry", what, msgid)
		}
	}
	for _, c := range commandList() {
		// commandList resolves the description as it builds the entry, so this
		// test runs in English where the description is its own message id.
		missing("the command palette", c.Desc)
	}
	for _, c := range welcomeCommands {
		missing("the welcome banner", c.Desc)
	}
	for _, group := range [][]string{dialogHintAlternatives, providerHintAlternatives, paletteHintAlternatives} {
		for _, alt := range group {
			missing("a key hint row", alt)
		}
	}
	for _, label := range []string{allowOnceLabel, allowSessionLabel, alwaysAllowPrefix} {
		missing("the permission dialog", label)
	}
}

// TestTheLanguageSwitchChangesWhatTheInterfaceDraws is the behavioural half: with
// Chinese selected, the palette and the welcome banner — both built from tables
// at draw time — come out in Chinese, and switching back restores English. It is
// also the guard against the classic mistake of translating at package
// initialisation, where the language is still English.
func TestTheLanguageSwitchChangesWhatTheInterfaceDraws(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage(i18n.English) })

	i18n.SetLanguage(i18n.English)
	palette := commandList()
	if len(palette) == 0 {
		t.Fatal("the command palette is empty")
	}
	english := palette[0].Desc
	if strings.ContainsAny(english, "，。：") {
		t.Fatalf("the English palette description already looks translated: %q", english)
	}

	i18n.SetLanguage(i18n.Chinese)
	chinese := commandList()[0].Desc
	if chinese == english {
		t.Fatalf("the palette description did not change with the language: %q", chinese)
	}
	if !containsHan(chinese) {
		t.Errorf("the Chinese palette description has no Chinese in it: %q", chinese)
	}

	banner := bannerText(t)
	if !strings.Contains(banner, i18n.T("Get started")) {
		t.Errorf("the welcome banner does not use the selected language:\n%s", banner)
	}
	if !containsHan(banner) {
		t.Errorf("the welcome banner has no Chinese in it:\n%s", banner)
	}

	i18n.SetLanguage(i18n.English)
	if got := commandList()[0].Desc; got != english {
		t.Errorf("switching back to English left %q, want %q", got, english)
	}
}

// bannerText flattens the drawn welcome banner into the text a person reads.
func bannerText(t *testing.T) string {
	t.Helper()
	info := welcomeInfo{Tools: 2, Skills: 3, Agents: 1}
	var b strings.Builder
	for _, line := range renderWelcomeLines(info, 100) {
		for _, chunk := range line {
			b.WriteString(chunk.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// containsHan reports whether s has at least one Han character, which is what
// distinguishes a translated string from the English it replaced.
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
