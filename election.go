// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

const (
	ElectionRetryBackoff = 100 * time.Millisecond
)

// RunElection waits for pod readiness, initializes the leadership label, and runs the leader election loop.
// It logs its own errors before returning.
func RunElection(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	klog.InfoS("starting leader-labeler",
		"election_name", cfg.ElectionName,
		"namespace", cfg.PodNamespace,
		"pod_name", cfg.PodName)

	// Wait for this pod to become Ready
	if err := waitForReadyPod(ctx, client, cfg); err != nil {
		klog.ErrorS(err, "failed waiting for pod readiness")
		return err
	}

	klog.InfoS("pod ready, joining election")

	// Apply initial leader label (false) to this pod
	if err := LabelPod(ctx, client, cfg, cfg.PodName, false); err != nil {
		klog.ErrorS(err, "failed to apply initial leader label", "label", cfg.LeadershipLabel)
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
					klog.InfoS("became leader", "pod_name", cfg.PodName)

					// Apply labels to all participants (sets is-leader=true on self, false on others)
					if err := ApplyAllLabels(ctx, client, cfg); err != nil {
						klog.ErrorS(err, "failed to reconcile labels, releasing leadership")
						electionCancel()
						return
					}
				},
				OnStoppedLeading: func() {
					klog.Warningf("lost leadership pod_name=%s", cfg.PodName)
					// Immediately mark self as non-leader.
					// Use context.Background() because electionCtx may already be cancelled
					// when this callback runs during termination, and we still need to
					// remove the leader label before the pod shuts down.
					if err := LabelPod(context.Background(), client, cfg, cfg.PodName, false); err != nil {
						klog.ErrorS(err, "failed to remove leader label", "label", cfg.LeadershipLabel)
					}
				},
				OnNewLeader: func(identity string) {
					if identity != cfg.PodName {
						klog.InfoS("new leader elected", "leader_identity", identity)
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
	klog.InfoS("waiting for pod to become ready", "pod_name", cfg.PodName)

	// Check immediately first
	pod, err := client.CoreV1().Pods(cfg.PodNamespace).Get(ctx, cfg.PodName, metav1.GetOptions{})
	if err == nil && isPodReady(pod) {
		klog.InfoS("pod is ready", "pod_name", cfg.PodName)
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
				klog.Warningf("failed to get pod while waiting for readiness error=%v", err)
				continue
			}

			if isPodReady(pod) {
				klog.InfoS("pod is ready", "pod_name", cfg.PodName)
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
