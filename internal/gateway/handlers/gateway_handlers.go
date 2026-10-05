package handlers

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"bank_proto_microservice/internal/money"
	accountpb "bank_proto_microservice/proto/account"
	authpb "bank_proto_microservice/proto/auth"
	transactionpb "bank_proto_microservice/proto/transaction"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Downstream — gRPC-соединение с сервисом (для readiness-проверки).
type Downstream struct {
	Name string
	Conn *grpc.ClientConn
}

type GatewayHandler struct {
	authClient        authpb.AuthServiceClient
	accountClient     accountpb.AccountServiceClient
	transactionClient transactionpb.TransactionServiceClient
	downstreams       []Downstream
	timeout           time.Duration // дедлайн каждого вызова gRPC-сервиса
	instance          string
}

func NewGatewayHandler(
	authConn, accountConn, txConn *grpc.ClientConn,
	timeout time.Duration,
) *GatewayHandler {
	instance, _ := os.Hostname()
	return &GatewayHandler{
		authClient:        authpb.NewAuthServiceClient(authConn),
		accountClient:     accountpb.NewAccountServiceClient(accountConn),
		transactionClient: transactionpb.NewTransactionServiceClient(txConn),
		downstreams: []Downstream{
			{"auth", authConn}, {"account", accountConn}, {"transaction", txConn},
		},
		timeout:  timeout,
		instance: instance,
	}
}

// call создаёт контекст с дедлайном для вызова сервиса. fasthttp.RequestCtx
// дедлайна не несёт, поэтому без этого зависший сервис держал бы запрос бесконечно.
func (h *GatewayHandler) call() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), h.timeout)
}

func userID(ctx *fasthttp.RequestCtx) string {
	id, _ := ctx.UserValue("user_id").(string)
	return id
}

func pathID(ctx *fasthttp.RequestCtx) string {
	id, _ := ctx.UserValue("id").(string)
	return id
}

func decode(ctx *fasthttp.RequestCtx, dst any) bool {
	if err := json.Unmarshal(ctx.PostBody(), dst); err != nil {
		writeBadRequest(ctx, "неверный формат JSON: "+err.Error())
		return false
	}
	return true
}

// ==================== HEALTH ====================

func (h *GatewayHandler) Health(ctx *fasthttp.RequestCtx) {
	writeJSON(ctx, fasthttp.StatusOK, map[string]string{
		"status":   "OK",
		"time":     time.Now().Format(time.RFC3339),
		"message":  "Bank Microservices API Gateway is running",
		"instance": h.instance,
	})
}

// Ready проверяет, что все gRPC-сервисы отвечают на health-check.
func (h *GatewayHandler) Ready(ctx *fasthttp.RequestCtx) {
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result := map[string]string{}
	ready := true
	for _, d := range h.downstreams {
		resp, err := healthpb.NewHealthClient(d.Conn).Check(c, &healthpb.HealthCheckRequest{})
		switch {
		case err != nil:
			result[d.Name], ready = err.Error(), false
		case resp.GetStatus() != healthpb.HealthCheckResponse_SERVING:
			result[d.Name], ready = resp.GetStatus().String(), false
		default:
			result[d.Name] = "SERVING"
		}
	}
	code := fasthttp.StatusOK
	if !ready {
		code = fasthttp.StatusServiceUnavailable
	}
	writeJSON(ctx, code, map[string]any{"ready": ready, "services": result, "instance": h.instance})
}

// ==================== AUTH ====================

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *GatewayHandler) Register(ctx *fasthttp.RequestCtx) {
	var req credentials
	if !decode(ctx, &req) {
		return
	}
	c, cancel := h.call()
	defer cancel()
	resp, err := h.authClient.Register(c, &authpb.RegisterRequest{Name: req.Name, Password: req.Password})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusCreated, map[string]string{
		"message":    "Пользователь успешно зарегистрирован",
		"user_id":    resp.GetUserId(),
		"name":       resp.GetName(),
		"created_at": resp.GetCreatedAt(),
	})
}

