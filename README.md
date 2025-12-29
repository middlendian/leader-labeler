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
* Timeouts and label names are configurable according to your needs.

## Quick Start

### 1. Install RBAC permissions

```bash
kubectl apply -f example/rbac.yaml
```

### 2. Deploy your application with the sidecar

```bash
kubectl apply -f example/deployment.yaml
```

### 3. Create a Service that routes to the leader

```bash
kubectl apply -f example/service.yaml
```



## Quick Start

### 1. Create/update a service account with the required permissions

These need to be available in the pod namespace, for managing leases (locks) and updating labels.

```yaml
rules:
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "create", "update"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "patch"]
```

See the example at [./example/rbac.yaml](./example/rbac.yaml).

### 2. Add the sidecar to your replica set

To use the default settings, add this to your manifest, replacing `my-app` with a unique election name relevant to
your specific application (it will be used for the lease object).

```yaml
containers:
- name: leader-labeler
  image: ghcr.io/middlendian/leader-labeler:latest # or a specific tag/SHA
  args:
  - "--election-name=my-app"
  env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
```

See the example at [./example/deployment.yaml](./example/deployment.yaml).

### 3. Use the label in pod selectors such as a Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
spec:
  selector:
    app: my-app
    my-app/is-leader: "true"  # <-- For routing traffic to the leader pod
```

See the example at [./example/service.yaml](./example/service.yaml).

### 4. Verify leader election and labels

```bash
# Check which pod is the leader
kubectl get pods -l app=my-app --show-labels

# Watch the leader-labeler logs
kubectl logs -f <pod-name> -c leader-labeler
```


## Configuration Details

### Command-Line Parameters

| Parameter | Required | Default | Description                                                                         |
|-----------|----------|---------|-------------------------------------------------------------------------------------|
| `--election-name` | Yes | - | Unique name for this election group. Used as the lease name and label prefix.       |
| `--pod-name` | No | `$POD_NAME` | Name of the current pod. Usually set via downward API.                              |
| `--pod-namespace` | No | `$POD_NAMESPACE` | Namespace of the pod and lease. Usually set via downward API.                       |
| `--leadership-label` | No | `<election-name>/is-leader` | Label key for leader status (`true` or `false`).                                    |
| `--lease-duration` | No | `15s` | How long non-leaders wait before attempting to acquire leadership.                  |
| `--timeout-deadline` | No | `10s` | How long the leader has to renew leadership (and related actions) before giving up. |
| `--retry-interval` | No | `2s` | How often to retry leadership actions.                                              |

### Environment Variables

The sidecar uses these environment variables (typically set via Kubernetes downward API):

```yaml
env:
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
```

## Usage Examples

### Basic Active-Standby Pattern

Deploy with 2 replicas where only the leader receives traffic:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  replicas: 2
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 100%      # Ensures new pods are Ready before old ones terminate
      maxUnavailable: 0
  template:
    spec:
      serviceAccountName: leader-labeler
      containers:
      - name: app
        image: my-app:latest
      - name: leader-labeler
        image: ghcr.io/middlendian/leader-labeler:latest
        args:
        - --election-name=my-app
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
---
apiVersion: v1
kind: Service
metadata:
  name: my-app
spec:
  selector:
    app: my-app
    my-app/is-leader: "true"  # Only route to leader
  ports:
  - port: 80
```

### Custom Label Name

```yaml
- name: leader-labeler
  args:
  - --election-name=database
  - --leadership-label=db.example.com/primary
```

### Tuning for Fast Failover

```yaml
- name: leader-labeler
  args:
  - --election-name=my-app
  - --lease-duration=10s
  - --timeout-deadline=5s
  - --retry-interval=1s
```

**Note**: Shorter timeouts increase API server load. Use defaults unless you need faster failover.

## Leadership transfer scenarios 

### At Pod Startup

1. Each pod runs the leader-labeler sidecar
2. Sidecars wait for the pod to become Ready, as determined by the other container(s)
3. Ready pods apply the label `<election-name>/is-leader=false` to themselves
4. Pods participate in leader election using Kubernetes Lease objects
5. When a pod becomes leader, it reconciles all labels:
   - Sets `<election-name>/is-leader=true` on itself
   - Sets `<election-name>/is-leader=false` on all other pods with the label

### Graceful Termination

During normal operations such as draining a node or scheduled pod deletion, shutdown is handled gracefully:

1. Pod receives SIGTERM
2. If leader, pod releases the lease immediately
3. An election is triggered within `retry-interval` (default 2s)

### Rolling Updates

For zero-downtime deployments, use `maxSurge: 100%` to ensure new pods are Ready before old ones terminate:

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxSurge: 100%      # Double pods temporarily for smooth transition
    maxUnavailable: 0   # Never go below desired replicas
```

**Why this works**:
1. Kubernetes creates new pods (all replicas) before terminating old ones
2. New pods become Ready and join the election
3. Kubernetes sends SIGTERM to old pods
4. Old leader releases lease, new follower acquires within `retry-interval` (2s)
5. Leadership transfers smoothly, old pods exit

This approach temporarily doubles the number of pods during updates, but ensures successors are always available before any termination begins.

## Troubleshooting

### Check Leader Status

```bash
# View pod labels
kubectl get pods -l app=my-app --show-labels

# Check which pod is leader
kubectl get pods -l app=my-app -l my-app/is-leader=true

# View leader-labeler logs
kubectl logs -l app=my-app -c leader-labeler --tail=50
```

### Common Issues

**No pod becomes leader**
- Check RBAC permissions: `kubectl get role,rolebinding -n <namespace>`
- Verify pods are Ready: `kubectl get pods`
- Check sidecar logs for errors

**Labels not appearing**
- Verify pods are Ready (label is applied after readiness)
- Check the leader pod has no errors in its logs
- Ensure RBAC allows `patch` on pods

**Leadership flapping**
- Check network connectivity between pods and API server
- Review lease timeout settings (may be too aggressive)
- Check API server load and response times

**Traffic not reaching leader**
- Verify Service selector matches the leadership label
- Ensure main container's readiness probe is passing
- Check Service endpoints: `kubectl get endpoints <service-name>`


## License

GPLv3; See [LICENSE](LICENSE) file for details.
