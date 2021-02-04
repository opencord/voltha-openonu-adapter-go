/*
 * Copyright 2021-present Open Networking Foundation
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
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
	"sync"
	"time"

	"errors"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

type alarmStoreInfo struct {
	classID    me.ClassID
	instanceID uint16
	alarmNo    uint8
}

type alarmStore map[alarmStoreInfo]struct{}

type onuAlarmManager struct {
	pDeviceHandler             *deviceHandler
	eventProxy                 eventif.EventProxy
	stopProcessingOmciMessages chan bool
	eventChannel               chan Message
	onuAlarmManagerLock        sync.RWMutex
	processMessage             bool
	activeAlarmsStore          alarmStore
}

func newAlarmManager(ctx context.Context, dh *deviceHandler) *onuAlarmManager {
	var alarmManager onuAlarmManager
	logger.Debugw(ctx, "init-alarmManager", log.Fields{"device-id": dh.deviceID})
	alarmManager.pDeviceHandler = dh
	alarmManager.eventProxy = dh.EventProxy // Or event proxy should be on cluster address ??
	alarmManager.eventChannel = make(chan Message)
	alarmManager.stopProcessingOmciMessages = make(chan bool)
	alarmManager.processMessage = false
	alarmManager.activeAlarmsStore = make(map[alarmStoreInfo]struct{})
	return &alarmManager
}

func (am *onuAlarmManager) startOMCIAlarmMessageProcessing(ctx context.Context) {
	am.onuAlarmManagerLock.Lock()
	am.processMessage = true
	am.onuAlarmManagerLock.Unlock()
	if stop := <-am.stopProcessingOmciMessages; stop {
		am.onuAlarmManagerLock.Lock()
		am.processMessage = false
		am.onuAlarmManagerLock.Unlock()
	}
}

func (am *onuAlarmManager) handleOmciAlarmNotificationMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "OMCI Alarm Notification Msg", log.Fields{"device-id": am.pDeviceHandler.deviceID,
		"msgType": msg.OmciMsg.MessageType})
	am.onuAlarmManagerLock.Lock()
	defer am.onuAlarmManagerLock.Unlock()

	if am.processMessage {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeAlarmNotification)
		if msgLayer == nil {
			logger.Errorw(ctx, "Omci Msg layer could not be detected for Alarm Notification",
				log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
		msgObj, msgOk := msgLayer.(*omci.AlarmNotificationMsg)
		if !msgOk {
			logger.Errorw(ctx, "Omci Msg layer could not be assigned for Alarm Notification",
				log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
		//Alarm Notification decoding at omci lib validates that the me class ID supports the
		// alarm notifications.
		logger.Debugw(ctx, "Alarm Notification Data", log.Fields{"device-id": am.pDeviceHandler.deviceID, "data-fields": msgObj})
		if err := am.processAlarmData(ctx, msgObj); err != nil {
			logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.pDeviceHandler.deviceID})
		}

	}
}

func (am *onuAlarmManager) processAlarmData(ctx context.Context, msg *omci.AlarmNotificationMsg) error {
	classID := msg.EntityClass
	sequenceNo := msg.AlarmSequenceNumber
	meInstance := msg.EntityInstance
	alarmBitmap := msg.AlarmBitmap
	logger.Debugw(ctx, "processing-alarm-data", log.Fields{"class-id": classID, "instance-id": meInstance,
		"alarmBitMap": alarmBitmap, "sequence-no": sequenceNo})
	/*
		if sequenceNo > 0 {
			// TODO Need Auditing if sequence no does not matches, after incrementing the last sequence no by 1
		}
	*/

	entity, omciErr := me.LoadManagedEntityDefinition(classID,
		me.ParamData{EntityID: meInstance})
	if omciErr.StatusCode() != me.Success {
		//log error and return
		logger.Error(ctx, "unable-to-get-managed-entity", log.Fields{"class-id": classID, "instance-id": meInstance})
		return errors.New("unable-to-get-managed-entity")
	}
	meAlarmMap := entity.GetAlarmMap()
	if meAlarmMap == nil {
		logger.Error(ctx, "unable-to-get-managed-entity-alarm-map", log.Fields{"class-id": classID, "instance-id": meInstance})
		return errors.New("unable-to-get-managed-entity-alarm-map")
	}
	// Loop over the supported alarm list for this me
	for alarmNo := range meAlarmMap {
		// Check if alarmNo was previously active in the alarm store, if yes clear it and remove it from active alarms
		_, exists := am.activeAlarmsStore[alarmStoreInfo{
			classID:    classID,
			instanceID: meInstance,
			alarmNo:    alarmNo,
		}]
		if exists {
			// Clear this alarm if It is cleared now, in that case IsAlarmClear would return true
			cleared, err := msg.IsAlarmClear(alarmNo)
			if err != nil {
				logger.Warnw(ctx, "unable-to-find-out-alarm-is-cleared", log.Fields{"class-id": classID,
					"instance-id": meInstance, "alarm-no": alarmNo})
			}
			if cleared {
				// Clear this alarm.
				am.clearAlarm(ctx, classID, meInstance, alarmNo)
			}
		} else {
			// If alarm entry was not present in the list of active alarms, we need to see if this alarm is now active
			// or not, if yes then raise it.
			raised, err := msg.IsAlarmActive(alarmNo)
			if err != nil {
				logger.Warnw(ctx, "unable-to-find-out-alarm-is-raised", log.Fields{"class-id": classID,
					"instance-id": meInstance, "alarm-no": alarmNo})
			}
			if raised {
				am.raiseAlarm(ctx, classID, meInstance, alarmNo)
			}
		}
	}
	return nil
}

