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

//Package pmmgr provides the utilities to manage onu metrics
package pmmgr

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
	"github.com/opencord/voltha-lib-go/v5/pkg/db"
	"github.com/opencord/voltha-lib-go/v5/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v5/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-protos/v4/go/extension"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

// events of L2 PM FSM
const (
	L2PmEventInit     = "L2PmEventInit"
	L2PmEventTick     = "L2PmEventTick"
	L2PmEventSuccess  = "L2PmEventSuccess"
	L2PmEventFailure  = "L2PmEventFailure"
	L2PmEventAddMe    = "L2PmEventAddMe"
	L2PmEventDeleteMe = "L2PmEventDeleteMe"
	L2PmEventStop     = "L2PmEventStop"
)

// states of L2 PM FSM
const (
	L2PmStNull        = "L2PmStNull"
	L2PmStStarting    = "L2PmStStarting"
	L2PmStSyncTime    = "L2PmStSyncTime"
	L2PmStIdle        = "L2PmStIdle"
	L2PmStCreatePmMe  = "L2PmStCreatePm"
	L2PmStDeletePmMe  = "L2PmStDeletePmMe"
	L2PmStCollectData = "L2PmStCollectData"
)

// CL2PmFsmIdleState - TODO: add comment
const CL2PmFsmIdleState = L2PmStIdle

// general constants used for overall Metric Collection management
const (
	DefaultMetricCollectionFrequency = 15 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration
	GroupMetricEnabled               = true    // This is READONLY and cannot be changed from VOLTHA NBI
	DefaultFrequencyOverrideEnabled  = true    // This is READONLY and cannot be changed from VOLTHA NBI
	FrequencyGranularity             = 5       // The frequency (in seconds) has to be multiple of 5. This setting cannot changed later.
)

// constants for ethernet frame extended pm collection
const (
	ExtendedPmCreateAttempts            = 3
	UnsupportedCounterValue32bit uint64 = 4294967294
	UnsupportedCounterValue64bit uint64 = 18446744073709551614
	dropEvents                          = "DropEvents"
	octets                              = "Octets"
	frames                              = "Frames"
	broadcastFrames                     = "BroadcastFrames"
	multicastFrames                     = "MulticastFrames"
	crcErroredFrames                    = "CrcErroredFrames"
	undersizeFrames                     = "UndersizeFrames"
	oversizeFrames                      = "OversizeFrames"
	frames64Octets                      = "Frames64Octets"
	frames65To127Octets                 = "Frames65To127Octets"
	frames128To255Octets                = "Frames128To255Octets"
	frames256To511Octets                = "Frames256To511Octets"
	frames512To1023Octets               = "Frames512To1023Octets"
	frames1024To1518Octets              = "Frames1024To1518Octets"
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
	"uni_port_no":       voltha.PmConfig_CONTEXT,
	"me_class_id":       voltha.PmConfig_CONTEXT,
	"entity_id":         voltha.PmConfig_CONTEXT,
	"configuration_ind": voltha.PmConfig_GAUGE,
	"oper_status":       voltha.PmConfig_GAUGE,
	"uni_admin_state":   voltha.PmConfig_GAUGE,
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

var maskToEthernetFrameExtendedPM32Bit = map[uint16][]string{
	0x3F00: {"drop_events", "octets", "frames", "broadcast_frames", "multicast_frames", "crc_errored_frames"},
	0x00FC: {"undersize_frames", "oversize_frames", "64_octets", "65_to_127_octets", "128_to_255_octets", "256_to_511_octets"},
	0x0003: {"512_to_1023_octets", "1024_to_1518_octets"},
}

var maskToEthernetFrameExtendedPM64Bit = map[uint16][]string{
	0x3800: {"drop_events", "octets", "frames"},
	0x0700: {"broadcast_frames", "multicast_frames", "crc_errored_frames"},
	0x00E0: {"undersize_frames", "oversize_frames", "64_octets"},
	0x001C: {"65_to_127_octets", "128_to_255_octets", "256_to_511_octets"},
	0x0003: {"512_to_1023_octets", "1024_to_1518_octets"},
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
	MaxL2PMGetPayLoadSize            = 24
	MaxEthernetFrameExtPmPayloadSize = 25
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
	cPmKvStorePrefix    = "%s/openonu/pm-data/%s" // <some-base-path>/openonu/pm-data/<onu-device-id>
	cPmAdd              = "add"
	cPmAdded            = "added"
	cPmRemove           = "remove"
	cPmRemoved          = "removed"
	cExtPmKvStorePrefix = "%s/omci_me" //<some-base-path>/omci_me/<onu_vendor>/<onu_equipment_id>/<onu_sw_version>
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
	Enabled                bool
	Frequency              uint32 // valid only if FrequencyOverride is enabled.
	metricMap              map[string]voltha.PmConfig_PmType
	NextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
	IsL2PMCounter          bool      // true for only L2 PM counters
	collectAttempts        uint32    // number of attempts to collect L2 PM data
	pmMEData               *pmMEData
}

type standaloneMetric struct {
	metricName             string
	Enabled                bool
	Frequency              uint32    // valid only if FrequencyOverride is enabled.
	NextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
}

// OnuMetricsManager - TODO: add comment
type OnuMetricsManager struct {
	deviceID        string
	pDeviceHandler  cmn.IdeviceHandler
	pOnuDeviceEntry cmn.IonuDeviceEntry
	PAdaptFsm       *cmn.AdapterFsm

	opticalMetricsChan                   chan me.AttributeValueMap
	uniStatusMetricsChan                 chan me.AttributeValueMap
	l2PmChan                             chan me.AttributeValueMap
	extendedPmMeChan                     chan me.AttributeValueMap
	syncTimeResponseChan                 chan bool       // true is success, false is fail
	l2PmCreateOrDeleteResponseChan       chan bool       // true is success, false is fail
	extendedPMCreateOrDeleteResponseChan chan me.Results // true is sucesss, false is fail

	activeL2Pms  []string // list of active l2 pm MEs created on the ONU.
	l2PmToDelete []string // list of L2 PMs to delete
	l2PmToAdd    []string // list of L2 PM to add

	GroupMetricMap      map[string]*groupMetric
	StandaloneMetricMap map[string]*standaloneMetric

	StopProcessingOmciResponses chan bool
	omciProcessingActive        bool

	StopTicks            chan bool
	tickGenerationActive bool

	NextGlobalMetricCollectionTime time.Time // valid only if pmConfig.FreqOverride is set to false.

	OnuMetricsManagerLock sync.RWMutex

	pmKvStore *db.Backend

	supportedEthernetFrameExtendedPMClass         me.ClassID
	ethernetFrameExtendedPmUpStreamMEByEntityID   map[uint16]*me.ManagedEntity
	ethernetFrameExtendedPmDownStreamMEByEntityID map[uint16]*me.ManagedEntity
	extPmKvStore                                  *db.Backend
	onuEthernetFrameExtendedPmLock                sync.RWMutex
	isDeviceReadyToCollectExtendedPmStats         bool
}

// NewOnuMetricsManager returns a new instance of the NewOnuMetricsManager
// The metrics manager module is responsible for configuration and management of individual and group metrics.
// Currently all the metrics are managed as a group which fall into two categories - L2 PM and "all others"
// The L2 PM counters have a fixed 15min interval for PM collection while all other group counters have
// the collection interval configurable.
// The global PM config is part of the voltha.Device struct and is backed up on KV store (by rw-core).
// This module also implements resiliency for L2 PM ME instances that are active/pending-delete/pending-add.
func NewOnuMetricsManager(ctx context.Context, dh cmn.IdeviceHandler, onuDev cmn.IonuDeviceEntry) *OnuMetricsManager {

	var metricsManager OnuMetricsManager
	metricsManager.deviceID = dh.GetDeviceID()
	logger.Debugw(ctx, "init-OnuMetricsManager", log.Fields{"device-id": metricsManager.deviceID})
	metricsManager.pDeviceHandler = dh
	metricsManager.pOnuDeviceEntry = onuDev

	commMetricsChan := make(chan cmn.Message)
	metricsManager.opticalMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.uniStatusMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.l2PmChan = make(chan me.AttributeValueMap)
	metricsManager.extendedPmMeChan = make(chan me.AttributeValueMap)

	metricsManager.syncTimeResponseChan = make(chan bool)
	metricsManager.l2PmCreateOrDeleteResponseChan = make(chan bool)
	metricsManager.extendedPMCreateOrDeleteResponseChan = make(chan me.Results)

	metricsManager.StopProcessingOmciResponses = make(chan bool)
	metricsManager.StopTicks = make(chan bool)

	metricsManager.GroupMetricMap = make(map[string]*groupMetric)
	metricsManager.StandaloneMetricMap = make(map[string]*standaloneMetric)

	metricsManager.ethernetFrameExtendedPmUpStreamMEByEntityID = make(map[uint16]*me.ManagedEntity)
	metricsManager.ethernetFrameExtendedPmDownStreamMEByEntityID = make(map[uint16]*me.ManagedEntity)

	if dh.GetPmConfigs() == nil { // dh.GetPmConfigs() is NOT nil if adapter comes back from a restart. We should NOT go back to defaults in this case
		metricsManager.initializeAllGroupMetrics()
	}

	metricsManager.populateLocalGroupMetricData(ctx)

	if err := metricsManager.initializeL2PmFsm(ctx, commMetricsChan); err != nil {
		return nil
	}

	// initialize the next metric collection intervals.
	metricsManager.InitializeMetricCollectionTime(ctx)

	baseKvStorePath := fmt.Sprintf(cPmKvStorePrefix, dh.GetBackendPathPrefix(), metricsManager.deviceID)
	metricsManager.pmKvStore = dh.SetBackend(ctx, baseKvStorePath)
	if metricsManager.pmKvStore == nil {
		logger.Errorw(ctx, "Can't initialize pmKvStore - no backend connection to PM module",
			log.Fields{"device-id": metricsManager.deviceID, "service": baseKvStorePath})
		return nil
	}
	// restore data from KV store
	if err := metricsManager.restorePmData(ctx); err != nil {
		logger.Errorw(ctx, "error restoring pm data", log.Fields{"err": err})
		// we continue given that it does not effect the actual services for the ONU,
		// but there may be some negative effect on PM collection (there may be some mismatch in
		// the actual PM config and what is present on the device).
	}

	baseExtPmKvStorePath := fmt.Sprintf(cExtPmKvStorePrefix, dh.GetBackendPathPrefix())
	metricsManager.extPmKvStore = dh.SetBackend(ctx, baseExtPmKvStorePath)
	if metricsManager.extPmKvStore == nil {
		logger.Errorw(ctx, "Can't initialize extPmKvStore - no backend connection to PM module",
			log.Fields{"device-id": metricsManager.deviceID, "service": baseExtPmKvStorePath})
		return nil
	}

	logger.Info(ctx, "init-OnuMetricsManager completed", log.Fields{"device-id": metricsManager.deviceID})
	return &metricsManager
}

// InitializeMetricCollectionTime - TODO: add comment
func (mm *OnuMetricsManager) InitializeMetricCollectionTime(ctx context.Context) {
	if mm.pDeviceHandler.GetPmConfigs().FreqOverride {
		// If mm.pDeviceHandler.GetPmConfigs().FreqOverride is set to true, then group/standalone metric specific interval applies
		mm.OnuMetricsManagerLock.Lock()
		defer mm.OnuMetricsManagerLock.Unlock()
		for _, v := range mm.GroupMetricMap {
			if v.Enabled && !v.IsL2PMCounter { // L2 PM counter collection is managed in a L2PmFsm
				v.NextCollectionInterval = time.Now().Add(time.Duration(v.Frequency) * time.Second)
			}
		}

		for _, v := range mm.StandaloneMetricMap {
			if v.Enabled {
				v.NextCollectionInterval = time.Now().Add(time.Duration(v.Frequency) * time.Second)
			}
		}
	} else {
		// If mm.pDeviceHandler.GetPmConfigs().FreqOverride is set to false, then overall metric specific interval applies
		mm.NextGlobalMetricCollectionTime = time.Now().Add(time.Duration(mm.pDeviceHandler.GetPmConfigs().DefaultFreq) * time.Second)
	}
	logger.Infow(ctx, "initialized standalone group/metric collection time", log.Fields{"device-id": mm.deviceID})
}

// UpdateDefaultFrequency - TODO: add comment
func (mm *OnuMetricsManager) UpdateDefaultFrequency(ctx context.Context, pmConfigs *voltha.PmConfigs) error {
	// Verify that the configured DefaultFrequency is > 0 and is a multiple of FrequencyGranularity
	if pmConfigs.DefaultFreq == 0 || (pmConfigs.DefaultFreq > 0 && pmConfigs.DefaultFreq%FrequencyGranularity != 0) {
		logger.Errorf(ctx, "frequency-%u-should-be-a-multiple-of-%u", pmConfigs.DefaultFreq, FrequencyGranularity)
		return fmt.Errorf("frequency-%d-should-be-a-multiple-of-%d", pmConfigs.DefaultFreq, FrequencyGranularity)
	}
	mm.pDeviceHandler.GetPmConfigs().DefaultFreq = pmConfigs.DefaultFreq
	// re-set the NextGlobalMetricCollectionTime based on the new DefaultFreq
	mm.NextGlobalMetricCollectionTime = time.Now().Add(time.Duration(mm.pDeviceHandler.GetPmConfigs().DefaultFreq) * time.Second)
	logger.Debugw(ctx, "frequency-updated--new-frequency", log.Fields{"device-id": mm.deviceID, "frequency": mm.pDeviceHandler.GetPmConfigs().DefaultFreq})
	return nil
}

// UpdateGroupFreq - TODO: add comment
func (mm *OnuMetricsManager) UpdateGroupFreq(ctx context.Context, aGroupName string, pmConfigs *voltha.PmConfigs) error {
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
		logger.Errorw(ctx, "group name not found", log.Fields{"device-id": mm.deviceID, "groupName": aGroupName})
		return fmt.Errorf("group-name-not-found-%v", aGroupName)
	}

	updated := false
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	for k, v := range mm.GroupMetricMap {
		if k == aGroupName && !v.IsL2PMCounter { // We cannot allow the L2 PM counter frequency to be updated. It is 15min fixed by OMCI spec
			v.Frequency = newGroupFreq
			// update internal pm config
			mm.pDeviceHandler.GetPmConfigs().Groups[groupSliceIdx].GroupFreq = newGroupFreq
			// Also updated the next group metric collection time from now
			v.NextCollectionInterval = time.Now().Add(time.Duration(newGroupFreq) * time.Second)
			updated = true
			logger.Infow(ctx, "group frequency updated", log.Fields{"device-id": mm.deviceID, "newGroupFreq": newGroupFreq, "groupName": aGroupName})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "group frequency not updated", log.Fields{"device-id": mm.deviceID, "newGroupFreq": newGroupFreq, "groupName": aGroupName})
		return fmt.Errorf("internal-error-during-group-freq-update--groupname-%s-freq-%d", aGroupName, newGroupFreq)
	}
	return nil
}

