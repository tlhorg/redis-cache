package cache

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// markerHook appends "<name>:<command>" to a shared log as each command enters
// it, so tests can assert both that Options.Hooks is wired into the client and
// in which order the hooks see a call.
type markerHook struct {
	name string
	mu   *sync.Mutex
	log  *[]string
}

func (h markerHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h markerHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		*h.log = append(*h.log, h.name+":"+cmd.Name())
		h.mu.Unlock()

		return next(ctx, cmd)
	}
}

func (h markerHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// hooksFor returns the names of the hooks that saw cmd, in the order they saw
// it. Filtering by command keeps these tests off go-redis's connection
// handshake, which also runs through the hooks (RESP3 sends HELLO on dial).
func hooksFor(log []string, cmd string) []string {
	var names []string

	for _, entry := range log {
		if name, ok := strings.CutSuffix(entry, ":"+cmd); ok {
			names = append(names, name)
		}
	}

	return names
}

// newTestCache starts an in-process Redis and returns a cache connected to it.
func newTestCache(t *testing.T) (*RedisCache, *miniredis.Miniredis) {
	t.Helper()

	s := miniredis.RunT(t)

	c, err := New(t.Context(), Options{Addr: s.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c, s
}

// Without ContextTimeoutEnabled, go-redis swaps the caller's context for
// context.Background() before the socket read/write, so per-call deadlines are
// silently dropped. miniredis answers instantly and so cannot observe this;
// assert on the constructed options instead.
func TestContextTimeoutIsEnabled(t *testing.T) {
	c, _ := newTestCache(t)

	require.True(t, c.client.Options().ContextTimeoutEnabled)
}

func TestSetRejectsNegativeTTL(t *testing.T) {
	c, s := newTestCache(t)

	err := c.Set(t.Context(), "key", "value", -5*time.Second)
	require.ErrorIs(t, err, ErrInvalidArgument)
	require.False(t, s.Exists("key"), "rejected Set must not write the key")
}

// Redis expiry is millisecond-granular, so go-redis rounds a sub-millisecond
// ttl up rather than dropping it. Pinned because the alternative — truncation
// to zero — would mean "no expiry at all".
func TestSetSubMillisecondTTLRoundsUpToOneMillisecond(t *testing.T) {
	c, s := newTestCache(t)

	require.NoError(t, c.Set(t.Context(), "tiny", "value", 500*time.Microsecond))
	require.Equal(t, time.Millisecond, s.TTL("tiny"))
}

func TestSetKeepTTLRetainsExpiry(t *testing.T) {
	c, s := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "key", "first", time.Minute))
	require.NoError(t, c.Set(ctx, "key", "second", KeepTTL))

	got, err := c.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "second", got)
	require.Equal(t, time.Minute, s.TTL("key"))
}

func TestGetReturnsStoredValue(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "user:123", "John Doe", time.Minute))

	got, err := c.Get(ctx, "user:123")
	require.NoError(t, err)
	require.Equal(t, "John Doe", got)
}

func TestGetMissingKeyReturnsErrCacheMiss(t *testing.T) {
	c, _ := newTestCache(t)

	got, err := c.Get(t.Context(), "absent")
	require.ErrorIs(t, err, ErrCacheMiss)
	require.Empty(t, got)
}

// A key holding an empty string is a hit, not a miss. This is the ambiguity
// ErrCacheMiss exists to remove, so it is pinned here.
func TestGetEmptyStringIsHitNotMiss(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "empty", "", 0))

	got, err := c.Get(ctx, "empty")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSetWithoutTTLDoesNotExpire(t *testing.T) {
	c, s := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "forever", "value", 0))
	s.FastForward(24 * time.Hour)

	got, err := c.Get(ctx, "forever")
	require.NoError(t, err)
	require.Equal(t, "value", got)
}

func TestSetTTLExpires(t *testing.T) {
	c, s := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "short", "value", time.Minute))

	s.FastForward(time.Minute + time.Second)

	_, err := c.Get(ctx, "short")
	require.ErrorIs(t, err, ErrCacheMiss)
}

func TestSetOverwritesExistingValue(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "key", "first", 0))
	require.NoError(t, c.Set(ctx, "key", "second", 0))

	got, err := c.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "second", got)
}

func TestSetNonStringValue(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "count", 42, 0))

	got, err := c.Get(ctx, "count")
	require.NoError(t, err)
	require.Equal(t, "42", got)
}

func TestDelRemovesKey(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "key", "value", 0))
	require.NoError(t, c.Del(ctx, "key"))

	_, err := c.Get(ctx, "key")
	require.ErrorIs(t, err, ErrCacheMiss)
}

func TestDelAbsentKeyIsNotAnError(t *testing.T) {
	c, _ := newTestCache(t)

	require.NoError(t, c.Del(t.Context(), "absent"))
}

func TestDelMultipleKeys(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "a", "1", 0))
	require.NoError(t, c.Set(ctx, "b", "2", 0))

	require.NoError(t, c.Del(ctx, "a", "b", "absent"))

	_, err := c.Get(ctx, "a")
	require.ErrorIs(t, err, ErrCacheMiss)
	_, err = c.Get(ctx, "b")
	require.ErrorIs(t, err, ErrCacheMiss)
}

