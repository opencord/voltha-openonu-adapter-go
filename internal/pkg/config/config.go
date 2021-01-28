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
)

// Open ONU default constants
const (
	etcdStoreName               = "etcd"
	defaultInstanceid           = "openonu"
	defaultKafkaadapteraddress  = "127.0.0.1:9092"
	defaultKafkaclusteraddress  = "127.0.0.1:9092"
	defaultKvstoretype          = etcdStoreName
	defaultKvstoretimeout       = 5 * time.Second
	defaultKvstoreaddress       = "127.0.0.1:2379"
	defaultLoglevel             = "WARN"
	defaultBanner               = false
	defaultDisplayVersionOnly   = false
	defaultAccIncrEvto          = false
	defaultTopic                = "openonu"
	defaultCoreTopic            = "rwcore"
	defaultEventTopic           = "voltha.events"
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
	defaultKafkaReconnectRetries      = -1
	defaultCurrentReplica             = 1
	defaultTotalReplicas              = 1
	defaultMaxTimeoutInterAdapterComm = 30 * time.Second
	defaultOnuVendorIds               = "OPEN,ALCL,BRCM,TWSH,ALPH,ISKT,SFAA,BBSM,SCOM,ARPX,DACM,ERSN,HWTC,CIGG,ADTN,ARCA,AVMG"

	// For Tracing
	defaultTraceEnabled          = false
	defaultTraceAgentAddress     = "127.0.0.1:6831"
	defaultLogCorrelationEnabled = true
)

// AdapterFlags represents the set of configurations used by the read-write adaptercore service
type AdapterFlags struct {
	// Command line parameters
	InstanceID                  string
	KafkaAdapterAddress         string
	KafkaClusterAddress         string // NOTE this is unused across the adapter
	KVStoreType                 string
	KVStoreTimeout              time.Duration
	KVStoreAddress              string
	Topic                       string
	CoreTopic                   string
	EventTopic                  string
	LogLevel                    string
	OnuNumber                   int
	Banner                      bool
	DisplayVersionOnly          bool
	AccIncrEvto                 bool
	ProbeHost                   string
	ProbePort                   int
	LiveProbeInterval           time.Duration
	NotLiveProbeInterval        time.Duration
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	KafkaReconnectRetries       int
	CurrentReplica              int
	TotalReplicas               int
	MaxTimeoutInterAdapterComm  time.Duration
	TraceEnabled                bool
	TraceAgentAddress           string
	LogCorrelationEnabled       bool
	OnuVendorIds                string
}

// NewAdapterFlags returns a new RWCore config
func NewAdapterFlags() *AdapterFlags {
	var adapterFlags = AdapterFlags{ // Default values
		InstanceID:                  defaultInstanceid,
		KafkaAdapterAddress:         defaultKafkaadapteraddress,
		KafkaClusterAddress:         defaultKafkaclusteraddress,
		KVStoreType:                 defaultKvstoretype,
		KVStoreTimeout:              defaultKvstoretimeout,
		KVStoreAddress:              defaultKvstoreaddress,
		Topic:                       defaultTopic,
		CoreTopic:                   defaultCoreTopic,
		EventTopic:                  defaultEventTopic,
		LogLevel:                    defaultLoglevel,
		OnuNumber:                   defaultOnunumber,
		Banner:                      defaultBanner,
		DisplayVersionOnly:          defaultDisplayVersionOnly,
		AccIncrEvto:                 defaultAccIncrEvto,
		ProbeHost:                   defaultProbeHost,
		ProbePort:                   defaultProbePort,
		LiveProbeInterval:           defaultLiveProbeInterval,
		NotLiveProbeInterval:        defaultNotLiveProbeInterval,
		HeartbeatCheckInterval:      defaultHearbeatCheckInterval,
		HeartbeatFailReportInterval: defaultHearbeatFailReportInterval,
		KafkaReconnectRetries:       defaultKafkaReconnectRetries,
		CurrentReplica:              defaultCurrentReplica,
		TotalReplicas:               defaultTotalReplicas,
		MaxTimeoutInterAdapterComm:  defaultMaxTimeoutInterAdapterComm,
		TraceEnabled:                defaultTraceEnabled,
		TraceAgentAddress:           defaultTraceAgentAddress,
		LogCorrelationEnabled:       defaultLogCorrelationEnabled,
		OnuVendorIds:                defaultOnuVendorIds,
	}
	return &adapterFlags
}

