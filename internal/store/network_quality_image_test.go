package store

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/qualityimage"
)

func TestQualityImageAtomicReplacementAndFailedRunPreservesLastGood(t *testing.T) {
	s, now := fixture(t)
	n := qualityNode(t, s)
	queue := func() Job {
		t.Helper()
		if _, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision); err != nil {
			t.Fatal(err)
		}
		j, err := s.NextJob()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.StartNetworkQuality(n.ID, j.ID, n.PublicIP); err != nil {
			t.Fatal(err)
		}
		return j
	}
	j := queue()
	raw := json.RawMessage(`{"Head":{"IP":"203.0.113.8"},"HQIPReportANSI":"\u001b[32m首次检测\u001b[0m","_hqipImage":"<svg onload='attack()'/>"}`)
	if err := s.FinishNetworkQuality(n.ID, j.ID, "complete", "", raw); err != nil {
		t.Fatal(err)
	}
	v, err := s.NetworkQuality(n.ID)
	if err != nil || !v.ReportImageAvailable || len(v.ReportImageVersion) != 64 {
		t.Fatal(v, err)
	}
	if strings.Contains(string(v.Report), "hqipImage") || strings.Contains(string(v.Report), "HQIPReportANSI") {
		t.Fatal("image leaked in polling payload")
	}
	first, err := s.NetworkQualityImage(n.ID, v.ReportImageVersion)
	if err != nil || !strings.Contains(string(first), "首次检测") || strings.Contains(string(first), "attack") {
		t.Fatal(string(first), err)
	}
	oldVersion := v.ReportImageVersion
	s.FinishJob(j, true)
	*now = now.Add(11 * time.Minute)
	j = queue()
	if err = s.FinishNetworkQuality(n.ID, j.ID, "failed", "unavailable", nil); err != nil {
		t.Fatal(err)
	}
	v, err = s.NetworkQuality(n.ID)
	if err != nil || v.ReportImageVersion != oldVersion {
		t.Fatal("failed attempt changed last good image", v, err)
	}
	preserved, err := s.NetworkQualityImage(n.ID, oldVersion)
	if err != nil || string(first) != string(preserved) {
		t.Fatal("last image lost", err)
	}
	s.FinishJob(j, true)
	*now = now.Add(11 * time.Minute)
	j = queue()
	if err = s.FinishNetworkQuality(n.ID, j.ID, "complete", "", json.RawMessage(`{"Head":{"IP":"203.0.113.8"},"HQIPReportANSI":"第二次检测"}`)); err != nil {
		t.Fatal(err)
	}
	v, err = s.NetworkQuality(n.ID)
	if err != nil || v.ReportImageVersion == oldVersion {
		t.Fatal("new report used old image version", err)
	}
	if _, err = s.NetworkQualityImage(n.ID, oldVersion); !errors.Is(err, ErrConflict) {
		t.Fatal("stale image version returned a different report", err)
	}
	latest, err := s.NetworkQualityImage(n.ID, v.ReportImageVersion)
	if err != nil || !strings.Contains(string(latest), "第二次检测") || strings.Contains(string(latest), "首次检测") {
		t.Fatal("image and report differ", err)
	}
}

func TestQualityImagePresentationFailureDoesNotLoseReport(t *testing.T) {
	s, _ := fixture(t)
	n := qualityNode(t, s)
	v, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"Head": map[string]string{"IP": n.PublicIP}, "HQIPReportANSI": "original" + strings.Repeat("\n", 2049)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNetworkQuality(n.ID, v.JobID, "complete", "", raw); err != nil {
		t.Fatal(err)
	}
	v, err = s.NetworkQuality(n.ID)
	if err != nil || v.Status != "complete" || !v.ReportImageAvailable {
		t.Fatal(v, err)
	}
	image, err := s.NetworkQualityImage(n.ID, v.ReportImageVersion)
	if err != nil || !strings.Contains(string(image), "根据已保存的 JSON 报告生成") {
		t.Fatal("unrenderable terminal did not fall back", err)
	}
	if len(image) > qualityimage.MaxSVG {
		t.Fatal("unbounded image")
	}
}

func TestLegacyQualityImageIsCachedWithoutChangingReportIdentity(t *testing.T) {
	s, now := fixture(t)
	n := qualityNode(t, s)
	raw := `{"Head":{"IP":"203.0.113.8"},"Info":{"Organization":"legacy-provider"},"Media":{"ChatGPT":{"Status":"Yes"}}}`
	_, err := s.DB.Exec("INSERT INTO node_network_quality(node_id,status,job_id,source_ip,requested_at,report_at,report_ip,report) VALUES(?,'complete',0,?,?,?,?,?)", n.ID, n.PublicIP, now.Unix(), now.Unix(), n.PublicIP, raw)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.NetworkQuality(n.ID)
	if err != nil || !v.ReportImageAvailable {
		t.Fatal(v, err)
	}
	const workers = 4
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			image, e := s.NetworkQualityImage(n.ID, v.ReportImageVersion)
			if e == nil && !strings.Contains(string(image), "根据已保存的 JSON 报告生成") {
				e = errors.New("unlabelled legacy image")
			}
			errorsCh <- e
		}()
	}
	wg.Wait()
	close(errorsCh)
	for e := range errorsCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	var stored string
	var at int64
	if err = s.DB.QueryRow("SELECT report,report_at FROM node_network_quality WHERE node_id=?", n.ID).Scan(&stored, &at); err != nil || !strings.Contains(stored, "_hqipImage") || at != now.Unix() {
		t.Fatal("cache missing or timestamp changed", err)
	}
	after, err := s.NetworkQuality(n.ID)
	if err != nil || after.ReportImageVersion != v.ReportImageVersion || string(after.Report) != string(v.Report) {
		t.Fatal("image cache changed report identity", err)
	}
	if _, err = s.NetworkQualityImage("missing", v.ReportImageVersion); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
