/*
 * Copyright 2021-present Open Networking Foundation

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

//Package adaptercoreonu provides the utility for onu devices, flows and statistics
package adaptercoreonu

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/db"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

const (
	// events of L2 PM FSM
	l2PmEventInit     = "l2PmEventInit"
	l2PmEventTick     = "l2PmEventTick"
	l2PmEventSuccess  = "l2PmEventSuccess"
	l2PmEventFailure  = "l2PmEventFailure"
	l2PmEventAddMe    = "l2PmEventAddMe"
	l2PmEventDeleteMe = "l2PmEventDeleteMe"
	l2PmEventStop     = "l2PmEventStop"
)
const (
	// states of L2 PM FSM
	l2PmStNull        = "l2PmStNull"
	l2PmStStarting    = "l2PmStStarting"
	l2PmStSyncTime    = "l2PmStSyncTime"
	l2PmStIdle        = "l2PmStIdle"
	l2PmStCreatePmMe  = "l2PmStCreatePm"
	l2PmStDeletePmMe  = "l2PmStDeletePmMe"
	l2PmStCollectData = "l2PmStCollectData"
)

const cL2PmFsmIdleState = l2PmStIdle

// general constants used for overall Metric Collection management
const (
	DefaultMetricCollectionFrequency = 15 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration
	GroupMetricEnabled               = true    // This is READONLY and cannot be changed from VOLTHA NBI
	DefaultFrequencyOverrideEnabled  = true    // This is READONLY and cannot be changed from VOLTHA NBI
	FrequencyGranularity             = 5       // The frequency (in seconds) has to be multiple of 5. This setting cannot changed later.
)

// OpticalPowerGroupMetrics are supported optical pm names
var OpticalPowerGroupMetrics = map[string]voltha.PmConfig_PmType{
	"ani_g_instance_id":  voltha.PmConfig_CONTEXT,
	"transmit_power_dBm": voltha.PmConfig_GAUGE,
	"receive_power_dBm":  voltha.PmConfig_GAUGE,
}

// OpticalPowerGroupMetrics specific constants
const (
	OpticalPowerGroupMetricName                = "PON_Optical"
	OpticalPowerGroupMetricEnabled             = true   // This setting can be changed from voltha NBI PmConfig configuration
	OpticalPowerMetricGroupCollectionFrequency = 5 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration
)

// UniStatusGroupMetrics are supported UNI status names
var UniStatusGroupMetrics = map[string]voltha.PmConfig_PmType{
	"uni_port_no":     voltha.PmConfig_CONTEXT,
	"me_class_id":     voltha.PmConfig_CONTEXT,
	"entity_id":       voltha.PmConfig_CONTEXT,
	"sensed_type":     voltha.PmConfig_GAUGE,
	"oper_status":     voltha.PmConfig_GAUGE,
	"uni_admin_state": voltha.PmConfig_GAUGE,
}

// UniStatusGroupMetrics specific constants
const (
	UniStatusGroupMetricName                = "UNI_Status"
	UniStatusGroupMetricEnabled             = true   // This setting can be changed from voltha NBI PmConfig configuration
	UniStatusMetricGroupCollectionFrequency = 5 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration
)

// *** Classical L2 PM Counters begin ***

// EthernetBridgeHistory are supported ethernet bridge history counters fetched from
// Ethernet Frame Performance Monitoring History Data Downstream and Ethernet Frame Performance Monitoring History Data Upstream MEs.
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

// EthernetUniHistory are supported ethernet uni history counters fetched from
// Ethernet Performance Monitoring History Data ME.
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

// FecHistory is supported FEC Performance Monitoring History Data related metrics
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

// GemPortHistory is supported GEM Port Network Ctp Performance Monitoring History Data
// related metrics
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

// Constants specific for L2 PM collection
const (
	L2PmCollectionInterval = 15 * 60 // Unit in seconds. Do not change this as this fixed by OMCI specification for L2 PM counters
	SyncTimeRetryInterval  = 15      // Unit seconds
	L2PmCreateAttempts     = 3
	L2PmDeleteAttempts     = 3
	L2PmCollectAttempts    = 3
	// Per Table 11.2.9-1 – OMCI baseline message limitations in G.988 spec, the max GET Response
	// payload size is 25. We define 24 (one less) to allow for dynamic insertion of IntervalEndTime
	// attribute (1 byte) in L2 PM GET Requests.
	MaxL2PMGetPayLoadSize = 24
)

// EthernetUniHistoryName specific constants
const (
	EthernetBridgeHistoryName      = "Ethernet_Bridge_Port_History"
	EthernetBridgeHistoryEnabled   = true // This setting can be changed from voltha NBI PmConfig configuration
	EthernetBridgeHistoryFrequency = L2PmCollectionInterval
)

// EthernetBridgeHistory specific constants
const (
	EthernetUniHistoryName      = "Ethernet_UNI_History"
	EthernetUniHistoryEnabled   = true // This setting can be changed from voltha NBI PmConfig configuration
	EthernetUniHistoryFrequency = L2PmCollectionInterval
)

// FecHistory specific constants
const (
	FecHistoryName      = "FEC_History"
	FecHistoryEnabled   = true // This setting can be changed from voltha NBI PmConfig configuration
	FecHistoryFrequency = L2PmCollectionInterval
)

// GemPortHistory specific constants
const (
	GemPortHistoryName      = "GEM_Port_History"
	GemPortHistoryEnabled   = true // This setting can be changed from voltha NBI PmConfig configuration
	GemPortHistoryFrequency = L2PmCollectionInterval
)

// KV Store related constants
const (
	cPmKvStorePrefix = "%s/openonu/pm-data/%s" // <some-base-path>/openonu/pm-data/<onu-device-id>
	cPmAdd           = "add"
	cPmAdded         = "added"
	cPmRemove        = "remove"
	cPmRemoved       = "removed"
)

// Defines the type for generic metric population function
type groupMetricPopulateFunc func(context.Context, me.ClassID, uint16, me.AttributeValueMap, me.AttributeValueMap, map[string]float32, *int) error

// *** Classical L2 PM Counters end   ***

type pmMEData struct {
	InstancesActive   []uint16 `json:"instances_active"`    // list of active ME instance IDs for the group
	InstancesToDelete []uint16 `json:"instances_to_delete"` // list of ME instance IDs marked for deletion for the group
	InstancesToAdd    []uint16 `json:"instances_to_add"`    // list of ME instance IDs marked for addition for the group
}

type groupMetric struct {
	groupName              string
	enabled                bool
	frequency              uint32 // valid only if FrequencyOverride is enabled.
	metricMap              map[string]voltha.PmConfig_PmType
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
	isL2PMCounter          bool      // true for only L2 PM counters
	collectAttempts        uint32    // number of attempts to collect L2 PM data
	pmMEData               *pmMEData
}

type standaloneMetric struct {
	metricName             string
	enabled                bool
	frequency              uint32    // valid only if FrequencyOverride is enabled.
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
}

type onuMetricsManager struct {
	pDeviceHandler *deviceHandler
	pAdaptFsm      *AdapterFsm

	opticalMetricsChan             chan me.AttributeValueMap
	uniStatusMetricsChan           chan me.AttributeValueMap
	l2PmChan                       chan me.AttributeValueMap
	syncTimeResponseChan           chan bool // true is success, false is fail
	l2PmCreateOrDeleteResponseChan chan bool // true is success, false is fail

	activeL2Pms  []string // list of active l2 pm MEs created on the ONU.
	l2PmToDelete []string // list of L2 PMs to delete
	l2PmToAdd    []string // list of L2 PM to add

	groupMetricMap      map[string]*groupMetric
	standaloneMetricMap map[string]*standaloneMetric

	stopProcessingOmciResponses chan bool

	stopTicks chan bool

	nextGlobalMetricCollectionTime time.Time // valid only if pmConfig.FreqOverride is set to false.

	onuMetricsManagerLock sync.RWMutex

	pmKvStore *db.Backend
}

// newonuMetricsManager returns a new instance of the newonuMetricsManager
// The metrics manager module is responsible for configuration and management of individual and group metrics.
// Currently all the metrics are managed as a group which fall into two categories - L2 PM and "all others"
// The L2 PM counters have a fixed 15min interval for PM collection while all other group counters have
// the collection interval configurable.
// The global PM config is part of the voltha.Device struct and is backed up on KV store (by rw-core).
// This module also implements resiliency for L2 PM ME instances that are active/pending-delete/pending-add.
func newonuMetricsManager(ctx context.Context, dh *deviceHandler) *onuMetricsManager {

	var metricsManager onuMetricsManager
	logger.Debugw(ctx, "init-onuMetricsManager", log.Fields{"device-id": dh.deviceID})
	metricsManager.pDeviceHandler = dh

	commMetricsChan := make(chan Message)
	metricsManager.opticalMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.uniStatusMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.l2PmChan = make(chan me.AttributeValueMap)

	metricsManager.syncTimeResponseChan = make(chan bool)
	metricsManager.l2PmCreateOrDeleteResponseChan = make(chan bool)

	metricsManager.stopProcessingOmciResponses = make(chan bool)
	metricsManager.stopTicks = make(chan bool)

	metricsManager.groupMetricMap = make(map[string]*groupMetric)
	metricsManager.standaloneMetricMap = make(map[string]*standaloneMetric)

	if dh.pmConfigs == nil { // dh.pmConfigs is NOT nil if adapter comes back from a restart. We should NOT go back to defaults in this case
		metricsManager.initializeAllGroupMetrics()
	}

	metricsManager.populateLocalGroupMetricData(ctx)

	if err := metricsManager.initializeL2PmFsm(ctx, commMetricsChan); err != nil {
		return nil
	}

	// initialize the next metric collection intervals.
	metricsManager.initializeMetricCollectionTime(ctx)

	baseKvStorePath := fmt.Sprintf(cPmKvStorePrefix, dh.pOpenOnuAc.cm.Backend.PathPrefix, dh.deviceID)
	metricsManager.pmKvStore = dh.setBackend(ctx, baseKvStorePath)
	if metricsManager.pmKvStore == nil {
		logger.Errorw(ctx, "Can't initialize pmKvStore - no backend connection to PM module",
			log.Fields{"device-id": dh.deviceID, "service": baseKvStorePath})
		return nil
	}

	logger.Info(ctx, "init-onuMetricsManager completed", log.Fields{"device-id": dh.deviceID})
	return &metricsManager
}

func (mm *onuMetricsManager) initializeMetricCollectionTime(ctx context.Context) {
	if mm.pDeviceHandler.pmConfigs.FreqOverride {
		// If mm.pDeviceHandler.pmConfigs.FreqOverride is set to true, then group/standalone metric specific interval applies
		mm.onuMetricsManagerLock.Lock()
		defer mm.onuMetricsManagerLock.Unlock()
		for _, v := range mm.groupMetricMap {
			if v.enabled && !v.isL2PMCounter { // L2 PM counter collection is managed in a L2PmFsm
				v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
			}
		}

		for _, v := range mm.standaloneMetricMap {
			if v.enabled {
				v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
			}
		}
	} else {
		// If mm.pDeviceHandler.pmConfigs.FreqOverride is set to false, then overall metric specific interval applies
		mm.nextGlobalMetricCollectionTime = time.Now().Add(time.Duration(mm.pDeviceHandler.pmConfigs.DefaultFreq) * time.Second)
	}
	logger.Infow(ctx, "initialized standalone group/metric collection time", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
}

func (mm *onuMetricsManager) updateDefaultFrequency(ctx context.Context, pmConfigs *voltha.PmConfigs) error {
	// Verify that the configured DefaultFrequency is > 0 and is a multiple of FrequencyGranularity
	if pmConfigs.DefaultFreq == 0 || (pmConfigs.DefaultFreq > 0 && pmConfigs.DefaultFreq%FrequencyGranularity != 0) {
		logger.Errorf(ctx, "frequency-%u-should-be-a-multiple-of-%u", pmConfigs.DefaultFreq, FrequencyGranularity)
		return fmt.Errorf("frequency-%d-should-be-a-multiple-of-%d", pmConfigs.DefaultFreq, FrequencyGranularity)
	}
	mm.pDeviceHandler.pmConfigs.DefaultFreq = pmConfigs.DefaultFreq
	// re-set the nextGlobalMetricCollectionTime based on the new DefaultFreq
	mm.nextGlobalMetricCollectionTime = time.Now().Add(time.Duration(mm.pDeviceHandler.pmConfigs.DefaultFreq) * time.Second)
	logger.Debugw(ctx, "frequency-updated--new-frequency", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "frequency": mm.pDeviceHandler.pmConfigs.DefaultFreq})
	return nil
}

func (mm *onuMetricsManager) updateGroupFreq(ctx context.Context, aGroupName string, pmConfigs *voltha.PmConfigs) error {
	var newGroupFreq uint32
	found := false
	groupSliceIdx := 0
	var group *voltha.PmGroupConfig
	for groupSliceIdx, group = range pmConfigs.Groups {
		if group.GroupName == aGroupName {
			// freq 0 is not allowed and it should be multiple of FrequencyGranularity
			if group.GroupFreq == 0 || (group.GroupFreq > 0 && group.GroupFreq%FrequencyGranularity != 0) {
				logger.Errorf(ctx, "frequency-%u-should-be-a-multiple-of-%u", group.GroupFreq, FrequencyGranularity)
				return fmt.Errorf("frequency-%d-should-be-a-multiple-of-%d", group.GroupFreq, FrequencyGranularity)
			}
			newGroupFreq = group.GroupFreq
			found = true
			break
		}
	}
	// if not found update group freq and next collection interval for the group
	if !found {
		logger.Errorw(ctx, "group name not found", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName})
		return fmt.Errorf("group-name-not-found-%v", aGroupName)
	}

	updated := false
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	for k, v := range mm.groupMetricMap {
		if k == aGroupName && !v.isL2PMCounter { // We cannot allow the L2 PM counter frequency to be updated. It is 15min fixed by OMCI spec
			v.frequency = newGroupFreq
			// update internal pm config
			mm.pDeviceHandler.pmConfigs.Groups[groupSliceIdx].GroupFreq = newGroupFreq
			// Also updated the next group metric collection time from now
			v.nextCollectionInterval = time.Now().Add(time.Duration(newGroupFreq) * time.Second)
			updated = true
			logger.Infow(ctx, "group frequency updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "newGroupFreq": newGroupFreq, "groupName": aGroupName})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "group frequency not updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "newGroupFreq": newGroupFreq, "groupName": aGroupName})
		return fmt.Errorf("internal-error-during-group-freq-update--groupname-%s-freq-%d", aGroupName, newGroupFreq)
	}
	return nil
}

func (mm *onuMetricsManager) updateMetricFreq(ctx context.Context, aMetricName string, pmConfigs *voltha.PmConfigs) error {
	var newMetricFreq uint32
	found := false
	metricSliceIdx := 0
	var metric *voltha.PmConfig
	for metricSliceIdx, metric = range pmConfigs.Metrics {
		if metric.Name == aMetricName {
			// freq 0 is not allowed and it should be multiple of FrequencyGranularity
			if metric.SampleFreq == 0 || (metric.SampleFreq > 0 && metric.SampleFreq%FrequencyGranularity != 0) {
				logger.Errorf(ctx, "frequency-%u-should-be-a-multiple-of-%u", metric.SampleFreq, FrequencyGranularity)
				return fmt.Errorf("frequency-%d-should-be-a-multiple-of-%d", metric.SampleFreq, FrequencyGranularity)
			}
			newMetricFreq = metric.SampleFreq
			found = true
			break
		}
	}
	if !found {
		logger.Errorw(ctx, "metric name not found", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": aMetricName})
		return fmt.Errorf("metric-name-not-found-%v", aMetricName)
	}

	updated := false
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	for k, v := range mm.groupMetricMap {
		if k == aMetricName {
			v.frequency = newMetricFreq
			// update internal pm config
			mm.pDeviceHandler.pmConfigs.Metrics[metricSliceIdx].SampleFreq = newMetricFreq
			// Also updated the next standalone metric collection time from now
			v.nextCollectionInterval = time.Now().Add(time.Duration(newMetricFreq) * time.Second)
			updated = true
			logger.Infow(ctx, "metric frequency updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "newMetricFreq": newMetricFreq, "aMetricName": aMetricName})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "metric frequency not updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "newMetricFreq": newMetricFreq, "aMetricName": aMetricName})
		return fmt.Errorf("internal-error-during-standalone-metric-update--matricnane-%s-freq-%d", aMetricName, newMetricFreq)
	}
	return nil
}

func (mm *onuMetricsManager) updateGroupSupport(ctx context.Context, aGroupName string, pmConfigs *voltha.PmConfigs) error {
	groupSliceIdx := 0
	var group *voltha.PmGroupConfig

	for groupSliceIdx, group = range pmConfigs.Groups {
		if group.GroupName == aGroupName {
			break
		}
	}
	if group == nil {
		logger.Errorw(ctx, "group metric not found", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName})
		return fmt.Errorf("group-not-found--groupName-%s", aGroupName)
	}

	updated := false
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	for k, v := range mm.groupMetricMap {
		if k == aGroupName && v.enabled != group.Enabled {
			mm.pDeviceHandler.pmConfigs.Groups[groupSliceIdx].Enabled = group.Enabled
			v.enabled = group.Enabled
			if group.Enabled {
				if v.isL2PMCounter {
					// If it is a L2 PM counter we need to mark the PM to be added
					mm.l2PmToAdd = mm.appendIfMissingString(mm.l2PmToAdd, v.groupName)
					// If the group support flag toggles too soon, we need to delete the group name from l2PmToDelete slice
					mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, v.groupName)

					// The GemPortHistory group requires some special handling as the instance IDs are not pre-defined
					// unlike other L2 PM counters. We need to fetch the active gemport instance IDs in the system to
					// take further action
					if v.groupName == GemPortHistoryName {
						mm.updateGemPortNTPInstanceToAddForPerfMonitoring(ctx)
					}
				} else if mm.pDeviceHandler.pmConfigs.FreqOverride { // otherwise just update the next collection interval
					v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
				}
			} else { // group counter is disabled
				if v.isL2PMCounter {
					// If it is a L2 PM counter we need to mark the PM to be deleted
					mm.l2PmToDelete = mm.appendIfMissingString(mm.l2PmToDelete, v.groupName)
					// If the group support flag toggles too soon, we need to delete the group name from l2PmToAdd slice
					mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, v.groupName)

					// The GemPortHistory group requires some special handling as the instance IDs are not pre-defined
					// unlike other L2 PM counters. We need to fetch the active gemport instance IDs in the system to
					// take further action
					if v.groupName == GemPortHistoryName {
						mm.updateGemPortNTPInstanceToDeleteForPerfMonitoring(ctx)
					}
				}
			}
			updated = true
			if v.isL2PMCounter {
				logger.Infow(ctx, "l2 pm group metric support updated",
					log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName, "enabled": group.Enabled, "l2PmToAdd": mm.l2PmToAdd, "l2PmToDelete": mm.l2PmToDelete})
			} else {
				logger.Infow(ctx, "group metric support updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName, "enabled": group.Enabled})
			}
			break
		}
	}

	if !updated {
		logger.Errorw(ctx, "group metric support not updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName})
		return fmt.Errorf("internal-error-during-group-support-update--groupName-%s", aGroupName)
	}
	return nil
}

func (mm *onuMetricsManager) updateMetricSupport(ctx context.Context, aMetricName string, pmConfigs *voltha.PmConfigs) error {
	metricSliceIdx := 0
	var metric *voltha.PmConfig

	for metricSliceIdx, metric = range pmConfigs.Metrics {
		if metric.Name == aMetricName {
			break
		}
	}

	if metric == nil {
		logger.Errorw(ctx, "standalone metric not found", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": aMetricName})
		return fmt.Errorf("metric-not-found--metricname-%s", aMetricName)
	}

	updated := false
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	for k, v := range mm.standaloneMetricMap {
		if k == aMetricName && v.enabled != metric.Enabled {
			mm.pDeviceHandler.pmConfigs.Metrics[metricSliceIdx].Enabled = metric.Enabled
			v.enabled = metric.Enabled
			// If the standalone metric is now enabled and frequency override is enabled, set the next metric collection time
			if metric.Enabled && mm.pDeviceHandler.pmConfigs.FreqOverride {
				v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
			}
			updated = true
			logger.Infow(ctx, "standalone metric support updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": aMetricName, "enabled": metric.Enabled})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "standalone metric support not updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": aMetricName})
		return fmt.Errorf("internal-error-during-standalone-support-update--metricname-%s", aMetricName)
	}
	return nil
}

func (mm *onuMetricsManager) collectAllGroupAndStandaloneMetrics(ctx context.Context) {
	if mm.pDeviceHandler.pmConfigs.Grouped { // metrics are managed as a group.
		go mm.collectAllGroupMetrics(ctx)
	} else {
		go mm.collectAllStandaloneMetrics(ctx)
	}
}

func (mm *onuMetricsManager) collectAllGroupMetrics(ctx context.Context) {
	go func() {
		logger.Debug(ctx, "startCollector before collecting optical metrics")
		metricInfo := mm.collectOpticalMetrics(ctx)
		if metricInfo != nil {
			mm.publishMetrics(ctx, metricInfo)
		}
	}()

	go func() {
		logger.Debug(ctx, "startCollector before collecting uni metrics")
		metricInfo := mm.collectUniStatusMetrics(ctx)
		if metricInfo != nil {
			mm.publishMetrics(ctx, metricInfo)
		}
	}()

	// Add more here
}

func (mm *onuMetricsManager) collectAllStandaloneMetrics(ctx context.Context) {
	// None exists as of now, add when available here
}

func (mm *onuMetricsManager) collectGroupMetric(ctx context.Context, groupName string) {
	switch groupName {
	case OpticalPowerGroupMetricName:
		go func() {
			if mi := mm.collectOpticalMetrics(ctx); mm != nil {
				mm.publishMetrics(ctx, mi)
			}
		}()
	case UniStatusGroupMetricName:
		go func() {
			if mi := mm.collectUniStatusMetrics(ctx); mm != nil {
				mm.publishMetrics(ctx, mi)
			}
		}()
	default:
		logger.Errorw(ctx, "unhandled group metric name", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": groupName})
	}
}

func (mm *onuMetricsManager) collectStandaloneMetric(ctx context.Context, metricName string) {
	switch metricName {
	// None exist as of now, add when available
	default:
		logger.Errorw(ctx, "unhandled standalone metric name", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": metricName})
	}
}

// collectOpticalMetrics collects groups metrics related to optical power from ani-g ME.
func (mm *onuMetricsManager) collectOpticalMetrics(ctx context.Context) []*voltha.MetricInformation {
	logger.Debugw(ctx, "collectOpticalMetrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})

	mm.onuMetricsManagerLock.RLock()
	if !mm.groupMetricMap[OpticalPowerGroupMetricName].enabled {
		mm.onuMetricsManagerLock.RUnlock()
		logger.Debugw(ctx, "optical power group metric is not enabled", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return nil
	}
	mm.onuMetricsManagerLock.RUnlock()

	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           OpticalPowerGroupMetricName,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	// get the ANI-G instance IDs
	anigInstKeys := mm.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.AniGClassID)
loop:
	for _, anigInstID := range anigInstKeys {
		var meAttributes me.AttributeValueMap
		opticalMetrics := make(map[string]float32)
		// Get the ANI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"OpticalSignalLevel": 0, "TransmitOpticalLevel": 0}
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.AniGClassID, anigInstID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
			select {
			case meAttributes = <-mm.opticalMetricsChan:
				logger.Debugw(ctx, "received optical metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for optical metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				// The metrics will be empty in this case
				break loop
			}
			// Populate metric only if it was enabled.
			for k := range OpticalPowerGroupMetrics {
				switch k {
				case "ani_g_instance_id":
					if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
						opticalMetrics[k] = float32(val.(uint16))
					}
				case "transmit_power_dBm":
					if val, ok := meAttributes["TransmitOpticalLevel"]; ok && val != nil {
						opticalMetrics[k] = float32(math.Round((float64(mm.twosComplementToSignedInt16(val.(uint16)))/500.0)*10) / 10) // convert to dBm rounded of to single decimal place
					}
				case "receive_power_dBm":
					if val, ok := meAttributes["OpticalSignalLevel"]; ok && val != nil {
						opticalMetrics[k] = float32(math.Round((float64(mm.twosComplementToSignedInt16(val.(uint16)))/500.0)*10) / 10) // convert to dBm rounded of to single decimal place
					}
				default:
					// do nothing
				}
			}
		}
		// create slice of metrics given that there could be more than one ANI-G instance and
		// optical metrics are collected per ANI-G instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: opticalMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	return metricInfoSlice
}

// collectUniStatusMetrics collects UNI status group metric from various MEs (uni-g, pptp and veip).
// nolint: gocyclo
func (mm *onuMetricsManager) collectUniStatusMetrics(ctx context.Context) []*voltha.MetricInformation {
	logger.Debugw(ctx, "collectUniStatusMetrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	mm.onuMetricsManagerLock.RLock()
	if !mm.groupMetricMap[UniStatusGroupMetricName].enabled {
		mm.onuMetricsManagerLock.RUnlock()
		logger.Debugw(ctx, "uni status group metric is not enabled", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return nil
	}
	mm.onuMetricsManagerLock.RUnlock()

	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           "UniStatus", // Is this ok to hard code?
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	// get the UNI-G instance IDs
	unigInstKeys := mm.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.UniGClassID)
loop1:
	for _, unigInstID := range unigInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		unigMetrics := make(map[string]float32)
		var meAttributes me.AttributeValueMap
		// Get the UNI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"AdministrativeState": 0}
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.UniGClassID, unigInstID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received uni-g metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				// The metrics could be empty in this case
				break loop1
			}
			// Populate metric only if it was enabled.
			for k := range UniStatusGroupMetrics {
				switch k {
				case "uni_admin_state":
					if val, ok := meAttributes["AdministrativeState"]; ok && val != nil {
						unigMetrics[k] = float32(val.(byte))
					}
				default:
					// do nothing
				}
			}
			if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
				entityID := val.(uint16)
				unigMetrics["entity_id"] = float32(entityID)
				// TODO: Rlock needed for reading uniEntityMap? May not be needed given uniEntityMap is populated setup at initial ONU bring up
				for _, uni := range mm.pDeviceHandler.uniEntityMap {
					if uni.entityID == entityID {
						unigMetrics["uni_port_no"] = float32(uni.portNo)
						break
					}
				}
			}
			unigMetrics["me_class_id"] = float32(me.UniGClassID)

			// create slice of metrics given that there could be more than one UNI-G instance
			metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: unigMetrics}
			metricInfoSlice = append(metricInfoSlice, &metricInfo)
		}
	}

	// get the PPTP instance IDs
	pptpInstKeys := mm.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.PhysicalPathTerminationPointEthernetUniClassID)
loop2:
	for _, pptpInstID := range pptpInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		var meAttributes me.AttributeValueMap
		pptpMetrics := make(map[string]float32)

		requestedAttributes := me.AttributeValueMap{"SensedType": 0, "OperationalState": 0, "AdministrativeState": 0}
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.PhysicalPathTerminationPointEthernetUniClassID, pptpInstID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received pptp metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				// The metrics could be empty in this case
				break loop2
			}

			// Populate metric only if it was enabled.
			for k := range UniStatusGroupMetrics {
				switch k {
				case "sensed_type":
					if val, ok := meAttributes["SensedType"]; ok && val != nil {
						pptpMetrics[k] = float32(val.(byte))
					}
				case "oper_status":
					if val, ok := meAttributes["OperationalState"]; ok && val != nil {
						pptpMetrics[k] = float32(val.(byte))
					}
				case "uni_admin_state":
					if val, ok := meAttributes["AdministrativeState"]; ok && val != nil {
						pptpMetrics[k] = float32(val.(byte))
					}
				default:
					// do nothing
				}
			}
		}
		if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
			entityID := val.(uint16)
			pptpMetrics["entity_id"] = float32(entityID)
			// TODO: Rlock needed for reading uniEntityMap? May not be needed given uniEntityMap is populated setup at initial ONU bring up
			for _, uni := range mm.pDeviceHandler.uniEntityMap {
				if uni.entityID == entityID {
					pptpMetrics["uni_port_no"] = float32(uni.portNo)
					break
				}
			}
		}
		pptpMetrics["me_class_id"] = float32(me.PhysicalPathTerminationPointEthernetUniClassID)

		// create slice of metrics given that there could be more than one PPTP instance and
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: pptpMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	// get the VEIP instance IDs
	veipInstKeys := mm.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.VirtualEthernetInterfacePointClassID)
loop3:
	for _, veipInstID := range veipInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		var meAttributes me.AttributeValueMap
		veipMetrics := make(map[string]float32)

		requestedAttributes := me.AttributeValueMap{"OperationalState": 0, "AdministrativeState": 0}
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.VirtualEthernetInterfacePointClassID, veipInstID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received veip metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				// The metrics could be empty in this case
				break loop3
			}

			// Populate metric only if it was enabled.
			for k := range UniStatusGroupMetrics {
				switch k {
				case "oper_status":
					if val, ok := meAttributes["OperationalState"]; ok && val != nil {
						veipMetrics[k] = float32(val.(byte))
					}
				case "uni_admin_state":
					if val, ok := meAttributes["AdministrativeState"]; ok && val != nil {
						veipMetrics[k] = float32(val.(byte))
					}
				default:
					// do nothing
				}
			}
		}

		if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
			entityID := val.(uint16)
			veipMetrics["entity_id"] = float32(entityID)
			// TODO: Rlock needed for reading uniEntityMap? May not be needed given uniEntityMap is populated setup at initial ONU bring up
			for _, uni := range mm.pDeviceHandler.uniEntityMap {
				if uni.entityID == entityID {
					veipMetrics["uni_port_no"] = float32(uni.portNo)
					break
				}
			}
		}
		veipMetrics["me_class_id"] = float32(me.VirtualEthernetInterfacePointClassID)

		// create slice of metrics given that there could be more than one VEIP instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: veipMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	return metricInfoSlice
}

// publishMetrics publishes the metrics on kafka
func (mm *onuMetricsManager) publishMetrics(ctx context.Context, metricInfo []*voltha.MetricInformation) {
	var ke voltha.KpiEvent2
	ts := time.Now().Unix()
	ke.SliceData = metricInfo
	ke.Type = voltha.KpiEventType_slice
	ke.Ts = float64(ts)

	if err := mm.pDeviceHandler.EventProxy.SendKpiEvent(ctx, "STATS_EVENT", &ke, voltha.EventCategory_EQUIPMENT, voltha.EventSubCategory_ONU, ts); err != nil {
		logger.Errorw(ctx, "failed-to-send-pon-stats", log.Fields{"err": err})
	}
}

func (mm *onuMetricsManager) processOmciMessages(ctx context.Context) {
	logger.Infow(ctx, "Start routine to process OMCI-GET messages for device-id", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	// Flush metric collection channels to be safe.
	// It is possible that there is stale data on this channel if the processOmciMessages routine
	// is stopped right after issuing a OMCI-GET request and started again.
	// The processOmciMessages routine will get stopped if startCollector routine (in device_handler.go)
	// is stopped - as a result of ONU going down.
	mm.flushMetricCollectionChannels(ctx)

	for {
		select {
		case <-mm.stopProcessingOmciResponses: // stop this routine
			logger.Infow(ctx, "Stop routine to process OMCI-GET messages for device-id", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			return
		case message, ok := <-mm.pAdaptFsm.commChan:
			if !ok {
				logger.Errorw(ctx, "Message couldn't be read from channel", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				continue
			}
			logger.Debugw(ctx, "Received message on ONU metrics channel", log.Fields{"device-id": mm.pDeviceHandler.deviceID})

			switch message.Type {
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				mm.handleOmciMessage(ctx, msg)
			default:
				logger.Warn(ctx, "Unknown message type received", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "message.Type": message.Type})
			}
		}
	}
}

func (mm *onuMetricsManager) handleOmciMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "omci Msg", log.Fields{"device-id": mm.pDeviceHandler.deviceID,
		"msgType": msg.OmciMsg.MessageType, "msg": msg})
	switch msg.OmciMsg.MessageType {
	case omci.GetResponseType:
		//TODO: error handling
		_ = mm.handleOmciGetResponseMessage(ctx, msg)
	case omci.SynchronizeTimeResponseType:
		_ = mm.handleOmciSynchronizeTimeResponseMessage(ctx, msg)
	case omci.CreateResponseType:
		_ = mm.handleOmciCreateResponseMessage(ctx, msg)
	case omci.DeleteResponseType:
		_ = mm.handleOmciDeleteResponseMessage(ctx, msg)
	default:
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})

	}
}

func (mm *onuMetricsManager) handleOmciGetResponseMessage(ctx context.Context, msg OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetResponse - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetResponse - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	logger.Debugw(ctx, "OMCI GetResponse Data", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		meAttributes := msgObj.Attributes
		switch msgObj.EntityClass {
		case me.AniGClassID:
			mm.opticalMetricsChan <- meAttributes
			return nil
		case me.UniGClassID:
			mm.uniStatusMetricsChan <- meAttributes
			return nil
		case me.PhysicalPathTerminationPointEthernetUniClassID:
			mm.uniStatusMetricsChan <- meAttributes
			return nil
		case me.VirtualEthernetInterfacePointClassID:
			mm.uniStatusMetricsChan <- meAttributes
			return nil
		case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID,
			me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID,
			me.EthernetPerformanceMonitoringHistoryDataClassID,
			me.FecPerformanceMonitoringHistoryDataClassID,
			me.GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID:
			mm.l2PmChan <- meAttributes
		default:
			logger.Errorw(ctx, "unhandled omci get response message",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
		}
	}

	return fmt.Errorf("unhandled-omci-get-response-message")
}

func (mm *onuMetricsManager) handleOmciSynchronizeTimeResponseMessage(ctx context.Context, msg OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSynchronizeTimeResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for synchronize time response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for synchronize time response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.SynchronizeTimeResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for synchronize time response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for synchronize time response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	logger.Debugw(ctx, "OMCI synchronize time response Data", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		switch msgObj.EntityClass {
		case me.OnuGClassID:
			logger.Infow(ctx, "omci synchronize time success", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			mm.syncTimeResponseChan <- true
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci message",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
		}
	}
	mm.syncTimeResponseChan <- false
	logger.Errorf(ctx, "unhandled-omci-synchronize-time-response-message--error-code-%v", msgObj.Result)
	return fmt.Errorf("unhandled-omci-synchronize-time-response-message--error-code-%v", msgObj.Result)
}

// flushMetricCollectionChannels flushes all metric collection channels for any stale OMCI responses
func (mm *onuMetricsManager) flushMetricCollectionChannels(ctx context.Context) {
	// flush commMetricsChan
	select {
	case <-mm.pAdaptFsm.commChan:
		logger.Debug(ctx, "flushed common metrics channel")
	default:
	}

	// flush opticalMetricsChan
	select {
	case <-mm.opticalMetricsChan:
		logger.Debug(ctx, "flushed optical metrics channel")
	default:
	}

	// flush uniStatusMetricsChan
	select {
	case <-mm.uniStatusMetricsChan:
		logger.Debug(ctx, "flushed uni status metrics channel")
	default:
	}

	// flush syncTimeResponseChan
	select {
	case <-mm.syncTimeResponseChan:
		logger.Debug(ctx, "flushed sync time response channel")
	default:
	}

	// flush l2PmChan
	select {
	case <-mm.l2PmChan:
		logger.Debug(ctx, "flushed L2 PM collection channel")
	default:
	}

	// flush stopTicks
	select {
	case <-mm.stopTicks:
		logger.Debug(ctx, "flushed stopTicks channel")
	default:
	}

}

// ** L2 PM FSM Handlers start **

func (mm *onuMetricsManager) l2PMFsmStarting(ctx context.Context, e *fsm.Event) {
	// restore data from KV store
	if err := mm.restorePmData(ctx); err != nil {
		logger.Errorw(ctx, "error restoring pm data", log.Fields{"err": err})
		// we continue given that it does not effect the actual services for the ONU,
		// but there may be some negative effect on PM collection (there may be some mismatch in
		// the actual PM config and what is present on the device).
	}

	// Loop through all the group metrics
	// If it is a L2 PM Interval metric and it is enabled, then if it is not in the
	// list of active L2 PM list then mark it for creation
	// It it is a L2 PM Interval metric and it is disabled, then if it is in the
	// list of active L2 PM list then mark it for deletion
	mm.onuMetricsManagerLock.Lock()
	for n, g := range mm.groupMetricMap {
		if g.isL2PMCounter { // it is a l2 pm counter
			if g.enabled { // metric enabled.
				found := false
			inner1:
				for _, v := range mm.activeL2Pms {
					if v == n {
						found = true // metric already present in active l2 pm list
						break inner1
					}
				}
				if !found { // metric not in active l2 pm list. Mark this to be added later
					mm.l2PmToAdd = mm.appendIfMissingString(mm.l2PmToAdd, n)
				}
			} else { // metric not enabled.
				found := false
			inner2:
				for _, v := range mm.activeL2Pms {
					if v == n {
						found = true // metric is found in active l2 pm list
						break inner2
					}
				}
				if found { // metric is found in active l2 pm list. Mark this to be deleted later
					mm.l2PmToDelete = mm.appendIfMissingString(mm.l2PmToDelete, n)
				}
			}
		}
	}
	mm.onuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "pms to add and delete",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": mm.l2PmToAdd, "pms-to-delete": mm.l2PmToDelete})
	go func() {
		// push a tick event to move to next state
		if err := mm.pAdaptFsm.pFsm.Event(l2PmEventTick); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PMFsmSyncTime(ctx context.Context, e *fsm.Event) {
	// Sync time with the ONU to establish 15min boundary for PM collection.
	if err := mm.syncTime(ctx); err != nil {
		go func() {
			time.Sleep(SyncTimeRetryInterval * time.Second) // retry to sync time after this timeout
			// This will result in FSM attempting to sync time again
			if err := mm.pAdaptFsm.pFsm.Event(l2PmEventFailure); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	}
	// Initiate a tick generation routine every L2PmCollectionInterval
	go mm.generateTicks(ctx)

	go func() {
		if err := mm.pAdaptFsm.pFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PMFsmNull(ctx context.Context, e *fsm.Event) {
	// We need to reset the local data so that the L2 PM MEs are re-provisioned once the ONU is back up based on the latest PM CONFIG
	mm.onuMetricsManagerLock.Lock()
	mm.activeL2Pms = nil
	mm.l2PmToAdd = nil
	mm.l2PmToDelete = nil
	mm.onuMetricsManagerLock.Unlock()
	// If the FSM was stopped, then clear PM data from KV store
	// The FSM is stopped when ONU goes down. It is time to clear its data from store
	if e.Event == l2PmEventStop {
		_ = mm.clearPmGroupData(ctx) // ignore error
	}

}
func (mm *onuMetricsManager) l2PMFsmIdle(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "Enter state idle", log.Fields{"device-id": mm.pDeviceHandler.deviceID})

	mm.onuMetricsManagerLock.RLock()
	numOfPmToDelete := len(mm.l2PmToDelete)
	numOfPmToAdd := len(mm.l2PmToAdd)
	mm.onuMetricsManagerLock.RUnlock()

	if numOfPmToDelete > 0 {
		logger.Debugw(ctx, "state idle - pms to delete", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-delete": numOfPmToDelete})
		go func() {
			if err := mm.pAdaptFsm.pFsm.Event(l2PmEventDeleteMe); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	} else if numOfPmToAdd > 0 {
		logger.Debugw(ctx, "state idle - pms to add", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": numOfPmToAdd})
		go func() {
			if err := mm.pAdaptFsm.pFsm.Event(l2PmEventAddMe); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	}
}

func (mm *onuMetricsManager) l2PmFsmCollectData(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "state collect data", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	// Copy the activeL2Pms for which we want to collect the metrics since activeL2Pms can change dynamically
	mm.onuMetricsManagerLock.RLock()
	copyOfActiveL2Pms := make([]string, len(mm.activeL2Pms))
	_ = copy(copyOfActiveL2Pms, mm.activeL2Pms)
	mm.onuMetricsManagerLock.RUnlock()

	for _, n := range copyOfActiveL2Pms {
		var metricInfoSlice []*voltha.MetricInformation

		// mm.groupMetricMap[n].pmMEData.InstancesActive could dynamically change, so make a copy
		mm.onuMetricsManagerLock.RLock()
		copyOfEntityIDs := make([]uint16, len(mm.groupMetricMap[n].pmMEData.InstancesActive))
		_ = copy(copyOfEntityIDs, mm.groupMetricMap[n].pmMEData.InstancesActive)
		mm.onuMetricsManagerLock.RUnlock()

		switch n {
		case EthernetBridgeHistoryName:
			logger.Debugw(ctx, "state collect data - collecting data for EthernetFramePerformanceMonitoringHistoryData ME", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			for _, entityID := range copyOfEntityIDs {
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, true, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, false, entityID); metricInfo != nil { // downstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}
		case EthernetUniHistoryName:
			logger.Debugw(ctx, "state collect data - collecting data for EthernetPerformanceMonitoringHistoryData ME", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			for _, entityID := range copyOfEntityIDs {
				if metricInfo := mm.collectEthernetUniHistoryData(ctx, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}

		case FecHistoryName:
			for _, entityID := range copyOfEntityIDs {
				if metricInfo := mm.collectFecHistoryData(ctx, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}
		case GemPortHistoryName:
			for _, entityID := range copyOfEntityIDs {
				if metricInfo := mm.collectGemHistoryData(ctx, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}

		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
		mm.handleMetricsPublish(ctx, n, metricInfoSlice)
	}
	// Does not matter we send success or failure here.
	// Those PMs that we failed to collect data will be attempted to collect again in the next PM collection cycle (assuming
	// we have not exceed max attempts to collect the PM data)
	go func() {
		if err := mm.pAdaptFsm.pFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

// nolint: gocyclo
func (mm *onuMetricsManager) l2PmFsmCreatePM(ctx context.Context, e *fsm.Event) {
	// Copy the l2PmToAdd for which we want to collect the metrics since l2PmToAdd can change dynamically
	mm.onuMetricsManagerLock.RLock()
	copyOfL2PmToAdd := make([]string, len(mm.l2PmToAdd))
	_ = copy(copyOfL2PmToAdd, mm.l2PmToAdd)
	mm.onuMetricsManagerLock.RUnlock()

	logger.Debugw(ctx, "state create pm - start", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": copyOfL2PmToAdd})
	for _, n := range copyOfL2PmToAdd {
		resp := false
		atLeastOneSuccess := false // flag indicates if at least one ME instance of the PM was successfully created.
		cnt := 0
		switch n {
		case EthernetBridgeHistoryName:
			boolForDirection := make([]bool, 2) // stores true and false to indicate upstream and downstream directions.
			boolForDirection = append(boolForDirection, true, false)
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range boolForDirection {
				for _, uniPort := range mm.pDeviceHandler.uniEntityMap {
					// Attach the EthernetFramePerformanceMonitoringHistoryData ME to MacBridgePortConfigData on the UNI port
					entityID := macBridgePortAniEID + uniPort.entityID
					_ = mm.updatePmData(ctx, n, entityID, cPmAdd) // TODO: ignore error for now
				inner1:
					// retry L2PmCreateAttempts times to create the instance of PM
					for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
						mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
							ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, direction, true, mm.pAdaptFsm.commChan, entityID)
						if resp = mm.waitForResponseOrTimeout(ctx, true, entityID, "EthernetFramePerformanceMonitoringHistoryData"); resp {
							atLeastOneSuccess = true
							_ = mm.updatePmData(ctx, n, entityID, cPmAdded) // TODO: ignore error for now
							break inner1
						}
					}
					if cnt == L2PmCreateAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
					}
				}
			}
		case EthernetUniHistoryName:
			for _, uniPort := range mm.pDeviceHandler.uniEntityMap {
				if uniPort.portType == uniPPTP { // This metric is only applicable for PPTP Uni Type
					// Attach the EthernetPerformanceMonitoringHistoryData ME to PPTP port instance
					entityID := uniPort.entityID
					_ = mm.updatePmData(ctx, n, entityID, cPmAdd) // TODO: ignore error for now
				inner2:
					// retry L2PmCreateAttempts times to create the instance of PM
					for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
						mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetUniHistoryME(
							ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, true, mm.pAdaptFsm.commChan, entityID)
						if resp = mm.waitForResponseOrTimeout(ctx, true, entityID, "EthernetPerformanceMonitoringHistoryData"); resp {
							atLeastOneSuccess = true
							_ = mm.updatePmData(ctx, n, entityID, cPmAdded) // TODO: ignore error for now
							break inner2
						}
					}
					if cnt == L2PmCreateAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
					}
				}
			}
		case FecHistoryName:
			for _, anigInstID := range mm.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.AniGClassID) {
				// Attach the FecPerformanceMonitoringHistoryData ME to the ANI-G ME instance
				mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteFecHistoryME(
					ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, true, mm.pAdaptFsm.commChan, anigInstID)
				_ = mm.updatePmData(ctx, n, anigInstID, cPmAdd) // TODO: ignore error for now
			inner3:
				// retry L2PmCreateAttempts times to create the instance of PM
				for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
					if resp = mm.waitForResponseOrTimeout(ctx, true, anigInstID, "FecPerformanceMonitoringHistoryData"); resp {
						atLeastOneSuccess = true
						_ = mm.updatePmData(ctx, n, anigInstID, cPmAdded) // TODO: ignore error for now
						break inner3
					}
				}
				if cnt == L2PmCreateAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
					_ = mm.updatePmData(ctx, n, anigInstID, cPmRemoved) // TODO: ignore error for now
				}
			}
		case GemPortHistoryName:

			mm.onuMetricsManagerLock.RLock()
			copyOfGemPortInstIDsToAdd := make([]uint16, len(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd))
			_ = copy(copyOfGemPortInstIDsToAdd, mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd)
			mm.onuMetricsManagerLock.RUnlock()

			if len(copyOfGemPortInstIDsToAdd) == 0 {
				// If there are no gemport history MEs to be created, just skip further processing
				// Otherwise down below (after 'switch' case handling) we assume the ME creation failed because resp and atLeastOneSuccess flag are false.
				// Normally there are no GemPortHistory MEs to create at start up. They come in only after provisioning service on the ONU.
				mm.onuMetricsManagerLock.Lock()
				mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)
				mm.onuMetricsManagerLock.Unlock()
				continue
			}

			for _, v := range copyOfGemPortInstIDsToAdd {
				mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteGemPortHistoryME(
					ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, true, mm.pAdaptFsm.commChan, v)
				_ = mm.updatePmData(ctx, n, v, cPmAdd) // TODO: ignore error for now
			inner4:
				// retry L2PmCreateAttempts times to create the instance of PM
				for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
					if resp = mm.waitForResponseOrTimeout(ctx, true, v, "GemPortNetworkCtpPerformanceMonitoringHistoryData"); resp {
						atLeastOneSuccess = true
						_ = mm.updatePmData(ctx, n, v, cPmAdded) // TODO: ignore error for now
						break inner4
					}
				}
				if cnt == L2PmCreateAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
					_ = mm.updatePmData(ctx, n, v, cPmRemoved) // TODO: ignore error for now
				}
			}

		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
		// On success of at least one instance of the PM for a given ME, update the local list maintained for active PMs and PMs to add
		if atLeastOneSuccess {
			mm.onuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.appendIfMissingString(mm.activeL2Pms, n)
			mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
			mm.onuMetricsManagerLock.Unlock()
		} else {
			// If we are here then no instance of the PM of the given ME were created successfully, so locally disable the PM
			// and also remove it from l2PmToAdd slice so that we do not try to create the PM ME anymore
			mm.onuMetricsManagerLock.Lock()
			logger.Debugw(ctx, "exceeded-max-add-retry-attempts--disabling-group", log.Fields{"groupName": n})
			mm.groupMetricMap[n].enabled = false
			mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)

			logger.Warnw(ctx, "state create pm - failed to create pm",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": n,
					"active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
			mm.onuMetricsManagerLock.Unlock()
		}
	}
	logger.Debugw(ctx, "state create pm - done", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
	// Does not matter we send success or failure here.
	// Those PMs that we failed to create will be attempted to create again in the next PM creation cycle (assuming
	// we have not exceed max attempts to create the PM ME)
	go func() {
		if err := mm.pAdaptFsm.pFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

// nolint: gocyclo
func (mm *onuMetricsManager) l2PmFsmDeletePM(ctx context.Context, e *fsm.Event) {
	// Copy the l2PmToDelete for which we want to collect the metrics since l2PmToDelete can change dynamically
	mm.onuMetricsManagerLock.RLock()
	copyOfL2PmToDelete := make([]string, len(mm.l2PmToDelete))
	_ = copy(copyOfL2PmToDelete, mm.l2PmToDelete)
	mm.onuMetricsManagerLock.RUnlock()

	logger.Debugw(ctx, "state delete pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-delete": mm.l2PmToDelete})
	for _, n := range copyOfL2PmToDelete {
		resp := false
		cnt := 0
		atLeastOneDeleteFailure := false

		// mm.groupMetricMap[n].pmMEData.InstancesActive could dynamically change, so make a copy
		mm.onuMetricsManagerLock.RLock()
		copyOfEntityIDs := make([]uint16, len(mm.groupMetricMap[n].pmMEData.InstancesActive))
		_ = copy(copyOfEntityIDs, mm.groupMetricMap[n].pmMEData.InstancesActive)
		mm.onuMetricsManagerLock.RUnlock()

		if len(copyOfEntityIDs) == 0 {
			// if there are no enityIDs to remove for the PM ME just clear the PM name entry from cache and continue
			mm.onuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.onuMetricsManagerLock.Unlock()
			continue
		}
		logger.Debugw(ctx, "entities to delete", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": n, "entityIDs": copyOfEntityIDs})
		switch n {
		case EthernetBridgeHistoryName:
			boolForDirection := make([]bool, 2) // stores true and false to indicate upstream and downstream directions.
			boolForDirection = append(boolForDirection, true, false)
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range boolForDirection {
				for _, entityID := range copyOfEntityIDs {
				inner1:
					// retry L2PmDeleteAttempts times to delete the instance of PM
					for cnt = 0; cnt < L2PmDeleteAttempts; cnt++ {
						mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
							ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, direction, false, mm.pAdaptFsm.commChan, entityID)
						_ = mm.updatePmData(ctx, n, entityID, cPmRemove) // TODO: ignore error for now
						if resp = mm.waitForResponseOrTimeout(ctx, false, entityID, "EthernetFramePerformanceMonitoringHistoryData"); !resp {
							atLeastOneDeleteFailure = true
						} else {
							_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
							break inner1
						}
					}
					if cnt == L2PmDeleteAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
					}
				}
			}
		case EthernetUniHistoryName:
			for _, entityID := range copyOfEntityIDs {
			inner2:
				// retry L2PmDeleteAttempts times to delete the instance of PM
				for cnt = 0; cnt < L2PmDeleteAttempts; cnt++ {
					mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetUniHistoryME(
						ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, false, mm.pAdaptFsm.commChan, entityID)
					if resp = mm.waitForResponseOrTimeout(ctx, false, entityID, "EthernetPerformanceMonitoringHistoryData"); !resp {
						atLeastOneDeleteFailure = true
					} else {
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
						break inner2
					}
				}
				if cnt == L2PmDeleteAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
					_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
				}
			}
		case FecHistoryName:
			for _, entityID := range copyOfEntityIDs {
			inner3:
				// retry L2PmDeleteAttempts times to delete the instance of PM
				for cnt = 0; cnt < L2PmDeleteAttempts; cnt++ {
					mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteFecHistoryME(
						ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, false, mm.pAdaptFsm.commChan, entityID)
					if resp := mm.waitForResponseOrTimeout(ctx, false, entityID, "FecPerformanceMonitoringHistoryData"); !resp {
						atLeastOneDeleteFailure = true
					} else {
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
						break inner3
					}
				}
				if cnt == L2PmDeleteAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
					_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
				}
			}
		case GemPortHistoryName:
			for _, entityID := range copyOfEntityIDs {
			inner4:
				// retry L2PmDeleteAttempts times to delete the instance of PM
				for cnt = 0; cnt < L2PmDeleteAttempts; cnt++ {
					mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteGemPortHistoryME(
						ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, false, mm.pAdaptFsm.commChan, entityID)
					if resp = mm.waitForResponseOrTimeout(ctx, false, entityID, "GemPortNetworkCtpPerformanceMonitoringHistoryData"); !resp {
						atLeastOneDeleteFailure = true
					} else {
						_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
						break inner4
					}
				}
				if cnt == L2PmDeleteAttempts { // if we reached max attempts just give up hope on this given instance of the PM ME
					_ = mm.updatePmData(ctx, n, entityID, cPmRemoved) // TODO: ignore error for now
				}
			}
		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
		// If we could not completely clean up the PM ME then just give up.
		if atLeastOneDeleteFailure {
			logger.Warnw(ctx, "state delete pm - failed to delete at least one instance of the PM ME",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": n,
					"active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.onuMetricsManagerLock.Lock()
			logger.Debugw(ctx, "exceeded-max-delete-retry-attempts--disabling-group", log.Fields{"groupName": n})
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			mm.groupMetricMap[n].enabled = false
			mm.onuMetricsManagerLock.Unlock()
		} else { // success case
			mm.onuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.onuMetricsManagerLock.Unlock()
		}
	}
	logger.Debugw(ctx, "state delete pm - done", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
	// Does not matter we send success or failure here.
	// Those PMs that we failed to delete will be attempted to create again in the next PM collection cycle
	go func() {
		if err := mm.pAdaptFsm.pFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

// ** L2 PM FSM Handlers end **

// syncTime synchronizes time with the ONU to establish a 15 min boundary for PM collection and reporting.
func (mm *onuMetricsManager) syncTime(ctx context.Context) error {
	if err := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendSyncTime(ctx, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); err != nil {
		logger.Errorw(ctx, "cannot send sync time request", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return err
	}

	select {
	case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
		logger.Errorf(ctx, "timed out waiting for sync time response from onu", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("timed-out-waiting-for-sync-time-response-%v", mm.pDeviceHandler.deviceID)
	case syncTimeRes := <-mm.syncTimeResponseChan:
		if !syncTimeRes {
			return fmt.Errorf("failed-to-sync-time-%v", mm.pDeviceHandler.deviceID)
		}
		logger.Infow(ctx, "sync time success", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return nil
	}
}

func (mm *onuMetricsManager) collectEthernetFramePerformanceMonitoringHistoryData(ctx context.Context, upstream bool, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream})
	meParam := me.ParamData{EntityID: entityID}
	if upstream {
		if mEnt, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataUpstream(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
			logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream})
			return nil
		}
		classID = me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID
	} else {
		if mEnt, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataDownstream(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
			logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream})
			return nil
		}
		classID = me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID
	}

	intervalEndTime := -1
	ethPMHistData := make(map[string]float32)
	if err := mm.populateGroupSpecificMetrics(ctx, mEnt, classID, entityID, meAttributes, ethPMHistData, &intervalEndTime); err != nil {
		return nil
	}

	// Populate some relevant context for the EthernetFramePerformanceMonitoringHistoryData PM
	ethPMHistData["class_id"] = float32(classID)
	ethPMHistData["interval_end_time"] = float32(intervalEndTime)
	ethPMHistData["parent_class_id"] = float32(me.MacBridgeConfigurationDataClassID) // EthernetFramePerformanceMonitoringHistoryData is attached to MBPCD ME
	ethPMHistData["parent_entity_id"] = float32(entityID)
	if upstream {
		ethPMHistData["upstream"] = float32(1)
	} else {
		ethPMHistData["upstream"] = float32(0)
	}

	metricInfo := mm.populateOnuMetricInfo(EthernetBridgeHistoryName, ethPMHistData)

	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData successful",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *onuMetricsManager) collectEthernetUniHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewEthernetPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		return nil
	}
	classID = me.EthernetPerformanceMonitoringHistoryDataClassID

	intervalEndTime := -1
	ethUniHistData := make(map[string]float32)
	if err := mm.populateGroupSpecificMetrics(ctx, mEnt, classID, entityID, meAttributes, ethUniHistData, &intervalEndTime); err != nil {
		return nil
	}

	// Populate some relevant context for the EthernetPerformanceMonitoringHistoryData PM
	ethUniHistData["class_id"] = float32(classID)
	ethUniHistData["interval_end_time"] = float32(intervalEndTime)

	metricInfo := mm.populateOnuMetricInfo(EthernetUniHistoryName, ethUniHistData)

	logger.Debugw(ctx, "collecting data for EthernetPerformanceMonitoringHistoryData successful",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *onuMetricsManager) collectFecHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for FecPerformanceMonitoringHistoryData", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewFecPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		return nil
	}
	classID = me.FecPerformanceMonitoringHistoryDataClassID

	intervalEndTime := -1
	fecHistData := make(map[string]float32)
	if err := mm.populateGroupSpecificMetrics(ctx, mEnt, classID, entityID, meAttributes, fecHistData, &intervalEndTime); err != nil {
		return nil
	}

	// Populate some relevant context for the EthernetPerformanceMonitoringHistoryData PM
	fecHistData["class_id"] = float32(classID)
	fecHistData["interval_end_time"] = float32(intervalEndTime)

	metricInfo := mm.populateOnuMetricInfo(FecHistoryName, fecHistData)

	logger.Debugw(ctx, "collecting data for FecPerformanceMonitoringHistoryData successful",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *onuMetricsManager) collectGemHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for GemPortNetworkCtpPerformanceMonitoringHistoryData", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewGemPortNetworkCtpPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		return nil
	}
	classID = me.GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID

	intervalEndTime := -1
	gemHistData := make(map[string]float32)
	if err := mm.populateGroupSpecificMetrics(ctx, mEnt, classID, entityID, meAttributes, gemHistData, &intervalEndTime); err != nil {
		return nil
	}

	// Populate some relevant context for the GemPortNetworkCtpPerformanceMonitoringHistoryData PM
	gemHistData["class_id"] = float32(classID)
	gemHistData["interval_end_time"] = float32(intervalEndTime)

	metricInfo := mm.populateOnuMetricInfo(GemPortHistoryName, gemHistData)

	logger.Debugw(ctx, "collecting data for GemPortNetworkCtpPerformanceMonitoringHistoryData successful",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

// nolint: gocyclo
func (mm *onuMetricsManager) populateEthernetBridgeHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, ethPMHistData map[string]float32, intervalEndTime *int) error {
	upstream := false
	if classID == me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID {
		upstream = true
	}
	// Make sure "IntervalEndTime" is part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received ethernet pm history data metrics",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "upstream": upstream, "entityID": entityID})
		case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for ethernet pm history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "upstream": upstream, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-ethernet-bridge-history-%v", mm.pDeviceHandler.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-ethernet-bridge-history-%v", mm.pDeviceHandler.deviceID)
		}
	}
	for k := range EthernetBridgeHistory {
		// populate ethPMHistData only if metric key not already present (or populated), since it is possible that we populate
		// the attributes in multiple iterations for a given L2 PM ME as there is a limit on the max OMCI GET payload size.
		if _, ok := ethPMHistData[k]; !ok {
			switch k {
			case "entity_id":
				if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint16))
				}
			case "drop_events":
				if val, ok := meAttributes["DropEvents"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "octets":
				if val, ok := meAttributes["Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "packets":
				if val, ok := meAttributes["Packets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "broadcast_packets":
				if val, ok := meAttributes["BroadcastPackets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "multicast_packets":
				if val, ok := meAttributes["MulticastPackets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "crc_errored_packets":
				if val, ok := meAttributes["CrcErroredPackets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "undersize_packets":
				if val, ok := meAttributes["UndersizePackets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "oversize_packets":
				if val, ok := meAttributes["OversizePackets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "64_octets":
				if val, ok := meAttributes["Packets64Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "65_to_127_octets":
				if val, ok := meAttributes["Packets65To127Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "128_to_255_octets":
				if val, ok := meAttributes["Packets128To255Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "256_to_511_octets":
				if val, ok := meAttributes["Packets256To511Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "512_to_1023_octets":
				if val, ok := meAttributes["Packets512To1023Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			case "1024_to_1518_octets":
				if val, ok := meAttributes["Packets1024To1518Octets"]; ok && val != nil {
					ethPMHistData[k] = float32(val.(uint32))
				}
			default:
				// do nothing
			}
		}
	}
	return nil
}

// nolint: gocyclo
func (mm *onuMetricsManager) populateEthernetUniHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, ethPMUniHistData map[string]float32, intervalEndTime *int) error {
	// Make sure "IntervalEndTime" is part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received ethernet uni history data metrics",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for ethernet uni history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-ethernet-uni-history-%v", mm.pDeviceHandler.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-ethernet-uni-history-%v", mm.pDeviceHandler.deviceID)
		}
	}
	for k := range EthernetUniHistory {
		// populate ethPMUniHistData only if metric key not already present (or populated), since it is possible that we populate
		// the attributes in multiple iterations for a given L2 PM ME as there is a limit on the max OMCI GET payload size.
		if _, ok := ethPMUniHistData[k]; !ok {
			switch k {
			case "entity_id":
				if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint16))
				}
			case "fcs_errors":
				if val, ok := meAttributes["FcsErrors"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "excessive_collision_counter":
				if val, ok := meAttributes["ExcessiveCollisionCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "late_collision_counter":
				if val, ok := meAttributes["LateCollisionCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "frames_too_long":
				if val, ok := meAttributes["FramesTooLong"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "buffer_overflows_on_rx":
				if val, ok := meAttributes["BufferOverflowsOnReceive"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "buffer_overflows_on_tx":
				if val, ok := meAttributes["BufferOverflowsOnTransmit"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "single_collision_frame_counter":
				if val, ok := meAttributes["SingleCollisionFrameCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "multiple_collisions_frame_counter":
				if val, ok := meAttributes["MultipleCollisionsFrameCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "sqe_counter":
				if val, ok := meAttributes["SqeCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "deferred_tx_counter":
				if val, ok := meAttributes["DeferredTransmissionCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "internal_mac_tx_error_counter":
				if val, ok := meAttributes["InternalMacTransmitErrorCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "carrier_sense_error_counter":
				if val, ok := meAttributes["CarrierSenseErrorCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "alignment_error_counter":
				if val, ok := meAttributes["AlignmentErrorCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			case "internal_mac_rx_error_counter":
				if val, ok := meAttributes["InternalMacReceiveErrorCounter"]; ok && val != nil {
					ethPMUniHistData[k] = float32(val.(uint32))
				}
			default:
				// do nothing
			}
		}
	}
	return nil
}

// nolint: gocyclo
func (mm *onuMetricsManager) populateFecHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, fecHistData map[string]float32, intervalEndTime *int) error {
	// Make sure "IntervalEndTime" is part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received fec history data metrics",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for fec history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-fec-history-%v", mm.pDeviceHandler.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-fec-history-%v", mm.pDeviceHandler.deviceID)
		}
	}
	for k := range FecHistory {
		// populate fecHistData only if metric key not already present (or populated), since it is possible that we populate
		// the attributes in multiple iterations for a given L2 PM ME as there is a limit on the max OMCI GET payload size.
		if _, ok := fecHistData[k]; !ok {
			switch k {
			case "entity_id":
				if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint16))
				}
			case "corrected_bytes":
				if val, ok := meAttributes["CorrectedBytes"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint32))
				}
			case "corrected_code_words":
				if val, ok := meAttributes["CorrectedCodeWords"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint32))
				}
			case "uncorrectable_code_words":
				if val, ok := meAttributes["UncorrectableCodeWords"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint32))
				}
			case "total_code_words":
				if val, ok := meAttributes["TotalCodeWords"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint32))
				}
			case "fec_seconds":
				if val, ok := meAttributes["FecSeconds"]; ok && val != nil {
					fecHistData[k] = float32(val.(uint16))
				}
			default:
				// do nothing
			}
		}
	}
	return nil
}

// nolint: gocyclo
func (mm *onuMetricsManager) populateGemPortMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, gemPortHistData map[string]float32, intervalEndTime *int) error {
	// Make sure "IntervalEndTime" is part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, mm.pAdaptFsm.commChan); meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received gem port history data metrics",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
		case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for gem port history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-gemport-history-%v", mm.pDeviceHandler.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-gemport-history-%v", mm.pDeviceHandler.deviceID)
		}
	}
	for k := range GemPortHistory {
		// populate gemPortHistData only if metric key not already present (or populated), since it is possible that we populate
		// the attributes in multiple iterations for a given L2 PM ME as there is a limit on the max OMCI GET payload size.
		if _, ok := gemPortHistData[k]; !ok {
			switch k {
			case "entity_id":
				if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint16))
				}
			case "transmitted_gem_frames":
				if val, ok := meAttributes["TransmittedGemFrames"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint32))
				}
			case "received_gem_frames":
				if val, ok := meAttributes["ReceivedGemFrames"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint32))
				}
			case "received_payload_bytes":
				if val, ok := meAttributes["ReceivedPayloadBytes"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint64))
				}
			case "transmitted_payload_bytes":
				if val, ok := meAttributes["TransmittedPayloadBytes"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint64))
				}
			case "encryption_key_errors":
				if val, ok := meAttributes["EncryptionKeyErrors"]; ok && val != nil {
					gemPortHistData[k] = float32(val.(uint32))
				}
			default:
				// do nothing
			}
		}
	}
	return nil
}

func (mm *onuMetricsManager) handleOmciCreateResponseMessage(ctx context.Context, msg OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for create response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for create response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for create response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for delete response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	logger.Debugw(ctx, "OMCI create response Data", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "data-fields": msgObj})
	switch msgObj.EntityClass {
	case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID,
		me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID,
		me.EthernetPerformanceMonitoringHistoryDataClassID,
		me.FecPerformanceMonitoringHistoryDataClassID,
		me.GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID:
		// If the result is me.InstanceExists it means the entity was already created. It is ok handled that as success
		if msgObj.Result == me.Success || msgObj.Result == me.InstanceExists {
			mm.l2PmCreateOrDeleteResponseChan <- true
		} else {
			logger.Warnw(ctx, "failed to create me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
			mm.l2PmCreateOrDeleteResponseChan <- false
		}
		return nil
	default:
		logger.Errorw(ctx, "unhandled omci create response message",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
	}
	return fmt.Errorf("unhandled-omci-create-response-message-%v", mm.pDeviceHandler.deviceID)
}

func (mm *onuMetricsManager) handleOmciDeleteResponseMessage(ctx context.Context, msg OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeDeleteResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for delete response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for create response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.DeleteResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for delete response - handling stopped", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for delete response - handling stopped: %s", mm.pDeviceHandler.deviceID)
	}
	logger.Debugw(ctx, "OMCI delete response Data", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "data-fields": msgObj})
	switch msgObj.EntityClass {
	case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID,
		me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID,
		me.EthernetPerformanceMonitoringHistoryDataClassID,
		me.FecPerformanceMonitoringHistoryDataClassID,
		me.GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID:
		// If the result is me.UnknownInstance it means the entity was already deleted. It is ok handled that as success
		if msgObj.Result == me.Success || msgObj.Result == me.UnknownInstance {
			mm.l2PmCreateOrDeleteResponseChan <- true
		} else {
			logger.Warnw(ctx, "failed to delete me", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
			mm.l2PmCreateOrDeleteResponseChan <- false
		}
		return nil
	default:
		logger.Errorw(ctx, "unhandled omci delete response message",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
	}
	return fmt.Errorf("unhandled-omci-delete-response-message-%v", mm.pDeviceHandler.deviceID)
}

func (mm *onuMetricsManager) generateTicks(ctx context.Context) {
	for {
		select {
		case <-time.After(L2PmCollectionInterval * time.Second):
			go func() {
				if err := mm.pAdaptFsm.pFsm.Event(l2PmEventTick); err != nil {
					logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
				}
			}()
		case <-mm.stopTicks:
			logger.Infow(ctx, "stopping ticks", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			return
		}
	}
}

func (mm *onuMetricsManager) handleMetricsPublish(ctx context.Context, metricName string, metricInfoSlice []*voltha.MetricInformation) {
	// Publish metrics if it is valid
	if metricInfoSlice != nil {
		mm.publishMetrics(ctx, metricInfoSlice)
	} else {
		// If collectAttempts exceeds L2PmCollectAttempts then remove it from activeL2Pms
		// slice so that we do not collect data from that PM ME anymore
		mm.onuMetricsManagerLock.Lock()
		mm.groupMetricMap[metricName].collectAttempts++
		if mm.groupMetricMap[metricName].collectAttempts > L2PmCollectAttempts {
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, metricName)
		}
		logger.Warnw(ctx, "state collect data - no metrics collected",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": metricName, "collectAttempts": mm.groupMetricMap[metricName].collectAttempts})
		mm.onuMetricsManagerLock.Unlock()
	}
}

func (mm *onuMetricsManager) populateGroupSpecificMetrics(ctx context.Context, mEnt *me.ManagedEntity, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, data map[string]float32, intervalEndTime *int) error {
	var grpFunc groupMetricPopulateFunc
	switch classID {
	case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID, me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID:
		grpFunc = mm.populateEthernetBridgeHistoryMetrics
	case me.EthernetPerformanceMonitoringHistoryDataClassID:
		grpFunc = mm.populateEthernetUniHistoryMetrics
	case me.FecPerformanceMonitoringHistoryDataClassID:
		grpFunc = mm.populateFecHistoryMetrics
	case me.GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID:
		grpFunc = mm.populateGemPortMetrics
	default:
		return fmt.Errorf("unknown-classid-%v", classID)
	}

	size := 0
	requestedAttributes := make(me.AttributeValueMap)
	for _, v := range mEnt.GetAttributeDefinitions() {
		if (v.Size + size) <= MaxL2PMGetPayLoadSize {
			requestedAttributes[v.Name] = v.DefValue
			size = v.Size + size
		} else { // We exceeded the allow omci get size
			// Let's collect the attributes via get now and collect remaining in the next iteration
			if err := grpFunc(ctx, classID, entityID, meAttributes, requestedAttributes, data, intervalEndTime); err != nil {
				logger.Errorw(ctx, "error during metric collection",
					log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "err": err})
				return err
			}
			size = 0                                         // reset size
			requestedAttributes = make(me.AttributeValueMap) // reset map
		}
	}
	// Collect the omci get attributes for the last bunch of attributes.
	if len(requestedAttributes) > 0 {
		if err := grpFunc(ctx, classID, entityID, meAttributes, requestedAttributes, data, intervalEndTime); err != nil {
			logger.Errorw(ctx, "error during metric collection",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "err": err})
			return err
		}
	}
	return nil
}

func (mm *onuMetricsManager) populateOnuMetricInfo(title string, data map[string]float32) voltha.MetricInformation {
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           title,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	// create slice of metrics given that there could be more than one VEIP instance
	metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: data}
	return metricInfo
}

func (mm *onuMetricsManager) updateAndValidateIntervalEndTime(ctx context.Context, entityID uint16, meAttributes me.AttributeValueMap, intervalEndTime *int) bool {
	valid := false
	if *intervalEndTime == -1 { // first time
		// Update the interval end time
		if val, ok := meAttributes["IntervalEndTime"]; ok && val != nil {
			*intervalEndTime = int(meAttributes["IntervalEndTime"].(uint8))
			valid = true
		}
	} else {
		var currIntervalEndTime int
		if val, ok := meAttributes["IntervalEndTime"]; ok && val != nil {
			currIntervalEndTime = int(meAttributes["IntervalEndTime"].(uint8))
		}
		if currIntervalEndTime != *intervalEndTime { // interval end time changed during metric collection
			logger.Errorw(ctx, "interval end time changed during metrics collection for ethernet pm history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID,
					"currIntervalEndTime": *intervalEndTime, "newIntervalEndTime": currIntervalEndTime})
		} else {
			valid = true
		}
	}
	return valid
}

func (mm *onuMetricsManager) waitForResponseOrTimeout(ctx context.Context, create bool, instID uint16, meClassName string) bool {
	logger.Debugw(ctx, "waitForResponseOrTimeout", log.Fields{"create": create, "instID": instID, "meClassName": meClassName})
	select {
	case resp := <-mm.l2PmCreateOrDeleteResponseChan:
		logger.Debugw(ctx, "received l2 pm me response",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "resp": resp, "create": create, "meClassName": meClassName, "instID": instID})
		return resp
	case <-time.After(time.Duration(mm.pDeviceHandler.pOpenOnuAc.omciTimeout) * time.Second):
		logger.Errorw(ctx, "timeout waiting for l2 pm me response",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "resp": false, "create": create, "meClassName": meClassName, "instID": instID})
	}
	return false
}

func (mm *onuMetricsManager) initializeGroupMetric(grpMtrcs map[string]voltha.PmConfig_PmType, grpName string, grpEnabled bool, grpFreq uint32) {
	var pmConfigSlice []*voltha.PmConfig
	for k, v := range grpMtrcs {
		pmConfigSlice = append(pmConfigSlice,
			&voltha.PmConfig{
				Name:       k,
				Type:       v,
				Enabled:    grpEnabled && mm.pDeviceHandler.pOpenOnuAc.metricsEnabled,
				SampleFreq: grpFreq})
	}
	groupMetric := voltha.PmGroupConfig{
		GroupName: grpName,
		Enabled:   grpEnabled && mm.pDeviceHandler.pOpenOnuAc.metricsEnabled,
		GroupFreq: grpFreq,
		Metrics:   pmConfigSlice,
	}
	mm.pDeviceHandler.pmConfigs.Groups = append(mm.pDeviceHandler.pmConfigs.Groups, &groupMetric)

}

func (mm *onuMetricsManager) initializeL2PmFsm(ctx context.Context, aCommChannel chan Message) error {
	mm.pAdaptFsm = NewAdapterFsm("L2PmFSM", mm.pDeviceHandler.deviceID, aCommChannel)
	if mm.pAdaptFsm == nil {
		logger.Errorw(ctx, "L2PMFsm AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf("nil-adapter-fsm")
	}
	// L2 PM FSM related state machine
	mm.pAdaptFsm.pFsm = fsm.NewFSM(
		l2PmStNull,
		fsm.Events{
			{Name: l2PmEventInit, Src: []string{l2PmStNull}, Dst: l2PmStStarting},
			{Name: l2PmEventTick, Src: []string{l2PmStStarting}, Dst: l2PmStSyncTime},
			{Name: l2PmEventTick, Src: []string{l2PmStIdle, l2PmEventDeleteMe, l2PmEventAddMe}, Dst: l2PmStCollectData},
			{Name: l2PmEventSuccess, Src: []string{l2PmStSyncTime, l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStIdle},
			{Name: l2PmEventFailure, Src: []string{l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStIdle},
			{Name: l2PmEventFailure, Src: []string{l2PmStSyncTime}, Dst: l2PmStSyncTime},
			{Name: l2PmEventAddMe, Src: []string{l2PmStIdle}, Dst: l2PmStCreatePmMe},
			{Name: l2PmEventDeleteMe, Src: []string{l2PmStIdle}, Dst: l2PmStDeletePmMe},
			{Name: l2PmEventStop, Src: []string{l2PmStNull, l2PmStStarting, l2PmStSyncTime, l2PmStIdle, l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStNull},
		},
		fsm.Callbacks{
			"enter_state":                func(e *fsm.Event) { mm.pAdaptFsm.logFsmStateChange(ctx, e) },
			"enter_" + l2PmStNull:        func(e *fsm.Event) { mm.l2PMFsmNull(ctx, e) },
			"enter_" + l2PmStIdle:        func(e *fsm.Event) { mm.l2PMFsmIdle(ctx, e) },
			"enter_" + l2PmStStarting:    func(e *fsm.Event) { mm.l2PMFsmStarting(ctx, e) },
			"enter_" + l2PmStSyncTime:    func(e *fsm.Event) { mm.l2PMFsmSyncTime(ctx, e) },
			"enter_" + l2PmStCollectData: func(e *fsm.Event) { mm.l2PmFsmCollectData(ctx, e) },
			"enter_" + l2PmStCreatePmMe:  func(e *fsm.Event) { mm.l2PmFsmCreatePM(ctx, e) },
			"enter_" + l2PmStDeletePmMe:  func(e *fsm.Event) { mm.l2PmFsmDeletePM(ctx, e) },
		},
	)
	return nil
}

func (mm *onuMetricsManager) initializeAllGroupMetrics() {
	mm.pDeviceHandler.pmConfigs = &voltha.PmConfigs{}
	mm.pDeviceHandler.pmConfigs.Id = mm.pDeviceHandler.deviceID
	mm.pDeviceHandler.pmConfigs.DefaultFreq = DefaultMetricCollectionFrequency
	mm.pDeviceHandler.pmConfigs.Grouped = GroupMetricEnabled
	mm.pDeviceHandler.pmConfigs.FreqOverride = DefaultFrequencyOverrideEnabled

	// Populate group metrics.
	// Lets populate irrespective of GroupMetricEnabled is true or not.
	// The group metrics collection will decided on this flag later

	mm.initializeGroupMetric(OpticalPowerGroupMetrics, OpticalPowerGroupMetricName,
		OpticalPowerGroupMetricEnabled, OpticalPowerMetricGroupCollectionFrequency)

	mm.initializeGroupMetric(UniStatusGroupMetrics, UniStatusGroupMetricName,
		UniStatusGroupMetricEnabled, UniStatusMetricGroupCollectionFrequency)

	// classical l2 pm counter start

	mm.initializeGroupMetric(EthernetBridgeHistory, EthernetBridgeHistoryName,
		EthernetBridgeHistoryEnabled, EthernetBridgeHistoryFrequency)

	mm.initializeGroupMetric(EthernetUniHistory, EthernetUniHistoryName,
		EthernetUniHistoryEnabled, EthernetUniHistoryFrequency)

	mm.initializeGroupMetric(FecHistory, FecHistoryName,
		FecHistoryEnabled, FecHistoryFrequency)

	mm.initializeGroupMetric(GemPortHistory, GemPortHistoryName,
		GemPortHistoryEnabled, GemPortHistoryFrequency)

	// classical l2 pm counter end

	// Add standalone metric (if present) after this (will be added to dh.pmConfigs.Metrics)
}

func (mm *onuMetricsManager) populateLocalGroupMetricData(ctx context.Context) {
	// Populate local group metric structures
	for _, g := range mm.pDeviceHandler.pmConfigs.Groups {
		mm.groupMetricMap[g.GroupName] = &groupMetric{
			groupName: g.GroupName,
			enabled:   g.Enabled,
			frequency: g.GroupFreq,
		}
		switch g.GroupName {
		case OpticalPowerGroupMetricName:
			mm.groupMetricMap[g.GroupName].metricMap = OpticalPowerGroupMetrics
		case UniStatusGroupMetricName:
			mm.groupMetricMap[g.GroupName].metricMap = UniStatusGroupMetrics
		case EthernetBridgeHistoryName:
			mm.groupMetricMap[g.GroupName].metricMap = EthernetBridgeHistory
			mm.groupMetricMap[g.GroupName].isL2PMCounter = true
		case EthernetUniHistoryName:
			mm.groupMetricMap[g.GroupName].metricMap = EthernetUniHistory
			mm.groupMetricMap[g.GroupName].isL2PMCounter = true
		case FecHistoryName:
			mm.groupMetricMap[g.GroupName].metricMap = FecHistory
			mm.groupMetricMap[g.GroupName].isL2PMCounter = true
		case GemPortHistoryName:
			mm.groupMetricMap[g.GroupName].metricMap = GemPortHistory
			mm.groupMetricMap[g.GroupName].isL2PMCounter = true
		default:
			logger.Errorw(ctx, "unhandled-group-name", log.Fields{"groupName": g.GroupName})
		}
	}

	// Populate local standalone metric structures
	for _, m := range mm.pDeviceHandler.pmConfigs.Metrics {
		mm.standaloneMetricMap[m.Name] = &standaloneMetric{
			metricName: m.Name,
			enabled:    m.Enabled,
			frequency:  m.SampleFreq,
		}
		switch m.Name {
		// None exist as of now. Add when available.
		default:
			logger.Errorw(ctx, "unhandled-metric-name", log.Fields{"metricName": m.Name})
		}
	}
}

func (mm *onuMetricsManager) AddGemPortForPerfMonitoring(gemPortNTPInstID uint16) {
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	// mark the instance for addition
	mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.appendIfMissingUnt16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, gemPortNTPInstID)
	// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToDelete slice
	mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.removeIfFoundUint16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, gemPortNTPInstID)

	mm.l2PmToAdd = mm.appendIfMissingString(mm.l2PmToAdd, GemPortHistoryName)
	// We do not need to remove from l2PmToDelete slice as we could have Add and Delete of
	// GemPortPerfHistory ME simultaneously for different instances of the ME.
	// The creation or deletion of an instance is decided based on its presence in gemPortNCTPPerfHistInstToDelete or
	// gemPortNCTPPerfHistInstToAdd slice
}

func (mm *onuMetricsManager) RemoveGemPortForPerfMonitoring(gemPortNTPInstID uint16) {
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.appendIfMissingUnt16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, gemPortNTPInstID)
	// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToAdd slice
	mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.removeIfFoundUint16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, gemPortNTPInstID)

	mm.l2PmToDelete = mm.appendIfMissingString(mm.l2PmToDelete, GemPortHistoryName)
	// We do not need to remove from l2PmToAdd slice as we could have Add and Delete of
	// GemPortPerfHistory ME simultaneously for different instances of the ME.
	// The creation or deletion of an instance is decided based on its presence in gemPortNCTPPerfHistInstToDelete or
	// gemPortNCTPPerfHistInstToAdd slice
}

func (mm *onuMetricsManager) updateGemPortNTPInstanceToAddForPerfMonitoring(ctx context.Context) {
	if mm.pDeviceHandler.pOnuTP != nil {
		gemPortInstIDs := mm.pDeviceHandler.pOnuTP.GetAllBidirectionalGemPortIDsForOnu()
		// NOTE: It is expected that caller of this function has acquired the required mutex for synchronization purposes
		for _, v := range gemPortInstIDs {
			// mark the instance for addition
			mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.appendIfMissingUnt16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, v)
			// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToDelete slice
			mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.removeIfFoundUint16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, v)
		}
		logger.Debugw(ctx, "updateGemPortNTPInstanceToAddForPerfMonitoring",
			log.Fields{"deviceID": mm.pDeviceHandler.deviceID, "gemToAdd": mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, "gemToDel": mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete})
	}
}

func (mm *onuMetricsManager) updateGemPortNTPInstanceToDeleteForPerfMonitoring(ctx context.Context) {
	if mm.pDeviceHandler.pOnuTP != nil {
		gemPortInstIDs := mm.pDeviceHandler.pOnuTP.GetAllBidirectionalGemPortIDsForOnu()
		// NOTE: It is expected that caller of this function has acquired the required mutex for synchronization purposes
		for _, v := range gemPortInstIDs {
			mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.appendIfMissingUnt16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, v)
			// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToAdd slice
			mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.removeIfFoundUint16(mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, v)
		}
	}
	logger.Debugw(ctx, "updateGemPortNTPInstanceToDeleteForPerfMonitoring",
		log.Fields{"deviceID": mm.pDeviceHandler.deviceID, "gemToAdd": mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, "gemToDel": mm.groupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete})
}

// restorePmData restores any PM data available on the KV store to local cache
func (mm *onuMetricsManager) restorePmData(ctx context.Context) error {
	logger.Debugw(ctx, "restorePmData - start", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.pDeviceHandler.deviceID))
	}
	var errorsList []error
	for groupName, group := range mm.groupMetricMap {
		group.pmMEData = &pmMEData{}
		Value, err := mm.pmKvStore.Get(ctx, groupName)
		if err == nil {
			if Value != nil {
				logger.Debugw(ctx, "PM data read",
					log.Fields{"Key": Value.Key, "device-id": mm.pDeviceHandler.deviceID})
				tmpBytes, _ := kvstore.ToByte(Value.Value)

				if err = json.Unmarshal(tmpBytes, &group.pmMEData); err != nil {
					logger.Errorw(ctx, "unable to unmarshal PM data", log.Fields{"error": err, "device-id": mm.pDeviceHandler.deviceID})
					errorsList = append(errorsList, fmt.Errorf(fmt.Sprintf("unable-to-unmarshal-PM-data-%s-for-group-%s", mm.pDeviceHandler.deviceID, groupName)))
					continue
				}
				logger.Debugw(ctx, "restorePmData - success", log.Fields{"pmData": group.pmMEData, "groupName": groupName, "device-id": mm.pDeviceHandler.deviceID})
			} else {
				logger.Debugw(ctx, "no PM data found", log.Fields{"groupName": groupName, "device-id": mm.pDeviceHandler.deviceID})
				continue
			}
		} else {
			logger.Errorw(ctx, "restorePmData - fail", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": groupName, "err": err})
			errorsList = append(errorsList, fmt.Errorf(fmt.Sprintf("unable-to-read-from-KVstore-%s-for-group-%s", mm.pDeviceHandler.deviceID, groupName)))
			continue
		}
	}
	if len(errorsList) > 0 {
		return fmt.Errorf("errors-restoring-pm-data-for-one-or-more-groups--errors:%v", errorsList)
	}
	logger.Debugw(ctx, "restorePmData - complete success", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	return nil
}

// getPmData gets pmMEData from cache. Since we have write through cache implementation for pmMEData,
// the data must be available in cache.
// Note, it is expected that caller of this function manages the required synchronization (like using locks etc.).
func (mm *onuMetricsManager) getPmData(ctx context.Context, groupName string) (*pmMEData, error) {
	if grp, ok := mm.groupMetricMap[groupName]; ok {
		return grp.pmMEData, nil
	}
	// Data not in cache, try to fetch from kv store.
	data := &pmMEData{}
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return data, fmt.Errorf("pmKvStore not set. device-id - %s", mm.pDeviceHandler.deviceID)
	}
	Value, err := mm.pmKvStore.Get(ctx, groupName)
	if err == nil {
		if Value != nil {
			logger.Debugw(ctx, "PM data read",
				log.Fields{"Key": Value.Key, "device-id": mm.pDeviceHandler.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, data); err != nil {
				logger.Errorw(ctx, "unable to unmarshal PM data", log.Fields{"error": err, "device-id": mm.pDeviceHandler.deviceID})
				return data, err
			}
			logger.Debugw(ctx, "PM data", log.Fields{"pmData": data, "groupName": groupName, "device-id": mm.pDeviceHandler.deviceID})
		} else {
			logger.Debugw(ctx, "no PM data found", log.Fields{"groupName": groupName, "device-id": mm.pDeviceHandler.deviceID})
			return data, err
		}
	} else {
		logger.Errorw(ctx, "unable to read from KVstore", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return data, err
	}

	return data, nil
}

// updatePmData update pmMEData to store. It is write through cache, i.e., write to cache first and then update store
func (mm *onuMetricsManager) updatePmData(ctx context.Context, groupName string, meInstanceID uint16, pmAction string) error {
	logger.Debugw(ctx, "updatePmData - start", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": groupName, "entityID": meInstanceID, "pmAction": pmAction})
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()

	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.pDeviceHandler.deviceID))
	}

	pmMEData, err := mm.getPmData(ctx, groupName)
	if err != nil || pmMEData == nil {
		// error already logged in called function.
		return err
	}
	switch pmAction {
	case cPmAdd:
		pmMEData.InstancesToAdd = mm.appendIfMissingUnt16(pmMEData.InstancesToAdd, meInstanceID)
		pmMEData.InstancesToDelete = mm.removeIfFoundUint16(pmMEData.InstancesToDelete, meInstanceID)
		pmMEData.InstancesActive = mm.removeIfFoundUint16(pmMEData.InstancesActive, meInstanceID)
	case cPmAdded:
		pmMEData.InstancesActive = mm.appendIfMissingUnt16(pmMEData.InstancesActive, meInstanceID)
		pmMEData.InstancesToAdd = mm.removeIfFoundUint16(pmMEData.InstancesToAdd, meInstanceID)
		pmMEData.InstancesToDelete = mm.removeIfFoundUint16(pmMEData.InstancesToDelete, meInstanceID)
	case cPmRemove:
		pmMEData.InstancesToDelete = mm.appendIfMissingUnt16(pmMEData.InstancesToDelete, meInstanceID)
		pmMEData.InstancesToAdd = mm.removeIfFoundUint16(pmMEData.InstancesToAdd, meInstanceID)
		pmMEData.InstancesActive = mm.removeIfFoundUint16(pmMEData.InstancesActive, meInstanceID)
	case cPmRemoved:
		pmMEData.InstancesToDelete = mm.removeIfFoundUint16(pmMEData.InstancesToDelete, meInstanceID)
		pmMEData.InstancesToAdd = mm.removeIfFoundUint16(pmMEData.InstancesToAdd, meInstanceID)
		pmMEData.InstancesActive = mm.removeIfFoundUint16(pmMEData.InstancesActive, meInstanceID)
	default:
		logger.Errorw(ctx, "unknown pm action", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pmAction": pmAction, "groupName": groupName})
		return fmt.Errorf(fmt.Sprintf("unknown-pm-action-deviceid-%s-groupName-%s-pmaction-%s", mm.pDeviceHandler.deviceID, groupName, pmAction))
	}
	// write through cache
	mm.groupMetricMap[groupName].pmMEData = pmMEData

	Value, err := json.Marshal(*pmMEData)
	if err != nil {
		logger.Errorw(ctx, "unable to marshal PM data", log.Fields{"groupName": groupName, "pmAction": pmAction, "pmData": *pmMEData, "err": err})
		return err
	}
	// Update back to kv store
	if err = mm.pmKvStore.Put(ctx, groupName, Value); err != nil {
		logger.Errorw(ctx, "unable to put PM data to kv store", log.Fields{"groupName": groupName, "pmData": *pmMEData, "pmAction": pmAction, "err": err})
		return err
	}
	logger.Debugw(ctx, "updatePmData - success", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": groupName, "pmData": *pmMEData, "pmAction": pmAction})

	return nil
}

// clearPmGroupData cleans PM Group data from store
func (mm *onuMetricsManager) clearPmGroupData(ctx context.Context) error {
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "clearPmGroupData - start", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.pDeviceHandler.deviceID))
	}

	for n := range mm.groupMetricMap {
		if err := mm.pmKvStore.Delete(ctx, n); err != nil {
			logger.Errorw(ctx, "clearPmGroupData - fail", log.Fields{"deviceID": mm.pDeviceHandler.deviceID, "groupName": n, "err": err})
			// do not abort this procedure. continue to delete next group.
		} else {
			logger.Debugw(ctx, "clearPmGroupData - success", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": n})
		}
	}

	return nil
}

// clearAllPmData clears all PM data associated with the device from KV store
func (mm *onuMetricsManager) clearAllPmData(ctx context.Context) error {
	mm.onuMetricsManagerLock.Lock()
	defer mm.onuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "clearAllPmData - start", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.pDeviceHandler.deviceID))
	}
	var value error
	for n := range mm.groupMetricMap {
		if err := mm.pmKvStore.Delete(ctx, n); err != nil {
			logger.Errorw(ctx, "clearPmGroupData - fail", log.Fields{"deviceID": mm.pDeviceHandler.deviceID, "groupName": n, "err": err})
			value = err
			// do not abort this procedure - continue to delete next group.
		} else {
			logger.Debugw(ctx, "clearPmGroupData - success", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": n})
		}
	}
	if value == nil {
		logger.Debugw(ctx, "clearAllPmData - success", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	}
	return value
}

func (mm *onuMetricsManager) appendIfMissingString(slice []string, n string) []string {
	for _, ele := range slice {
		if ele == n {
			return slice
		}
	}
	return append(slice, n)
}

func (mm *onuMetricsManager) removeIfFoundString(slice []string, n string) []string {
	for i, ele := range slice {
		if ele == n {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (mm *onuMetricsManager) appendIfMissingUnt16(slice []uint16, n uint16) []uint16 {
	for _, ele := range slice {
		if ele == n {
			return slice
		}
	}
	return append(slice, n)
}

func (mm *onuMetricsManager) removeIfFoundUint16(slice []uint16, n uint16) []uint16 {
	for i, ele := range slice {
		if ele == n {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (mm *onuMetricsManager) twosComplementToSignedInt16(val uint16) int16 {
	var uint16MsbMask uint16 = 0x8000
	if val&uint16MsbMask == uint16MsbMask {
		return int16(^val+1) * -1
	}

	return int16(val)
}

/* // These are need in the future

func (mm *onuMetricsManager) twosComplementToSignedInt32(val uint32) int32 {
	var uint32MsbMask uint32 = 0x80000000
	if val & uint32MsbMask == uint32MsbMask {
		return int32(^val + 1) * -1
	}

	return int32(val)
}

func (mm *onuMetricsManager) twosComplementToSignedInt64(val uint64) int64 {
	var uint64MsbMask uint64 = 0x8000000000000000
	if val & uint64MsbMask == uint64MsbMask {
		return int64(^val + 1) * -1
	}

	return int64(val)
}

*/
