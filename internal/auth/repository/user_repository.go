package repository

import (
	"context"
	"errors"

	"bank_proto_microservice/internal/auth/models"
	"bank_proto_microservice/internal/utils"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUserExists   = errors.New("пользователь с таким именем уже существует")
	ErrUserNotFound = errors.New("пользователь не найден")
)

type UserRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(ctx context.Context, user *models.User) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO users (name, password_hash) VALUES ($1, $2) RETURNING id, created_at`,
		user.Name, user.PasswordHash,
	).Scan(&user.ID, &user.CreatedAt)
	if utils.PgCode(err) == utils.PgUniqueViolation {
		return ErrUserExists
	}
	return err
}

func (r *UserRepository) GetByName(ctx context.Context, name string) (*models.User, error) {
	user := &models.User{}
	err := r.db.QueryRow(ctx,
		`SELECT id, name, password_hash, created_at FROM users WHERE name = $1`, name,
	).Scan(&user.ID, &user.Name, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return user, err
}

func (r *UserRepository) Delete(ctx context.Context, userID string) error {
	result, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
