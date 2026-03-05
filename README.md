# Notifications Service

## Proto generation (no committed code)

From the repository root, run:

```bash
buf export buf.build/agynio/api --output internal/.proto
buf generate internal/.proto --template ./buf.gen.yaml
```

The generated sources in `internal/.proto` and `internal/.gen` are ignored by git.
