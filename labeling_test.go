// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestLabelPod(t *testing.T) {
	tests := []struct {
		name          string
		podName       string
		isLeader      bool
		failCount     int           // number of times to fail before succeeding
		retryPeriod   time.Duration // retry interval
		renewDeadline time.Duration // total deadline for retries
		wantErr       bool
	}{
		{
			name:          "successful label as leader",
			podName:       "test-pod",
			isLeader:      true,
			failCount:     0,
			retryPeriod:   10 * time.Millisecond,
			renewDeadline: 100 * time.Millisecond,
			wantErr:       false,
		},
		{
			name:          "successful label as non-leader",
			podName:       "test-pod",
			isLeader:      false,
			failCount:     0,
			retryPeriod:   10 * time.Millisecond,
			renewDeadline: 100 * time.Millisecond,
			wantErr:       false,
		},
		{
			name:          "retry succeeds on second attempt",
			podName:       "test-pod",
			isLeader:      true,
			failCount:     1,
			retryPeriod:   10 * time.Millisecond,
			renewDeadline: 100 * time.Millisecond,
			wantErr:       false,
		},
		{
			name:          "retry succeeds on third attempt",
			podName:       "test-pod",
			isLeader:      true,
			failCount:     2,
			retryPeriod:   10 * time.Millisecond,
			renewDeadline: 100 * time.Millisecond,
			wantErr:       false,
		},
		{
			name:          "fails after deadline exceeded",
			podName:       "test-pod",
			isLeader:      true,
			failCount:     1000,                  // always fail
			retryPeriod:   10 * time.Millisecond, // short retry period
			renewDeadline: 25 * time.Millisecond, // deadline will be exceeded after ~2 retries
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create pod for the fake client
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.podName,
					Namespace: "default",
					Labels:    map[string]string{},
				},
			}

			client := fake.NewClientset(pod)

			// Track attempt count
			var attempts int32

			// Add reactor to simulate failures
			client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				attempt := atomic.AddInt32(&attempts, 1)
				if int(attempt) <= tt.failCount {
					return true, nil, errors.New("simulated failure")
				}
				// Let the default handler process the patch
				return false, nil, nil
			})

			cfg := &Config{
				PodNamespace:    "default",
				LeadershipLabel: "test/is-leader",
				RetryInterval:   tt.retryPeriod,
				TimeoutDeadline: tt.renewDeadline,
			}

			// Use a short timeout context for tests
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()

			err := LabelPod(ctx, client, cfg, tt.podName, tt.isLeader)

			if tt.wantErr {
				if err == nil {
					t.Error("LabelPod() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("LabelPod() unexpected error: %v", err)
				return
			}

			// Verify the label was applied
			updatedPod, err := client.CoreV1().Pods("default").Get(ctx, tt.podName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed to get pod: %v", err)
			}

			expectedValue := "false"
			if tt.isLeader {
				expectedValue = "true"
			}

			if updatedPod.Labels[cfg.LeadershipLabel] != expectedValue {
				t.Errorf("label %s = %q, want %q",
					cfg.LeadershipLabel,
					updatedPod.Labels[cfg.LeadershipLabel],
					expectedValue)
			}
		})
	}
}

