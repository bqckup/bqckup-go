package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"golang.org/x/sys/unix"
)

type Locker struct {
	directory string
}

func New(directory string) *Locker { return &Locker{directory: directory} }

// Owner identifies the process that currently owns a site lock. StartTicks
// prevents a PID that has been reused by Linux from being mistaken for the
// original backup process.
type Owner struct {
	Version    int       `json:"version"`
	PID        int       `json:"pid"`
	StartTicks uint64    `json:"process_start_ticks"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// Inspection describes a lock without changing it. Unknown is true for a
// held legacy or corrupt lock which must be handled manually.
type Inspection struct {
	Held    bool
	Owner   *Owner
	Unknown bool
}

func (l *Locker) TryLock(ctx context.Context, site string) (func() error, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !config.SafeName.MatchString(site) {
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
	startTicks, err := processStartTicks(os.Getpid())
	if err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		return nil, false, fmt.Errorf("read lock owner process identity: %w", err)
	}
	owner := Owner{Version: 1, PID: os.Getpid(), StartTicks: startTicks, AcquiredAt: time.Now().UTC()}
	if err := writeOwner(file, owner); err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		return nil, false, fmt.Errorf("write site lock metadata: %w", err)
	}

	var once sync.Once
	var unlockErr error
	unlock := func() error {
		once.Do(func() {
			if err := clearOwner(file); err != nil {
				unlockErr = fmt.Errorf("clear site lock metadata: %w", err)
			}
			if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
				if unlockErr == nil {
					unlockErr = fmt.Errorf("release site lock: %w", err)
				}
			}
			if err := file.Close(); err != nil && unlockErr == nil {
				unlockErr = fmt.Errorf("close site lock: %w", err)
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}

// Inspect reports whether a lock is held and, when available, verifies the
// owner identity against /proc. It never removes a lock or its metadata.
func (l *Locker) Inspect(ctx context.Context, site string) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	if !config.SafeName.MatchString(site) {
		return Inspection{}, fmt.Errorf("unsafe site lock name %q", site)
	}
	file, err := os.OpenFile(filepath.Join(l.directory, site+".lock"), os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return Inspection{}, nil
	}
	if err != nil {
		return Inspection{}, fmt.Errorf("open site lock: %w", err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		return Inspection{}, nil
	} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		return Inspection{}, fmt.Errorf("inspect site lock: %w", err)
	}
	owner, err := readOwner(file)
	if err != nil || !ownerIsLive(owner) {
		return Inspection{Held: true, Unknown: true}, nil
	}
	return Inspection{Held: true, Owner: &owner}, nil
}

// Sites returns safe site names with an existing local lock file. It is used
// for visibility so locks from removed configuration are not silently hidden.
func (l *Locker) Sites() ([]string, error) {
	entries, err := os.ReadDir(l.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lock directory: %w", err)
	}
	sites := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		site := strings.TrimSuffix(name, ".lock")
		if config.SafeName.MatchString(site) {
			sites = append(sites, site)
		}
	}
	return sites, nil
}

func writeOwner(file *os.File, owner Owner) error {
	encoded, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	return file.Sync()
}

func clearOwner(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	return file.Sync()
}

func readOwner(file *os.File) (Owner, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return Owner{}, err
	}
	contents, err := os.ReadFile(file.Name())
	if err != nil {
		return Owner{}, err
	}
	var owner Owner
	if len(contents) == 0 {
		return Owner{}, errors.New("empty metadata")
	}
	if err := json.Unmarshal(contents, &owner); err != nil {
		return Owner{}, err
	}
	if owner.Version != 1 || owner.PID <= 0 || owner.StartTicks == 0 || owner.AcquiredAt.IsZero() {
		return Owner{}, errors.New("invalid metadata")
	}
	return owner, nil
}

func ownerIsLive(owner Owner) bool {
	startTicks, err := processStartTicks(owner.PID)
	return err == nil && startTicks == owner.StartTicks
}

// SignalTerm verifies the process identity immediately before asking it to
// stop. It intentionally sends no SIGKILL; callers must report a timeout.
func (l *Locker) SignalTerm(owner Owner) error {
	if !ownerIsLive(owner) {
		return errors.New("lock owner process changed or exited")
	}
	if err := syscall.Kill(owner.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to backup process %d: %w", owner.PID, err)
	}
	return nil
}

// WaitReleased waits until this site's lock is no longer held. It does not
// mutate the lock and returns a timeout error without escalating the signal.
func (l *Locker) WaitReleased(ctx context.Context, site string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := l.Inspect(ctx, site)
		if err != nil {
			return err
		}
		if !inspection.Held {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for site lock %q to be released", site)
		case <-ticker.C:
		}
	}
}

// processStartTicks reads field 22 from proc(5). The executable name may
// contain spaces and parentheses, so split only after its final ')'.
func processStartTicks(pid int) (uint64, error) {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	close := strings.LastIndexByte(string(contents), ')')
	if close < 0 {
		return 0, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(contents)[close+1:])
	// fields begins with process state (field 3); starttime is field 22.
	if len(fields) < 20 {
		return 0, errors.New("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
