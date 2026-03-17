# Kubernetes Bootstrap Guide

This guide walks through deploying the notifications service on a local Kubernetes
cluster (Kind or Minikube) using the provided Helm chart.

## Prerequisites

- Helm 3.11+
- kubectl
- A running Kind or Minikube cluster
- Access to the notifications container image (built locally or pulled from GHCR)

## 1. Generate protobufs

```bash
buf generate buf.build/agynio/api --template ./buf.gen.yaml
```

## 2. Build and load the container image

For Kind:

```bash
docker build -t agynio/notifications:dev .
kind load docker-image agynio/notifications:dev
```

For Minikube:

```bash
eval "$(minikube docker-env)"
docker build -t agynio/notifications:dev .
```

## 3. Install Redis (separately)

The notifications chart does **not** deploy Redis. Install one with your preferred
method; for local development the Bitnami chart is convenient:

```bash
helm repo add bitnami https://charts.bitnami.com/bitnami
helm upgrade --install notifications-redis bitnami/redis \
  --set architecture=standalone \
  --set auth.enabled=false \
  --wait
```

## 4. Deploy notifications via Helm

The chart installs the service without environment configuration. Supply your image
overrides (if needed) using the example values file:

```bash
helm upgrade --install notifications ./charts/notifications \
  -f charts/notifications/values.local.yaml \
  --set image.tag=dev
```

## 5. Apply bootstrap environment patch

The deployment expects gRPC and Redis configuration via environment variables.
Apply the provided patch manifest (adjust the Redis address if you installed it in
a different namespace or with another release name):

```bash
kubectl apply -f bootstrap/notifications-env.yaml
kubectl rollout status deployment/notifications
```

## 6. Port-forward the services

```bash
kubectl port-forward svc/notifications 50051:50051 &
kubectl port-forward svc/notifications-redis-master 6379:6379 &
```

## 7. E2E test (optional)

Set the expected environment variables and run the smoke harness:

```bash
NOTIFICATIONS_GRPC_ADDR=localhost:50051 \
NOTIFICATIONS_REDIS_ADDR=localhost:6379 \
NOTIFICATIONS_REDIS_DB=0 \
NOTIFICATIONS_CHANNEL=notifications.v1 \
go test -tags e2e ./test/e2e -run TestSmoke -count=1
```

Kill the background port-forward processes when finished.
