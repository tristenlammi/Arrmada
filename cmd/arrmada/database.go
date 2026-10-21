package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/store"
)

func postgresOptions(cfg config.Config) store.PostgresOptions {
	return store.PostgresOptions{URL: cfg.PostgresURL, Schema: cfg.PostgresSchema, MaxConnections: cfg.PostgresMaxConnections, BinDir: cfg.PostgresBinDir}
}

func openConfiguredStore(cfg config.Config, log *slog.Logger) (*store.Store, error) {
	return store.OpenWith(cfg.DataDir, store.Options{Backend: store.Dialect(cfg.DatabaseBackend), Postgres: postgresOptions(cfg), Log: log,
		SkipMigrationSnapshot: cfg.SkipMigrationSnapshot, AllowNewerSchema: cfg.AllowNewerSchema})
}

func openMaintenanceStore(ctx context.Context, cfg config.Config) (*store.Store, error) {
	if cfg.DatabaseBackend == "postgres" {
		if cfg.AllowNewerSchema || cfg.SkipMigrationSnapshot {
			return nil, errors.New("SQLite migration escape hatches are not supported for PostgreSQL; unset them before using PostgreSQL maintenance commands")
		}
		return store.OpenPostgresNoMigrate(ctx, cfg.DataDir, postgresOptions(cfg))
	}
	return store.OpenNoMigrate(cfg.DataDir)
}
