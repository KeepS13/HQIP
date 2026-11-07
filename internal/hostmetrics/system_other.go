//go:build !linux && !windows

package hostmetrics

func readSystem(string) (*cpuTimes, *Usage, *Usage) { return nil, nil, nil }
