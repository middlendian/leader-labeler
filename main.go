// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Config holds all configuration for the leader-labeler
type Config struct {
	ElectionName    string
	PodName         string
	Namespace       string
	LeadershipLabel string
	LeaseDuration   time.Duration
	RenewDeadline   time.Duration
	RetryPeriod     time.Duration
}

var (
	isLeader atomic.Bool
	client   kubernetes.Interface
	cfg      Config
)

func main() {
	// Parse command-line flags
	parseFlags()

	// Setup structured JSON logging
	setupLogger()

	slog.Info("starting leader-labeler",
		"election_name", cfg.ElectionName,
		"namespace", cfg.Namespace,
		"pod_name", cfg.PodName)

	// Build Kubernetes client
	var err error
	client, err = buildKubernetesClient()
	if err != nil {
		slog.Error("failed to build kubernetes client", "error", err)
		os.Exit(1)
	}

	// Setup context for application lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get this pod object
	pod, err := client.CoreV1().Pods(cfg.Namespace).Get(ctx, cfg.PodName, metav1.GetOptions{})
	if err != nil {
		slog.Error("failed to get pod", "error", err, "pod_name", cfg.PodName)
		os.Exit(1)
	}

	// Wait for this pod to become Ready
	if err := waitForReadyPod(ctx, pod.Name); err != nil {
		slog.Error("failed waiting for pod readiness", "error", err)
		os.Exit(1)
	}

	slog.Info("pod ready, joining election")

	// Apply initial leader label (false) to this pod
	if err := labelPod(ctx, pod.Name, false); err != nil {
		slog.Error("failed to apply leader label", "error", err, "label", cfg.LeadershipLabel)
		os.Exit(1)
	}

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigChan
		handleShutdown(cancel)
	}()

	// Run leader election (blocks until shutdown)
	runLeaderElection(ctx)

	slog.Info("leader-labeler terminated")
}

// parseConfig parses command-line arguments from a string slice and returns a Config.
func parseConfig(args []string) (*Config, error) {
	fs := flag.NewFlagSet("leader-labeler", flag.ContinueOnError)

	c := &Config{}

	// Define flags
	fs.StringVar(&c.ElectionName, "election-name", "", "Name of the election (required)")
	fs.StringVar(&c.PodName, "pod-name", os.Getenv("POD_NAME"), "Name of this pod")
	fs.StringVar(&c.Namespace, "pod-namespace", os.Getenv("POD_NAMESPACE"), "Namespace of this pod")
	fs.StringVar(&c.LeadershipLabel, "leadership-label", "", "Label for leader status (default: <election-name>/is-leader)")
	fs.DurationVar(&c.LeaseDuration, "lease-duration", 15*time.Second, "Lease duration")
	fs.DurationVar(&c.RenewDeadline, "renew-deadline", 10*time.Second, "Renew deadline")
	fs.DurationVar(&c.RetryPeriod, "retry-period", 2*time.Second, "Retry period")

	// Parse arguments
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Validate required flags
	if c.ElectionName == "" {
		return nil, fmt.Errorf("--election-name is required")
	}
	if c.PodName == "" {
		return nil, fmt.Errorf("--pod-name is required (or set POD_NAME env var)")
	}
	if c.Namespace == "" {
		return nil, fmt.Errorf("--pod-namespace is required (or set POD_NAMESPACE env var)")
	}

	// Set default leadership label if not provided
	if c.LeadershipLabel == "" {
		c.LeadershipLabel = c.ElectionName + "/is-leader"
	}

	return c, nil
}

func parseFlags() {
	c, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cfg = *c
}

func setupLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))
}

func buildKubernetesClient() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	return clientset, nil
}

func waitForReadyPod(ctx context.Context, podName string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	slog.Info("waiting for pod to become ready", "pod_name", podName)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := client.CoreV1().Pods(cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				slog.Warn("failed to get pod while waiting for readiness", "error", err)
				continue
			}

			if isPodReady(pod) {
				slog.Info("pod is ready", "pod_name", podName)
				return nil
			}
		}
	}
}

func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func runLeaderElection(ctx context.Context) {
	for ctx.Err() == nil {
		// Create a cancellable context for this election cycle
		electionCtx, electionCancel := context.WithCancel(ctx)

		lock := &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{
				Name:      cfg.ElectionName,
				Namespace: cfg.Namespace,
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
			RenewDeadline:   cfg.RenewDeadline,
			RetryPeriod:     cfg.RetryPeriod,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(ctx context.Context) {
					isLeader.Store(true)
					slog.Info("became leader", "pod_name", cfg.PodName)

					// Reconcile all participants (sets is-leader=true on self, false on others)
					if err := reconcileLabels(ctx); err != nil {
						slog.Error("failed to reconcile labels, releasing leadership", "error", err)
						electionCancel()
						return
					}
				},
				OnStoppedLeading: func() {
					isLeader.Store(false)
					slog.Warn("lost leadership", "pod_name", cfg.PodName)
					// Immediately mark self as non-leader
					if err := labelPod(electionCtx, cfg.PodName, false); err != nil {
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
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// reconcileLabels sets is-leader=true on self, is-leader=false on all other participants.
// Called once when this pod becomes leader. Returns an error if labeling self fails.
func reconcileLabels(ctx context.Context) error {
	// List all pods with the leadership label (any value)
	labelSelector := cfg.LeadershipLabel
	pods, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list participant pods: %w", err)
	}

	// First, label self as leader (critical - must succeed)
	if err := labelPod(ctx, cfg.PodName, true); err != nil {
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
		wg.Go(func() {
			if err := labelPod(ctx, pod.Name, false); err != nil {
				slog.Error("failed to label pod during reconciliation",
					"error", err,
					"pod_name", pod.Name)
			}
		})
	}
	wg.Wait()

	slog.Debug("reconciliation complete", "participant_count", len(pods.Items))
	return nil
}

func labelPod(ctx context.Context, podName string, isLeaderPod bool) error {
	leaderValue := "false"
	if isLeaderPod {
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
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := client.CoreV1().Pods(cfg.Namespace).Patch(ctx, podName,
			types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			slog.Info("labeled pod",
				"pod_name", podName,
				cfg.LeadershipLabel, leaderValue)
			return nil
		}

		if attempt < 3 {
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

func handleShutdown(cancel context.CancelFunc) {
	slog.Info("shutdown signal received, releasing leadership and terminating")
	cancel()
}
