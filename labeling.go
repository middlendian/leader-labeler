// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// LabelPod sets the leadership label on a pod with retry logic.
// Retries every cfg.RetryInterval until success or cfg.TimeoutDeadline is exceeded.
func LabelPod(ctx context.Context, client kubernetes.Interface, cfg *Config, podName string, isLeader bool) error {
	leaderValue := "false"
	if isLeader {
		leaderValue = "true"
	}

	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				cfg.LeadershipLabel: leaderValue,
			},
		},
	}
	patchBytes, _ := json.Marshal(patchData)

	deadline := time.Now().Add(cfg.TimeoutDeadline)
	var lastErr error

	for {
		_, err := client.CoreV1().Pods(cfg.PodNamespace).Patch(ctx, podName,
			types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			// Verify the label was actually updated
			updatedPod, getErr := client.CoreV1().Pods(cfg.PodNamespace).Get(ctx, podName, metav1.GetOptions{})
			if getErr != nil {
				klog.Warningf("failed to verify label update; error=%v pod_name=%s", getErr, podName)
				// Don't fail - the patch succeeded, verification is best-effort
			} else if updatedPod.Labels[cfg.LeadershipLabel] != leaderValue {
				return fmt.Errorf("label verification failed: expected %s=%s, got %s",
					cfg.LeadershipLabel, leaderValue, updatedPod.Labels[cfg.LeadershipLabel])
			}
			klog.InfoS("labeled pod",
				"pod_name", podName,
				cfg.LeadershipLabel, leaderValue)
			return nil
		}

		lastErr = err

		if time.Now().After(deadline) {
			return fmt.Errorf("deadline exceeded: %w", lastErr)
		}

		klog.Warningf("label update failed (will retry); error=%v pod_name=%s retry_period=%v",
			err, podName, cfg.RetryInterval)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.RetryInterval):
			continue // make another attempt
		}
	}
}

// ApplyAllLabels sets is-leader=true on self, is-leader=false on all other participants.
// Called once when this pod becomes leader. Returns an error if labeling any pod fails,
// which should trigger a leadership release to ensure consistent state.
func ApplyAllLabels(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	// List all pods with the leadership label (any value)
	labelSelector := cfg.LeadershipLabel
	klog.InfoS("reconciling pod labels", "selector", labelSelector)
	pods, err := client.CoreV1().Pods(cfg.PodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list participant pods: %w", err)
	}

	// Apply labels to all pods in parallel (including self)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for i := range pods.Items {
		pod := &pods.Items[i]
		isLeader := pod.Name == cfg.PodName
		targetValue := "false"
		if isLeader {
			targetValue = "true"
		}

		// Skip if label already has the correct value
		currentValue := pod.Labels[cfg.LeadershipLabel]
		if currentValue == targetValue {
			klog.InfoS("pod label already correct",
				"pod_name", pod.Name,
				cfg.LeadershipLabel, currentValue)
			continue
		}

		// Update label in parallel
		wg.Go(func() {
			if err := LabelPod(ctx, client, cfg, pod.Name, isLeader); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("pod %s: %w", pod.Name, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("failed to apply labels: %w", errors.Join(errs...))
	}

	klog.InfoS("label reconciliation complete", "participant_count", len(pods.Items))
	return nil
}
