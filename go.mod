module github.com/opencord/voltha-openonu-adapter-go

go 1.16

replace (
	github.com/coreos/bbolt v1.3.4 => go.etcd.io/bbolt v1.3.4
	go.etcd.io/bbolt v1.3.4 => github.com/coreos/bbolt v1.3.4
	google.golang.org/grpc => google.golang.org/grpc v1.25.1
)

require (
	github.com/boguslaw-wojcik/crc32a v1.0.0
	github.com/cevaris/ordered_map v0.0.0-20190319150403-3adeae072e73
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.5.2
	github.com/google/gopacket v1.1.17
	github.com/looplab/fsm v0.2.0
	github.com/opencord/omci-lib-go/v2 v2.2.0
	github.com/opencord/voltha-lib-go/v7 v7.1.3
	github.com/opencord/voltha-protos/v5 v5.1.2
	github.com/stretchr/testify v1.7.0
	google.golang.org/grpc v1.42.0
)
