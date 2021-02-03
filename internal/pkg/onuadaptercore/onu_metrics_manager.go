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
	"errors"
	"fmt"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
	"sync"
	"time"
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

// general constants used for overall Metric Collection management
const (
	DefaultMetricCollectionFrequency = 15 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration
	GroupMetricEnabled               = true    // This is READONLY and cannot be changed from VOLTHA NBI
	DefaultFrequencyOverrideEnabled  = true    // This is READONLY and cannot be changed from VOLTHA NBI
	FrequencyGranularity             = 5       // The frequency (in seconds) has to be multiple of 5. This setting cannot changed later.
)

// OpticalPowerGroupMetrics are supported optical pm names
var OpticalPowerGroupMetrics = map[string]voltha.PmConfig_PmType{
	"ani_g_instance_id": voltha.PmConfig_CONTEXT,
	"transmit_power":    voltha.PmConfig_GAUGE,
	"receive_power":     voltha.PmConfig_GAUGE,
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
	"ethernet_type":   voltha.PmConfig_GAUGE,
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

// Constants specific for L2 PM collection
const (
	L2PmCollectionInterval = 15 * 60 // Unit in seconds. Do not change this as this fixed by OMCI specification for L2 PM counters
	SyncTimeRetryInterval  = 15      // Unit seconds
	L2PmCreateAttempts     = 3
	L2PmCollectAttempts    = 3
	MaxL2PMGetPayLoadSize  = 29
)

// EthernetBridgeHistory specific constants
const (
	EthernetBridgeHistoryName      = "Ethernet_Bridge_Port_History"
	EthernetBridgeHistoryEnabled   = true // This setting can be changed from voltha NBI PmConfig configuration
	EthernetBridgeHistoryFrequency = L2PmCollectionInterval
)

// *** Classical L2 PM Counters end   ***

type groupMetric struct {
	groupName              string
	enabled                bool
	frequency              uint32 // valid only if FrequencyOverride is enabled.
	metricMap              map[string]voltha.PmConfig_PmType
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
	isL2PMCounter          bool      // true for only L2 PM counters
	collectAttempts        uint32    // number of attempts to collect L2 PM data
	createRetryAttempts    uint32    // number of attempts to try creating the L2 PM ME
}

type standaloneMetric struct {
	metricName             string
	enabled                bool
	frequency              uint32    // valid only if FrequencyOverride is enabled.
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
}

type onuMetricsManager struct {
	pDeviceHandler *deviceHandler
	pL2PMFsm       *fsm.FSM

	commMetricsChan chan Message

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

	nextGlobalMetricCollectionTime time.Time // valid only if pmConfig.FreqOverride is set to false.

	onuMetricsManagerLock sync.RWMutex
}

// newonuMetricsManager returns a new instance of the newonuMetricsManager
// Note that none of the context stored internally in onuMetricsManager is backed up on KV store for resiliency.
// Metric collection is not a critical operation that needs support for resiliency. On adapter restart, some context
// could be lost (except for Device.PmConfigs which is backed up the rw-core on KV store). An example of information
// that is lost on adapter restart is nextCollectionInterval time.
func newonuMetricsManager(ctx context.Context, dh *deviceHandler) *onuMetricsManager {

	var metricsManager onuMetricsManager
	logger.Debugw(ctx, "init-onuMetricsManager", log.Fields{"device-id": dh.deviceID})
	metricsManager.pDeviceHandler = dh

	metricsManager.commMetricsChan = make(chan Message)
	metricsManager.opticalMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.uniStatusMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.l2PmChan = make(chan me.AttributeValueMap)

	metricsManager.syncTimeResponseChan = make(chan bool)
	metricsManager.l2PmCreateOrDeleteResponseChan = make(chan bool)

	metricsManager.stopProcessingOmciResponses = make(chan bool)

	metricsManager.groupMetricMap = make(map[string]*groupMetric)
	metricsManager.standaloneMetricMap = make(map[string]*standaloneMetric)

	if dh.pmConfigs == nil { // dh.pmConfigs is NOT nil if adapter comes back from a restart. We should NOT go back to defaults in this case
		dh.pmConfigs = &voltha.PmConfigs{}
		dh.pmConfigs.Id = dh.deviceID
		dh.pmConfigs.DefaultFreq = DefaultMetricCollectionFrequency
		dh.pmConfigs.Grouped = GroupMetricEnabled
		dh.pmConfigs.FreqOverride = DefaultFrequencyOverrideEnabled

		// Populate group metrics.
		// Lets populate irrespective of GroupMetricEnabled is true or not.
		// The group metrics collection will decided on this flag later

		// Populate optical power group metrics
		var opPmConfigSlice []*voltha.PmConfig
		for k, v := range OpticalPowerGroupMetrics {
			opPmConfigSlice = append(opPmConfigSlice, &voltha.PmConfig{Name: k, Type: v})
		}
		opticalPowerGroupMetric := voltha.PmGroupConfig{
			GroupName: OpticalPowerGroupMetricName,
			Enabled:   OpticalPowerGroupMetricEnabled && dh.pOpenOnuAc.metricsEnabled,
			GroupFreq: OpticalPowerMetricGroupCollectionFrequency,
			Metrics:   opPmConfigSlice,
		}
		dh.pmConfigs.Groups = append(dh.pmConfigs.Groups, &opticalPowerGroupMetric)

		// Populate uni status group metrics
		var uniStPmConfigSlice []*voltha.PmConfig
		for k, v := range UniStatusGroupMetrics {
			uniStPmConfigSlice = append(uniStPmConfigSlice, &voltha.PmConfig{Name: k, Type: v})
		}
		uniStatusGroupMetric := voltha.PmGroupConfig{
			GroupName: UniStatusGroupMetricName,
			Enabled:   UniStatusGroupMetricEnabled && dh.pOpenOnuAc.metricsEnabled,
			GroupFreq: UniStatusMetricGroupCollectionFrequency,
			Metrics:   uniStPmConfigSlice,
		}
		dh.pmConfigs.Groups = append(dh.pmConfigs.Groups, &uniStatusGroupMetric)

		// classical l2 pm counter start

		// Populate ethernet bridge history group metrics
		var ethBridgeHistoryConfigSlice []*voltha.PmConfig
		for k, v := range EthernetBridgeHistory {
			ethBridgeHistoryConfigSlice = append(ethBridgeHistoryConfigSlice, &voltha.PmConfig{Name: k, Type: v})
		}
		ethBridgeHistoryGroupMetric := voltha.PmGroupConfig{
			GroupName: EthernetBridgeHistoryName,
			Enabled:   EthernetBridgeHistoryEnabled && dh.pOpenOnuAc.metricsEnabled,
			GroupFreq: EthernetBridgeHistoryFrequency,
			Metrics:   ethBridgeHistoryConfigSlice,
		}
		dh.pmConfigs.Groups = append(dh.pmConfigs.Groups, &ethBridgeHistoryGroupMetric)

		// classical l2 pm counter end

		// Add standalone metric (if present) after this (will be added to dh.pmConfigs.Metrics)
	}

	// Populate local group metric structures
	for _, g := range dh.pmConfigs.Groups {
		metricsManager.groupMetricMap[g.GroupName] = &groupMetric{
			groupName: g.GroupName,
			enabled:   g.Enabled,
			frequency: g.GroupFreq,
		}
		switch g.GroupName {
		case OpticalPowerGroupMetricName:
			metricsManager.groupMetricMap[g.GroupName].metricMap = OpticalPowerGroupMetrics
		case UniStatusGroupMetricName:
			metricsManager.groupMetricMap[g.GroupName].metricMap = UniStatusGroupMetrics
		case EthernetBridgeHistoryName:
			metricsManager.groupMetricMap[g.GroupName].metricMap = EthernetBridgeHistory
			metricsManager.groupMetricMap[g.GroupName].isL2PMCounter = true
		default:
			logger.Errorw(ctx, "unhandled-group-name", log.Fields{"groupName": g.GroupName})
		}
	}

	// Populate local standalone metric structures
	for _, m := range dh.pmConfigs.Metrics {
		metricsManager.standaloneMetricMap[m.Name] = &standaloneMetric{
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

	// L2 PM FSM related state machine
	metricsManager.pL2PMFsm = fsm.NewFSM(
		l2PmStNull,
		fsm.Events{
			{Name: l2PmEventInit, Src: []string{l2PmStNull}, Dst: l2PmStStarting},
			{Name: l2PmEventTick, Src: []string{l2PmStStarting}, Dst: l2PmStSyncTime},
			{Name: l2PmEventTick, Src: []string{l2PmStIdle}, Dst: l2PmStCollectData},
			{Name: l2PmEventSuccess, Src: []string{l2PmStSyncTime, l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStIdle},
			{Name: l2PmEventFailure, Src: []string{l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStIdle},
			{Name: l2PmEventFailure, Src: []string{l2PmStSyncTime}, Dst: l2PmStSyncTime},
			{Name: l2PmEventAddMe, Src: []string{l2PmStIdle}, Dst: l2PmStCreatePmMe},
			{Name: l2PmEventDeleteMe, Src: []string{l2PmStIdle}, Dst: l2PmStDeletePmMe},
			{Name: l2PmEventStop, Src: []string{l2PmStNull, l2PmStStarting, l2PmStSyncTime, l2PmStIdle, l2PmStCreatePmMe, l2PmStDeletePmMe, l2PmStCollectData}, Dst: l2PmStNull},
		},
		fsm.Callbacks{
			"enter_state":                func(e *fsm.Event) { metricsManager.l2PMFsmLogFsmStateChange(ctx, e) },
			"enter_" + l2PmStIdle:        func(e *fsm.Event) { metricsManager.l2PMFsmIdle(ctx, e) },
			"enter_" + l2PmStStarting:    func(e *fsm.Event) { metricsManager.l2PMFsmStarting(ctx, e) },
			"enter_" + l2PmStSyncTime:    func(e *fsm.Event) { metricsManager.l2PMFsmSyncTime(ctx, e) },
			"enter_" + l2PmStCollectData: func(e *fsm.Event) { metricsManager.l2PmFsmCollectData(ctx, e) },
			"enter_" + l2PmStCreatePmMe:  func(e *fsm.Event) { metricsManager.l2PmFsmCreatePM(ctx, e) },
			"enter_" + l2PmStDeletePmMe:  func(e *fsm.Event) { metricsManager.l2PmFsmDeletePM(ctx, e) },
		},
	)

	// initialize the next metric collection intervals.
	metricsManager.initializeMetricCollectionTime(ctx)
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
				if v.isL2PMCounter { // If it is a L2 PM counter we need to mark the PM to be added
					mm.l2PmToAdd = append(mm.l2PmToAdd, v.groupName)
				} else if mm.pDeviceHandler.pmConfigs.FreqOverride { // otherwise just update the next collection interval
					v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
				}
			} else { // group counter is disabled
				if v.isL2PMCounter { // If it is a L2 PM counter we need to mark the PM to be deleted
					mm.l2PmToDelete = append(mm.l2PmToDelete, v.groupName)
				}
			}
			updated = true
			logger.Infow(ctx, "group metric support updated", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "groupName": aGroupName, "enabled": group.Enabled})
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
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.AniGClassID, anigInstID, requestedAttributes, ConstDefaultOmciTimeout, true, mm.commMetricsChan); meInstance != nil {
			select {
			case meAttributes = <-mm.opticalMetricsChan:
				logger.Debugw(ctx, "received optical metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
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
				case "transmit_power":
					if val, ok := meAttributes["TransmitOpticalLevel"]; ok && val != nil {
						opticalMetrics[k] = float32(val.(uint16))
					}
				case "receive_power":
					if val, ok := meAttributes["OpticalSignalLevel"]; ok && val != nil {
						opticalMetrics[k] = float32(val.(uint16))
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
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.UniGClassID, unigInstID, requestedAttributes, ConstDefaultOmciTimeout, true, mm.commMetricsChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received uni-g metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
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
			var entityID uint32
			if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
				entityID = uint32(val.(uint16))
			}
			// TODO: Rlock needed for reading uniEntityMap?
			if uniPort, ok := mm.pDeviceHandler.uniEntityMap[entityID]; ok && uniPort != nil {
				unigMetrics["uni_port_no"] = float32(uniPort.portNo)
			}
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
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.PhysicalPathTerminationPointEthernetUniClassID, pptpInstID, requestedAttributes, ConstDefaultOmciTimeout, true, mm.commMetricsChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received pptp metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
				logger.Errorw(ctx, "timeout waiting for omci-get response for uni status", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
				// The metrics could be empty in this case
				break loop2
			}

			// Populate metric only if it was enabled.
			for k := range UniStatusGroupMetrics {
				switch k {
				case "ethernet_type":
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
		var entityID uint32
		if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
			entityID = uint32(val.(uint16))
		}
		// TODO: Rlock needed for reading uniEntityMap?
		if uniPort, ok := mm.pDeviceHandler.uniEntityMap[entityID]; ok && uniPort != nil {
			pptpMetrics["uni_port_no"] = float32(uniPort.portNo)
		}

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
		if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, me.VirtualEthernetInterfacePointClassID, veipInstID, requestedAttributes, ConstDefaultOmciTimeout, true, mm.commMetricsChan); meInstance != nil {
			// Wait for metrics or timeout
			select {
			case meAttributes = <-mm.uniStatusMetricsChan:
				logger.Debugw(ctx, "received veip metrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
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

		var entityID uint32
		if val, ok := meAttributes["ManagedEntityId"]; ok && val != nil {
			entityID = uint32(meAttributes["ManagedEntityId"].(uint16))
		}
		// TODO: Rlock needed for reading uniEntityMap?
		if uniPort, ok := mm.pDeviceHandler.uniEntityMap[entityID]; ok && uniPort != nil {
			veipMetrics["uni_port_no"] = float32(uniPort.portNo)
		}

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
		case message, ok := <-mm.commMetricsChan:
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
		case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID, me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID:
			mm.l2PmChan <- meAttributes
		default:
			logger.Errorw(ctx, "unhandled omci get response message",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "class-id": msgObj.EntityClass})
		}
	}

	return errors.New("unhandled-omci-get-response-message")
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
	case <-mm.commMetricsChan:
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
}

// ** L2 PM FSM Handlers start **

func (mm *onuMetricsManager) l2PMFsmLogFsmStateChange(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "FSM state change", log.Fields{"device-id": mm.pDeviceHandler.deviceID,
		"event name": e.Event, "src state": e.Src, "dst state": e.Dst})
}

func (mm *onuMetricsManager) l2PMFsmStarting(ctx context.Context, e *fsm.Event) {
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
					mm.l2PmToAdd = mm.appendIfMissing(mm.l2PmToAdd, n)
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
					mm.l2PmToDelete = mm.appendIfMissing(mm.l2PmToDelete, n)
				}
			}
		}
	}
	mm.onuMetricsManagerLock.Unlock()
	logger.Debugw(ctx, "pms to add and delete",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": mm.l2PmToAdd, "pms-to-delete": mm.l2PmToDelete})
	go func() {
		// push a tick event to move to next state
		if err := mm.pL2PMFsm.Event(l2PmEventTick); err != nil {
			logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PMFsmSyncTime(ctx context.Context, e *fsm.Event) {
	// Sync time with the ONU to establish 15min boundary for PM collection.
	if err := mm.syncTime(ctx); err != nil {
		go func() {
			time.Sleep(SyncTimeRetryInterval * time.Second) // retry to sync time after this timeout
			// This will result in FSM attempting to sync time again
			if err := mm.pL2PMFsm.Event(l2PmEventFailure); err != nil {
				logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	}
	go func() {
		if err := mm.pL2PMFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PMFsmIdle(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "Enter state idle", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	if len(mm.l2PmToDelete) > 0 {
		logger.Debugw(ctx, "state idle - pms to delete", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-delete": mm.l2PmToDelete})
		go func() {
			if err := mm.pL2PMFsm.Event(l2PmEventDeleteMe); err != nil {
				logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	} else if len(mm.l2PmToAdd) > 0 {
		logger.Debugw(ctx, "state idle - pms to add", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": mm.l2PmToAdd})
		go func() {
			if err := mm.pL2PMFsm.Event(l2PmEventAddMe); err != nil {
				logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
			}
		}()
	} else {
		logger.Debugw(ctx, "state idle - pushing tick after timeout for data collection",
			log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-delete": mm.l2PmToDelete})
		// below routine pushes a tick event for the FSM to collect PM data after L2PmCollectionInterval seconds
		go func() {
			time.Sleep(L2PmCollectionInterval * time.Second)
			_ = mm.pL2PMFsm.Event(l2PmEventTick)
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
		switch n {
		case EthernetBridgeHistoryName:
			logger.Debugw(ctx, "state collect data - collecting data for EthernetFramePerformanceMonitoringHistoryData ME", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
			var metricInfoSlice []*voltha.MetricInformation
			for _, uniPort := range mm.pDeviceHandler.uniEntityMap {
				// Attach the EthernetFramePerformanceMonitoringHistoryData ME to MacBridgePortConfigData on the UNI port
				entityID := macBridgePortAniEID + uniPort.entityID
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, true, entityID); metricInfo != nil { // upstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
				if metricInfo := mm.collectEthernetFramePerformanceMonitoringHistoryData(ctx, false, entityID); metricInfo != nil { // downstream
					metricInfoSlice = append(metricInfoSlice, metricInfo)
				}
			}
			// Publish metrics if it is valid
			if metricInfoSlice != nil {
				mm.publishMetrics(ctx, metricInfoSlice)
			} else {
				// If collectAttempts exceeds L2PmCollectAttempts then remove it from activeL2Pms
				// slice so that we do not collect data from that PM ME anymore
				mm.onuMetricsManagerLock.Lock()
				mm.groupMetricMap[n].collectAttempts++
				if mm.groupMetricMap[n].collectAttempts > L2PmCollectAttempts {
					mm.removeIfFound(mm.activeL2Pms, n)
				}
				logger.Warnw(ctx, "state collect data - no metrics collected",
					log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": n, "collectAttempts": mm.groupMetricMap[n].collectAttempts})
				mm.onuMetricsManagerLock.Unlock()
			}
		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
	}
	// Does not matter we send success or failure here.
	// Those PMs that we failed to collect data will be attempted to collect again in the next PM collection cycle (assuming
	// we have not exceed max attempts to collect the PM data)
	go func() {
		if err := mm.pL2PMFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PmFsmCreatePM(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "state create pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": mm.l2PmToAdd})
	for _, n := range mm.l2PmToAdd {
		resp := false
		switch n {
		case EthernetBridgeHistoryName:
			boolForDirection := make([]bool, 2) // stores true and false to indicate upstream and downstream directions.
			boolForDirection = append(boolForDirection, true, false)
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range boolForDirection {
			inner:
				for _, uniPort := range mm.pDeviceHandler.uniEntityMap {
					// Attach the EthernetFramePerformanceMonitoringHistoryData ME to MacBridgePortConfigData on the UNI port
					entityID := macBridgePortAniEID + uniPort.entityID
					mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
						ctx, ConstDefaultOmciTimeout, true, direction, true, mm.commMetricsChan, entityID)
					select {
					case resp = <-mm.l2PmCreateOrDeleteResponseChan:
						logger.Debugw(ctx, "received create EthernetFramePerformanceMonitoringHistoryData l2 pm me response",
							log.Fields{"device-id": mm.pDeviceHandler.deviceID, "resp": resp, "uni": uniPort.uniID})
						if !resp {
							// We will attempt to create the MEs again in the next L2 PM Collection cycle
							break inner
						}
					case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
						logger.Errorw(ctx, "timeout waiting for create EthernetFramePerformanceMonitoringHistoryData l2 pm me response",
							log.Fields{"device-id": mm.pDeviceHandler.deviceID, "uni": uniPort.uniID})
					}
				}
			}
		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
		// On success Update the local list maintained for active PMs and PMs to add
		if resp {
			mm.onuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.appendIfMissing(mm.activeL2Pms, n)
			mm.l2PmToAdd = mm.removeIfFound(mm.l2PmToAdd, n)
			mm.onuMetricsManagerLock.Unlock()
		} else {
			// If createRetryAttempts exceeds L2PmCreateAttempts then locally disable the PM
			// and also remove it from l2PmToAdd slice so that we do not try to create the PM ME anymore
			mm.onuMetricsManagerLock.Lock()
			mm.groupMetricMap[n].createRetryAttempts++
			if mm.groupMetricMap[n].createRetryAttempts > L2PmCreateAttempts {
				mm.groupMetricMap[n].enabled = false
				mm.removeIfFound(mm.l2PmToAdd, n)
			}
			logger.Warnw(ctx, "state create pm - failed to create pm",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "metricName": n, "createRetryAttempts": mm.groupMetricMap[n].createRetryAttempts})
			mm.onuMetricsManagerLock.Unlock()
		}
	}
	// Does not matter we send success or failure here.
	// Those PMs that we failed to create will be attempted to create again in the next PM creation cycle (assuming
	// we have not exceed max attempts to create the PM ME)
	go func() {
		if err := mm.pL2PMFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (mm *onuMetricsManager) l2PmFsmDeletePM(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "state delete pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "pms-to-add": mm.l2PmToAdd})
	for _, n := range mm.l2PmToDelete {
		resp := false
		switch n {
		case EthernetBridgeHistoryName:
			boolForDirection := make([]bool, 2) // stores true and false to indicate upstream and downstream directions.
			boolForDirection = append(boolForDirection, true, false)
			// Create ME twice, one for each direction. Boolean true is used to indicate upstream and false for downstream.
			for _, direction := range boolForDirection {
			inner:
				for _, uniPort := range mm.pDeviceHandler.uniEntityMap {
					// Attach the EthernetFramePerformanceMonitoringHistoryData ME to MacBridgePortConfigData on the UNI port
					entityID := macBridgePortAniEID + uniPort.entityID
					mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(
						ctx, ConstDefaultOmciTimeout, true, direction, false, mm.commMetricsChan, entityID)
					select {
					case resp = <-mm.l2PmCreateOrDeleteResponseChan:
						logger.Debugw(ctx, "received delete EthernetFramePerformanceMonitoringHistoryData l2 pm me response",
							log.Fields{"device-id": mm.pDeviceHandler.deviceID, "resp": resp, "uni": uniPort.uniID})
						if !resp {
							// We will attempt to delete the MEs again in the next L2 PM Collection cycle
							break inner
						}
					case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
						logger.Errorw(ctx, "timeout waiting for delete EthernetFramePerformanceMonitoringHistoryData l2 pm me response",
							log.Fields{"device-id": mm.pDeviceHandler.deviceID, "uni": uniPort.uniID})
					}
				}
			}
		default:
			logger.Errorw(ctx, "unsupported l2 pm", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "name": n})
		}
		// On success Update the local list maintained for active PMs and PMs to delete
		if resp {
			mm.onuMetricsManagerLock.Lock()
			mm.activeL2Pms = mm.removeIfFound(mm.activeL2Pms, n)
			mm.l2PmToDelete = mm.removeIfFound(mm.l2PmToDelete, n)
			mm.onuMetricsManagerLock.Unlock()
		}
	}
	// Does not matter we send success or failure here.
	// Those PMs that we failed to delete will be attempted to create again in the next PM collection cycle
	go func() {
		if err := mm.pL2PMFsm.Event(l2PmEventSuccess); err != nil {
			logger.Errorw(ctx, "error calling event event", log.Fields{"device-id": mm.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

// ** L2 PM FSM Handlers end **

// syncTime synchronizes time with the ONU to establish a 15 min boundary for PM collection and reporting.
func (mm *onuMetricsManager) syncTime(ctx context.Context) error {
	if err := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendSyncTime(ctx, ConstDefaultOmciTimeout, true, mm.commMetricsChan); err != nil {
		logger.Errorw(ctx, "cannot send sync time request", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return err
	}

	select {
	case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
		logger.Errorf(ctx, "timed out waiting for sync time response from onu", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
		return errors.New("timed-out-waiting-for-sync-time-response")
	case syncTimeRes := <-mm.syncTimeResponseChan:
		if !syncTimeRes {
			return errors.New("failed-to-sync-time")
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

	requestedAttributes := make(me.AttributeValueMap)
	size := 0
	intervalEndTime := -1
	ethPMHistData := make(map[string]float32)

	for _, v := range mEnt.GetAttributeDefinitions() {
		if (v.Size + size) <= MaxL2PMGetPayLoadSize {
			requestedAttributes[v.Name] = v.DefValue
			size = v.Size + size
		} else { // We exceeded the allow omci get size
			// Let's collect the attributes via get now and collect remaining in the next iteration
			if err := mm.populateEthernetBridgeHistoryMetrics(ctx, upstream, classID, entityID, meAttributes, requestedAttributes, ethPMHistData, &intervalEndTime); err != nil {
				logger.Errorw(ctx, "error during metric collection",
					log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream, "err": err})
				return nil
			}
			size = 0                                         // reset size
			requestedAttributes = make(me.AttributeValueMap) // reset map
		}
	}
	// Collect the omci get attributes.
	if len(requestedAttributes) > 0 {
		if err := mm.populateEthernetBridgeHistoryMetrics(ctx, upstream, classID, entityID, meAttributes, requestedAttributes, ethPMHistData, &intervalEndTime); err != nil {
			logger.Errorw(ctx, "error during metric collection",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream, "err": err})
			return nil
		}
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

	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().Unix()
	mmd := voltha.MetricMetaData{
		Title:           EthernetBridgeHistoryName,
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	// create slice of metrics given that there could be more than one VEIP instance
	metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: ethPMHistData}
	logger.Debugw(ctx, "collecting data for EthernetFramePerformanceMonitoringHistoryData successful",
		log.Fields{"device-id": mm.pDeviceHandler.deviceID, "entityID": entityID, "upstream": upstream, "metricInfo": metricInfo})
	return &metricInfo
}

// nolint: gocyclo
func (mm *onuMetricsManager) populateEthernetBridgeHistoryMetrics(ctx context.Context, upstream bool, classID me.ClassID, entityID uint16,
	meAttributes me.AttributeValueMap, requestedAttributes me.AttributeValueMap, ethPMHistData map[string]float32, intervalEndTime *int) error {
	if meInstance := mm.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetMe(ctx, classID, entityID, requestedAttributes, ConstDefaultOmciTimeout, true, mm.commMetricsChan); meInstance != nil {
		select {
		case meAttributes = <-mm.l2PmChan:
			logger.Debugw(ctx, "received ethernet pm history data metrics",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "upstream": upstream, "entityID": entityID})
		case <-time.After(time.Duration(ConstDefaultOmciTimeout) * time.Second):
			logger.Errorw(ctx, "timeout waiting for omci-get response for ethernet pm history data",
				log.Fields{"device-id": mm.pDeviceHandler.deviceID, "upstream": upstream, "entityID": entityID})
			// The metrics will be empty in this case
			return errors.New("timeout-during-l2-pm-collection-for-ethernet-bridge-history")
		}
		// verify that interval end time has not changed during metric collection. If it changed, we abort the procedure
		if *intervalEndTime == -1 { // first time
			// Update the interval end time
			if val, ok := meAttributes["IntervalEndTime"]; ok && val != nil {
				*intervalEndTime = int(meAttributes["IntervalEndTime"].(uint8))
			}
		} else {
			var currIntervalEndTime int
			if val, ok := meAttributes["IntervalEndTime"]; ok && val != nil {
				currIntervalEndTime = int(meAttributes["IntervalEndTime"].(uint8))
			}
			if currIntervalEndTime != *intervalEndTime { // interval end time changed during metric collection
				logger.Errorw(ctx, "interval end time changed during metrics collection for ethernet pm history data",
					log.Fields{"device-id": mm.pDeviceHandler.deviceID, "upstream": upstream, "entityID": entityID})
				return errors.New("interval-end-time-changed-during-metric-collection-for-ethernet-bridge-history")
			}
		}
	}
	for k := range EthernetBridgeHistory {
		switch k {
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
	case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID, me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID:
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
	return errors.New("unhandled-omci-create-response-message")
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
	case me.EthernetFramePerformanceMonitoringHistoryDataUpstreamClassID, me.EthernetFramePerformanceMonitoringHistoryDataDownstreamClassID:
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
	return errors.New("unhandled-omci-delete-response-message")
}

func (mm *onuMetricsManager) resetActiveL2PMList() {
	mm.activeL2Pms = nil
}

func (mm *onuMetricsManager) appendIfMissing(slice []string, n string) []string {
	for _, ele := range slice {
		if ele == n {
			return slice
		}
	}
	return append(slice, n)
}

func (mm *onuMetricsManager) removeIfFound(slice []string, n string) []string {
	for i, ele := range slice {
		if ele == n {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}
