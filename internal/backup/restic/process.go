package restic

import (
	"context"
	"io"
	"os"
	"os/exec"
)

type ProcessSpec struct {
	Command string
	Args    []string
	Env     []string
	Stdout  io.Writer
	Stderr  io.Writer
}

type ProcessRunner interface {
	LookPath(command string) (string, error)
	Run(ctx context.Context, spec ProcessSpec) error
}

type osProcessRunner struct{}

func NewProcessRunner() ProcessRunner { return osProcessRunner{} }

func (osProcessRunner) LookPath(command string) (string, error) {
	return exec.LookPath(command)
}

func (osProcessRunner) Run(ctx context.Context, spec ProcessSpec) error {
	command := exec.CommandContext(ctx, spec.Command, spec.Args...)
	command.Env = append(os.Environ(), spec.Env...)
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	return command.Run()
}
