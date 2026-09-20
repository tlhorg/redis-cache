# Redis Cache

A small Redis cache client for Go, wrapping [go-redis/v9](https://github.com/redis/go-redis) behind a
three-method interface.

## Features

- Context-aware: every call takes a `context.Context` and honours its deadline
- Explicit cache-miss signalling via `ErrCacheMiss`
- Caller mistakes are separable from outages via `ErrInvalidArgument`
- Configuration by struct, or from a `redis://` URL
- Instrumentable: an `Instrument` hook for OpenTelemetry, metrics or tracing
- Errors are wrapped and returned, never logged on your behalf

## Installation

```bash
go get github.com/tlhorg/redis-cache
```

## Prerequisites

- Go 1.24 or higher (see the `go` directive in `go.mod`)
- A reachable Redis server

## Quick Start

```go
package main

import (
	"context"
	"errors"
	"log"
	"time"

	cache "github.com/tlhorg/redis-cache"
)

func main() {
	ctx := context.Background()

	c, err := cache.New(ctx, cache.Options{Addr: "localhost:6379"})
	if err != nil {
		log.Fatal("connecting to Redis: ", err)
	}
	defer c.Close()

	if err := c.Set(ctx, "user:123", "John Doe", 5*time.Minute); err != nil {
		log.Printf("set: %v", err)
	}

	value, err := c.Get(ctx, "user:123")
	switch {
	case errors.Is(err, cache.ErrCacheMiss):
		// Not cached — fall back to the source of truth.
	case err != nil:
		log.Printf("get: %v", err)
	default:
		log.Printf("cached value: %s", value)
	}

	if err := c.Del(ctx, "user:123"); err != nil {
		log.Printf("del: %v", err)
	}
}
```

The package is named `cache` while the import path ends in `redis-cache`, so import it with an
explicit `cache` alias as above.

## API Reference

```go
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
}
```

`Close` is deliberately **not** on the interface — it is a lifecycle concern belonging to whoever
constructed the client, so in-memory fakes don't have to carry a no-op method.

### Constructors

#### `New(ctx context.Context, opts Options) (*RedisCache, error)`

Connects and verifies the connection with a `PING` bounded by `ctx`. `Addr` is required. A failed
ping closes the pool before returning the error.

```go
type Options struct {
	Addr         string // "host:port"; required
	Username     string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	Instrument   Instrumenter // func(*redis.Client) error
}
```

Zero-valued timeouts fall through to the go-redis defaults: `DialTimeout` 5s, `ReadTimeout` 3s,
`WriteTimeout` equal to `ReadTimeout`.

A missing `Addr` returns `ErrInvalidArgument`.

#### `NewFromURL(ctx context.Context, url string, instrument ...Instrumenter) (*RedisCache, error)`

Same, from a connection string — useful when configuration arrives as a `REDIS_URL` environment
variable.

```go
c, err := cache.NewFromURL(ctx, "redis://user:pass@localhost:6379/0")
```

### Methods

#### `Get(ctx, key) (string, error)`

Returns the stored value. Reports `ErrCacheMiss` if the key does not exist — test with
`errors.Is(err, cache.ErrCacheMiss)`.

A key holding an empty string is a **hit**, returning `("", nil)`. This is the distinction
`ErrCacheMiss` exists to make; do not test for a miss by comparing the value to `""`.

#### `Set(ctx, key, value, ttl) error`

Stores `value` at `key`. A `ttl` of zero means no expiry; pass `cache.KeepTTL` to retain the key's
existing expiry. A negative `ttl` is rejected with `ErrInvalidArgument` rather than silently stored
forever.

Redis expiry is millisecond-granular. A `ttl` between 0 and 1ms is rounded **up** to 1ms by go-redis,
which also reports the rounding through its process-global logger — the one path in this package that
can write to stderr. See [Timeouts and cancellation](#timeouts-and-cancellation) for redirecting it.

`value` must be a type go-redis can marshal — `string`, `[]byte`, a numeric type, `bool`,
`time.Time`, `time.Duration`, or an `encoding.BinaryMarshaler`. Anything else fails at run time, not
compile time.

#### `Del(ctx, keys ...string) error`

Removes the given keys in one round trip. Absent keys are ignored, and no keys is a no-op.

#### `Close() error`

Releases the connection pool. Always pair it with a successful constructor call.

## Errors

Two sentinels, both matched with `errors.Is`; everything else is a real failure:

| Sentinel | Meaning | Retry? |
| --- | --- | --- |
| `ErrCacheMiss` | `Get` found no such key | No — fall back to the source of truth |
| `ErrInvalidArgument` | The call was rejected before reaching Redis: missing `Addr`, or a negative `ttl` that isn't `KeepTTL` | No — the same arguments will fail identically |
| *(anything else)* | Transport, protocol or server failure, wrapped with `%w` | Yes, if your call site is idempotent |

`ErrInvalidArgument` exists so a retry wrapper or circuit breaker can tell a caller bug from an
outage. Without it the only way to separate the two is matching on error strings.

`ErrCacheMiss` is returned unwrapped — a miss is the expected hot path for a cache, so it costs no
allocation. Everything else is wrapped with context.

## Instrumentation

```go
type Instrumenter func(*redis.Client) error
```

`Options.Instrument` is called once, with the underlying go-redis client, during construction.
It takes the *client* rather than a `redis.Hook` because that is what the instrumentation libraries
want — `redisotel.InstrumentTracing` and `InstrumentMetrics` are
`func(redis.UniversalClient, ...) error` and register their own hooks internally:

```go
import "github.com/redis/go-redis/extra/redisotel/v9"

c, err := cache.New(ctx, cache.Options{
	Addr:       "localhost:6379",
	Instrument: func(rdb *redis.Client) error { return redisotel.InstrumentTracing(rdb) },
})
```

A plain hook works the same way:

```go
Instrument: func(rdb *redis.Client) error { rdb.AddHook(myHook{}); return nil },
```

It runs **before** the connection-verifying `PING`, so that `PING` is instrumented too and a failed
connect is visible to tracing rather than happening behind it. An error returned from `Instrument`
fails construction and closes the pool. Note that go-redis's RESP3 handshake (`HELLO`) also passes
through any hook you register, on dial.

`redisotel` lives in a separate module (`github.com/redis/go-redis/extra/redisotel/v9`) which this
package deliberately does not depend on — it pulls in the OpenTelemetry SDK, and that belongs in your
application's dependency graph, not in a cache wrapper's.

This package emits no metrics, traces or logs of its own; `Instrument` is the only seam, and the only
place the underlying client is exposed.

## Timeouts and cancellation

The client is constructed with go-redis's `ContextTimeoutEnabled` forced on. Without it, go-redis
substitutes `context.Background()` for your context at the socket layer and every deadline you pass
is silently discarded. With it, a `context.WithTimeout` around any call is respected.

This library does not write to the `log` package. go-redis itself still logs some conditions to
stderr through its own logger; `redis.SetLogger` can redirect that, but it is process-global, so
that call belongs in your application rather than here.

## Migrating from v0.1.x

v0.2.0 is a breaking release.

| v0.1.x | v0.2.0 |
| --- | --- |
| `New("localhost", "6379", "", "0")` | `New(ctx, Options{Addr: "localhost:6379"})` |
| `Get(key)` | `Get(ctx, key)` |
| `Set(key, value, ttl)` | `Set(ctx, key, value, ttl)` |
| `Del(key)` | `Del(ctx, key)` — now variadic |
| miss returns `("", nil)` | miss returns `ErrCacheMiss` |
| `if value != ""` | `if errors.Is(err, cache.ErrCacheMiss)` |
| errors logged and returned | errors wrapped and returned only |
| no way to release the pool | `defer c.Close()` |

Because this module is `v0`, `go get -u` will move you across this break without a new module path.
Pin a tag if that matters to you.

## Migrating from v0.2.x

v0.3.0 is additive apart from two error strings. No call site needs to change.

| v0.2.x | v0.3.0 |
| --- | --- |
| no way to instrument the client | `Options.Instrument`, and variadic `instrument` on `NewFromURL` |
| validation errors matched by string | `errors.Is(err, cache.ErrInvalidArgument)` |
| `"cache: Addr is required"` | `"cache: invalid argument: Addr is required"` |
| `"cache: set k: negative ttl -5s"` | `"cache: invalid argument: set k: negative ttl -5s"` |

`NewFromURL` gained a variadic parameter, which is source-compatible: existing two-argument calls
still compile. Only code asserting on those two exact error *strings* needs attention — and the
point of `ErrInvalidArgument` is that it no longer has to.

## Development

```bash
make test       # go test -v ./...
make cover      # race detector + coverage report
make lint       # golangci-lint
make vulncheck  # govulncheck
```

Tests run against [miniredis](https://github.com/alicebob/miniredis), an in-process Redis, so no
server or Docker is needed.

## Dependencies

- [go-redis/v9](https://github.com/redis/go-redis)
- [miniredis/v2](https://github.com/alicebob/miniredis) and
  [testify](https://github.com/stretchr/testify) (test-only)

## License

MIT — see [LICENSE](LICENSE).
