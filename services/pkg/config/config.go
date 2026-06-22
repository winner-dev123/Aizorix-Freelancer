// Package config loads service configuration from environment variables with sane
// defaults. In production, secrets are injected from AWS Secrets Manager via the pod's
// environment (External Secrets Operator); this loader treats them as ordinary env vars.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Base struct {
	Environment string // local|staging|production
	LogLevel    string
	HTTPPort    int
	GRPCPort    int
	DatabaseURL string
	RedisAddr   string
	KafkaBrokers string
	OTELEndpoint string
}

func LoadBase() Base {
	return Base{
		Environment:  Get("ENVIRONMENT", "local"),
		LogLevel:     Get("LOG_LEVEL", "info"),
		HTTPPort:     GetInt("HTTP_PORT", 8080),
		GRPCPort:     GetInt("GRPC_PORT", 9090),
		DatabaseURL:  Get("DATABASE_URL", ""),
		RedisAddr:    Get("REDIS_ADDR", "localhost:6379"),
		KafkaBrokers: Get("KAFKA_BROKERS", "localhost:19092"),
		OTELEndpoint: Get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}
}

func Get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func GetInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func GetDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func MustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("config: required env %q is not set", key))
	}
	return v
}
