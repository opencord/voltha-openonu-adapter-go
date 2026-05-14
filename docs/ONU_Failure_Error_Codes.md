# ONU Failure Event Error Codes

## Overview

Failure events raised by the openonu adapter now include a structured `error-code` field in
addition to the free-text description. Each failure detection point in the adapter is assigned
a unique, self-documenting error code so that event consumers (NMS, ONOS, test frameworks)
can programmatically identify the root cause without parsing description strings.

Error codes are carried in the `DeviceEvent.Context` map under the key **`error-code`** and
are also embedded in the `Description` field for human readability.

## Affected Failure Events

The error codes apply to the following Kafka device events:

| Event Name | Description |
|---|---|
| `ONU_INITIALIZATION_FAILED` | ONU failed during startup, MIB sync, MIB download, or port setup |
| `ONU_DEVICE_UPDATE_FAILED` | Device object or device state update to rw-core failed |
| `ONU_DEVICE_DB_UPDATE_FAILURE` | Persistent KV store update for ONU data failed |

## Error Code Reference

| Error Code | Go Constant | Event | Trigger |
|---|---|---|---|
| `ERR_DEVICE_STATE_TRANSITION_FAILED` | `ErrCodeDeviceStateTransitionFailed` | `ONU_INITIALIZATION_FAILED` | Device FSM entered the fail state |
| `ERR_MIB_RESET_FAILED` | `ErrCodeMibResetFailed` | `ONU_INITIALIZATION_FAILED` | MIB reset command failed during initial sync |
| `ERR_PORT_STATE_UPDATE_FAILED` | `ErrCodePortStateUpdateFailed` | `ONU_INITIALIZATION_FAILED` | UNI port state update to rw-core returned unavailable |
| `ERR_CORE_UNAVAILABLE` | `ErrCodeCoreUnavailable` | `ONU_INITIALIZATION_FAILED` | rw-core unreachable when sending ONU oper-state event |
| `ERR_UNI_PORT_CREATION_FAILED` | `ErrCodeUniPortCreationFailed` | `ONU_INITIALIZATION_FAILED` | UNI port creation at rw-core exhausted retries |
| `ERR_UNI_OMCI_TIMEOUT` | `ErrCodeUniOmciTimeout` | `ONU_INITIALIZATION_FAILED` | OMCI request timed out during UNI port lock/unlock |
| `ERR_UNI_OMCI_RESPONSE_ERROR` | `ErrCodeUniOmciResponseError` | `ONU_INITIALIZATION_FAILED` | OMCI response error during UNI port lock/unlock |
| `ERR_MIB_DOWNLOAD_FAILED` | `ErrCodeMibDownloadFailed` | `ONU_INITIALIZATION_FAILED` | MIB download FSM failed and entered reset state |
| `ERR_MIB_UPLOAD_FAILED` | `ErrCodeMibUploadFailed` | `ONU_INITIALIZATION_FAILED` | MIB upload could not be completed for the device |
| `ERR_DEVICE_UPDATE_AT_CORE` | `ErrCodeDeviceUpdateAtCore` | `ONU_DEVICE_UPDATE_FAILED` | DeviceUpdate RPC to rw-core returned error |
| `ERR_DEVICE_STATE_UPDATE_AT_CORE` | `ErrCodeDeviceStateUpdateAtCore` | `ONU_DEVICE_UPDATE_FAILED` | DeviceStateUpdate RPC to rw-core returned error |
| `ERR_DEVICE_DB_KV_STORE_UPDATE` | `ErrCodeDeviceDbKvStoreUpdate` | `ONU_DEVICE_DB_UPDATE_FAILURE` | Persisting ONU data to KV store failed |

## Kafka Event Structure

When a failure event is published to Kafka, the `DeviceEvent` payload has the following shape:

```
DeviceEvent {
  ResourceId:      "<device-id>"
  DeviceEventName: "ONU_INITIALIZATION_FAILED"
  Description:     "ONU Event - ONU_INITIALIZATION_FAILED - Raised - ErrorCode: ERR_MIB_UPLOAD_FAILED - Reason: Unable to complete the MIB upload for the device"
  Context: {
    "pon-id":             "0"
    "onu-id":             "1"
    "serial-number":      "BBSM00000001"
    "olt-serial-number":  "BBSIM_OLT_0"
    "device-id":          "abcdef1234"
    "registration-id":    "abcdef1234"
    "parent-id":          "parent1234"
    "onu-device-id":      "abcdef1234"
    "num-of-unis":        "1"
    "error-code":         "ERR_MIB_UPLOAD_FAILED"
    "failure-reason":     "Unable to complete the MIB upload for the device"
  }
}
```

## Design Notes

- **Type**: `OnuFailureErrorCode` (typed `string` alias) defined in `internal/pkg/common/defines.go`.
- **Scope**: Error codes are adapter-local, consistent with how all adapter-specific event names
  and context keys are defined in the VOLTHA ecosystem. The `DeviceEvent` proto uses a
  `map<string,string>` context — there is no proto-level error code enum.
- **Naming**: Codes use descriptive uppercase strings (e.g., `ERR_MIB_RESET_FAILED`) rather than
  numeric identifiers, making them self-documenting on the wire without requiring a lookup table.
- **Extensibility**: New failure detection points are added by defining a new `ErrCodeXxx` constant
  in `defines.go` and passing it at the call site.