func (h *GatewayHandler) Login(ctx *fasthttp.RequestCtx) {
	var req credentials
	if !decode(ctx, &req) {
		return
	}
	c, cancel := h.call()
	defer cancel()
	resp, err := h.authClient.Login(c, &authpb.LoginRequest{Name: req.Name, Password: req.Password})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]string{
		"message":    "Вход выполнен успешно",
		"token":      resp.GetToken(),
		"user_id":    resp.GetUserId(),
		"name":       resp.GetName(),
		"expires_in": resp.GetExpiresIn(),
	})
}

func (h *GatewayHandler) DeleteUser(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.authClient.DeleteUser(c, &authpb.DeleteUserRequest{UserId: userID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]string{"message": resp.GetMessage(), "user_id": userID(ctx)})
}

// ==================== ACCOUNTS ====================

type accountJSON struct {
	ID        string       `json:"id"`
	AccountID string       `json:"account_id"` // дублирует id: его читают k6-сценарии
	Balance   money.Amount `json:"balance"`
	Status    string       `json:"status"`
	CreatedAt string       `json:"created_at"`
}

func toAccountJSON(a *accountpb.AccountResponse) accountJSON {
	return accountJSON{
		ID:        a.GetId(),
		AccountID: a.GetId(),
		Balance:   money.Amount(a.GetBalance()),
		Status:    a.GetStatus(),
		CreatedAt: a.GetCreatedAt(),
	}
}

