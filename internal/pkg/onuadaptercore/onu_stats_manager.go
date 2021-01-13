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
	"fmt"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
	"time"
)

type onuStatsManager struct {
	pDeviceHandler *deviceHandler
	pDevOmciCC     *omciCC
}

// newOnuStatsManager returns a new instance of the newOnuStatsManager
func newOnuStatsManager(ctx context.Context, dh *deviceHandler) *onuStatsManager {

	var statsManager onuStatsManager
	logger.Debugw(ctx, "init-OnuStatsManager", log.Fields{"device-id": dh.deviceID})
	statsManager.pDeviceHandler = dh
	statsManager.pDevOmciCC = dh.pOnuOmciDevice.PDevOmciCC
	return &statsManager
}

func (stats *onuStatsManager) collectOpticalMetrics(ctx context.Context) []*voltha.MetricInformation {

	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", stats.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", stats.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = stats.pDeviceHandler.DeviceType

	raisedTs := time.Now().UnixNano()
	mmd := voltha.MetricMetaData{
		Title:    "OpticalMetrics", // Is this ok to hard code?
		Ts:       float64(raisedTs),
		Context:  metricsContext,
		DeviceId: stats.pDeviceHandler.deviceID,
	}

	enabledMetrics := make([]string, 0)
	// Populate enabled metrics
	for _, m := range stats.pDeviceHandler.pmMetrics.ToPmConfigs().Metrics {
		if m.Enabled {
			enabledMetrics = append(enabledMetrics, m.Name)
		}
	}

	// get the ANI-G instance IDs
	anigInstKeys := stats.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.AniGClassID)
	for _, anigInstID := range anigInstKeys {
		opticalMetrics := make(map[string]float32)

		// Get the ANI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"OpticalSignalLevel": 0, "TransmitOpticalLevel": 0}
		meInstance := stats.pDevOmciCC.sendGetMe(ctx, me.AniGClassID, anigInstID, requestedAttributes, ConstDefaultOmciTimeout, true)
		meAttributes := meInstance.GetAttributeValueMap()

		// Populate metric only if it was enabled.
		for _, v := range enabledMetrics {
			switch v {
			case "transmit_power":
				opticalMetrics["transmit_power"] = float32(meAttributes["OpticalSignalLevel"].(uint16))
			case "receive_power":
				opticalMetrics["receive_power"] = float32(meAttributes["TransmitOpticalLevel"].(uint16))
			default:
				// do nothing
			}
		}
		// create slice of metrics given that there could be more than one ANI-G instance and
		// optical metrics are collected per ANI-G instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: opticalMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	return metricInfoSlice
}

// Note: UNI status does not seem to be a metric, but this is being treated as metric in Python implementation
func (stats *onuStatsManager) collectUniStatusMetrics(ctx context.Context) []*voltha.MetricInformation {
	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", stats.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", stats.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = stats.pDeviceHandler.DeviceType

	raisedTs := time.Now().UnixNano()
	mmd := voltha.MetricMetaData{
		Title:    "UniStatus", // Is this ok to hard code?
		Ts:       float64(raisedTs),
		Context:  metricsContext,
		DeviceId: stats.pDeviceHandler.deviceID,
	}

	enabledMetrics := make([]string, 0)
	// Populate enabled metrics
	for _, m := range stats.pDeviceHandler.pmMetrics.ToPmConfigs().Metrics {
		if m.Enabled {
			enabledMetrics = append(enabledMetrics, m.Name)
		}
	}

	// get the UNI-G instance IDs
	unigInstKeys := stats.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.UniGClassID)
	for _, anigInstID := range unigInstKeys {
		unigMetrics := make(map[string]float32)

		// Get the UNI-G instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"AdministrativeState": 0}
		meInstance := stats.pDevOmciCC.sendGetMe(ctx, me.UniGClassID, anigInstID, requestedAttributes, ConstDefaultOmciTimeout, true)
		meAttributes := meInstance.GetAttributeValueMap()

		// Populate metric only if it was enabled.
		for _, v := range enabledMetrics {
			switch v {
			case "uni_admin_state":
				unigMetrics["uni_admin_state"] = float32(meAttributes["AdministrativeState"].(byte))
			default:
				// do nothing
			}
		}
		// create slice of metrics given that there could be more than one UNI-G instance and
		// optical metrics are collected per UNI-G instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: unigMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	// get the PPTP instance IDs
	pptpInstKeys := stats.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, me.PhysicalPathTerminationPointEthernetUniClassID)
	for _, anigInstID := range pptpInstKeys {
		pptpMetrics := make(map[string]float32)

		// Get the PPTP instance optical power attributes
		requestedAttributes := me.AttributeValueMap{"SensedType": 0, "OperationalState": 0}
		meInstance := stats.pDevOmciCC.sendGetMe(ctx, me.PhysicalPathTerminationPointEthernetUniClassID, anigInstID, requestedAttributes, ConstDefaultOmciTimeout, true)
		meAttributes := meInstance.GetAttributeValueMap()

		// Populate metric only if it was enabled.
		for _, v := range enabledMetrics {
			switch v {
			case "ethernet_type":
				pptpMetrics["ethernet_type"] = float32(meAttributes["SensedType"].(byte))
			case "oper_status":
				pptpMetrics["oper_status"] = float32(meAttributes["OperationalState"].(byte))
			case "uni_admin_state":
				pptpMetrics["uni_admin_state"] = float32(meAttributes["AdministrativeState"].(byte))
			default:
				// do nothing
			}
		}
		// create slice of metrics given that there could be more than one PPTP instance and
		// optical metrics are collected per PPTP instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: pptpMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}
	return metricInfoSlice
}

// publishMetrics publishes the metrics on kafka
func (stats *onuStatsManager) publishMetrics(ctx context.Context, metricInfo []*voltha.MetricInformation) {
	var ke voltha.KpiEvent2
	ts := time.Now().UnixNano()
	ke.SliceData = metricInfo
	ke.Type = voltha.KpiEventType_slice
	ke.Ts = float64(ts)

	if err := stats.pDeviceHandler.EventProxy.SendKpiEvent(ctx, "STATS_EVENT", &ke, voltha.EventCategory_EQUIPMENT, voltha.EventSubCategory_ONU, ts); err != nil {
		logger.Errorw(ctx, "failed-to-send-pon-stats", log.Fields{"err": err})
	}
}
