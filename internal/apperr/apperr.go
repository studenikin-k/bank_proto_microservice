// Package apperr — единый формат ошибок между сервисами.
//
// Каждая ошибка gRPC несёт стандартный код (codes.Code) и машиночитаемую причину
// (Reason) в деталях google.rpc.ErrorInfo. Код определяет HTTP-статус в шлюзе,
// причина уходит клиенту в поле "code" и позволяет отличать, например,
// «недостаточно средств» от «счёт закрыт», не разбирая текст сообщения.
package apperr

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const domain = "bank"

// Причины ошибок.
const (
	ReasonInvalidArgument     = "INVALID_ARGUMENT"
	ReasonUnauthenticated     = "UNAUTHENTICATED"
	ReasonBadCredentials      = "BAD_CREDENTIALS"
	ReasonUserExists          = "USER_EXISTS"
	ReasonUserNotFound        = "USER_NOT_FOUND"
	ReasonAccountNotFound     = "ACCOUNT_NOT_FOUND"
	ReasonRecipientNotFound   = "RECIPIENT_NOT_FOUND"
	ReasonAccountForbidden    = "ACCOUNT_FORBIDDEN"
	ReasonAccountClosed       = "ACCOUNT_CLOSED"
	ReasonRecipientClosed     = "RECIPIENT_CLOSED"
	ReasonAccountLimit        = "ACCOUNT_LIMIT_REACHED"
	ReasonInsufficientFunds   = "INSUFFICIENT_FUNDS"
	ReasonTransferAborted     = "TRANSFER_ABORTED"
	ReasonIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	ReasonConcurrencyConflict = "CONCURRENCY_CONFLICT"
	ReasonTransactionNotFound = "TRANSACTION_NOT_FOUND"
	ReasonOutcomeUnknown      = "OUTCOME_UNKNOWN"
	ReasonOverloaded          = "OVERLOADED"
	ReasonTimeout             = "TIMEOUT"
	ReasonUnavailable         = "SERVICE_UNAVAILABLE"
	ReasonInternal            = "INTERNAL"
)

// MetaTransactionID — ключ метаданных с ID перевода, исход которого неизвестен.
const MetaTransactionID = "transaction_id"

// New создаёт gRPC-ошибку с кодом, причиной и необязательными метаданными (пары ключ-значение).
func New(code codes.Code, reason, message string, kv ...string) error {
	st := status.New(code, message)
	info := &errdetails.ErrorInfo{Reason: reason, Domain: domain}
	if len(kv) > 1 {
		info.Metadata = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			info.Metadata[kv[i]] = kv[i+1]
		}
	}
	if withInfo, err := st.WithDetails(info); err == nil {
		return withInfo.Err()
	}
	return st.Err()
}

// Info извлекает причину и метаданные из gRPC-ошибки.
// Если причины нет, она выводится из кода.
func Info(err error) (reason string, meta map[string]string) {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason(), info.GetMetadata()
		}
	}
	return defaultReason(st.Code()), nil
}

// Reason — сокращение для Info без метаданных.
func Reason(err error) string {
	r, _ := Info(err)
	return r
}

// Code возвращает gRPC-код ошибки, в том числе для ошибок контекста.
func Code(err error) codes.Code {
	if errors.Is(err, context.DeadlineExceeded) {
		return codes.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return codes.Canceled
	}
	return status.Code(err)
}

// IsDefinite сообщает, что сервис-получатель точно не выполнил операцию:
// он проверил запрос и отказал (бизнес-правило, валидация, конфликт с откатом).
// Для остальных кодов (таймаут, обрыв, внутренняя ошибка) исход неизвестен:
// операция могла успеть выполниться.
func IsDefinite(err error) bool {
	switch Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied,
		codes.FailedPrecondition, codes.AlreadyExists, codes.Aborted,
		codes.OutOfRange, codes.Unauthenticated:
		return true
	}
	return false
}

// CodeForReason восстанавливает gRPC-код по сохранённой причине отказа
// (нужно, чтобы повтор запроса с тем же ключом идемпотентности вернул ту же ошибку).
func CodeForReason(reason string) codes.Code {
	switch reason {
	case ReasonInvalidArgument:
		return codes.InvalidArgument
	case ReasonAccountNotFound, ReasonRecipientNotFound, ReasonTransactionNotFound, ReasonUserNotFound:
		return codes.NotFound
	case ReasonAccountForbidden:
		return codes.PermissionDenied
	case ReasonAccountClosed, ReasonRecipientClosed, ReasonInsufficientFunds,
		ReasonTransferAborted, ReasonAccountLimit:
		return codes.FailedPrecondition
	case ReasonIdempotencyConflict, ReasonUserExists:
		return codes.AlreadyExists
	case ReasonConcurrencyConflict:
		return codes.Aborted
	case ReasonOverloaded:
		return codes.ResourceExhausted
	case ReasonTimeout:
		return codes.DeadlineExceeded
	case ReasonUnavailable, ReasonOutcomeUnknown:
		return codes.Unavailable
	}
	return codes.Internal
}

func defaultReason(c codes.Code) string {
	switch c {
	case codes.InvalidArgument, codes.OutOfRange:
		return ReasonInvalidArgument
	case codes.Unauthenticated:
		return ReasonUnauthenticated
	case codes.NotFound:
		return "NOT_FOUND"
	case codes.PermissionDenied:
		return "PERMISSION_DENIED"
	case codes.AlreadyExists:
		return "ALREADY_EXISTS"
	case codes.FailedPrecondition:
		return "FAILED_PRECONDITION"
	case codes.Aborted:
		return ReasonConcurrencyConflict
	case codes.ResourceExhausted:
		return ReasonOverloaded
	case codes.DeadlineExceeded:
		return ReasonTimeout
	case codes.Unavailable, codes.Canceled:
		return ReasonUnavailable
	}
	return ReasonInternal
}

// FromContext превращает ошибку контекста (дедлайн, отмена) в gRPC-ошибку, иначе возвращает nil.
func FromContext(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return New(codes.DeadlineExceeded, ReasonTimeout, "превышено время ожидания")
	case errors.Is(err, context.Canceled):
		return New(codes.Canceled, ReasonUnavailable, "запрос отменён")
	}
	return nil
}
