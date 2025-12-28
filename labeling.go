// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Labeling constants
const (
	LabelRetryAttempts = 3
)

// LabelPod sets the leadership label on a pod with retry logic.
// It logs its actions and errors.
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

	// Retry with exponential backoff
	for attempt := 1; attempt <= LabelRetryAttempts; attempt++ {
		_, err := client.CoreV1().Pods(cfg.Namespace).Patch(ctx, podName,
			types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			slog.Info("labeled pod",
				"pod_name", podName,
				cfg.LeadershipLabel, leaderValue)
			return nil
		}

		if attempt < LabelRetryAttempts {
			backoff := time.Duration(attempt*attempt) * time.Second
			slog.Warn("label update failed, retrying",
				"error", err,
				"pod_name", podName,
				"attempt", attempt,
				"backoff_seconds", backoff.Seconds())
			time.Sleep(backoff)
		} else {
			return fmt.Errorf("failed after %d attempts: %w", attempt, err)
		}
	}

	return nil
}

// ReconcileLabels sets is-leader=true on self, is-leader=false on all other participants.
// Called once when this pod becomes leader. Returns an error if labeling self fails.
func ReconcileLabels(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	// List all pods with the leadership label (any value)
	labelSelector := cfg.LeadershipLabel
	pods, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list participant pods: %w", err)
	}

	// First, label self as leader (critical - must succeed)
	if err := LabelPod(ctx, client, cfg, cfg.PodName, true); err != nil {
		return fmt.Errorf("failed to label self as leader: %w", err)
	}

	// Then, label others in parallel (best effort - just log errors)
	var wg sync.WaitGroup
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Name == cfg.PodName {
			continue // Already labeled self above
		}

		// Skip if label already has the correct value
		currentValue := pod.Labels[cfg.LeadershipLabel]
		if currentValue == "false" {
			slog.Debug("skipping pod, label already correct",
				"pod_name", pod.Name,
				cfg.LeadershipLabel, currentValue)
			continue
		}

		// Update label in parallel
		wg.Add(1)
		go func(podName string) {
			defer wg.Done()
			if err := LabelPod(ctx, client, cfg, podName, false); err != nil {
				slog.Error("failed to label pod during reconciliation",
					"error", err,
					"pod_name", podName)
			}
		}(pod.Name)
	}
	wg.Wait()

	slog.Debug("reconciliation complete", "participant_count", len(pods.Items))
	return nil
}
