package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens the connection pool and proves it works before the service
// reports itself ready.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Deliberately not including the URL in the message: it carries the
		// password, and error strings end up in logs.
		return nil, fmt.Errorf("invalid database configuration: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database is not reachable: %w", err)
	}

	return pool, nil
}
