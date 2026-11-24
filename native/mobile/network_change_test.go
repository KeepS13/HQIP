package mobile

import "testing"

func TestNetworkNotificationsOnlyResetChangedEgress(t *testing.T) {
	var changes networkChanges
	resets := 0
	apply := func() { resets++ }
	wifi := `[{"networkId":1,"name":"wlan0","index":5,"default":true,"addresses":["a","b"],"dns":["dns1"]}]`
	reordered := `[{"name":"rmnet0","index":6},{"networkId":1,"name":"wlan0","index":5,"default":true,"addresses":["b","a"],"dns":["dns1"]}]`
	for _, raw := range []string{wifi, wifi, reordered} {
		if err := changes.update(raw, apply); err != nil {
			t.Fatal(err)
		}
	}
	if resets != 1 {
		t.Fatalf("duplicate/unrelated notifications reset %d times", resets)
	}
	if err := changes.update("broken snapshot", apply); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	if resets != 1 {
		t.Fatal("read failure destroyed active sockets")
	}
	for _, raw := range []string{`[]`, `[]`, wifi, `[{"networkId":2,"name":"wlan0","index":5,"default":true,"addresses":["a","b"],"dns":["dns1"]}]`} {
		if err := changes.update(raw, apply); err != nil {
			t.Fatal(err)
		}
	}
	if resets != 4 {
		t.Fatalf("loss, restoration and new network identity must reset: %d", resets)
	}
}