func (h *GatewayHandler) CreateAccount(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.accountClient.CreateAccount(c, &accountpb.CreateAccountRequest{UserId: userID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusCreated, toAccountJSON(resp))
}

func (h *GatewayHandler) GetAccounts(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.accountClient.GetUserAccounts(c, &accountpb.GetUserAccountsRequest{UserId: userID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	accounts := make([]accountJSON, 0, len(resp.GetAccounts()))
	for _, a := range resp.GetAccounts() {
		accounts = append(accounts, toAccountJSON(a))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{
		"accounts":        accounts,
		"total":           resp.GetTotal(),
		"active_count":    resp.GetActiveCount(),
		"closed_count":    resp.GetClosedCount(),
		"max_accounts":    resp.GetMaxAccounts(),
		"can_create_more": resp.GetCanCreateMore(),
	})
}

func (h *GatewayHandler) GetAccountByID(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.accountClient.GetAccount(c, &accountpb.GetAccountRequest{AccountId: pathID(ctx), UserId: userID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, toAccountJSON(resp))
}

func (h *GatewayHandler) DeleteAccount(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.accountClient.DeleteAccount(c, &accountpb.DeleteAccountRequest{AccountId: pathID(ctx), UserId: userID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]string{"message": resp.GetMessage(), "account_id": resp.GetAccountId()})
}

// ==================== TRANSACTIONS ====================

type transactionJSON struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	FromAccountID string       `json:"from_account_id"`
	ToAccountID   string       `json:"to_account_id"`
	Amount        money.Amount `json:"amount"`
	FeePercent    int32        `json:"fee_percent"`
	FeeAmount     money.Amount `json:"fee_amount"`
	TotalDebit    money.Amount `json:"total_debit"`
	Status        string       `json:"status"`
	FailureReason string       `json:"failure_reason,omitempty"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
}

func toTransactionJSON(t *transactionpb.TransactionResponse) transactionJSON {
	return transactionJSON{
		ID:            t.GetId(),
		Type:          t.GetType(),
		FromAccountID: t.GetFromAccountId(),
		ToAccountID:   t.GetToAccountId(),
		Amount:        money.Amount(t.GetAmount()),
		FeePercent:    t.GetFeePercent(),
		FeeAmount:     money.Amount(t.GetFeeAmount()),
		TotalDebit:    money.Amount(t.GetTotalDebit()),
		Status:        t.GetStatus(),
		FailureReason: t.GetFailureReason(),
		CreatedAt:     t.GetCreatedAt(),
		UpdatedAt:     t.GetUpdatedAt(),
	}
}

type transactionRequest struct {
	Type          string       `json:"type"` // "transfer" (по умолчанию) или "payment"
	FromAccountID string       `json:"from_account_id"`
	ToAccountID   string       `json:"to_account_id"`
	Amount        money.Amount `json:"amount"`
}

// CreateTransaction возвращает обработчик перевода. forcedType задаёт тип операции
// для маршрутов /transactions/transfer и /transactions/payment; для POST /transactions
// тип берётся из тела запроса. Заголовок Idempotency-Key делает повтор запроса безопасным.
func (h *GatewayHandler) CreateTransaction(forcedType string, async bool) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		var req transactionRequest
		if !decode(ctx, &req) {
			return
		}
		if forcedType != "" {
			req.Type = forcedType
		}
		grpcReq := &transactionpb.CreateTransactionRequest{
			UserId:         userID(ctx),
			IdempotencyKey: string(ctx.Request.Header.Peek("Idempotency-Key")),
			Type:           req.Type,
			FromAccountId:  req.FromAccountID,
			ToAccountId:    req.ToAccountID,
			Amount:         req.Amount.Kopecks(),
		}

		c, cancel := h.call()
		defer cancel()
		var resp *transactionpb.TransactionResponse
		var err error
		if async {
			resp, err = h.transactionClient.CreateTransactionAsync(c, grpcReq)
		} else {
			resp, err = h.transactionClient.CreateTransaction(c, grpcReq)
		}
		if err != nil {
			writeError(ctx, err)
			return
		}
		if resp.GetIdempotentReplay() {
			ctx.Response.Header.Set("Idempotent-Replayed", "true")
		}

		if async {
			writeJSON(ctx, fasthttp.StatusAccepted, map[string]any{
				"status":         "accepted",
				"message":        "Перевод принят в обработку, итог — GET /transactions/" + resp.GetId(),
				"transaction_id": resp.GetId(),
				"transaction":    toTransactionJSON(resp),
			})
			return
		}
		writeJSON(ctx, fasthttp.StatusCreated, map[string]any{
			"message":     resp.GetType() + " successful",
			"transaction": toTransactionJSON(resp),
		})
	}
}

func (h *GatewayHandler) GetHistory(ctx *fasthttp.RequestCtx) {
	req := &transactionpb.GetHistoryRequest{UserId: userID(ctx)}
	if accountID := ctx.QueryArgs().Peek("account_id"); len(accountID) > 0 {
		s := string(accountID)
		req.AccountId = &s
	}
	if limit := ctx.QueryArgs().Peek("limit"); len(limit) > 0 {
		n, err := strconv.Atoi(string(limit))
		if err != nil || n <= 0 {
			writeBadRequest(ctx, "limit должен быть положительным целым числом")
			return
		}
		req.Limit = int32(min(n, 1000))
	}

	c, cancel := h.call()
	defer cancel()
	resp, err := h.transactionClient.GetHistory(c, req)
	if err != nil {
		writeError(ctx, err)
		return
	}
	list := make([]transactionJSON, 0, len(resp.GetTransactions()))
	for _, t := range resp.GetTransactions() {
		list = append(list, toTransactionJSON(t))
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"transactions": list, "total": resp.GetTotal()})
}

func (h *GatewayHandler) GetTransactionByID(ctx *fasthttp.RequestCtx) {
	c, cancel := h.call()
	defer cancel()
	resp, err := h.transactionClient.GetByID(c, &transactionpb.GetByIDRequest{UserId: userID(ctx), TransactionId: pathID(ctx)})
	if err != nil {
		writeError(ctx, err)
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, toTransactionJSON(resp))
}
