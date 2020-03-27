/*
* Copyright 2018-present Open Networking Foundation

* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at

* http://www.apache.org/licenses/LICENSE-2.0

* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

//Package config provides the Log, kvstore, Kafka configuration
package config

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/opencord/voltha-lib-go/v3/pkg/log"
)

// Open ONT default constants
const (
	EtcdStoreName               = "etcd"
	defaultInstanceid           = "openonu"
	defaultKafkaadapterhost     = "192.168.0.20"
	defaultKafkaadapterport     = 9092
	defaultKafkaclusterhost     = "10.100.198.220"
	defaultKafkaclusterport     = 9092
	defaultKvstoretype          = EtcdStoreName
	defaultKvstoretimeout       = 5 //in seconds
	defaultKvstorehost          = "localhost"
	defaultKvstoreport          = 2379 // Consul = 8500; Etcd = 2379
	defaultLoglevel             = "DEBUG"
	defaultBanner               = false
	defaultDisplayVersionOnly   = false
	defaultTopic                = "openonu"
	defaultCoretopic            = "rwcore"
	defaultEventtopic           = "voltha.events"
	defaultOnunumber            = 1
	defaultProbeHost            = ""
	defaultProbePort            = 8080
	defaultLiveProbeInterval    = 60 * time.Second
	defaultNotLiveProbeInterval = 5 * time.Second // Probe more frequently when not alive
	//defaultHearbeatFailReportInterval is the time in seconds the adapter will keep checking the hardware for heartbeat.
	defaultHearbeatCheckInterval = 30 * time.Second
	// defaultHearbeatFailReportInterval is the time adapter will wait before updating the state to the core.
	defaultHearbeatFailReportInterval = 180 * time.Second
	//defaultKafkaReconnectRetries -1: reconnect endlessly.
	defaultKafkaReconnectRetries = -1
)

// AdapterFlags represents the set of configurations used by the read-write adaptercore service
type AdapterFlags struct {
	// Command line parameters
	InstanceID                  string
	KafkaAdapterHost            string
	KafkaAdapterPort            int
	KafkaClusterHost            string
	KafkaClusterPort            int
	KVStoreType                 string
	KVStoreTimeout              int // in seconds
	KVStoreHost                 string
	KVStorePort                 int
	Topic                       string
	CoreTopic                   string
	EventTopic                  string
	LogLevel                    string
	OnuNumber                   int
	Banner                      bool
	DisplayVersionOnly          bool
	ProbeHost                   string
	ProbePort                   int
	LiveProbeInterval           time.Duration
	NotLiveProbeInterval        time.Duration
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	KafkaReconnectRetries       int
}

func init() {
	_, _ = log.AddPackage(log.JSON, log.WarnLevel, nil)
}

// NewAdapterFlags returns a new RWCore config
func NewAdapterFlags() *AdapterFlags {
	var adapterFlags = AdapterFlags{ // Default values
		InstanceID:                  defaultInstanceid,
		KafkaAdapterHost:            defaultKafkaadapterhost,
		KafkaAdapterPort:            defaultKafkaadapterport,
		KafkaClusterHost:            defaultKafkaclusterhost,
		KafkaClusterPort:            defaultKafkaclusterport,
		KVStoreType:                 defaultKvstoretype,
		KVStoreTimeout:              defaultKvstoretimeout,
		KVStoreHost:                 defaultKvstorehost,
		KVStorePort:                 defaultKvstoreport,
		Topic:                       defaultTopic,
		CoreTopic:                   defaultCoretopic,
		EventTopic:                  defaultEventtopic,
		LogLevel:                    defaultLoglevel,
		OnuNumber:                   defaultOnunumber,
		Banner:                      defaultBanner,
		DisplayVersionOnly:          defaultDisplayVersionOnly,
		ProbeHost:                   defaultProbeHost,
		ProbePort:                   defaultProbePort,
		LiveProbeInterval:           defaultLiveProbeInterval,
		NotLiveProbeInterval:        defaultNotLiveProbeInterval,
		HeartbeatCheckInterval:      defaultHearbeatCheckInterval,
		HeartbeatFailReportInterval: defaultHearbeatFailReportInterval,
		KafkaReconnectRetries:       defaultKafkaReconnectRetries,
	}
	return &adapterFlags
}

// ParseCommandArguments parses the arguments when running read-write adaptercore service
func (so *AdapterFlags) ParseCommandArguments() {

	help := fmt.Sprintf("Kafka - Adapter messaging host")
	flag.StringVar(&(so.KafkaAdapterHost), "kafka_adapter_host", defaultKafkaadapterhost, help)

	help = fmt.Sprintf("Kafka - Adapter messaging port")
	flag.IntVar(&(so.KafkaAdapterPort), "kafka_adapter_port", defaultKafkaadapterport, help)

	help = fmt.Sprintf("Kafka - Cluster messaging host")
	flag.StringVar(&(so.KafkaClusterHost), "kafka_cluster_host", defaultKafkaclusterhost, help)

	help = fmt.Sprintf("Kafka - Cluster messaging port")
	flag.IntVar(&(so.KafkaClusterPort), "kafka_cluster_port", defaultKafkaclusterport, help)

	help = fmt.Sprintf("Open ONT topic")
	flag.StringVar(&(so.Topic), "adapter_topic", defaultTopic, help)

	help = fmt.Sprintf("Core topic")
	flag.StringVar(&(so.CoreTopic), "core_topic", defaultCoretopic, help)

	help = fmt.Sprintf("Event topic")
	flag.StringVar(&(so.EventTopic), "event_topic", defaultEventtopic, help)

	help = fmt.Sprintf("KV store type")
	flag.StringVar(&(so.KVStoreType), "kv_store_type", defaultKvstoretype, help)

	help = fmt.Sprintf("The default timeout when making a kv store request")
	flag.IntVar(&(so.KVStoreTimeout), "kv_store_request_timeout", defaultKvstoretimeout, help)

	help = fmt.Sprintf("KV store host")
	flag.StringVar(&(so.KVStoreHost), "kv_store_host", defaultKvstorehost, help)

	help = fmt.Sprintf("KV store port")
	flag.IntVar(&(so.KVStorePort), "kv_store_port", defaultKvstoreport, help)

	help = fmt.Sprintf("Log level")
	flag.StringVar(&(so.LogLevel), "log_level", defaultLoglevel, help)

	help = fmt.Sprintf("Number of ONUs")
	flag.IntVar(&(so.OnuNumber), "onu_number", defaultOnunumber, help)

	help = fmt.Sprintf("Show startup banner log lines")
	flag.BoolVar(&(so.Banner), "banner", defaultBanner, help)

	help = fmt.Sprintf("Show version information and exit")
	flag.BoolVar(&(so.DisplayVersionOnly), "version", defaultDisplayVersionOnly, help)

	help = fmt.Sprintf("The address on which to listen to answer liveness and readiness probe queries over HTTP.")
	flag.StringVar(&(so.ProbeHost), "probe_host", defaultProbeHost, help)

	help = fmt.Sprintf("The port on which to listen to answer liveness and readiness probe queries over HTTP.")
	flag.IntVar(&(so.ProbePort), "probe_port", defaultProbePort, help)

	help = fmt.Sprintf("Number of seconds for the default liveliness check")
	flag.DurationVar(&(so.LiveProbeInterval), "live_probe_interval", defaultLiveProbeInterval, help)

	help = fmt.Sprintf("Number of seconds for liveliness check if probe is not running")
	flag.DurationVar(&(so.NotLiveProbeInterval), "not_live_probe_interval", defaultNotLiveProbeInterval, help)

	help = fmt.Sprintf("Number of seconds for heartbeat check interval.")
	flag.DurationVar(&(so.HeartbeatCheckInterval), "hearbeat_check_interval", defaultHearbeatCheckInterval, help)

	help = fmt.Sprintf("Number of seconds adapter has to wait before reporting core on the hearbeat check failure.")
	flag.DurationVar(&(so.HeartbeatFailReportInterval), "hearbeat_fail_interval", defaultHearbeatFailReportInterval, help)

	help = fmt.Sprintf("Number of retries to connect to Kafka.")
	flag.IntVar(&(so.KafkaReconnectRetries), "kafka_reconnect_retries", defaultKafkaReconnectRetries, help)

	flag.Parse()
	containerName := getContainerInfo()
	if len(containerName) > 0 {
		so.InstanceID = containerName
	}

}

func getContainerInfo() string {
	return os.Getenv("HOSTNAME")
}
