/*
 * Copyright 2021-2024 Open Networking Foundation (ONF) and the ONF Contributors
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

// Package almgr provides the utilities for managing alarm notifications
package almgr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-protos/v5/go/extension"
	"github.com/opencord/voltha-protos/v5/go/voltha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	circuitPackClassID                             = me.CircuitPackClassID
	physicalPathTerminationPointEthernetUniClassID = me.PhysicalPathTerminationPointEthernetUniClassID
	onuGClassID                                    = me.OnuGClassID
	aniGClassID                                    = me.AniGClassID
	defaultTimeoutDelay                            = 10
	alarmBitMapSizeBytes                           = 28
)

// events of alarm sync FSM
const (
	AsEvStart   = "AsEvStart"
	AsEvStop    = "AsEvStop"
	AsEvAudit   = "AsEvAudit"
	AsEvSync    = "AsEvSync"
	AsEvSuccess = "AsEvSuccess"
	AsEvFailure = "AsEvFailure"
	AsEvResync  = "AsEvResync"
)

// states of alarm sync FSM
const (
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

type onuDevice struct {
	classID me.ClassID
	alarmno uint8
}
type onuDeviceEvent struct {
	EventName        string
	EventDescription string
	EventCategory    eventif.EventCategory
	EventSubCategory eventif.EventSubCategory
}

// OnuAlarmManager holds alarm manager related data
type OnuAlarmManager struct {
	pDeviceHandler             cmn.IdeviceHandler
	pOnuDeviceEntry            cmn.IonuDeviceEntry
	eventProxy                 eventif.EventProxy
	StopProcessingOmciMessages chan bool
	eventChannel               chan cmn.Message
	activeAlarms               alarms
	alarmBitMapDB              alarmBitMapDB
	onuEventsList              map[onuDevice]onuDeviceEvent
	AlarmSyncFsm               *cmn.AdapterFsm
	oltDbCopy                  alarmBitMapDB
	onuDBCopy                  alarmBitMapDB
	StopAlarmAuditTimer        chan struct{}
	AsyncAlarmsCommChan        chan struct{}
	deviceID                   string
	bufferedNotifications      []*omci.AlarmNotificationMsg
	onuAlarmManagerLock        sync.RWMutex
	onuAlarmRequestLock        sync.RWMutex
	alarmUploadSeqNo           uint16
	alarmUploadNoOfCmdsOrMEs   uint16
	processMessage             bool
	lastAlarmSequence          uint8
	isExtendedOmci             bool
	isAsyncAlarmRequest        bool
}

// NewAlarmManager - TODO: add comment
func NewAlarmManager(ctx context.Context, dh cmn.IdeviceHandler, onuDev cmn.IonuDeviceEntry) *OnuAlarmManager {
	var alarmManager OnuAlarmManager
	alarmManager.deviceID = dh.GetDeviceID()
	logger.Debugw(ctx, "init-alarm-manager", log.Fields{"device-id": alarmManager.deviceID})
	alarmManager.pDeviceHandler = dh
	alarmManager.pOnuDeviceEntry = onuDev
	alarmManager.eventProxy = dh.GetEventProxy() // Or event proxy should be on cluster address ??
	alarmManager.eventChannel = make(chan cmn.Message)
	alarmManager.StopProcessingOmciMessages = make(chan bool)
	alarmManager.processMessage = false
	alarmManager.activeAlarms = make(map[alarmInfo]struct{})
	alarmManager.alarmBitMapDB = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	alarmManager.StopAlarmAuditTimer = make(chan struct{})
	alarmManager.AsyncAlarmsCommChan = make(chan struct{})
	alarmManager.isAsyncAlarmRequest = false
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
		{classID: aniGClassID, alarmno: 2}: {EventName: "ONU_BIT_ERROR_BASED_SIGNAL_FAIL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu bit error based signal fail"},
		{classID: aniGClassID, alarmno: 3}: {EventName: "ONU_BIT_ERROR_BASED_SIGNAL_DEGRADE",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu bit error based signal degrade"},
		{classID: aniGClassID, alarmno: 4}: {EventName: "ONU_LOW_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu low tx optical power"},
		{classID: aniGClassID, alarmno: 5}: {EventName: "ONU_HIGH_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu high tx optical power"},
		{classID: aniGClassID, alarmno: 6}: {EventName: "ONU_LASER_BIAS_CURRENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "onu laser bias current"},
	}
	alarmManager.AlarmSyncFsm = cmn.NewAdapterFsm("AlarmSync", alarmManager.deviceID, alarmManager.eventChannel)
	alarmManager.AlarmSyncFsm.PFsm = fsm.NewFSM(
		asStDisabled,
		fsm.Events{
			{Name: AsEvStart, Src: []string{asStDisabled}, Dst: asStStarting},
			{Name: AsEvAudit, Src: []string{asStStarting, asStInSync}, Dst: asStAuditing},
			{Name: AsEvSync, Src: []string{asStStarting}, Dst: asStInSync},
			{Name: AsEvSuccess, Src: []string{asStAuditing, asStResynchronizing}, Dst: asStInSync},
			{Name: AsEvFailure, Src: []string{asStAuditing, asStResynchronizing}, Dst: asStAuditing},
			{Name: AsEvResync, Src: []string{asStAuditing}, Dst: asStResynchronizing},
			{Name: AsEvStop, Src: []string{asStDisabled, asStStarting, asStAuditing, asStInSync, asStIdle, asStResynchronizing}, Dst: asStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                  func(e *fsm.Event) { alarmManager.AlarmSyncFsm.LogFsmStateChange(ctx, e) },
			"enter_" + asStStarting:        func(e *fsm.Event) { alarmManager.asFsmStarting(ctx, e) },
			"enter_" + asStAuditing:        func(e *fsm.Event) { alarmManager.asFsmAuditing(ctx, e) },
			"enter_" + asStInSync:          func(e *fsm.Event) { alarmManager.asFsmInSync(ctx, e) },
			"enter_" + asStResynchronizing: func(e *fsm.Event) { alarmManager.asFsmResynchronizing(ctx, e) },
			"leave_" + asStAuditing:        func(e *fsm.Event) { alarmManager.asFsmLeaveAuditing(ctx, e) },
		},
	)
	return &alarmManager
}

func (am *OnuAlarmManager) asFsmStarting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm-start-processing-msgs-in-state", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})
	go am.processAlarmSyncMessages(ctx)
	// Start the first audit, if audit interval configured, else reach the sync state
	if am.pDeviceHandler.GetAlarmAuditInterval() > 0 {
		select {
		//Transition into auditing state, using a very shorter timeout delay here, hence it is the first audit
		case <-time.After(defaultTimeoutDelay * time.Second):
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		case <-am.StopAlarmAuditTimer:
			logger.Infow(ctx, "stopping-alarm-timer", log.Fields{"device-id": am.deviceID})
			return
		}
	} else {
		// Transition into sync state directly.
		go func() {
			if err := am.AlarmSyncFsm.PFsm.Event(AsEvSync); err != nil {
				logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.deviceID, "err": err})
			}
		}()
	}
}

func (am *OnuAlarmManager) asFsmAuditing(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm-start-auditing", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})
	// Always reset the buffered notifications and db copies before starting the audit
	am.onuAlarmManagerLock.Lock()
	am.bufferedNotifications = make([]*omci.AlarmNotificationMsg, 0)
	am.oltDbCopy = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	am.onuDBCopy = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	am.onuAlarmManagerLock.Unlock()
	failureTransition := func() {
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.deviceID, "err": err})
		}
	}
	if err := am.pOnuDeviceEntry.GetDevOmciCC().SendGetAllAlarm(log.WithSpanFromContext(context.TODO(), ctx), 0,
		am.pDeviceHandler.GetOmciTimeout(), true, am.isExtendedOmci); err != nil {
		// Transition to failure so that alarm sync can be restarted again
		go failureTransition()
	}
}
func (am *OnuAlarmManager) asFsmLeaveAuditing(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm-leave-auditing state", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})
}

func (am *OnuAlarmManager) asFsmResynchronizing(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})
	failureTransition := func() {
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.deviceID, "err": err})
		}
	}

	// Process onu only differences
	if err := am.processOnuOnlyDifferences(ctx, failureTransition); err != nil {
		return
	}

	// Process olt only differences
	if err := am.processOltOnlyDifferences(ctx, failureTransition); err != nil {
		return
	}

	// Process attribute differences
	if err := am.processAttributeDifferences(ctx, failureTransition); err != nil {
		return
	}

	// Process buffered notifications
	if err := am.processBufferedNotifications(ctx, failureTransition); err != nil {
		return
	}

	go func() {
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvSuccess); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.deviceID, "err": err})
		}
	}()
}

func (am *OnuAlarmManager) asFsmInSync(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "alarm-sync-fsm", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})

	if am.isAsyncAlarmRequest {
		logger.Debugw(ctx, "alarm-sync-fsm-before entering the sync state process the updated ONU alarms ", log.Fields{"state": e.FSM.Current(), "device-id": am.deviceID})
		am.AsyncAlarmsCommChan <- struct{}{}
		am.isAsyncAlarmRequest = false

	}

	if am.pDeviceHandler.GetAlarmAuditInterval() > 0 {
		select {
		case <-time.After(am.pDeviceHandler.GetAlarmAuditInterval()):
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		case <-am.StopAlarmAuditTimer:
			logger.Infow(ctx, "stopping-alarm-timer", log.Fields{"device-id": am.deviceID})
			return

		case <-am.AsyncAlarmsCommChan:
			go func() {
				logger.Debugw(ctx, "On demand Auditing the ONU for Alarms  ", log.Fields{"device-id": am.deviceID})
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvAudit); err != nil {
					logger.Errorw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing, use current snapshot of alarms", log.Fields{"device-id": am.deviceID, "err": err})
					am.isAsyncAlarmRequest = false
					am.AsyncAlarmsCommChan <- struct{}{}
				}
			}()

		}
	} else {
		<-am.AsyncAlarmsCommChan
		go func() {
			logger.Debugw(ctx, "On demand Auditing the ONU for Alarms  ", log.Fields{"device-id": am.deviceID})
			if err := am.AlarmSyncFsm.PFsm.Event(AsEvAudit); err != nil {
				logger.Errorw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing, use current snapshot of alarms", log.Fields{"device-id": am.deviceID, "err": err})
				am.isAsyncAlarmRequest = false
				am.AsyncAlarmsCommChan <- struct{}{}
			}
		}()
	}
}

func (am *OnuAlarmManager) processAlarmSyncMessages(ctx context.Context) {
	logger.Debugw(ctx, "start-routine-to-process-omci-messages-for-alarm-sync", log.Fields{"device-id": am.deviceID})
	for {
		select {
		case message, ok := <-am.eventChannel:
			if !ok {
				logger.Info(ctx, "alarm-sync-omci-message-could-not-be-read-from-channel", log.Fields{"device-id": am.deviceID})
				continue
			}
			logger.Debugw(ctx, "alarm-sync-omci-message-received", log.Fields{"device-id": am.deviceID})

			switch message.Type {
			case cmn.OMCI:
				msg, _ := message.Data.(cmn.OmciMessage)
				am.handleOmciMessage(ctx, msg)
			default:
				logger.Warn(ctx, "alarm-sync-unknown-message-type-received", log.Fields{"device-id": am.deviceID, "message.Type": message.Type})
			}
		case <-am.StopProcessingOmciMessages:
			logger.Infow(ctx, "alarm-manager-stop-omci-alarm-message-processing-routines", log.Fields{"device-id": am.deviceID})
			am.onuAlarmManagerLock.Lock()
			am.processMessage = false
			am.activeAlarms = nil
			am.alarmBitMapDB = nil
			am.alarmUploadNoOfCmdsOrMEs = 0
			am.alarmUploadSeqNo = 0
			am.onuAlarmManagerLock.Unlock()
			return

		}
	}
}

func (am *OnuAlarmManager) handleOmciMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "alarm-sync-omci-message-received", log.Fields{"device-id": am.deviceID,
		"msg-type": msg.OmciMsg.MessageType, "msg": msg})
	switch msg.OmciMsg.MessageType {
	case omci.GetAllAlarmsResponseType:
		am.handleOmciGetAllAlarmsResponseMessage(ctx, msg)
	case omci.GetAllAlarmsNextResponseType:
		am.handleOmciGetAllAlarmNextResponseMessage(ctx, msg)
	default:
		logger.Warnw(ctx, "unknown-message-type", log.Fields{"device-id": am.deviceID, "msg-type": msg.OmciMsg.MessageType})

	}
}

func (am *OnuAlarmManager) handleOmciGetAllAlarmsResponseMessage(ctx context.Context, msg cmn.OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetAllAlarmsResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected", log.Fields{"device-id": am.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.GetAllAlarmsResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned", log.Fields{"device-id": am.deviceID})
		return
	}
	logger.Debugw(ctx, "get-all-alarm-response-data", log.Fields{"device-id": am.deviceID, "data-fields": msgObj})
	if am.AlarmSyncFsm.PFsm.Is(asStDisabled) {
		logger.Debugw(ctx, "alarm-sync-fsm-is-disabled-ignoring-response-message", log.Fields{"device-id": am.deviceID, "data-fields": msgObj})
		return
	}
	am.onuAlarmManagerLock.Lock()
	am.alarmUploadNoOfCmdsOrMEs = msgObj.NumberOfCommands
	am.onuAlarmManagerLock.Unlock()
	failureTransition := func() {
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.deviceID, "err": err})
		}
	}
	am.onuAlarmManagerLock.Lock()
	if am.alarmUploadSeqNo < am.alarmUploadNoOfCmdsOrMEs {
		// Reset Onu Alarm Sequence
		am.resetAlarmSequence()
		// Get a copy of the alarm bit map db.
		for alarms, bitmap := range am.alarmBitMapDB {
			am.oltDbCopy[alarms] = bitmap
		}
		am.onuAlarmManagerLock.Unlock()
		if err := am.pOnuDeviceEntry.GetDevOmciCC().SendGetAllAlarmNext(
			log.WithSpanFromContext(context.TODO(), ctx), am.pDeviceHandler.GetOmciTimeout(), true, am.isExtendedOmci); err != nil {
			// Transition to failure
			go failureTransition()
		}
	} else if am.alarmUploadNoOfCmdsOrMEs == 0 {
		// Reset Onu Alarm Sequence
		am.resetAlarmSequence()
		// Get a copy of the alarm bit map db.
		for alarms, bitmap := range am.alarmBitMapDB {
			am.oltDbCopy[alarms] = bitmap
		}
		am.onuAlarmManagerLock.Unlock()
		if am.isAlarmDBDiffPresent(ctx) {
			// transition to resync state
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvResync); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-resynchronizing", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		} else {
			// Transition to sync state
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvSuccess); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()

		}
	} else {
		logger.Errorw(ctx, "invalid-number-of-commands-received", log.Fields{"device-id": am.deviceID,
			"upload-no-of-cmds": am.alarmUploadNoOfCmdsOrMEs, "upload-seq-no": am.alarmUploadSeqNo})
		am.onuAlarmManagerLock.Unlock()
		go failureTransition()
	}
}

func (am *OnuAlarmManager) handleOmciGetAllAlarmNextResponseMessage(ctx context.Context, msg cmn.OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetAllAlarmsNextResponse)

	if msgLayer == nil {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected", log.Fields{"device-id": am.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.GetAllAlarmsNextResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned", log.Fields{"device-id": am.deviceID})
		return
	}
	logger.Debugw(ctx, "get-all-alarms-next-response-data",
		log.Fields{"device-id": am.deviceID, "data-fields": msgObj})
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
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvFailure); err != nil {
			logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-failure", log.Fields{"device-id": am.deviceID, "err": err})
		}
	}
	if msg.OmciMsg.DeviceIdentifier == omci.ExtendedIdent {
		logger.Debugw(ctx, "get-all-alarms-next-response-additional-data",
			log.Fields{"device-id": am.deviceID, "additional-data": msgObj.AdditionalAlarms})

		for _, additionalAlarmReport := range msgObj.AdditionalAlarms {
			meClassID := additionalAlarmReport.AlarmEntityClass
			meEntityID := additionalAlarmReport.AlarmEntityInstance
			meAlarmBitMap := additionalAlarmReport.AlarmBitMap

			am.onuAlarmManagerLock.Lock()
			am.onuDBCopy[meAlarmKey{
				classID:    meClassID,
				instanceID: meEntityID,
			}] = meAlarmBitMap
			am.onuAlarmManagerLock.Unlock()

			am.IncrementAlarmUploadSeqNo()
		}
	}
	am.onuAlarmManagerLock.RLock()
	if am.alarmUploadSeqNo < am.alarmUploadNoOfCmdsOrMEs {
		am.onuAlarmManagerLock.RUnlock()
		if err := am.pOnuDeviceEntry.GetDevOmciCC().SendGetAllAlarmNext(
			log.WithSpanFromContext(context.TODO(), ctx), am.pDeviceHandler.GetOmciTimeout(), true, am.isExtendedOmci); err != nil {
			// Transition to failure
			go failureTransition()
		} //TODO: needs to handle timeouts
	} else {
		am.onuAlarmManagerLock.RUnlock()
		if am.isAlarmDBDiffPresent(ctx) {
			// transition to resync state
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvResync); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-resynchronizing", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		} else {
			// Transition to sync state
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvSuccess); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-sync", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		}

	}
}

// StartOMCIAlarmMessageProcessing - TODO: add comment: add comment
func (am *OnuAlarmManager) StartOMCIAlarmMessageProcessing(ctx context.Context) {
	logger.Infow(ctx, "alarm-manager-start-omci-alarm-message-processing-routines", log.Fields{"device-id": am.deviceID})
	am.onuAlarmManagerLock.Lock()
	am.processMessage = true
	if am.activeAlarms == nil {
		am.activeAlarms = make(map[alarmInfo]struct{})
	}
	am.alarmBitMapDB = make(map[meAlarmKey][alarmBitMapSizeBytes]byte)
	// when instantiating alarm manager it was too early, but now we can check for ONU's extended OMCI support
	am.isExtendedOmci = am.pOnuDeviceEntry.GetPersIsExtOmciSupported()
	am.onuAlarmManagerLock.Unlock()
	am.flushAlarmSyncChannels(ctx) // Need to do this first as there might be stale data on the channels and the start state waits on same channels

	if am.AlarmSyncFsm.PFsm.Is(asStDisabled) {
		if err := am.AlarmSyncFsm.PFsm.Event(AsEvStart); err != nil {
			logger.Errorw(ctx, "alarm-sync-fsm-can-not-go-to-state-starting", log.Fields{"device-id": am.deviceID, "err": err})
			return
		}
	} else {
		logger.Errorw(ctx, "wrong-state-of-alarm-sync-fsm-want-disabled", log.Fields{"state": string(am.AlarmSyncFsm.PFsm.Current()),
			"device-id": am.deviceID})
		return
	}
	logger.Debugw(ctx, "alarm-sync-fsm-started", log.Fields{"state": string(am.AlarmSyncFsm.PFsm.Current())})
}

// HandleOmciAlarmNotificationMessage - TODO: add comment
func (am *OnuAlarmManager) HandleOmciAlarmNotificationMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "omci-alarm-notification-msg", log.Fields{"device-id": am.deviceID,
		"msg-type": msg.OmciMsg.MessageType})

	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeAlarmNotification)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-detected-for-alarm-notification",
			log.Fields{"device-id": am.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.AlarmNotificationMsg)
	if !msgOk {
		logger.Errorw(ctx, "omci-msg-layer-could-not-be-assigned-for-alarm-notification",
			log.Fields{"device-id": am.deviceID})
		return
	}
	//Alarm Notification decoding at omci lib validates that the me class ID supports the
	// alarm notifications.
	logger.Debugw(ctx, "alarm-notification-data-received", log.Fields{"device-id": am.deviceID, "data-fields": msgObj})
	if err := am.processAlarmData(ctx, msgObj); err != nil {
		logger.Errorw(ctx, "unable-to-process-alarm-notification", log.Fields{"device-id": am.deviceID})
	}

}

func (am *OnuAlarmManager) processAlarmData(ctx context.Context, msg *omci.AlarmNotificationMsg) error {
	classID := msg.EntityClass
	sequenceNo := msg.AlarmSequenceNumber
	meInstance := msg.EntityInstance
	alarmBitmap := msg.AlarmBitmap
	logger.Debugw(ctx, "processing-alarm-data", log.Fields{"class-id": classID, "instance-id": meInstance,
		"alarmBitMap": alarmBitmap, "sequence-no": sequenceNo})
	am.onuAlarmManagerLock.Lock()
	defer am.onuAlarmManagerLock.Unlock()
	if !am.processMessage {
		logger.Warnw(ctx, "ignoring-alarm-notification-received-for-me-as-channel-for-processing-is-closed",
			log.Fields{"device-id": am.deviceID})
		return status.Error(codes.Unavailable, "alarm-manager-is-in-stopped-state")
	}
	if meAttributes := am.pOnuDeviceEntry.GetOnuDB().GetMe(classID, meInstance); meAttributes == nil {
		logger.Errorw(ctx, "me-class-instance-not-present",
			log.Fields{"class-id": classID, "instance-id": meInstance, "device-id": am.deviceID})
		return status.Error(codes.NotFound, "me-class-instance-not-present")
	}
	if sequenceNo > 0 {
		if am.AlarmSyncFsm.PFsm.Is(asStAuditing) || am.AlarmSyncFsm.PFsm.Is(asStResynchronizing) {
			am.bufferedNotifications = append(am.bufferedNotifications, msg)
			logger.Debugw(ctx, "adding-notification-to-buffered-notification-list", log.Fields{"device-id": am.deviceID,
				"notification": msg})
			return nil
		}
		am.incrementAlarmSequence()
		if sequenceNo != am.lastAlarmSequence && am.pDeviceHandler.GetAlarmAuditInterval() > 0 {
			// signal early audit, if no match(if we are reaching here it means that audit is not going on currently)
			go func() {
				if err := am.AlarmSyncFsm.PFsm.Event(AsEvAudit); err != nil {
					logger.Debugw(ctx, "alarm-sync-fsm-cannot-go-to-state-auditing", log.Fields{"device-id": am.deviceID, "err": err})
				}
			}()
		}
	}
	entity, omciErr := me.LoadManagedEntityDefinition(classID,
		me.ParamData{EntityID: meInstance})
	if omciErr.StatusCode() != me.Success {
		//log error and return
		logger.Error(ctx, "unable-to-get-managed-entity", log.Fields{"class-id": classID, "instance-id": meInstance})
		return status.Error(codes.NotFound, "unable-to-get-managed-entity")
	}
	meAlarmMap := entity.GetAlarmMap()
	if meAlarmMap == nil {
		logger.Error(ctx, "unable-to-get-managed-entity-alarm-map", log.Fields{"class-id": classID, "instance-id": meInstance})
		return status.Error(codes.NotFound, "unable-to-get-managed-entity-alarm-map")
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
				logger.Warnw(ctx, "unable-to-find-out-alarm-is-cleared", log.Fields{"device-id": am.deviceID,
					"class-id": classID, "instance-id": meInstance, "alarm-no": alarmNo})
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
				logger.Warnw(ctx, "unable-to-find-out-alarm-is-raised", log.Fields{"device-id": am.deviceID,
					"class-id": classID, "instance-id": meInstance, "alarm-no": alarmNo})
				return err
			}
			if raised {
				am.raiseAlarm(ctx, classID, meInstance, alarmNo)
			}
		}
	}
	return nil
}

func (am *OnuAlarmManager) raiseAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	am.activeAlarms[alarmInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	}] = struct{}{}

	go am.sendAlarm(ctx, classID, instanceID, alarm, true)
}

func (am *OnuAlarmManager) clearAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8) {
	go am.sendAlarm(ctx, classID, instanceID, alarm, false)
	delete(am.activeAlarms, alarmInfo{
		classID:    classID,
		instanceID: instanceID,
		alarmNo:    alarm,
	})
	key := meAlarmKey{
		classID:    classID,
		instanceID: instanceID,
	}
	if am.alarmBitMapDB[key] == [alarmBitMapSizeBytes]byte{0} {
		delete(am.alarmBitMapDB, key)
	}
}

func (am *OnuAlarmManager) getIntfIDAlarm(ctx context.Context, classID me.ClassID, instanceID uint16) *uint32 {
	var intfID *uint32
	switch classID {
	case circuitPackClassID, physicalPathTerminationPointEthernetUniClassID:
		for _, uniPort := range *am.pDeviceHandler.GetUniEntityMap() {
			if uniPort.EntityID == instanceID {
				intfID = &uniPort.PortNo
				return intfID
			}
		}
	case aniGClassID, onuGClassID:
		intfID = am.pDeviceHandler.GetPonPortNumber()
		return intfID
	default:
		logger.Warnw(ctx, "me-not-supported", log.Fields{"device-id": am.deviceID, "class-id": classID, "instance-id": instanceID})
	}
	return nil
}

func (am *OnuAlarmManager) sendAlarm(ctx context.Context, classID me.ClassID, instanceID uint16, alarm uint8, raised bool) {
	context := make(map[string]string)
	intfID := am.getIntfIDAlarm(ctx, classID, instanceID)
	onuID := am.deviceID
	serialNo := am.pOnuDeviceEntry.GetPersSerialNumber()
	if intfID == nil {
		logger.Warn(ctx, "intf-id-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	context["onu-intf-id"] = fmt.Sprintf("%d", *intfID)
	context["onu-id"] = onuID
	context["onu-serial-number"] = serialNo

	raisedTimestamp := time.Now().Unix()
	eventDetails, err := am.getDeviceEventData(ctx, classID, alarm)
	if err != nil {
		logger.Warn(ctx, "event-details-for-alarm-not-found", log.Fields{"alarm-no": alarm, "class-id": classID})
		return
	}
	suffixDesc := "Raised"
	clearOrRaiseEvent := "RAISE_EVENT"
	if !raised {
		suffixDesc = "Cleared"
		clearOrRaiseEvent = "CLEAR_EVENT"
	}
	deviceEvent := &voltha.DeviceEvent{
		ResourceId:      onuID,
		DeviceEventName: fmt.Sprintf("%s_%s", eventDetails.EventName, clearOrRaiseEvent),
		Description: fmt.Sprintf("%s Event - %s - %s", eventDetails.EventDescription, eventDetails.EventName,
			suffixDesc),
		Context: context,
	}
	_ = am.eventProxy.SendDeviceEvent(ctx, deviceEvent, eventDetails.EventCategory, eventDetails.EventSubCategory,
		raisedTimestamp)
}

//nolint:unparam
func (am *OnuAlarmManager) isAlarmDBDiffPresent(ctx context.Context) bool {
	return !reflect.DeepEqual(am.onuDBCopy, am.oltDbCopy)
}

func (am *OnuAlarmManager) incrementAlarmSequence() {
	//alarm sequence number wraps from 255 to 1.
	if am.lastAlarmSequence == 255 {
		am.lastAlarmSequence = 1
	} else {
		am.lastAlarmSequence++
	}
}

func (am *OnuAlarmManager) resetAlarmSequence() {
	am.lastAlarmSequence = 0
}

// flushAlarmSyncChannels flushes all alarm sync channels to discard any previous response
func (am *OnuAlarmManager) flushAlarmSyncChannels(ctx context.Context) {
	// flush alarm sync channel
	select {
	case <-am.eventChannel:
		logger.Debug(ctx, "flushed-alarm-sync-channel")
	default:
	}
	select {
	case <-am.StopAlarmAuditTimer:
		logger.Debug(ctx, "flushed-alarm-audit-timer-channel")
	default:
	}
}

// getDeviceEventData returns the event data for a device
//
//nolint:unparam
func (am *OnuAlarmManager) getDeviceEventData(ctx context.Context, classID me.ClassID, alarmNo uint8) (onuDeviceEvent, error) {
	if onuEventDetails, ok := am.onuEventsList[onuDevice{classID: classID, alarmno: alarmNo}]; ok {
		return onuEventDetails, nil
	}
	return onuDeviceEvent{}, errors.New("onu Event Detail not found")
}

// ResetAlarmUploadCounters resets alarm upload sequence number and number of commands
func (am *OnuAlarmManager) ResetAlarmUploadCounters() {
	am.onuAlarmManagerLock.Lock()
	am.alarmUploadSeqNo = 0
	am.alarmUploadNoOfCmdsOrMEs = 0
	am.onuAlarmManagerLock.Unlock()
}

// IncrementAlarmUploadSeqNo increments alarm upload sequence number
func (am *OnuAlarmManager) IncrementAlarmUploadSeqNo() {
	am.onuAlarmManagerLock.Lock()
	am.alarmUploadSeqNo++
	am.onuAlarmManagerLock.Unlock()
}

// GetAlarmUploadSeqNo gets alarm upload sequence number
func (am *OnuAlarmManager) GetAlarmUploadSeqNo() uint16 {
	am.onuAlarmManagerLock.RLock()
	value := am.alarmUploadSeqNo
	am.onuAlarmManagerLock.RUnlock()
	return value
}

// GetAlarmMgrEventChannel gets alarm manager event channel
func (am *OnuAlarmManager) GetAlarmMgrEventChannel() chan cmn.Message {
	return am.eventChannel
}

// GetOnuActiveAlarms - Fetch the Active Alarms on demand
func (am *OnuAlarmManager) GetOnuActiveAlarms(ctx context.Context) *extension.SingleGetValueResponse {

	am.onuAlarmRequestLock.Lock()
	defer am.onuAlarmRequestLock.Unlock()

	resp := extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status: extension.GetValueResponse_OK,
			Response: &extension.GetValueResponse_OnuActiveAlarms{
				OnuActiveAlarms: &extension.GetOnuOmciActiveAlarmsResponse{},
			},
		},
	}

	logger.Debugw(ctx, "Requesting to start audit on demand  ", log.Fields{"device-id": am.deviceID})
	am.isAsyncAlarmRequest = true

	am.AsyncAlarmsCommChan <- struct{}{}

	select {
	case <-time.After(10 * time.Second): //the time to wait needs further discussion.
		logger.Errorw(ctx, "Couldn't get the Alarms with in 10 seconds stipulated time frame ", log.Fields{"device-id": am.deviceID})
		am.isAsyncAlarmRequest = false

	case <-am.AsyncAlarmsCommChan:
		logger.Debugw(ctx, "Received response for the alarm audit ", log.Fields{"device-id": am.deviceID})

	}

	for activeAlarm := range am.activeAlarms {

		onuEventDetails := am.onuEventsList[onuDevice{classID: activeAlarm.classID, alarmno: activeAlarm.alarmNo}]
		activeAlarmData := extension.AlarmData{}
		activeAlarmData.ClassId = uint32(activeAlarm.classID)
		activeAlarmData.InstanceId = uint32(activeAlarm.instanceID)
		activeAlarmData.Name = onuEventDetails.EventName
		activeAlarmData.Description = onuEventDetails.EventDescription

		resp.Response.GetOnuActiveAlarms().ActiveAlarms = append(resp.Response.GetOnuActiveAlarms().ActiveAlarms, &activeAlarmData)

	}

	return &resp
}

// PrepareForGarbageCollection - remove references to prepare for garbage collection
func (am *OnuAlarmManager) PrepareForGarbageCollection(ctx context.Context, aDeviceID string) {
	logger.Debugw(ctx, "prepare for garbage collection", log.Fields{"device-id": aDeviceID})
	am.pDeviceHandler = nil
	am.pOnuDeviceEntry = nil
}

func (am *OnuAlarmManager) processOnuOnlyDifferences(ctx context.Context, failureTransition func()) error {
	for alarm := range am.onuDBCopy {
		if _, exists := am.oltDbCopy[meAlarmKey{classID: alarm.classID, instanceID: alarm.instanceID}]; !exists {
			omciAlarmMessage := createOmciAlarmMessage(alarm, am.onuDBCopy[alarm])
			if err := am.processAlarm(ctx, omciAlarmMessage, failureTransition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (am *OnuAlarmManager) processOltOnlyDifferences(ctx context.Context, failureTransition func()) error {
	for alarm := range am.oltDbCopy {
		if _, exists := am.onuDBCopy[meAlarmKey{classID: alarm.classID, instanceID: alarm.instanceID}]; !exists {
			omciAlarmMessage := createOmciAlarmMessage(alarm, am.oltDbCopy[alarm])
			if err := am.processAlarm(ctx, omciAlarmMessage, failureTransition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (am *OnuAlarmManager) processAttributeDifferences(ctx context.Context, failureTransition func()) error {
	for alarm := range am.onuDBCopy {
		if _, exists := am.oltDbCopy[alarm]; exists && am.onuDBCopy[alarm] != am.oltDbCopy[alarm] {
			omciAlarmMessage := createOmciAlarmMessage(alarm, am.onuDBCopy[alarm])
			if err := am.processAlarm(ctx, omciAlarmMessage, failureTransition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (am *OnuAlarmManager) processBufferedNotifications(ctx context.Context, failureTransition func()) error {
	for _, notif := range am.bufferedNotifications {
		logger.Debugw(ctx, "processing-buffered-alarm-notification", log.Fields{"device-id": am.deviceID, "notification": notif})
		if err := am.processAlarm(ctx, notif, failureTransition); err != nil {
			return err
		}
	}
	return nil
}

func (am *OnuAlarmManager) processAlarm(ctx context.Context, omciAlarmMessage *omci.AlarmNotificationMsg, failureTransition func()) error {
	// [https://jira.opencord.org/browse/VOL-5223]
	//The following test scenarios cause AlarmMgr to get into a loop state.
	//Test Scenario:
	// - Unconfigured MEs being reported by vendor ONTs.
	// - Undefined Alarm Bit Map (ONU-G ME for Example.)
	// - MEs created by OLT as per G984.4 standard are not part of ONU DB.
	if err := am.processAlarmData(ctx, omciAlarmMessage); err != nil {
		if statusErr, ok := status.FromError(err); ok {
			switch statusErr.Code() {
			case codes.NotFound:
				logger.Warnw(ctx, "ME Instance or ME Alarm Map not found in ONUDB", log.Fields{"device-id": am.deviceID, "Error": err})
			case codes.Unavailable:
				logger.Warnw(ctx, "Alarm Mgr is stopped, stop further processing", log.Fields{"device-id": am.deviceID, "Error": err})
				return err
			default:
				logger.Errorw(ctx, "Unexpected error", log.Fields{"device-id": am.deviceID, "Error": err})
				go failureTransition()
				return err
			}
		}
	}
	return nil
}

func createOmciAlarmMessage(alarm meAlarmKey, alarmBitmap [alarmBitMapSizeBytes]byte) *omci.AlarmNotificationMsg {
	return &omci.AlarmNotificationMsg{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    alarm.classID,
			EntityInstance: alarm.instanceID,
		},
		AlarmBitmap: alarmBitmap,
	}
}
