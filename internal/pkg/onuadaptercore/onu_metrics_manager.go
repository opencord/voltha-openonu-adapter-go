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
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
	"sync"
	"time"
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

// EthernetBridgeHistory specific constants
const (
	EthernetBridgeHistoryName      = "Ethernet_Bridge_Port_History"
	EthernetBridgeHistoryEnabled   = true    // This setting can be changed from voltha NBI PmConfig configuration
	EthernetBridgeHistoryFrequency = 15 * 60 // unit in seconds. This setting can be changed from voltha NBI PmConfig configuration, BUT DONT!
)

// *** Classical L2 PM Counters end   ***

type groupMetric struct {
	groupName              string
	enabled                bool
	frequency              uint32 // valid only if FrequencyOverride is enabled.
	metricMap              map[string]voltha.PmConfig_PmType
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
}

type standaloneMetric struct {
	metricName             string
	enabled                bool
	frequency              uint32    // valid only if FrequencyOverride is enabled.
	nextCollectionInterval time.Time // valid only if FrequencyOverride is enabled.
}

type onuMetricsManager struct {
	pDeviceHandler *deviceHandler

	commMetricsChan chan Message

	opticalMetricsChan   chan me.AttributeValueMap
	uniStatusMetricsChan chan me.AttributeValueMap

	// Channels used for classical l2 pm counters
	syncTimeResponseChan chan bool

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

	metricsManager.syncTimeResponseChan = make(chan bool)

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
			if v.enabled {
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
		if k == aGroupName {
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
			// If the group is now enabled and frequency override is enabled, set the next group metric collection time
			if group.Enabled && mm.pDeviceHandler.pmConfigs.FreqOverride {
				v.nextCollectionInterval = time.Now().Add(time.Duration(v.frequency) * time.Second)
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

func (mm *onuMetricsManager) initializeClassicalL2PMIntervalCounters(ctx context.Context) error {
	logger.Infow(ctx, "initializing classical l2 pm interval counters", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	if err := mm.syncTime(ctx); err != nil {
		// no point in proceeding further if we cannot sync time with ONU and set the 15 min boundary for PM collection
		// Should we retry?
		return err
	}
	mm.createClassicalL2PM(ctx)
	return nil
}

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

func (mm *onuMetricsManager) createClassicalL2PM(ctx context.Context) {

}

func (mm *onuMetricsManager) deletePM(ctx context.Context) {

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
}