// UpdateMetricFreq - TODO: add comment
func (mm *OnuMetricsManager) UpdateMetricFreq(ctx context.Context, aMetricName string, pmConfigs *voltha.PmConfigs) error {
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
		logger.Errorw(ctx, "metric name not found", log.Fields{"device-id": mm.deviceID, "metricName": aMetricName})
		return fmt.Errorf("metric-name-not-found-%v", aMetricName)
	}

	updated := false
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	for k, v := range mm.GroupMetricMap {
		if k == aMetricName {
			v.Frequency = newMetricFreq
			// update internal pm config
			mm.pDeviceHandler.GetPmConfigs().Metrics[metricSliceIdx].SampleFreq = newMetricFreq
			// Also updated the next standalone metric collection time from now
			v.NextCollectionInterval = time.Now().Add(time.Duration(newMetricFreq) * time.Second)
			updated = true
			logger.Infow(ctx, "metric frequency updated", log.Fields{"device-id": mm.deviceID, "newMetricFreq": newMetricFreq, "aMetricName": aMetricName})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "metric frequency not updated", log.Fields{"device-id": mm.deviceID, "newMetricFreq": newMetricFreq, "aMetricName": aMetricName})
		return fmt.Errorf("internal-error-during-standalone-metric-update--matricnane-%s-freq-%d", aMetricName, newMetricFreq)
	}
	return nil
}

// UpdateGroupSupport - TODO: add comment
func (mm *OnuMetricsManager) UpdateGroupSupport(ctx context.Context, aGroupName string, pmConfigs *voltha.PmConfigs) error {
	groupSliceIdx := 0
	var group *voltha.PmGroupConfig

	for groupSliceIdx, group = range pmConfigs.Groups {
		if group.GroupName == aGroupName {
			break
		}
	}
	if group == nil {
		logger.Errorw(ctx, "group metric not found", log.Fields{"device-id": mm.deviceID, "groupName": aGroupName})
		return fmt.Errorf("group-not-found--groupName-%s", aGroupName)
	}

	updated := false
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	for k, v := range mm.GroupMetricMap {
		if k == aGroupName && v.Enabled != group.Enabled {
			mm.pDeviceHandler.GetPmConfigs().Groups[groupSliceIdx].Enabled = group.Enabled
			v.Enabled = group.Enabled
			if group.Enabled {
				if v.IsL2PMCounter {
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
				} else if mm.pDeviceHandler.GetPmConfigs().FreqOverride { // otherwise just update the next collection interval
					v.NextCollectionInterval = time.Now().Add(time.Duration(v.Frequency) * time.Second)
				}
			} else { // group counter is disabled
				if v.IsL2PMCounter {
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
			if v.IsL2PMCounter {
				logger.Infow(ctx, "l2 pm group metric support updated",
					log.Fields{"device-id": mm.deviceID, "groupName": aGroupName, "enabled": group.Enabled, "l2PmToAdd": mm.l2PmToAdd, "l2PmToDelete": mm.l2PmToDelete})
			} else {
				logger.Infow(ctx, "group metric support updated", log.Fields{"device-id": mm.deviceID, "groupName": aGroupName, "enabled": group.Enabled})
			}
			break
		}
	}

	if !updated {
		logger.Errorw(ctx, "group metric support not updated", log.Fields{"device-id": mm.deviceID, "groupName": aGroupName})
		return fmt.Errorf("internal-error-during-group-support-update--groupName-%s", aGroupName)
	}
	return nil
}

// UpdateMetricSupport - TODO: add comment
func (mm *OnuMetricsManager) UpdateMetricSupport(ctx context.Context, aMetricName string, pmConfigs *voltha.PmConfigs) error {
	metricSliceIdx := 0
	var metric *voltha.PmConfig

	for metricSliceIdx, metric = range pmConfigs.Metrics {
		if metric.Name == aMetricName {
			break
		}
	}

	if metric == nil {
		logger.Errorw(ctx, "standalone metric not found", log.Fields{"device-id": mm.deviceID, "metricName": aMetricName})
		return fmt.Errorf("metric-not-found--metricname-%s", aMetricName)
	}

	updated := false
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	for k, v := range mm.StandaloneMetricMap {
		if k == aMetricName && v.Enabled != metric.Enabled {
			mm.pDeviceHandler.GetPmConfigs().Metrics[metricSliceIdx].Enabled = metric.Enabled
			v.Enabled = metric.Enabled
			// If the standalone metric is now enabled and frequency override is enabled, set the next metric collection time
			if metric.Enabled && mm.pDeviceHandler.GetPmConfigs().FreqOverride {
				v.NextCollectionInterval = time.Now().Add(time.Duration(v.Frequency) * time.Second)
			}
			updated = true
			logger.Infow(ctx, "standalone metric support updated", log.Fields{"device-id": mm.deviceID, "metricName": aMetricName, "enabled": metric.Enabled})
			break
		}
	}
	if !updated {
		logger.Errorw(ctx, "standalone metric support not updated", log.Fields{"device-id": mm.deviceID, "metricName": aMetricName})
		return fmt.Errorf("internal-error-during-standalone-support-update--metricname-%s", aMetricName)
	}
	return nil
}

// CollectAllGroupAndStandaloneMetrics - TODO: add comment
func (mm *OnuMetricsManager) CollectAllGroupAndStandaloneMetrics(ctx context.Context) {
	if mm.pDeviceHandler.GetPmConfigs().Grouped { // metrics are managed as a group.
		go mm.collectAllGroupMetrics(ctx)
	} else {
		go mm.collectAllStandaloneMetrics(ctx)
	}
}

func (mm *OnuMetricsManager) collectAllGroupMetrics(ctx context.Context) {
	go func() {
		logger.Debug(ctx, "startCollector before collecting optical metrics")
		metricInfo, err := mm.collectOpticalMetrics(ctx)
		if err != nil {
			logger.Errorw(ctx, "collectOpticalMetrics failed",
				log.Fields{"device-id": mm.deviceID, "Error": err})
			return
		}
		if metricInfo != nil {
			mm.publishMetrics(ctx, metricInfo)
		}
	}()

	go func() {
		logger.Debug(ctx, "startCollector before collecting uni metrics")
		metricInfo, err := mm.collectUniStatusMetrics(ctx)
		if err != nil {
			logger.Errorw(ctx, "collectOpticalMetrics failed",
				log.Fields{"device-id": mm.deviceID, "Error": err})
			return
		}
		if metricInfo != nil {
			mm.publishMetrics(ctx, metricInfo)
		}
	}()

	// Add more here
}

func (mm *OnuMetricsManager) collectAllStandaloneMetrics(ctx context.Context) {
	// None exists as of now, add when available here
}

// CollectGroupMetric - TODO: add comment
func (mm *OnuMetricsManager) CollectGroupMetric(ctx context.Context, groupName string) {
	switch groupName {
	case OpticalPowerGroupMetricName:
		go func() {
			if mi, _ := mm.collectOpticalMetrics(ctx); mi != nil {
				mm.publishMetrics(ctx, mi)
			}
		}()
	case UniStatusGroupMetricName:
		go func() {
			if mi, _ := mm.collectUniStatusMetrics(ctx); mi != nil {
				mm.publishMetrics(ctx, mi)
			}
		}()
	default:
		logger.Errorw(ctx, "unhandled group metric name", log.Fields{"device-id": mm.deviceID, "groupName": groupName})
	}
}

// CollectStandaloneMetric - TODO: add comment
func (mm *OnuMetricsManager) CollectStandaloneMetric(ctx context.Context, metricName string) {
	switch metricName {
	// None exist as of now, add when available
	default:
		logger.Errorw(ctx, "unhandled standalone metric name", log.Fields{"device-id": mm.deviceID, "metricName": metricName})
	}
}

// collectOpticalMetrics collects groups metrics related to optical power from ani-g ME.
func (mm *OnuMetricsManager) collectOpticalMetrics(ctx context.Context) ([]*voltha.MetricInformation, error) {
	logger.Debugw(ctx, "collectOpticalMetrics", log.Fields{"device-id": mm.deviceID})

	mm.OnuMetricsManagerLock.RLock()
	if !mm.GroupMetricMap[OpticalPowerGroupMetricName].Enabled {
		mm.OnuMetricsManagerLock.RUnlock()
		logger.Debugw(ctx, "optical power group metric is not enabled", log.Fields{"device-id": mm.deviceID})
		return nil, nil
	}
	mm.OnuMetricsManagerLock.RUnlock()

	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetProxyAddress().OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetProxyAddress().ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.GetDeviceType()

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           OpticalPowerGroupMetricName,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.GetLogicalDeviceID(),
		SerialNo:        mm.pDeviceHandler.GetDevice().SerialNumber,
	}

	// get the ANI-G instance IDs
	anigInstKeys := mm.pOnuDeviceEntry.GetOnuDB().GetSortedInstKeys(ctx, me.AniGClassID)
loop:
	for _, anigInstID := range anigInstKeys {
		var meAttributes me.AttributeValueMap
		opticalMetrics := make(map[string]float32)
		// Get the ANI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"OpticalSignalLevel": 0, "TransmitOpticalLevel": 0}
		meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, me.AniGClassID, anigInstID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
		if err != nil {
			logger.Errorw(ctx, "GetMe failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
			_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
			return nil, err
		}

		if meInstance != nil {
			select {
			case meAttributes = <-mm.opticalMetricsChan:
				logger.Debugw(ctx, "received optical metrics", log.Fields{"device-id": mm.deviceID})
			case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for optical metrics", log.Fields{"device-id": mm.deviceID})
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
						opticalMetrics[k] = float32(math.Round((float64(cmn.TwosComplementToSignedInt16(val.(uint16)))/500.0)*10) / 10) // convert to dBm rounded of to single decimal place
					}
				case "receive_power_dBm":
					if val, ok := meAttributes["OpticalSignalLevel"]; ok && val != nil {
						opticalMetrics[k] = float32(math.Round((float64(cmn.TwosComplementToSignedInt16(val.(uint16)))/500.0)*10) / 10) // convert to dBm rounded of to single decimal place
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

	return metricInfoSlice, nil
}

// collectUniStatusMetrics collects UNI status group metric from various MEs (uni-g, pptp and veip).
// nolint: gocyclo
func (mm *OnuMetricsManager) collectUniStatusMetrics(ctx context.Context) ([]*voltha.MetricInformation, error) {
	logger.Debugw(ctx, "collectUniStatusMetrics", log.Fields{"device-id": mm.deviceID})
	mm.OnuMetricsManagerLock.RLock()
	if !mm.GroupMetricMap[UniStatusGroupMetricName].Enabled {
		mm.OnuMetricsManagerLock.RUnlock()
		logger.Debugw(ctx, "uni status group metric is not enabled", log.Fields{"device-id": mm.deviceID})
		return nil, nil
	}
	mm.OnuMetricsManagerLock.RUnlock()

	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetDevice().ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetDevice().ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.GetDeviceType()

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           UniStatusGroupMetricName,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.GetLogicalDeviceID(),
		SerialNo:        mm.pDeviceHandler.GetDevice().SerialNumber,
	}

	// get the UNI-G instance IDs
	unigInstKeys := mm.pOnuDeviceEntry.GetOnuDB().GetSortedInstKeys(ctx, me.UniGClassID)
loop1:
	for _, unigInstID := range unigInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		unigMetrics := make(map[string]float32)
		var meAttributes me.AttributeValueMap
		// Get the UNI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"AdministrativeState": 0}
		meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, me.UniGClassID, unigInstID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
		if err != nil {
			logger.Errorw(ctx, "UNI-G failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
			_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
			return nil, err
		}
		if meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received uni-g metrics", log.Fields{"device-id": mm.deviceID})
			case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.deviceID})
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
				for _, uni := range *mm.pDeviceHandler.GetUniEntityMap() {
					if uni.EntityID == entityID {
						unigMetrics["uni_port_no"] = float32(uni.PortNo)
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
	pptpInstKeys := mm.pOnuDeviceEntry.GetOnuDB().GetSortedInstKeys(ctx, me.PhysicalPathTerminationPointEthernetUniClassID)
loop2:
	for _, pptpInstID := range pptpInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		var meAttributes me.AttributeValueMap
		pptpMetrics := make(map[string]float32)

		requestedAttributes := me.AttributeValueMap{"ConfigurationInd": 0, "OperationalState": 0, "AdministrativeState": 0}
		meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, me.PhysicalPathTerminationPointEthernetUniClassID, pptpInstID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
		if err != nil {
			logger.Errorw(ctx, "GetMe failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
			_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
			return nil, err
		}
		if meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received pptp metrics", log.Fields{"device-id": mm.deviceID})
			case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.deviceID})
				// The metrics could be empty in this case
				break loop2
			}

			// Populate metric only if it was enabled.
			for k := range UniStatusGroupMetrics {
				switch k {
				case "configuration_ind":
					if val, ok := meAttributes["ConfigurationInd"]; ok && val != nil {
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
			for _, uni := range *mm.pDeviceHandler.GetUniEntityMap() {
				if uni.EntityID == entityID {
					pptpMetrics["uni_port_no"] = float32(uni.PortNo)
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
	veipInstKeys := mm.pOnuDeviceEntry.GetOnuDB().GetSortedInstKeys(ctx, me.VirtualEthernetInterfacePointClassID)
loop3:
	for _, veipInstID := range veipInstKeys {
		// TODO: Include additional information in the voltha.MetricMetaData - like portno, uni-id, instance-id
		// to uniquely identify this ME instance and also to correlate the ME instance to physical instance
		var meAttributes me.AttributeValueMap
		veipMetrics := make(map[string]float32)

		requestedAttributes := me.AttributeValueMap{"OperationalState": 0, "AdministrativeState": 0}
		meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, me.VirtualEthernetInterfacePointClassID, veipInstID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
		if err != nil {
			logger.Errorw(ctx, "GetMe failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
			_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
			return nil, err
		}
		if meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received veip metrics", log.Fields{"device-id": mm.deviceID})
			case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.deviceID})
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
			for _, uni := range *mm.pDeviceHandler.GetUniEntityMap() {
				if uni.EntityID == entityID {
					veipMetrics["uni_port_no"] = float32(uni.PortNo)
					break
				}
			}
		}
		veipMetrics["me_class_id"] = float32(me.VirtualEthernetInterfacePointClassID)

		// create slice of metrics given that there could be more than one VEIP instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: veipMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	return metricInfoSlice, nil
}

// publishMetrics publishes the metrics on kafka
func (mm *OnuMetricsManager) publishMetrics(ctx context.Context, metricInfo []*voltha.MetricInformation) {
	var ke voltha.KpiEvent2
	ts := time.Now().Unix()
	ke.SliceData = metricInfo
	ke.Type = voltha.KpiEventType_slice
	ke.Ts = float64(ts)

	if err := mm.pDeviceHandler.GetEventProxy().SendKpiEvent(ctx, "STATS_EVENT", &ke, voltha.EventCategory_EQUIPMENT, voltha.EventSubCategory_ONU, ts); err != nil {
		logger.Errorw(ctx, "failed-to-send-pon-stats", log.Fields{"err": err})
	}
}

// ProcessOmciMessages - TODO: add comment
func (mm *OnuMetricsManager) ProcessOmciMessages(ctx context.Context) {
	logger.Infow(ctx, "Start routine to process OMCI-GET messages for device-id", log.Fields{"device-id": mm.deviceID})
	// Flush metric collection channels to be safe.
	// It is possible that there is stale data on this channel if the ProcessOmciMessages routine
	// is stopped right after issuing a OMCI-GET request and started again.
	// The ProcessOmciMessages routine will get stopped if startCollector routine (in device_handler.go)
	// is stopped - as a result of ONU going down.
	mm.flushMetricCollectionChannels(ctx)
	mm.updateOmciProcessingStatus(true)
	for {
		select {
		case <-mm.StopProcessingOmciResponses: // stop this routine
			logger.Infow(ctx, "Stop routine to process OMCI-GET messages for device-id", log.Fields{"device-id": mm.deviceID})
			mm.updateOmciProcessingStatus(false)
			return
		case message, ok := <-mm.PAdaptFsm.CommChan:
			if !ok {
				logger.Errorw(ctx, "Message couldn't be read from channel", log.Fields{"device-id": mm.deviceID})
				continue
			}
			logger.Debugw(ctx, "Received message on ONU metrics channel", log.Fields{"device-id": mm.deviceID})

			switch message.Type {
			case cmn.OMCI:
				msg, _ := message.Data.(cmn.OmciMessage)
				mm.handleOmciMessage(ctx, msg)
			default:
				logger.Warn(ctx, "Unknown message type received", log.Fields{"device-id": mm.deviceID, "message.Type": message.Type})
			}
		}
	}
}

func (mm *OnuMetricsManager) handleOmciMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "omci Msg", log.Fields{"device-id": mm.deviceID,
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
	case omci.GetCurrentDataResponseType:
		_ = mm.handleOmciGetCurrentDataResponseMessage(ctx, msg)
	default:
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})

	}
}

func (mm *OnuMetricsManager) handleOmciGetResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetResponse - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling stopped: %s", mm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetResponse - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling stopped: %s", mm.deviceID)
	}
	logger.Debugw(ctx, "OMCI GetResponse Data", log.Fields{"device-id": mm.deviceID, "data-fields": msgObj, "result": msgObj.Result})
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
			return nil
		case me.EthernetFrameExtendedPmClassID,
			me.EthernetFrameExtendedPm64BitClassID:
			mm.extendedPmMeChan <- meAttributes
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci get response message",
				log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
		}
	} else {
		meAttributes := msgObj.Attributes
		switch msgObj.EntityClass {
		case me.EthernetFrameExtendedPmClassID,
			me.EthernetFrameExtendedPm64BitClassID:
			// not all counters may be supported in which case we have seen some ONUs throwing
			// AttributeFailure error code, while correctly populating other counters it supports
			mm.extendedPmMeChan <- meAttributes
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci get response message",
				log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
		}
	}
	return fmt.Errorf("unhandled-omci-get-response-message")
}

