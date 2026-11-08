package store

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestPostgresConcurrentAdmissionReplayAcrossPools(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	for _, boundary := range []struct {
		name  string
		slots int
		quota int64
	}{
		{"last_slot", 1, 64 << 20},
		{"last_budget", 2, 2 * model.MinimumSubscriptionLeaseBytes},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			s, _, user, _, node, password := portalFixture(t, boundary.slots, &boundary.quota)
			peer, err := reopenFixture(s)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			peer.Now = s.Now
			pools := []*Store{s, peer}
			const workers = 32
			type result struct {
				lease model.ConnectionLease
				err   error
			}
			results := make(chan result, workers)
			start := make(chan struct{})
			var done sync.WaitGroup
			for i := 0; i < workers; i++ {
				done.Add(1)
				go func(i int) {
					defer done.Done()
					<-start
					lease, err := pools[i%2].AcquireLease(node.ID, strings.Repeat("a", 32), password)
					results <- result{lease, err}
				}(i)
			}
			close(start)
			done.Wait()
			close(results)
			var first model.ConnectionLease
			for got := range results {
				if got.err != nil {
					t.Fatal("same-connection replay failed", got.err)
				}
				if first.ID == "" {
					first = got.lease
				} else if got.lease.ID != first.ID || got.lease.BudgetBytes != first.BudgetBytes {
					t.Fatal("same-connection replay created another reservation")
				}
			}
			sub, err := peer.UserSubscription(user.ID, first.SubscriptionID)
			if err != nil || sub.Occupied != 1 || sub.ReservedBytes != first.BudgetBytes || sub.UsedBytes != 0 {
				t.Fatal("same-connection replay changed the quota ledger", sub, err)
			}
			_, err = peer.AcquireLease(node.ID, strings.Repeat("b", 32), password)
			want := ErrSlots
			if boundary.slots > 1 {
				want = ErrEntitlement
			}
			if !errors.Is(err, want) {
				t.Fatal("another connection exceeded the boundary", err)
			}
			var count int
			if err = peer.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=?", first.SubscriptionID).Scan(&count); err != nil || count != 1 {
				t.Fatal("denied connection persisted a lease", count, err)
			}
			for _, pool := range pools {
				if _, err = pool.RenewLease(node.ID, first.ID, model.LeaseUsage{UploadBytes: 3, DownloadBytes: 4, Closed: true, AppliedRateVersion: first.RateVersion}); err != nil {
					t.Fatal(err)
				}
			}
			sub, err = peer.UserSubscription(user.ID, first.SubscriptionID)
			if err != nil || sub.Occupied != 0 || sub.ReservedBytes != 0 || sub.UsedBytes != 7 {
				t.Fatal("replayed settlement did not conserve the quota ledger", sub, err)
			}
		})
	}
}
