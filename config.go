// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Default configuration values
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

// Config holds all configuration for the leader-labeler
type Config struct {
	ElectionName    string
	PodName         string
	Namespace       string
	LeadershipLabel string
	LeaseDuration   time.Duration
	RenewDeadline   time.Duration
	RetryPeriod     time.Duration
}

// SetupLogger configures structured JSON logging to stdout
func SetupLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))
}

// LoadConfig parses command-line arguments and returns a validated Config.
// It logs errors before returning them.
func LoadConfig(args []string) (*Config, error) {
	fs := flag.NewFlagSet("leader-labeler", flag.ContinueOnError)

	cfg := &Config{}

	// Define flags
	fs.StringVar(&cfg.ElectionName, "election-name", "", "Name of the election (required)")
	fs.StringVar(&cfg.PodName, "pod-name", os.Getenv("POD_NAME"), "Name of this pod")
	fs.StringVar(&cfg.Namespace, "pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace of this pod")
	fs.StringVar(&cfg.LeadershipLabel, "leadership-label", "", "Label for leader status (default: <election-name>/is-leader)")
	fs.DurationVar(&cfg.LeaseDuration, "lease-duration", DefaultLeaseDuration, "Lease duration")
	fs.DurationVar(&cfg.RenewDeadline, "renew-deadline", DefaultRenewDeadline, "Renew deadline")
	fs.DurationVar(&cfg.RetryPeriod, "retry-period", DefaultRetryPeriod, "Retry period")

	// Parse arguments
	if err := fs.Parse(args); err != nil {
		slog.Error("failed to parse flags", "error", err)
		return nil, err
	}

	// Validate required flags
	if cfg.ElectionName == "" {
		err := fmt.Errorf("--election-name is required")
		slog.Error("configuration error", "error", err)
		return nil, err
	}
	if cfg.PodName == "" {
		err := fmt.Errorf("--pod-name is required (or set POD_NAME env var)")
		slog.Error("configuration error", "error", err)
		return nil, err
	}
	if cfg.Namespace == "" {
		err := fmt.Errorf("--pod-namespace is required (or set POD_NAMESPACE env var)")
		slog.Error("configuration error", "error", err)
		return nil, err
	}

	// Set default leadership label if not provided
	if cfg.LeadershipLabel == "" {
		cfg.LeadershipLabel = cfg.ElectionName + "/is-leader"
	}

	return cfg, nil
}
