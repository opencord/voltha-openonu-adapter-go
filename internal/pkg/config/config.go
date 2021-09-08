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
	"os"
	"time"
)

// Open ONU default constants
const (
	EtcdStoreName = "etcd"
	OnuVendorIds  = "OPEN,ALCL,BRCM,TWSH,ALPH,ISKT,SFAA,BBSM,SCOM,ARPX,DACM,ERSN,HWTC,CIGG,ADTN,ARCA,AVMG"
)

// AdapterFlags represents the set of configurations used by the read-write adaptercore service
type AdapterFlags struct {
	// Command line parameters
	InstanceID                  string
	KafkaClusterAddress         string // NOTE this is unused across the adapter
	KVStoreType                 string
	KVStoreTimeout              time.Duration
	KVStoreAddress              string
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
	MaxTimeoutReconciling       time.Duration
	TraceEnabled                bool
	TraceAgentAddress           string
	LogCorrelationEnabled       bool
	OnuVendorIds                string
	MetricsEnabled              bool
	MibAuditInterval            time.Duration
	OmciTimeout                 time.Duration
	AlarmAuditInterval          time.Duration
	DownloadToAdapterTimeout    time.Duration
	DownloadToOnuTimeout4MB     time.Duration
	UniPortMask                 int
	MinBackoffRetryDelay        time.Duration
	MaxBackoffRetryDelay        time.Duration
	AdapterEndpoint             string
	GrpcAddress                 string
	CoreEndpoint                string
	RPCTimeout                  time.Duration
	MaxConcurrentFlowsPerUni    int
}