func (am *onuAlarmManager) raiseAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	am.activeAlarmsStore[alarmStoreInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	}] = struct{}{}
	go am.sendAlarm(ctx, classID, instanceID, alarm, true)
}

func (am *onuAlarmManager) clearAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	go am.sendAlarm(ctx, classID, instanceID, alarm, false)
	delete(am.activeAlarmsStore, alarmStoreInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	})
}

func (am *onuAlarmManager) getIntfIDAlarm(ctx context.Context, classID me.ClassID, instanceID uint16) *uint32 {
	var intfID *uint32
	if classID == me.CircuitPackClassID || classID == me.PhysicalPathTerminationPointEthernetUniClassID {
		if uniPort, ok := am.pDeviceHandler.uniEntityMap[uint32(instanceID)]; ok && uniPort != nil {
			intfID = &uniPort.portNo
			return intfID
		}
	} else if classID == me.AniGClassID || classID == me.OnuGClassID {
		intfID = &am.pDeviceHandler.ponPortNumber
		return intfID
	}
	return nil
}

func (am *onuAlarmManager) sendAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8, raised bool) {
	context := make(map[string]string)
	intfID := am.getIntfIDAlarm(ctx, classID, instanceID)
	onuID := am.pDeviceHandler.deviceID
	serialNo := am.pDeviceHandler.pOnuOmciDevice.serialNumber
	if intfID == nil {
		logger.Warn(ctx, "intf-id-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	context["onu-intf-id"] = string(*intfID)
	context["onu-id"] = onuID
	context["onu-serial-number"] = serialNo

	raisedTimestamp := time.Now().UnixNano()
	eventDetails, err := getDeviceEventData(ctx, classID, alarm)
	if err != nil {
		logger.Warn(ctx, "event-details-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	suffixDesc := "Raised"
	if !raised {
		suffixDesc = "Cleared"
	}
	deviceEvent := &voltha.DeviceEvent{
		ResourceId:      onuID,
		DeviceEventName: fmt.Sprintf("%s_RAISE_EVENT", eventDetails.EventName),
		Description: fmt.Sprintf("%s Event - %s - %s", eventDetails.EventDescription, eventDetails.EventName,
			suffixDesc),
		Context: context,
	}
	_ = am.eventProxy.SendDeviceEvent(ctx, deviceEvent, eventDetails.EventCategory, eventDetails.EventSubCategory,
		raisedTimestamp)
}
