package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackgroundFailureRestartsWithoutStoppingAPILifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	attempts := 0
	err := superviseBackground(ctx, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("transient ownership connection failure")
		}
		cancel()
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("supervisor propagated failure: %v, attempts %d", err, attempts)
	}
}
