module github.com/KoukeNeko/Plaitway

go 1.27.1

require (
	github.com/Microsoft/go-winio v0.6.2
	golang.org/x/net v0.59.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wireguard v0.0.0-20261006164505-2631ce99a06f
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2 // indirect
)

tool (
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)
