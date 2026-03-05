# Kubernetes Bootstrap Guide

This guide walks through deploying the notifications service on a local Kubernetes
cluster (Kind or Minikube) using the provided Helm chart.

## Prerequisites

- Helm 3.11+
- kubectl
- A running Kind or Minikube cluster
- Access to the notifications container image (built locally or pulled from GHCR)

## 1. Export and generate protobufs

```bash
make proto
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

## 3. Install Redis

```bash
helm repo add bitnami https://charts.bitnami.com/bitnami
helm upgrade --install notifications-redis bitnami/redis \
  --set auth.enabled=false \
  --wait
```

## 4. Deploy notifications via Helm

Use the provided example values file as a starting point:

```bash
helm upgrade --install notifications ./charts/notifications \
  -f charts/notifications/values.local.yaml \
  --set image.tag=dev \
  --set NOTIFICATIONS_REDIS_URL=redis://notifications-redis-master:6379/0
```

## 5. Port-forward the services

```bash
kubectl port-forward svc/notifications 9090:9090 &
kubectl port-forward svc/notifications-redis-master 6379:6379 &
```

## 6. Smoke test (optional)

Set the expected environment variables and run the smoke harness:

```bash
SMOKE_GRPC_ADDR=localhost:9090 \
SMOKE_REDIS_ADDR=redis://localhost:6379/0 \
go test ./test/smoke -run TestSmoke -count=1
```

Kill the background port-forward processes when finished.
