# Notifications Service

## Generating protobufs locally

Run the following **before** invoking `go build` or `go test` to ensure the latest API definitions are available:

```bash
make proto
```

This command runs Buf to fetch the API definitions and generate Go bindings. The directories `internal/.proto` and `internal/.gen` are git-ignored and must not be committed. CI and the Docker build already execute the same generation steps automatically.

## Helm installation

Install the service from the published OCI chart (replace `X.Y.Z` and Redis URL with your values):

```bash
helm install notifications oci://ghcr.io/agynio/charts/notifications \
  --version X.Y.Z \
  --set NOTIFICATIONS_REDIS_URL=redis://redis:6379 \
  --set NOTIFICATIONS_CHANNEL=notifications.v1
```

`NOTIFICATIONS_REDIS_URL` is required. Override `NOTIFICATIONS_CHANNEL` if you need a custom Redis topic.

## Chart values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Worker replicas for the deployment. |
| `image.repository` | `ghcr.io/agynio/notifications` | Container image repository. |
| `image.tag` | (Chart `appVersion`) | Image tag; defaults to the chart app version when empty. |
| `image.pullPolicy` | `IfNotPresent` | Kubernetes image pull policy. |
| `NOTIFICATIONS_REDIS_URL` | `""` | Redis connection string (**required**). |
| `NOTIFICATIONS_CHANNEL` | `notifications.v1` | Redis channel for published events. |
| `service.type` | `ClusterIP` | Kubernetes service type. |
| `service.port` | `50051` | Service and container port for gRPC traffic. |
| `resources` | `{}` | Container resource requests/limits. |
| `nodeSelector` | `{}` | Node selector rules. |
| `tolerations` | `[]` | Pod tolerations. |
| `affinity` | `{}` | Pod affinity rules. |
| `podSecurityContext` | `{}` | Pod-level security context. |
| `securityContext` | `{}` | Container-level security context. |
| `serviceAccount.create` | `true` | Whether to create a service account. |
| `serviceAccount.annotations` | `{}` | Annotations applied to the service account. |
| `podAnnotations` | `{}` | Extra annotations for the pod template. |
| `podLabels` | `{}` | Extra labels for the pod template. |
| `service.annotations` | `{}` | Annotations applied to the service. |
| `service.labels` | `{}` | Additional labels applied to the service. |
| `probes.liveness` | see `values.yaml` | TCP liveness probe configuration. |
| `probes.readiness` | see `values.yaml` | TCP readiness probe configuration. |
| `serviceMonitor.enabled` | `false` | Enable Prometheus ServiceMonitor creation. |
| `serviceMonitor.interval` | `30s` | Scrape interval when ServiceMonitor is enabled. |
| `serviceMonitor.scrapeTimeout` | `""` | Optional scrape timeout. |
