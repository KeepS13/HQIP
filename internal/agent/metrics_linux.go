//go:build linux

package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"golang.org/x/sys/unix"
)

type systemMetrics struct {
	lastTotal uint64
	lastIdle  uint64
}

func (m *systemMetrics) read(statePath string) model.Metrics {
	metrics := model.Metrics{}
	if contents, err := os.ReadFile("/proc/stat"); err == nil {
		line := strings.SplitN(string(contents), "\n", 2)[0]
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "cpu" {
			var total, idle uint64
			// guest and guest_nice are already included in user/nice.
			for i := 1; i < len(fields) && i <= 8; i++ {
				value, _ := strconv.ParseUint(fields[i], 10, 64)
				total += value
				if i == 4 || i == 5 {
					idle += value
				}
			}
			if m.lastTotal > 0 && total > m.lastTotal && idle >= m.lastIdle {
				busy := float64(total-m.lastTotal) - float64(idle-m.lastIdle)
				if busy > 0 {
					metrics.CPUPercent = 100 * busy / float64(total-m.lastTotal)
				}
			}
			m.lastTotal, m.lastIdle = total, idle
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(f)
		var available uint64
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				metrics.MemoryTotal = value * 1024
			case "MemAvailable:":
				available = value * 1024
			}
		}
		_ = f.Close()
		if available <= metrics.MemoryTotal {
			metrics.MemoryUsed = metrics.MemoryTotal - available
		}
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(statePath), &stat); err == nil {
		metrics.DiskTotal = stat.Blocks * uint64(stat.Bsize)
		metrics.DiskUsed = (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	}
	return metrics
}
