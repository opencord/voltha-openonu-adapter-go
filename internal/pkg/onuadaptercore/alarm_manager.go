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
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
	"reflect"
	"sync"
	"time"
)

const (
	circuitPackClassID                             = me.CircuitPackClassID
	physicalPathTerminationPointEthernetUniClassID = me.PhysicalPathTerminationPointEthernetUniClassID
	onuGClassID                                    = me.OnuGClassID
	aniGClassID                                    = me.AniGClassID
	auditInterval                                  = 300
	defaultTimeoutDelay                            = 10
	alarmBitMapSizeBytes                           = 28
)

const (
	// events of alarm sync FSM
	asEvStart   = "asEvStart"
	asEvStop    = "asEvStop"
	asEvAudit   = "asEvAudit"
	asEvSync    = "asEvSync"
	asEvSuccess = "asEvSuccess"
	asEvFailure = "asEvFailure"
	asEvResync  = "asEvResync"
)
const (
	// states of alarm sync FSM
	asStStarting        = "asStStarting"
	asStDisabled        = "asStDisabled"
	asStInSync          = "asStInSync"
	asStAuditing        = "asStAuditing"
	asStResynchronizing = "asStResynchronizing"
	asStIdle            = "asStIdle"
)

//const cAsFsmIdleState = asStIdle not using idle state currently

type alarmInfo struct {
	classID    me.ClassID
	instanceID uint16
	alarmNo    uint8
}

type alarms map[alarmInfo]struct{}

type meAlarmKey struct {
	classID    me.ClassID
	instanceID uint16
}

