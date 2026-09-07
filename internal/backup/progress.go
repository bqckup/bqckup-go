package backup

// Progress reports coarse-grained backup/update stage progress to a consumer
// (the CLI renderer). Implementations must never emit credentials, signed
// URLs, passwords, or absolute local paths; stage labels are fixed strings
// chosen by callers and unit counters are integer byte/unit totals.
//
// The contract is intentionally small so leaf components (archiver, exporter,
// storage adapters) can report bytes without importing the renderer.
type Progress interface {
	// StartStage begins a stage. total <= 0 marks the stage indeterminate:
	// callers do not know its size up front (e.g. compress/export).
	StartStage(label string, total int64)
	// Add reports completed units (bytes) within the current stage.
	Add(units int64)
	// FinishStage marks the current stage as complete.
	FinishStage()
	// FailStage aborts the current stage after an error.
	FailStage()
	// Done is called once after all stages; it allows terminal cleanup.
	Done()
}

// NoopProgress discards every event. It is the default for runners that have
// no progress consumer wired (library calls, JSON output, tests).
type NoopProgress struct{}

func (NoopProgress) StartStage(string, int64) {}
func (NoopProgress) Add(int64)                {}
func (NoopProgress) FinishStage()             {}
func (NoopProgress) FailStage()               {}
func (NoopProgress) Done()                    {}

// progressOrNoop converts a nil progress to a no-op so leaving callers and
// tests can omit the renderer entirely.
func progressOrNoop(p Progress) Progress {
	if p == nil {
		return NoopProgress{}
	}
	return p
}
