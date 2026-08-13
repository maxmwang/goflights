gen:
	protoc --go_out=. --go_opt=module=github.com/maxmwang/goflights \
		--go-grpc_out=. --go-grpc_opt=module=github.com/maxmwang/goflights \
		internal/pb/request.proto internal/pb/response.proto
