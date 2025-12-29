// Copyright 2025 middlendian
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// NewKubernetesClient creates an in-cluster Kubernetes client and validates RBAC permissions.
// It logs errors before returning them.
func NewKubernetesClient(ctx context.Context, cfg *Config) (kubernetes.Interface, error) {
	client, err := buildClient()
	if err != nil {
		klog.ErrorS(err, "failed to build kubernetes client")
		return nil, err
	}

	if err := validatePermissions(ctx, client, cfg); err != nil {
		klog.ErrorS(err, "RBAC permission validation failed")
		return nil, err
	}

	return client, nil
}

// buildClient creates an in-cluster Kubernetes clientset
func buildClient() (kubernetes.Interface, error) {
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

// permissionCheck describes a single RBAC permission to validate
type permissionCheck struct {
	Resource     string
	APIGroup     string
	Verb         string
	ResourceName string // optional: specific resource name (e.g., lease name)
}

// validatePermissions checks that the service account has all required RBAC permissions.
// It uses SelfSubjectAccessReview to verify each permission before the application starts.
// Returns an error with detailed remediation instructions if any permission is missing.
func validatePermissions(ctx context.Context, client kubernetes.Interface, cfg *Config) error {
	klog.InfoS("validating RBAC permissions")

	// Define all required permissions
	checks := []permissionCheck{
		// Pod permissions (core API group)
		{Resource: "pods", APIGroup: "", Verb: "get"},
		{Resource: "pods", APIGroup: "", Verb: "list"},
		{Resource: "pods", APIGroup: "", Verb: "patch"},
		// Lease permissions (coordination.k8s.io API group)
		{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "get", ResourceName: cfg.ElectionName},
		{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "create", ResourceName: cfg.ElectionName},
		{Resource: "leases", APIGroup: "coordination.k8s.io", Verb: "update", ResourceName: cfg.ElectionName},
	}

	var missingPermissions []permissionCheck

	for _, check := range checks {
		allowed, err := checkPermission(ctx, client, cfg, check)
		if err != nil {
			return fmt.Errorf("failed to check permission (resource=%s, verb=%s): %w; "+
				"this may indicate the cluster does not support SelfSubjectAccessReview or "+
				"the service account lacks permission to perform access reviews",
				check.Resource, check.Verb, err)
		}
		if !allowed {
			missingPermissions = append(missingPermissions, check)
		}
	}

	if len(missingPermissions) > 0 {
		return formatPermissionError(missingPermissions, cfg)
	}

	klog.InfoS("all required RBAC permissions validated successfully")
	return nil
}

// checkPermission performs a SelfSubjectAccessReview for a single permission.
// Returns (true, nil) if allowed, (false, nil) if denied, or (false, error) on failure.
func checkPermission(ctx context.Context, client kubernetes.Interface, cfg *Config, check permissionCheck) (bool, error) {
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: cfg.PodNamespace,
				Verb:      check.Verb,
				Group:     check.APIGroup,
				Resource:  check.Resource,
				Name:      check.ResourceName,
			},
		},
	}

	result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(
		ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}

	if !result.Status.Allowed {
		klog.V(4).InfoS("permission check denied",
			"resource", check.Resource,
			"apiGroup", check.APIGroup,
			"verb", check.Verb,
			"name", check.ResourceName,
			"reason", result.Status.Reason,
			"evaluationError", result.Status.EvaluationError)
	}

	return result.Status.Allowed, nil
}

// formatPermissionError creates a detailed error message with remediation instructions.
func formatPermissionError(missing []permissionCheck, cfg *Config) error {
	var sb strings.Builder

	sb.WriteString("missing required RBAC permissions:\n\n")

	// Group by resource type for cleaner output
	var podVerbs, leaseVerbs []string
	for _, p := range missing {
		switch p.Resource {
		case "pods":
			podVerbs = append(podVerbs, p.Verb)
		case "leases":
			leaseVerbs = append(leaseVerbs, p.Verb)
		}
	}

	if len(podVerbs) > 0 {
		sb.WriteString(fmt.Sprintf("  - pods (core API): %s\n", strings.Join(podVerbs, ", ")))
	}
	if len(leaseVerbs) > 0 {
		sb.WriteString(fmt.Sprintf("  - leases (coordination.k8s.io): %s\n", strings.Join(leaseVerbs, ", ")))
	}

	sb.WriteString(fmt.Sprintf("\nNamespace: %s\n", cfg.PodNamespace))
	sb.WriteString("\nTo fix this, ensure the ServiceAccount has a Role and RoleBinding with the required permissions.\n")
	sb.WriteString("Example Role rules:\n\n")

	if len(podVerbs) > 0 {
		sb.WriteString("  - apiGroups: [\"\"]\n")
		sb.WriteString("    resources: [\"pods\"]\n")
		sb.WriteString(fmt.Sprintf("    verbs: [%s]\n\n", formatVerbList(podVerbs)))
	}

	if len(leaseVerbs) > 0 {
		sb.WriteString("  - apiGroups: [\"coordination.k8s.io\"]\n")
		sb.WriteString("    resources: [\"leases\"]\n")
		sb.WriteString(fmt.Sprintf("    verbs: [%s]\n", formatVerbList(leaseVerbs)))
	}

	return fmt.Errorf("%s", sb.String())
}

// formatVerbList formats verbs for YAML output: "get", "list", "patch"
func formatVerbList(verbs []string) string {
	quoted := make([]string, len(verbs))
	for i, v := range verbs {
		quoted[i] = fmt.Sprintf("\"%s\"", v)
	}
	return strings.Join(quoted, ", ")
}
