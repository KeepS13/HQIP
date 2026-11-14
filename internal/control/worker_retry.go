package control

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

// Retry only durable database work here, never the remote side effect. A lost
// commit acknowledgement is safe because completion writes are idempotent.
func retryTaskState(ctx context.Context, label string, attempt func() error) bool {
	for failures := 0; ctx.Err() == nil; failures++ {
		if err := attempt(); err == nil {
			if failures > 0 {
				log.Printf("HQIP 后台任务已恢复: %s", label)
			}
			return true
		} else if failures == 0 || failures%10 == 0 {
			log.Printf("HQIP 后台任务暂不可用，将重试: %s (%s)", label, databaseFailureClass(err))
		}
		ceiling := min(500*time.Millisecond<<min(failures, 6), 30*time.Second)
		wait := time.NewTimer(ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2))))
		select {
		case <-ctx.Done():
			wait.Stop()
			return false
		case <-wait.C:
		}
	}
	return false
}

// Database errors can contain query parameters. Log only their category.
func databaseFailureClass(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return "SQLSTATE=" + state.SQLState()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "database unavailable"
}

func (s *Server) finishJob(ctx context.Context, j store.Job, success bool) bool {
	return retryTaskState(ctx, "保存节点任务结束状态", func() error { return s.Store.FinishJob(j, success) })
}

func (s *Server) maintain(ctx context.Context) {
	if err := s.Store.MaintenanceContext(ctx); err != nil {
		// Maintenance errors expose only static table names and safe categories.
		log.Printf("HQIP 历史数据清理未完成，将在下轮继续: %v", err)
	}
	if err := s.Store.ScheduleCredentialChecks(); err != nil {
		log.Printf("HQIP 凭据巡检排队失败，将在下轮重试 (%s)", databaseFailureClass(err))
	}
	s.signal()
}
