package model

import "testing"

func TestCanonicalClientIP(t *testing.T) {
	for raw, want := range map[string]string{
		"198.51.100.7": "198.51.100.7", "::ffff:198.51.100.7": "198.51.100.7",
		"2001:0db8:0:0::7": "2001:db8::7", "127.0.0.1": "127.0.0.1",
	} {
		if got, err := CanonicalClientIP(raw); err != nil || got != want {
			t.Fatalf("%q: got %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "0.0.0.0", "::", "::ffff:0.0.0.0", "224.0.0.1", "::ffff:224.0.0.1", "ff02::1", "fe80::1%eth0", "::ffff:198.51.100.7%eth0", "198.51.100.7:123", "[::1]", " 198.51.100.7", "hostname"} {
		if _, err := CanonicalClientIP(raw); err == nil {
			t.Errorf("accepted invalid peer %q", raw)
		}
	}
}
