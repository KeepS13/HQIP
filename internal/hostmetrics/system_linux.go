//go:build linux

package hostmetrics

import (
	"os"

	"golang.org/x/sys/unix"
)

func readSystem(dataDir string) (*cpuTimes, *Usage, *Usage) {
	stat, _ := os.ReadFile("/proc/stat")
	mem, _ := os.ReadFile("/proc/meminfo")
	var disk *Usage
	var fs unix.Statfs_t
	if err := unix.Statfs(dataDir, &fs); err == nil && fs.Bsize > 0 && fs.Blocks > 0 && fs.Bfree <= fs.Blocks {
		disk = &Usage{Used: (fs.Blocks - fs.Bfree) * uint64(fs.Bsize), Total: fs.Blocks * uint64(fs.Bsize)}
	}
	return parseCPU(string(stat)), parseMemory(string(mem)), disk
}
