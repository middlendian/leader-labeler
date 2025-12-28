// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// testTimeout is the default timeout for test contexts
const testTimeout = 5 * time.Second

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name       string
		conditions []corev1.PodCondition
		want       bool
	}{
		{
			name:       "no conditions",
			conditions: nil,
			want:       false,
		},
		{
			name: "ready condition true",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			want: true,
		},
		{
			name: "ready condition false",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
			want: false,
		},
		{
			name: "ready condition unknown",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionUnknown},
			},
			want: false,
		},
		{
			name: "multiple conditions, ready is true",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			},
			want: true,
		},
		{
			name: "multiple conditions, ready is false",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse},
			},
			want: false,
		},
		{
			name: "no ready condition but other conditions exist",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: tt.conditions,
				},
			}

			got := isPodReady(pod)
			if got != tt.want {
				t.Errorf("isPodReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWaitForReadyPod(t *testing.T) {
	tests := []struct {
		name          string
		podReady      bool
		contextCancel bool
		wantErr       bool
	}{
		{
			name:     "pod already ready",
			podReady: true,
			wantErr:  false,
		},
		{
			name:          "context cancelled before ready",
			podReady:      false,
			contextCancel: true,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readyStatus := corev1.ConditionFalse
			if tt.podReady {
				readyStatus = corev1.ConditionTrue
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: readyStatus},
					},
				},
			}

			client := fake.NewClientset(pod)

			cfg := &Config{
				PodName:     "test-pod",
				Namespace:   "default",
				RetryPeriod: 10 * time.Millisecond,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			if tt.contextCancel {
				cancel() // cancel immediately
			}

			err := waitForReadyPod(ctx, client, cfg)

			if tt.wantErr {
				if err == nil {
					t.Error("waitForReadyPod() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("waitForReadyPod() unexpected error: %v", err)
			}
		})
	}
}

func TestWaitForReadyPod_BecomesReadyAfterPolling(t *testing.T) {
	// Create a pod that starts not ready
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}

	client := fake.NewClientset(pod)

	cfg := &Config{
		PodName:     "test-pod",
		Namespace:   "default",
		RetryPeriod: 10 * time.Millisecond,
	}

	// Make the pod ready after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		pod.Status.Conditions[0].Status = corev1.ConditionTrue
		_, err := client.CoreV1().Pods("default").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{})
		if err != nil {
			t.Logf("failed to update pod status: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := waitForReadyPod(ctx, client, cfg)
	if err != nil {
		t.Errorf("waitForReadyPod() error = %v, want nil", err)
	}
}

func TestRunElection_InitialSetup(t *testing.T) {
	// Test that RunElection performs initial setup correctly
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	objects := []runtime.Object{pod}
	client := fake.NewClientset(objects...)

	cfg := &Config{
		PodName:         "test-pod",
		Namespace:       "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
	}

	// Cancel context immediately to exit after initial setup
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Run election (will return quickly due to cancelled context)
	_ = RunElection(ctx, client, cfg)

	// Verify the pod was labeled as non-leader initially
	updatedPod, err := client.CoreV1().Pods("default").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	if updatedPod.Labels[cfg.LeadershipLabel] != "false" {
		t.Errorf("pod label = %q, want %q", updatedPod.Labels[cfg.LeadershipLabel], "false")
	}
}
