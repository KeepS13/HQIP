package control

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPaymentOrderFilterByteLimitAndBeijingRange(t *testing.T) {
	for _, tc := range []struct {
		search string
		valid  bool
	}{{strings.Repeat("a", 120), true}, {strings.Repeat("a", 121), false}, {strings.Repeat("订", 40), true}, {strings.Repeat("订", 41), false}} {
		q := url.Values{"search": {tc.search}, "from": {"2026-12-31T00:00:00+08:00"}, "to": {"2027-01-01T00:00:00+08:00"}}
		f, err := paymentFilter(httptest.NewRequest("GET", "/api/v1/orders?"+q.Encode(), nil))
		if (err == nil) != tc.valid {
			t.Fatalf("%d-byte search: %v", len(tc.search), err)
		}
		if tc.valid && (f.To.Sub(*f.From) != 24*time.Hour || f.From.UTC().Hour() != 16) {
			t.Fatal("Beijing date range changed")
		}
	}
}
