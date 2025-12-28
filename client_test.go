// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestValidatePermissions(t *testing.T) {
	tests := []struct {
		name          string
		allowedChecks map[string]bool // "resource:verb" -> allowed
		wantErr       bool
		errContains   string
		reactorErr    error // if set, reactor returns this error
	}{
		{
			name: "all permissions allowed",
			allowedChecks: map[string]bool{
				"pods:get":      true,
				"pods:list":     true,
				"pods:patch":    true,
				"leases:get":    true,
				"leases:create": true,
				"leases:update": true,
			},
			wantErr: false,
		},
		{
			name: "missing pod permissions",
			allowedChecks: map[string]bool{
				"pods:get":      false,
				"pods:list":     false,
				"pods:patch":    false,
				"leases:get":    true,
				"leases:create": true,
				"leases:update": true,
			},
			wantErr:     true,
			errContains: "pods (core API)",
		},
		{
			name: "missing lease permissions",
			allowedChecks: map[string]bool{
				"pods:get":      true,
				"pods:list":     true,
				"pods:patch":    true,
				"leases:get":    false,
				"leases:create": false,
				"leases:update": false,
			},
			wantErr:     true,
			errContains: "leases (coordination.k8s.io)",
		},
		{
			name: "missing some permissions",
			allowedChecks: map[string]bool{
				"pods:get":      true,
				"pods:list":     true,
				"pods:patch":    false, // missing
				"leases:get":    true,
				"leases:create": true,
				"leases:update": false, // missing
			},
			wantErr:     true,
			errContains: "missing required RBAC permissions",
		},
		{
			name:        "SSAR API error",
			reactorErr:  errors.New("API server error"),
			wantErr:     true,
			errContains: "failed to check permission",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // NewClientset requires generated apply configs
			client := fake.NewSimpleClientset()

			// Add reactor to control SSAR responses
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if tt.reactorErr != nil {
					return true, nil, tt.reactorErr
				}

				createAction := action.(k8stesting.CreateAction)
				review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)

				resource := review.Spec.ResourceAttributes.Resource
				verb := review.Spec.ResourceAttributes.Verb
				key := resource + ":" + verb

				allowed := tt.allowedChecks[key]

				review.Status.Allowed = allowed
				return true, review, nil
			})

			cfg := &Config{
				ElectionName: "test-election",
				Namespace:    "default",
			}

			err := validatePermissions(context.Background(), client, cfg)

			if tt.wantErr {
				if err == nil {
					t.Error("validatePermissions() expected error, got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("validatePermissions() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("validatePermissions() unexpected error: %v", err)
			}
		})
	}
}

func TestCheckPermission(t *testing.T) {
	tests := []struct {
		name       string
		check      PermissionCheck
		allowed    bool
		reactorErr error
		wantOK     bool
		wantErr    bool
	}{
		{
			name:    "permission allowed",
			check:   PermissionCheck{Resource: "pods", Verb: "get"},
			allowed: true,
			wantOK:  true,
			wantErr: false,
		},
		{
			name:    "permission denied",
			check:   PermissionCheck{Resource: "pods", Verb: "delete"},
			allowed: false,
			wantOK:  false,
			wantErr: false,
		},
		{
			name:       "API error",
			check:      PermissionCheck{Resource: "pods", Verb: "get"},
			reactorErr: errors.New("connection refused"),
			wantOK:     false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // NewClientset requires generated apply configs
			client := fake.NewSimpleClientset()

			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if tt.reactorErr != nil {
					return true, nil, tt.reactorErr
				}

				createAction := action.(k8stesting.CreateAction)
				review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
				review.Status.Allowed = tt.allowed
				return true, review, nil
			})

			cfg := &Config{Namespace: "default"}

			ok, err := checkPermission(context.Background(), client, cfg, tt.check)

			if tt.wantErr {
				if err == nil {
					t.Error("checkPermission() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("checkPermission() unexpected error: %v", err)
				return
			}

			if ok != tt.wantOK {
				t.Errorf("checkPermission() = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestFormatPermissionError(t *testing.T) {
	tests := []struct {
		name     string
		missing  []PermissionCheck
		contains []string
	}{
		{
			name: "missing pod permissions",
			missing: []PermissionCheck{
				{Resource: "pods", Verb: "get"},
				{Resource: "pods", Verb: "patch"},
			},
			contains: []string{
				"pods (core API)",
				"get",
				"patch",
				"apiGroups: [\"\"]",
				"resources: [\"pods\"]",
			},
		},
		{
			name: "missing lease permissions",
			missing: []PermissionCheck{
				{Resource: "leases", Verb: "create"},
				{Resource: "leases", Verb: "update"},
			},
			contains: []string{
				"leases (coordination.k8s.io)",
				"create",
				"update",
				"apiGroups: [\"coordination.k8s.io\"]",
				"resources: [\"leases\"]",
			},
		},
		{
			name: "mixed permissions",
			missing: []PermissionCheck{
				{Resource: "pods", Verb: "list"},
				{Resource: "leases", Verb: "get"},
			},
			contains: []string{
				"pods (core API)",
				"leases (coordination.k8s.io)",
				"Namespace: test-ns",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Namespace: "test-ns"}
			err := formatPermissionError(tt.missing, cfg)

			errStr := err.Error()
			for _, s := range tt.contains {
				if !strings.Contains(errStr, s) {
					t.Errorf("formatPermissionError() error should contain %q, got:\n%s", s, errStr)
				}
			}
		})
	}
}

func TestFormatVerbList(t *testing.T) {
	tests := []struct {
		verbs []string
		want  string
	}{
		{[]string{"get"}, `"get"`},
		{[]string{"get", "list"}, `"get", "list"`},
		{[]string{"get", "list", "patch"}, `"get", "list", "patch"`},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.verbs, ","), func(t *testing.T) {
			got := formatVerbList(tt.verbs)
			if got != tt.want {
				t.Errorf("formatVerbList(%v) = %q, want %q", tt.verbs, got, tt.want)
			}
		})
	}
}
