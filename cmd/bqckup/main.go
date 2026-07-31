package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bqckup/bqckup-go/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	os.Exit(cli.Execute(ctx, os.Stdout, os.Stderr))
}
