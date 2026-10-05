package models

import (
	"time"

	"bank_proto_microservice/internal/money"
)

const (
	TypeTransfer = "transfer" // перевод между счетами, комиссия 1%
	TypePayment  = "payment"  // платёж, комиссия 3%

	StatusPending   = "pending"   // запись создана, исход шага в Account Service ещё не зафиксирован
	StatusCompleted = "completed" // деньги переведены
	StatusFailed    = "failed"    // перевод отклонён или отменён, деньги не списаны
)

// FeePercent возвращает комиссию для типа операции.
func FeePercent(txType string) (int, bool) {
	switch txType {
	case TypeTransfer:
		return 1, true
	case TypePayment:
		return 3, true
	}
	return 0, false
}

type Transaction struct {
	ID             string
	UserID         string
	IdempotencyKey string
	Type           string
	FromAccountID  string
	ToAccountID    string
	Amount         money.Amount
	FeePercent     int
	FeeAmount      money.Amount
	TotalDebit     money.Amount
	FeeAccountID   string
	Status         string
	FailureReason  string
	FailureMessage string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SameRequest сообщает, что две операции описывают один и тот же запрос клиента.
func (t *Transaction) SameRequest(o *Transaction) bool {
	return t.Type == o.Type && t.FromAccountID == o.FromAccountID &&
		t.ToAccountID == o.ToAccountID && t.Amount == o.Amount
}
