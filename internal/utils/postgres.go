package utils

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Коды ошибок PostgreSQL, которые обрабатываются явно.
const (
	PgUniqueViolation     = "23505"
	PgInvalidTextRepr     = "22P02"
	PgCheckViolation      = "23514"
	PgSerializationFailed = "40001"
	PgDeadlockDetected    = "40P01"
	PgLockNotAvailable    = "55P03" // сработал lock_timeout
	PgQueryCanceled       = "57014" // сработал statement_timeout
)

// PgCode возвращает SQLSTATE ошибки PostgreSQL или пустую строку.
func PgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

type PostgresConfig struct {
	URL              string
	MaxConns         int32
	StatementTimeout time.Duration // защита от зависших запросов
	LockTimeout      time.Duration // сколько транзакция готова ждать блокировку строки
}

// PostgresFromEnv читает DATABASE_URL, DB_MAX_CONNS, DB_STATEMENT_TIMEOUT, DB_LOCK_TIMEOUT.
func PostgresFromEnv(defaultURL string) PostgresConfig {
	return PostgresConfig{
		URL:              Env("DATABASE_URL", defaultURL),
		MaxConns:         int32(EnvInt("DB_MAX_CONNS", 20)),
		StatementTimeout: EnvDuration("DB_STATEMENT_TIMEOUT", 5*time.Second),
		LockTimeout:      EnvDuration("DB_LOCK_TIMEOUT", 2*time.Second),
	}
}

// ConnectPostgres создаёт пул соединений и ждёт готовности БД, пока не истечёт ctx:
// в docker-compose база может подниматься дольше сервиса.
func ConnectPostgres(ctx context.Context, cfg PostgresConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("разбор DATABASE_URL: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.StatementTimeout > 0 {
		pc.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	}
	if cfg.LockTimeout > 0 {
		pc.ConnConfig.RuntimeParams["lock_timeout"] = strconv.FormatInt(cfg.LockTimeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	for {
		err = pool.Ping(ctx)
		if err == nil {
			return pool, nil
		}
		slog.Warn("PostgreSQL недоступен, повтор через секунду", "host", pc.ConnConfig.Host, "err", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("PostgreSQL недоступен: %w", err)
		case <-time.After(time.Second):
		}
	}
}

// PublishPoolStats публикует статистику пула в /debug/vars. Рост empty_acquire_count
// и acquire_wait_ms означает, что запросы ждут свободное соединение — пул стал узким местом.
func PublishPoolStats(name string, pool *pgxpool.Pool) {
	key := "pgxpool_" + name
	if expvar.Get(key) != nil {
		return
	}
	expvar.Publish(key, expvar.Func(func() any {
		s := pool.Stat()
		return map[string]any{
			"max_conns":           s.MaxConns(),
			"total_conns":         s.TotalConns(),
			"acquired_conns":      s.AcquiredConns(),
			"idle_conns":          s.IdleConns(),
			"acquire_count":       s.AcquireCount(),
			"empty_acquire_count": s.EmptyAcquireCount(),
			"acquire_wait_ms":     s.EmptyAcquireWaitTime().Milliseconds(),
			"canceled_acquires":   s.CanceledAcquireCount(),
		}
	}))
}
