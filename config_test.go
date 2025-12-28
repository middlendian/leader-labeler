// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		envVars     map[string]string
		wantErr     bool
		errContains string
		validate    func(t *testing.T, cfg *Config)
	}{
		{
			name:        "missing election-name",
			args:        []string{"--pod-name=test-pod", "--pod-namespace=default"},
			wantErr:     true,
			errContains: "--election-name is required",
		},
		{
			name:        "missing pod-name",
			args:        []string{"--election-name=my-election", "--pod-namespace=default"},
			wantErr:     true,
			errContains: "--pod-name is required",
		},
		{
			name:        "missing pod-namespace",
			args:        []string{"--election-name=my-election", "--pod-name=test-pod"},
			wantErr:     true,
			errContains: "--pod-namespace is required",
		},
		{
			name: "all required flags provided",
			args: []string{"--election-name=my-election", "--pod-name=test-pod", "--pod-namespace=default"},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.ElectionName != "my-election" {
					t.Errorf("ElectionName = %q, want %q", cfg.ElectionName, "my-election")
				}
				if cfg.PodName != "test-pod" {
					t.Errorf("PodName = %q, want %q", cfg.PodName, "test-pod")
				}
				if cfg.PodNamespace != "default" {
					t.Errorf("PodNamespace = %q, want %q", cfg.PodNamespace, "default")
				}
			},
		},
		{
			name: "default leadership label",
			args: []string{"--election-name=my-election", "--pod-name=test-pod", "--pod-namespace=default"},
			validate: func(t *testing.T, cfg *Config) {
				want := "my-election/is-leader"
				if cfg.LeadershipLabel != want {
					t.Errorf("LeadershipLabel = %q, want %q", cfg.LeadershipLabel, want)
				}
			},
		},
		{
			name: "custom leadership label",
			args: []string{"--election-name=my-election", "--pod-name=test-pod", "--pod-namespace=default", "--leadership-label=custom/leader"},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.LeadershipLabel != "custom/leader" {
					t.Errorf("LeadershipLabel = %q, want %q", cfg.LeadershipLabel, "custom/leader")
				}
			},
		},
		{
			name: "default durations",
			args: []string{"--election-name=my-election", "--pod-name=test-pod", "--pod-namespace=default"},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.LeaseDuration != 15*time.Second {
					t.Errorf("LeaseDuration = %v, want %v", cfg.LeaseDuration, 15*time.Second)
				}
				if cfg.TimeoutDeadline != 10*time.Second {
					t.Errorf("TimeoutDeadline = %v, want %v", cfg.TimeoutDeadline, 10*time.Second)
				}
				if cfg.RetryInterval != 2*time.Second {
					t.Errorf("RetryInterval = %v, want %v", cfg.RetryInterval, 2*time.Second)
				}
			},
		},
		{
			name: "custom durations",
			args: []string{
				"--election-name=my-election",
				"--pod-name=test-pod",
				"--pod-namespace=default",
				"--lease-duration=30s",
				"--renew-deadline=20s",
				"--retry-period=5s",
			},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.LeaseDuration != 30*time.Second {
					t.Errorf("LeaseDuration = %v, want %v", cfg.LeaseDuration, 30*time.Second)
				}
				if cfg.TimeoutDeadline != 20*time.Second {
					t.Errorf("TimeoutDeadline = %v, want %v", cfg.TimeoutDeadline, 20*time.Second)
				}
				if cfg.RetryInterval != 5*time.Second {
					t.Errorf("RetryInterval = %v, want %v", cfg.RetryInterval, 5*time.Second)
				}
			},
		},
		{
			name:    "env var POD_NAME",
			args:    []string{"--election-name=my-election", "--pod-namespace=default"},
			envVars: map[string]string{"POD_NAME": "env-pod"},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.PodName != "env-pod" {
					t.Errorf("PodName = %q, want %q", cfg.PodName, "env-pod")
				}
			},
		},
		{
			name:    "env var POD_NAMESPACE",
			args:    []string{"--election-name=my-election", "--pod-name=test-pod"},
			envVars: map[string]string{"POD_NAMESPACE": "env-namespace"},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.PodNamespace != "env-namespace" {
					t.Errorf("PodNamespace = %q, want %q", cfg.PodNamespace, "env-namespace")
				}
			},
		},
		{
			name: "flag overrides env var",
			args: []string{"--election-name=my-election", "--pod-name=flag-pod", "--pod-namespace=flag-ns"},
			envVars: map[string]string{
				"POD_NAME":      "env-pod",
				"POD_NAMESPACE": "env-ns",
			},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.PodName != "flag-pod" {
					t.Errorf("PodName = %q, want %q (flag should override env)", cfg.PodName, "flag-pod")
				}
				if cfg.PodNamespace != "flag-ns" {
					t.Errorf("PodNamespace = %q, want %q (flag should override env)", cfg.PodNamespace, "flag-ns")
				}
			},
		},
		{
			name:        "invalid flag",
			args:        []string{"--invalid-flag=value"},
			wantErr:     true,
			errContains: "flag provided but not defined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore env vars
			origPodName := os.Getenv("POD_NAME")
			origPodNamespace := os.Getenv("POD_NAMESPACE")
			defer func() {
				_ = os.Setenv("POD_NAME", origPodName)
				_ = os.Setenv("POD_NAMESPACE", origPodNamespace)
			}()

			// Clear env vars first
			_ = os.Unsetenv("POD_NAME")
			_ = os.Unsetenv("POD_NAMESPACE")

			// Set test env vars
			for k, v := range tt.envVars {
				_ = os.Setenv(k, v)
			}

			cfg, err := LoadConfig(tt.args)

			if tt.wantErr {
				if err == nil {
					t.Errorf("LoadConfig() expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("LoadConfig() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("LoadConfig() unexpected error: %v", err)
				return
			}

			if tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSetupLogger(t *testing.T) {
	// SetupLogger should not panic
	SetupLogger()
}
