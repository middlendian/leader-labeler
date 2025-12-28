// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
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
			types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			slog.Info("labeled pod",
				"pod_name", podName,
				cfg.LeadershipLabel, leaderValue)
			return nil
		}

		lastErr = err

		if time.Now().After(deadline) {
			return fmt.Errorf("deadline exceeded: %w", lastErr)
		}

		slog.Warn("label update failed, retrying",
			"error", err,
			"pod_name", podName,
			"retry_period", cfg.RetryInterval)

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
			slog.Debug("skipping pod, label already correct",
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

	slog.Debug("reconciliation complete", "participant_count", len(pods.Items))
	return nil
}
