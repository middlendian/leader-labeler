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
			client := fake.NewClientset()

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
				PodNamespace: "default",
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
		check      permissionCheck
		allowed    bool
		reactorErr error
		wantOK     bool
		wantErr    bool
	}{
		{
			name:    "permission allowed",
			check:   permissionCheck{Resource: "pods", Verb: "get"},
			allowed: true,
			wantOK:  true,
			wantErr: false,
		},
		{
			name:    "permission denied",
			check:   permissionCheck{Resource: "pods", Verb: "delete"},
			allowed: false,
			wantOK:  false,
			wantErr: false,
		},
		{
			name:       "API error",
			check:      permissionCheck{Resource: "pods", Verb: "get"},
			reactorErr: errors.New("connection refused"),
			wantOK:     false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientset()

			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if tt.reactorErr != nil {
					return true, nil, tt.reactorErr
				}

				createAction := action.(k8stesting.CreateAction)
				review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
				review.Status.Allowed = tt.allowed
				return true, review, nil
			})

			cfg := &Config{PodNamespace: "default"}

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
		missing  []permissionCheck
		contains []string
	}{
		{
			name: "missing pod permissions",
			missing: []permissionCheck{
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
			missing: []permissionCheck{
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
			missing: []permissionCheck{
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
			cfg := &Config{PodNamespace: "test-ns"}
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

func TestCheckPermission_PassesResourceName(t *testing.T) {
	client := fake.NewClientset()

	var capturedName string
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
		capturedName = review.Spec.ResourceAttributes.Name
		review.Status.Allowed = true
		return true, review, nil
	})

	cfg := &Config{
		PodNamespace: "default",
		ElectionName: "my-election",
	}

	check := permissionCheck{
		Resource:     "leases",
		APIGroup:     "coordination.k8s.io",
		Verb:         "get",
		ResourceName: "my-lease-name",
	}

	_, err := checkPermission(context.Background(), client, cfg, check)
	if err != nil {
		t.Fatalf("checkPermission() error: %v", err)
	}

	if capturedName != "my-lease-name" {
		t.Errorf("ResourceAttributes.Name = %q, want %q", capturedName, "my-lease-name")
	}
}

func TestCheckPermission_PassesCorrectNamespace(t *testing.T) {
	client := fake.NewClientset()

	var capturedNamespace string
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
		capturedNamespace = review.Spec.ResourceAttributes.Namespace
		review.Status.Allowed = true
		return true, review, nil
	})

	cfg := &Config{
		PodNamespace: "my-custom-namespace",
	}

	check := permissionCheck{
		Resource: "pods",
		Verb:     "get",
	}

	_, err := checkPermission(context.Background(), client, cfg, check)
	if err != nil {
		t.Fatalf("checkPermission() error: %v", err)
	}

	if capturedNamespace != "my-custom-namespace" {
		t.Errorf("ResourceAttributes.PodNamespace = %q, want %q", capturedNamespace, "my-custom-namespace")
	}
}

func TestCheckPermission_PassesAPIGroup(t *testing.T) {
	tests := []struct {
		name      string
		check     permissionCheck
		wantGroup string
	}{
		{
			name: "core API group (empty)",
			check: permissionCheck{
				Resource: "pods",
				APIGroup: "",
				Verb:     "get",
			},
			wantGroup: "",
		},
		{
			name: "coordination API group",
			check: permissionCheck{
				Resource: "leases",
				APIGroup: "coordination.k8s.io",
				Verb:     "get",
			},
			wantGroup: "coordination.k8s.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientset()

			var capturedGroup string
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				createAction := action.(k8stesting.CreateAction)
				review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
				capturedGroup = review.Spec.ResourceAttributes.Group
				review.Status.Allowed = true
				return true, review, nil
			})

			cfg := &Config{PodNamespace: "default"}

			_, err := checkPermission(context.Background(), client, cfg, tt.check)
			if err != nil {
				t.Fatalf("checkPermission() error: %v", err)
			}

			if capturedGroup != tt.wantGroup {
				t.Errorf("ResourceAttributes.Group = %q, want %q", capturedGroup, tt.wantGroup)
			}
		})
	}
}

func TestValidatePermissions_ChecksAllRequiredPermissions(t *testing.T) {
	client := fake.NewClientset()

	// Collect all permission checks
	type permCheck struct {
		Resource string
		Verb     string
		Group    string
		Name     string
	}
	var capturedChecks []permCheck

	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		review := createAction.GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes

		capturedChecks = append(capturedChecks, permCheck{
			Resource: attrs.Resource,
			Verb:     attrs.Verb,
			Group:    attrs.Group,
			Name:     attrs.Name,
		})

		review.Status.Allowed = true
		return true, review, nil
	})

	cfg := &Config{
		ElectionName: "test-election",
		PodNamespace: "default",
	}

	err := validatePermissions(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("validatePermissions() error: %v", err)
	}

	// Expected checks
	expectedChecks := []permCheck{
		{Resource: "pods", Verb: "get", Group: "", Name: ""},
		{Resource: "pods", Verb: "list", Group: "", Name: ""},
		{Resource: "pods", Verb: "patch", Group: "", Name: ""},
		{Resource: "leases", Verb: "get", Group: "coordination.k8s.io", Name: "test-election"},
		{Resource: "leases", Verb: "create", Group: "coordination.k8s.io", Name: "test-election"},
		{Resource: "leases", Verb: "update", Group: "coordination.k8s.io", Name: "test-election"},
	}

	if len(capturedChecks) != len(expectedChecks) {
		t.Fatalf("expected %d permission checks, got %d: %+v", len(expectedChecks), len(capturedChecks), capturedChecks)
	}

	// Build a set of expected checks for easy lookup
	expectedSet := make(map[string]bool)
	for _, e := range expectedChecks {
		key := e.Resource + ":" + e.Verb + ":" + e.Group + ":" + e.Name
		expectedSet[key] = true
	}

	// Verify each captured check is expected
	for _, c := range capturedChecks {
		key := c.Resource + ":" + c.Verb + ":" + c.Group + ":" + c.Name
		if !expectedSet[key] {
			t.Errorf("unexpected permission check: %+v", c)
		}
	}
}

func TestValidatePermissions_ContextCancellation(t *testing.T) {
	client := fake.NewClientset()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context on first SSAR call
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		cancel() // Cancel context
		return true, nil, ctx.Err()
	})

	cfg := &Config{
		ElectionName: "test-election",
		PodNamespace: "default",
	}

	err := validatePermissions(ctx, client, cfg)
	if err == nil {
		t.Error("validatePermissions() expected error on context cancellation, got nil")
	}
}

func TestValidatePermissions_StopsOnFirstAPIError(t *testing.T) {
	client := fake.NewClientset()

	var checkCount int
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		checkCount++
		return true, nil, errors.New("API server unavailable")
	})

	cfg := &Config{
		ElectionName: "test-election",
		PodNamespace: "default",
	}

	err := validatePermissions(context.Background(), client, cfg)
	if err == nil {
		t.Error("validatePermissions() expected error, got nil")
	}

	if checkCount != 1 {
		t.Errorf("expected 1 check before stopping, got %d", checkCount)
	}
}
