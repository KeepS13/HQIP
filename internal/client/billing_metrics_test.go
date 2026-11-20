package client

import (
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"testing"
	"time"
)

func TestBillingMetricIntervalSplitsAtConfirmedBoundary(t *testing.T) {
	start := time.Unix(1000, 0)
	policies := []model.BillingDisplay{{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, AppliedAt: start, ValidUntil: start.Add(time.Hour)}, {Policy: billing.Policy{Version: 2, Multiplier: 250, Protocol: "hysteria2"}, AppliedAt: start.Add(500 * time.Millisecond), ValidUntil: start.Add(time.Hour)}}
	up, down, valid := scaledInterval(billingSample{At: start}, billingSample{At: start.Add(time.Second), Upload: 1000, Download: 2000}, policies)
	if !valid || up != 200000 || down != 400000 {
		t.Fatalf("split mismatch %d %d %v", up, down, valid)
	}
	policies[1].ValidUntil = start
	if _, _, valid = scaledInterval(billingSample{At: start}, billingSample{At: start.Add(time.Second), Upload: 1000}, policies); valid {
		t.Fatal("expired policy used")
	}
}

func TestBillingMetricsNeverRescaleEarlierTraffic(t *testing.T) {
	start := time.Unix(1000, 0)
	controller := &Controller{}
	controller.billingMetrics.Policies = []model.BillingDisplay{{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, AppliedAt: start, ValidUntil: start.Add(time.Hour)}}
	controller.billingMetrics.Updated = start
	controller.applyBillingSample(CoreSample{ProxyOnly: true}, start)
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 1000, DownloadTotal: 1000}, start.Add(time.Second))
	if controller.view.Metrics.UploadBytes != 1500 || controller.view.Metrics.UploadRate != 1500 {
		t.Fatalf("bad initial scaling %+v", controller.view.Metrics)
	}
	controller.billingMetrics.Policies = append(controller.billingMetrics.Policies, model.BillingDisplay{Policy: billing.Policy{Version: 2, Multiplier: 250, Protocol: "hysteria2"}, AppliedAt: start.Add(time.Second), ValidUntil: start.Add(time.Hour)})
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 2000, DownloadTotal: 2000}, start.Add(2*time.Second))
	if controller.view.Metrics.UploadBytes != 4000 || controller.view.Metrics.UploadRate != 2500 || controller.view.Metrics.History[0].UploadRate != 1500 {
		t.Fatalf("history repriced %+v", controller.view.Metrics)
	}
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 10, DownloadTotal: 10}, start.Add(3*time.Second))
	if controller.view.Metrics.UploadBytes != 4000 || controller.view.Metrics.UploadRate != 0 {
		t.Fatal("counter reset lost charges")
	}
}

func TestBillingMetricsUnknownPolicyNeverDisplaysRawCounters(t *testing.T) {
	controller := &Controller{}
	start := time.Unix(1000, 0)
	controller.applyBillingSample(CoreSample{ProxyOnly: true}, start)
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 123456}, start.Add(time.Second))
	if controller.view.Metrics.Available || controller.view.Metrics.UploadBytes != 0 || !controller.view.Metrics.Stale {
		t.Fatal("unknown multiplier exposed raw values")
	}
}

func TestBillingMetricsResumeAfterUnpricedGap(t *testing.T) {
	start := time.Unix(1000, 0)
	controller := &Controller{}
	controller.billingMetrics.Policies = []model.BillingDisplay{{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, AppliedAt: start, ValidUntil: start.Add(2 * time.Second)}}
	controller.billingMetrics.Updated = start
	controller.applyBillingSample(CoreSample{ProxyOnly: true}, start)
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 100}, start.Add(time.Second))
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 300}, start.Add(3*time.Second))
	if controller.view.Metrics.Available || controller.view.Metrics.UploadBytes != 150 {
		t.Fatal("expired policy used")
	}
	controller.billingMetrics.Policies = append(controller.billingMetrics.Policies, model.BillingDisplay{Policy: billing.Policy{Version: 2, Multiplier: 200, Protocol: "hysteria2"}, AppliedAt: start.Add(4 * time.Second), ValidUntil: start.Add(time.Minute)})
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 500}, start.Add(5*time.Second))
	controller.applyBillingSample(CoreSample{ProxyOnly: true, UploadTotal: 600}, start.Add(6*time.Second))
	if !controller.view.Metrics.Available || controller.view.Metrics.UploadBytes != 350 {
		t.Fatal("recovery guessed unknown gap or failed to resume", controller.view.Metrics)
	}
}
