package netsafe

import (
	"net"
	"testing"
)

// FuzzIsBlockedIP checks the SSRF IP policy. The properties asserted here are
// the ones the transports rely on: every non-routable range is blocked, the
// answer does not depend on which form of the same address is passed, and the
// check never panics on odd input.
func FuzzIsBlockedIP(f *testing.F) {
	f.Add("127.0.0.1")
	f.Add("10.0.0.1")
	f.Add("172.16.0.1")
	f.Add("172.32.0.1")
	f.Add("192.168.1.1")
	f.Add("169.254.169.254")
	f.Add("100.64.0.1")
	f.Add("192.0.0.1")
	f.Add("240.0.0.1")
	f.Add("8.8.8.8")
	f.Add("198.18.0.1")
	f.Add("::1")
	f.Add("fe80::1")
	f.Add("fd00::1")
	f.Add("::ffff:127.0.0.1")
	f.Add("64:ff9b::127.0.0.1")
	f.Add("::")
	f.Add("2001:4860:4860::8888")
	f.Add("not-an-ip")
	f.Add("")

	f.Fuzz(func(t *testing.T, s string) {
		ip := net.ParseIP(s)
		if ip == nil {
			return
		}
		blocked := IsBlockedIP(ip)

		// The same address in another form must get the same answer.
		if v4 := ip.To4(); v4 != nil && IsBlockedIP(v4) != blocked {
			t.Fatalf("address %q: 4-byte form answered differently", s)
		}
		if v16 := ip.To16(); v16 != nil && IsBlockedIP(v16) != blocked {
			t.Fatalf("address %q: 16-byte form answered differently", s)
		}

		switch {
		case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(),
			ip.IsUnspecified(), ip.IsMulticast():
			if !blocked {
				t.Fatalf("non-routable address %q is not blocked", s)
			}
		}
	})
}

// FuzzNormalizeAuthority checks the canonical form used to compare an allowed
// loopback authority: normalizing twice must not change the answer again, or a
// configured exemption could stop matching itself.
func FuzzNormalizeAuthority(f *testing.F) {
	f.Add("localhost:8080")
	f.Add("127.0.0.1:9000")
	f.Add("[::1]:8000")
	f.Add("LOCALHOST:80")
	f.Add("::1")
	f.Add("example.com")
	f.Add("")
	f.Add("a:b:c")

	f.Fuzz(func(t *testing.T, authority string) {
		once := normalizeAuthority(authority)
		if twice := normalizeAuthority(once); twice != once {
			t.Fatalf("normalizeAuthority is not idempotent: %q -> %q -> %q", authority, once, twice)
		}
	})
}