type alarmBitMapDB map[meAlarmKey][alarmBitMapSizeBytes]byte
type onuAlarmManager struct {
	pDeviceHandler             *deviceHandler
	eventProxy                 eventif.EventProxy
	stopProcessingOmciMessages chan bool
	eventChannel               chan Message
	onuAlarmManagerLock        sync.RWMutex
	processMessage             bool
	activeAlarms               alarms
	alarmBitMapDB              alarmBitMapDB
	onuEventsList              map[onuDevice]onuDeviceEvent
	lastAlarmSequence          uint8
	alarmSyncFsm               *AdapterFsm
	oltDbCopy                  alarmBitMapDB
	onuDBCopy                  alarmBitMapDB
	bufferedNotifications      []*omci.AlarmNotificationMsg
	alarmUploadSeqNo           uint16
	alarmUploadNoOfCmds        uint16
	stopAlarmAuditTimer        chan struct{}
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
	logger.Debugw(ctx, "init-alarm-manager", log.Fields{"device-id": dh.deviceID})
	alarmManager.pDeviceHandler = dh
	alarmManager.eventProxy = dh.EventProxy // Or event proxy should be on cluster address ??
	alarmManager.eventChannel = make(chan Message)
	alarmManager.stopProcessingOmciMessages = make(chan bool)
	alarmManager.processMessage = false
	alarmManager.activeAlarms = make(map[alarmInfo]struct{})
	alarmManager.alarmBitMapDB = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	alarmManager.stopAlarmAuditTimer = make(chan struct{})
	alarmManager.onuEventsList = map[onuDevice]onuDeviceEvent{
		{classID: circuitPackClassID, alarmno: 0}: {EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu equipment"},
		{classID: circuitPackClassID, alarmno: 2}: {EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu self-test failure"},
		{classID: circuitPackClassID, alarmno: 3}: {EventName: "ONU_LASER_EOL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu laser EOL"},
		{classID: circuitPackClassID, alarmno: 4}: {EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature yellow"},
		{classID: circuitPackClassID, alarmno: 5}: {EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature red"},
		{classID: physicalPathTerminationPointEthernetUniClassID, alarmno: 0}: {EventName: "ONU_Ethernet_UNI", EventCategory: voltha.EventCategory_EQUIPMENT,
			EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "LAN Loss Of Signal"},
		{classID: onuGClassID, alarmno: 0}: {EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu equipment"},
		{classID: onuGClassID, alarmno: 6}: {EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu self-test failure"},
		{classID: onuGClassID, alarmno: 7}: {EventName: "ONU_DYING_GASP",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu DYING_GASP"},
		{classID: onuGClassID, alarmno: 8}: {EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature yellow"},
		{classID: onuGClassID, alarmno: 9}: {EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu temperature red"},
		{classID: onuGClassID, alarmno: 10}: {EventName: "ONU_VOLTAGE_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu voltage yellow"},
		{classID: onuGClassID, alarmno: 11}: {EventName: "ONU_VOLTAGE_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu voltage red"},
		{classID: aniGClassID, alarmno: 0}: {EventName: "ONU_LOW_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu low rx optical power"},
		{classID: aniGClassID, alarmno: 1}: {EventName: "ONU_HIGH_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu high rx optical power"},
		{classID: aniGClassID, alarmno: 4}: {EventName: "ONU_LOW_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu low tx optical power"},
		{classID: aniGClassID, alarmno: 5}: {EventName: "ONU_HIGH_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu high tx optical power"},
		{classID: aniGClassID, alarmno: 6}: {EventName: "ONU_LASER_BIAS_CURRENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu laser bias current"},
	}
	alarmManager.alarmSyncFsm.pFsm = fsm.NewFSM(
		asStDisabled,
		fsm.Events{
			{Name: asEvStart, Src: []string{asStDisabled}, Dst: asStStarting},
			{Name: asEvAudit, Src: []string{asStStarting, asStInSync}, Dst: asStAuditing},
			{Name: asEvSync, Src: []string{asStStarting}, Dst: asStInSync},
			{Name: asEvSuccess, Src: []string{asStAuditing, asStResynchronizing}, Dst: asStInSync},
			{Name: asEvFailure, Src: []string{asStAuditing, asStResynchronizing}, Dst: asStAuditing},
			{Name: asEvResync, Src: []string{asStAuditing}, Dst: asStResynchronizing},
			{Name: asEvStop, Src: []string{asStDisabled, asStStarting, asStAuditing, asStInSync, asStIdle, asStResynchronizing}, Dst: asStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                  func(e *fsm.Event) { alarmManager.alarmSyncFsm.logFsmStateChange(ctx, e) },
			"enter_" + asStStarting:        func(e *fsm.Event) { alarmManager.asFsmStarting(ctx, e) },
			"enter_" + asStAuditing:        func(e *fsm.Event) { alarmManager.asFsmAuditing(ctx, e) },
			"enter_" + asStInSync:          func(e *fsm.Event) { alarmManager.asFsmInSync(ctx, e) },
			"enter_" + asStResynchronizing: func(e *fsm.Event) { alarmManager.asFsmResynchronizing(ctx, e) },
		},
	)
	return &alarmManager
}

func (am *onuAlarmManager) asFsmStarting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm-start-processing-msgs-in-state", log.Fields{"state": e.FSM.Current(), "device-id": am.pDeviceHandler.deviceID})
	go am.processAlarmSyncMessages(ctx)
	// Start the first audit, if audit interval configured, else reach the sync state
	if auditInterval > 0 {
		select {
		//Transition into auditing state, using a very shorter timeout delay here, hence it is the first audit
		case <-time.After(defaultTimeoutDelay * time.Second):
			go func() {
				if err := am.alarmSyncFsm.pFsm.Event(asEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
				}
			}()
		case <-am.stopAlarmAuditTimer:
			logger.Infow(ctx, "stopping-alarm-timer", log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
	} else {
		// Transition into sync state directly.
		go func() {
			if err := am.alarmSyncFsm.pFsm.Event(asEvSync); err != nil {
				logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
			}
		}()
	}
}

func (am *onuAlarmManager) asFsmAuditing(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm-start-auditing", log.Fields{"state": e.FSM.Current(), "device-id": am.pDeviceHandler.deviceID})
	// Always reset the buffered notifications and db copies before starting the audit
	am.onuAlarmManagerLock.Lock()
	am.bufferedNotifications = make([]*omci.AlarmNotificationMsg, 0)
	am.oltDbCopy = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	am.onuDBCopy = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	am.onuAlarmManagerLock.Unlock()
	failureTransition := func() {
		if err := am.alarmSyncFsm.pFsm.Event(asEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
		}
	}
	if err := am.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetAllAlarm(log.WithSpanFromContext(context.TODO(), ctx), 0,
		ConstDefaultOmciTimeout, true); err != nil {
		// Transition to failure so that alarm sync can be restarted again
		go failureTransition()
	}
}

func (am *onuAlarmManager) asFsmResynchronizing(ctx context.Context, e *fsm.Event) {
	failureTransition := func() {
		if err := am.alarmSyncFsm.pFsm.Event(asEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
		}
	}
	// See if there is any onu only diff, meaning the class and entity is only in onu DB
	for alarm := range am.onuDBCopy {
		if _, exists := am.oltDbCopy[meAlarmKey{
			classID:    alarm.classID,
			instanceID: alarm.instanceID,
		}]; !exists {
			// We need to raise all such alarms as OLT wont have received notification for these alarms
			omciAlarmMessage := &omci.AlarmNotificationMsg{
				MeBasePacket: omci.MeBasePacket{
					EntityClass:    alarm.classID,
					EntityInstance: alarm.instanceID,
				},
				AlarmBitmap: am.onuDBCopy[alarm],
			}
			if err := am.processAlarmData(ctx, omciAlarmMessage); err != nil {
				logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.pDeviceHandler.deviceID})
				// Transition to failure.
				go failureTransition()
				return
			}
		}
	}
	// See if there is any olt only diff, meaning the class and entity is only in olt DB
	for alarm := range am.oltDbCopy {
		if _, exists := am.onuDBCopy[meAlarmKey{
			classID:    alarm.classID,
			instanceID: alarm.instanceID,
		}]; !exists {
			// We need to clear all such alarms as OLT might have stale data and the alarms are already cleared.
			omciAlarmMessage := &omci.AlarmNotificationMsg{
				MeBasePacket: omci.MeBasePacket{
					EntityClass:    alarm.classID,
					EntityInstance: alarm.instanceID,
				},
				AlarmBitmap: am.oltDbCopy[alarm],
			}
			if err := am.processAlarmData(ctx, omciAlarmMessage); err != nil {
				logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.pDeviceHandler.deviceID})
				// Transition to failure
				go failureTransition()
				return
			}
		}
	}
	// See if there is any attribute difference
	for alarm := range am.onuDBCopy {
		if _, exists := am.oltDbCopy[alarm]; exists {
			if am.onuDBCopy[alarm] != am.oltDbCopy[alarm] {
				omciAlarmMessage := &omci.AlarmNotificationMsg{
					MeBasePacket: omci.MeBasePacket{
						EntityClass:    alarm.classID,
						EntityInstance: alarm.instanceID,
					},
					AlarmBitmap: am.onuDBCopy[alarm],
				}
				// We will assume that onudb is correct always in this case and process the changed bitmap.
				if err := am.processAlarmData(ctx, omciAlarmMessage); err != nil {
					logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.pDeviceHandler.deviceID})
					// Transition to failure
					go failureTransition()
					return
				}
			}
		}
	}
	// Send the buffered notifications if no failure.
	for _, notif := range am.bufferedNotifications {
		if err := am.processAlarmData(ctx, notif); err != nil {
			logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.pDeviceHandler.deviceID})
			go failureTransition()
		}
	}
	go func() {
		if err := am.alarmSyncFsm.pFsm.Event(asEvSuccess); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
		}
	}()
}

func (am *onuAlarmManager) asFsmInSync(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm", log.Fields{"state": e.FSM.Current(), "device-id": am.pDeviceHandler.deviceID})
	if auditInterval > 0 {
		select {
		case <-time.After(auditInterval * time.Second):
			go func() {
				if err := am.alarmSyncFsm.pFsm.Event(asEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
				}
			}()
		case <-am.stopAlarmAuditTimer:
			logger.Infow(ctx, "stopping-alarm-timer", log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
	}
}

func (am *onuAlarmManager) processAlarmSyncMessages(ctx context.Context) {
	logger.Debugw(ctx, "start-routine-to-process-omci-messages-for-alarm-sync", log.Fields{"device-id": am.pDeviceHandler.deviceID})
	am.flushAlarmSyncChannels(ctx)
loop:
	for {
		message, ok := <-am.eventChannel
		if !ok {
			logger.Info(ctx, "alarm-sync-omci-message-could-not-be-read-from-channel", log.Fields{"device-id": am.pDeviceHandler.deviceID})
			break loop
		}
		logger.Debugw(ctx, "alarm-sync-omci-message-received", log.Fields{"device-id": am.pDeviceHandler.deviceID})

		switch message.Type {
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			am.handleOmciMessage(ctx, msg)
		default:
			logger.Warn(ctx, "alarm-sync-unknown-message-type-received", log.Fields{"device-id": am.pDeviceHandler.deviceID, "message.Type": message.Type})
		}
	}
	logger.Info(ctx, "alarm-sync-stopped-handling-of-alarm-sync-omci-message", log.Fields{"device-id": am.pDeviceHandler.deviceID})
	_ = am.alarmSyncFsm.pFsm.Event(asEvStop)
}

func (am *onuAlarmManager) handleOmciMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "alarm-sync-omci-message-received", log.Fields{"device-id": am.pDeviceHandler.deviceID,
		"msg-type": msg.OmciMsg.MessageType, "msg": msg})
	switch msg.OmciMsg.MessageType {
	case omci.GetAllAlarmsResponseType:
		am.handleOmciGetAllAlarmsResponseMessage(ctx, msg)
	case omci.GetAllAlarmsNextResponseType:
		am.handleOmciGetAllAlarmNextResponseMessage(ctx, msg)
	default:
		logger.Warnw(ctx, "unknown-message-type", log.Fields{"msg-type": msg.OmciMsg.MessageType})

	}
}

func (am *onuAlarmManager) handleOmciGetAllAlarmsResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetAllAlarmsResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected", log.Fields{"device-id": am.pDeviceHandler.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.GetAllAlarmsResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned", log.Fields{"device-id": am.pDeviceHandler.deviceID})
		return
	}
	logger.Debugw(ctx, "get-all-alarm-response-data", log.Fields{"device-id": am.pDeviceHandler.deviceID, "data-fields": msgObj})
	if am.alarmSyncFsm.pFsm.Is(asStDisabled) {
		logger.Debugw(ctx, "alarm-sync-fsm-is-disabled-ignoring-response-message", log.Fields{"device-id": am.pDeviceHandler.deviceID, "data-fields": msgObj})
		return
	}
	am.alarmUploadNoOfCmds = msgObj.NumberOfCommands
	failureTransition := func() {
		if err := am.alarmSyncFsm.pFsm.Event(asEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
		}
	}
	if am.alarmUploadSeqNo < am.alarmUploadNoOfCmds {
		// Reset Onu Alarm Sequence
		am.onuAlarmManagerLock.Lock()
		am.resetAlarmSequence()
		// Get a copy of the alarm bit map db.
		for alarms, bitmap := range am.alarmBitMapDB {
			am.oltDbCopy[alarms] = bitmap
		}
		am.onuAlarmManagerLock.Unlock()
		if err := am.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetAllAlarmNext(
			log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true); err != nil {
			// Transition to failure
			go failureTransition()
		}
	} else {
		logger.Errorw(ctx, "invalid-number-of-commands-received", log.Fields{"device-id": am.pDeviceHandler.deviceID,
			"upload-no-of-cmds": am.alarmUploadNoOfCmds, "upload-seq-no": am.alarmUploadSeqNo})
		go failureTransition()
	}
}

