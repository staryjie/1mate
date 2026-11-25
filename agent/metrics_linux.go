//go:build linux

package main

import (
	"golang.org/x/sys/unix"
)

// readDiskUsage 返回指定路径所在文件系统的磁盘使用率（Linux 实现）。
func readDiskUsage(path string) float64 {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return -1
	}
	total := stat.Blocks * uint64(stat.Bsize)
	if total == 0 {
		return -1
	}
	free := stat.Bfree * uint64(stat.Bsize)
	return float64(total-free) / float64(total) * 100.0
}
