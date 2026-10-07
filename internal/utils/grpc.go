package utils

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"bank_proto_microservice/internal/apperr"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Если клиент не передал дедлайн, сервер всё равно ограничивает время обработки.
const defaultServerTimeout = 10 * time.Second

// NewGRPCServer создаёт gRPC-сервер с перехватчиками (восстановление после паники,
// дедлайн по умолчанию, метрики, debug-лог) и стандартным health-сервисом.
func NewGRPCServer() (*grpc.Server, *health.Server) {
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(recoverInterceptor, observeInterceptor))
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	return srv, hs
}

func recoverInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("паника в обработчике gRPC", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
			err = apperr.New(codes.Internal, apperr.ReasonInternal, "внутренняя ошибка сервиса")
		}
	}()
	return handler(ctx, req)
}

func observeInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultServerTimeout)
		defer cancel()
	}

	start := time.Now()
	resp, err := handler(ctx, req)
	d := time.Since(start)

	outcome := OutcomeOK
	if err != nil {
		outcome = OutcomeError
		if apperr.IsDefinite(err) {
			outcome = OutcomeRejected
		}
	}
	Track("grpc "+info.FullMethod).Observe(d, outcome)
	slog.Debug("gRPC", "method", info.FullMethod, "code", apperr.Code(err).String(), "dur_ms", d.Milliseconds())
	return resp, err
}

// DialGRPC создаёт клиентское соединение. Адрес резолвится через DNS, а round_robin
// распределяет вызовы по всем адресам — это позволяет масштабировать сервис репликами.
//
// WithDisableServiceConfig запрещает DNS-резолверу искать TXT-запись _grpc_config.<host>:
// DNS docker-compose пересылает такой запрос наружу, он висит 5 секунд, и первый вызов
// каждого сервиса упирался в таймаут. Политика балансировки задаётся здесь, в коде.
func DialGRPC(target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableServiceConfig(),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
	)
}

// GRPCHealthcheck опрашивает health-сервис локального gRPC-сервера.
// Используется командой `<binary> healthcheck` в HEALTHCHECK docker-compose.
func GRPCHealthcheck(addr string) error {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("статус %s", resp.GetStatus())
	}
	return nil
}

// RunHealthcheckCommand обрабатывает аргумент `healthcheck`: выполняет check и завершает процесс.
func RunHealthcheckCommand(check func() error) {
	if len(os.Args) < 2 || os.Args[1] != "healthcheck" {
		return
	}
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// WaitForShutdown блокируется до получения SIGINT/SIGTERM.
func WaitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	sig := <-stop
	slog.Info("получен сигнал остановки", "signal", sig.String())
}

// IsCanceled сообщает, что ошибка вызвана отменой или дедлайном контекста.
func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
