// Command platform-api: the identity-aware proxy brain (ForwardAuth, sessions, signing keys).
//
//	platform-api migrate   apply database migrations
//	platform-api serve     run the internal and public listeners
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"platform/api/internal/postgres"
)

const usage = "usage: platform-api migrate|serve"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "migrate":
		return migrate(ctx)
	case "serve":
		cfg, err := loadConfig(os.Getenv)
		if err != nil {
			return err
		}
		// JSON logs; request data (tokens, cookies) is never logged by any handler.
		log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		slog.SetDefault(log)
		return serve(ctx, cfg, log)
	default:
		return fmt.Errorf("%s", usage)
	}
}

func migrate(ctx context.Context) error {
	pool, err := postgres.NewPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := postgres.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Printf("applied %d migration(s)\n", len(applied))
	return nil
}
