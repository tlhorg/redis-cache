package cache

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache interface {
	Get(key string) (string, error)
	Set(key string, value interface{}, expiration time.Duration) error
	Del(key string) error
}

type RedisCache struct {
	client *redis.Client
}

func New(
	host string,
	port string,
	pass string,
	defaultDb string,
) (*RedisCache, error) {
	redisConnURL := fmt.Sprintf(
		"%s:%s",
		host,
		port,
	)

	db, err := strconv.Atoi(defaultDb)

	if err != nil {
		return nil, fmt.Errorf("invalid database number: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisConnURL,
		Password: pass,
		DB:       db,
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("could not connect to client: %w", err)
	}

	return &RedisCache{client: rdb}, nil
}

func (c *RedisCache) Get(key string) (string, error) {
	v, err := c.client.Get(context.Background(), key).Result()

	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		}

		log.Printf("Error while reading RedisCache cache %q\n", err)
		return "", fmt.Errorf("error getting key %s: %w", key, err)
	}

	return v, nil
}

func (c *RedisCache) Set(key string, value interface{}, expiration time.Duration) error {
	if err := c.client.Set(context.Background(), key, value, expiration).Err(); err != nil {
		log.Printf("Error while writing RedisCache cache %q\n", err)
		return err
	}

	return nil
}

func (c *RedisCache) Del(key string) error {
	if err := c.client.Del(context.Background(), key).Err(); err != nil {
		log.Printf("Error while deleting key `%v`", key)

		return err
	}

	return nil
}