func TestDelNoKeysIsNoOp(t *testing.T) {
	c, _ := newTestCache(t)

	require.NoError(t, c.Del(t.Context()))
}

// A transport failure must not be reported as a cache miss, or callers would
// silently treat an outage as "not cached" and stampede the origin.
func TestServerFailureIsNotReportedAsMiss(t *testing.T) {
	c, s := newTestCache(t)
	ctx := t.Context()

	require.NoError(t, c.Set(ctx, "key", "value", 0))
	s.Close()

	_, err := c.Get(ctx, "key")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCacheMiss)

	require.Error(t, c.Set(ctx, "key", "value", 0))
	require.Error(t, c.Del(ctx, "key"))

	// A transport failure is not a caller mistake, so a retry wrapper keying on
	// ErrInvalidArgument must not swallow it.
	require.NotErrorIs(t, err, ErrInvalidArgument)
}

func TestNewRequiresAddr(t *testing.T) {
	_, err := New(t.Context(), Options{})
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestNewFailsAgainstDeadAddress(t *testing.T) {
	s := miniredis.RunT(t)
	addr := s.Addr()
	s.Close()

	_, err := New(t.Context(), Options{Addr: addr, DialTimeout: time.Second})
	require.Error(t, err)
}

func TestNewHonoursCancelledContext(t *testing.T) {
	s := miniredis.RunT(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := New(ctx, Options{Addr: s.Addr()})
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetHonoursCancelledContext(t *testing.T) {
	c, _ := newTestCache(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := c.Get(ctx, "key")
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrCacheMiss)
}

func TestNewFromURL(t *testing.T) {
	s := miniredis.RunT(t)

	c, err := NewFromURL(t.Context(), "redis://"+s.Addr()+"/0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set(t.Context(), "key", "value", 0))

	got, err := c.Get(t.Context(), "key")
	require.NoError(t, err)
	require.Equal(t, "value", got)
}

func TestNewFromURLRejectsMalformedURL(t *testing.T) {
	_, err := NewFromURL(t.Context(), "://not a url")
	require.Error(t, err)
}

func TestNewSelectsDB(t *testing.T) {
	s := miniredis.RunT(t)

	c, err := New(t.Context(), Options{Addr: s.Addr(), DB: 3})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set(t.Context(), "key", "value", 0))

	s.Select(3)
	require.True(t, s.Exists("key"))

	s.Select(0)
	require.False(t, s.Exists("key"))
}

// Instrumentation is the reason Options.Instrument exists, so assert the hook it
// registers both reaches the client and sees the constructor's own PING — a
// connect that fails should be visible to tracing, not invisible because
// instrumentation lands afterwards.
func TestInstrumentAppliesBeforePing(t *testing.T) {
	s := miniredis.RunT(t)

	var mu sync.Mutex
	var log []string

	c, err := New(t.Context(), Options{
		Addr: s.Addr(),
		Instrument: func(rdb *redis.Client) error {
			rdb.AddHook(markerHook{name: "h", mu: &mu, log: &log})
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set(t.Context(), "key", "value", 0))

	mu.Lock()
	got := slices.Clone(log)
	mu.Unlock()

	require.Equal(t, []string{"h"}, hooksFor(got, "ping"))
	require.Equal(t, []string{"h"}, hooksFor(got, "set"))
	require.Equal(t, "h:ping", got[0], "instrumentation must precede the connection check")
}

func TestInstrumentErrorFailsConstruction(t *testing.T) {
	s := miniredis.RunT(t)

	sentinel := errors.New("instrumentation exploded")

	_, err := New(t.Context(), Options{
		Addr:       s.Addr(),
		Instrument: func(*redis.Client) error { return sentinel },
	})
	require.ErrorIs(t, err, sentinel)
}

func TestNewFromURLAppliesInstrument(t *testing.T) {
	s := miniredis.RunT(t)

	var mu sync.Mutex
	var log []string

	c, err := NewFromURL(t.Context(), "redis://"+s.Addr()+"/0",
		func(rdb *redis.Client) error {
			rdb.AddHook(markerHook{name: "h", mu: &mu, log: &log})
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	mu.Lock()
	got := slices.Clone(log)
	mu.Unlock()

	require.Equal(t, []string{"h"}, hooksFor(got, "ping"))
}

// A nil Instrumenter is the zero value of Options.Instrument, so it must be
// skipped rather than called.
func TestNilInstrumenterIsSkipped(t *testing.T) {
	s := miniredis.RunT(t)

	c, err := NewFromURL(t.Context(), "redis://"+s.Addr()+"/0", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set(t.Context(), "key", "value", 0))
}

func TestCloseIsSafeToCallTwice(t *testing.T) {
	s := miniredis.RunT(t)

	c, err := New(t.Context(), Options{Addr: s.Addr()})
	require.NoError(t, err)

	require.NoError(t, c.Close())
	require.Error(t, c.Close())
}