func (am *onuAlarmManager) handleOmciGetAllAlarmNextResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetAllAlarmsNextResponse)

	if msgLayer == nil {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected", log.Fields{"device-id": am.pDeviceHandler.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.GetAllAlarmsNextResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned", log.Fields{"device-id": am.pDeviceHandler.deviceID})
		return
	}
	logger.Debugw(ctx, "get-all-alarms-next-response-data",
		log.Fields{"device-id": am.pDeviceHandler.deviceID, "data-fields": msgObj})
	meClassID := msgObj.AlarmEntityClass
	meEntityID := msgObj.AlarmEntityInstance
	meAlarmBitMap := msgObj.AlarmBitMap

	am.onuAlarmManagerLock.Lock()
	am.onuDBCopy[meAlarmKey{
		classID:    meClassID,
		instanceID: meEntityID,
	}] = meAlarmBitMap
	am.onuAlarmManagerLock.Unlock()
	failureTransition := func() {
		if err := am.alarmSyncFsm.pFsm.Event(asEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
		}
	}
	if am.alarmUploadSeqNo < am.alarmUploadNoOfCmds {
		if err := am.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendGetAllAlarmNext(
			log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true); err != nil {
			// Transition to failure
			go failureTransition()
		} //TODO: needs to handle timeouts
	} else {
		if am.isAlarmDBDiffPresent(ctx) {
			// transition to resync state
			go func() {
				if err := am.alarmSyncFsm.pFsm.Event(asEvResync); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-resynchronizing", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
				}
			}()
		} else {
			// Transition to sync state
			go failureTransition()
		}
	}
}

