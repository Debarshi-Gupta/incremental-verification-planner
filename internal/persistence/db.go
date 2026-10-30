package persistence

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type DB struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("database connection string is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() {
	if d != nil && d.Pool != nil {
		d.Pool.Close()
	}
}

func (d *DB) Ready(ctx context.Context) error {
	if d == nil || d.Pool == nil {
		return errors.New("database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return d.Pool.Ping(ctx)
}

func (d *DB) Migrate(ctx context.Context) error {
	if d == nil || d.Pool == nil {
		return errors.New("database unavailable")
	}
	goose.SetDialect("postgres")
	goose.SetBaseFS(migrationFiles)
	conn := stdlib.OpenDBFromPool(d.Pool)
	defer conn.Close()
	if err := goose.UpContext(ctx, conn, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// TenantTx starts a transaction with both an explicit transaction-local tenant
// context for row-level security and an application-level tenant predicate.
func (d *DB) TenantTx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return errors.New("tenant id required")
	}
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SELECT set_config('ivp.tenant_id',$1,true)", tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResolveMembership satisfies security.MembershipResolver. Role assignments
// come exclusively from database membership rows, never token role claims.
func (d *DB) ResolveMembership(ctx context.Context, subject, tenantID string) (security.Membership, error) {
	var m security.Membership
	var disabledAt *time.Time
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return m, errors.New("invalid tenant id")
	}
	err = d.TenantTx(ctx, tenantUUID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT u.id::text, tm.tenant_id::text, tm.role, u.disabled_at
		FROM users u JOIN tenant_memberships tm ON tm.user_id=u.id
		WHERE u.oidc_subject=$1 AND tm.tenant_id=$2`, subject, tenantUUID).Scan(&m.UserID, &m.TenantID, &m.Role, &disabledAt)
	})
	if err != nil {
		return m, err
	}
	m.Disabled = disabledAt != nil
	return m, nil
}

var _ security.MembershipResolver = (*DB)(nil)