func TestReconcileLabels(t *testing.T) {
	tests := []struct {
		name         string
		selfPodName  string
		existingPods []*corev1.Pod
		failPodName  string // pod name to fail labeling for (empty = no failure)
		wantErr      bool
		wantLabels   map[string]string // podName -> expected label value
	}{
		{
			name:        "reconcile with no other pods",
			selfPodName: "leader-pod",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
			},
			wantErr: false,
			wantLabels: map[string]string{
				"leader-pod": "true",
			},
		},
		{
			name:        "reconcile with other pods",
			selfPodName: "leader-pod",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "follower-1",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "true"}, // was leader before
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "follower-2",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
			},
			wantErr: false,
			wantLabels: map[string]string{
				"leader-pod": "true",
				"follower-1": "false",
				"follower-2": "false", // unchanged (already false)
			},
		},
		{
			name:        "fails if self label fails",
			selfPodName: "leader-pod",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
			},
			failPodName: "leader-pod",
			wantErr:     true,
		},
		{
			name:        "fails if follower label fails",
			selfPodName: "leader-pod",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "follower-1",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "true"}, // needs update
					},
				},
			},
			failPodName: "follower-1",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert pods to runtime.Object slice
			objects := make([]runtime.Object, len(tt.existingPods))
			for i, pod := range tt.existingPods {
				objects[i] = pod
			}

			client := fake.NewClientset(objects...)

			// Add reactor to simulate label failure if needed
			if tt.failPodName != "" {
				client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					patchAction := action.(k8stesting.PatchAction)
					if patchAction.GetName() == tt.failPodName {
						return true, nil, errors.New("simulated label failure")
					}
					return false, nil, nil
				})
			}

			cfg := &Config{
				PodName:         tt.selfPodName,
				PodNamespace:    "default",
				LeadershipLabel: "test/is-leader",
				RetryInterval:   10 * time.Millisecond,
				TimeoutDeadline: 100 * time.Millisecond,
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()

			err := ApplyAllLabels(ctx, client, cfg)

			if tt.wantErr {
				if err == nil {
					t.Error("ApplyAllLabels() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ApplyAllLabels() unexpected error: %v", err)
				return
			}

			// Verify labels
			for podName, expectedValue := range tt.wantLabels {
				pod, err := client.CoreV1().Pods("default").Get(ctx, podName, metav1.GetOptions{})
				if err != nil {
					t.Errorf("failed to get pod %s: %v", podName, err)
					continue
				}

				actualValue := pod.Labels[cfg.LeadershipLabel]
				if actualValue != expectedValue {
					t.Errorf("pod %s label = %q, want %q", podName, actualValue, expectedValue)
				}
			}
		})
	}
}

func TestReconcileLabels_SkipsCorrectlyLabeledPods(t *testing.T) {
	tests := []struct {
		name         string
		existingPods []*corev1.Pod
		selfPodName  string
		wantPatched  []string
	}{
		{
			name: "skips follower already labeled false",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "already-false",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
			},
			selfPodName: "leader-pod",
			wantPatched: []string{"leader-pod"},
		},
		{
			name: "skips leader already labeled true",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "true"}, // already correct
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "needs-update",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "true"}, // was old leader
					},
				},
			},
			selfPodName: "leader-pod",
			wantPatched: []string{"needs-update"},
		},
		{
			name: "skips all when labels already correct",
			existingPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "leader-pod",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "true"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "follower",
						Namespace: "default",
						Labels:    map[string]string{"test/is-leader": "false"},
					},
				},
			},
			selfPodName: "leader-pod",
			wantPatched: []string{}, // no patches needed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]runtime.Object, len(tt.existingPods))
			for i, pod := range tt.existingPods {
				objects[i] = pod
			}

			client := fake.NewClientset(objects...)

			// Track which pods were patched
			var patchedPods []string
			client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				patchAction := action.(k8stesting.PatchAction)
				patchedPods = append(patchedPods, patchAction.GetName())
				return false, nil, nil // let default handler process
			})

			cfg := &Config{
				PodName:         tt.selfPodName,
				PodNamespace:    "default",
				LeadershipLabel: "test/is-leader",
				RetryInterval:   10 * time.Millisecond,
				TimeoutDeadline: 100 * time.Millisecond,
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()

			err := ApplyAllLabels(ctx, client, cfg)
			if err != nil {
				t.Fatalf("ApplyAllLabels() error: %v", err)
			}

			if len(patchedPods) != len(tt.wantPatched) {
				t.Errorf("expected %d pods to be patched, got %d: %v", len(tt.wantPatched), len(patchedPods), patchedPods)
				return
			}

			// Check each expected pod was patched (order may vary due to parallelism)
			for _, want := range tt.wantPatched {
				found := false
				for _, got := range patchedPods {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected pod %s to be patched, but it wasn't. Patched pods: %v", want, patchedPods)
				}
			}
		})
	}
}
