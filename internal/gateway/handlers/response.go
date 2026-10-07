package handlers

import (
	"encoding/json"
	"net/http"

	"bank_proto_microservice/internal/apperr"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func writeJSON(ctx *fasthttp.RequestCtx, statusCode int, v any) {
	ctx.SetContentType("application/json; charset=utf-8")
	ctx.SetStatusCode(statusCode)
	if err := json.NewEncoder(ctx).Encode(v); err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}
}

func writeBadRequest(ctx *fasthttp.RequestCtx, msg string) {
	writeJSON(ctx, fasthttp.StatusBadRequest, map[string]string{"error": msg, "code": apperr.ReasonInvalidArgument})
}

// HTTPStatus — соответствие кодов gRPC и HTTP. Клиенту важно различать три класса:
//
//	4xx — запрос точно не выполнен, повтор без изменений даст тот же результат;
//	409 — конфликт, запрос не выполнен, можно повторить позже;
//	503/504 — сбой или таймаут: для перевода исход может быть неизвестен,
//	          повтор с тем же Idempotency-Key безопасен.
func HTTPStatus(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.FailedPrecondition:
		return http.StatusUnprocessableEntity
	case codes.ResourceExhausted, codes.Unavailable, codes.Canceled:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

// writeError отдаёт ошибку gRPC как JSON: {"error": текст, "code": причина[, "transaction_id": id]}.
func writeError(ctx *fasthttp.RequestCtx, err error) {
	st := status.Convert(err)
	reason, meta := apperr.Info(err)
	code := HTTPStatus(st.Code())

	body := map[string]string{"error": st.Message(), "code": reason}
	if id := meta[apperr.MetaTransactionID]; id != "" {
		body["transaction_id"] = id
	}
	if code == http.StatusServiceUnavailable {
		ctx.Response.Header.Set("Retry-After", "1")
	}
	writeJSON(ctx, code, body)
}
