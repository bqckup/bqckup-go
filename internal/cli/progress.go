package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type progressHeartbeat struct {
	stop     chan struct{}
	done     chan struct{}
	terminal bool
	stopOnce sync.Once
}

func startProgressHeartbeat(out io.Writer, label string) *progressHeartbeat {
	heartbeat := &progressHeartbeat{
		stop: make(chan struct{}), done: make(chan struct{}), terminal: isTerminalWriter(out),
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
		rendered := false
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				if heartbeat.terminal {
					frames := "|/-\\"
					_, _ = fmt.Fprintf(out, "\r[%c] %s: running (%s elapsed)", frames[frame%len(frames)], label, elapsed)
					frame++
					rendered = true
				} else {
					_, _ = fmt.Fprintf(out, "[...] %s: still running (%s elapsed)\n", label, elapsed)
				}
			case <-heartbeat.stop:
				writeProgressCleanup(out, heartbeat.terminal && rendered)
				return
			}
		}
	}()
	return heartbeat
}

func writeProgressCleanup(out io.Writer, rendered bool) {
	if rendered {
		_, _ = fmt.Fprint(out, "\r\033[2K")
	}
}

func (h *progressHeartbeat) Stop() {
	h.stopOnce.Do(func() { close(h.stop) })
	<-h.done
}