func (am *onuAlarmManager) startOMCIAlarmMessageProcessing(ctx context.Context) {
	am.onuAlarmManagerLock.Lock()
	am.processMessage = true
	if am.activeAlarms == nil {
		am.activeAlarms = make(map[alarmInfo]struct{})
	}
	am.alarmBitMapDB = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	am.onuAlarmManagerLock.Unlock()
	if am.alarmSyncFsm.pFsm.Is(asStDisabled) {
		if err := am.alarmSyncFsm.pFsm.Event(ulEvStart); err != nil {
			logger.Errorw(ctx, "alarm-sync-fsm-can-not-go-to-state-starting", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
			return
		}
	} else {
		logger.Errorw(ctx, "wrong-state-of-alarm-sync-fsm-want-disabled", log.Fields{"state": string(am.alarmSyncFsm.pFsm.Current()),
			"device-id": am.pDeviceHandler.deviceID})
		return
	}
	logger.Debugw(ctx, "alarm-sync-fsm-started", log.Fields{"state": string(am.alarmSyncFsm.pFsm.Current())})

	if stop := <-am.stopProcessingOmciMessages; stop {
		am.onuAlarmManagerLock.Lock()
		am.processMessage = false
		am.activeAlarms = nil
		am.alarmBitMapDB = nil
		am.alarmUploadNoOfCmds = 0
		am.alarmUploadSeqNo = 0
		am.onuAlarmManagerLock.Unlock()
		_ = am.alarmSyncFsm.pFsm.Event(asEvStop)
	}
}

func (am *onuAlarmManager) handleOmciAlarmNotificationMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "omci-alarm-notification-msg", log.Fields{"device-id": am.pDeviceHandler.deviceID,
		"msg-type": msg.OmciMsg.MessageType})
	am.onuAlarmManagerLock.Lock()
	defer am.onuAlarmManagerLock.Unlock()

	if am.processMessage {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeAlarmNotification)
		if msgLayer == nil {
			logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected-for-alarm-notification",
				log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
		msgObj, msgOk := msgLayer.(*omci.AlarmNotificationMsg)
		if !msgOk {
			logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned-for-alarm-notification",
				log.Fields{"device-id": am.pDeviceHandler.deviceID})
			return
		}
		//Alarm Notification decoding at omci lib validates that the me class ID supports the
		// alarm notifications.
		logger.Debugw(ctx, "alarm-notification-data", log.Fields{"device-id": am.pDeviceHandler.deviceID, "data-fields": msgObj})
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
	if _, present := am.pDeviceHandler.pOnuOmciDevice.pOnuDB.meDb[classID][meInstance]; !present {
		logger.Errorw(ctx, "me-class-instance-not-present", log.Fields{"class-id": classID, "instance-id": meInstance})
		return fmt.Errorf("me-class-%d-instance-%d-not-present", classID, meInstance)
	}

	if sequenceNo > 0 {
		if am.alarmSyncFsm.pFsm.Is(asStAuditing) || am.alarmSyncFsm.pFsm.Is(asStResynchronizing) {
			am.bufferedNotifications = append(am.bufferedNotifications, msg)
			return nil
		}
		am.incrementAlarmSequence()
		if sequenceNo != am.lastAlarmSequence && auditInterval > 0 {
			// signal early audit, if no match(if we are reaching here it means that audit is not going on currently)
			go func() {
				if err := am.alarmSyncFsm.pFsm.Event(asEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.pDeviceHandler.deviceID, "err": err})
				}
			}()
		}
	}
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
	am.alarmBitMapDB[meAlarmKey{
		classID:    classID,
		instanceID: meInstance,
	}] = alarmBitmap
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
	serialNo := am.pDeviceHandler.pOnuOmciDevice.sOnuPersistentData.PersSerialNumber
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

