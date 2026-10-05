package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"bank_proto_microservice/internal/gateway/handlers"
	"bank_proto_microservice/internal/gateway/middleware"
	"bank_proto_microservice/internal/utils"

	"github.com/valyala/fasthttp"
)

func main() {
	utils.InitLogger("api-gateway")

	port := strings.TrimPrefix(utils.Env("PORT", "8080"), ":")
	utils.RunHealthcheckCommand(func() error { return httpHealthcheck("http://127.0.0.1:" + port + "/health") })

	jwtSecret := utils.Env("JWT_SECRET", "dev-only-jwt-secret-change-me")
	authAddr := utils.Env("AUTH_SERVICE_ADDR", "localhost:50051")
	accountAddr := utils.Env("ACCOUNT_SERVICE_ADDR", "localhost:50052")
	txAddr := utils.Env("TRANSACTION_SERVICE_ADDR", "localhost:50053")
	// Должен быть больше ACCOUNT_CALL_TIMEOUT в Transaction Service, чтобы исход
	// перевода успел определиться до того, как шлюз ответит клиенту таймаутом.
	requestTimeout := utils.EnvDuration("REQUEST_TIMEOUT", 5*time.Second)

	authConn, err := utils.DialGRPC(authAddr)
	if err != nil {
		utils.Fatal("некорректный адрес Auth Service", "addr", authAddr, "err", err)
	}
	defer authConn.Close()
	accountConn, err := utils.DialGRPC(accountAddr)
	if err != nil {
		utils.Fatal("некорректный адрес Account Service", "addr", accountAddr, "err", err)
	}
	defer accountConn.Close()
	txConn, err := utils.DialGRPC(txAddr)
	if err != nil {
		utils.Fatal("некорректный адрес Transaction Service", "addr", txAddr, "err", err)
	}
	defer txConn.Close()

	gateway := handlers.NewGatewayHandler(authConn, accountConn, txConn, requestTimeout)
	router := handlers.NewRouter(gateway, middleware.NewAuthMiddleware(jwtSecret))

	server := &fasthttp.Server{
		Handler:      router.Handler,
		Name:         "bank-api-gateway",
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		// Дольше keepalive_timeout nginx (60s): простаивающее соединение закрывает nginx,
		// а не шлюз, — иначе nginx может отправить запрос в уже закрываемое соединение.
		IdleTimeout:        90 * time.Second,
		MaxRequestBodySize: 1 << 20,
	}

	utils.StartDebugServer(utils.Env("DEBUG_ADDR", ":6060"))

	go func() {
		if err := server.ListenAndServe(":" + port); err != nil {
			utils.Fatal("HTTP сервер остановлен с ошибкой", "err", err)
		}
	}()
	slog.Info("API Gateway запущен", "http", ":"+port, "auth", authAddr, "account", accountAddr,
		"transaction", txAddr, "request_timeout", requestTimeout.String())

	utils.WaitForShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.ShutdownWithContext(ctx); err != nil {
		slog.Warn("остановка HTTP сервера", "err", err)
	}
	slog.Info("API Gateway остановлен")
}

func httpHealthcheck(url string) error {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("статус %d", resp.StatusCode)
	}
	return nil
}
