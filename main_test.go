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
