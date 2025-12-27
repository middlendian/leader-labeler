package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	ElectionName       string
	PodName            string
	Namespace          string
	LeadershipLabel    string
	ParticipationLabel string
	LeaseDuration      time.Duration
	RenewDeadline      time.Duration
	RetryPeriod        time.Duration
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

	// Extract termination grace period for draining timeout
	terminationGracePeriod := getTerminationGracePeriod(pod)
	slog.Info("pod ready, joining election",
		"termination_grace_period_seconds", terminationGracePeriod.Seconds())

	// Apply participation label to this pod
	if err := applyParticipationLabel(ctx, pod.Name); err != nil {
		slog.Error("failed to apply participation label", "error", err)
		os.Exit(1)
	}

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigChan
		handleShutdown(ctx, cancel, terminationGracePeriod)
	}()

	// Run leader election (blocks until shutdown)
	runLeaderElection(ctx)

	slog.Info("leader-labeler terminated")
}

func parseFlags() {
	flag.StringVar(&cfg.ElectionName, "election-name", "", "Name of the election (required)")
	flag.StringVar(&cfg.PodName, "pod-name", os.Getenv("POD_NAME"), "Name of this pod")
	flag.StringVar(&cfg.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Namespace of this pod")
	flag.StringVar(&cfg.LeadershipLabel, "leadership-label", "", "Label for leader status (default: <election-name>/is-leader)")
	flag.StringVar(&cfg.ParticipationLabel, "participation-label", "", "Label for participants (default: <election-name>/participant)")
	flag.DurationVar(&cfg.LeaseDuration, "lease-duration", 15*time.Second, "Lease duration")
	flag.DurationVar(&cfg.RenewDeadline, "renew-deadline", 10*time.Second, "Renew deadline")
	flag.DurationVar(&cfg.RetryPeriod, "retry-period", 2*time.Second, "Retry period")
	flag.Parse()

	// Validate required flags
	if cfg.ElectionName == "" {
		fmt.Fprintf(os.Stderr, "Error: --election-name is required\n")
		flag.Usage()
		os.Exit(1)
	}
	if cfg.PodName == "" {
		fmt.Fprintf(os.Stderr, "Error: --pod-name is required (or set POD_NAME env var)\n")
		flag.Usage()
		os.Exit(1)
	}
	if cfg.Namespace == "" {
		fmt.Fprintf(os.Stderr, "Error: --namespace is required (or set POD_NAMESPACE env var)\n")
		flag.Usage()
		os.Exit(1)
	}

	// Set default labels if not provided
	if cfg.LeadershipLabel == "" {
		cfg.LeadershipLabel = cfg.ElectionName + "/is-leader"
	}
	if cfg.ParticipationLabel == "" {
		cfg.ParticipationLabel = cfg.ElectionName + "/participant"
	}
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

func getTerminationGracePeriod(pod *corev1.Pod) time.Duration {
	if pod.Spec.TerminationGracePeriodSeconds != nil {
		return time.Duration(*pod.Spec.TerminationGracePeriodSeconds) * time.Second
	}
	// Default to 30s (Kubernetes default)
	return 30 * time.Second
}

func applyParticipationLabel(ctx context.Context, podName string) error {
	pod, err := client.CoreV1().Pods(cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	}

	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[cfg.ParticipationLabel] = "true"

	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				cfg.ParticipationLabel: "true",
			},
		},
	}
	patchBytes, _ := json.Marshal(patchData)

	_, err = client.CoreV1().Pods(cfg.Namespace).Patch(ctx, podName,
		types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch pod: %w", err)
	}

	slog.Info("applied participation label",
		"pod_name", podName,
		"label", cfg.ParticipationLabel)
	return nil
}

