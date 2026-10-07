package utils

import (
	"os"
	"strconv"
	"time"
)

// Конфигурация сервисов читается из переменных окружения (12-factor).
// Значения по умолчанию подходят для запуска сервиса через `go run` рядом с docker-compose.

// Env возвращает значение переменной окружения или def, если она не задана.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvInt читает целое число; некорректное значение — фатальная ошибка конфигурации.
func EnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		Fatal("некорректное значение переменной окружения", "key", key, "value", v)
	}
	return n
}

// EnvDuration читает длительность в формате Go: 500ms, 3s, 1m.
func EnvDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		Fatal("некорректное значение переменной окружения", "key", key, "value", v)
	}
	return d
}
