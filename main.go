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

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
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

	slog.Info("pod ready, joining election")

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
		handleShutdown(ctx, cancel)
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
	fs.StringVar(&c.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Namespace of this pod")
	fs.StringVar(&c.LeadershipLabel, "leadership-label", "", "Label for leader status (default: <election-name>/is-leader)")
	fs.StringVar(&c.ParticipationLabel, "participation-label", "", "Label for participants (default: <election-name>/participant)")
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
		return nil, fmt.Errorf("--namespace is required (or set POD_NAMESPACE env var)")
	}

	// Set default labels if not provided
	c.LeadershipLabel, c.ParticipationLabel = determineLabelNames(
		c.ElectionName,
		c.LeadershipLabel,
		c.ParticipationLabel,
	)

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

// determineLabelNames generates default label names based on the election name if custom labels aren't provided.
func determineLabelNames(electionName, leadershipLabel, participationLabel string) (string, string) {
	if leadershipLabel == "" {
		leadershipLabel = electionName + "/is-leader"
	}
	if participationLabel == "" {
		participationLabel = electionName + "/participant"
	}
	return leadershipLabel, participationLabel
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

// watchLeaseForEarlyElection monitors the Lease object and triggers early election
// attempts when the lease holder is cleared or the lease is deleted.
func watchLeaseForEarlyElection(ctx context.Context, triggerChan chan<- struct{}) {
	watcher, err := client.CoordinationV1().Leases(cfg.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + cfg.ElectionName,
	})
	if err != nil {
		slog.Warn("failed to start lease watcher, falling back to polling", "error", err)
		return
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				slog.Debug("lease watcher channel closed")
				return
			}

			// Only trigger for non-leaders when lease becomes available
			if isLeader.Load() {
				continue
			}

			if event.Type == watch.Deleted {
				slog.Info("lease deleted, triggering early election")
				select {
				case triggerChan <- struct{}{}:
				default: // Don't block if channel full
				}
				continue
			}

			if event.Type == watch.Modified {
				lease, ok := event.Object.(*coordinationv1.Lease)
				if !ok {
					continue
				}
				if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
					slog.Info("lease holder cleared, triggering early election")
					select {
					case triggerChan <- struct{}{}:
					default:
					}
				}
			}
		}
	}
}

func runLeaderElection(ctx context.Context) {
	triggerChan := make(chan struct{}, 1)

	// Start the lease watcher for early election triggering
	go watchLeaseForEarlyElection(ctx, triggerChan)

	for ctx.Err() == nil {
		// Create a cancellable context for this election cycle
		electionCtx, electionCancel := context.WithCancel(ctx)

		// Goroutine to cancel election on early trigger (for non-leaders only)
		go func() {
			select {
			case <-electionCtx.Done():
				return
			case <-triggerChan:
				if !isLeader.Load() {
					slog.Debug("early election trigger received, restarting election cycle")
					electionCancel()
				}
			}
		}()

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

		electionCancel() // Clean up election context

		// Brief pause before retrying (if not shutting down)
		if ctx.Err() == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
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

func handleShutdown(ctx context.Context, cancel context.CancelFunc) {
	slog.Info("shutdown signal received, removing participation label")

	// Remove participation label immediately
	removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer removeCancel()
	if err := removeParticipationLabel(removeCtx, cfg.PodName); err != nil {
		slog.Error("failed to remove participation label during shutdown", "error", err)
	}

	slog.Info("releasing leadership and terminating")
	cancel()
}
