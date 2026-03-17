# syntax=docker/dockerfile:1
ARG GO_VERSION=1.22
ARG BUF_VERSION=1.64.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS buf
ARG BUF_VERSION
RUN apk add --no-cache curl
RUN curl -sSL \
      "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-$(uname -s)-$(uname -m)" \
      -o /usr/local/bin/buf && \
    chmod +x /usr/local/bin/buf

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY --from=buf /usr/local/bin/buf /usr/local/bin/buf
COPY go.mod go.sum ./
RUN go mod download
COPY buf.gen.yaml ./
RUN buf generate buf.build/agynio/api --template ./buf.gen.yaml
COPY . .
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN go build -o /out/notifications ./cmd/notifications

FROM alpine:3.19
WORKDIR /app
COPY --from=build /out/notifications /app/notifications
ENTRYPOINT ["/app/notifications"]
