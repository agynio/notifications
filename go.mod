module github.com/agynio/notifications

go 1.22

require (
	github.com/alicebob/miniredis/v2 v2.37.0
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.18.0
	go.uber.org/zap v1.27.1
	google.golang.org/grpc v1.67.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
)

replace golang.org/x/net => golang.org/x/net v0.26.0

replace golang.org/x/sys => golang.org/x/sys v0.13.0

replace golang.org/x/text => golang.org/x/text v0.14.0

replace google.golang.org/genproto => google.golang.org/genproto v0.0.0-20240123012728-ef4313101c80

replace google.golang.org/genproto/googleapis/api => google.golang.org/genproto/googleapis/api v0.0.0-20240123012728-ef4313101c80

replace google.golang.org/genproto/googleapis/rpc => google.golang.org/genproto/googleapis/rpc v0.0.0-20240123012728-ef4313101c80
