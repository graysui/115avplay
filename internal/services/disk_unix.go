//go:build !windows

package services

import "syscall"

// GetFreeDiskSpace returns available disk space in bytes for the specified directory path on Unix.
func GetFreeDiskSpace(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
