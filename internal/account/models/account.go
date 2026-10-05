package models

import (
	"time"

	"bank_proto_microservice/internal/money"
)

const (
	StatusActive = "active"
	StatusClosed = "closed"
)

type Account struct {
	ID        string       `json:"id"`
	UserID    string       `json:"user_id"`
	Balance   money.Amount `json:"balance"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
}

// Transfer — шаг саги, который выполняет Account Service.
type Transfer struct {
	ID     string // transfer_id = ID записи в Transaction Service
	UserID string // инициатор, должен владеть счётом списания
	FromID string
	ToID   string
	Amount money.Amount // зачисляется получателю
	Fee    money.Amount // комиссия, списывается сверх Amount
}

// ApplyResult — итог ApplyTransfer.
type ApplyResult struct {
	AlreadyApplied bool
	FromUserID     string // владельцы счетов — для инвалидации кеша
	ToUserID       string
}
