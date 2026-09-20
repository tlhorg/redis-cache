// Package cache provides a small Redis-backed cache client.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrCacheMiss is returned by Get when the key does not exist. Use errors.Is to
// test for it; a key holding an empty string is a hit and returns ("", nil).
//
// It is returned unwrapped: a miss is the expected hot path for a cache, so it
// costs no allocation.
var ErrCacheMiss = errors.New("cache: key not found")

// ErrInvalidArgument is returned when a call is rejected before it reaches
// Redis, because the arguments themselves are wrong: a missing Addr, or a
// negative ttl that is not KeepTTL.
//
// It exists so a caller can distinguish its own bug from an outage. A retry or
// circuit breaker should never retry an ErrInvalidArgument — the same arguments
// will fail the same way — whereas a transport error is worth retrying.
var ErrInvalidArgument = errors.New("cache: invalid argument")

// KeepTTL can be passed as the ttl argument to Set to keep the key's existing
// expiry. It is re-exported so callers need not import go-redis directly.
const KeepTTL = redis.KeepTTL

// Cache is the set of operations a cache backend provides. Lifecycle is not part
// of this interface: closing belongs to whoever constructed the concrete client.
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
}

// Options configures a RedisCache. Zero-valued timeout fields fall through to
// the go-redis defaults: DialTimeout 5s, ReadTimeout 3s, WriteTimeout equal to
// ReadTimeout.
type Options struct {
	Addr         string // "host:port"; required
	Username     string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// Instrument, if non-nil, is the seam for tracing and metrics. See
	// Instrumenter.
	Instrument Instrumenter
}

// Instrumenter is handed the underlying go-redis client during construction,
// before the connection-verifying PING, so instrumentation also covers that
// PING and a failed connect is visible to tracing rather than happening behind
// it.
//
// It takes the client rather than a redis.Hook because that is what the
// instrumentation libraries want: redisotel.InstrumentTracing and
// InstrumentMetrics are func(redis.UniversalClient, ...) error and register
// their hooks themselves. A plain hook still works:
//
//	func(rdb *redis.Client) error { rdb.AddHook(myHook{}); return nil }
//
// Returning an error fails construction and closes the pool. This is the only
// place the client escapes; nothing in this package emits traces, metrics or
// logs on its own.
type Instrumenter func(*redis.Client) error

// RedisCache is a Cache backed by a Redis server.
type RedisCache struct {
	client *redis.Client
}

var _ Cache = (*RedisCache)(nil)

// New connects to Redis and verifies the connection with a PING bounded by ctx.
// The returned cache owns its connection pool; call Close when done with it.
func New(ctx context.Context, opts Options) (*RedisCache, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("%w: Addr is required", ErrInvalidArgument)
	}

	return newCache(ctx, &redis.Options{
		Addr:         opts.Addr,
		Username:     opts.Username,
		Password:     opts.Password,
		DB:           opts.DB,
		DialTimeout:  opts.DialTimeout,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	}, opts.Instrument)
}

// NewFromURL connects using a Redis connection string, for example
// "redis://user:pass@localhost:6379/0". The optional instrument argument is
// applied exactly as Options.Instrument is by New.
func NewFromURL(ctx context.Context, url string, instrument ...Instrumenter) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("cache: parse url: %w", err)
	}

	return newCache(ctx, opts, instrument...)
}

func newCache(ctx context.Context, opts *redis.Options, instrument ...Instrumenter) (*RedisCache, error) {
	// go-redis defaults this to false, in which case baseClient.context()
	// replaces the caller's context with context.Background() before the socket
	// read/write — silently discarding every deadline and making the ctx
	// arguments on this API decorative. Forced on, not exposed as an option.
	opts.ContextTimeoutEnabled = true

	rdb := redis.NewClient(opts)

	// Before the PING, so the connection check is instrumented like any other
	// call rather than slipping past the hooks.
	for _, fn := range instrument {
		if fn == nil {
			continue
		}

		if err := fn(rdb); err != nil {
			// Don't leak the pool we just created.
			_ = rdb.Close()
			return nil, fmt.Errorf("cache: instrument: %w", err)
		}
	}

	if err := rdb.Ping(ctx).Err(); err != nil {
		// Don't leak the pool we just created.
		_ = rdb.Close()
		return nil, fmt.Errorf("cache: connect %s: %w", opts.Addr, err)
	}

	return &RedisCache{client: rdb}, nil
}

// Get returns the value stored at key. It reports ErrCacheMiss if the key does
// not exist.
func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	v, err := c.client.Get(ctx, key).Result()

	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", ErrCacheMiss
		}

		return "", fmt.Errorf("cache: get %s: %w", key, err)
	}

	return v, nil
}

// Set stores value at key. A ttl of zero means the key does not expire; pass
// KeepTTL to retain the key's existing expiry. A negative ttl that is not
// KeepTTL is rejected with ErrInvalidArgument.
//
// Redis expiry has millisecond resolution. A ttl in (0, 1ms) is rounded up to
// 1ms by go-redis, which also reports the rounding through its process-global
// logger — the one case where a call here can produce output on stderr. Use
// redis.SetLogger from your application to redirect it.
//
// value must be a type go-redis can marshal: string, []byte, a numeric type,
// bool, time.Time, time.Duration, or an encoding.BinaryMarshaler. Anything else
// fails at run time, not compile time.
func (c *RedisCache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	// go-redis only emits EX/PX for ttl > 0 and KEEPTTL for exactly -1, so any
	// other negative duration would silently store the key with no expiry at
	// all. Reject it rather than persist data the caller meant to expire.
	if ttl < 0 && ttl != KeepTTL {
		return fmt.Errorf("%w: set %s: negative ttl %s", ErrInvalidArgument, key, ttl)
	}

	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache: set %s: %w", key, err)
	}

	return nil
}

// Del removes the given keys. Keys that do not exist are ignored.
func (c *RedisCache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}

	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache: del: %w", err)
	}

	return nil
}

// Close releases the underlying connection pool. It is safe to call more than
// once; subsequent calls return an error reporting that the client is closed.
func (c *RedisCache) Close() error {
	if err := c.client.Close(); err != nil {
		return fmt.Errorf("cache: close: %w", err)
	}

	return nil
}
