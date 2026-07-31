package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"golang.org/x/sys/unix"
)

var safeSiteName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type Locker struct {
	directory string
}

func New(directory string) *Locker { return &Locker{directory: directory} }

func (l *Locker) TryLock(ctx context.Context, site string) (func() error, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !safeSiteName.MatchString(site) {
		return nil, false, fmt.Errorf("unsafe site lock name %q", site)
	}
	if err := os.MkdirAll(l.directory, 0o700); err != nil {
		return nil, false, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(l.directory, site+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open site lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return func() error { return nil }, false, nil
		}
		return nil, false, fmt.Errorf("acquire site lock: %w", err)
	}

	var once sync.Once
	var unlockErr error
	unlock := func() error {
		once.Do(func() {
			if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
				unlockErr = fmt.Errorf("release site lock: %w", err)
			}
			if err := file.Close(); err != nil && unlockErr == nil {
				unlockErr = fmt.Errorf("close site lock: %w", err)
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}
