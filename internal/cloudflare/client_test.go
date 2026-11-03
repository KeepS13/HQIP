package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, result any) *http.Response {
	raw, _ := json.Marshal(map[string]any{"success": status < 400, "result": result})
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}
}

const testZone = "11111111111111111111111111111111"
const testID = "22222222222222222222222222222222"

func wanted() Record {
	return Record{Type: "A", Name: "n-a.example.com", Content: "203.0.113.4", TTL: 120, Comment: "HQIP node n random-owner"}
}

func TestRecordRecoveryAfterLostPOSTResponse(t *testing.T) {
	var records []Record
	posts := 0
	c := NewWithTransport("token-sensitive-value-never-log", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.cloudflare.com" {
			t.Fatal("unexpected API origin")
		}
		if r.Header.Get("Authorization") != "Bearer token-sensitive-value-never-log" {
			t.Fatal("missing token")
		}
		switch r.Method {
		case "GET":
			return response(200, records), nil
		case "POST":
			posts++
			var record Record
			_ = json.NewDecoder(r.Body).Decode(&record)
			if record.Proxied {
				t.Fatal("HY2 record must not be proxied")
			}
			record.ID = testID
			records = append(records, record)
			return nil, errors.New("lost response")
		}
		t.Fatalf("unexpected method %s", r.Method)
		return nil, nil
	}))
	if _, err := c.EnsureRecord(context.Background(), testZone, wanted()); err == nil {
		t.Fatal("expected interrupted request")
	}
	record, err := c.EnsureRecord(context.Background(), testZone, wanted())
	if err != nil || record.ID != testID || posts != 1 {
		t.Fatalf("recovery: %+v %v posts=%d", record, err, posts)
	}
}
func TestRecordCollisionDoesNotOverwrite(t *testing.T) {
	for _, existing := range []Record{{ID: testID, Type: "A", Name: "n-a.example.com", Content: "198.51.100.1"}, {ID: testID, Type: "AAAA", Name: "n-a.example.com", Content: "2001:db8::1"}, {ID: testID, Type: "CNAME", Name: "n-a.example.com", Content: "elsewhere.example.com"}} {
		c := NewWithTransport("secret", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" {
				t.Fatal("collision caused a write")
			}
			return response(200, []Record{existing}), nil
		}))
		if _, err := c.EnsureRecord(context.Background(), testZone, wanted()); !errors.Is(err, ErrConflict) {
			t.Fatalf("collision not refused: %v", err)
		}
	}
}
func TestOwnedRecordModificationRefusesDeletionAndRecreation(t *testing.T) {
	want := wanted()
	want.ID = testID
	changed := want
	changed.Content = "198.51.100.5"
	deletes := 0
	c := NewWithTransport("secret", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			deletes++
		}
		if r.URL.RawQuery != "" {
			return response(200, []Record{changed}), nil
		}
		return response(200, changed), nil
	}))
	if err := c.DeleteOwned(context.Background(), testZone, want); !errors.Is(err, ErrChanged) {
		t.Fatalf("modified record deletion: %v", err)
	}
	if _, err := c.EnsureRecord(context.Background(), testZone, want); !errors.Is(err, ErrChanged) {
		t.Fatalf("modified record replacement: %v", err)
	}
	if deletes != 0 {
		t.Fatal("deleted external changes")
	}
}
func TestTXTCoexistsAndDeletesOnlyItsRecord(t *testing.T) {
	want := Record{Type: "TXT", Name: "_acme-challenge.n.example.com", Content: "our-value", Comment: "HQIP challenge ours", TTL: 120}
	external := want
	external.Comment = "other ACME client"
	external.Content = "other-value"
	external.ID = testID
	c := NewWithTransport("secret", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case "GET":
			return response(200, []Record{external}), nil
		case "POST":
			created := want
			created.ID = "33333333333333333333333333333333"
			return response(200, created), nil
		}
		t.Fatal("unexpected mutation")
		return nil, nil
	}))
	created, err := c.EnsureRecord(context.Background(), testZone, want)
	if err != nil || created.ID == external.ID {
		t.Fatalf("TXT coexistence: %+v %v", created, err)
	}
}
func TestAPIErrorsAndRedirectsNeverLeakToken(t *testing.T) {
	secret := "super-sensitive-token-value"
	calls := 0
	c := NewWithTransport(secret, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":false,"errors":[{"code":10000,"message":"` + secret + `"}]}`))}, nil
	}))
	if err := c.Verify(context.Background()); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe API error: %v", err)
	}
	c = NewWithTransport(secret, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.invalid"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	if err := c.Verify(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 2 {
		t.Fatal("redirect followed")
	}
}
func TestInvalidIdentifiersNeverMakeRequest(t *testing.T) {
	c := NewWithTransport("secret", roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid identifier sent"); return nil, nil }))
	if _, err := c.Zone(context.Background(), "../accounts"); err == nil {
		t.Fatal("invalid zone accepted")
	}
	if err := c.DeleteOwned(context.Background(), testZone, Record{ID: "../dns_records"}); err == nil {
		t.Fatal("invalid record accepted")
	}
}

func TestAcknowledgedRecordRemovedExternallyIsNotRecreated(t *testing.T) {
	want := wanted()
	want.ID = testID
	c := NewWithTransport("secret", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("recreated externally removed record")
		}
		return response(200, []Record{}), nil
	}))
	if _, err := c.EnsureRecord(context.Background(), testZone, want); !errors.Is(err, ErrChanged) {
		t.Fatalf("missing acknowledged record should require manual repair: %v", err)
	}
}
