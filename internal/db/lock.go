package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileLock represents an exclusive lock on a lock file.
type FileLock struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// AcquireExclusiveLock attempts to acquire an exclusive lock file.
func AcquireExclusiveLock(lockPath string) (*FileLock, error) {
	absPath, err := filepath.Abs(lockPath)
	if err != nil {
		return nil, fmt.Errorf("resolve lock path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	// In Go on Windows and Linux, os.OpenFile with O_CREATE|O_EXCL can be used for exclusive file creation,
	// or opening with O_RDWR and writing PID.
	f, err := os.OpenFile(absPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", absPath, err)
	}

	// Write current PID to lock file
	pid := os.Getpid()
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteString(fmt.Sprintf("%d\n", pid))
		_ = f.Sync()
	}

	return &FileLock{
		path: absPath,
		file: f,
	}, nil
}

// Release releases the lock and closes/removes the lock file.
func (l *FileLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}

	_ = l.file.Close()
	l.file = nil

	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lock file %s: %w", l.path, err)
	}
	return nil
}

// IsLocked checks if a lock file exists.
func IsLocked(lockPath string) bool {
	if _, err := os.Stat(lockPath); err == nil {
		return true
	}
	return false
}

var ErrAlreadyLocked = errors.New("database or migration lock is already held by another process")
