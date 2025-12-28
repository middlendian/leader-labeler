// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	SetupLogger()

	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		os.Exit(1) // LoadConfig logs its own errors
	}

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
	slog.Info("leader-labeler terminated")
}

func setupSignalHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigChan
		slog.Info("shutdown signal received")
		cancel()
	}()
}
