package cache

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache — cache-aside поверх Redis. Нулевой указатель — кеш выключен:
// любое чтение возвращает промах, запись и удаление ничего не делают.
//
// Балансы меняются при каждом переводе, поэтому записи живут недолго (ttl),
// а изменяющие операции удаляют затронутые ключи сразу после COMMIT.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

var (
	cacheHits   = expvar.NewInt("cache_hits")
	cacheMisses = expvar.NewInt("cache_misses")
	cacheErrors = expvar.NewInt("cache_errors")
)

func NewRedisCache(addr string, ttl time.Duration) *RedisCache {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolSize:     50,
		MinIdleConns: 5,
	})
	return &RedisCache{client: client, ttl: ttl}
}

func (r *RedisCache) Ping(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.client.Ping(ctx).Err()
}

func (r *RedisCache) Close() error {
	if r == nil {
		return nil
	}
	return r.client.Close()
}

// GetJSON читает значение в dest. Возвращает false при промахе; ошибка Redis — тоже промах.
func (r *RedisCache) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	if r == nil {
		return false, nil
	}
	data, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		cacheMisses.Add(1)
		return false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, dest)
	}
	if err != nil {
		cacheErrors.Add(1)
		return false, err
	}
	cacheHits.Add(1)
	return true, nil
}

func (r *RedisCache) SetJSON(ctx context.Context, key string, value any) error {
	if r == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := r.client.Set(ctx, key, data, r.ttl).Err(); err != nil {
		cacheErrors.Add(1)
		return err
	}
	return nil
}

func (r *RedisCache) Delete(ctx context.Context, keys ...string) error {
	if r == nil || len(keys) == 0 {
		return nil
	}
	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		cacheErrors.Add(1)
		return err
	}
	return nil
}

func AccountKey(accountID string) string {
	return "account:" + accountID
}

func UserAccountsKey(userID string) string {
	return "user:accounts:" + userID
}
