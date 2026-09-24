// Command dns-consistency-checker queries one DNS record against many
// resolvers concurrently and reports whether the answers are consistent.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kmpoltorak/dns-consistency-checker/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
