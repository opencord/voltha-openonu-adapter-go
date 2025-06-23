module github.com/opencord/voltha-openonu-adapter-go

go 1.24.0

replace (
	github.com/coreos/bbolt v1.3.4 => go.etcd.io/bbolt v1.3.4
	go.etcd.io/bbolt v1.3.4 => github.com/coreos/bbolt v1.3.4
	google.golang.org/grpc => google.golang.org/grpc v1.25.1
)

require (
	github.com/boguslaw-wojcik/crc32a v1.0.0
	github.com/cevaris/ordered_map v0.0.0-20190319150403-3adeae072e73
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.5.4
	github.com/google/gopacket v1.1.17
	github.com/grpc-ecosystem/go-grpc-middleware v1.3.0
	github.com/looplab/fsm v0.2.0
	github.com/opencord/omci-lib-go/v2 v2.2.3
	github.com/opencord/voltha-lib-go/v7 v7.6.6
	github.com/opencord/voltha-protos/v5 v5.6.2
	github.com/stretchr/testify v1.7.0
	google.golang.org/grpc v1.56.3
)

require (
	github.com/Shopify/sarama v1.29.1 // indirect
	github.com/aead/cmac v0.0.0-20160719120800-7af84192f0b1 // indirect
	github.com/bsm/sarama-cluster v2.1.15+incompatible // indirect
	github.com/cespare/xxhash/v2 v2.1.1 // indirect
	github.com/coreos/etcd v3.3.25+incompatible // indirect
	github.com/coreos/go-semver v0.3.0 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/coreos/pkg v0.0.0-20180928190104-399ea9e2e55f // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/deckarep/golang-set v1.7.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/eapache/go-resiliency v1.2.0 // indirect
	github.com/eapache/go-xerial-snappy v0.0.0-20180814174437-776d5712da21 // indirect
	github.com/eapache/queue v1.1.0 // indirect
	github.com/go-redis/redis/v8 v8.3.4 // indirect
	github.com/golang/snappy v0.0.3 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/hashicorp/go-uuid v1.0.2 // indirect
	github.com/jcmturner/aescts/v2 v2.0.0 // indirect
	github.com/jcmturner/dnsutils/v2 v2.0.0 // indirect
	github.com/jcmturner/gofork v1.0.0 // indirect
	github.com/jcmturner/gokrb5/v8 v8.4.2 // indirect
	github.com/jcmturner/rpc/v2 v2.0.3 // indirect
	github.com/jhump/protoreflect v1.10.2 // indirect
	github.com/klauspost/compress v1.12.2 // indirect
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/pierrec/lz4 v2.6.0+incompatible // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rcrowley/go-metrics v0.0.0-20201227073835-cf1acfcdf475 // indirect
	github.com/uber/jaeger-client-go v2.29.1+incompatible // indirect
	github.com/uber/jaeger-lib v2.4.1+incompatible // indirect
	go.etcd.io/etcd v3.3.25+incompatible // indirect
	go.opentelemetry.io/otel v0.13.0 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	go.uber.org/multierr v1.6.0 // indirect
	go.uber.org/zap v1.18.1 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/sys v0.29.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto v0.0.0-20220208230804-65c12eb4c068 // indirect
	google.golang.org/protobuf v1.36.5 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)
