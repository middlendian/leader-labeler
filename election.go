// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	ElectionRetryBackoff = 100 * time.Millisecond
)

// RunElection waits for pod readiness, initializes the leadership label, and runs the leader election loop.
// It logs its own errors before returning.
func RunElection(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	slog.Info("starting leader-labeler",
		"election_name", cfg.ElectionName,
		"namespace", cfg.PodNamespace,
		"pod_name", cfg.PodName)

	// Wait for this pod to become Ready
	if err := waitForReadyPod(ctx, client, cfg); err != nil {
		slog.Error("failed waiting for pod readiness", "error", err)
		return err
	}

	slog.Info("pod ready, joining election")

	// Apply initial leader label (false) to this pod
	if err := LabelPod(ctx, client, cfg, cfg.PodName, false); err != nil {
		slog.Error("failed to apply initial leader label", "error", err, "label", cfg.LeadershipLabel)
		return err
	}

	// Run the leader election loop
	runLeaderElectionLoop(ctx, client, cfg)

	return nil
}

// runLeaderElectionLoop runs the leader election, retrying on failure until context is cancelled.
func runLeaderElectionLoop(ctx context.Context, client kubernetes.Interface, cfg *Config) {
	for ctx.Err() == nil {
		// Create a cancellable context for this election cycle
		electionCtx, electionCancel := context.WithCancel(ctx)

		lock := &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{
				Name:      cfg.ElectionName,
				Namespace: cfg.PodNamespace,
			},
			Client: client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: cfg.PodName,
			},
		}

		leaderelection.RunOrDie(electionCtx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			LeaseDuration:   cfg.LeaseDuration,
			RenewDeadline:   cfg.TimeoutDeadline,
			RetryPeriod:     cfg.RetryInterval,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(ctx context.Context) {
					slog.Info("became leader", "pod_name", cfg.PodName)

					// Apply labels to all participants (sets is-leader=true on self, false on others)
					if err := ApplyAllLabels(ctx, client, cfg); err != nil {
						slog.Error("failed to reconcile labels, releasing leadership", "error", err)
						electionCancel()
						return
					}
				},
				OnStoppedLeading: func() {
					slog.Warn("lost leadership", "pod_name", cfg.PodName)
					// Immediately mark self as non-leader
					if err := LabelPod(electionCtx, client, cfg, cfg.PodName, false); err != nil {
						slog.Error("failed to remove leader label", "error", err, "label", cfg.LeadershipLabel)
					}
				},
				OnNewLeader: func(identity string) {
					if identity != cfg.PodName {
						slog.Info("new leader elected", "leader_identity", identity)
					}
				},
			},
		})

		electionCancel() // Clean up election context

		// Brief pause before retrying (if not shutting down)
		if ctx.Err() == nil {
			time.Sleep(ElectionRetryBackoff)
		}
	}
}

// waitForReadyPod blocks until the pod is ready or context is cancelled.
func waitForReadyPod(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	slog.Info("waiting for pod to become ready", "pod_name", cfg.PodName)

	// Check immediately first
	pod, err := client.CoreV1().Pods(cfg.PodNamespace).Get(ctx, cfg.PodName, metav1.GetOptions{})
	if err == nil && isPodReady(pod) {
		slog.Info("pod is ready", "pod_name", cfg.PodName)
		return nil
	}

	ticker := time.NewTicker(cfg.RetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := client.CoreV1().Pods(cfg.PodNamespace).Get(ctx, cfg.PodName, metav1.GetOptions{})
			if err != nil {
				slog.Warn("failed to get pod while waiting for readiness", "error", err)
				continue
			}

			if isPodReady(pod) {
				slog.Info("pod is ready", "pod_name", cfg.PodName)
				return nil
			}
		}
	}
}

// isPodReady returns true if the pod has Ready condition True.
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
