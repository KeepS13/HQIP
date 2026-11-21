package store

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/payment"
)

func TestRenewalSnapshotSurvivesPriceDurationAndListingChanges(t *testing.T) {
	s, _, user, template, sub, _ := renewalFixture(t)
	order, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, sub, "renewal-snapshot"), paymentTestClient())
	if err != nil {
		t.Fatal(err)
	}
	price := *template.PriceCents + 100
	template.PriceCents, template.ValidityDays, template.Listed = &price, template.ValidityDays+9, false
	if _, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.PaymentOrder(user.ID, order.ID)
	if err != nil || got.Status != "fulfilled" || got.AmountCents != order.AmountCents || got.ValidityDays != order.ValidityDays || got.PeriodEndsAt.Sub(*got.PeriodStartsAt) != time.Duration(order.ValidityDays)*24*time.Hour {
		t.Fatal(got, err)
	}
}

func TestRenewalConcurrentCreateDispatchesOnce(t *testing.T) {
	s, _, user, template, sub, _ := renewalFixture(t)
	var calls atomic.Int64
	base := paymentTestClient()
	client := &payment.Client{ReturnOrigin: base.ReturnOrigin, Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return base.Transport.RoundTrip(r)
	})}
	in := renewalInput(template, sub, "renewal-double-click")
	var group sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			o, err := s.CreatePaymentOrder(context.Background(), user.ID, in, client)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- o.ID
		}()
	}
	group.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("created multiple orders")
		}
		id = got
	}
	if calls.Load() != 1 || id == "" {
		t.Fatal("provider dispatch count", calls.Load())
	}
}

func TestRenewalPaymentClosesWhenAdminShortensSubscription(t *testing.T) {
	s, now, user, template, sub, _ := renewalFixture(t)
	order, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, sub, "renewal-expiry-cut"), paymentTestClient())
	if err != nil {
		t.Fatal(err)
	}
	end := now.Add(time.Minute)
	updated, err := s.UpdateSubscriptionExpiry(user.ID, sub.ID, end, sub.Revision)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PaymentOrder(user.ID, order.ID)
	if err != nil || !got.ExpiresAt.Equal(end) || got.PayURL == "" {
		t.Fatal(got, err)
	}
	*now = end
	got, err = s.PaymentOrder(user.ID, order.ID)
	if err != nil || got.Status != "expired" || got.PayURL != "" {
		t.Fatal(got, err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PaymentOrder(user.ID, order.ID)
	if got.PaymentState != "paid" || got.FulfillmentState != "blocked" {
		t.Fatal(got)
	}
	current, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || !current.ExpiresAt.Equal(updated.ExpiresAt) || len(current.Periods) != 1 {
		t.Fatal(current, err)
	}
}
