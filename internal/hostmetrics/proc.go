package hostmetrics

import (
	"strconv"
	"strings"
)

func parseCPU(raw string) *cpuTimes {
	fields := strings.Fields(strings.SplitN(raw, "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return nil
	}
	times := &cpuTimes{}
	// Guest time is included in user/nice already; iowait is idle time.
	for i := 1; i < len(fields) && i <= 8; i++ {
		value, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return nil
		}
		times.total += value
		if i == 4 || i == 5 {
			times.idle += value
		}
	}
	return times
}

func parseMemory(raw string) *Usage {
	values := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		if fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil
		}
		values[fields[0]] = value * 1024
	}
	total, hasTotal := values["MemTotal:"]
	available, hasAvailable := values["MemAvailable:"]
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return nil
	}
	return &Usage{Used: total - available, Total: total}
}
