// Package hostmetrics reads the machine running the control service, not proxy nodes.
package hostmetrics

import "time"

type Usage struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

type Sample struct {
	SampledAt  time.Time `json:"sampledAt"`
	CPUPercent *float64  `json:"cpuPercent"`
	Memory     *Usage    `json:"memory"`
	Disk       *Usage    `json:"disk"`
}

type cpuTimes struct{ total, idle uint64 }

const maxCPUSampleInterval = 20 * time.Second

// Reader is used by one sampler goroutine at a time. A missing or first CPU
// sample is unknown; it is never presented as zero usage.
type Reader struct {
	previous   *cpuTimes
	previousAt time.Time
}

func (r *Reader) Read(dataDir string) Sample {
	times, memory, disk := readSystem(dataDir)
	now := time.Now()
	s := Sample{SampledAt: now.UTC(), Memory: memory, Disk: disk}
	s.CPUPercent = r.cpuUsage(times, now)
	return s
}

func (r *Reader) cpuUsage(times *cpuTimes, now time.Time) *float64 {
	var percent *float64
	// Sampling is demand-driven. After the admin page is closed or suspended,
	// an old baseline would report the whole idle interval as current CPU load.
	interval := now.Sub(r.previousAt)
	if times != nil && r.previous != nil && interval > 0 && interval <= maxCPUSampleInterval {
		before := r.previous
		if times.total > before.total && times.idle >= before.idle {
			total, idle := times.total-before.total, times.idle-before.idle
			if idle <= total {
				value := 100 * float64(total-idle) / float64(total)
				percent = &value
			}
		}
	}
	r.previous = times
	r.previousAt = now
	return percent
}
