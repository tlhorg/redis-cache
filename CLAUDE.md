# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A single-package Go library (`package cache`, module `github.com/tlhorg/redis-cache`) wrapping
`go-redis/v9` behind a three-method `Cache` interface. There is no binary and no `main` — it is
consumed as a dependency. Note the package name (`cache`) differs from the last path element
(`redis-cache`), so consumers import it with an explicit alias.

## Commands

```bash
make test        # go test -v ./...
make cover       # go test -race -covermode=atomic + per-function coverage report
make lint        # golangci-lint run  (no config file in repo -> golangci-lint defaults)
make vulncheck   # govulncheck -show verbose ./...
make clean       # go mod tidy + clean build/test caches
make upgrade     # go get -u all
```

Run a single test: `go test -v -run TestGetMissingKeyReturnsErrCacheMiss ./...`

`golangci-lint` and `govulncheck` are not vendored; they must be installed on the machine.

**After `make upgrade`, always check `git diff go.mod` before committing.** It runs `go get -u all`,
which upgrades *indirect* dependencies to latest and will rewrite the `go` directive if any of them
demands a newer toolchain. This has already happened once: `x/sys` went to v0.48.0, which requires
`go 1.26.0`, silently taking the directive `1.24 => 1.26.0`. For a library that directive is the
consumer's minimum Go version, so that is a breaking change for every consumer *and* it breaks the
`test (go 1.24)` CI job — arriving disguised as a routine dependency refresh. Pin the offending
indirect dependency to what a direct dependency actually requires (go-redis v9.22.0 asks only for
`x/sys v0.30.0`) rather than accepting the bump.

The corollary: `x/sys` is deliberately held at a version carrying GO-2026-5024, a Windows-only
advisory this module never calls (`govulncheck` exits 0). Every x/sys release that fixes it requires
`go >= 1.25.0`. Take the fix only in a change that intends to move the published Go floor, and bump
both together.

## Architecture

Everything is in `redis.go` (the `Cache` interface, `Options`, `RedisCache`, `New`, `NewFromURL`,
`Get`/`Set`/`Del`/`Close`) and `redis_test.go`.

Non-obvious constraints, each of which is pinned by a test — do not "simplify" them away:

- **`ContextTimeoutEnabled` is forced on in `newCache`.** go-redis defaults it to `false`, and
  `baseClient.context()` then replaces the caller's context with `context.Background()` before the
  socket read/write, silently discarding every deadline. Without this line the `ctx` parameter on
  every method is decorative. `TestContextTimeoutIsEnabled` asserts it, because miniredis answers
  instantly and so cannot observe the difference behaviourally.
- **`Set` rejects negative TTLs.** go-redis emits `EX`/`PX` only for `ttl > 0` and `KEEPTTL` only for
  exactly `-1`, so any other negative duration stores the key with *no expiry at all*. A caller
  computing `time.Until(deadline)` on a past deadline would otherwise persist data forever.
- **`ErrCacheMiss` is returned unwrapped.** A miss is the expected hot path; wrapping it would
  allocate on every miss. Real failures *are* wrapped, and tests assert a transport error is never
  mistaken for a miss.
- **`ErrInvalidArgument` wraps the two pre-flight rejections** (missing `Addr`, negative non-`KeepTTL`
  ttl) so a retry wrapper can tell a caller bug from an outage. Tests assert a transport error is
  *not* an `ErrInvalidArgument`. Keep new validation errors wrapping it.
- **A ttl in `(0, 1ms)` is rounded up to 1ms by go-redis**, which also logs the rounding through its
  process-global logger — the one path here that can write to stderr. Pinned by
  `TestSetSubMillisecondTTLRoundsUpToOneMillisecond`; the alternative (truncating to 0) would mean no
  expiry at all.
- **A key holding `""` is a hit**, returning `("", nil)`. Distinguishing this from a miss is the
  entire reason `ErrCacheMiss` exists.
- **`Del` short-circuits on zero keys.** A bare `DEL` with no arguments is a Redis protocol error.
- **`Options.Instrument` takes the `*redis.Client`, not a `redis.Hook`.** This looks like needless
  indirection until you check the target: `redisotel.InstrumentTracing`/`InstrumentMetrics` are
  `func(redis.UniversalClient, ...) error` and register their hooks themselves, so a `[]redis.Hook`
  field cannot express the main instrumentation path at all. A plain hook is still one line
  (`rdb.AddHook(h); return nil`).
- **Instrumentation is applied before the `PING`**, so the connection check is traced and a failed
  connect is visible rather than happening behind the hooks. `TestInstrumentAppliesBeforePing`
  asserts the `ping` entry is first. Note go-redis's RESP3 `HELLO` also flows through hooks on dial,
  so assert on filtered commands, not on an exact command sequence.
- **A nil `Instrumenter` is skipped, not called.** It is the zero value of `Options.Instrument`, so
  every construction without instrumentation goes through that branch.
- **`New` closes the pool if its `PING` fails**, and likewise if `Instrument` returns an error, so
  neither failure path leaks the pool.
- **`Close` is on `*RedisCache`, not on `Cache`.** The interface is the consumption contract;
  lifecycle belongs to whoever constructed the client.
- **No `log.Printf`.** Errors are wrapped with `%w` and returned; the caller decides whether to log.

## Testing approach

Tests run the real `RedisCache` against `miniredis` (in-process Redis — no server, no Docker) and use
`t.Context()`. Coverage is 100% of statements and the suite is race-clean; keep it that way.

miniredis limits worth knowing: time is virtual (TTLs advance only via `s.FastForward`, `time.Sleep`
does nothing), and there is no network pathology, so `ReadTimeout`/`WriteTimeout`/`PoolTimeout`,
pool exhaustion and retry backoff are not exercised by this suite.

## Versioning

Published at `v0.x` with no `/v2` module suffix, so `go get -u` carries consumers across breaking
changes. Any change to the exported API needs a README migration-table entry.
