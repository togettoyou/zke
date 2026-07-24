package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 0x5a4b45

var migrationFilename = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.sql$`)

//go:embed sql/*.sql
var migrationFiles embed.FS

type Result struct {
	CurrentVersion  int64
	AppliedVersions []int64
}

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

type appliedMigration struct {
	name     string
	checksum string
}

func Apply(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	available, err := load()
	if err != nil {
		return Result{}, err
	}

	connection, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, errors.New("acquire PostgreSQL connection for migrations")
	}
	defer connection.Release()

	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return Result{}, errors.New("acquire PostgreSQL migration lock")
	}
	defer releaseLock(connection)

	if _, err := connection.Exec(ctx, `
CREATE TABLE IF NOT EXISTS zke_schema_migrations (
    version bigint PRIMARY KEY,
    name text NOT NULL,
    checksum char(64) NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return Result{}, errors.New("initialize PostgreSQL migration metadata")
	}

	applied, err := loadApplied(ctx, connection)
	if err != nil {
		return Result{}, err
	}
	if err := validateApplied(available, applied); err != nil {
		return Result{}, err
	}

	result := Result{}
	for _, item := range available {
		if _, exists := applied[item.version]; exists {
			result.CurrentVersion = item.version
			continue
		}

		transaction, err := connection.Begin(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("begin PostgreSQL migration %06d: %w", item.version, err)
		}
		if _, err := transaction.Exec(ctx, item.sql); err != nil {
			_ = transaction.Rollback(ctx)
			return Result{}, fmt.Errorf("apply PostgreSQL migration %06d: %w", item.version, err)
		}
		if _, err := transaction.Exec(ctx,
			"INSERT INTO zke_schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
			item.version,
			item.name,
			item.checksum,
		); err != nil {
			_ = transaction.Rollback(ctx)
			return Result{}, fmt.Errorf("record PostgreSQL migration %06d: %w", item.version, err)
		}
		if err := transaction.Commit(ctx); err != nil {
			return Result{}, fmt.Errorf("commit PostgreSQL migration %06d: %w", item.version, err)
		}

		result.CurrentVersion = item.version
		result.AppliedVersions = append(result.AppliedVersions, item.version)
	}

	return result, nil
}

func load() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("sql")
	if err != nil {
		return nil, errors.New("read embedded PostgreSQL migrations")
	}

	available := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid PostgreSQL migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse PostgreSQL migration version %q: %w", matches[1], err)
		}

		filename := path.Join("sql", entry.Name())
		content, err := migrationFiles.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read embedded PostgreSQL migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("PostgreSQL migration %q is empty", entry.Name())
		}

		digest := sha256.Sum256(content)
		available = append(available, migration{
			version:  version,
			name:     matches[2],
			checksum: hex.EncodeToString(digest[:]),
			sql:      string(content),
		})
	}

	sort.Slice(available, func(i, j int) bool {
		return available[i].version < available[j].version
	})
	for index, item := range available {
		expectedVersion := int64(index + 1)
		if item.version != expectedVersion {
			return nil, fmt.Errorf(
				"PostgreSQL migrations must be contiguous: found %06d, expected %06d",
				item.version,
				expectedVersion,
			)
		}
	}
	if len(available) == 0 {
		return nil, errors.New("no embedded PostgreSQL migrations found")
	}

	return available, nil
}

func loadApplied(ctx context.Context, connection *pgxpool.Conn) (map[int64]appliedMigration, error) {
	rows, err := connection.Query(ctx, `
SELECT version, name, checksum
FROM zke_schema_migrations
ORDER BY version`)
	if err != nil {
		return nil, errors.New("read applied PostgreSQL migrations")
	}
	defer rows.Close()

	applied := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var item appliedMigration
		if err := rows.Scan(&version, &item.name, &item.checksum); err != nil {
			return nil, errors.New("scan applied PostgreSQL migration")
		}
		applied[version] = item
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate applied PostgreSQL migrations")
	}

	return applied, nil
}

func validateApplied(available []migration, applied map[int64]appliedMigration) error {
	known := make(map[int64]migration, len(available))
	for _, item := range available {
		known[item.version] = item
	}

	appliedVersions := make([]int64, 0, len(applied))
	for version := range applied {
		appliedVersions = append(appliedVersions, version)
	}
	sort.Slice(appliedVersions, func(i, j int) bool {
		return appliedVersions[i] < appliedVersions[j]
	})

	for index, version := range appliedVersions {
		expectedVersion := int64(index + 1)
		if version != expectedVersion {
			return fmt.Errorf(
				"applied PostgreSQL migrations must be contiguous: found %06d, expected %06d",
				version,
				expectedVersion,
			)
		}

		actual := applied[version]
		expected, exists := known[version]
		if !exists {
			return fmt.Errorf("database schema version %06d is newer than this ZKE binary", version)
		}
		if actual.name != expected.name {
			return fmt.Errorf("PostgreSQL migration %06d name does not match the embedded migration", version)
		}
		if actual.checksum != expected.checksum {
			return fmt.Errorf("PostgreSQL migration %06d checksum does not match the embedded migration", version)
		}
	}

	return nil
}

func releaseLock(connection *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := connection.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
		_ = connection.Conn().Close(ctx)
	}
}
