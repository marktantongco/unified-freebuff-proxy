package credentials

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// CredentialLockPath returns the path to the flock lock file.
func CredentialLockPath(credsPath string) string {
	return credsPath + ".lock"
}

// AcquireWriteLock acquires an exclusive advisory flock on the credentials lock file.
func AcquireWriteLock(credsPath string) (*os.File, error) {
	lockPath := CredentialLockPath(credsPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open credential lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock credential lock (EX): %w", err)
	}
	return f, nil
}

// AcquireReadLock acquires a shared advisory flock on the credentials lock file.
func AcquireReadLock(credsPath string) (*os.File, error) {
	lockPath := CredentialLockPath(credsPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open credential lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock credential lock (SH): %w", err)
	}
	return f, nil
}

// ReleaseLock closes the lock file, releasing the flock.
func ReleaseLock(f *os.File) {
	if f == nil {
		return
	}
	_ = f.Close()
}
