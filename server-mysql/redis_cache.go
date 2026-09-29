package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	loginKeyTTL         = 5 * time.Minute
	loginVoucherTTL     = 30 * 24 * time.Hour
	onlineTTL           = 90 * time.Second
	configReloadChannel = "mhq:config:reload"
)

type RedisCache struct {
	client *redis.Client
	prefix string
}

// PublishConfigReload broadcasts a committed MySQL configuration revision.
// The payload is deliberately plain text (revision|name,name) so the runtime
// control plane does not introduce another JSON persistence format.
func (cache *RedisCache) PublishConfigReload(revision int64, names []string) error {
	if cache == nil || cache.client == nil {
		return fmt.Errorf("Redis cache is not initialized")
	}
	payload := strconv.FormatInt(revision, 10)
	if len(names) > 0 {
		payload += "|" + strings.Join(names, ",")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return cache.client.Publish(ctx, configReloadChannel, payload).Err()
}

func (cache *RedisCache) SubscribeConfigReload(ctx context.Context) *redis.PubSub {
	if cache == nil || cache.client == nil {
		return nil
	}
	return cache.client.Subscribe(ctx, configReloadChannel)
}

func OpenRedisCache(addr, password string, db int) (*RedisCache, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("Redis address is required")
	}
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("connect Redis %s: %w", addr, err)
	}
	return &RedisCache{client: client, prefix: "mhq:"}, nil
}

func (cache *RedisCache) Close() error {
	if cache == nil || cache.client == nil {
		return nil
	}
	return cache.client.Close()
}

func (cache *RedisCache) loginKey(key int64) string {
	return cache.prefix + "login:key:" + strconv.FormatInt(key, 10)
}

func (cache *RedisCache) loginVoucher(key int64) string {
	return cache.prefix + "login:voucher:" + strconv.FormatInt(key, 10)
}

func (cache *RedisCache) onlineKey(playerID int64) string {
	return cache.prefix + "online:player:" + strconv.FormatInt(playerID, 10)
}

func (cache *RedisCache) PutLoginKey(key, accountID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return cache.client.Set(ctx, cache.loginKey(key), accountID, loginKeyTTL).Err()
}

func (cache *RedisCache) ConsumeLoginKey(key int64) (int64, bool, error) {
	return cache.consumeCredential(cache.loginKey(key))
}

func (cache *RedisCache) PutLoginVoucher(key, accountID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return cache.client.Set(ctx, cache.loginVoucher(key), accountID, loginVoucherTTL).Err()
}

func (cache *RedisCache) ConsumeLoginVoucher(key int64) (int64, bool, error) {
	return cache.consumeCredential(cache.loginVoucher(key))
}

func (cache *RedisCache) consumeCredential(key string) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	value, err := cache.client.GetDel(ctx, key).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	accountID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || accountID <= 0 {
		return 0, false, err
	}
	return accountID, true, nil
}

func (cache *RedisCache) SetPlayerOnline(playerID int64, online bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !online {
		return cache.client.Del(ctx, cache.onlineKey(playerID)).Err()
	}
	return cache.client.Set(ctx, cache.onlineKey(playerID), "1", onlineTTL).Err()
}

func (cache *RedisCache) RefreshPlayerOnline(playerID int64) error {
	if playerID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return cache.client.Expire(ctx, cache.onlineKey(playerID), onlineTTL).Err()
}

func (cache *RedisCache) IsPlayerOnline(playerID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	count, err := cache.client.Exists(ctx, cache.onlineKey(playerID)).Result()
	return count > 0, err
}
