package store

import (
	"errors"
	"sync"
	"testing"
)

func TestSubscriptionConcurrentRequestsGrantsAndCancellation(t *testing.T) {
	s, _, user, template, _, _ := portalFixture(t, 1, nil)
	template = allocationStockForTest(t, s, template, 3)
	run := func(f func(int) error) {
		t.Helper()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if err := f(i); err != nil {
					t.Error(err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
	}
	run(func(int) error {
		_, err := s.CreateSubscriptionRequest(user.ID, template.ID, "one-request", template.Revision)
		return err
	})
	requests, err := s.SubscriptionRequests(user.ID)
	if err != nil || len(requests) != 1 {
		t.Fatal("concurrent request was duplicated", len(requests), err)
	}
	current, err := s.PortalUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	run(func(int) error {
		_, err := s.AssignSubscription(user.ID, template.ID, "one-assignment", current.Revision, template.Revision)
		return err
	})
	subs, err := s.UserSubscriptions(user.ID)
	if err != nil || len(subs) != 2 {
		t.Fatal("concurrent assignment was duplicated", len(subs), err)
	}
	r := requests[0]
	run(func(i int) error {
		var err error
		if i%2 == 0 {
			_, err = s.DecideSubscriptionRequest(r.ID, "approve", "confirmed", r.Revision, template.Revision)
		} else {
			err = s.CancelSubscriptionRequest(user.ID, r.ID, r.Revision)
		}
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	})
	requests, err = s.SubscriptionRequests(user.ID)
	if err != nil || len(requests) != 1 {
		t.Fatal(err)
	}
	want := 2
	switch requests[0].Status {
	case "approved":
		want++
	case "cancelled":
	default:
		t.Fatal("request did not reach one terminal state", requests[0].Status)
	}
	if subs, err = s.UserSubscriptions(user.ID); err != nil || len(subs) != want {
		t.Fatal("cancellation/approval race granted inconsistent subscriptions", len(subs), want, err)
	}
}
