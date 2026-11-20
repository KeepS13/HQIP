package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestIPSwitchDNSRecoversLostResponseAndRejectsExternalEdits(t *testing.T) {
	before := wanted()
	before.ID = testID
	want := before
	want.Content = "1.1.1.1"
	actual := before
	puts := 0
	c := NewWithTransport("secret", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return response(200, []Record{actual}), nil
		}
		if r.Method != "PUT" {
			t.Fatalf("unexpected method %s", r.Method)
		}
		puts++
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("lost PUT response")
	}))
	if _, err := c.SwitchOwnedIPv4(context.Background(), testZone, before, want.Content); err == nil {
		t.Fatal("expected lost response")
	}
	got, err := c.SwitchOwnedIPv4(context.Background(), testZone, before, want.Content)
	if err != nil || !SameRecord(got, want) || got.ID != testID || puts != 1 {
		t.Fatal("did not recover PUT", got, err, puts)
	}
	for _, mutate := range []func(*Record){func(r *Record) { r.Content = "9.9.9.9" }, func(r *Record) { r.Comment = "foreign" }, func(r *Record) { r.Proxied = true }, func(r *Record) { r.ID = testZone }} {
		actual = before
		mutate(&actual)
		if _, err = c.SwitchOwnedIPv4(context.Background(), testZone, before, want.Content); !errors.Is(err, ErrChanged) {
			t.Fatal("external edit accepted", actual, err)
		}
	}
	if puts != 1 {
		t.Fatal("external record overwritten")
	}
}
