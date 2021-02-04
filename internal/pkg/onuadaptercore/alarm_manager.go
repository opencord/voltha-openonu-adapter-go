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
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/db"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/onu"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

const alarmBitMapSize = omci.AlarmBitmapSize / 8

type onuAlarmManager struct {
	pDeviceHandler             *deviceHandler
	eventProxy                 eventif.EventProxy
	stopProcessingOmciMessages chan bool
	eventChannel               chan Message
	onuAlarmManagerLock        sync.RWMutex
	processMessage             bool
	alarmKVStore               *db.Backend
	alarmKVStoreMutex          sync.RWMutex
}

func incrementAlarmSequence() {
	// increment alarm sequence
	// wrap to 1 after 255
}

func decrementAlarmSequence() {
	// decrement alarm sequence
}

func newAlarmManager(ctx context.Context, dh *deviceHandler) *onuAlarmManager {
	var alarmManager onuAlarmManager
	logger.Debugw(ctx, "init-alarmManager", log.Fields{"device-id": dh.deviceID})
	alarmManager.pDeviceHandler = dh
	alarmManager.eventProxy = dh.EventProxy // Or event proxy should be on cluster address ??
	alarmManager.eventChannel = make(chan Message)
	alarmManager.processMessage = false
	alarmManager.alarmKVStore = alarmManager.pDeviceHandler.setBackend(ctx, cBasePathAlarmKvStore)
	if alarmManager.alarmKVStore == nil {
		logger.Errorw(ctx, "Can't access onuAlarmKVStore - no backend connection to service",
			log.Fields{"device-id": dh.deviceID, "service": cBasePathAlarmKvStore})
	}
	return &alarmManager
}

func (am *onuAlarmManager) processOMCIAlarmMsgs(ctx context.Context) {
	am.onuAlarmManagerLock.Lock()
	am.processMessage = true
	am.onuAlarmManagerLock.Unlock()
	for {
		select {
		case <-am.stopProcessingOmciMessages:
			am.onuAlarmManagerLock.Lock()
			am.processMessage = false
			am.onuAlarmManagerLock.Unlock()
		}
	}
}

func (am *onuAlarmManager) handleOmciAlarmNotificationMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "OMCI Alarm Notification Msg", log.Fields{"device-id": am.pDeviceHandler.deviceID,
		"msgType": msg.OmciMsg.MessageType})
	am.onuAlarmManagerLock.Lock()
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
		am.processAlarmData(ctx, msgObj)

	}
	am.onuAlarmManagerLock.Unlock()
}

func (am *onuAlarmManager) getMEInstanceAlarmBitMapFromDB(ctx context.Context, classID me.ClassID, instanceID uint16) ([alarmBitMapSize]byte, error) {
	am.alarmKVStoreMutex.RLock()
	defer am.alarmKVStoreMutex.RUnlock()
	var bitMap [alarmBitMapSize]byte
	path := fmt.Sprintf(cSuffixAlarmKvStore, am.pDeviceHandler.deviceID, classID, instanceID)
	value, err := am.alarmKVStore.Get(log.WithSpanFromContext(context.TODO(), ctx), path)
	if err != nil {
		logger.Errorw(ctx, "failed-to-retreive-alarm-bitmap", log.Fields{"device-id": am.pDeviceHandler.deviceID,
			"class-id": classID, "instance-id": instanceID})
		bitmap := [alarmBitMapSize]byte{0}
		return bitmap, err
	}
	strBitMap, _ := kvstore.ToString(value.Value)
	copy(bitMap[:], strBitMap)
	return bitMap, nil
}

