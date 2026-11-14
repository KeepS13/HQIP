package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/KeepS13/HQIP/internal/control"
	"github.com/KeepS13/HQIP/internal/store"
)

func runBackground(ctx context.Context, db *store.Store, server *control.Server) error {
	return superviseBackground(ctx, func(ctx context.Context) error {
		return db.WithMaintenanceLeadership(ctx, func(leader context.Context) error {
			work, cancel := context.WithCancel(leader)
			defer cancel()
			done := make(chan error, 3)
			go func() { done <- server.RunWorker(work) }()
			go func() { done <- server.RunNotifications(work) }()
			go func() { done <- server.RunPayments(work) }()
			first := <-done
			cancel()
			return errors.Join(first, <-done, <-done)
		})
	})
}

// A lost leadership connection stops and joins all work before reacquiring a
// fresh lock. Background availability never decides HTTP server lifetime.
func superviseBackground(ctx context.Context, run func(context.Context) error) error {
	for failures := 0; ctx.Err() == nil; failures++ {
		started := time.Now()
		_ = run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > time.Minute {
			failures = 0
		}
		delay := min(time.Second<<min(failures, 5), 30*time.Second)
		// Raw errors can contain connection details; do not log them.
		log.Printf("HQIP 后台任务已停止，%s 后重新获取任务所有权；API 继续服务", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
