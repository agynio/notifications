package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultGRPCAddr         = ":50051"
	defaultRedisAddr        = "127.0.0.1:6379"
	defaultRedisDB          = 0
	defaultRedisChannel     = "notifications"
	defaultStreamBufferSize = 64
	defaultLogLevel         = "info"
)

// Config captures runtime configuration derived from the environment.
type Config struct {
	GRPCAddr         string
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	RedisChannel     string
	StreamBufferSize int
	LogLevel         string
}

// Load reads configuration from environment variables, applying defaults when
// values are not provided. Returns an error when supplied values are invalid.
func Load() (Config, error) {
	var cfg Config

	cfg.GRPCAddr = readEnv("GRPC_ADDR", defaultGRPCAddr)
	cfg.RedisAddr = readEnv("REDIS_ADDR", defaultRedisAddr)
	cfg.RedisPassword = readEnv("REDIS_PASSWORD", "")

	redisDBStr := readEnv("REDIS_DB", "")
	if redisDBStr == "" {
		cfg.RedisDB = defaultRedisDB
	} else {
		value, err := strconv.Atoi(redisDBStr)
		if err != nil {
			return Config{}, fmt.Errorf("invalid REDIS_DB: %w", err)
		}
		cfg.RedisDB = value
	}

	cfg.RedisChannel = readEnv("REDIS_CHANNEL", defaultRedisChannel)

	bufferStr := readEnv("STREAM_BUFFER_SIZE", "")
	if bufferStr == "" {
		cfg.StreamBufferSize = defaultStreamBufferSize
	} else {
		value, err := strconv.Atoi(bufferStr)
		if err != nil {
			return Config{}, fmt.Errorf("invalid STREAM_BUFFER_SIZE: %w", err)
		}
		if value <= 0 {
			return Config{}, fmt.Errorf("invalid STREAM_BUFFER_SIZE: must be > 0")
		}
		cfg.StreamBufferSize = value
	}

	cfg.LogLevel = normalizeLogLevel(readEnv("LOG_LEVEL", defaultLogLevel))

	return cfg, nil
}

func readEnv(key, def string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return def
}

func normalizeLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return "info"
	case "debug":
		return "debug"
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	default:
		return "info"
	}
}
