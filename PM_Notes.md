# Notes on Performance metrics module in openonu-go adapter
Openonu-go adapter supports performance metrics configuration and collection from the ONUs.
There are two types of performance metrics
- Group metrics
- Standalone metrics

The group metrics are managed as a group, meaning we can only set sampling frequency, disable/enable group as a whole and not individual metrics within the group. While in contrast the standalone metrics can be managed individually.

The performance metrics configuration is carried out via the [PmConfigs](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L61) protobuf messages.

The meaning of various information elements in the `PmConfigs` and its default value (if applicable) for openonu-go adapter is documented in the below table.

| Key      | Meaning                  | Default for openonu-go | Additional notes|
| :------: | :----------------------- | :--------------------- | :---------|
| id       | onu device id | onu device id|
| default_freq| Default sampling rate| 15 * 60 seconds | Applicable only when `freq_override` is set to false |
| grouped| Forces group names and group semantics| True | When this is set to true, it forces group names and group semantics. This is a `READ ONLY` attribute and cannot be changed from NBI|
|freq_override | Allows Pm to set an individual sample frequency | True| When this is set to true, the group specific sampling rate comes into effect. This is a `READ ONLY` attribute and cannot be changed from NBI|
| groups| The groups if grouped is true| | More details about the groups supported by openonu-go adapter is documented further in this notes.|
|metrics| The metrics themselves if grouped is false.| | Unsupported at the moment|
| max_skew| How long the next sampling is skewed| 0 | openonu-go adapter metrics collection does not skew metric collection |

## Group Metrics

The following group metrics are supported by openonu-go adapter.

### _OpticalPower_
```
// OpticalPowerGroupMetrics are supported optical pm names
var OpticalPowerGroupMetrics = map[string]voltha.PmConfig_PmType{
	"ani_g_instance_id":  voltha.PmConfig_CONTEXT,
	"transmit_power_dBm": voltha.PmConfig_GAUGE,
	"receive_power_dBm":  voltha.PmConfig_GAUGE,
}
```

### _UniStatus_
```
// UniStatusGroupMetrics are supported UNI status names
var UniStatusGroupMetrics = map[string]voltha.PmConfig_PmType{
	"uni_port_no":     voltha.PmConfig_CONTEXT,
	"ethernet_type":   voltha.PmConfig_GAUGE,
	"oper_status":     voltha.PmConfig_GAUGE,
	"uni_admin_state": voltha.PmConfig_GAUGE,
}
```

### _EthernetBridgeHistory_
```
var EthernetBridgeHistory = map[string]voltha.PmConfig_PmType{
	"class_id":          voltha.PmConfig_CONTEXT,
	"entity_id":         voltha.PmConfig_CONTEXT,
	"interval_end_time": voltha.PmConfig_CONTEXT,
	"parent_class_id":   voltha.PmConfig_CONTEXT,
	"parent_entity_id":  voltha.PmConfig_CONTEXT,
	"upstream":          voltha.PmConfig_CONTEXT,

	"drop_events":         voltha.PmConfig_COUNTER,
	"octets":              voltha.PmConfig_COUNTER,
	"packets":             voltha.PmConfig_COUNTER,
	"broadcast_packets":   voltha.PmConfig_COUNTER,
	"multicast_packets":   voltha.PmConfig_COUNTER,
	"crc_errored_packets": voltha.PmConfig_COUNTER,
	"undersize_packets":   voltha.PmConfig_COUNTER,
	"oversize_packets":    voltha.PmConfig_COUNTER,
	"64_octets":           voltha.PmConfig_COUNTER,
	"65_to_127_octets":    voltha.PmConfig_COUNTER,
	"128_to_255_octets":   voltha.PmConfig_COUNTER,
	"256_to_511_octets":   voltha.PmConfig_COUNTER,
	"512_to_1023_octets":  voltha.PmConfig_COUNTER,
	"1024_to_1518_octets": voltha.PmConfig_COUNTER,
}
```

### _EthernetUniHistory_
```
var EthernetUniHistory = map[string]voltha.PmConfig_PmType{
	"class_id":          voltha.PmConfig_CONTEXT,
	"entity_id":         voltha.PmConfig_CONTEXT,
	"interval_end_time": voltha.PmConfig_CONTEXT,

	"fcs_errors":                        voltha.PmConfig_COUNTER,
	"excessive_collision_counter":       voltha.PmConfig_COUNTER,
	"late_collision_counter":            voltha.PmConfig_COUNTER,
	"frames_too_long":                   voltha.PmConfig_COUNTER,
	"buffer_overflows_on_rx":            voltha.PmConfig_COUNTER,
	"buffer_overflows_on_tx":            voltha.PmConfig_COUNTER,
	"single_collision_frame_counter":    voltha.PmConfig_COUNTER,
	"multiple_collisions_frame_counter": voltha.PmConfig_COUNTER,
	"sqe_counter":                       voltha.PmConfig_COUNTER,
	"deferred_tx_counter":               voltha.PmConfig_COUNTER,
	"internal_mac_tx_error_counter":     voltha.PmConfig_COUNTER,
	"carrier_sense_error_counter":       voltha.PmConfig_COUNTER,
	"alignment_error_counter":           voltha.PmConfig_COUNTER,
	"internal_mac_rx_error_counter":     voltha.PmConfig_COUNTER,
}
```

