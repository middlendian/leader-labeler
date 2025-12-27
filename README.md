# leader-labeler

Leader election sidecar for Kubernetes with automatic pod labeling

## What is it?

A lightweight Kubernetes sidecar container that performs leader election and automatically labels the leader pod. This
enables simple active-standby failover patterns and service routing to leader pods without modifying your application
code.

## Why?

The leader-labeler sidecar solves the problem of implementing active-standby failover (and other single-leader logic) in
Kubernetes for applications that:

- **Cannot be modified** to use the Kubernetes API directly (legacy apps, third-party software)
- **Need high availability** with automatic failover to standby replicas
- **Require traffic routing** to only the active/leader instance
- **Want simple leader awareness** without complex application changes

## How does it work?

* Make sure you have a deployment or stateful set with at least 2 pods (1 leader and 1+ followers).
* The `leader-labeler` sidecar runs in each pod, participating in leader elections using the standard Kubernetes API for
  lease objects.
* The leader automatically applies labels to itself (default: `is-leader=true`) and its followers (default:
  `is-leader=false`).
* A `Service` object can use the label selector `is-leader: true` to route traffic _only_ to the leader pod. Other
  Kubernetes objects and clients can also use these labels to easily identify the leader and follower pods.
* The sidecar handles graceful startup and termination to minimize downtime during normal deployments and other
  operational activities.
* Timeouts, update intervals, and label names are all configurable according to your needs.

## Parameters

(TODO: turn this into a nicely-formatted table.)

* `--election-name` (required): a unique name for this election group within the pod namespace; this defines the shared
  lease/lock file which all participating pods will reference. Must be a valid object name (
  see [Kubernetes docs](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#rfc-1035-label-names))
* `--pod-name`  (default: `$POD_NAME` from env): the name of the current pod
* `--namespace` (default: `$POD_NAMESPACE` from env): the namespace of the current pod (and associated lease object)
* `--leadership-label` (default: `<election-name>/is-leader`): a label whose value will be either `true` or `false`
  indicating if the pod is the active leader of the given `<election-name>` group.
* `--participation-label` (default: `<election-name>/participant`): a label which is auto-applied for all pods
  participating in this leader election.
* `--lease-duration` (default: `15s`): Duration non-leaders wait before attempting to acquire leadership
* `--renew-deadline` (default: `10s`): Duration the leader has to renew leadership before giving up
* `--retry-period` (default: `2s`): Duration clients wait between leadership action attempts
