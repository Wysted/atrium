// Command portal is the portal app (contract block 6): the home of the apps a user can open and, in later
// parts, the administration API and UI. It is an app like any other behind ForwardAuth, but a separate process
// from platform-api with its own database role; it never holds the KEK or any signing key.
//
//	portal serve   run the public and internal listeners
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

const usage = "usage: portal serve"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return fmt.Errorf("%s", usage)
	}
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	// JSON logs; request data (assertions, cookies) is never logged by any handler.
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	return serve(ctx, cfg, log)
}
