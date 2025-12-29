// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Default configuration values
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second

	// NamespaceFile is the path to the service account namespace file
	NamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Config holds all configuration for the leader-labeler
type Config struct {
	ElectionName    string
	PodName         string
	PodNamespace    string
	LeadershipLabel string
	LeaseDuration   time.Duration
	TimeoutDeadline time.Duration
	RetryInterval   time.Duration
}

// Detection function variables - can be overridden in tests
var (
	hostnameFunc      = os.Hostname
	readNamespaceFile = func() ([]byte, error) {
		return os.ReadFile(NamespaceFile)
	}
)

// SetupLogger configures structured JSON logging to stdout
func SetupLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))
}

// detectNamespace reads the namespace from the service account token mount.
// Returns empty string if the file doesn't exist or can't be read.
func detectNamespace() string {
	data, err := readNamespaceFile()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// detectPodName uses the hostname, which Kubernetes sets to the pod name by default.
// Returns empty string if hostname can't be determined.
func detectPodName() string {
	hostname, err := hostnameFunc()
	if err != nil {
		return ""
	}
	return hostname
}

// LoadConfig parses command-line arguments and returns a validated Config.
// It logs errors before returning them.
func LoadConfig(args []string) (*Config, error) {
	fs := flag.NewFlagSet("leader-labeler", flag.ContinueOnError)

	cfg := &Config{}

	// Define flags
	fs.StringVar(&cfg.ElectionName, "election-name", "", "Name of the election (required)")
	fs.StringVar(&cfg.PodName, "pod-name", detectPodName(), "Name of this pod (auto-detected from hostname)")
	fs.StringVar(&cfg.PodNamespace, "pod-namespace", detectNamespace(), "Namespace of this pod (auto-detected from service account)")
	fs.StringVar(&cfg.LeadershipLabel, "leadership-label", "", "Label for leader status (default: <election-name>/is-leader)")
	fs.DurationVar(&cfg.LeaseDuration, "lease-duration", DefaultLeaseDuration, "Lease duration")
	fs.DurationVar(&cfg.TimeoutDeadline, "timeout-deadline", DefaultRenewDeadline, "Timeout deadline")
	fs.DurationVar(&cfg.RetryInterval, "retry-interval", DefaultRetryPeriod, "Retry interval")

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
		err := fmt.Errorf("--pod-name is required (auto-detection failed - not running in a standard pod?)")
		slog.Error("configuration error", "error", err)
		return nil, err
	}
	if cfg.PodNamespace == "" {
		err := fmt.Errorf("--pod-namespace is required (auto-detection failed - service account token not mounted?)")
		slog.Error("configuration error", "error", err)
		return nil, err
	}

	// Set default leadership label if not provided
	if cfg.LeadershipLabel == "" {
		cfg.LeadershipLabel = cfg.ElectionName + "/is-leader"
	}

	return cfg, nil
}
