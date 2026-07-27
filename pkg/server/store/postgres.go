package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig describes the PostgreSQL connection pool. Sizing it explicitly
// keeps Server from exhausting the database's connection budget when several
// replicas run against the same instance.
type PoolConfig struct {
	URL             string
	MaxConnections  int32
	MinConnections  int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

func Open(ctx context.Context, poolConfig PoolConfig) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(poolConfig.URL)
	if err != nil {
		// The URL may embed credentials, so report the parser's reason without
		// echoing the value back.
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	if poolConfig.MaxConnections > 0 {
		config.MaxConns = poolConfig.MaxConnections
	}
	if poolConfig.MinConnections > 0 {
		config.MinConns = poolConfig.MinConnections
	}
	if poolConfig.MaxConnLifetime > 0 {
		config.MaxConnLifetime = poolConfig.MaxConnLifetime
	}
	if poolConfig.MaxConnIdleTime > 0 {
		config.MaxConnIdleTime = poolConfig.MaxConnIdleTime
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("initialize PostgreSQL connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	return pool, nil
}