func (mm *OnuMetricsManager) handleOmciGetCurrentDataResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetCurrentDataResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetCurrentDataResponse - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for GetCurrentDataResponse - handling stopped: %s", mm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetCurrentDataResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetCurrentDataResponse - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for GetCurrentDataResponse - handling stopped: %s", mm.deviceID)
	}
	logger.Debugw(ctx, "OMCI GetCurrentDataResponse Data", log.Fields{"device-id": mm.deviceID, "data-fields": msgObj, "result": msgObj.Result})
	if msgObj.Result == me.Success {
		meAttributes := msgObj.Attributes
		switch msgObj.EntityClass {
		case me.EthernetFrameExtendedPmClassID,
			me.EthernetFrameExtendedPm64BitClassID:
			mm.extendedPmMeChan <- meAttributes
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci get current data response message",
				log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
		}
	} else {
		meAttributes := msgObj.Attributes
		switch msgObj.EntityClass {
		case me.EthernetFrameExtendedPmClassID,
			me.EthernetFrameExtendedPm64BitClassID:
			// not all counters may be supported in which case we have seen some ONUs throwing
			// AttributeFailure error code, while correctly populating other counters it supports
			mm.extendedPmMeChan <- meAttributes
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci get current data response message",
				log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
		}
	}
	return fmt.Errorf("unhandled-omci-get-current-data-response-message")
}

func (mm *OnuMetricsManager) handleOmciSynchronizeTimeResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSynchronizeTimeResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for synchronize time response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for synchronize time response - handling stopped: %s", mm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.SynchronizeTimeResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for synchronize time response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for synchronize time response - handling stopped: %s", mm.deviceID)
	}
	logger.Debugw(ctx, "OMCI synchronize time response Data", log.Fields{"device-id": mm.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		switch msgObj.EntityClass {
		case me.OnuGClassID:
			logger.Infow(ctx, "omci synchronize time success", log.Fields{"device-id": mm.deviceID})
			mm.syncTimeResponseChan <- true
			return nil
		default:
			logger.Errorw(ctx, "unhandled omci message",
				log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
		}
	}
	mm.syncTimeResponseChan <- false
	logger.Errorf(ctx, "unhandled-omci-synchronize-time-response-message--error-code-%v", msgObj.Result)
	return fmt.Errorf("unhandled-omci-synchronize-time-response-message--error-code-%v", msgObj.Result)
}

// flushMetricCollectionChannels flushes all metric collection channels for any stale OMCI responses
func (mm *OnuMetricsManager) flushMetricCollectionChannels(ctx context.Context) {
	// flush commMetricsChan
	select {
	case <-mm.PAdaptFsm.CommChan:
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

	// flush StopTicks
	select {
	case <-mm.StopTicks:
		logger.Debug(ctx, "flushed StopTicks channel")
	default:
	}

}

// ** L2 PM FSM Handlers start **

func (mm *OnuMetricsManager) l2PMFsmStarting(ctx context.Context, e *fsm.Event) {

	// Loop through all the group metrics
	// If it is a L2 PM Interval metric and it is enabled, then if it is not in the
	// list of active L2 PM list then mark it for creation
	// It it is a L2 PM Interval metric and it is disabled, then if it is in the
	// list of active L2 PM list then mark it for deletion
	mm.OnuMetricsManagerLock.Lock()
	for n, g := range mm.GroupMetricMap {
		if g.IsL2PMCounter { // it is a l2 pm counter
			if g.Enabled { // metric enabled.
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
	mm.OnuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "pms to add and delete",
		log.Fields{"device-id": mm.deviceID, "pms-to-add": mm.l2PmToAdd, "pms-to-delete": mm.l2PmToDelete})
	go func() {
		// push a tick event to move to next state
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventTick); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
}

func (mm *OnuMetricsManager) l2PMFsmSyncTime(ctx context.Context, e *fsm.Event) {
	// Sync time with the ONU to establish 15min boundary for PM collection.
	if err := mm.syncTime(ctx); err != nil {
		go func() {
			time.Sleep(SyncTimeRetryInterval * time.Second) // retry to sync time after this timeout
			// This will result in FSM attempting to sync time again
			if err := mm.PAdaptFsm.PFsm.Event(L2PmEventFailure); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
			}
		}()
	}
	// Initiate a tick generation routine every L2PmCollectionInterval
	go mm.generateTicks(ctx)

	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
}

func (mm *OnuMetricsManager) l2PMFsmNull(ctx context.Context, e *fsm.Event) {
	// We need to reset the local data so that the L2 PM MEs are re-provisioned once the ONU is back up based on the latest PM CONFIG
	mm.OnuMetricsManagerLock.Lock()
	mm.activeL2Pms = nil
	mm.l2PmToAdd = nil
	mm.l2PmToDelete = nil
	mm.OnuMetricsManagerLock.Unlock()
	// If the FSM was stopped, then clear PM data from KV store
	// The FSM is stopped when ONU goes down. It is time to clear its data from store
	if e.Event == L2PmEventStop {
		_ = mm.clearPmGroupData(ctx) // ignore error
	}

}
func (mm *OnuMetricsManager) l2PMFsmIdle(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "Enter state idle", log.Fields{"device-id": mm.deviceID})

	mm.OnuMetricsManagerLock.RLock()
	numOfPmToDelete := len(mm.l2PmToDelete)
	numOfPmToAdd := len(mm.l2PmToAdd)
	mm.OnuMetricsManagerLock.RUnlock()

	if numOfPmToDelete > 0 {
		logger.Debugw(ctx, "state idle - pms to delete", log.Fields{"device-id": mm.deviceID, "pms-to-delete": numOfPmToDelete})
		go func() {
			if err := mm.PAdaptFsm.PFsm.Event(L2PmEventDeleteMe); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
			}
		}()
	} else if numOfPmToAdd > 0 {
		logger.Debugw(ctx, "state idle - pms to add", log.Fields{"device-id": mm.deviceID, "pms-to-add": numOfPmToAdd})
		go func() {
			if err := mm.PAdaptFsm.PFsm.Event(L2PmEventAddMe); err != nil {
				logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
			}
		}()
	}
}

func (mm *OnuMetricsManager) l2PmFsmCollectData(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "state collect data", log.Fields{"device-id": mm.deviceID})
	// Copy the activeL2Pms for which we want to collect the metrics since activeL2Pms can change dynamically
	mm.OnuMetricsManagerLock.RLock()
	copyOfActiveL2Pms := make([]string, len(mm.activeL2Pms))
	_ = copy(copyOfActiveL2Pms, mm.activeL2Pms)
	mm.OnuMetricsManagerLock.RUnlock()

	for _, n := range copyOfActiveL2Pms {
		var metricInfoSlice []*voltha.MetricInformation

		// mm.GroupMetricMap[n].pmMEData.InstancesActive could dynamically change, so make a copy
		mm.OnuMetricsManagerLock.RLock()
		copyOfEntityIDs := make([]uint16, len(mm.GroupMetricMap[n].pmMEData.InstancesActive))
		_ = copy(copyOfEntityIDs, mm.GroupMetricMap[n].pmMEData.InstancesActive)
		mm.OnuMetricsManagerLock.RUnlock()

		switch n {
		case EthernetBridgeHistoryName:
			logger.Debugw(ctx, "state collect data - collecting data for EthernetFramePerformanceMonitoringHistoryData ME", log.Fields{"device-id": mm.deviceID})
			for _, entityID := range copyOfEntityIDs {
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, true, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, false, entityID); metricInfo != nil { // downstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}
		case EthernetUniHistoryName:
			logger.Debugw(ctx, "state collect data - collecting data for EthernetPerformanceMonitoringHistoryData ME", log.Fields{"device-id": mm.deviceID})
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
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.deviceID, "name": n})
		}
		mm.handleMetricsPublish(ctx, n, metricInfoSlice)
	}
	// Does not matter we send success or failure here.
	// Those PMs that we failed to collect data will be attempted to collect again in the next PM collection cycle (assuming
	// we have not exceed max attempts to collect the PM data)
	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
}

