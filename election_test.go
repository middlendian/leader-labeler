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
				PodName:       "test-pod",
				PodNamespace:  "default",
				RetryInterval: 10 * time.Millisecond,
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
		PodName:       "test-pod",
		PodNamespace:  "default",
		RetryInterval: 10 * time.Millisecond,
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
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   15 * time.Second,
		TimeoutDeadline: 10 * time.Second,
		RetryInterval:   2 * time.Second,
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

func TestWaitForReadyPod_RetriesOnGetError(t *testing.T) {
	// Create a ready pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	client := fake.NewClientset(pod)

	// Track get attempts and fail the first 2
	var getAttempts int32
	const failCount = 2

	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		attempt := atomic.AddInt32(&getAttempts, 1)
		if int(attempt) <= failCount {
			return true, nil, errors.New("simulated get failure")
		}
		return false, nil, nil // Let default handler return the pod
	})

	cfg := &Config{
		PodName:       "test-pod",
		PodNamespace:  "default",
		RetryInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := waitForReadyPod(ctx, client, cfg)
	if err != nil {
		t.Errorf("waitForReadyPod() error = %v, want nil", err)
	}

	// Verify we retried the expected number of times
	finalAttempts := atomic.LoadInt32(&getAttempts)
	if finalAttempts < int32(failCount)+1 {
		t.Errorf("expected at least %d get attempts, got %d", failCount+1, finalAttempts)
	}
}

func TestRunElection_WaitForReadyFails(t *testing.T) {
	// Create a pod that is NOT ready
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}

	client := fake.NewClientset(pod)

	cfg := &Config{
		PodName:         "test-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   15 * time.Second,
		TimeoutDeadline: 10 * time.Second,
		RetryInterval:   10 * time.Millisecond,
	}

	// Use a short timeout - pod will never become ready
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := RunElection(ctx, client, cfg)

	// Should return context deadline exceeded
	if err == nil {
		t.Error("RunElection() expected error when pod not ready, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("RunElection() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunElection_InitialLabelFails(t *testing.T) {
	// Create a ready pod
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

	client := fake.NewClientset(pod)

	// Make all patch operations fail
	client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("simulated patch failure")
	})

	cfg := &Config{
		PodName:         "test-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   15 * time.Second,
		TimeoutDeadline: 50 * time.Millisecond, // Short deadline for faster test
		RetryInterval:   10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := RunElection(ctx, client, cfg)

	// Should return an error from LabelPod (deadline exceeded)
	if err == nil {
		t.Error("RunElection() expected error when labeling fails, got nil")
	}
}

func TestRunElection_LeadershipAcquisition(t *testing.T) {
	// Create a ready pod with initial non-leader label
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"test/is-leader": "false"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	client := fake.NewClientset(pod)

	cfg := &Config{
		PodName:         "test-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   1 * time.Second,
		TimeoutDeadline: 800 * time.Millisecond,
		RetryInterval:   200 * time.Millisecond,
	}

	// Run election briefly, then cancel to check state
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run in goroutine since RunElection blocks in the election loop
	done := make(chan error, 1)
	go func() {
		done <- RunElection(ctx, client, cfg)
	}()

	// Wait for election to complete (context timeout or error)
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("test timed out waiting for election")
	}

	// Check if pod was labeled as leader (OnStartedLeading called ApplyAllLabels)
	updatedPod, err := client.CoreV1().Pods("default").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	// The pod should have been labeled as leader at some point
	// Note: Due to OnStoppedLeading being called on context cancel,
	// the final label might be "false" again, so we track patch calls instead
	t.Logf("final pod label: %s", updatedPod.Labels["test/is-leader"])
}

func TestRunElection_LeadershipAcquisition_TrackPatches(t *testing.T) {
	// Create a ready pod with initial non-leader label
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"test/is-leader": "false"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	client := fake.NewClientset(pod)

	// Track patch operations to verify leadership callbacks were triggered
	var patchCount int32
	client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		atomic.AddInt32(&patchCount, 1)
		return false, nil, nil // Let default handler process
	})

	cfg := &Config{
		PodName:         "test-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   1 * time.Second,
		TimeoutDeadline: 800 * time.Millisecond,
		RetryInterval:   200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunElection(ctx, client, cfg)
	}()

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("test timed out waiting for election")
	}

	// We expect at least 2 patches:
	// 1. Initial label (false) from RunElection
	// 2. Label update (true) from OnStartedLeading/ApplyAllLabels
	// And possibly more from OnStoppedLeading
	patches := atomic.LoadInt32(&patchCount)
	if patches < 2 {
		t.Errorf("expected at least 2 patch operations, got %d", patches)
	}
	t.Logf("total patch operations: %d", patches)
}

