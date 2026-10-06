package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	"os"
	"time"
)

func main() {
	target := flag.Int("target", migrations.Latest(), "target schema version")
	allowDown := flag.Bool("allow-down", false, "allow a destructive target below latest")
	flag.Parse()
	if *target < migrations.Latest() && !*allowDown {
		fmt.Fprintln(os.Stderr, "lower targets require -allow-down; use only a disposable database")
		os.Exit(1)
	}
	url := os.Getenv("MIGRATION_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "MIGRATION_DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.Run(ctx, url, *target); err != nil {
		fmt.Fprintln(os.Stderr, "Migration failed:", err)
		os.Exit(1)
	}
	fmt.Printf("Database schema is at version %d.\n", *target)
}
