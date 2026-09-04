package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type progressHeartbeat struct {
	mu       sync.Mutex
	stop     chan struct{}
	done     chan struct{}
	pause    chan chan struct{}
	resume   chan struct{}
	terminal bool
	paused   bool
	stopped  bool
}

func startProgressHeartbeat(out io.Writer, action, siteName, verb string) *progressHeartbeat {
	heartbeat := &progressHeartbeat{
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		pause:    make(chan chan struct{}),
		resume:   make(chan struct{}),
		terminal: isTerminalWriter(out),
	}
	go func() {
		defer close(heartbeat.done)
		interval := 5 * time.Second
		if heartbeat.terminal {
			interval = 250 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		started := time.Now()
		frame := 0
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				if heartbeat.terminal {
					frames := "|/-\\"
					_, _ = fmt.Fprintf(out, "\r[%c] %s:%s: %s (%s elapsed)", frames[frame%len(frames)], action, siteName, verb, elapsed)
					frame++
				} else {
					_, _ = fmt.Fprintf(out, "[...] %s:%s: still %s (%s elapsed)\n", action, siteName, verb, elapsed)
				}
			case ack := <-heartbeat.pause:
				if heartbeat.terminal {
					_, _ = fmt.Fprint(out, "\r\033[2K")
				}
				close(ack)
				select {
				case <-heartbeat.resume:
				case <-heartbeat.stop:
					if heartbeat.terminal {
						_, _ = fmt.Fprint(out, "\r\033[2K\n")
					}
					return
				}
			case <-heartbeat.stop:
				if heartbeat.terminal {
					_, _ = fmt.Fprint(out, "\r\033[2K\n")
				}
				return
			}
		}
	}()
	return heartbeat
}

func (h *progressHeartbeat) Pause() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.stopped || h.paused {
		h.mu.Unlock()
		return
	}
	h.paused = true
	ack := make(chan struct{})
	select {
	case h.pause <- ack:
		h.mu.Unlock()
		<-ack
	case <-h.done:
		h.mu.Unlock()
	}
}

func (h *progressHeartbeat) Resume() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped || !h.paused {
		return
	}
	h.paused = false
	select {
	case h.resume <- struct{}{}:
	case <-h.done:
	}
}

func (h *progressHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		<-h.done
		return
	}
	h.stopped = true
	close(h.stop)
	h.mu.Unlock()
	<-h.done
}

func writeCheckStartText(out io.Writer, site, destination string, readData bool) error {
	suffix := ""
	if readData {
		suffix = " (read-data)"
	}
	color := ansiColor{on: isTerminalWriter(out)}
	_, err := fmt.Fprintf(out, "%s check:%s: checking repository on %s%s\n", color.yellow("[>]"), site, destination, suffix)
	return err
}

func writeRepairIndexStartText(out io.Writer, site, destination string) error {
	color := ansiColor{on: isTerminalWriter(out)}
	_, err := fmt.Fprintf(out, "%s repair-index:%s: repairing index on %s\n", color.yellow("[>]"), site, destination)
	return err
}

func writeRestoreStartText(out io.Writer, site, destination, snapshot, target string) error {
	color := ansiColor{on: isTerminalWriter(out)}
	_, err := fmt.Fprintf(out, "%s restore:%s: restoring snapshot %s from %s to %s\n", color.yellow("[>]"), site, snapshot, destination, target)
	return err
}
