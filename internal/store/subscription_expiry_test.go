package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func expirySnapshot(t *testing.T, s *Store) string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"portal_usage", "portal_credentials", "portal_sessions", "portal_leases", "subscription_allocations", "payment_orders", "portal_templates"} {
		rows, err := s.DB.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], jsonString(values))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return jsonString(out)
}

func TestSubscriptionExpiryPreservesGrantAndOtherCopies(t *testing.T) {
	s, _, user, template, node, password := portalFixture(t, 2, nil)
	a := user.Subscriptions[0]
	b := grantAnother(t, s, user.ID, template, "another-copy")
	other, err := s.RegisterPortal("other-expiry@example.com", "expiry-password-123")
	if err != nil {
		t.Fatal(err)
	}
	c := grantAnother(t, s, other.ID, template, "other-user")
	nativeSubscriptionSession(t, s, user.ID, a.ID)
	lease := acquire(t, s, node, password, "a")
	_ = renew(t, s, lease, 111, 222, false)
	current, _ := s.UserSubscription(user.ID, a.ID)
	before, _ := s.PortalUser(user.ID)
	unchanged := expirySnapshot(t, s)
	var oldHash, oldSecret string
	if err = s.DB.QueryRow("SELECT subscription_hash,subscription_secret FROM portal_subscriptions WHERE id=?", a.ID).Scan(&oldHash, &oldSecret); err != nil {
		t.Fatal(err)
	}
	for _, target := range []time.Time{a.ExpiresAt.Add(24 * time.Hour), a.ExpiresAt.Add(-time.Hour)} {
		updated, e := s.UpdateSubscriptionExpiry(user.ID, a.ID, target, current.Revision)
		if e != nil {
			t.Fatal(e)
		}
		if !updated.ExpiresAt.Equal(target) || updated.Revision != current.Revision+1 {
			t.Fatal("expiry/revision not updated")
		}
		expected := *current
		expected.ExpiresAt, expected.Revision = updated.ExpiresAt, updated.Revision
		if !reflect.DeepEqual(updated, expected) {
			t.Fatal("other entitlement changed")
		}
		if expirySnapshot(t, s) != unchanged {
			t.Fatal("usage, credentials, sessions, leases, nodes or orders changed")
		}
		current = &updated
	}
	after, _ := s.PortalUser(user.ID)
	if after.Revision != before.Revision+2 {
		t.Fatal("profile version not advanced")
	}
	for _, expected := range []model.UserSubscription{b, c} {
		actual, e := s.UserSubscription(expected.UserID, expected.ID)
		if e != nil || !reflect.DeepEqual(*actual, expected) {
			t.Fatal("another subscription changed", e)
		}
	}
	var hash, secret string
	if err = s.DB.QueryRow("SELECT subscription_hash,subscription_secret FROM portal_subscriptions WHERE id=?", a.ID).Scan(&hash, &secret); err != nil || hash != oldHash || secret != oldSecret {
		t.Fatal("import credential rotated", err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.expiry_updated'").Scan(&count); err != nil || count != 2 {
		t.Fatal("missing audit", err)
	}
	var raw string
	if err = s.DB.QueryRow("SELECT subject FROM audit WHERE action='subscription.expiry_updated' ORDER BY rowid DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		UserID            string
		SubscriptionID    string
		PreviousExpiresAt time.Time
		ExpiresAt         time.Time
	}
	if err = json.Unmarshal([]byte(raw), &audit); err != nil || audit.UserID != user.ID || audit.SubscriptionID != a.ID || !audit.PreviousExpiresAt.Equal(a.ExpiresAt.Add(24*time.Hour)) || !audit.ExpiresAt.Equal(current.ExpiresAt) {
		t.Fatal("invalid audit", raw, err)
	}
	if _, err = s.UpdateSubscriptionExpiry(user.ID, a.ID, current.ExpiresAt, current.Revision); err != nil {
		t.Fatal("unchanged expiry", err)
	}
	unchangedUser, _ := s.PortalUser(user.ID)
	if unchangedUser.Revision != after.Revision {
		t.Fatal("no-op advanced revision")
	}
}

func TestSubscriptionExpiryRejectsInvalidStateAndStaleWrites(t *testing.T) {
	for _, scenario := range []string{"past", "now", "expired", "disabled-expired", "revoked", "stale", "foreign", "expires-during-update", "expires-during-save", "target-expires-during-save", "audit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			s, now, user, _, _, _ := portalFixture(t, 1, nil)
			sub := user.Subscriptions[0]
			owner, revision, target, want := user.ID, sub.Revision, sub.ExpiresAt.Add(time.Hour), ErrSubscriptionExpiry
			switch scenario {
			case "past":
				target = now.Add(-time.Minute)
			case "now":
				target = *now
			case "expired", "disabled-expired":
				*now = sub.ExpiresAt
				want = ErrSubscriptionExpired
				if scenario == "disabled-expired" {
					audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
				}
			case "revoked":
				audienceExec(t, s, "UPDATE portal_subscriptions SET revoked=1 WHERE id=?", sub.ID)
				want = ErrSubscriptionRemoved
			case "stale":
				revision--
				want = ErrConflict
			case "foreign":
				owner = "another-user"
				want = ErrNotFound
			case "expires-during-update", "expires-during-save", "target-expires-during-save":
				calls := 0
				deadlineCall := 6
				if scenario == "expires-during-update" {
					deadlineCall = 3
				}
				boundary := sub.ExpiresAt
				want = ErrSubscriptionExpired
				if scenario == "target-expires-during-save" {
					target = now.Add(time.Minute)
					boundary = target
					want = ErrSubscriptionExpiry
				}
				clock := *now
				s.Now = func() time.Time {
					calls++
					if calls >= deadlineCall {
						return boundary
					}
					return clock
				}
			case "audit-failure":
				if s.Postgres() {
					t.Skip("SQLite fault injection; transaction semantics also run against PostgreSQL")
				}
				audienceExec(t, s, "CREATE TRIGGER expiry_audit_failure BEFORE INSERT ON audit WHEN NEW.action='subscription.expiry_updated' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END")
				want = nil
			}
			var oldExpiry, oldRevision, oldUserRevision int64
			s.DB.QueryRow("SELECT expires,revision FROM portal_subscriptions WHERE id=?", sub.ID).Scan(&oldExpiry, &oldRevision)
			s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user.ID).Scan(&oldUserRevision)
			_, err := s.UpdateSubscriptionExpiry(owner, sub.ID, target, revision)
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			var expiry, rev, userRev, audits int64
			s.DB.QueryRow("SELECT expires,revision FROM portal_subscriptions WHERE id=?", sub.ID).Scan(&expiry, &rev)
			s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user.ID).Scan(&userRev)
			s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.expiry_updated'").Scan(&audits)
			if expiry != oldExpiry || rev != oldRevision || userRev != oldUserRevision || audits != 0 {
				t.Fatal("failed operation committed partial data")
			}
		})
	}
}

