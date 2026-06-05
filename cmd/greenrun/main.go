package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kiwi-init/greenrun/internal/cli"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	os.Exit(cli.Execute(ctx, version, os.Args[1:]))
}
