package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Port              string
	DatabaseURL       string
	IPHashSalt        string
	TrustProxy        bool
	RateLimitRequests int
	RateLimitWindow   time.Duration
	RateLimitBurst    int
	SubmitMinInterval time.Duration
	SubmitDailyLimit  int
}

func loadConfig(path string) (config, error) {
	if err := loadEnvFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config{}, err
	}

	cfg := config{
		Port:              envOr("PORT", "8080"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		IPHashSalt:        strings.TrimSpace(os.Getenv("IP_HASH_SALT")),
		TrustProxy:        envBool("TRUST_PROXY", false),
		RateLimitRequests: envInt("RATE_LIMIT_REQUESTS", 120),
		RateLimitWindow:   envDuration("RATE_LIMIT_WINDOW", time.Minute),
		RateLimitBurst:    envInt("RATE_LIMIT_BURST", 30),
		SubmitMinInterval: envDuration("SUBMIT_MIN_INTERVAL", 10*time.Minute),
		SubmitDailyLimit:  envInt("SUBMIT_DAILY_LIMIT", 5),
	}

	if cfg.DatabaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required in .env")
	}
	if len(cfg.IPHashSalt) < 32 {
		return config{}, errors.New("IP_HASH_SALT must contain at least 32 characters")
	}
	if cfg.RateLimitRequests <= 0 || cfg.RateLimitBurst <= 0 || cfg.RateLimitWindow <= 0 {
		return config{}, errors.New("rate limit values must be positive")
	}
	if cfg.SubmitMinInterval <= 0 || cfg.SubmitDailyLimit <= 0 {
		return config{}, errors.New("submission limit values must be positive")
	}
	return cfg, nil
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid .env line: %q", line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if key == "" {
			return errors.New("empty key in .env")
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}