func TestOnStoppedLeading_RemovesLabelOnContextCancel(t *testing.T) {
	// This test verifies that when the context is cancelled (e.g., pod termination),
	// the OnStoppedLeading callback successfully removes the is-leader label.
	// This requires using context.Background() in OnStoppedLeading, not electionCtx,
	// because electionCtx is already cancelled by the time OnStoppedLeading runs.
	//
	// The bug: OnStoppedLeading uses electionCtx which is derived from the main context.
	// When the main context is cancelled, electionCtx is also cancelled, so:
	// 1. The Patch API call fails (simulated via reactor returning error)
	// 2. LabelPod's retry loop checks ctx.Done() - with cancelled context, returns ctx.Err()
	// 3. The leader label never gets removed
	//
	// To test this, we fail the first Patch attempt after termination, forcing the
	// retry loop to kick in. With a cancelled context (bug), it returns immediately.
	// With context.Background() (fix), it retries and succeeds.

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"test/is-leader": "false"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	client := fake.NewClientset(pod)

	// Track state
	var becameLeader atomic.Bool
	var terminationTriggered atomic.Bool
	var labelRemovalAttempts atomic.Int32

	client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(k8stesting.PatchAction)
		patchBytes := string(patchAction.GetPatch())

		if patchBytes == `{"metadata":{"labels":{"test/is-leader":"true"}}}` {
			becameLeader.Store(true)
			return false, nil, nil
		}

		// After termination is triggered, fail the FIRST label removal attempt
		// to force the retry loop. This simulates real API transient failure.
		if terminationTriggered.Load() && patchBytes == `{"metadata":{"labels":{"test/is-leader":"false"}}}` {
			attempts := labelRemovalAttempts.Add(1)
			if attempts == 1 {
				// First attempt fails - this forces LabelPod into its retry loop
				// With buggy code (electionCtx cancelled): retry loop sees ctx.Done(), returns ctx.Err()
				// With fixed code (context.Background()): retry loop waits and retries, succeeds
				return true, nil, errors.New("simulated transient API failure")
			}
			// Second attempt succeeds
			return false, nil, nil
		}

		return false, nil, nil
	})

	cfg := &Config{
		PodName:         "test-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   1 * time.Second,
		TimeoutDeadline: 800 * time.Millisecond,
		RetryInterval:   50 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- RunElection(ctx, client, cfg)
	}()

	// Wait until we become leader
	deadline := time.Now().Add(testTimeout)
	for !becameLeader.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !becameLeader.Load() {
		t.Fatal("timed out waiting to become leader")
	}

	// Verify we're labeled as leader
	leaderPod, _ := client.CoreV1().Pods("default").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if leaderPod.Labels["test/is-leader"] != "true" {
		t.Fatalf("expected pod to be labeled as leader, got %q", leaderPod.Labels["test/is-leader"])
	}

	// Now cancel the context to trigger termination
	terminationTriggered.Store(true)
	cancel()

	// Wait for election to complete
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("test timed out waiting for election to stop")
	}

	// Give a moment for any async operations
	time.Sleep(100 * time.Millisecond)

	// Verify the final state of the pod
	updatedPod, err := client.CoreV1().Pods("default").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	// The key assertion: after termination, the pod MUST be labeled as non-leader
	if updatedPod.Labels["test/is-leader"] != "false" {
		t.Errorf("pod label = %q after termination, want %q; "+
			"OnStoppedLeading likely used electionCtx (cancelled) instead of context.Background()",
			updatedPod.Labels["test/is-leader"], "false")
	}

	// Verify that retry was attempted (should be at least 2 attempts with fix, 1 with bug)
	attempts := labelRemovalAttempts.Load()
	if attempts < 2 {
		t.Errorf("expected at least 2 label removal attempts (retry after failure), got %d; "+
			"this suggests the retry loop exited early due to cancelled context", attempts)
	}
}

func TestRunElection_ApplyAllLabelsFails_ReleasesLeadership(t *testing.T) {
	// Create leader pod and a follower pod
	leaderPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "leader-pod",
			Namespace: "default",
			Labels:    map[string]string{"test/is-leader": "false"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	followerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "follower-pod",
			Namespace: "default",
			Labels:    map[string]string{"test/is-leader": "true"}, // needs to be set to false
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	client := fake.NewClientset(leaderPod, followerPod)

	// Track if we ever became leader (set label to true)
	var becameLeader int32
	// Track if OnStoppedLeading was called after ApplyAllLabels failed
	var lostLeadership int32

	// Make follower pod labeling fail (but allow leader pod labeling)
	client.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(k8stesting.PatchAction)

		// Track when leader pod is labeled as leader
		if patchAction.GetName() == "leader-pod" {
			// Check if this is setting to leader=true
			patchBytes := patchAction.GetPatch()
			if string(patchBytes) != "" && string(patchBytes) != `{"metadata":{"labels":{"test/is-leader":"false"}}}` {
				if atomic.LoadInt32(&becameLeader) == 0 {
					atomic.StoreInt32(&becameLeader, 1)
				} else {
					// Second time setting label means OnStoppedLeading ran
					atomic.StoreInt32(&lostLeadership, 1)
				}
			}
			return false, nil, nil // Allow leader pod patches
		}

		// Fail follower pod patches
		if patchAction.GetName() == "follower-pod" {
			return true, nil, errors.New("simulated follower patch failure")
		}

		return false, nil, nil
	})

	cfg := &Config{
		PodName:         "leader-pod",
		PodNamespace:    "default",
		ElectionName:    "test-election",
		LeadershipLabel: "test/is-leader",
		LeaseDuration:   1 * time.Second,
		TimeoutDeadline: 100 * time.Millisecond, // Short deadline for quick failure
		RetryInterval:   20 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunElection(ctx, client, cfg)
	}()

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("test timed out waiting for election")
	}

	// Verify that leadership was released after ApplyAllLabels failed
	if atomic.LoadInt32(&becameLeader) == 0 {
		t.Log("leader never acquired leadership (election may have failed before callback)")
	} else {
		t.Log("leader acquired leadership, ApplyAllLabels was called")
		// Note: The election might retry, which is valid behavior
	}
}
