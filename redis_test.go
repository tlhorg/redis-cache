package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

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
	require.Error(t, err)
	require.False(t, s.Exists("key"), "rejected Set must not write the key")
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
}

func TestNewRequiresAddr(t *testing.T) {
	_, err := New(t.Context(), Options{})
	require.Error(t, err)
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

func TestCloseIsSafeToCallTwice(t *testing.T) {
	s := miniredis.RunT(t)

	c, err := New(t.Context(), Options{Addr: s.Addr()})
	require.NoError(t, err)

	require.NoError(t, c.Close())
	require.Error(t, c.Close())
}
