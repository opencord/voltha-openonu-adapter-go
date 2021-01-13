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
	"time"
)

type onuMetricsManager struct {
	pDeviceHandler *deviceHandler

	commMetricsChan      chan Message
	opticalMetricsChan   chan me.AttributeValueMap
	uniStatusMetricsChan chan me.AttributeValueMap

	stopProcessingOmciResponses chan bool
}

// newonuMetricsManager returns a new instance of the newonuMetricsManager
func newonuMetricsManager(ctx context.Context, dh *deviceHandler) *onuMetricsManager {

	var metricsManager onuMetricsManager
	logger.Debugw(ctx, "init-onuMetricsManager", log.Fields{"device-id": dh.deviceID})
	metricsManager.pDeviceHandler = dh

	metricsManager.commMetricsChan = make(chan Message)
	metricsManager.opticalMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.uniStatusMetricsChan = make(chan me.AttributeValueMap)
	metricsManager.stopProcessingOmciResponses = make(chan bool)

	return &metricsManager
}

func (mm *onuMetricsManager) collectOpticalMetrics(ctx context.Context) []*voltha.MetricInformation {
	logger.Debugw(ctx, "collectOpticalMetrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().UnixNano()
	mmd := voltha.MetricMetaData{
		Title:           "OpticalMetrics",
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	enabledMetrics := make([]string, 0)
	// Populate enabled metrics
	for _, m := range mm.pDeviceHandler.pmMetrics.ToPmConfigs().Metrics {
		if m.Enabled {
			enabledMetrics = append(enabledMetrics, m.Name)
		}
	}
	logger.Debugw(ctx, "enabled metrics", log.Fields{"enabledMetrics": enabledMetrics})
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
			for _, v := range enabledMetrics {
				switch v {
				case "transmit_power":
					opticalMetrics["transmit_power"] = float32(meAttributes["TransmitOpticalLevel"].(uint16))
				case "receive_power":
					opticalMetrics["receive_power"] = float32(meAttributes["OpticalSignalLevel"].(uint16))
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

// Note: UNI status does not seem to be a metric, but this is being treated as metric in Python implementation
// nolint: gocyclo
func (mm *onuMetricsManager) collectUniStatusMetrics(ctx context.Context) []*voltha.MetricInformation {
	logger.Debugw(ctx, "collectUniStatusMetrics", log.Fields{"device-id": mm.pDeviceHandler.deviceID})
	var metricInfoSlice []*voltha.MetricInformation
	metricsContext := make(map[string]string)
	metricsContext["onuID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.OnuId)
	metricsContext["intfID"] = fmt.Sprintf("%d", mm.pDeviceHandler.device.ProxyAddress.ChannelId)
	metricsContext["devicetype"] = mm.pDeviceHandler.DeviceType

	raisedTs := time.Now().UnixNano()
	mmd := voltha.MetricMetaData{
		Title:           "UniStatus", // Is this ok to hard code?
		Ts:              float64(raisedTs),
		Context:         metricsContext,
		DeviceId:        mm.pDeviceHandler.deviceID,
		LogicalDeviceId: mm.pDeviceHandler.logicalDeviceID,
		SerialNo:        mm.pDeviceHandler.device.SerialNumber,
	}

	enabledMetrics := make([]string, 0)
	// Populate enabled metrics
	for _, m := range mm.pDeviceHandler.pmMetrics.ToPmConfigs().Metrics {
		if m.Enabled {
			enabledMetrics = append(enabledMetrics, m.Name)
		}
	}
	logger.Debugw(ctx, "enabled metrics", log.Fields{"enabledMetrics": enabledMetrics})

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
			for _, v := range enabledMetrics {
				switch v {
				case "uni_admin_state":
					unigMetrics["uni_admin_state"] = float32(meAttributes["AdministrativeState"].(byte))
				default:
					// do nothing
				}
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
		pptpMetrics := make(map[string]float32)

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
			for _, v := range enabledMetrics {
				switch v {
				case "oper_status":
					pptpMetrics["oper_status"] = float32(meAttributes["OperationalState"].(byte))
				case "uni_admin_state":
					pptpMetrics["uni_admin_state"] = float32(meAttributes["AdministrativeState"].(byte))
				default:
					// do nothing
				}
			}
		}
		// create slice of metrics given that there could be more than one VEIP instance
		metricInfo := voltha.MetricInformation{Metadata: &mmd, Metrics: pptpMetrics}
		metricInfoSlice = append(metricInfoSlice, &metricInfo)
	}

	return metricInfoSlice
}

// publishMetrics publishes the metrics on kafka
func (mm *onuMetricsManager) publishMetrics(ctx context.Context, metricInfo []*voltha.MetricInformation) {
	var ke voltha.KpiEvent2
	ts := time.Now().UnixNano()
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
