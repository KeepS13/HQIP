package client

import "testing"

func TestPauseOnlyExemptsExplicitManagementDestinations(t *testing.T) {
	p := Profile{Nodes: []Node{{Server: "203.0.113.1"}, {Server: "node.test"}}}
	for _, tc := range []struct {
		host, ip, process string
		want              bool
	}{
		{"api.test", "", "", true}, {"node.test", "", "", true}, {"", "203.0.113.1", "", true},
		{"", "", `C:\Program Files\HQIP\HQIP.exe (Administrator)`, true},
		{"ordinary.test", "", "", false}, {"api.test.evil", "", "", false}, {"", "", `C:\Windows\ssh.exe`, false},
	} {
		if got := managementConnection(tc.host, tc.ip, tc.process, p, "https://api.test"); got != tc.want {
			t.Fatalf("management classification=%v for %+v", got, tc)
		}
	}
}
