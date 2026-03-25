package main

import (
	"log"
	"os"
	"strings"
)

type Config struct {
	JWTSecret    string // required
	DBPath       string // optional, default: app_limits.db
	ResendAPIKey string // required
	ResendFrom   string // optional, default: onboarding@resend.dev
}

var cfg Config

func loadConfig() {
	var missing []string

	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	if cfg.JWTSecret == "" {
		missing = append(missing, "JWT_SECRET")
	}

	cfg.ResendAPIKey = os.Getenv("RESEND_API_KEY")
	if cfg.ResendAPIKey == "" {
		missing = append(missing, "RESEND_API_KEY")
	}

	cfg.DBPath = os.Getenv("DB_PATH")
	if cfg.DBPath == "" {
		cfg.DBPath = "app_limits.db"
	}

	cfg.ResendFrom = os.Getenv("RESEND_FROM")
	if cfg.ResendFrom == "" {
		cfg.ResendFrom = "onboarding@resend.dev"
	}

	if len(missing) > 0 {
		log.Fatalf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
}
