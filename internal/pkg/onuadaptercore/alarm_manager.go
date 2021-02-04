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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

const (
	circuitPackClassID                             = me.CircuitPackClassID
	physicalPathTerminationPointEthernetUniClassID = me.PhysicalPathTerminationPointEthernetUniClassID
	onuGClassID                                    = me.OnuGClassID
	aniGClassID                                    = me.AniGClassID
)

type alarmInfo struct {
	classID    me.ClassID
	instanceID uint16
	alarmNo    uint8
}

type alarms map[alarmInfo]struct{}

type onuAlarmManager struct {
	pDeviceHandler             *deviceHandler
	eventProxy                 eventif.EventProxy
	stopProcessingOmciMessages chan bool
	eventChannel               chan Message
	onuAlarmManagerLock        sync.RWMutex
	processMessage             bool
	activeAlarms               alarms
}

type onuDevice struct {
	classID me.ClassID
	alarmno uint8
}
type onuDeviceEvent struct {
	EventName        string
	EventCategory    eventif.EventCategory
	EventSubCategory eventif.EventSubCategory
	EventDescription string
}

func newAlarmManager(ctx context.Context, dh *deviceHandler) *onuAlarmManager {
	var alarmManager onuAlarmManager
	logger.Debugw(ctx, "init-alarmManager", log.Fields{"device-id": dh.deviceID})
	alarmManager.pDeviceHandler = dh
	alarmManager.eventProxy = dh.EventProxy // Or event proxy should be on cluster address ??
	alarmManager.eventChannel = make(chan Message)
	alarmManager.stopProcessingOmciMessages = make(chan bool)
	alarmManager.processMessage = false
	alarmManager.activeAlarms = make(map[alarmInfo]struct{})
	return &alarmManager
}

// getDeviceEventData returns the event data for a device
func (am *onuAlarmManager) getDeviceEventData(ctx context.Context, classID me.ClassID, alarmNo uint8) (onuDeviceEvent, error) {
	onuEventsList := map[onuDevice]onuDeviceEvent{
		onuDevice{classID: circuitPackClassID, alarmno: 0}: onuDeviceEvent{EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu equipment"},
		onuDevice{classID: circuitPackClassID, alarmno: 2}: onuDeviceEvent{EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu self-test failure"},
		onuDevice{classID: circuitPackClassID, alarmno: 3}: onuDeviceEvent{EventName: "ONU_LASER_EOL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu laser EOL"},
		onuDevice{classID: circuitPackClassID, alarmno: 4}: onuDeviceEvent{EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature yellow"},
		onuDevice{classID: circuitPackClassID, alarmno: 5}: onuDeviceEvent{EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature red"},
		onuDevice{classID: physicalPathTerminationPointEthernetUniClassID, alarmno: 0}: onuDeviceEvent{EventName: "ONU_Ethernet_UNI", EventCategory: voltha.EventCategory_EQUIPMENT,
			EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "LAN Loss Of Signal"},
		onuDevice{classID: onuGClassID, alarmno: 0}: onuDeviceEvent{EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu equipment"},
		onuDevice{classID: onuGClassID, alarmno: 6}: onuDeviceEvent{EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu self-test failure"},
		onuDevice{classID: onuGClassID, alarmno: 7}: onuDeviceEvent{EventName: "ONU_DYING_GASP",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu DYING_GASP"},
		onuDevice{classID: onuGClassID, alarmno: 8}: onuDeviceEvent{EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature yellow"},
		onuDevice{classID: onuGClassID, alarmno: 9}: onuDeviceEvent{EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature red"},
		onuDevice{classID: onuGClassID, alarmno: 10}: onuDeviceEvent{EventName: "ONU_VOLTAGE_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu voltage yellow"},
		onuDevice{classID: onuGClassID, alarmno: 11}: onuDeviceEvent{EventName: "ONU_VOLTAGE_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu voltage red"},
		onuDevice{classID: aniGClassID, alarmno: 0}: onuDeviceEvent{EventName: "ONU_LOW_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu low rx optical power"},
		onuDevice{classID: aniGClassID, alarmno: 1}: onuDeviceEvent{EventName: "ONU_HIGH_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu high rx optical power"},
		onuDevice{classID: aniGClassID, alarmno: 4}: onuDeviceEvent{EventName: "ONU_LOW_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu low tx optical power"},
		onuDevice{classID: aniGClassID, alarmno: 5}: onuDeviceEvent{EventName: "ONU_HIGH_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu high tx optical power"},
		onuDevice{classID: aniGClassID, alarmno: 6}: onuDeviceEvent{EventName: "ONU_LASER_BIAS_CURRENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu laser bias current"},
	}
	if onuEventDetails, ok := onuEventsList[onuDevice{classID: classID, alarmno: alarmNo}]; ok {
		return onuEventDetails, nil
	}
	return onuDeviceEvent{}, errors.New("onu Event Detail not found")

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

	} else {
		logger.Warnw(ctx, "ignoring-alarm-notification-received-for-me-as-channel-for-processing-is-closed",
			log.Fields{"device-id": am.pDeviceHandler.deviceID})
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
		return fmt.Errorf("unable-to-get-managed-entity-class-%d-instance-%d", classID, meInstance)
	}
	meAlarmMap := entity.GetAlarmMap()
	if meAlarmMap == nil {
		logger.Error(ctx, "unable-to-get-managed-entity-alarm-map", log.Fields{"class-id": classID, "instance-id": meInstance})
		return fmt.Errorf("unable-to-get-managed-entity-alarm-map-%d-instance-%d", classID, meInstance)
	}
	// Loop over the supported alarm list for this me
	for alarmNo := range meAlarmMap {
		// Check if alarmNo was previously active in the alarms, if yes clear it and remove it from active alarms
		_, exists := am.activeAlarms[alarmInfo{
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
				return err
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
				return err
			}
			if raised {
				am.raiseAlarm(ctx, classID, meInstance, alarmNo)
			}
		}
	}
	return nil
}

func (am *onuAlarmManager) raiseAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	am.activeAlarms[alarmInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	}] = struct{}{}
	go am.sendAlarm(ctx, classID, instanceID, alarm, true)
}

func (am *onuAlarmManager) clearAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	go am.sendAlarm(ctx, classID, instanceID, alarm, false)
	delete(am.activeAlarms, alarmInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	})
}

func (am *onuAlarmManager) getIntfIDAlarm(ctx context.Context, classID me.ClassID, instanceID uint16) *uint32 {
	var intfID *uint32
	if classID == circuitPackClassID || classID == physicalPathTerminationPointEthernetUniClassID {
		for _, uniPort := range am.pDeviceHandler.uniEntityMap {
			if uniPort.entityID == instanceID {
				intfID = &uniPort.portNo
				return intfID
			}
		}
	} else if classID == aniGClassID || classID == onuGClassID {
		intfID = &am.pDeviceHandler.ponPortNumber
		return intfID
	} else {
		logger.Warnw(ctx, "me-not-supported", log.Fields{"class-id": classID, "instance-id": instanceID})
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
	context["onu-intf-id"] = fmt.Sprintf("%d", *intfID)
	context["onu-id"] = onuID
	context["onu-serial-number"] = serialNo

	raisedTimestamp := time.Now().UnixNano()
	eventDetails, err := am.getDeviceEventData(ctx, classID, alarm)
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
