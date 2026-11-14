package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestTaskStateRetryKeepsServingAndCancelsPromptly(t *testing.T) {
	s, _ := newServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	done := make(chan bool, 1)
	go func() {
		done <- retryTaskState(ctx, "test completion", func() error {
			if attempts.Add(1) < 3 {
				return errors.New("temporary failure")
			}
			return nil
		})
	}()
	w := httptest.NewRecorder()
	s.PublicHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("API unavailable during task failure")
	}
	select {
	case ok := <-done:
		if !ok || attempts.Load() != 3 {
			t.Fatal("completion not retried")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("retry did not recover")
	}
	go func() {
		done <- retryTaskState(ctx, "persistent failure", func() error { return errors.New("unavailable") })
	}()
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled work succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored shutdown")
	}
}

func TestWorkerRecoversClaimFailureWithoutLosingQueuedOperation(t *testing.T) {
	s, h := newServer(t)
	if s.Store.Postgres() {
		t.Skip("SQLite failure injection; PostgreSQL conflicts covered by integration suite")
	}
	s.runner = &operationsInstaller{}
	v, err := s.Store.CreateServerWithCredential(model.Server{Name: "claim-retry", SSHHost: "192.0.2.10", HostKey: "SHA256:fixed-test-host-key", MachineID: "machine-a"}, "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.QueueServerOperation(v.ID, "", "check", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec("CREATE TRIGGER fail_claim BEFORE UPDATE ON server_operations WHEN NEW.state='running' BEGIN SELECT RAISE(ABORT,'locked'); END"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunWorker(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker failed to stop")
		}
	}()
	select {
	case err := <-done:
		t.Fatalf("worker exited on claim error: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	w := request(h, "GET", "/healthz", nil, "", "")
	if w.Code != 200 {
		t.Fatal("HTTP serving stopped on claim error")
	}
	if _, err = s.Store.DB.Exec("DROP TRIGGER fail_claim"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		jobs, e := s.Store.ServerOperations(v.ID)
		if e == nil && len(jobs) == 1 && jobs[0].State == "succeeded" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("queued operation did not recover after database lock cleared")
}