func (am *onuAlarmManager) putMEInstanceAlarmBitMapFromDB(ctx context.Context, classID me.ClassID, instanceID uint16, alarmBitMap [alarmBitMapSize]byte) error {
	am.alarmKVStoreMutex.Lock()
	path := fmt.Sprintf(cSuffixAlarmKvStore, am.pDeviceHandler.deviceID, classID, instanceID)
	fmt.Println(path)
	am.alarmKVStore.Put(log.WithSpanFromContext(context.TODO(), ctx), path, string(alarmBitMap[:]))
	am.alarmKVStoreMutex.Unlock()
	return nil
}
func (am *onuAlarmManager) processAlarmData(ctx context.Context, msg *omci.AlarmNotificationMsg) error {
	classID := msg.EntityClass
	sequenceNo := msg.AlarmSequenceNumber
	meInstance := msg.EntityInstance
	alarmBitmap := msg.AlarmBitmap
	var prevBitmap [alarmBitMapSize]byte
	logger.Debugw(ctx, "processing-alarm-data", log.Fields{"class-id": classID, "instance-id": meInstance,
		"alarmBitMap": alarmBitmap, "sequence-no": sequenceNo})
	if sequenceNo > 0 {
		// TODO Need Auditing if sequence no does not matches, after incrementing the last sequence no by 1
	}
	// Get the Previous alarm notification message for the class ID and entity ID from the database
	prevBitmap, err := am.getMEInstanceAlarmBitMapFromDB(ctx, classID, meInstance)
	if err != nil {
		return err
	}
	// Save the current entry before proceeding
	if err := am.putMEInstanceAlarmBitMapFromDB(ctx, classID, meInstance, alarmBitmap); err != nil {
		return err
	}
	// Get A list of Newly cleared and Newly raised alarms.
	prevRaisedAlarmList := make(map[uint8]bool)
	newRaisedAlarmList := make(map[uint8]bool)
	var alarmNo uint8
	prevAlarmNotification := *msg
	prevAlarmNotification.AlarmBitmap = prevBitmap
	for alarmNo = 0; alarmNo < 224; alarmNo++ {
		isActive, err := prevAlarmNotification.IsAlarmActive(alarmNo)
		if err != nil && isActive {
			prevRaisedAlarmList[alarmNo] = true
		}
		prevRaisedAlarmList[alarmNo] = false
	}
	for alarmNo = 0; alarmNo < 224; alarmNo++ {
		isActive, err := msg.IsAlarmActive(alarmNo)
		if err != nil && isActive {
			newRaisedAlarmList[alarmNo] = true
		}
		newRaisedAlarmList[alarmNo] = false
	}
	// Send Newly Cleared Raised/Cleared Alarms
	for alarm, active := range prevRaisedAlarmList {
		if active && newRaisedAlarmList[alarm] != active {
			// TODO clear this alarm
		}
	}
	for alarm, active := range newRaisedAlarmList {
		if active && prevRaisedAlarmList[alarm] != active {
			// TODO raise this alarm
		}
	}
	return nil
}

func (am *onuAlarmManager) raiseAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	go am.sendAlarm(ctx, classID, instanceID, alarm, true)
}

func (am *onuAlarmManager) clearAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	go am.sendAlarm(ctx, classID, instanceID, alarm, false)
}

func (am *onuAlarmManager) getIntfIDAlarm(ctx context.Context, classID me.ClassID, instanceID uint16) *uint32 {
	var intf_id *uint32
	if classID == me.CircuitPackClassID || classID == me.PhysicalPathTerminationPointEthernetUniClassID {
		if uniPort, ok := am.pDeviceHandler.uniEntityMap[uint32(instanceID)]; ok && uniPort != nil {
			intf_id = &uniPort.portNo
			return intf_id
		}
	} else if classID == me.AniGClassID || classID == me.OnuGClassID {
		intf_id = &am.pDeviceHandler.ponPortNumber
		return intf_id
	}
	return nil
}

func (am *onuAlarmManager) sendAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8, raised bool) {
	context := make(map[string]string)
	intf_id := am.getIntfIDAlarm(ctx, classID, instanceID)
	onu_id := am.pDeviceHandler.deviceID
	serial_no := am.pDeviceHandler.pOnuOmciDevice.serialNumber
	if intf_id == nil {
		logger.Warn(ctx,"intf-id-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	context["onu-intf-id"] = string(*intf_id)
	context["onu-id"] = onu_id
	context["onu-serial-number"] = serial_no

	raised_timestamp := time.Now().UnixNano()
	event_details, err := onu.GetDeviceEventData(ctx, classID, alarm)
	if err != nil {
		logger.Warn(ctx,"event-details-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	suffixDesc := "Raised"
	if !raised {
		suffixDesc = "Cleared"
	}
	deviceEvent := &voltha.DeviceEvent{
		ResourceId:      onu_id,
		DeviceEventName: fmt.Sprintf("%s_RAISE_EVENT", event_details.EventName),
		Description: fmt.Sprintf("%s Event - %s - %s", event_details.EventDescription, event_details.EventName,
			suffixDesc),
		Context: context,
	}
	_ = am.eventProxy.SendDeviceEvent(ctx, deviceEvent, event_details.EventCategory, event_details.EventSubCategory,
		raised_timestamp)
}