func removeParticipationLabel(ctx context.Context, podName string) error {
	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				cfg.ParticipationLabel: nil, // Setting to nil removes the label
			},
		},
	}
	patchBytes, _ := json.Marshal(patchData)

	_, err := client.CoreV1().Pods(cfg.Namespace).Patch(ctx, podName,
		types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		slog.Error("failed to remove participation label", "error", err, "pod_name", podName)
		return err
	}

	slog.Info("removed participation label", "pod_name", podName)
	return nil
}

func runLeaderElection(ctx context.Context) {
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

	// Reconciliation context and cancel function for leader
	var reconcileCtx context.Context
	var reconcileCancel context.CancelFunc

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   cfg.LeaseDuration,
		RenewDeadline:   cfg.RenewDeadline,
		RetryPeriod:     cfg.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				isLeader.Store(true)
				slog.Info("became leader", "pod_name", cfg.PodName)

				// Start reconciliation loop
				reconcileCtx, reconcileCancel = context.WithCancel(ctx)
				go reconcileLabels(reconcileCtx)
			},
			OnStoppedLeading: func() {
				if reconcileCancel != nil {
					reconcileCancel()
				}
				isLeader.Store(false)
				slog.Warn("lost leadership", "pod_name", cfg.PodName)
			},
			OnNewLeader: func(identity string) {
				if identity != cfg.PodName {
					slog.Info("new leader elected", "leader_identity", identity)
				}
			},
		},
	})
}

func reconcileLabels(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Run immediately on start
	performReconciliation(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("stopping label reconciliation")
			return
		case <-ticker.C:
			performReconciliation(ctx)
		}
	}
}

func performReconciliation(ctx context.Context) {
	// List all participants
	labelSelector := fmt.Sprintf("%s=true", cfg.ParticipationLabel)
	pods, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		slog.Error("failed to list participant pods", "error", err)
		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		isLeaderPod := pod.Name == cfg.PodName
		if err := labelPod(ctx, pod.Name, isLeaderPod); err != nil {
			slog.Error("failed to label pod during reconciliation",
				"error", err,
				"pod_name", pod.Name,
				"is_leader", isLeaderPod)
		}
	}

	slog.Debug("reconciliation complete", "participant_count", len(pods.Items))
}

func labelPod(ctx context.Context, podName string, isLeaderPod bool) error {
	leaderValue := "false"
	if isLeaderPod {
		leaderValue = "true"
	}

	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				cfg.ParticipationLabel: "true",
				cfg.LeadershipLabel:    leaderValue,
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

func handleShutdown(ctx context.Context, cancel context.CancelFunc, terminationGracePeriod time.Duration) {
	slog.Info("shutdown signal received, removing participation label")

	// FIRST: Remove participation label immediately
	// Derive from parent context but with our own timeout to ensure it completes
	removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer removeCancel()
	if err := removeParticipationLabel(removeCtx, cfg.PodName); err != nil {
		slog.Error("failed to remove participation label during shutdown", "error", err)
		// Continue with shutdown anyway - don't block on label removal
	}

	// Check if we're the leader
	if !isLeader.Load() {
		// Follower shutdown: terminate immediately
		slog.Info("follower shutdown, terminating immediately")
		cancel()
		return
	}

	// Leader shutdown: wait for successor
	slog.Info("leader shutdown, entering drain mode",
		"drain_timeout_seconds", terminationGracePeriod.Seconds())

	drainTimeout := time.After(terminationGracePeriod)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-drainTimeout:
			slog.Warn("drain timeout exceeded, forcing shutdown",
				"timeout_seconds", terminationGracePeriod.Seconds())
			cancel()
			return

		case <-ticker.C:
			// Count remaining participants (excluding self - we already removed our label)
			labelSelector := fmt.Sprintf("%s=true", cfg.ParticipationLabel)
			pods, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				slog.Error("failed to list participants during drain", "error", err)
				continue
			}

			participantCount := len(pods.Items)
			if participantCount > 0 {
				slog.Info("successor participant found, releasing leadership",
					"participant_count", participantCount)
				cancel()
				return
			}

			slog.Debug("waiting for successor participant",
				"participant_count", participantCount)
		}
	}
}
