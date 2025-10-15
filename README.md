# Redis Cache

A simple and efficient Redis cache client for Go applications, providing a clean interface for caching operations.

## Features

- Simple and intuitive API
- Connection management with ping validation
- Support for key expiration
- Error handling with proper logging
- Thread-safe operations

## Installation

```bash
go get github.com/tlhorg/redis-cache
```

## Prerequisites

- Go 1.18 or higher
- Redis server running

## Quick Start

```go
package main

import (
    "log"
    "time"

    "github.com/tlhorg/redis-cache"
)

func main() {
    // Initialize Redis cache
    redisCache, err := cache.New("localhost", "6379", "", "0")
    if err != nil {
        log.Fatal("Failed to connect to Redis:", err)
    }

    // Set a value with expiration
    err = redisCache.Set("user:123", "John Doe", 5*time.Minute)
    if err != nil {
        log.Printf("Error setting cache: %v", err)
    }

    // Get a value
    value, err := redisCache.Get("user:123")
    if err != nil {
        log.Printf("Error getting cache: %v", err)
    }
    
    if value != "" {
        log.Printf("Cached value: %s", value)
    }

    // Delete a key
    err = redisCache.Del("user:123")
    if err != nil {
        log.Printf("Error deleting cache: %v", err)
    }
}
```

## API Reference

### Interface

The package implements the following `Cache` interface:

```go
type Cache interface {
    Get(key string) (string, error)
    Set(key string, value interface{}, expiration time.Duration) error
    Del(key string) error
}
```

### Constructor

#### `New(host, port, password, defaultDb string) (*Redis, error)`

Creates a new Redis cache client.

**Parameters:**
- `host`: Redis server hostname (e.g., "localhost")
- `port`: Redis server port (e.g., "6379")
- `password`: Redis password (empty string if no password)
- `defaultDb`: Database number as string (e.g., "0")

**Returns:**
- `*Redis`: Redis cache instance
- `error`: Connection error if any

### Methods

#### `Get(key string) (string, error)`

Retrieves a value from the cache.

**Parameters:**
- `key`: Cache key

**Returns:**
- `string`: Cached value (empty string if key doesn't exist)
- `error`: Error if operation fails

#### `Set(key string, value interface{}, expiration time.Duration) error`

Stores a value in the cache with expiration.

**Parameters:**
- `key`: Cache key
- `value`: Value to store (any type)
- `expiration`: Expiration duration (use `0` for no expiration)

**Returns:**
- `error`: Error if operation fails

#### `Del(key string) error`

Deletes a key from the cache.

**Parameters:**
- `key`: Cache key to delete

**Returns:**
- `error`: Error if operation fails

## Configuration Examples

### Basic Configuration
```go
cache, err := cache.New("localhost", "6379", "", "0")
```

### With Authentication
```go
cache, err := cache.New("redis.example.com", "6379", "your-password", "1")
```

### Using Different Database
```go
cache, err := cache.New("localhost", "6379", "", "2")
```

## Error Handling

The package handles various error scenarios:

- **Connection errors**: Returned during initialization if Redis server is unreachable
- **Invalid database number**: Returned if `defaultDb` is not a valid integer
- **Operation errors**: Logged and returned for get/set/delete operations
- **Key not found**: `Get()` returns empty string with no error (Redis `Nil` error is handled)

## Dependencies

- [go-redis/redis/v9](https://github.com/redis/go-redis): Redis client for Go

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License.

## Support

If you encounter any issues or have questions, please open an issue on GitHub.

