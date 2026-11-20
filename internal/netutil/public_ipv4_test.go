package netutil

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPublicIPv4SharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/public-ipv4.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input string
		Valid bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Input, func(t *testing.T) {
			got, err := ParsePublicIPv4(tc.Input)
			if (err == nil) != tc.Valid {
				t.Fatalf("%q: %q, %v", tc.Input, got, err)
			}
			if tc.Valid && got != strings.TrimSpace(tc.Input) {
				t.Fatalf("noncanonical result: %q", got)
			}
		})
	}
}
