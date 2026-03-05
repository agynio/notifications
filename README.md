# Notifications Service

## Generating protobufs locally

Run the following before building or testing locally to ensure the latest API definitions are available:

```bash
make proto
```

This command runs Buf to fetch the API definitions and generate Go bindings. The directories `internal/.proto` and `internal/.gen` are git-ignored and must not be committed. CI and the Docker build already execute the same generation steps automatically.
