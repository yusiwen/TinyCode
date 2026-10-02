package session

import (
	"fmt"
	"strings"
)

// keys maps a key name to the bytes a terminal sends for it. The vocabulary is
// the one an agent or a test actually needs to drive a TUI: navigation, editing,
// control combinations and the function keys.
var keys = map[string]string{
	"enter":     "\r",
	"return":    "\r",
	"tab":       "\t",
	"shift+tab": "\x1b[Z",
	"esc":       "\x1b",
	"escape":    "\x1b",
	"space":     " ",
	"backspace": "\x7f",
	"delete":    "\x1b[3~",
	"insert":    "\x1b[2~",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"home":      "\x1b[H",
	"end":       "\x1b[F",
	"pageup":    "\x1b[5~",
	"pagedown":  "\x1b[6~",
	"f1":        "\x1bOP",
	"f2":        "\x1bOQ",
	"f3":        "\x1bOR",
	"f4":        "\x1bOS",
	"f5":        "\x1b[15~",
	"f6":        "\x1b[17~",
	"f7":        "\x1b[18~",
	"f8":        "\x1b[19~",
	"f9":        "\x1b[20~",
	"f10":       "\x1b[21~",
	"f11":       "\x1b[23~",
	"f12":       "\x1b[24~",
}

// Key returns the byte sequence for a key name.
//
// It understands the plain names above, `ctrl+<letter>` (the control byte), and
// an `alt+` prefix (ESC before the rest), so "ctrl+c", "alt+left" and "shift+tab"
// all work without the caller knowing any escape sequences.
func Key(name string) ([]byte, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return nil, false
	}
	if seq, ok := keys[lower]; ok {
		return []byte(seq), true
	}
	if rest, ok := strings.CutPrefix(lower, "ctrl+"); ok {
		if len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' {
			return []byte{rest[0] - 'a' + 1}, true
		}
		switch rest {
		case "@", "space":
			return []byte{0x00}, true
		case "[":
			return []byte{0x1b}, true
		case `\`:
			return []byte{0x1c}, true
		case "]":
			return []byte{0x1d}, true
		case "^":
			return []byte{0x1e}, true
		case "_":
			return []byte{0x1f}, true
		case "?":
			return []byte{0x7f}, true
		}
		return nil, false
	}
	if rest, ok := strings.CutPrefix(lower, "alt+"); ok {
		inner, ok := Key(rest)
		if !ok {
			if len([]rune(rest)) != 1 {
				return nil, false
			}
			inner = []byte(rest)
		}
		return append([]byte{0x1b}, inner...), true
	}
	// A single character is a key press; anything longer is text and is sent
	// literally by Send.
	runes := []rune(name)
	if len(runes) == 1 {
		return []byte(name), true
	}
	return nil, false
}

// Repeat returns a key sequence repeated n times, for the cases where a TUI needs
// to be walked a fixed distance.
func Repeat(name string, n int) ([]byte, error) {
	seq, ok := Key(name)
	if !ok {
		return nil, fmt.Errorf("session: %q is not a key name", name)
	}
	if n < 1 {
		return nil, fmt.Errorf("session: repeat count %d must be at least 1", n)
	}
	return []byte(strings.Repeat(string(seq), n)), nil
}

// KeyNames lists the key names the package understands, for help output.
func KeyNames() []string {
	names := make([]string, 0, len(keys)+8)
	for name := range keys {
		names = append(names, name)
	}
	names = append(names, "ctrl+a … ctrl+z", "alt+<key>", "<single character>")
	return names
}
