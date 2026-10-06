// Package migrations embeds versioned financial database migrations.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS
var names = []string{"001_finance", "002_accounting", "003_tracing"}

// Run migrates to target (0..Latest). Down migrations are destructive and are
// intended for disposable test databases. Each version and its history row share
// a SQL transaction. A session advisory lock serializes migrators only.
func Run(ctx context.Context, url string, target int) error {
	if target < 0 || target > len(names) {
		return fmt.Errorf("invalid migration target")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect migrator: %w", err)
	}
	defer conn.Close(context.Background())
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(481927331)"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(cleanup, "SELECT pg_advisory_unlock(481927331)")
	}()
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS wagering.schema_migrations(version integer PRIMARY KEY,name text NOT NULL,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	rows, err := conn.Query(ctx, "SELECT version,name,checksum FROM wagering.schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	current := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err = rows.Scan(&version, &name, &checksum); err != nil {
			rows.Close()
			return err
		}
		if version != current+1 || version > len(names) || name != names[version-1] {
			rows.Close()
			return fmt.Errorf("unknown or noncontiguous migration history")
		}
		data, _ := files.ReadFile(name + ".up.sql")
		if checksum != fmt.Sprintf("%x", sha256.Sum256(data)) {
			rows.Close()
			return fmt.Errorf("migration checksum mismatch at %d", version)
		}
		current = version
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for current != target {
		up := current < target
		version := current
		suffix := ".down.sql"
		if up {
			version++
			suffix = ".up.sql"
		}
		data, err := files.ReadFile(names[version-1] + suffix)
		if err != nil {
			return err
		}
		transaction, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = transaction.Exec(ctx, string(data), pgx.QueryExecModeSimpleProtocol)
		if err == nil {
			if up {
				_, err = transaction.Exec(ctx, "INSERT INTO wagering.schema_migrations(version,name,checksum) VALUES($1,$2,$3)", version, names[version-1], fmt.Sprintf("%x", sha256.Sum256(data)))
			} else {
				_, err = transaction.Exec(ctx, "DELETE FROM wagering.schema_migrations WHERE version=$1", version)
			}
		}
		if err != nil {
			_ = transaction.Rollback(context.Background())
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if err = transaction.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
		if up {
			current++
		} else {
			current--
		}
	}
	return nil
}
func Latest() int { return len(names) }