// nolint: gocyclo
func (mm *OnuMetricsManager) l2PmFsmCreatePM(ctx context.Context, e *fsm.Event) error {
	// Copy the l2PmToAdd for which we want to collect the metrics since l2PmToAdd can change dynamically
	mm.OnuMetricsManagerLock.RLock()
	copyOfL2PmToAdd := make([]string, len(mm.l2PmToAdd))
	_ = copy(copyOfL2PmToAdd, mm.l2PmToAdd)
	mm.OnuMetricsManagerLock.RUnlock()

	logger.Debugw(ctx, "state create pm - start", log.Fields{"device-id": mm.deviceID, "pms-to-add": copyOfL2PmToAdd})
	for _, n := range copyOfL2PmToAdd {
		resp := false
		atLeastOneSuccess := false // flag indicates if at least one ME instance of the PM was successfully created.
		cnt := 0
		switch n {
		case EthernetBridgeHistoryName:
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range []bool{true, false} {
				for _, uniPort := range *mm.pDeviceHandler.GetUniEntityMap() {
					// Attach the EthernetFramePerformanceMonitoringHistoryData ME to MacBridgePortConfigData on the UNI port
					entityID := cmn.MacBridgePortAniBaseEID + uniPort.EntityID
					_ = mm.updatePmData(ctx, n, entityID, cPmAdd) // TODO: ignore error for now
				inner1:
					// retry L2PmCreateAttempts times to create the instance of PM
					for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
						_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
							ctx, mm.pDeviceHandler.GetOmciTimeout(), true, direction, true, mm.PAdaptFsm.CommChan, entityID)
						if err != nil {
							logger.Errorw(ctx, "EthernetPerformanceMonitoringHistoryME create or delete failed, failure PM FSM!",
								log.Fields{"device-id": mm.deviceID})
							pPMFsm := mm.PAdaptFsm
							if pPMFsm != nil {
								go func(p_pmFsm *cmn.AdapterFsm) {
									_ = p_pmFsm.PFsm.Event(L2PmEventFailure)
								}(pPMFsm)
							}
							return fmt.Errorf(fmt.Sprintf("CreateOrDeleteEthernetPerformanceMonitoringHistoryMe-failed-%s-%s",
								mm.deviceID, err))
						}
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
			for _, uniPort := range *mm.pDeviceHandler.GetUniEntityMap() {
				if uniPort.PortType == cmn.UniPPTP { // This metric is only applicable for PPTP Uni Type
					// Attach the EthernetPerformanceMonitoringHistoryData ME to PPTP port instance
					entityID := uniPort.EntityID
					_ = mm.updatePmData(ctx, n, entityID, cPmAdd) // TODO: ignore error for now
				inner2:
					// retry L2PmCreateAttempts times to create the instance of PM
					for cnt = 0; cnt < L2PmCreateAttempts; cnt++ {
						_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteEthernetUniHistoryME(
							ctx, mm.pDeviceHandler.GetOmciTimeout(), true, true, mm.PAdaptFsm.CommChan, entityID)
						if err != nil {
							logger.Errorw(ctx, "CreateOrDeleteEthernetUNIHistoryME failed, failure PM FSM!",
								log.Fields{"device-id": mm.deviceID})
							_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
							return fmt.Errorf(fmt.Sprintf("CreateOrDeleteEthernetUniHistoryMe-failed-%s-%s",
								mm.deviceID, err))
						}
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
			for _, anigInstID := range mm.pOnuDeviceEntry.GetOnuDB().GetSortedInstKeys(ctx, me.AniGClassID) {
				// Attach the FecPerformanceMonitoringHistoryData ME to the ANI-G ME instance
				_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteFecHistoryME(
					ctx, mm.pDeviceHandler.GetOmciTimeout(), true, true, mm.PAdaptFsm.CommChan, anigInstID)
				if err != nil {
					logger.Errorw(ctx, "CreateOrDeleteFecHistoryME failed, failure PM FSM!",
						log.Fields{"device-id": mm.deviceID})
					_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
					return fmt.Errorf(fmt.Sprintf("CreateOrDeleteFecHistoryMe-failed-%s-%s",
						mm.deviceID, err))
				}
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

			mm.OnuMetricsManagerLock.RLock()
			copyOfGemPortInstIDsToAdd := make([]uint16, len(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd))
			_ = copy(copyOfGemPortInstIDsToAdd, mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd)
			mm.OnuMetricsManagerLock.RUnlock()

			if len(copyOfGemPortInstIDsToAdd) == 0 {
				// If there are no gemport history MEs to be created, just skip further processing
				// Otherwise down below (after 'switch' case handling) we assume the ME creation failed because resp and atLeastOneSuccess flag are false.
				// Normally there are no GemPortHistory MEs to create at start up. They come in only after provisioning service on the ONU.
				mm.OnuMetricsManagerLock.Lock()
				mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)
				mm.OnuMetricsManagerLock.Unlock()
				continue
			}

			for _, v := range copyOfGemPortInstIDsToAdd {
				_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteGemPortHistoryME(
					ctx, mm.pDeviceHandler.GetOmciTimeout(), true, true, mm.PAdaptFsm.CommChan, v)
				if err != nil {
					logger.Errorw(ctx, "CreateOrDeleteGemPortHistoryME failed, failure PM FSM!",
						log.Fields{"device-id": mm.deviceID})
					_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
					return fmt.Errorf(fmt.Sprintf("CreateOrDeleteGemPortHistoryMe-failed-%s-%s",
						mm.deviceID, err))
				}
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
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.deviceID, "name": n})
		}
		// On success of at least one instance of the PM for a given ME, update the local list maintained for active PMs and PMs to add
		if atLeastOneSuccess {
			mm.OnuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.appendIfMissingString(mm.activeL2Pms, n)
			// gem ports can be added dynamically for perf monitoring. We want to clear the GemPortHistoryName from mm.l2PmToAdd
			// only if no more new gem port instances created.
			if n != GemPortHistoryName || (n == GemPortHistoryName && len(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd) == 0) {
				mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)
			}
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
			mm.OnuMetricsManagerLock.Unlock()
		} else {
			// If we are here then no instance of the PM of the given ME were created successfully, so locally disable the PM
			// and also remove it from l2PmToAdd slice so that we do not try to create the PM ME anymore
			mm.OnuMetricsManagerLock.Lock()
			logger.Debugw(ctx, "exceeded-max-add-retry-attempts--disabling-group", log.Fields{"groupName": n})
			mm.GroupMetricMap[n].Enabled = false
			mm.l2PmToAdd = mm.removeIfFoundString(mm.l2PmToAdd, n)

			logger.Warnw(ctx, "state create pm - failed to create pm",
				log.Fields{"device-id": mm.deviceID, "metricName": n,
					"active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
			mm.OnuMetricsManagerLock.Unlock()
		}
	}
	mm.OnuMetricsManagerLock.RLock()
	logger.Debugw(ctx, "state create pm - done", log.Fields{"device-id": mm.deviceID, "active-l2-pms": mm.activeL2Pms, "pms-to-add": mm.l2PmToAdd})
	mm.OnuMetricsManagerLock.RUnlock()
	// Does not matter we send success or failure here.
	// Those PMs that we failed to create will be attempted to create again in the next PM creation cycle (assuming
	// we have not exceed max attempts to create the PM ME)
	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
	return nil
}

// nolint: gocyclo
func (mm *OnuMetricsManager) l2PmFsmDeletePM(ctx context.Context, e *fsm.Event) error {
	// Copy the l2PmToDelete for which we want to collect the metrics since l2PmToDelete can change dynamically
	mm.OnuMetricsManagerLock.RLock()
	copyOfL2PmToDelete := make([]string, len(mm.l2PmToDelete))
	_ = copy(copyOfL2PmToDelete, mm.l2PmToDelete)
	mm.OnuMetricsManagerLock.RUnlock()

	logger.Debugw(ctx, "state delete pm", log.Fields{"device-id": mm.deviceID, "pms-to-delete": copyOfL2PmToDelete})
	for _, n := range copyOfL2PmToDelete {
		resp := false
		cnt := 0
		atLeastOneDeleteFailure := false

		// mm.GroupMetricMap[n].pmMEData.InstancesActive could dynamically change, so make a copy
		mm.OnuMetricsManagerLock.RLock()
		copyOfEntityIDs := make([]uint16, len(mm.GroupMetricMap[n].pmMEData.InstancesActive))
		_ = copy(copyOfEntityIDs, mm.GroupMetricMap[n].pmMEData.InstancesActive)
		mm.OnuMetricsManagerLock.RUnlock()

		if len(copyOfEntityIDs) == 0 {
			// if there are no enityIDs to remove for the PM ME just clear the PM name entry from cache and continue
			mm.OnuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.OnuMetricsManagerLock.Unlock()
			continue
		}
		logger.Debugw(ctx, "entities to delete", log.Fields{"device-id": mm.deviceID, "metricName": n, "entityIDs": copyOfEntityIDs})
		switch n {
		case EthernetBridgeHistoryName:
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range []bool{true, false} {
				for _, entityID := range copyOfEntityIDs {
				inner1:
					// retry L2PmDeleteAttempts times to delete the instance of PM
					for cnt = 0; cnt < L2PmDeleteAttempts; cnt++ {
						_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
							ctx, mm.pDeviceHandler.GetOmciTimeout(), true, direction, false, mm.PAdaptFsm.CommChan, entityID)
						if err != nil {
							logger.Errorw(ctx, "CreateOrDeleteEthernetPerformanceMonitoringHistoryME failed, failure PM FSM!",
								log.Fields{"device-id": mm.deviceID})
							pPMFsm := mm.PAdaptFsm
							if pPMFsm != nil {
								go func(p_pmFsm *cmn.AdapterFsm) {
									_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
								}(pPMFsm)
							}
							return fmt.Errorf(fmt.Sprintf("CreateOrDeleteEthernetPerformanceMonitoringHistoryMe-failed-%s-%s",
								mm.deviceID, err))
						}
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
					_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteEthernetUniHistoryME(
						ctx, mm.pDeviceHandler.GetOmciTimeout(), true, false, mm.PAdaptFsm.CommChan, entityID)
					if err != nil {
						logger.Errorw(ctx, "CreateOrDeleteEthernetUniHistoryME failed, failure PM FSM!",
							log.Fields{"device-id": mm.deviceID})
						pmFsm := mm.PAdaptFsm
						if pmFsm != nil {
							go func(p_pmFsm *cmn.AdapterFsm) {
								_ = p_pmFsm.PFsm.Event(L2PmEventFailure)
							}(pmFsm)
							return err
						}
						return fmt.Errorf(fmt.Sprintf("CreateOrDeleteEthernetUniHistoryMe-failed-%s-%s",
							mm.deviceID, err))
					}
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
					_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteFecHistoryME(
						ctx, mm.pDeviceHandler.GetOmciTimeout(), true, false, mm.PAdaptFsm.CommChan, entityID)
					if err != nil {
						logger.Errorw(ctx, "CreateOrDeleteFecHistoryME failed, failure PM FSM!",
							log.Fields{"device-id": mm.deviceID})
						_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
						return fmt.Errorf(fmt.Sprintf("CreateOrDeleteFecHistoryMe-failed-%s-%s",
							mm.deviceID, err))
					}
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
					_, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteGemPortHistoryME(
						ctx, mm.pDeviceHandler.GetOmciTimeout(), true, false, mm.PAdaptFsm.CommChan, entityID)
					if err != nil {
						logger.Errorw(ctx, "CreateOrDeleteGemPortHistoryME failed, failure PM FSM!",
							log.Fields{"device-id": mm.deviceID})
						_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
						return fmt.Errorf(fmt.Sprintf("CreateOrDeleteGemPortHistoryMe-failed-%s-%s",
							mm.deviceID, err))
					}
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
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.deviceID, "name": n})
		}
		// If we could not completely clean up the PM ME then just give up.
		if atLeastOneDeleteFailure {
			logger.Warnw(ctx, "state delete pm - failed to delete at least one instance of the PM ME",
				log.Fields{"device-id": mm.deviceID, "metricName": n,
					"active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.OnuMetricsManagerLock.Lock()
			logger.Debugw(ctx, "exceeded-max-delete-retry-attempts--disabling-group", log.Fields{"groupName": n})
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			mm.GroupMetricMap[n].Enabled = false
			mm.OnuMetricsManagerLock.Unlock()
		} else { // success case
			mm.OnuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, n)
			// gem ports can be deleted dynamically from perf monitoring. We want to clear the GemPortHistoryName from mm.l2PmToDelete
			// only if no more new gem port instances removed.
			if n != GemPortHistoryName || (n == GemPortHistoryName && len(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete) == 0) {
				mm.l2PmToDelete = mm.removeIfFoundString(mm.l2PmToDelete, n)
			}
			logger.Debugw(ctx, "success-resp", log.Fields{"pm-name": n, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
			mm.OnuMetricsManagerLock.Unlock()
		}
	}
	mm.OnuMetricsManagerLock.RLock()
	logger.Debugw(ctx, "state delete pm - done", log.Fields{"device-id": mm.deviceID, "active-l2-pms": mm.activeL2Pms, "pms-to-delete": mm.l2PmToDelete})
	mm.OnuMetricsManagerLock.RUnlock()
	// Does not matter we send success or failure here.
	// Those PMs that we failed to delete will be attempted to create again in the next PM collection cycle
	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
	return nil
}

// ** L2 PM FSM Handlers end **

// syncTime synchronizes time with the ONU to establish a 15 min boundary for PM collection and reporting.
func (mm *OnuMetricsManager) syncTime(ctx context.Context) error {
	if err := mm.pOnuDeviceEntry.GetDevOmciCC().SendSyncTime(ctx, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan); err != nil {
		logger.Errorw(ctx, "cannot send sync time request", log.Fields{"device-id": mm.deviceID})
		return err
	}

	select {
	case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "timed out waiting for sync time response from onu", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("timed-out-waiting-for-sync-time-response-%v", mm.deviceID)
	case syncTimeRes := <-mm.syncTimeResponseChan:
		if !syncTimeRes {
			return fmt.Errorf("failed-to-sync-time-%v", mm.deviceID)
		}
		logger.Infow(ctx, "sync time success", log.Fields{"device-id": mm.deviceID})
		return nil
	}
}

func (mm *OnuMetricsManager) collectEthernetFramePerformanceMonitoringHistoryData(ctx context.Context, upstream bool, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData", log.Fields{"device-id": mm.deviceID, "entityID": entityID, "upstream": upstream})
	meParam := me.ParamData{EntityID: entityID}
	if upstream {
		if mEnt, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataUpstream(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
			logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.deviceID, "entityID": entityID, "upstream": upstream})
			return nil
		}
		classID = me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID
	} else {
		if mEnt, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataDownstream(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
			logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.deviceID, "entityID": entityID, "upstream": upstream})
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
		log.Fields{"device-id": mm.deviceID, "entityID": entityID, "upstream": upstream, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *OnuMetricsManager) collectEthernetUniHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewEthernetPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
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
		log.Fields{"device-id": mm.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *OnuMetricsManager) collectFecHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for FecPerformanceMonitoringHistoryData", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewFecPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
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
		log.Fields{"device-id": mm.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

func (mm *OnuMetricsManager) collectGemHistoryData(ctx context.Context, entityID uint16) *voltha.MetricInformation {
	var mEnt *me.ManagedEntity
	var omciErr me.OmciErrors
	var classID me.ClassID
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "collecting data for GemPortNetworkCtpPerformanceMonitoringHistoryData", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
	meParam := me.ParamData{EntityID: entityID}
	if mEnt, omciErr = me.NewGemPortNetworkCtpPerformanceMonitoringHistoryData(meParam); omciErr == nil || mEnt == nil || omciErr.GetError() != nil {
		logger.Errorw(ctx, "error creating me", log.Fields{"device-id": mm.deviceID, "entityID": entityID})
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
		log.Fields{"device-id": mm.deviceID, "entityID": entityID, "metricInfo": metricInfo})
	return &metricInfo
}

// nolint: gocyclo
func (mm *OnuMetricsManager) populateEthernetBridgeHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, ethPMHistData map[string]float32, intervalEndTime *int) error {
	upstream := false
	if classID == me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID {
		upstream = true
	}
	// Insert "IntervalEndTime" as part of the requested attributes as we need this to compare the get responses when get request is multipart
	requestedAttributes["IntervalEndTime"] = 0
	meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "GetME failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
		pmFsm := mm.PAdaptFsm
		if pmFsm != nil {
			go func(p_pmFsm *cmn.AdapterFsm) {
				_ = p_pmFsm.PFsm.Event(L2PmEventFailure)
			}(pmFsm)
			return err
		}
		return fmt.Errorf(fmt.Sprintf("GetME-failed-%s-%s", mm.deviceID, err))
	}
	if meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received ethernet pm history data metrics",
				log.Fields{"device-id": mm.deviceID, "upstream": upstream, "entityID": entityID})
		case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for ethernet pm history data",
				log.Fields{"device-id": mm.deviceID, "upstream": upstream, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-ethernet-bridge-history-%v", mm.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-ethernet-bridge-history-%v", mm.deviceID)
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
func (mm *OnuMetricsManager) populateEthernetUniHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, ethPMUniHistData map[string]float32, intervalEndTime *int) error {
	// Insert "IntervalEndTime" as part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "GetMe failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
		_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
		return fmt.Errorf(fmt.Sprintf("GetME-failed-%s-%s", mm.deviceID, err))
	}
	if meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received ethernet uni history data metrics",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
		case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for ethernet uni history data",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-ethernet-uni-history-%v", mm.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-ethernet-uni-history-%v", mm.deviceID)
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
func (mm *OnuMetricsManager) populateFecHistoryMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, fecHistData map[string]float32, intervalEndTime *int) error {
	// Insert "IntervalEndTime" as part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "GetMe failed, failure PM FSM!", log.Fields{"device-id": mm.deviceID})
		_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
		return fmt.Errorf(fmt.Sprintf("GetME-failed-%s-%s", mm.deviceID, err))
	}
	if meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received fec history data metrics",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
		case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for fec history data",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-fec-history-%v", mm.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-fec-history-%v", mm.deviceID)
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
func (mm *OnuMetricsManager) populateGemPortMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, gemPortHistData map[string]float32, intervalEndTime *int) error {
	// Insert "IntervalEndTime" is part of the requested attributes as we need this to compare the get responses when get request is multipart
	if _, ok := requestedAttributes["IntervalEndTime"]; !ok {
		requestedAttributes["IntervalEndTime"] = 0
	}
	meInstance, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMe(ctx, classID, entityID, requestedAttributes, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "GetMe failed", log.Fields{"device-id": mm.deviceID})
		_ = mm.PAdaptFsm.PFsm.Event(L2PmEventFailure)
		return fmt.Errorf(fmt.Sprintf("GetME-failed-%s-%s", mm.deviceID, err))
	}
	if meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received gem port history data metrics",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
		case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for gem port history data",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID})
			// The metrics will be empty in this case
			return fmt.Errorf("timeout-during-l2-pm-collection-for-gemport-history-%v", mm.deviceID)
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if valid := mm.updateAndValidateIntervalEndTime(ctx, entityID, meAttributes, intervalEndTime); !valid {
			return fmt.Errorf("interval-end-time-changed-during-metric-collection-for-gemport-history-%v", mm.deviceID)
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

func (mm *OnuMetricsManager) handleOmciCreateResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for create response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for create response - handling stopped: %s", mm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for create response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for delete response - handling stopped: %s", mm.deviceID)
	}
	logger.Debugw(ctx, "OMCI create response Data", log.Fields{"device-id": mm.deviceID, "data-fields": msgObj})
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
			logger.Warnw(ctx, "failed to create me", log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
			mm.l2PmCreateOrDeleteResponseChan <- false
		}
		return nil
	case me.EthernetFrameExtendedPmClassID,
		me.EthernetFrameExtendedPm64BitClassID:
		mm.extendedPMCreateOrDeleteResponseChan <- msgObj.Result
		return nil
	default:
		logger.Errorw(ctx, "unhandled omci create response message",
			log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
	}
	return fmt.Errorf("unhandled-omci-create-response-message-%v", mm.deviceID)
}