func TestSubscriptionExpiryAllowsUnexpiredDisabledAndExhausted(t *testing.T) {
	for _, status := range []string{"disabled", "exhausted"} {
		t.Run(status, func(t *testing.T) {
			quota := int64(100000000)
			s, _, u, _, _, _ := portalFixture(t, 1, &quota)
			sub := u.Subscriptions[0]
			if status == "disabled" {
				audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", u.ID)
			} else {
				audienceExec(t, s, "UPDATE portal_usage SET upload=? WHERE subscription_id=?", quota, sub.ID)
			}
			v, err := s.UpdateSubscriptionExpiry(u.ID, sub.ID, sub.ExpiresAt.Add(time.Hour), sub.Revision)
			if err != nil || v.Status != status {
				t.Fatal(v.Status, err)
			}
		})
	}
}

func TestSubscriptionExpiryConcurrentUpdatesHaveOneWinner(t *testing.T) {
	s, _, u, _, _, _ := portalFixture(t, 1, nil)
	sub := u.Subscriptions[0]
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.UpdateSubscriptionExpiry(u.ID, sub.ID, sub.ExpiresAt.Add(time.Duration(i+1)*time.Hour), sub.Revision)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}

func TestSubscriptionExpiryLeaseRenewalKeepsConnectionAndHonorsNewEnd(t *testing.T) {
	s, now, u, _, node, password := portalFixture(t, 2, nil)
	sub := u.Subscriptions[0]
	short, err := s.UpdateSubscriptionExpiry(u.ID, sub.ID, now.Add(60*time.Second), sub.Revision)
	if err != nil {
		t.Fatal(err)
	}
	l := acquire(t, s, node, password, "b")
	if !l.ExpiresAt.Equal(short.ExpiresAt) {
		t.Fatal("initial lease ignored end")
	}
	extended, err := s.UpdateSubscriptionExpiry(u.ID, sub.ID, now.Add(time.Hour), short.Revision)
	if err != nil {
		t.Fatal(err)
	}
	l = renew(t, s, l, 100, 200, false)
	if l.ID == "" || !l.ExpiresAt.Equal(now.Add(120*time.Second)) || l.Revision != extended.Revision {
		t.Fatal("extension interrupted or failed to extend lease")
	}
	end := now.Add(30 * time.Second)
	if _, err = s.UpdateSubscriptionExpiry(u.ID, sub.ID, end, extended.Revision); err != nil {
		t.Fatal(err)
	}
	l = renew(t, s, l, 110, 220, false)
	if !l.ExpiresAt.Equal(end) {
		t.Fatal("renewal ignored shortened end")
	}
	*now = end
	if _, err = s.RenewLease(node.ID, l.ID, model.LeaseUsage{UploadBytes: 110, DownloadBytes: 220, AppliedRateVersion: l.RateVersion}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("expired renewal allowed", err)
	}
	if _, err = s.AcquireLease(node.ID, strings.Repeat("new-expiry", 4), password); !errors.Is(err, ErrEntitlement) {
		t.Fatal("expired admission allowed", err)
	}
}
