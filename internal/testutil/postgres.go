// Package testutil — помощники для интеграционных тестов с PostgreSQL.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaSeq atomic.Int64

// PostgresSchema создаёт в базе из переменной окружения envVar временную схему,
// применяет к ней миграцию (путь от корня репозитория) и возвращает пул соединений
// с search_path на эту схему. После теста схема удаляется.
// Если переменная не задана, тест пропускается.
func PostgresSchema(t testing.TB, envVar, migration string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(envVar)
	if url == "" {
		t.Skipf("%s не задан: интеграционный тест пропущен (см. make test-integration)", envVar)
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("подключение к %s: %v", envVar, err)
	}
	schema := fmt.Sprintf("test_%d_%d_%d", time.Now().UnixNano(), os.Getpid(), schemaSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("создание схемы: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 40
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	sql, err := os.ReadFile(filepath.Join(repoRoot(t), migration))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("миграция %s: %v", migration, err)
	}
	return pool
}

func repoRoot(t testing.TB) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("не найден корень репозитория (go.mod)")
		}
		dir = parent
	}
}