func (mm *OnuMetricsManager) handleOmciDeleteResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeDeleteResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for delete response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be detected for create response - handling stopped: %s", mm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.DeleteResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for delete response - handling stopped", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf("omci Msg layer could not be assigned for delete response - handling stopped: %s", mm.deviceID)
	}
	logger.Debugw(ctx, "OMCI delete response Data", log.Fields{"device-id": mm.deviceID, "data-fields": msgObj})
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
			logger.Warnw(ctx, "failed to delete me", log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
			mm.l2PmCreateOrDeleteResponseChan <- false
		}
		return nil
	default:
		logger.Errorw(ctx, "unhandled omci delete response message",
			log.Fields{"device-id": mm.deviceID, "class-id": msgObj.EntityClass})
	}
	return fmt.Errorf("unhandled-omci-delete-response-message-%v", mm.deviceID)
}

func (mm *OnuMetricsManager) generateTicks(ctx context.Context) {
	mm.updateTickGenerationStatus(true)
	for {
		select {
		case <-time.After(L2PmCollectionInterval * time.Second):
			go func() {
				if err := mm.PAdaptFsm.PFsm.Event(L2PmEventTick); err != nil {
					logger.Errorw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
				}
			}()
		case <-mm.StopTicks:
			logger.Infow(ctx, "stopping ticks", log.Fields{"device-id": mm.deviceID})
			mm.updateTickGenerationStatus(false)
			return
		}
	}
}

func (mm *OnuMetricsManager) handleMetricsPublish(ctx context.Context, metricName string, metricInfoSlice []*voltha.MetricInformation) {
	// Publish metrics if it is valid
	if metricInfoSlice != nil {
		mm.publishMetrics(ctx, metricInfoSlice)
	} else {
		// If collectAttempts exceeds L2PmCollectAttempts then remove it from activeL2Pms
		// slice so that we do not collect data from that PM ME anymore
		mm.OnuMetricsManagerLock.Lock()
		mm.GroupMetricMap[metricName].collectAttempts++
		if mm.GroupMetricMap[metricName].collectAttempts > L2PmCollectAttempts {
			mm.activeL2Pms = mm.removeIfFoundString(mm.activeL2Pms, metricName)
		}
		logger.Warnw(ctx, "state collect data - no metrics collected",
			log.Fields{"device-id": mm.deviceID, "metricName": metricName, "collectAttempts": mm.GroupMetricMap[metricName].collectAttempts})
		mm.OnuMetricsManagerLock.Unlock()
	}
}

func (mm *OnuMetricsManager) populateGroupSpecificMetrics(ctx context.Context, mEnt *me.ManagedEntity, classID me.ClassID, entityID uint16,
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
		if v.Name == "ManagedEntityId" || v.Name == "IntervalEndTime" || v.Name == "ThresholdData12Id" {
			// Exclude the ManagedEntityId , it will be inserted by omci library based on 'entityID' information
			// Exclude IntervalEndTime. It will be inserted by the group PM populater function.
			// Exclude ThresholdData12Id as that is of no particular relevance for metrics collection.
			continue
		}
		if (v.Size + size) <= MaxL2PMGetPayLoadSize {
			requestedAttributes[v.Name] = v.DefValue
			size = v.Size + size
		} else { // We exceeded the allow omci get size
			// Let's collect the attributes via get now and collect remaining in the next iteration
			if err := grpFunc(ctx, classID, entityID, meAttributes, requestedAttributes, data, intervalEndTime); err != nil {
				logger.Errorw(ctx, "error during metric collection",
					log.Fields{"device-id": mm.deviceID, "entityID": entityID, "err": err})
				return err
			}
			requestedAttributes = make(me.AttributeValueMap) // reset map
			requestedAttributes[v.Name] = v.DefValue         // populate the metric that was missed in the current iteration
			size = v.Size                                    // reset size
		}
	}
	// Collect the omci get attributes for the last bunch of attributes.
	if err := grpFunc(ctx, classID, entityID, meAttributes, requestedAttributes, data, intervalEndTime); err != nil {
		logger.Errorw(ctx, "error during metric collection",
			log.Fields{"device-id": mm.deviceID, "entityID": entityID, "err": err})
		return err
	}
	return nil
}

func (mm *OnuMetricsManager) populateOnuMetricInfo(title string, data map[string]float32) voltha.MetricInformation {
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetDevice().ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.GetDevice().ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.GetDeviceType()

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           title,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.GetLogicalDeviceID(),
		SerialNo:        mm.pDeviceHandler.GetDevice().SerialNumber,
	}

	// create slice of metrics given that there could be more than one VEIP instance
	metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: data}
	return metricInfo
}

func (mm *OnuMetricsManager) updateAndValidateIntervalEndTime(ctx context.Context, entityID uint16, meAttributes me.AttributeValueMap, intervalEndTime *int) bool {
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
				log.Fields{"device-id": mm.deviceID, "entityID": entityID,
					"currIntervalEndTime": *intervalEndTime, "newIntervalEndTime": currIntervalEndTime})
		} else {
			valid = true
		}
	}
	return valid
}

func (mm *OnuMetricsManager) waitForResponseOrTimeout(ctx context.Context, create bool, instID uint16, meClassName string) bool {
	logger.Debugw(ctx, "waitForResponseOrTimeout", log.Fields{"create": create, "instID": instID, "meClassName": meClassName})
	select {
	case resp := <-mm.l2PmCreateOrDeleteResponseChan:
		logger.Debugw(ctx, "received l2 pm me response",
			log.Fields{"device-id": mm.deviceID, "resp": resp, "create": create, "meClassName": meClassName, "instID": instID})
		return resp
	case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "timeout waiting for l2 pm me response",
			log.Fields{"device-id": mm.deviceID, "resp": false, "create": create, "meClassName": meClassName, "instID": instID})
	}
	return false
}

func (mm *OnuMetricsManager) initializeGroupMetric(grpMtrcs map[string]voltha.PmConfig_PmType, grpName string, grpEnabled bool, grpFreq uint32) {
	var pmConfigSlice []*voltha.PmConfig
	for k, v := range grpMtrcs {
		pmConfigSlice = append(pmConfigSlice,
			&voltha.PmConfig{
				Name:       k,
				Type:       v,
				Enabled:    grpEnabled && mm.pDeviceHandler.GetMetricsEnabled(),
				SampleFreq: grpFreq})
	}
	groupMetric := voltha.PmGroupConfig{
		GroupName: grpName,
		Enabled:   grpEnabled && mm.pDeviceHandler.GetMetricsEnabled(),
		GroupFreq: grpFreq,
		Metrics:   pmConfigSlice,
	}
	mm.pDeviceHandler.GetPmConfigs().Groups = append(mm.pDeviceHandler.GetPmConfigs().Groups, &groupMetric)

}

func (mm *OnuMetricsManager) initializeL2PmFsm(ctx context.Context, aCommChannel chan cmn.Message) error {
	mm.PAdaptFsm = cmn.NewAdapterFsm("L2PmFSM", mm.deviceID, aCommChannel)
	if mm.PAdaptFsm == nil {
		logger.Errorw(ctx, "L2PMFsm cmn.AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": mm.deviceID})
		return fmt.Errorf("nil-adapter-fsm")
	}
	// L2 PM FSM related state machine
	mm.PAdaptFsm.PFsm = fsm.NewFSM(
		L2PmStNull,
		fsm.Events{
			{Name: L2PmEventInit, Src: []string{L2PmStNull}, Dst: L2PmStStarting},
			{Name: L2PmEventTick, Src: []string{L2PmStStarting}, Dst: L2PmStSyncTime},
			{Name: L2PmEventTick, Src: []string{L2PmStIdle, L2PmStCreatePmMe, L2PmStDeletePmMe}, Dst: L2PmStCollectData},
			{Name: L2PmEventSuccess, Src: []string{L2PmStSyncTime, L2PmStCreatePmMe, L2PmStDeletePmMe, L2PmStCollectData}, Dst: L2PmStIdle},
			{Name: L2PmEventFailure, Src: []string{L2PmStCreatePmMe, L2PmStDeletePmMe, L2PmStCollectData}, Dst: L2PmStIdle},
			{Name: L2PmEventFailure, Src: []string{L2PmStSyncTime}, Dst: L2PmStSyncTime},
			{Name: L2PmEventAddMe, Src: []string{L2PmStIdle}, Dst: L2PmStCreatePmMe},
			{Name: L2PmEventDeleteMe, Src: []string{L2PmStIdle}, Dst: L2PmStDeletePmMe},
			{Name: L2PmEventStop, Src: []string{L2PmStNull, L2PmStStarting, L2PmStSyncTime, L2PmStIdle, L2PmStCreatePmMe, L2PmStDeletePmMe, L2PmStCollectData}, Dst: L2PmStNull},
		},
		fsm.Callbacks{
			"enter_state":                func(e *fsm.Event) { mm.PAdaptFsm.LogFsmStateChange(ctx, e) },
			"enter_" + L2PmStNull:        func(e *fsm.Event) { mm.l2PMFsmNull(ctx, e) },
			"enter_" + L2PmStIdle:        func(e *fsm.Event) { mm.l2PMFsmIdle(ctx, e) },
			"enter_" + L2PmStStarting:    func(e *fsm.Event) { mm.l2PMFsmStarting(ctx, e) },
			"enter_" + L2PmStSyncTime:    func(e *fsm.Event) { mm.l2PMFsmSyncTime(ctx, e) },
			"enter_" + L2PmStCollectData: func(e *fsm.Event) { mm.l2PmFsmCollectData(ctx, e) },
			"enter_" + L2PmStCreatePmMe:  func(e *fsm.Event) { _ = mm.l2PmFsmCreatePM(ctx, e) },
			"enter_" + L2PmStDeletePmMe:  func(e *fsm.Event) { _ = mm.l2PmFsmDeletePM(ctx, e) },
		},
	)
	return nil
}

