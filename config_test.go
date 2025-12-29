// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"testing"
	"time"
)

// withMockedDetection runs a test with mocked hostname and namespace detection.
// It restores the original functions after the test.
func withMockedDetection(t *testing.T, hostname string, hostnameErr error, namespace string, namespaceErr error, testFn func()) {
	t.Helper()

	// Save originals
	origHostname := hostnameFunc
	origReadNS := readNamespaceFile

	// Set mocks
	hostnameFunc = func() (string, error) {
		return hostname, hostnameErr
	}
	readNamespaceFile = func() ([]byte, error) {
		if namespaceErr != nil {
			return nil, namespaceErr
		}
		return []byte(namespace), nil
	}

	// Restore after test
	defer func() {
		hostnameFunc = origHostname
		readNamespaceFile = origReadNS
	}()

	testFn()
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
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
			name:        "empty pod-name override",
			args:        []string{"--election-name=my-election", "--pod-name=", "--pod-namespace=default"},
			wantErr:     true,
			errContains: "--pod-name is required",
		},
		{
			name:        "empty pod-namespace override",
			args:        []string{"--election-name=my-election", "--pod-name=test-pod", "--pod-namespace="},
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
				"--timeout-deadline=20s",
				"--retry-interval=5s",
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
			name:        "invalid flag",
			args:        []string{"--invalid-flag=value"},
			wantErr:     true,
			errContains: "flag provided but not defined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func TestDetectPodName(t *testing.T) {
	t.Run("returns hostname", func(t *testing.T) {
		withMockedDetection(t, "my-pod-abc123", nil, "", nil, func() {
			name := detectPodName()
			if name != "my-pod-abc123" {
				t.Errorf("detectPodName() = %q, want %q", name, "my-pod-abc123")
			}
		})
	})

	t.Run("returns empty on error", func(t *testing.T) {
		withMockedDetection(t, "", errors.New("hostname unavailable"), "", nil, func() {
			name := detectPodName()
			if name != "" {
				t.Errorf("detectPodName() = %q, want empty string", name)
			}
		})
	})
}

func TestDetectNamespace(t *testing.T) {
	t.Run("returns namespace from file", func(t *testing.T) {
		withMockedDetection(t, "", nil, "kube-system", nil, func() {
			ns := detectNamespace()
			if ns != "kube-system" {
				t.Errorf("detectNamespace() = %q, want %q", ns, "kube-system")
			}
		})
	})

	t.Run("trims whitespace", func(t *testing.T) {
		withMockedDetection(t, "", nil, "  production\n", nil, func() {
			ns := detectNamespace()
			if ns != "production" {
				t.Errorf("detectNamespace() = %q, want %q", ns, "production")
			}
		})
	})

	t.Run("returns empty on error", func(t *testing.T) {
		withMockedDetection(t, "", nil, "", os.ErrNotExist, func() {
			ns := detectNamespace()
			if ns != "" {
				t.Errorf("detectNamespace() = %q, want empty string", ns)
			}
		})
	})
}

func TestAutoDetection(t *testing.T) {
	t.Run("uses hostname as default pod-name", func(t *testing.T) {
		withMockedDetection(t, "auto-detected-pod", nil, "auto-detected-ns", nil, func() {
			cfg, err := LoadConfig([]string{"--election-name=test"})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.PodName != "auto-detected-pod" {
				t.Errorf("PodName = %q, want %q (auto-detected from hostname)", cfg.PodName, "auto-detected-pod")
			}
		})
	})

	t.Run("uses namespace file as default pod-namespace", func(t *testing.T) {
		withMockedDetection(t, "some-pod", nil, "auto-detected-ns", nil, func() {
			cfg, err := LoadConfig([]string{"--election-name=test"})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.PodNamespace != "auto-detected-ns" {
				t.Errorf("PodNamespace = %q, want %q (auto-detected from SA token)", cfg.PodNamespace, "auto-detected-ns")
			}
		})
	})

	t.Run("flag overrides auto-detected pod-name", func(t *testing.T) {
		withMockedDetection(t, "auto-detected-pod", nil, "auto-ns", nil, func() {
			cfg, err := LoadConfig([]string{"--election-name=test", "--pod-name=explicit-pod"})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.PodName != "explicit-pod" {
				t.Errorf("PodName = %q, want %q (flag should override auto-detection)", cfg.PodName, "explicit-pod")
			}
		})
	})

	t.Run("flag overrides auto-detected pod-namespace", func(t *testing.T) {
		withMockedDetection(t, "some-pod", nil, "auto-detected-ns", nil, func() {
			cfg, err := LoadConfig([]string{"--election-name=test", "--pod-namespace=explicit-ns"})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.PodNamespace != "explicit-ns" {
				t.Errorf("PodNamespace = %q, want %q (flag should override auto-detection)", cfg.PodNamespace, "explicit-ns")
			}
		})
	})

	t.Run("fails when hostname detection fails and no flag provided", func(t *testing.T) {
		withMockedDetection(t, "", errors.New("no hostname"), "some-ns", nil, func() {
			_, err := LoadConfig([]string{"--election-name=test"})
			if err == nil {
				t.Error("LoadConfig() expected error when hostname detection fails")
			}
			if !containsString(err.Error(), "--pod-name is required") {
				t.Errorf("error = %v, want error containing %q", err, "--pod-name is required")
			}
		})
	})

	t.Run("fails when namespace detection fails and no flag provided", func(t *testing.T) {
		withMockedDetection(t, "some-pod", nil, "", os.ErrNotExist, func() {
			_, err := LoadConfig([]string{"--election-name=test"})
			if err == nil {
				t.Error("LoadConfig() expected error when namespace detection fails")
			}
			if !containsString(err.Error(), "--pod-namespace is required") {
				t.Errorf("error = %v, want error containing %q", err, "--pod-namespace is required")
			}
		})
	})

	t.Run("succeeds with both auto-detection", func(t *testing.T) {
		withMockedDetection(t, "my-pod-xyz", nil, "my-namespace", nil, func() {
			cfg, err := LoadConfig([]string{"--election-name=test-election"})
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.ElectionName != "test-election" {
				t.Errorf("ElectionName = %q, want %q", cfg.ElectionName, "test-election")
			}
			if cfg.PodName != "my-pod-xyz" {
				t.Errorf("PodName = %q, want %q", cfg.PodName, "my-pod-xyz")
			}
			if cfg.PodNamespace != "my-namespace" {
				t.Errorf("PodNamespace = %q, want %q", cfg.PodNamespace, "my-namespace")
			}
		})
	})
}
