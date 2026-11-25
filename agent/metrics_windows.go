//go:build windows

package main

// readDiskUsage 返回指定路径所在文件系统的磁盘使用率。
// Windows 占位实现，返回 0。
func readDiskUsage(path string) float64 {
	return 0
}
