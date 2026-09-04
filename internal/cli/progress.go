package cli

import (
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

// CLIProgress implements backup.Progress using github.com/schollz/progressbar/v3.
// It renders to the specified writer (usually stderr in text mode).
type CLIProgress struct {
	out         io.Writer
	terminal    bool
	mu          sync.Mutex
	bar         *progressbar.ProgressBar
	spinnerStop chan struct{}
	spinnerDone chan struct{}
	labelWidth  int
	activeLabel string
	activeTotal int64
}

// NewCLIProgress constructs a new CLIProgress renderer.
func NewCLIProgress(out io.Writer) *CLIProgress {
	return &CLIProgress{
		out:        out,
		terminal:   isTerminalWriter(out),
		labelWidth: 25,
	}
}

// formatStageTitle capitalizes and normalizes the stage description for display.
func formatStageTitle(label string) string {
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case lower == "compress files":
		return "Compressing files"
	case strings.HasPrefix(lower, "export "):
		name := strings.TrimSpace(strings.TrimPrefix(label, "export "))
		if name == "" {
			return "Exporting database"
		}
		return "Exporting " + name
	case strings.HasPrefix(lower, "upload "):
		dest := strings.TrimSpace(strings.TrimPrefix(label, "upload "))
		if dest == "" {
			return "Uploading archive"
		}
		return "Uploading to " + dest
	case lower == "downloading release":
		return "Downloading release"
	case lower == "verifying checksum":
		return "Verifying checksum"
	case lower == "installing update":
		return "Installing update"
	default:
		if len(label) > 0 {
			return strings.ToUpper(label[:1]) + label[1:]
		}
		return label
	}
}

// StartStage starts a new progress bar or spinner stage.
func (p *CLIProgress) StartStage(label string, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Reset any stale in-flight progress before starting the next stage. This
	// matters when a previous upload/export was interrupted or when the caller
	// switches destinations without explicitly calling FinishStage() first.
	p.finishActiveStageLocked(false)

	displayLabel := formatStageTitle(label)
	p.activeLabel = displayLabel
	p.activeTotal = total

	if !p.terminal {
		if total > 0 {
			_, _ = fmt.Fprintf(p.out, "-> %s (%s)\n", displayLabel, humanBytes(total))
		} else {
			_, _ = fmt.Fprintf(p.out, "-> %s...\n", displayLabel)
		}
		return
	}

	if total > 0 {
		p.bar = progressbar.NewOptions64(
			total,
			progressbar.OptionSetWriter(io.Discard),
			progressbar.OptionEnableColorCodes(false),
			progressbar.OptionSetWidth(24),
			progressbar.OptionSetDescription(fmt.Sprintf("%-*s", p.labelWidth, displayLabel)),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "█",
				SaucerHead:    "█",
				SaucerPadding: "░",
				BarStart:      "[",
				BarEnd:        "]",
			}),
			progressbar.OptionShowCount(),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionSetPredictTime(true),
		)
		p.renderTerminalLocked()
		return
	}

	// Indeterminate stage: render dynamic spinner.
	p.spinnerStop = make(chan struct{})
	p.spinnerDone = make(chan struct{})
	stop := p.spinnerStop
	done := p.spinnerDone
	out := p.out
	go func() {
		defer close(done)
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		frames := []string{"[      ]", "[=     ]", "[==    ]", "[===   ]", "[====  ]", "[===== ]", "[======]", "[ =====]", "[  ====]", "[   ===]", "[    ==]", "[     =]"}
		idx := 0
		started := time.Now()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				_, _ = fmt.Fprintf(out, "\r\033[2K%-*s %s  %s elapsed", p.labelWidth, displayLabel, frames[idx%len(frames)], elapsed)
				idx++
			case <-stop:
				return
			}
		}
	}()
}

// Add reports completed units (e.g. bytes) within the current stage.
func (p *CLIProgress) Add(units int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.bar != nil && units > 0 {
		_ = p.bar.Add64(units)
		p.renderTerminalLocked()
	}
}