### _FecHistory_
```
var FecHistory = map[string]voltha.PmConfig_PmType{
	"class_id":          voltha.PmConfig_CONTEXT,
	"entity_id":         voltha.PmConfig_CONTEXT,
	"interval_end_time": voltha.PmConfig_CONTEXT,

	"corrected_bytes":          voltha.PmConfig_COUNTER,
	"corrected_code_words":     voltha.PmConfig_COUNTER,
	"uncorrectable_code_words": voltha.PmConfig_COUNTER,
	"total_code_words":         voltha.PmConfig_COUNTER,
	"fec_seconds":              voltha.PmConfig_COUNTER,
}
```

### _GemPortHistory_
```
var GemPortHistory = map[string]voltha.PmConfig_PmType{
	"class_id":          voltha.PmConfig_CONTEXT,
	"entity_id":         voltha.PmConfig_CONTEXT,
	"interval_end_time": voltha.PmConfig_CONTEXT,

	"transmitted_gem_frames":    voltha.PmConfig_COUNTER,
	"received_gem_frames":       voltha.PmConfig_COUNTER,
	"received_payload_bytes":    voltha.PmConfig_COUNTER,
	"transmitted_payload_bytes": voltha.PmConfig_COUNTER,
	"encryption_key_errors":     voltha.PmConfig_COUNTER,
}
```

## Basic KPI Format (**KpiEvent2**)

The KPI information is published on the kafka bus under the _voltha.events_ topic. For
VOLTHA PM information, the kafka key is empty and the value is a JSON message composed
of the following key-value pairs.

| key        | value  | Notes |
| :--------: | :----- | :---- |
| type       | string | "slice" or "ts". A "slice" is a set of path/metric data for the same time-stamp. A "ts" is a time-series: array of data for same metric |
| ts         | float  | UTC time-stamp of when the KpiEvent2 was created (seconds since the epoch of January 1, 1970) |
| slice_data | list   | One or more sets of metrics composed of a _metadata_ section and a _metrics_ section. |

**NOTE**: Time-series metrics and corresponding protobuf messages have not been defined.

## Slice Data Format

For KPI slice KPI messages, the _slice_data_ portion of the **KpiEvent2** is composed of a _metadata_ section and a _metrics_ section.

### _metadata_ Section Format

The metadata section is used to:
 - Define which metric/metric-group is being reported (The _title_ field)
 - Provide some common fields required by all metrics (_title_, _timestamp_, _device ID_, ...)
 - Provide metric/metric-group specific context (the _context_ fields)

| key        | value  | Notes |
| :--------: | :----- | :---- |
| title       | string | "slice" or "ts". A "slice" is a set of path/metric data for the same time-stamp. A "ts" is a time-series: array of data for same metric |
| ts         | float | UTC time-stamp of data at the time of collection (seconds since the epoch of January 1, 1970) |
| logical_device_id | string | The logical ID that the device belongs to. This is equivalent to the DPID reported in ONOS for the VOLTHA logical device with the 'of:' prefix removed. |
| device_id | string | The physical device ID that is reporting the metric. |
| serial_number | string | The reported serial number for the physical device reporting the metric. |
| context | map | A key-value map of metric/metric-group specific information.|

The context map is composed of key-value pairs where the key (string) is the label for the context
specific value and the value (string) is the corresponding context value. While most values may be
better represented as a float/integer, there may be some that are better represented as text. For
this reason, values are always represented as strings to allow the ProtoBuf message format to be
as simple as possible.

Here is an JSON _example_ of a current KPI published on the kafka bus under the
_voltha.events_ topic.

```json
   "kpiEvent2":{
      "type":"slice",
      "ts":1611605902528748000,
      "sliceData":[
         {
            "metadata":{
               "title":"OpticalPower",
               "ts":1611605902515836000,
               "logicalDeviceId":"1eda0422-f1cc-42e6-9a23-08039e7cdf43",
               "serialNo":"ALPHe3d1cf70",
               "deviceId":"1eda0422-f1cc-42e6-9a23-08039e7cdf43",
               "context":{
                  "devicetype":"brcm_openomci_onu",
                  "intfID":"0",
                  "onuID":"1"
               },
               "uuid":""
            },
            "metrics":{
               "ani_g_instance_id":32769,
               "receive_power":57645,
               "transmit_power":2748
            }
         }
      ]
   }

```

## Useful _voltctl_ commands to manage Performance Metrics

### Get PM config
```
voltctl device pmconfig get <onu-device-id>
```

### List metric groups for a device
```
voltctl device pmconfig group list <onu-device-id>
```

### List metrics within a group

```
voltctl device pmconfig groupmetric list <onu-device-id> <group-name>
```

### Set default sampling rate

```
 voltctl device pmconfig frequency set <onu-device-id> <sampling-rate>
```
Note1: Sampling rate unit is in seconds

Note2: default sample rate is applicable only if frequency override is false

Note3 : The `frequency` has to be greater than 0 and a multiple of _FrequencyGranularity_ which is currently set to 5.

### Disable a group metric

```
voltctl device pmconfig group disable <onu-device-id> <group-name>
```

### Enable a group metric

```
voltctl device pmconfig group enable <onu-device-id> <group-name>
```

### Set group frequency

```
voltctl device pmconfig group set <onu-device-id> <group-name> <frequency>
```
Note1 : The `frequency` has to be greater than 0 and a multiple of _FrequencyGranularity_ which is currently set to 5.

Note2 : The `frequency` of L2 PM counters is fixed at 15m and cannot be changed.

### Listen for KPI events on kafka

```
voltctl -k <kafka-ip:port> event listen --show-body -t 10000 -o json -F
```
Note: For more `event listen` options, check `voltctl event listen --help` command.

## Remaining work
The following Metrics could be supported in the future.

- xgPON_TC_History
- xgPON_Downstream_History
- xgPON_Upstream_History