func (am *onuAlarmManager) isAlarmDBDiffPresent(ctx context.Context) bool {
	return !reflect.DeepEqual(am.onuDBCopy, am.oltDbCopy)
}

func (am *onuAlarmManager) incrementAlarmSequence() {
	//alarm sequence number wraps from 255 to 1.
	if am.lastAlarmSequence == 255 {
		am.lastAlarmSequence = 1
	} else {
		am.lastAlarmSequence++
	}
}

func (am *onuAlarmManager) resetAlarmSequence() {
	am.lastAlarmSequence = 0
}

// flushAlarmSyncChannels flushes all alarm sync channels to discard any previous response
func (am *onuAlarmManager) flushAlarmSyncChannels(ctx context.Context) {
	// flush alarm sync channel
	select {
	case <-am.eventChannel:
		logger.Debug(ctx, "flushed-alarm-sync-channel")
	default:
	}
	select {
	case <-am.stopAlarmAuditTimer:
		logger.Debug(ctx, "flushed-alarm-audit-timer-channel")
	default:
	}
}

// getDeviceEventData returns the event data for a device
func (am *onuAlarmManager) getDeviceEventData(ctx context.Context, classID me.ClassID, alarmNo uint8) (onuDeviceEvent, error) {
	if onuEventDetails, ok := am.onuEventsList[onuDevice{classID: classID, alarmno: alarmNo}]; ok {
		return onuEventDetails, nil
	}
	return onuDeviceEvent{}, errors.New("onu Event Detail not found")
}
