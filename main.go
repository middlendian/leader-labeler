// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"
)

func main() {
	// configure klog with defaults
	klog.InitFlags(nil)
	defer klog.Flush()

	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		os.Exit(1) // LoadConfig logs its own errors
	}

	klog.InfoS("starting leader-labeler",
		"election_name", cfg.ElectionName,
		"pod_name", cfg.PodName,
		"pod_namespace", cfg.PodNamespace,
		"leadership_label", cfg.LeadershipLabel,
		"lease_duration", cfg.LeaseDuration,
		"timeout_deadline", cfg.TimeoutDeadline,
		"retry_interval", cfg.RetryInterval)

	ctx, cancel := context.WithCancel(context.Background())
	setupSignalHandler(cancel)
	defer cancel()

	client, err := NewKubernetesClient(ctx, cfg)
	if err != nil {
		os.Exit(1) // NewKubernetesClient logs its own errors
	}

	if err := RunElection(ctx, client, cfg); err != nil {
		os.Exit(1) // RunElection logs its own errors
	}
	klog.InfoS("leader-labeler terminated")
}

func setupSignalHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigChan
		klog.InfoS("shutdown signal received")
		cancel()
	}()
}
