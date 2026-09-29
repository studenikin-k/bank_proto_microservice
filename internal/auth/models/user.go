package models

import "time"

type User struct {
	ID           string
	Name         string
	PasswordHash string
	CreatedAt    time.Time
}
