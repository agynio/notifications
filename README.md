# Notifications

Notifications is a gRPC service that publishes and delivers notification events backed by Redis.

Architecture: [Notifications](https://github.com/agynio/architecture/blob/main/architecture/notifications.md)

## Local Development

Full setup: [Local Development](https://github.com/agynio/architecture/blob/main/architecture/operations/local-development.md)

### Prepare environment

```bash
git clone https://github.com/agynio/bootstrap.git
cd bootstrap
chmod +x apply.sh
./apply.sh -y
```

See [bootstrap](https://github.com/agynio/bootstrap) for details.

### Run from sources

```bash
# Deploy once (exit when healthy)
devspace dev

# Watch mode (streams logs, re-syncs on changes)
devspace dev -w
```

### Run tests

```bash
devspace run test:e2e
```

See [E2E Testing](https://github.com/agynio/architecture/blob/main/architecture/operations/e2e-testing.md).
