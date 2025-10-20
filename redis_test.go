package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRedisCache_Get(t *testing.T) {
	cache := new(mockRedisClient)

	cache.On("Get", mock.AnythingOfType("string")).Return("value", nil)
	_, err := cache.Get("key")
	require.NoError(t, err)
	cache.AssertExpectations(t)
}

func TestRedisCache_Set(t *testing.T) {
	cache := new(mockRedisClient)
	cache.On(
		"Set",
		mock.AnythingOfType("string"),
		mock.AnythingOfType("string"),
		mock.AnythingOfType("time.Duration")).Return(nil)
	err := cache.Set("key", "value", time.Minute)
	require.NoError(t, err)
	cache.AssertExpectations(t)
}

func TestRedisCache_Delete(t *testing.T) {
	cache := new(mockRedisClient)
	cache.On("Del", mock.AnythingOfType("string")).Return(nil)
	err := cache.Del("key")
	require.NoError(t, err)
	cache.AssertExpectations(t)
}

type mockRedisClient struct {
	mock.Mock
}

func (m *mockRedisClient) Get(key string) (string, error) {
	args := m.Called(key)
	return args.String(0), args.Error(1)
}

func (m *mockRedisClient) Set(key string, value interface{}, expiration time.Duration) error {
	args := m.Called(key, value, expiration)
	return args.Error(0)
}

func (m *mockRedisClient) Del(key string) error {
	args := m.Called(key)
	return args.Error(0)
}
