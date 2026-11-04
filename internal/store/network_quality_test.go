package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func qualityNode(t *testing.T, s *Store) model.Node {
	t.Helper()
	n := newNode(t, s, "quality-node")
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.FinishJob(j, true)
	if err = s.SetStatus(n.ID, "ready", "verified", false); err != nil {
		t.Fatal(err)
	}
	n, err = s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestQualityQueuePreservesNodeAndReportAcrossFailure(t *testing.T) {
	s, now := fixture(t)
	n := qualityNode(t, s)
	if _, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision+1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	v, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision)
	if err != nil || v.Status != "queued" {
		t.Fatal(v, err)
	}
	password, _, _, err := s.NodeSecrets(n.ID)
	if err != nil || password != "secret" {
		t.Fatal("credential was not encrypted/retrievable", err)
	}
	if _, err = s.QueueNetworkQuality(n.ID, "secret", n.Revision); err == nil {
		t.Fatal("duplicate quality job allowed")
	}
	j, err := s.NextJob()
	if err != nil || j.Kind != "quality" {
		t.Fatal(j, err)
	}
	if err = s.StartNetworkQuality(n.ID, j.ID, n.PublicIP); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"Head":{"IPv4":"203.0.113.8","Version":"v2026-09-16"}}`)
	if err = s.FinishNetworkQuality(n.ID, j.ID, "complete", "", raw); err != nil {
		t.Fatal(err)
	}
	s.ClearSSHPassword(n.ID)
	s.FinishJob(j, true)
	if _, err = s.QueueNetworkQuality(n.ID, "new-secret", n.Revision); !errors.Is(err, ErrQualityCooldown) {
		t.Fatal("missing cooldown", err)
	}
	*now = now.Add(10 * time.Minute)
	v, err = s.QueueNetworkQuality(n.ID, "new-secret", n.Revision)
	if err != nil || string(v.Report) != string(raw) {
		t.Fatal(v, err)
	}
	j, err = s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartNetworkQuality(n.ID, j.ID, n.PublicIP); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNetworkQuality(n.ID, j.ID, "failed", "external API unavailable", nil); err != nil {
		t.Fatal(err)
	}
	v, err = s.NetworkQuality(n.ID)
	if err != nil || v.Status != "failed" || string(v.Report) != string(raw) || v.ReportAt == nil || v.ReportSourceIP != n.PublicIP {
		t.Fatal(v, err)
	}
	got, err := s.Node(n.ID)
	if err != nil || got.Status != "ready" || got.Revision != n.Revision {
		t.Fatal("diagnostic changed proxy state", got, err)
	}
}

func TestQualityDeleteAndRestartCancelWithoutCredentialRetention(t *testing.T) {
	for _, start := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "running"}[start], func(t *testing.T) {
			s, _ := fixture(t)
			n := qualityNode(t, s)
			v, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if start {
				j, err := s.NextJob()
				if err != nil {
					t.Fatal(err)
				}
				if err = s.StartNetworkQuality(n.ID, j.ID, n.PublicIP); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision}); err != nil {
				t.Fatal(err)
			}
			got, err := s.NetworkQuality(n.ID)
			if err != nil || got.Status != "cancelled" {
				t.Fatal(got, err)
			}
			password, _, _, _ := s.NodeSecrets(n.ID)
			if password != "" {
				t.Fatal("deleted quality job retained SSH password")
			}
			if err = s.StartNetworkQuality(n.ID, v.JobID, n.PublicIP); !errors.Is(err, ErrConflict) {
				t.Fatal("deleted scan restarted", err)
			}
		})
	}
	s, _ := fixture(t)
	n := qualityNode(t, s)
	if _, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverNetworkQuality(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverJobs(); err != nil {
		t.Fatal(err)
	}
	v, err := s.NetworkQuality(n.ID)
	if err != nil || v.Status != "cancelled" {
		t.Fatal(v, err)
	}
	password, _, _, _ := s.NodeSecrets(n.ID)
	if password != "" {
		t.Fatal("recovery retained SSH password")
	}
	if _, err = s.NextJob(); !errors.Is(err, ErrNotFound) {
		t.Fatal("interrupted diagnostic was silently replayed", err)
	}
}

func TestQualityReportRejectsInvalidOrLateOutput(t *testing.T) {
	s, _ := fixture(t)
	n := qualityNode(t, s)
	v, err := s.QueueNetworkQuality(n.ID, "secret", n.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNetworkQuality(n.ID, v.JobID, "complete", "", json.RawMessage(`[]`)); err == nil {
		t.Fatal("array report accepted")
	}
	if err = s.FinishNetworkQuality(n.ID, v.JobID+1, "failed", "late", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.NetworkQuality(n.ID)
	if got.Status != "queued" {
		t.Fatal("stale task overwrote current run")
	}
}
