FROM golang:1.22 AS builder

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOBIN=/usr/local/bin

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    protobuf-compiler && \
    rm -rf /var/lib/apt/lists/*

RUN curl -sSL https://github.com/bufbuild/buf/releases/download/v1.64.0/buf-Linux-x86_64.tar.gz \
      | tar -xzf - -C /usr/local --strip-components=1 buf/bin/buf

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN buf export buf.build/agynio/api --output internal/.proto && \
    buf generate internal/.proto --template ./buf.gen.yaml

RUN go build -o /out/notifications ./cmd/notifications

FROM gcr.io/distroless/static-debian12

COPY --from=builder /out/notifications /bin/notifications

ENTRYPOINT ["/bin/notifications"]
