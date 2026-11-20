package client

import (
	"context"
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"math/bits"
	"net/url"
	"sort"
	"time"
)

type billingSample struct {
	At               time.Time
	Upload, Download int64
}
type billingMetrics struct {
	Samples                            []billingSample
	Policies                           []model.BillingDisplay
	Updated                            time.Time
	UploadUnits, DownloadUnits         int64
	BaseUploadUnits, BaseDownloadUnits int64
}

func (controller *Controller) runBillingSync(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		controller.syncBillingMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (controller *Controller) syncBillingMetrics(parent context.Context) {
	controller.mu.Lock()
	api, generation := controller.api, controller.generation
	nodeID := controller.view.ActualNodeID
	var selected Node
	for _, node := range controller.saved.Profile.Nodes {
		if node.ID == nodeID {
			selected = node
		}
	}
	subscription := controller.view.ActualSubscriptionID
	connected := connectionRunning(controller.view.RunState) && !controller.view.SelectionPending
	controller.mu.Unlock()
	if api == nil || !connected || selected.ID == "" {
		return
	}
	physical, protocol := selected.NodeID, selected.Protocol
	if physical == "" {
		physical = selected.ID
	}
	if protocol == "" {
		protocol = "hysteria2"
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	raw, _, _, err := api.auth(ctx, "GET", "/api/v3/client/billing?nodeId="+url.QueryEscape(physical)+"&protocol="+url.QueryEscape(protocol), nil, "")
	var policies []model.BillingDisplay
	if err != nil || json.Unmarshal(raw, &policies) != nil {
		return
	}
	for _, policy := range policies {
		if !policy.Policy.Valid() || policy.NodeID != physical || policy.Protocol != protocol || policy.SubscriptionID != subscription || policy.AppliedAt.IsZero() || !policy.ValidUntil.After(policy.AppliedAt) {
			return
		}
	}
	sort.Slice(policies, func(left, right int) bool { return policies[left].AppliedAt.Before(policies[right].AppliedAt) })
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.api != api || controller.generation != generation || controller.view.ActualNodeID != nodeID {
		return
	}
	// Keep confirmed historical boundaries when a closed lease disappears from
	// the API. A fresh response may extend validity, never reprice a version.
	known := controller.billingMetrics.Policies
	for _, policy := range policies {
		found := false
		for index, old := range known {
			if old.LeaseID == policy.LeaseID && old.Version == policy.Version {
				if old.Policy != policy.Policy || !old.AppliedAt.Equal(policy.AppliedAt) {
					return
				}
				known[index] = policy
				found = true
				break
			}
		}
		if !found {
			known = append(known, policy)
		}
	}
	sort.Slice(known, func(left, right int) bool { return known[left].AppliedAt.Before(known[right].AppliedAt) })
	controller.billingMetrics.Policies = known
	controller.billingMetrics.Updated = time.Now()
}

func proportion(total, part, whole int64) int64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	if part >= whole {
		return total
	}
	high, low := bits.Mul64(uint64(total), uint64(part))
	quotient, _ := bits.Div64(high, low, uint64(whole))
	return int64(quotient)
}

func scaledInterval(previous, current billingSample, policies []model.BillingDisplay) (int64, int64, bool) {
	if !current.At.After(previous.At) || current.Upload < previous.Upload || current.Download < previous.Download {
		return 0, 0, false
	}
	active := -1
	for index, policy := range policies {
		if !policy.AppliedAt.After(previous.At) {
			active = index
		}
	}
	if active < 0 {
		return 0, 0, false
	}
	start := previous.At
	var up, down, assignedUp, assignedDown int64
	for start.Before(current.At) {
		policy := policies[active]
		end := current.At
		if active+1 < len(policies) && policies[active+1].AppliedAt.Before(end) {
			end = policies[active+1].AppliedAt
		}
		if !policy.ValidUntil.After(start) || policy.ValidUntil.Before(end) {
			return 0, 0, false
		}
		part, whole := end.Sub(previous.At).Nanoseconds(), current.At.Sub(previous.At).Nanoseconds()
		upload := proportion(current.Upload-previous.Upload, part, whole)
		download := proportion(current.Download-previous.Download, part, whole)
		upPart, err := billing.Units(upload-assignedUp, policy.Multiplier)
		if err != nil {
			return 0, 0, false
		}
		downPart, err := billing.Units(download-assignedDown, policy.Multiplier)
		if err != nil {
			return 0, 0, false
		}
		if up, err = billing.Add(up, upPart); err != nil {
			return 0, 0, false
		}
		if down, err = billing.Add(down, downPart); err != nil {
			return 0, 0, false
		}
		assignedUp, assignedDown = upload, download
		start = end
		active++
	}
	return up, down, true
}

func (controller *Controller) applyBillingSample(sample CoreSample, now time.Time) {
	state := &controller.billingMetrics
	dashboard := &controller.view.Metrics
	dashboard.BillingEstimated = true
	dashboard.ApplicationConnections = sample.Connections
	dashboard.MemoryBytes = sample.Memory
	current := billingSample{At: now, Upload: sample.UploadTotal, Download: sample.DownloadTotal}
	if len(state.Samples) == 0 {
		state.Samples = append(state.Samples, current)
		dashboard.Available = false
		return
	}
	previous := state.Samples[len(state.Samples)-1]
	if current.Upload < previous.Upload || current.Download < previous.Download {
		state.Samples = []billingSample{current}
		state.BaseUploadUnits = state.UploadUnits
		state.BaseDownloadUnits = state.DownloadUnits
		dashboard.Stale = true
		dashboard.UploadRate = 0
		dashboard.DownloadRate = 0
		return
	}
	state.Samples = append(state.Samples, current)
	// The first core sample can precede admission. Retain raw samples while
	// waiting for confirmation, then interpolate that first confirmed boundary.
	if len(state.Policies) > 0 {
		boundary := state.Policies[0].AppliedAt
		for len(state.Samples) > 1 && !state.Samples[1].At.After(boundary) {
			state.Samples = state.Samples[1:]
		}
		if len(state.Samples) > 1 && state.Samples[0].At.Before(boundary) {
			left, right := state.Samples[0], state.Samples[1]
			part, whole := boundary.Sub(left.At).Nanoseconds(), right.At.Sub(left.At).Nanoseconds()
			state.Samples[0] = billingSample{At: boundary, Upload: left.Upload + proportion(right.Upload-left.Upload, part, whole), Download: left.Download + proportion(right.Download-left.Download, part, whole)}
		}
	}
	upload, download := state.BaseUploadUnits, state.BaseDownloadUnits
	history := []MetricPoint{}
	valid := len(state.Samples) > 1
	for index := 1; index < len(state.Samples); index++ {
		left, right := state.Samples[index-1], state.Samples[index]
		up, down, ok := scaledInterval(left, right, state.Policies)
		if !ok {
			valid = false
			break
		}
		var err error
		if upload, err = billing.Add(upload, up); err != nil {
			valid = false
			break
		}
		if download, err = billing.Add(download, down); err != nil {
			valid = false
			break
		}
		seconds := right.At.Sub(left.At).Seconds()
		if now.Sub(right.At) < time.Minute {
			history = append(history, MetricPoint{At: right.At, UploadRate: int64(float64(up) / 100 / seconds), DownloadRate: int64(float64(down) / 100 / seconds)})
		}
	}
	if valid {
		if len(history) > 60 {
			history = history[len(history)-60:]
		}
		state.UploadUnits, state.DownloadUnits = upload, download
		dashboard.UploadBytes, dashboard.DownloadBytes = upload/100, download/100
		dashboard.Available = true
		dashboard.Stale = now.Sub(state.Updated) > 15*time.Second
		dashboard.UpdatedAt = &now
		dashboard.History = history
		if len(history) > 0 {
			last := history[len(history)-1]
			dashboard.UploadRate, dashboard.DownloadRate = last.UploadRate, last.DownloadRate
		}
	} else {
		dashboard.Available = false
		dashboard.Stale = true
		dashboard.UploadRate, dashboard.DownloadRate = 0, 0
		// A confirmed new lease can follow an interval with no valid policy.
		// Preserve the last estimate and resume from its own raw baseline;
		// never fill the unknown gap with a guessed multiplier.
		if len(state.Policies) > 0 {
			latest := state.Policies[len(state.Policies)-1]
			if latest.AppliedAt.After(state.Samples[0].At) && latest.ValidUntil.After(now) {
				for index, sample := range state.Samples {
					if !sample.At.Before(latest.AppliedAt) {
						state.Samples = state.Samples[index:]
						state.BaseUploadUnits, state.BaseDownloadUnits = state.UploadUnits, state.DownloadUnits
						break
					}
				}
			}
		}
	}
	if len(state.Samples) > 300 {
		if valid {
			up, down, _ := scaledInterval(state.Samples[0], state.Samples[1], state.Policies)
			state.BaseUploadUnits += up
			state.BaseDownloadUnits += down
			state.Samples = state.Samples[1:]
		} else {
			state.Samples = []billingSample{current}
			state.BaseUploadUnits = state.UploadUnits
			state.BaseDownloadUnits = state.DownloadUnits
		}
	}
}