// FinishStage marks the current stage as successfully finished.
func (p *CLIProgress) FinishStage() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.finishActiveStageLocked(true)
}

// FailStage aborts the current stage on error.
func (p *CLIProgress) FailStage() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.finishActiveStageLocked(false)
}

// Done marks the end of all stages and performs cleanup.
func (p *CLIProgress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.finishActiveStageLocked(false)
}

func (p *CLIProgress) renderTerminalLocked() {
	if p.bar == nil || !p.terminal || p.activeLabel == "" {
		return
	}
	state := p.bar.State()
	if state.Max <= 0 {
		return
	}
	percentFloat := math.Min(1.0, math.Max(0, state.CurrentPercent)) * 100
	_, _ = fmt.Fprint(p.out, formatProgressLine(p.labelWidth, p.activeLabel, percentFloat, int64(state.CurrentNum), int64(state.Max), state.KBsPerSecond*1024, state.SecondsLeft))
}

func formatProgressLine(labelWidth int, label string, percent float64, current, total int64, rate float64, eta float64) string {
	percentText := fmt.Sprintf("%3.0f%%", math.Round(percent))
	currentText := "--"
	totalText := "--"
	if total > 0 {
		totalText = humanBytes(total)
		if current < 0 {
			currentText = "0 B"
		} else {
			currentText = humanBytes(current)
		}
	} else if current > 0 {
		currentText = humanBytes(current)
	}
	return fmt.Sprintf("\r\033[2K%-*s %s %s  %-10s/%-10s  %-10s  ETA %s",
		labelWidth,
		label,
		renderBar(percent, 24),
		percentText,
		currentText,
		totalText,
		formatRateBytesPerSecond(rate),
		formatETA(eta),
	)
}

func renderBar(percent float64, width int) string {
	filled := int(math.Round(percent / 100 * float64(width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func formatRateBytesPerSecond(bytesPerSecond float64) string {
	if math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) || bytesPerSecond <= 0 {
		return "--"
	}
	value := int64(bytesPerSecond)
	return fmt.Sprintf("%s", humanBytes(value))
}

func formatETA(seconds float64) string {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return "--"
	}
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int((d%time.Minute)/time.Second))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int((d%time.Hour)/time.Minute))
}

func (p *CLIProgress) finishActiveStageLocked(completed bool) {
	if p.spinnerStop != nil {
		close(p.spinnerStop)
		<-p.spinnerDone
		p.spinnerStop = nil
		p.spinnerDone = nil
		if p.terminal {
			if completed {
				current := int64(0)
				if p.activeTotal > 0 {
					current = p.activeTotal
				}
				_, _ = fmt.Fprint(p.out, formatProgressLine(p.labelWidth, p.activeLabel, 100, current, p.activeTotal, 0, 0))
				_, _ = fmt.Fprintln(p.out)
			} else {
				_, _ = fmt.Fprint(p.out, "\r\033[2K")
			}
		}
	}

	if p.bar != nil {
		if completed {
			_ = p.bar.Finish()
			p.renderTerminalLocked()
			_, _ = fmt.Fprintln(p.out)
		} else {
			_ = p.bar.Clear()
			if p.terminal {
				_, _ = fmt.Fprint(p.out, "\r\033[2K")
			}
		}
		p.bar = nil
	}

	// Clear stale label bookkeeping even when a bar was already removed by the
	// caller or when a previous stage was interrupted in the middle of upload.
	p.activeLabel = ""
	p.activeTotal = 0
}

type progressHeartbeat struct {
	stop     chan struct{}
	done     chan struct{}
	terminal bool
	stopOnce sync.Once
}

func startProgressHeartbeat(out io.Writer, label string) *progressHeartbeat {
	heartbeat := &progressHeartbeat{
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
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
		rendered := false
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				if heartbeat.terminal {
					frames := "|/-\\"
					_, _ = fmt.Fprintf(out, "\r\033[2K[%c] %s: running (%s elapsed)", frames[frame%len(frames)], label, elapsed)
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
