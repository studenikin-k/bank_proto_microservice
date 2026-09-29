package repository

import (
	"context"
	"fmt"

	"bank_proto_microservice/internal/auth/models"
	"bank_proto_microservice/internal/utils"

	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) *UserRepository {
	utils.LogSuccess("UserRepository", "Инициализирован репозиторий пользователей")
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(ctx context.Context, user *models.User) error {
	query := `INSERT INTO users (name, password_hash) VALUES ($1, $2) RETURNING id, created_at`

	utils.LogDB("CREATE USER", fmt.Sprintf("Создание пользователя: %s", user.Name))

	err := r.db.QueryRow(ctx, query, user.Name, user.PasswordHash).Scan(&user.ID, &user.CreatedAt)
	if err != nil {
		utils.LogError("UserRepository", fmt.Sprintf("Ошибка создания пользователя %s", user.Name), err)
		return err
	}

	utils.LogSuccess("UserRepository", fmt.Sprintf("Пользователь создан: %s (ID: %s)", user.Name, user.ID))
	return nil
}

func (r *UserRepository) GetByName(ctx context.Context, name string) (*models.User, error) {
	query := `SELECT id, name, password_hash, created_at FROM users WHERE name = $1`

	utils.LogDB("GET USER", fmt.Sprintf("Поиск пользователя: %s", name))

	user := &models.User{}
	err := r.db.QueryRow(ctx, query, name).Scan(&user.ID, &user.Name, &user.PasswordHash, &user.CreatedAt)
	if err != nil {
		utils.LogWarning("UserRepository", fmt.Sprintf("Пользователь не найден: %s", name))
		return nil, err
	}

	utils.LogSuccess("UserRepository", fmt.Sprintf("Пользователь найден: %s (ID: %s)", user.Name, user.ID))
	return user, nil
}

func (r *UserRepository) Delete(ctx context.Context, userID string) error {
	query := `DELETE FROM users WHERE id = $1`

	utils.LogDB("DELETE USER", fmt.Sprintf("Удаление пользователя: %s", userID))

	result, err := r.db.Exec(ctx, query, userID)
	if err != nil {
		utils.LogError("UserRepository", fmt.Sprintf("Ошибка удаления пользователя %s", userID), err)
		return err
	}

	if result.RowsAffected() == 0 {
		utils.LogWarning("UserRepository", fmt.Sprintf("Пользователь %s не найден для удаления", userID))
		return fmt.Errorf("user not found")
	}

	utils.LogSuccess("UserRepository", fmt.Sprintf("Пользователь удалён: %s", userID))
	return nil
}
