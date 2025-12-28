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

### 4. Verify leader election

```bash
# Check which pod is the leader
kubectl get pods -l app=my-app --show-labels

# Watch the leader-labeler logs
kubectl logs -f <pod-name> -c leader-labeler
```

## Installation

### Using Pre-built Container Image

```yaml
containers:
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
```

### Building from Source

```bash
# Clone the repository
git clone https://github.com/middlendian/leader-labeler.git
cd leader-labeler

# Build the binary
make build

# Run tests
make test

# Build Docker image
make docker-build

# Push to your registry
export REGISTRY=your-registry.io/your-org
make docker-push
```

## Configuration

### Command-Line Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `--election-name` | Yes | - | Unique name for this election group. Used as the lease name and label prefix. |
| `--pod-name` | No | `$POD_NAME` | Name of the current pod. Usually set via downward API. |
| `--namespace` | No | `$POD_NAMESPACE` | Namespace of the pod and lease. Usually set via downward API. |
| `--leadership-label` | No | `<election-name>/is-leader` | Label key for leader status (`true` or `false`). |
| `--participation-label` | No | `<election-name>/participant` | Label key for active participants (`true` when participating). |
| `--lease-duration` | No | `15s` | How long non-leaders wait before attempting to acquire leadership. |
| `--renew-deadline` | No | `10s` | How long the leader has to renew leadership before giving up. |
| `--retry-period` | No | `2s` | How often to retry leadership actions. |

### Environment Variables

The sidecar requires these environment variables (typically set via Kubernetes downward API):

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

### Custom Label Names

```yaml
- name: leader-labeler
  args:
  - --election-name=database
  - --leadership-label=db.example.com/primary
  - --participation-label=db.example.com/member
```

### Tuning for Fast Failover

```yaml
- name: leader-labeler
  args:
  - --election-name=my-app
  - --lease-duration=10s
  - --renew-deadline=5s
  - --retry-period=1s
```

**Note**: Shorter timeouts increase API server load. Use defaults unless you need faster failover.

## How It Works

### Leader Election

1. Each pod runs the leader-labeler sidecar
2. Sidecars wait for the main container to become Ready
3. Ready pods apply the participation label (`<election-name>/participant=true`)
4. Pods participate in leader election using Kubernetes Lease objects
5. The leader continuously reconciles labels every 5 seconds:
   - Sets `<election-name>/is-leader=true` on itself
   - Sets `<election-name>/is-leader=false` on all other participants

### Graceful Shutdown

**Zero-downtime deployments** through clean shutdown handling:

#### Any Pod Shutdown
1. Receives SIGTERM
2. Removes participation label immediately
3. Releases the lease (if leader)
4. Terminates

With `maxSurge: 100%` rolling updates, new pods are Ready and participating in the election **before** SIGTERM is sent to old pods. This ensures:
- A successor is always available to take over leadership
- Leadership transfer happens within the `retry-period` (default 2s)
- No draining timeout logic is needed

### Traffic Continuity

For the leader to continue serving traffic during graceful shutdown, the **main container** must:

- **Option A**: Keep its readiness probe passing during SIGTERM handling
- **Option B**: Not have a readiness probe (use only liveness)

This ensures the pod remains Ready and in the Service endpoints while waiting for a successor.

## Rolling Update Strategy

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
4. Old leader releases lease, new follower acquires within `retry-period` (2s)
5. Leadership transfers smoothly, old pods exit

This approach temporarily doubles the number of pods during updates, but ensures successors are always available before any termination begins.

## RBAC Requirements

The sidecar requires these permissions in the pod's namespace:

```yaml
rules:
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "create", "update"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]
```

Apply the included RBAC manifest:

```bash
kubectl apply -f example/rbac.yaml
```

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
- Ensure pods have the participation label after becoming Ready
- Verify the leader pod has no errors in its logs
- Check RBAC allows `patch` on pods

**Leadership flapping**
- Check network connectivity between pods and API server
- Review lease timeout settings (may be too aggressive)
- Check API server load and response times

**Traffic not reaching leader**
- Verify Service selector matches the leadership label
- Ensure main container's readiness probe is passing
- Check Service endpoints: `kubectl get endpoints <service-name>`

## Contributing

Contributions welcome! Please ensure:
- All tests pass: `make test`
- Code is formatted: `make fmt`
- Linter passes: `make lint` (requires golangci-lint)

## License

See [LICENSE](LICENSE) file for details
