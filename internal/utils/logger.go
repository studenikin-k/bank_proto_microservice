package utils

import (
	"log/slog"
	"os"
	"strings"
)

// InitLogger настраивает стандартный slog для сервиса.
//
//	LOG_LEVEL  = debug | info | warn | error (по умолчанию info)
//	LOG_FORMAT = text | json                 (по умолчанию text)
//
// На уровне info пишутся только события жизненного цикла и аномалии (неизвестный исход
// перевода, восстановление, сбои). Лог каждого запроса — уровень debug: синхронная запись
// строки на каждый запрос заметно искажает latency под нагрузкой.
func InitLogger(service string) {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("LOG_LEVEL"))}
	var h slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h).With("service", service))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// Fatal пишет ошибку в лог и завершает процесс.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