func (mm *OnuMetricsManager) initializeAllGroupMetrics() {
	mm.pDeviceHandler.InitPmConfigs()
	mm.pDeviceHandler.GetPmConfigs().Id = mm.deviceID
	mm.pDeviceHandler.GetPmConfigs().DefaultFreq = DefaultMetricCollectionFrequency
	mm.pDeviceHandler.GetPmConfigs().Grouped = GroupMetricEnabled
	mm.pDeviceHandler.GetPmConfigs().FreqOverride = DefaultFrequencyOverrideEnabled

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

func (mm *OnuMetricsManager) populateLocalGroupMetricData(ctx context.Context) {
	// Populate local group metric structures
	for _, g := range mm.pDeviceHandler.GetPmConfigs().Groups {
		mm.GroupMetricMap[g.GroupName] = &groupMetric{
			groupName: g.GroupName,
			Enabled:   g.Enabled,
			Frequency: g.GroupFreq,
		}
		switch g.GroupName {
		case OpticalPowerGroupMetricName:
			mm.GroupMetricMap[g.GroupName].metricMap = OpticalPowerGroupMetrics
		case UniStatusGroupMetricName:
			mm.GroupMetricMap[g.GroupName].metricMap = UniStatusGroupMetrics
		case EthernetBridgeHistoryName:
			mm.GroupMetricMap[g.GroupName].metricMap = EthernetBridgeHistory
			mm.GroupMetricMap[g.GroupName].IsL2PMCounter = true
		case EthernetUniHistoryName:
			mm.GroupMetricMap[g.GroupName].metricMap = EthernetUniHistory
			mm.GroupMetricMap[g.GroupName].IsL2PMCounter = true
		case FecHistoryName:
			mm.GroupMetricMap[g.GroupName].metricMap = FecHistory
			mm.GroupMetricMap[g.GroupName].IsL2PMCounter = true
		case GemPortHistoryName:
			mm.GroupMetricMap[g.GroupName].metricMap = GemPortHistory
			mm.GroupMetricMap[g.GroupName].IsL2PMCounter = true
		default:
			logger.Errorw(ctx, "unhandled-group-name", log.Fields{"groupName": g.GroupName})
		}
	}

	// Populate local standalone metric structures
	for _, m := range mm.pDeviceHandler.GetPmConfigs().Metrics {
		mm.StandaloneMetricMap[m.Name] = &standaloneMetric{
			metricName: m.Name,
			Enabled:    m.Enabled,
			Frequency:  m.SampleFreq,
		}
		switch m.Name {
		// None exist as of now. Add when available.
		default:
			logger.Errorw(ctx, "unhandled-metric-name", log.Fields{"metricName": m.Name})
		}
	}
}

// AddGemPortForPerfMonitoring - TODO: add comment
func (mm *OnuMetricsManager) AddGemPortForPerfMonitoring(ctx context.Context, gemPortNTPInstID uint16) {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "add gemport for perf monitoring - start", log.Fields{"device-id": mm.deviceID, "gemPortID": gemPortNTPInstID})
	// mark the instance for addition
	mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.appendIfMissingUnt16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, gemPortNTPInstID)
	// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToDelete slice
	mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.removeIfFoundUint16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, gemPortNTPInstID)

	mm.l2PmToAdd = mm.appendIfMissingString(mm.l2PmToAdd, GemPortHistoryName)
	// We do not need to remove from l2PmToDelete slice as we could have Add and Delete of
	// GemPortPerfHistory ME simultaneously for different instances of the ME.
	// The creation or deletion of an instance is decided based on its presence in gemPortNCTPPerfHistInstToDelete or
	// gemPortNCTPPerfHistInstToAdd slice

	logger.Debugw(ctx, "add gemport for perf monitoring - end",
		log.Fields{"device-id": mm.deviceID, "pms-to-add": mm.l2PmToAdd,
			"instances-to-add": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd})
	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventAddMe); err != nil {
			// log at warn level as the gem port for monitoring is going to be added eventually
			logger.Warnw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
}

// RemoveGemPortForPerfMonitoring - TODO: add comment
func (mm *OnuMetricsManager) RemoveGemPortForPerfMonitoring(ctx context.Context, gemPortNTPInstID uint16) {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "remove gemport for perf monitoring - start", log.Fields{"device-id": mm.deviceID, "gemPortID": gemPortNTPInstID})
	mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.appendIfMissingUnt16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, gemPortNTPInstID)
	// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToAdd slice
	mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.removeIfFoundUint16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, gemPortNTPInstID)

	mm.l2PmToDelete = mm.appendIfMissingString(mm.l2PmToDelete, GemPortHistoryName)
	// We do not need to remove from l2PmToAdd slice as we could have Add and Delete of
	// GemPortPerfHistory ME simultaneously for different instances of the ME.
	// The creation or deletion of an instance is decided based on its presence in gemPortNCTPPerfHistInstToDelete or
	// gemPortNCTPPerfHistInstToAdd slice

	logger.Debugw(ctx, "remove gemport from perf monitoring - end",
		log.Fields{"device-id": mm.deviceID, "pms-to-delete": mm.l2PmToDelete,
			"instances-to-delete": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete})
	go func() {
		if err := mm.PAdaptFsm.PFsm.Event(L2PmEventDeleteMe); err != nil {
			// log at warn level as the gem port for monitoring is going to be removed eventually
			logger.Warnw(ctx, "error calling event", log.Fields{"device-id": mm.deviceID, "err": err})
		}
	}()
}

func (mm *OnuMetricsManager) updateGemPortNTPInstanceToAddForPerfMonitoring(ctx context.Context) {
	if mm.pDeviceHandler.GetOnuTP() != nil {
		gemPortInstIDs := mm.pDeviceHandler.GetOnuTP().GetAllBidirectionalGemPortIDsForOnu()
		// NOTE: It is expected that caller of this function has acquired the required mutex for synchronization purposes
		for _, v := range gemPortInstIDs {
			// mark the instance for addition
			mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.appendIfMissingUnt16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, v)
			// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToDelete slice
			mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.removeIfFoundUint16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, v)
		}
		logger.Debugw(ctx, "updateGemPortNTPInstanceToAddForPerfMonitoring",
			log.Fields{"deviceID": mm.deviceID, "gemToAdd": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, "gemToDel": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete})
	}
}

func (mm *OnuMetricsManager) updateGemPortNTPInstanceToDeleteForPerfMonitoring(ctx context.Context) {
	if mm.pDeviceHandler.GetOnuTP() != nil {
		gemPortInstIDs := mm.pDeviceHandler.GetOnuTP().GetAllBidirectionalGemPortIDsForOnu()
		// NOTE: It is expected that caller of this function has acquired the required mutex for synchronization purposes
		for _, v := range gemPortInstIDs {
			mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete = mm.appendIfMissingUnt16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete, v)
			// If the instance presence toggles too soon, we need to remove it from gemPortNCTPPerfHistInstToAdd slice
			mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd = mm.removeIfFoundUint16(mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, v)
		}
	}
	logger.Debugw(ctx, "updateGemPortNTPInstanceToDeleteForPerfMonitoring",
		log.Fields{"deviceID": mm.deviceID, "gemToAdd": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToAdd, "gemToDel": mm.GroupMetricMap[GemPortHistoryName].pmMEData.InstancesToDelete})
}

// restorePmData restores any PM data available on the KV store to local cache
func (mm *OnuMetricsManager) restorePmData(ctx context.Context) error {
	logger.Debugw(ctx, "restorePmData - start", log.Fields{"device-id": mm.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.deviceID))
	}
	var errorsList []error
	for groupName, group := range mm.GroupMetricMap {
		group.pmMEData = &pmMEData{}
		Value, err := mm.pmKvStore.Get(ctx, groupName)
		if err == nil {
			if Value != nil {
				logger.Debugw(ctx, "PM data read",
					log.Fields{"Key": Value.Key, "device-id": mm.deviceID})
				tmpBytes, _ := kvstore.ToByte(Value.Value)

				if err = json.Unmarshal(tmpBytes, &group.pmMEData); err != nil {
					logger.Errorw(ctx, "unable to unmarshal PM data", log.Fields{"error": err, "device-id": mm.deviceID})
					errorsList = append(errorsList, fmt.Errorf(fmt.Sprintf("unable-to-unmarshal-PM-data-%s-for-group-%s", mm.deviceID, groupName)))
					continue
				}
				logger.Debugw(ctx, "restorePmData - success", log.Fields{"pmData": group.pmMEData, "groupName": groupName, "device-id": mm.deviceID})
			} else {
				logger.Debugw(ctx, "no PM data found", log.Fields{"groupName": groupName, "device-id": mm.deviceID})
				continue
			}
		} else {
			logger.Errorw(ctx, "restorePmData - fail", log.Fields{"device-id": mm.deviceID, "groupName": groupName, "err": err})
			errorsList = append(errorsList, fmt.Errorf(fmt.Sprintf("unable-to-read-from-KVstore-%s-for-group-%s", mm.deviceID, groupName)))
			continue
		}
	}
	if len(errorsList) > 0 {
		return fmt.Errorf("errors-restoring-pm-data-for-one-or-more-groups--errors:%v", errorsList)
	}
	logger.Debugw(ctx, "restorePmData - complete success", log.Fields{"device-id": mm.deviceID})
	return nil
}

// getPmData gets pmMEData from cache. Since we have write through cache implementation for pmMEData,
// the data must be available in cache.
// Note, it is expected that caller of this function manages the required synchronization (like using locks etc.).
func (mm *OnuMetricsManager) getPmData(ctx context.Context, groupName string) (*pmMEData, error) {
	if grp, ok := mm.GroupMetricMap[groupName]; ok {
		return grp.pmMEData, nil
	}
	// Data not in cache, try to fetch from kv store.
	data := &pmMEData{}
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.deviceID})
		return data, fmt.Errorf("pmKvStore not set. device-id - %s", mm.deviceID)
	}
	Value, err := mm.pmKvStore.Get(ctx, groupName)
	if err == nil {
		if Value != nil {
			logger.Debugw(ctx, "PM data read",
				log.Fields{"Key": Value.Key, "device-id": mm.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, data); err != nil {
				logger.Errorw(ctx, "unable to unmarshal PM data", log.Fields{"error": err, "device-id": mm.deviceID})
				return data, err
			}
			logger.Debugw(ctx, "PM data", log.Fields{"pmData": data, "groupName": groupName, "device-id": mm.deviceID})
		} else {
			logger.Debugw(ctx, "no PM data found", log.Fields{"groupName": groupName, "device-id": mm.deviceID})
			return data, err
		}
	} else {
		logger.Errorw(ctx, "unable to read from KVstore", log.Fields{"device-id": mm.deviceID})
		return data, err
	}

	return data, nil
}

// updatePmData update pmMEData to store. It is write through cache, i.e., write to cache first and then update store
func (mm *OnuMetricsManager) updatePmData(ctx context.Context, groupName string, meInstanceID uint16, pmAction string) error {
	logger.Debugw(ctx, "updatePmData - start", log.Fields{"device-id": mm.deviceID, "groupName": groupName, "entityID": meInstanceID, "pmAction": pmAction})
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()

	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.deviceID))
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
		logger.Errorw(ctx, "unknown pm action", log.Fields{"device-id": mm.deviceID, "pmAction": pmAction, "groupName": groupName})
		return fmt.Errorf(fmt.Sprintf("unknown-pm-action-deviceid-%s-groupName-%s-pmaction-%s", mm.deviceID, groupName, pmAction))
	}
	// write through cache
	mm.GroupMetricMap[groupName].pmMEData = pmMEData

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
	logger.Debugw(ctx, "updatePmData - success", log.Fields{"device-id": mm.deviceID, "groupName": groupName, "pmData": *pmMEData, "pmAction": pmAction})

	return nil
}

// clearPmGroupData cleans PM Group data from store
func (mm *OnuMetricsManager) clearPmGroupData(ctx context.Context) error {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "clearPmGroupData - start", log.Fields{"device-id": mm.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.deviceID))
	}

	for n := range mm.GroupMetricMap {
		if err := mm.pmKvStore.Delete(ctx, n); err != nil {
			logger.Errorw(ctx, "clearPmGroupData - fail", log.Fields{"deviceID": mm.deviceID, "groupName": n, "err": err})
			// do not abort this procedure. continue to delete next group.
		} else {
			logger.Debugw(ctx, "clearPmGroupData - success", log.Fields{"device-id": mm.deviceID, "groupName": n})
		}
	}

	return nil
}

// ClearAllPmData clears all PM data associated with the device from KV store
func (mm *OnuMetricsManager) ClearAllPmData(ctx context.Context) error {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "ClearAllPmData - start", log.Fields{"device-id": mm.deviceID})
	if mm.pmKvStore == nil {
		logger.Errorw(ctx, "pmKvStore not set - abort", log.Fields{"device-id": mm.deviceID})
		return fmt.Errorf(fmt.Sprintf("pmKvStore-not-set-abort-%s", mm.deviceID))
	}
	var value error
	for n := range mm.GroupMetricMap {
		if err := mm.pmKvStore.Delete(ctx, n); err != nil {
			logger.Errorw(ctx, "clearPmGroupData - fail", log.Fields{"deviceID": mm.deviceID, "groupName": n, "err": err})
			value = err
			// do not abort this procedure - continue to delete next group.
		} else {
			logger.Debugw(ctx, "clearPmGroupData - success", log.Fields{"device-id": mm.deviceID, "groupName": n})
		}
	}
	if value == nil {
		logger.Debugw(ctx, "ClearAllPmData - success", log.Fields{"device-id": mm.deviceID})
	}
	return value
}

func (mm *OnuMetricsManager) updateOmciProcessingStatus(status bool) {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	mm.omciProcessingActive = status
}

// updateTickGenerationStatus - TODO: add comment
func (mm *OnuMetricsManager) updateTickGenerationStatus(status bool) {
	mm.OnuMetricsManagerLock.Lock()
	defer mm.OnuMetricsManagerLock.Unlock()
	mm.tickGenerationActive = status
}

// GetOmciProcessingStatus - TODO: add comment
func (mm *OnuMetricsManager) GetOmciProcessingStatus() bool {
	mm.OnuMetricsManagerLock.RLock()
	defer mm.OnuMetricsManagerLock.RUnlock()
	return mm.omciProcessingActive
}

// GetTickGenerationStatus - TODO: add comment
func (mm *OnuMetricsManager) GetTickGenerationStatus() bool {
	mm.OnuMetricsManagerLock.RLock()
	defer mm.OnuMetricsManagerLock.RUnlock()
	return mm.tickGenerationActive
}