// ParseCommandArguments parses the arguments when running read-write adaptercore service
func (so *AdapterFlags) ParseCommandArguments(args []string) {

	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	fs.StringVar(&(so.KafkaClusterAddress),
		"kafka_cluster_address",
		"127.0.0.1:9092",
		"Kafka - Cluster messaging address")

	fs.StringVar(&(so.EventTopic),
		"event_topic",
		"voltha.events",
		"Event topic")

	fs.StringVar(&(so.KVStoreType),
		"kv_store_type",
		EtcdStoreName,
		"KV store type")

	fs.DurationVar(&(so.KVStoreTimeout),
		"kv_store_request_timeout",
		5*time.Second,
		"The default timeout when making a kv store request")

	fs.StringVar(&(so.KVStoreAddress),
		"kv_store_address",
		"127.0.0.1:2379",
		"KV store address")

	fs.StringVar(&(so.LogLevel),
		"log_level",
		"WARN",
		"Log level")

	fs.IntVar(&(so.OnuNumber),
		"onu_number",
		1,
		"Number of ONUs")

	fs.BoolVar(&(so.Banner),
		"banner",
		false,
		"Show startup banner log lines")

	fs.BoolVar(&(so.DisplayVersionOnly),
		"version",
		false,
		"Show version information and exit")

	fs.BoolVar(&(so.AccIncrEvto),
		"accept_incr_evto",
		false,
		"Acceptance of incremental EVTOCD configuration")

	fs.StringVar(&(so.ProbeHost),
		"probe_host",
		"",
		"The address on which to listen to answer liveness and readiness probe queries over HTTP")

	fs.IntVar(&(so.ProbePort),
		"probe_port",
		8080,
		"The port on which to listen to answer liveness and readiness probe queries over HTTP")

	fs.DurationVar(&(so.LiveProbeInterval),
		"live_probe_interval",
		60*time.Second,
		"Number of seconds for the default liveliness check")

	fs.DurationVar(&(so.NotLiveProbeInterval),
		"not_live_probe_interval",
		60*time.Second,
		"Number of seconds for liveliness check if probe is not running")

	fs.DurationVar(&(so.HeartbeatCheckInterval),
		"hearbeat_check_interval",
		30*time.Second,
		"Number of seconds for heartbeat check interval")

	fs.DurationVar(&(so.HeartbeatFailReportInterval),
		"hearbeat_fail_interval",
		30*time.Second,
		"Number of seconds adapter has to wait before reporting core on the hearbeat check failure")

	fs.IntVar(&(so.KafkaReconnectRetries),
		"kafka_reconnect_retries",
		-1,
		"Number of retries to connect to Kafka")

	fs.IntVar(&(so.CurrentReplica),
		"current_replica",
		1,
		"Replica number of this particular instance")

	fs.IntVar(&(so.TotalReplicas),
		"total_replica",
		1,
		"Total number of instances for this adapter")

	fs.DurationVar(&(so.MaxTimeoutInterAdapterComm),
		"max_timeout_interadapter_comm",
		30*time.Second,
		"Maximum Number of seconds for the default interadapter communication timeout")

	fs.DurationVar(&(so.MaxTimeoutReconciling),
		"max_timeout_reconciling",
		10*time.Second,
		"Maximum Number of seconds for the default ONU reconciling timeout")

	fs.BoolVar(&(so.TraceEnabled),
		"trace_enabled",
		false,
		"Whether to send logs to tracing agent")

	fs.StringVar(&(so.TraceAgentAddress),
		"trace_agent_address",
		"127.0.0.1:6831",
		"The address of tracing agent to which span info should be sent")

	fs.BoolVar(&(so.LogCorrelationEnabled),
		"log_correlation_enabled",
		true,
		"Whether to enrich log statements with fields denoting operation being executed for achieving correlation")

	fs.StringVar(&(so.OnuVendorIds),
		"allowed_onu_vendors",
		OnuVendorIds,
		"List of Allowed ONU Vendor Ids")

	fs.BoolVar(&(so.MetricsEnabled),
		"metrics_enabled",
		false,
		"Whether to enable metrics collection")

	fs.DurationVar(&(so.MibAuditInterval),
		"mib_audit_interval",
		300*time.Second,
		"Mib Audit Interval in seconds - the value zero will disable Mib Audit")

	fs.DurationVar(&(so.OmciTimeout),
		"omci_timeout",
		3*time.Second,
		"OMCI timeout duration - this timeout value is used on the OMCI channel for waiting on response from ONU")

	fs.DurationVar(&(so.AlarmAuditInterval),
		"alarm_audit_interval",
		300*time.Second,
		"Alarm Audit Interval in seconds - the value zero will disable alarm audit")

	fs.DurationVar(&(so.DownloadToAdapterTimeout),
		"download_to_adapter_timeout",
		10*time.Second,
		"File download to adapter timeout in seconds")

	fs.DurationVar(&(so.DownloadToOnuTimeout4MB),
		"download_to_onu_timeout_4MB",
		60*time.Minute,
		"File download to ONU timeout in minutes for a block of 4MB")

	//Mask to indicate which possibly active ONU UNI state  is really reported to the core
	// compare python code - at the moment restrict active state to the first ONU UNI port
	// check is limited to max 16 uni ports - cmp above UNI limit!!!
	fs.IntVar(&(so.UniPortMask),
		"uni_port_mask",
		0x0001,
		"The bitmask to identify UNI ports that need to be enabled")

	fs.StringVar(&(so.GrpcAddress),
		"grpc_address",
		":50060",
		"Adapter GRPC Server address")

	fs.StringVar(&(so.CoreEndpoint),
		"core_endpoint",
		":55555",
		"Core endpoint")

	fs.StringVar(&(so.AdapterEndpoint),
		"adapter_endpoint",
		"",
		"Adapter Endpoint")

	fs.DurationVar(&(so.RPCTimeout),
		"rpc_timeout",
		10*time.Second,
		"The default timeout when making an RPC request")

	fs.DurationVar(&(so.MinBackoffRetryDelay),
		"min_retry_delay",
		500*time.Millisecond,
		"The minimum number of milliseconds to delay before a connection retry attempt")

	fs.DurationVar(&(so.MaxBackoffRetryDelay),
		"max_retry_delay",
		10*time.Second,
		"The maximum number of milliseconds to delay before a connection retry attempt")
	fs.IntVar(&(so.MaxConcurrentFlowsPerUni),
		"max_concurrent_flows_per_uni",
		16,
		"The max number of concurrent flows (add/remove) that can be queued per UNI")

	_ = fs.Parse(args)
	containerName := getContainerInfo()
	if len(containerName) > 0 {
		so.InstanceID = containerName
	} else {
		so.InstanceID = "openonu"
	}

}

func getContainerInfo() string {
	return os.Getenv("HOSTNAME")
}
