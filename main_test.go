package main

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestGetTerminationGracePeriod(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected time.Duration
	}{
		{
			name: "with termination grace period set",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: int64Ptr(60),
				},
			},
			expected: 60 * time.Second,
		},
		{
			name: "with nil termination grace period (default)",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: nil,
				},
			},
			expected: 30 * time.Second,
		},
		{
			name: "with zero termination grace period",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: int64Ptr(0),
				},
			},
			expected: 0 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getTerminationGracePeriod(tt.pod)
			if result != tt.expected {
				t.Errorf("getTerminationGracePeriod() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

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
				"--namespace=default",
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
				// Check default labels are generated
				if c.LeadershipLabel != "my-app/is-leader" {
					t.Errorf("LeadershipLabel = %v, expected my-app/is-leader", c.LeadershipLabel)
				}
				if c.ParticipationLabel != "my-app/participant" {
					t.Errorf("ParticipationLabel = %v, expected my-app/participant", c.ParticipationLabel)
				}
				// Check default durations
				if c.LeaseDuration != 15*time.Second {
					t.Errorf("LeaseDuration = %v, expected 15s", c.LeaseDuration)
				}
			},
		},
		{
			name: "custom labels",
			args: []string{
				"--election-name=my-app",
				"--pod-name=my-pod",
				"--namespace=default",
				"--leadership-label=custom/leader",
				"--participation-label=custom/member",
			},
			expectError: false,
			validate: func(t *testing.T, c *Config) {
				if c.LeadershipLabel != "custom/leader" {
					t.Errorf("LeadershipLabel = %v, expected custom/leader", c.LeadershipLabel)
				}
				if c.ParticipationLabel != "custom/member" {
					t.Errorf("ParticipationLabel = %v, expected custom/member", c.ParticipationLabel)
				}
			},
		},
		{
			name: "custom timeouts",
			args: []string{
				"--election-name=my-app",
				"--pod-name=my-pod",
				"--namespace=default",
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
			args:        []string{"--pod-name=my-pod", "--namespace=default"},
			expectError: true,
			errorMsg:    "--election-name is required",
		},
		{
			name:        "missing pod-name",
			args:        []string{"--election-name=my-app", "--namespace=default"},
			expectError: true,
			errorMsg:    "--pod-name is required (or set POD_NAME env var)",
		},
		{
			name:        "missing namespace",
			args:        []string{"--election-name=my-app", "--pod-name=my-pod"},
			expectError: true,
			errorMsg:    "--namespace is required (or set POD_NAMESPACE env var)",
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

func TestApplyDefaultLabels(t *testing.T) {
	tests := []struct {
		name                       string
		electionName               string
		inputLeadershipLabel       string
		inputParticipationLabel    string
		expectedLeadershipLabel    string
		expectedParticipationLabel string
	}{
		{
			name:                       "generates default labels when empty",
			electionName:               "my-app",
			inputLeadershipLabel:       "",
			inputParticipationLabel:    "",
			expectedLeadershipLabel:    "my-app/is-leader",
			expectedParticipationLabel: "my-app/participant",
		},
		{
			name:                       "preserves custom leadership label",
			electionName:               "my-app",
			inputLeadershipLabel:       "custom/leader",
			inputParticipationLabel:    "",
			expectedLeadershipLabel:    "custom/leader",
			expectedParticipationLabel: "my-app/participant",
		},
		{
			name:                       "preserves custom participation label",
			electionName:               "my-app",
			inputLeadershipLabel:       "",
			inputParticipationLabel:    "custom/member",
			expectedLeadershipLabel:    "my-app/is-leader",
			expectedParticipationLabel: "custom/member",
		},
		{
			name:                       "preserves both custom labels",
			electionName:               "my-app",
			inputLeadershipLabel:       "custom/leader",
			inputParticipationLabel:    "custom/member",
			expectedLeadershipLabel:    "custom/leader",
			expectedParticipationLabel: "custom/member",
		},
		{
			name:                       "handles election name with dashes",
			electionName:               "prod-service",
			inputLeadershipLabel:       "",
			inputParticipationLabel:    "",
			expectedLeadershipLabel:    "prod-service/is-leader",
			expectedParticipationLabel: "prod-service/participant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leadershipLabel, participationLabel := determineLabelNames(
				tt.electionName,
				tt.inputLeadershipLabel,
				tt.inputParticipationLabel,
			)

			if leadershipLabel != tt.expectedLeadershipLabel {
				t.Errorf("leadershipLabel = %v, expected %v", leadershipLabel, tt.expectedLeadershipLabel)
			}
			if participationLabel != tt.expectedParticipationLabel {
				t.Errorf("participationLabel = %v, expected %v", participationLabel, tt.expectedParticipationLabel)
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

// Helper function to create *int64
func int64Ptr(i int64) *int64 {
	return &i
}