// ParseCommandArguments parses the arguments when running read-write adaptercore service
func (so *AdapterFlags) ParseCommandArguments() {

	help := fmt.Sprintf("Kafka - Adapter messaging address")
	flag.StringVar(&(so.KafkaAdapterAddress), "kafka_adapter_address", defaultKafkaadapteraddress, help)

	help = fmt.Sprintf("Kafka - Cluster messaging address")
	flag.StringVar(&(so.KafkaClusterAddress), "kafka_cluster_address", defaultKafkaclusteraddress, help)

	help = fmt.Sprintf("Open ONU topic")
	baseAdapterTopic := flag.String("adapter_topic", defaultTopic, help)

	help = fmt.Sprintf("Core topic")
	flag.StringVar(&(so.CoreTopic), "core_topic", defaultCoreTopic, help)

	help = fmt.Sprintf("Event topic")
	flag.StringVar(&(so.EventTopic), "event_topic", defaultEventTopic, help)

	help = fmt.Sprintf("KV store type")
	flag.StringVar(&(so.KVStoreType), "kv_store_type", defaultKvstoretype, help)

	help = fmt.Sprintf("The default timeout when making a kv store request")
	flag.DurationVar(&(so.KVStoreTimeout), "kv_store_request_timeout", defaultKvstoretimeout, help)

	help = fmt.Sprintf("KV store address")
	flag.StringVar(&(so.KVStoreAddress), "kv_store_address", defaultKvstoreaddress, help)

	help = fmt.Sprintf("Log level")
	flag.StringVar(&(so.LogLevel), "log_level", defaultLoglevel, help)

	help = fmt.Sprintf("Number of ONUs")
	flag.IntVar(&(so.OnuNumber), "onu_number", defaultOnunumber, help)

	help = fmt.Sprintf("Show startup banner log lines")
	flag.BoolVar(&(so.Banner), "banner", defaultBanner, help)

	help = fmt.Sprintf("Show version information and exit")
	flag.BoolVar(&(so.DisplayVersionOnly), "version", defaultDisplayVersionOnly, help)

	help = fmt.Sprintf("Acceptance of incremental EVTOCD configuration")
	flag.BoolVar(&(so.AccIncrEvto), "accept_incr_evto", defaultAccIncrEvto, help)

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

	help = "Replica number of this particular instance (default: %s)"
	flag.IntVar(&(so.CurrentReplica), "current_replica", defaultCurrentReplica, help)

	help = "Total number of instances for this adapter"
	flag.IntVar(&(so.TotalReplicas), "total_replica", defaultTotalReplicas, help)

	help = fmt.Sprintf("Maximum Number of seconds for the default interadapter communication timeout")
	flag.DurationVar(&(so.MaxTimeoutInterAdapterComm), "max_timeout_interadapter_comm",
		defaultMaxTimeoutInterAdapterComm, help)

	help = fmt.Sprintf("Whether to send logs to tracing agent?")
	flag.BoolVar(&(so.TraceEnabled), "trace_enabled", defaultTraceEnabled, help)

	help = fmt.Sprintf("The address of tracing agent to which span info should be sent.")
	flag.StringVar(&(so.TraceAgentAddress), "trace_agent_address", defaultTraceAgentAddress, help)

	help = fmt.Sprintf("Whether to enrich log statements with fields denoting operation being executed for achieving correlation?")
	flag.BoolVar(&(so.LogCorrelationEnabled), "log_correlation_enabled", defaultLogCorrelationEnabled, help)

	help = fmt.Sprintf("List of Allowed ONU Vendor Ids")
	flag.StringVar(&(so.OnuVendorIds), "allowed_onu_vendors", defaultOnuVendorIds, help)

	flag.Parse()
	containerName := getContainerInfo()
	if len(containerName) > 0 {
		so.InstanceID = containerName
	}

	so.Topic = fmt.Sprintf("%s_%d", *baseAdapterTopic, int32(so.CurrentReplica))

}

func getContainerInfo() string {
	return os.Getenv("HOSTNAME")
}
