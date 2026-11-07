package main

import (
	"context"
	"errors"

	"github.com/KeepS13/HQIP/internal/control"
	"github.com/KeepS13/HQIP/internal/store"
)

func runBackground(ctx context.Context, db *store.Store, server *control.Server) error {
	return db.WithMaintenanceLeadership(ctx, func(leader context.Context) error {
		work, cancel := context.WithCancel(leader)
		defer cancel()
		done := make(chan error, 2)
		go func() { done <- server.RunWorker(work) }()
		go func() { done <- server.RunNotifications(work) }()
		first := <-done
		cancel()
		return errors.Join(first, <-done)
	})
}