func (mm *OnuMetricsManager) appendIfMissingString(slice []string, n string) []string {
	for _, ele := range slice {
		if ele == n {
			return slice
		}
	}
	return append(slice, n)
}

func (mm *OnuMetricsManager) removeIfFoundString(slice []string, n string) []string {
	for i, ele := range slice {
		if ele == n {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (mm *OnuMetricsManager) appendIfMissingUnt16(slice []uint16, n uint16) []uint16 {
	for _, ele := range slice {
		if ele == n {
			return slice
		}
	}
	return append(slice, n)
}

func (mm *OnuMetricsManager) removeIfFoundUint16(slice []uint16, n uint16) []uint16 {
	for i, ele := range slice {
		if ele == n {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (mm *OnuMetricsManager) getEthernetFrameExtendedMETypeFromKvStore(ctx context.Context) (bool, error) {
	// Check if the data is already available in KV store, if yes, do not send the request for get me.
	var data me.ClassID
	key := fmt.Sprintf("%s/%s/%s", mm.pOnuDeviceEntry.GetPersVendorID(),
		mm.pOnuDeviceEntry.GetPersEquipmentID(),
		mm.pOnuDeviceEntry.GetPersActiveSwVersion())
	Value, err := mm.extPmKvStore.Get(ctx, key)
	if err == nil {
		if Value != nil {
			logger.Debugw(ctx, "me-type-read",
				log.Fields{"key": Value.Key, "device-id": mm.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, &data); err != nil {
				logger.Errorw(ctx, "unable-to-unmarshal-data", log.Fields{"error": err, "device-id": mm.deviceID})
				return false, err
			}
			logger.Debugw(ctx, "me-ext-pm-class-data", log.Fields{"class-id": data, "device-id": mm.deviceID})
			// We have found the data from db, no need to get through omci get message.
			mm.supportedEthernetFrameExtendedPMClass = data
			return true, nil
		}
		logger.Debugw(ctx, "no-me-ext-pm-class-data-found", log.Fields{"device-id": mm.deviceID})
		return false, nil
	}
	logger.Errorw(ctx, "unable-to-read-from-kv-store", log.Fields{"device-id": mm.deviceID})
	return false, err
}

func (mm *OnuMetricsManager) waitForEthernetFrameCreateOrDeleteResponseOrTimeout(ctx context.Context, create bool, instID uint16, meClassID me.ClassID, upstream bool) (bool, error) {
	logger.Debugw(ctx, "wait-for-ethernet-frame-create-or-delete-response-or-timeout", log.Fields{"create": create, "instID": instID, "meClassID": meClassID})
	select {
	case resp := <-mm.extendedPMCreateOrDeleteResponseChan:
		logger.Debugw(ctx, "received-extended-pm-me-response",
			log.Fields{"device-id": mm.deviceID, "resp": resp, "create": create, "meClassID": meClassID, "instID": instID, "upstream": upstream})
		// If the result is me.InstanceExists it means the entity was already created. It is ok handled that as success
		if resp == me.Success || resp == me.InstanceExists {
			return true, nil
		} else if resp == me.UnknownEntity || resp == me.ParameterError ||
			resp == me.ProcessingError || resp == me.NotSupported || resp == me.AttributeFailure {
			return false, fmt.Errorf("not-supported-me--resp-code-%v", resp)
		} else {
			logger.Warnw(ctx, "failed to create me", log.Fields{"device-id": mm.deviceID, "resp": resp, "class-id": meClassID, "instID": instID, "upstream": upstream})
			return true, fmt.Errorf("error-while-creating-me--resp-code-%v", resp)
		}
	case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "timeout-waiting-for-ext-pm-me-response",
			log.Fields{"device-id": mm.deviceID, "resp": false, "create": create, "meClassID": meClassID, "instID": instID, "upstream": upstream})
	}
	return false, fmt.Errorf("timeout-while-waiting-for-response")
}

func (mm *OnuMetricsManager) tryCreateExtPmMe(ctx context.Context, meType me.ClassID) (bool, error) {
	cnt := 0
	// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
	for _, direction := range []bool{true, false} {
		for _, uniPort := range *mm.pDeviceHandler.GetUniEntityMap() {
			var entityID uint16
			if direction {
				entityID = uniPort.EntityID + 0x100
			} else {
				entityID = uniPort.EntityID
			}

			// parent entity id will be same for both direction
			controlBlock := mm.getControlBlockForExtendedPMDirection(ctx, direction, uniPort.EntityID)

		inner1:
			// retry ExtendedPmCreateAttempts times to create the instance of PM
			for cnt = 0; cnt < ExtendedPmCreateAttempts; cnt++ {
				meEnt, err := mm.pOnuDeviceEntry.GetDevOmciCC().SendCreateOrDeleteEthernetFrameExtendedPMME(
					ctx, mm.pDeviceHandler.GetOmciTimeout(), true, direction, true,
					mm.PAdaptFsm.CommChan, entityID, meType, controlBlock)
				if err != nil {
					logger.Errorw(ctx, "EthernetFrameExtendedPMME-create-or-delete-failed",
						log.Fields{"device-id": mm.deviceID})
					return false, err
				}
				if supported, err := mm.waitForEthernetFrameCreateOrDeleteResponseOrTimeout(ctx, true, entityID, meType, direction); err == nil && supported {
					if direction {
						mm.ethernetFrameExtendedPmUpStreamMEByEntityID[entityID] = meEnt
					} else {
						mm.ethernetFrameExtendedPmDownStreamMEByEntityID[entityID] = meEnt
					}
					break inner1
				} else if err != nil {
					if !supported {
						// Need to return immediately
						return false, err
					}
					//In case of failure, go for a retry
				}
			}
			if cnt == ExtendedPmCreateAttempts {
				logger.Error(ctx, "exceeded-attempts-while-creating-me-for-ethernet-frame-extended-pm")
				return true, fmt.Errorf("unable-to-create-me")
			}
		}
	}
	return true, nil
}

func (mm *OnuMetricsManager) putExtPmMeKvStore(ctx context.Context) {
	key := fmt.Sprintf("%s/%s/%s", mm.pOnuDeviceEntry.GetPersVendorID(),
		mm.pOnuDeviceEntry.GetPersEquipmentID(),
		mm.pOnuDeviceEntry.GetPersActiveSwVersion())
	// check if we get the supported type me for ethernet frame extended pm class id
	if mm.supportedEthernetFrameExtendedPMClass == 0 {
		logger.Error(ctx, "unable-to-get-any-supported-extended-pm-me-class")
	}
	classSupported, err := json.Marshal(mm.supportedEthernetFrameExtendedPMClass)
	if err != nil {
		logger.Errorw(ctx, "unable-to-marshal-data", log.Fields{"err": err})
	}
	if err := mm.extPmKvStore.Put(ctx, key, classSupported); err != nil {
		logger.Errorw(ctx, "unable-to-add-data-in-db", log.Fields{"err": err})
	}
}

func (mm *OnuMetricsManager) setAllExtPmMeCreatedFlag() {
	mm.onuEthernetFrameExtendedPmLock.Lock()
	mm.isDeviceReadyToCollectExtendedPmStats = true
	mm.onuEthernetFrameExtendedPmLock.Unlock()
}

// CreateEthernetFrameExtendedPMME - TODO: add comment
func (mm *OnuMetricsManager) CreateEthernetFrameExtendedPMME(ctx context.Context) {
	//get the type of extended frame pm me supported by onu first
	exist, err := mm.getEthernetFrameExtendedMETypeFromKvStore(ctx)
	if err != nil {
		logger.Error(ctx, "unable-to-get-supported-me-for-ethernet-frame-extended-pm")
		return
	}
	if exist {
		// we have the me type, go ahead with the me type supported.
		if _, err := mm.tryCreateExtPmMe(ctx, mm.supportedEthernetFrameExtendedPMClass); err != nil {
			logger.Errorw(ctx, "unable-to-create-me-type", log.Fields{"device-id": mm.deviceID,
				"meClassID": mm.supportedEthernetFrameExtendedPMClass})
			return
		}
		mm.setAllExtPmMeCreatedFlag()
		return
	}
	// First try with 64 bit me
	// we have the me type, go ahead with the me type supported.
	supported64Bit, err := mm.tryCreateExtPmMe(ctx, me.EthernetFrameExtendedPm64BitClassID)
	if err != nil && !supported64Bit {
		logger.Errorw(ctx, "unable-to-create-me-type-as-it-is-not-supported",
			log.Fields{"device-id": mm.deviceID, "meClassID": me.EthernetFrameExtendedPm64BitClassID,
				"supported": supported64Bit})
		// Then Try with 32 bit type
		if supported32Bit, err := mm.tryCreateExtPmMe(ctx, me.EthernetFrameExtendedPmClassID); err != nil {
			logger.Errorw(ctx, "unable-to-create-me-type", log.Fields{"device-id": mm.deviceID,
				"meClassID": me.EthernetFrameExtendedPmClassID, "supported": supported32Bit})
		} else if supported32Bit {
			mm.supportedEthernetFrameExtendedPMClass = me.EthernetFrameExtendedPmClassID
			mm.putExtPmMeKvStore(ctx)
			mm.setAllExtPmMeCreatedFlag()
		}
	} else if err == nil && supported64Bit {
		mm.supportedEthernetFrameExtendedPMClass = me.EthernetFrameExtendedPm64BitClassID
		mm.putExtPmMeKvStore(ctx)
		mm.setAllExtPmMeCreatedFlag()
	}
}

// CollectEthernetFrameExtendedPMCounters - TODO: add comment
func (mm *OnuMetricsManager) CollectEthernetFrameExtendedPMCounters(ctx context.Context) *extension.SingleGetValueResponse {
	errFunc := func(reason extension.GetValueResponse_ErrorReason) *extension.SingleGetValueResponse {
		return &extension.SingleGetValueResponse{
			Response: &extension.GetValueResponse{
				Status:    extension.GetValueResponse_ERROR,
				ErrReason: reason,
			},
		}
	}
	mm.onuEthernetFrameExtendedPmLock.RLock()
	if !mm.isDeviceReadyToCollectExtendedPmStats {
		mm.onuEthernetFrameExtendedPmLock.RUnlock()
		return errFunc(extension.GetValueResponse_INTERNAL_ERROR)
	}
	mm.onuEthernetFrameExtendedPmLock.RUnlock()
	// Collect metrics for upstream for all the PM Mes per uni port and aggregate
	var pmUpstream extension.OmciEthernetFrameExtendedPm
	var pmDownstream extension.OmciEthernetFrameExtendedPm
	for entityID, meEnt := range mm.ethernetFrameExtendedPmUpStreamMEByEntityID {
		var receivedMask uint16
		if metricInfo, errResp := mm.collectEthernetFrameExtendedPMData(ctx, meEnt, entityID, true, &receivedMask); metricInfo != nil { // upstream
			if receivedMask == 0 {
				pmUpstream = mm.aggregateEthernetFrameExtendedPM(metricInfo, pmUpstream, false)
				logger.Error(ctx, "all-the-attributes-of-ethernet-frame-extended-pm-counters-are-unsupported")
				pmDownstream = pmUpstream
				singleValResp := extension.SingleGetValueResponse{
					Response: &extension.GetValueResponse{
						Status: extension.GetValueResponse_OK,
						Response: &extension.GetValueResponse_OnuCounters{
							OnuCounters: &extension.GetOmciEthernetFrameExtendedPmResponse{
								Upstream:   &pmUpstream,
								Downstream: &pmDownstream,
							},
						},
					},
				}
				return &singleValResp
			}
			// Aggregate the result for upstream
			pmUpstream = mm.aggregateEthernetFrameExtendedPM(metricInfo, pmUpstream, true)
		} else {
			return errFunc(errResp)
		}
	}

	for entityID, meEnt := range mm.ethernetFrameExtendedPmDownStreamMEByEntityID {
		var receivedMask uint16
		if metricInfo, errResp := mm.collectEthernetFrameExtendedPMData(ctx, meEnt, entityID, false, &receivedMask); metricInfo != nil { // downstream
			// Aggregate the result for downstream
			pmDownstream = mm.aggregateEthernetFrameExtendedPM(metricInfo, pmDownstream, true)
		} else {
			return errFunc(errResp)
		}
	}
	singleValResp := extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status: extension.GetValueResponse_OK,
			Response: &extension.GetValueResponse_OnuCounters{
				OnuCounters: &extension.GetOmciEthernetFrameExtendedPmResponse{
					Upstream:   &pmUpstream,
					Downstream: &pmDownstream,
				},
			},
		},
	}
	return &singleValResp
}

func (mm *OnuMetricsManager) collectEthernetFrameExtendedPMData(ctx context.Context, meEnt *me.ManagedEntity, entityID uint16, upstream bool, receivedMask *uint16) (map[string]uint64, extension.GetValueResponse_ErrorReason) {
	var classID me.ClassID
	logger.Debugw(ctx, "collecting-data-for-ethernet-frame-extended-pm", log.Fields{"device-id": mm.deviceID, "entityID": entityID, "upstream": upstream})

	classID = mm.supportedEthernetFrameExtendedPMClass
	attributeMaskList := maskToEthernetFrameExtendedPM64Bit
	if classID == me.EthernetFrameExtendedPmClassID {
		attributeMaskList = maskToEthernetFrameExtendedPM32Bit
	}
	ethPMData := make(map[string]uint64)
	var sumReceivedMask uint16
	for mask := range attributeMaskList {
		if errResp, err := mm.populateEthernetFrameExtendedPMMetrics(ctx, classID, entityID, mask, ethPMData, upstream, &sumReceivedMask); err != nil {
			logger.Errorw(ctx, "error-during-metric-collection",
				log.Fields{"device-id": mm.deviceID, "entityID": entityID, "err": err})
			return nil, errResp
		}
		if (mask == 0x3F00 || mask == 0x3800) && sumReceivedMask == 0 {
			//It means the first attributes fetch was a failure, hence instead of sending multiple failure get requests
			//populate all counters as failure and return
			mm.fillAllErrorCountersEthernetFrameExtendedPM(ethPMData)
			break
		}
	}
	*receivedMask = sumReceivedMask
	return ethPMData, extension.GetValueResponse_REASON_UNDEFINED
}

// nolint: gocyclo
func (mm *OnuMetricsManager) populateEthernetFrameExtendedPMMetrics(ctx context.Context, classID me.ClassID, entityID uint16,
	requestedAttributesMask uint16, ethFrameExtPMData map[string]uint64, upstream bool, sumReceivedMask *uint16) (extension.GetValueResponse_ErrorReason, error) {
	var meAttributes me.AttributeValueMap
	logger.Debugw(ctx, "requesting-attributes", log.Fields{"attributes-mask": requestedAttributesMask, "entityID": entityID, "classID": classID})
	err := mm.pOnuDeviceEntry.GetDevOmciCC().SendGetMeWithAttributeMask(ctx, classID, entityID, requestedAttributesMask, mm.pDeviceHandler.GetOmciTimeout(), true, mm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "get-me-failed", log.Fields{"device-id": mm.deviceID})
		return extension.GetValueResponse_INTERNAL_ERROR, err
	}
	select {
	case meAttributes = <-mm.extendedPmMeChan:
		logger.Debugw(ctx, "received-extended-pm-data",
			log.Fields{"device-id": mm.deviceID, "upstream": upstream, "entityID": entityID})
	case <-time.After(mm.pOnuDeviceEntry.GetDevOmciCC().GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "timeout-waiting-for-omci-get-response-for-received-extended-pm-data",
			log.Fields{"device-id": mm.deviceID, "upstream": upstream, "entityID": entityID})
		return extension.GetValueResponse_TIMEOUT, fmt.Errorf("timeout-waiting-for-omci-get-response-for-received-extended-pm-data")
	}
	if mm.supportedEthernetFrameExtendedPMClass == me.EthernetFrameExtendedPmClassID {
		mask := mm.getEthFrameExtPMDataFromResponse(ctx, ethFrameExtPMData, meAttributes, requestedAttributesMask)
		*sumReceivedMask += mask
		logger.Debugw(ctx, "data-received-for-ethernet-frame-ext-pm", log.Fields{"data": ethFrameExtPMData, "entityID": entityID})
	} else {
		mask := mm.getEthFrameExtPM64BitDataFromResponse(ctx, ethFrameExtPMData, meAttributes, requestedAttributesMask)
		*sumReceivedMask += mask
		logger.Debugw(ctx, "data-received-for-ethernet-frame-ext-pm", log.Fields{"data": ethFrameExtPMData, "entityID": entityID})
	}

	return extension.GetValueResponse_REASON_UNDEFINED, nil
}

func (mm *OnuMetricsManager) fillAllErrorCountersEthernetFrameExtendedPM(ethFrameExtPMData map[string]uint64) {
	sourceMap := maskToEthernetFrameExtendedPM64Bit
	errorCounterValue := UnsupportedCounterValue64bit
	if mm.supportedEthernetFrameExtendedPMClass == me.EthernetFrameExtendedPmClassID {
		sourceMap = maskToEthernetFrameExtendedPM32Bit
		errorCounterValue = UnsupportedCounterValue32bit
	}
	for _, value := range sourceMap {
		for _, k := range value {
			if _, ok := ethFrameExtPMData[k]; !ok {
				ethFrameExtPMData[k] = errorCounterValue
			}
		}
	}
}

// nolint: gocyclo
func (mm *OnuMetricsManager) getEthFrameExtPMDataFromResponse(ctx context.Context, ethFrameExtPMData map[string]uint64, meAttributes me.AttributeValueMap, requestedAttributesMask uint16) uint16 {
	receivedMask := uint16(0)
	switch requestedAttributesMask {
	case 0x3F00:
		for _, k := range maskToEthernetFrameExtendedPM32Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "drop_events":
					if val, ok := meAttributes[dropEvents]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x2000
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "octets":
					if val, ok := meAttributes[octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x1000
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "frames":
					if val, ok := meAttributes[frames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x800
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "broadcast_frames":
					if val, ok := meAttributes[broadcastFrames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x400
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "multicast_frames":
					if val, ok := meAttributes[multicastFrames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x200
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "crc_errored_frames":
					if val, ok := meAttributes[crcErroredFrames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x100
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				default:
					//do nothing
				}
			}
		}
	case 0x00FC:
		for _, k := range maskToEthernetFrameExtendedPM32Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "undersize_frames":
					if val, ok := meAttributes[undersizeFrames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x80
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "oversize_frames":
					if val, ok := meAttributes[oversizeFrames]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x40
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "64_octets":
					if val, ok := meAttributes[frames64Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x20
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "65_to_127_octets":
					if val, ok := meAttributes[frames65To127Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x10
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "128_to_255_octets":
					if val, ok := meAttributes[frames128To255Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x8
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "256_to_511_octets":
					if val, ok := meAttributes[frames256To511Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x4
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				default:
					//do nothing
				}
			}
		}
	case 0x0003:
		for _, k := range maskToEthernetFrameExtendedPM32Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "512_to_1023_octets":
					if val, ok := meAttributes[frames512To1023Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x2
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				case "1024_to_1518_octets":
					if val, ok := meAttributes[frames1024To1518Octets]; ok && val != nil {
						ethFrameExtPMData[k] = uint64(val.(uint32))
						receivedMask |= 0x1
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue32bit
					}
				default:
					//do nothing
				}
			}
		}
	default:
		//do nothing
	}
	return receivedMask
}

// nolint: gocyclo
func (mm *OnuMetricsManager) getEthFrameExtPM64BitDataFromResponse(ctx context.Context, ethFrameExtPMData map[string]uint64, meAttributes me.AttributeValueMap, requestedAttributesMask uint16) uint16 {
	receivedMask := uint16(0)
	switch requestedAttributesMask {
	case 0x3800:
		for _, k := range maskToEthernetFrameExtendedPM64Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "drop_events":
					if val, ok := meAttributes[dropEvents]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x2000
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "octets":
					if val, ok := meAttributes[octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x1000
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "frames":
					if val, ok := meAttributes[frames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x800
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				}
			}
		}
	case 0x0700:
		for _, k := range maskToEthernetFrameExtendedPM64Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "broadcast_frames":
					if val, ok := meAttributes[broadcastFrames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x400
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "multicast_frames":
					if val, ok := meAttributes[multicastFrames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x200
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "crc_errored_frames":
					if val, ok := meAttributes[crcErroredFrames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x100
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				}
			}
		}
	case 0x00E0:
		for _, k := range maskToEthernetFrameExtendedPM64Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "undersize_frames":
					if val, ok := meAttributes[undersizeFrames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x80
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "oversize_frames":
					if val, ok := meAttributes[oversizeFrames]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x40
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "64_octets":
					if val, ok := meAttributes[frames64Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x20
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				}
			}
		}
	case 0x001C:
		for _, k := range maskToEthernetFrameExtendedPM64Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "65_to_127_octets":
					if val, ok := meAttributes[frames65To127Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x10
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "128_to_255_octets":
					if val, ok := meAttributes[frames128To255Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x8
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "256_to_511_octets":
					if val, ok := meAttributes[frames256To511Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x4
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				default:
					//do nothing
				}
			}
		}
	case 0x0003:
		for _, k := range maskToEthernetFrameExtendedPM64Bit[requestedAttributesMask] {
			if _, ok := ethFrameExtPMData[k]; !ok {
				switch k {
				case "512_to_1023_octets":
					if val, ok := meAttributes[frames512To1023Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x2
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				case "1024_to_1518_octets":
					if val, ok := meAttributes[frames1024To1518Octets]; ok && val != nil {
						ethFrameExtPMData[k] = val.(uint64)
						receivedMask |= 0x1
					} else if !ok {
						ethFrameExtPMData[k] = UnsupportedCounterValue64bit
					}
				default:
					//do nothing
				}
			}
		}
	}
	return receivedMask
}

func (mm *OnuMetricsManager) aggregateEthernetFrameExtendedPM(pmDataIn map[string]uint64, pmData extension.OmciEthernetFrameExtendedPm, aggregate bool) extension.OmciEthernetFrameExtendedPm {
	mm.onuEthernetFrameExtendedPmLock.Lock()
	defer mm.onuEthernetFrameExtendedPmLock.Unlock()
	errorCounterValue := UnsupportedCounterValue64bit
	if mm.supportedEthernetFrameExtendedPMClass == me.EthernetFrameExtendedPmClassID {
		errorCounterValue = UnsupportedCounterValue32bit
	}
	var pmDataOut extension.OmciEthernetFrameExtendedPm
	if aggregate {
		if pmData.DropEvents != errorCounterValue {
			pmDataOut.DropEvents = pmData.DropEvents + pmDataIn["drop_events"]
		} else {
			pmDataOut.DropEvents = pmData.DropEvents
		}
		if pmData.Octets != errorCounterValue {
			pmDataOut.Octets = pmData.Octets + pmDataIn["octets"]
		} else {
			pmDataOut.Octets = pmData.Octets
		}
		if pmData.Frames != errorCounterValue {
			pmDataOut.Frames = pmData.Frames + pmDataIn["frames"]
		} else {
			pmDataOut.Frames = pmData.Frames
		}
		if pmData.BroadcastFrames != errorCounterValue {
			pmDataOut.BroadcastFrames = pmData.BroadcastFrames + pmDataIn["broadcast_frames"]
		} else {
			pmDataOut.BroadcastFrames = pmData.BroadcastFrames
		}
		if pmData.MulticastFrames != errorCounterValue {
			pmDataOut.MulticastFrames = pmData.MulticastFrames + pmDataIn["multicast_frames"]
		} else {
			pmDataOut.MulticastFrames = pmData.MulticastFrames
		}
		if pmData.CrcErroredFrames != errorCounterValue {
			pmDataOut.CrcErroredFrames = pmData.CrcErroredFrames + pmDataIn["crc_errored_frames"]
		} else {
			pmDataOut.CrcErroredFrames = pmData.CrcErroredFrames
		}
		if pmData.UndersizeFrames != errorCounterValue {
			pmDataOut.UndersizeFrames = pmData.UndersizeFrames + pmDataIn["undersize_frames"]
		} else {
			pmDataOut.UndersizeFrames = pmData.UndersizeFrames
		}
		if pmData.OversizeFrames != errorCounterValue {
			pmDataOut.OversizeFrames = pmData.OversizeFrames + pmDataIn["oversize_frames"]
		} else {
			pmDataOut.OversizeFrames = pmData.OversizeFrames
		}
		if pmData.Frames_64Octets != errorCounterValue {
			pmDataOut.Frames_64Octets = pmData.Frames_64Octets + pmDataIn["64_octets"]
		} else {
			pmDataOut.Frames_64Octets = pmData.Frames_64Octets
		}
		if pmData.Frames_65To_127Octets != errorCounterValue {
			pmDataOut.Frames_65To_127Octets = pmData.Frames_65To_127Octets + pmDataIn["65_to_127_octets"]
		} else {
			pmDataOut.Frames_65To_127Octets = pmData.Frames_65To_127Octets
		}
		if pmData.Frames_128To_255Octets != errorCounterValue {
			pmDataOut.Frames_128To_255Octets = pmData.Frames_128To_255Octets + pmDataIn["128_to_255_octets"]
		} else {
			pmDataOut.Frames_128To_255Octets = pmData.Frames_128To_255Octets
		}
		if pmData.Frames_256To_511Octets != errorCounterValue {
			pmDataOut.Frames_256To_511Octets = pmData.Frames_256To_511Octets + pmDataIn["256_to_511_octets"]
		} else {
			pmDataOut.Frames_256To_511Octets = pmData.Frames_256To_511Octets
		}
		if pmData.Frames_512To_1023Octets != errorCounterValue {
			pmDataOut.Frames_512To_1023Octets = pmData.Frames_512To_1023Octets + pmDataIn["512_to_1023_octets"]
		} else {
			pmDataOut.Frames_512To_1023Octets = pmData.Frames_512To_1023Octets
		}
		if pmData.Frames_1024To_1518Octets != errorCounterValue {
			pmDataOut.Frames_1024To_1518Octets = pmData.Frames_1024To_1518Octets + pmDataIn["1024_to_1518_octets"]
		} else {
			pmDataOut.Frames_1024To_1518Octets = pmData.Frames_1024To_1518Octets
		}
	} else {
		pmDataOut.DropEvents = pmDataIn["drop_events"]
		pmDataOut.Octets = pmDataIn["octets"]
		pmDataOut.Frames = pmDataIn["frames"]
		pmDataOut.BroadcastFrames = pmDataIn["broadcast_frames"]
		pmDataOut.MulticastFrames = pmDataIn["multicast_frames"]
		pmDataOut.CrcErroredFrames = pmDataIn["crc_errored_frames"]
		pmDataOut.UndersizeFrames = pmDataIn["undersize_frames"]
		pmDataOut.OversizeFrames = pmDataIn["oversize_frames"]
		pmDataOut.Frames_64Octets = pmDataIn["64_octets"]
		pmDataOut.Frames_65To_127Octets = pmDataIn["65_to_127_octets"]
		pmDataOut.Frames_128To_255Octets = pmDataIn["128_to_255_octets"]
		pmDataOut.Frames_256To_511Octets = pmDataIn["256_to_511_octets"]
		pmDataOut.Frames_512To_1023Octets = pmDataIn["512_to_1023_octets"]
		pmDataOut.Frames_1024To_1518Octets = pmDataIn["1024_to_1518_octets"]
	}
	return pmDataOut
}

func (mm *OnuMetricsManager) getControlBlockForExtendedPMDirection(ctx context.Context, upstream bool, entityID uint16) []uint16 {
	controlBlock := make([]uint16, 8)
	// Control Block First two bytes are for threshold data 1/2 id - does not matter here
	controlBlock[0] = 0
	// Next two bytes are for the parent class ID
	controlBlock[1] = (uint16)(me.PhysicalPathTerminationPointEthernetUniClassID)
	// Next two bytes are for the parent me instance id
	controlBlock[2] = entityID
	// Next two bytes are for accumulation disable
	controlBlock[3] = 0
	// Next two bytes are for tca disable
	controlBlock[4] = 0x4000 //tca global disable
	// Next two bytes are for control fields - bit 1(lsb) as 1 for continuous accumulation and bit 2(0 for upstream)
	if upstream {
		controlBlock[5] = 1 << 0
	} else {
		controlBlock[5] = (1 << 0) | (1 << 1)
	}
	// Next two bytes are for tci - does not matter here
	controlBlock[6] = 0
	// Next two bytes are for reserved bits - does not matter here
	controlBlock[7] = 0
	return controlBlock
}
