// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod is ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod is not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod has no conditions",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{},
				},
			},
			expected: false,
		},
		{
			name: "pod has other conditions but not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodInitialized,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPodReady(tt.pod)
			if result != tt.expected {
				t.Errorf("isPodReady() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
		validate    func(*testing.T, *Config)
	}{
		{
			name: "valid minimal config",
			args: []string{
				"--election-name=my-app",
				"--pod-name=my-pod",
				"--pod-namespace=default",
			},
			expectError: false,
			validate: func(t *testing.T, c *Config) {
				if c.ElectionName != "my-app" {
					t.Errorf("ElectionName = %v, expected my-app", c.ElectionName)
				}
				if c.PodName != "my-pod" {
					t.Errorf("PodName = %v, expected my-pod", c.PodName)
				}
				if c.Namespace != "default" {
					t.Errorf("Namespace = %v, expected default", c.Namespace)
				}
				// Check default label is generated
				if c.LeadershipLabel != "my-app/is-leader" {
					t.Errorf("LeadershipLabel = %v, expected my-app/is-leader", c.LeadershipLabel)
				}
				// Check default durations
				if c.LeaseDuration != 15*time.Second {
					t.Errorf("LeaseDuration = %v, expected 15s", c.LeaseDuration)
				}
			},
		},
		{
			name: "custom leadership label",
			args: []string{
				"--election-name=my-app",
				"--pod-name=my-pod",
				"--pod-namespace=default",
				"--leadership-label=custom/leader",
			},
			expectError: false,
			validate: func(t *testing.T, c *Config) {
				if c.LeadershipLabel != "custom/leader" {
					t.Errorf("LeadershipLabel = %v, expected custom/leader", c.LeadershipLabel)
				}
			},
		},
		{
			name: "custom timeouts",
			args: []string{
				"--election-name=my-app",
				"--pod-name=my-pod",
				"--pod-namespace=default",
				"--lease-duration=30s",
				"--renew-deadline=20s",
				"--retry-period=5s",
			},
			expectError: false,
			validate: func(t *testing.T, c *Config) {
				if c.LeaseDuration != 30*time.Second {
					t.Errorf("LeaseDuration = %v, expected 30s", c.LeaseDuration)
				}
				if c.RenewDeadline != 20*time.Second {
					t.Errorf("RenewDeadline = %v, expected 20s", c.RenewDeadline)
				}
				if c.RetryPeriod != 5*time.Second {
					t.Errorf("RetryPeriod = %v, expected 5s", c.RetryPeriod)
				}
			},
		},
		{
			name:        "missing election-name",
			args:        []string{"--pod-name=my-pod", "--pod-namespace=default"},
			expectError: true,
			errorMsg:    "--election-name is required",
		},
		{
			name:        "missing pod-name",
			args:        []string{"--election-name=my-app", "--pod-namespace=default"},
			expectError: true,
			errorMsg:    "--pod-name is required (or set POD_NAME env var)",
		},
		{
			name:        "missing namespace",
			args:        []string{"--election-name=my-app", "--pod-name=my-pod"},
			expectError: true,
			errorMsg:    "--pod-namespace is required (or set POD_NAMESPACE env var)",
		},
		{
			name:        "invalid flag",
			args:        []string{"--election-name=my-app", "--invalid-flag=value"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseConfig(tt.args)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				} else if tt.errorMsg != "" && err.Error() != tt.errorMsg {
					t.Errorf("error = %v, expected %v", err.Error(), tt.errorMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if config == nil {
				t.Errorf("expected config but got nil")
				return
			}

			if tt.validate != nil {
				tt.validate(t, config)
			}
		})
	}
}

func TestConfigurationDefaults(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		min      time.Duration
		max      time.Duration
	}{
		{
			name:     "lease duration reasonable",
			duration: 15 * time.Second,
			min:      5 * time.Second,
			max:      60 * time.Second,
		},
		{
			name:     "renew deadline reasonable",
			duration: 10 * time.Second,
			min:      2 * time.Second,
			max:      30 * time.Second,
		},
		{
			name:     "retry period reasonable",
			duration: 2 * time.Second,
			min:      1 * time.Second,
			max:      10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.duration < tt.min || tt.duration > tt.max {
				t.Errorf("duration %v is outside reasonable range [%v, %v]", tt.duration, tt.min, tt.max)
			}
		})
	}
}

func TestFormatVerbList(t *testing.T) {
	tests := []struct {
		name     string
		verbs    []string
		expected string
	}{
		{
			name:     "single verb",
			verbs:    []string{"get"},
			expected: `"get"`,
		},
		{
			name:     "two verbs",
			verbs:    []string{"get", "list"},
			expected: `"get", "list"`,
		},
		{
			name:     "multiple verbs",
			verbs:    []string{"get", "list", "patch"},
			expected: `"get", "list", "patch"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatVerbList(tt.verbs)
			if result != tt.expected {
				t.Errorf("formatVerbList(%v) = %q, expected %q", tt.verbs, result, tt.expected)
			}
		})
	}
}

func TestFormatPermissionError(t *testing.T) {
	// Save and restore cfg
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg.Namespace = "test-ns"

	tests := []struct {
		name     string
		missing  []PermissionCheck
		contains []string
	}{
		{
			name: "missing pod permissions",
			missing: []PermissionCheck{
				{Resource: "pods", APIGroup: "", Verb: "list"},
				{Resource: "pods", APIGroup: "", Verb: "patch"},
			},
			contains: []string{
				"missing required RBAC permissions",
				"pods (core API): list, patch",
				"Namespace: test-ns",
				`apiGroups: [""]`,
				`resources: ["pods"]`,
				`"list"`,
				`"patch"`,
			},
		},
		{
			name: "missing lease permissions",
			missing: []PermissionCheck{
				{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "create"},
				{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "update"},
			},
			contains: []string{
				"leases (coordination.k8s.io): create, update",
				`apiGroups: ["coordination.k8s.io"]`,
				`resources: ["leases"]`,
			},
		},
		{
			name: "missing both pod and lease permissions",
			missing: []PermissionCheck{
				{Resource: "pods", APIGroup: "", Verb: "get"},
				{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "create"},
			},
			contains: []string{
				"pods (core API): get",
				"leases (coordination.k8s.io): create",
				"To fix this, ensure the ServiceAccount has a Role and RoleBinding",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := formatPermissionError(tt.missing)
			errStr := err.Error()
			for _, substr := range tt.contains {
				if !strings.Contains(errStr, substr) {
					t.Errorf("error message missing expected substring %q:\n%s", substr, errStr)
				}
			}
		})
	}
}
