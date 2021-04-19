/*
 * Copyright 2020-present Open Networking Foundation
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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/looplab/fsm"

	//"sync"
	"time"

	//"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

type sLastTxMeParameter struct {
	lastTxMessageType omci.MessageType
	pLastTxMeInstance *me.ManagedEntity
	repeatCount       uint8
}

var supportedClassIds = []me.ClassID{
	me.CardholderClassID,                              // 5
	me.CircuitPackClassID,                             // 6
	me.SoftwareImageClassID,                           // 7
	me.PhysicalPathTerminationPointEthernetUniClassID, // 11
	me.OltGClassID,                                    // 131
	me.OnuPowerSheddingClassID,                        // 133
	me.IpHostConfigDataClassID,                        // 134
	me.OnuGClassID,                                    // 256
	me.Onu2GClassID,                                   // 257
	me.TContClassID,                                   // 262
	me.AniGClassID,                                    // 263
	me.UniGClassID,                                    // 264
	me.PriorityQueueClassID,                           // 277
	me.TrafficSchedulerClassID,                        // 278
	me.VirtualEthernetInterfacePointClassID,           // 329
	me.EnhancedSecurityControlClassID,                 // 332
	me.OnuDynamicPowerManagementControlClassID,        // 336
	// 347 // definitions for ME "IPv6 host config data" are currently missing in omci-lib-go!
}

var fsmMsg TestMessageType

func (oo *OnuDeviceEntry) enterStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.pOnuDB = newOnuDeviceDB(log.WithSpanFromContext(context.TODO(), ctx), oo)
	go oo.processMibSyncMessages(ctx)
}

func (oo *OnuDeviceEntry) enterResettingMibState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": oo.deviceID})

	if (!oo.isNewOnu() && !oo.baseDeviceHandler.isReconciling()) || //use case: re-auditing failed
		oo.baseDeviceHandler.isSkipOnuConfigReconciling() { //use case: reconciling without omci-config failed
		oo.baseDeviceHandler.prepareReconcilingWithActiveAdapter(ctx)
		oo.devState = DeviceStatusInit
	}
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibReset(log.WithSpanFromContext(context.TODO(), ctx), oo.pOpenOnuAc.omciTimeout, true)
	//TODO: needs to handle timeouts
	//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
	//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
	oo.lastTxParamStruct.lastTxMessageType = omci.MibResetRequestType
	oo.lastTxParamStruct.repeatCount = 0
}

func (oo *OnuDeviceEntry) enterGettingVendorAndSerialState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting VendorId and SerialNumber in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"VendorId": "", "SerialNumber": 0}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.OnuGClassID, onugMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingEquipmentIDState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting EquipmentId in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"EquipmentId": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.Onu2GClassID, onu2gMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingFirstSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of first SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsCommitted": 0, "IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, firstSwImageMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingSecondSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of second SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsCommitted": 0, "IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, secondSwImageMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMacAddressState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting MacAddress in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"MacAddress": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.IpHostConfigDataClassID, ipHostConfigDataMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMibTemplateState(ctx context.Context, e *fsm.Event) {

	if oo.onuSwImageIndications.activeEntityEntry.valid {
		oo.sOnuPersistentData.PersActiveSwVersion = oo.onuSwImageIndications.activeEntityEntry.version
	} else {
		logger.Errorw(ctx, "get-mib-template: no active SW version found, working with empty SW version, which might be untrustworthy",
			log.Fields{"device-id": oo.deviceID})
	}
	if oo.getMibFromTemplate(ctx) {
		logger.Debug(ctx, "MibSync FSM - valid MEs stored from template")
		oo.pOnuDB.logMeDb(ctx)
		fsmMsg = LoadMibTemplateOk
	} else {
		logger.Debug(ctx, "MibSync FSM - no valid MEs stored from template - perform MIB-upload!")
		fsmMsg = LoadMibTemplateFailed

		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath]; exist {
			if mibTemplateIsGenerated {
				logger.Debugw(ctx,
					"MibSync FSM - template was successfully generated before, but doesn't exist or isn't usable anymore - reset flag in map",
					log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
				oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
			}
		}
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
	}
	mibSyncMsg := Message{
		Type: TestMsg,
		Data: TestMessage{
			TestMessageVal: fsmMsg,
		},
	}
	oo.pMibUploadFsm.commChan <- mibSyncMsg
}

func (oo *OnuDeviceEntry) enterUploadingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send MibUpload in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibUpload(log.WithSpanFromContext(context.TODO(), ctx), oo.pOpenOnuAc.omciTimeout, true)
	//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
	//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
	oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadRequestType
}

func (oo *OnuDeviceEntry) enterUploadDoneState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.transferSystemEvent(ctx, MibDatabaseSync)
	go func() {
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
	}()
}

func (oo *OnuDeviceEntry) enterInSyncState(ctx context.Context, e *fsm.Event) {
	oo.sOnuPersistentData.PersMibLastDbSync = uint32(time.Now().Unix())
	if oo.mibAuditInterval > 0 {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"trigger next Audit in State": e.FSM.Current(), "oo.mibAuditInterval": oo.mibAuditInterval, "device-id": oo.deviceID})
		go func() {
			time.Sleep(oo.mibAuditInterval)
			if err := oo.pMibUploadFsm.pFsm.Event(ulEvAuditMib); err != nil {
				logger.Debugw(ctx, "MibSyncFsm: Can't go to state auditing", log.Fields{"device-id": oo.deviceID, "err": err})
			}
		}()
	}
}

func (oo *OnuDeviceEntry) enterExaminingMdsState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.requestMdsValue(ctx)
}

func (oo *OnuDeviceEntry) enterResynchronizingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
	// TODOs:
	// VOL-3805 - Provide exclusive OMCI channel for one FSM
	// VOL-3785 - New event notifications and corresponding performance counters for openonu-adapter-go
	// VOL-3792 - Support periodical audit via mib resync
	// VOL-3793 - ONU-reconcile handling after adapter restart based on mib resync
}

func (oo *OnuDeviceEntry) enterExaminingMdsSuccessState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM",
		log.Fields{"Start processing on examining MDS success in State": e.FSM.Current(), "device-id": oo.deviceID})

	if oo.getMibFromTemplate(ctx) {
		oo.baseDeviceHandler.startReconciling(ctx, true)
		oo.baseDeviceHandler.addAllUniPorts(ctx)
		oo.baseDeviceHandler.setDeviceReason(drInitialMibDownloaded)
		oo.baseDeviceHandler.ReadyForSpecificOmciConfig = true
		// no need to reconcile additional data for MibDownloadFsm, LockStateFsm, or UnlockStateFsm

		oo.baseDeviceHandler.reconcileDeviceTechProf(ctx)

		// start go routine with select() on reconciling flow channel before
		// starting flow reconciling process to prevent loss of any signal
		go func() {
			// In multi-ONU/multi-flow environment stopping reconcilement has to be delayed until
			// we get a signal that the processing of the last step to rebuild the adapter internal
			// flow data is finished.
			select {
			case success := <-oo.baseDeviceHandler.chReconcilingFlowsFinished:
				if success {
					logger.Debugw(ctx, "reconciling flows has been finished in time",
						log.Fields{"device-id": oo.deviceID})
					oo.baseDeviceHandler.stopReconciling(ctx)
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)

				} else {
					logger.Debugw(ctx, "wait for reconciling flows aborted",
						log.Fields{"device-id": oo.deviceID})
					oo.baseDeviceHandler.setReconcilingFlows(false)
				}
			case <-time.After(500 * time.Millisecond):
				logger.Errorw(ctx, "timeout waiting for reconciling flows to be finished!",
					log.Fields{"device-id": oo.deviceID})
				oo.baseDeviceHandler.setReconcilingFlows(false)
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
			}
		}()
		oo.baseDeviceHandler.reconcileDeviceFlowConfig(ctx)

		if oo.sOnuPersistentData.PersUniDisableDone {
			oo.baseDeviceHandler.disableUniPortStateUpdate(ctx)
			oo.baseDeviceHandler.setDeviceReason(drOmciAdminLock)
		} else {
			oo.baseDeviceHandler.enableUniPortStateUpdate(ctx)
		}
	} else {
		logger.Debugw(ctx, "MibSync FSM",
			log.Fields{"Getting MIB from template not successful": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			//switch to reconciling with OMCI config
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
		}()
	}
}

func (oo *OnuDeviceEntry) enterAuditingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibAudit processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	if oo.baseDeviceHandler.checkAuditStartCondition(ctx, cUploadFsm) {
		oo.requestMdsValue(ctx)
	} else {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"Configuration is ongoing or missing - skip auditing!": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		}()
	}
}

func (oo *OnuDeviceEntry) enterReAuditingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start retest MdsValue processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	if oo.baseDeviceHandler.checkAuditStartCondition(ctx, cUploadFsm) {
		oo.requestMdsValue(ctx)
	} else {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"Configuration is ongoing or missing - skip re-auditing!": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		}()
	}
}

func (oo *OnuDeviceEntry) enterOutOfSyncState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
}

func (oo *OnuDeviceEntry) processMibSyncMessages(ctx context.Context) {
	logger.Debugw(ctx, "MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": oo.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		message, ok := <-oo.pMibUploadFsm.commChan
		if !ok {
			logger.Info(ctx, "MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": oo.deviceID})
			break loop
		}
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": oo.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			oo.handleTestMsg(ctx, msg)
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oo.handleOmciMessage(ctx, msg)
		default:
			logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "message.Type": message.Type})
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	// TODO: only this action?
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (oo *OnuDeviceEntry) handleTestMsg(ctx context.Context, msg TestMessage) {

	logger.Debugw(ctx, "MibSync Msg", log.Fields{"TestMessage received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case LoadMibTemplateFailed:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvUploadMib)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	case LoadMibTemplateOk:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (oo *OnuDeviceEntry) handleOmciMibResetResponseMessage(ctx context.Context, msg OmciMessage) {
	if oo.pMibUploadFsm.pFsm.Is(ulStResettingMib) {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer != nil {
			msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
			if msgOk {
				logger.Debugw(ctx, "MibResetResponse Data", log.Fields{"data-fields": msgObj})
				if msgObj.Result == me.Success {
					oo.sOnuPersistentData.PersMibDataSyncAdpt = 0
					// trigger retrieval of VendorId and SerialNumber
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetVendorAndSerial)
					return
				}
				logger.Errorw(ctx, "Omci MibResetResponse Error", log.Fields{"device-id": oo.deviceID, "Error": msgObj.Result})
			} else {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
			}
		} else {
			logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		}
	} else {
		//in case the last request was MdsGetRequest this issue may appear if the ONU was online before and has received the MIB reset
		//  with Sequence number 0x8000 as last request before - so it may still respond to that
		//  then we may force the ONU to react on the MdsGetRequest with a new message that uses an increased Sequence number
		if oo.lastTxParamStruct.lastTxMessageType == omci.GetRequestType && oo.lastTxParamStruct.repeatCount == 0 {
			logger.Debugw(ctx, "MibSync FSM - repeat MdsGetRequest (updated SequenceNumber)", log.Fields{"device-id": oo.deviceID})
			requestedAttributes := me.AttributeValueMap{"MibDataSync": ""}
			_ = oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
				me.OnuDataClassID, onuDataMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
			//TODO: needs extra handling of timeouts
			oo.lastTxParamStruct.repeatCount = 1
			return
		}
		logger.Errorw(ctx, "unexpected MibResetResponse - ignoring", log.Fields{"device-id": oo.deviceID})
		//perhaps some still lingering message from some prior activity, let's wait for the real response
		return
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (oo *OnuDeviceEntry) handleOmciMibUploadResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
		return
	}
	logger.Debugw(ctx, "MibUploadResponse Data for:", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	/* to be verified / reworked !!! */
	oo.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx), oo.pOpenOnuAc.omciTimeout, true)
		//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
		//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
		oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadNextRequestType
	} else {
		logger.Errorw(ctx, "Invalid number of commands received for:", log.Fields{"device-id": oo.deviceID, "uploadNoOfCmds": oo.PDevOmciCC.uploadNoOfCmds})
		//TODO right action?
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvTimeout)
	}
}

func (oo *OnuDeviceEntry) handleOmciMibUploadNextResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)

	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
		return
	}
	meName := msgObj.ReportedME.GetName()
	if meName == "UnknownItuG988ManagedEntity" || meName == "UnknownVendorSpecificManagedEntity" {
		logger.Debugw(ctx, "MibUploadNextResponse Data for unknown ME received - temporary workaround is to ignore it!",
			log.Fields{"device-id": oo.deviceID, "data-fields": msgObj, "meName": meName})
	} else {
		logger.Debugw(ctx, "MibUploadNextResponse Data for:",
			log.Fields{"device-id": oo.deviceID, "meName": meName, "data-fields": msgObj})
		meClassID := msgObj.ReportedME.GetClassID()
		meEntityID := msgObj.ReportedME.GetEntityID()
		meAttributes := msgObj.ReportedME.GetAttributeValueMap()
		oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, meAttributes)
	}
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx), oo.pOpenOnuAc.omciTimeout, true)
		//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
		//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
		oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadNextRequestType
	} else {
		oo.pOnuDB.logMeDb(ctx)
		err := oo.createAndPersistMibTemplate(ctx)
		if err != nil {
			logger.Errorw(ctx, "MibSync - MibTemplate - Failed to create and persist the mib template", log.Fields{"error": err, "device-id": oo.deviceID})
		}

		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseMessage(ctx context.Context, msg OmciMessage) error {
	var err error = nil

	if oo.lastTxParamStruct.lastTxMessageType != omci.GetRequestType ||
		oo.lastTxParamStruct.pLastTxMeInstance == nil {
		//in case the last request was MibReset this issue may appear if the ONU was online before and has received the MDS GetRequest
		//  with Sequence number 0x8000 as last request before - so it may still respond to that
		//  then we may force the ONU to react on the MIB reset with a new message that uses an increased Sequence number
		if oo.lastTxParamStruct.lastTxMessageType == omci.MibResetRequestType && oo.lastTxParamStruct.repeatCount == 0 {
			logger.Debugw(ctx, "MibSync FSM - repeat mibReset (updated SequenceNumber)", log.Fields{"device-id": oo.deviceID})
			_ = oo.PDevOmciCC.sendMibReset(log.WithSpanFromContext(context.TODO(), ctx), oo.pOpenOnuAc.omciTimeout, true)
			//TODO: needs extra handling of timeouts
			oo.lastTxParamStruct.repeatCount = 1
			return nil
		}
		logger.Warnw(ctx, "unexpected GetResponse - ignoring", log.Fields{"device-id": oo.deviceID})
		//perhaps some still lingering message from some prior activity, let's wait for the real response
		return nil
	}
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	logger.Debugw(ctx, "MibSync FSM - GetResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		entityID := oo.lastTxParamStruct.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.lastTxParamStruct.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meAttributes := msgObj.Attributes
			meInstance := oo.lastTxParamStruct.pLastTxMeInstance.GetName()
			logger.Debugf(ctx, "MibSync FSM - GetResponse Data for %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
			switch meInstance {
			case "OnuG":
				oo.sOnuPersistentData.PersVendorID = trimStringFromInterface(meAttributes["VendorId"])
				snBytes, _ := me.InterfaceToOctets(meAttributes["SerialNumber"])
				if onugSerialNumberLen == len(snBytes) {
					snVendorPart := fmt.Sprintf("%s", snBytes[:4])
					snNumberPart := hex.EncodeToString(snBytes[4:])
					oo.sOnuPersistentData.PersSerialNumber = snVendorPart + snNumberPart
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-G - VendorId/SerialNumber", log.Fields{"device-id": oo.deviceID,
						"onuDeviceEntry.vendorID": oo.sOnuPersistentData.PersVendorID, "onuDeviceEntry.serialNumber": oo.sOnuPersistentData.PersSerialNumber})
				} else {
					logger.Infow(ctx, "MibSync FSM - SerialNumber has wrong length - fill serialNumber with zeros", log.Fields{"device-id": oo.deviceID, "length": len(snBytes)})
					oo.sOnuPersistentData.PersSerialNumber = cEmptySerialNumberString
				}
				// trigger retrieval of EquipmentId
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetEquipmentID)
				return nil
			case "Onu2G":
				oo.sOnuPersistentData.PersEquipmentID = trimStringFromInterface(meAttributes["EquipmentId"])
				logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu2-G - EquipmentId", log.Fields{"device-id": oo.deviceID,
					"onuDeviceEntry.equipmentID": oo.sOnuPersistentData.PersEquipmentID})
				// trigger retrieval of 1st SW-image info
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetFirstSwVersion)
				return nil
			case "SoftwareImage":
				if entityID > secondSwImageMeID {
					logger.Errorw(ctx, "mibSync FSM - Failed to GetResponse Data for SoftwareImage with expected EntityId",
						log.Fields{"device-id": oo.deviceID, "entity-ID": entityID})
					return fmt.Errorf("mibSync FSM - SwResponse Data with unexpected EntityId: %s %x",
						oo.deviceID, entityID)
				}
				// need to use function for go lint complexity
				oo.handleSwImageIndications(ctx, entityID, meAttributes)
				return nil
			case "IpHostConfigData":
				macBytes, _ := me.InterfaceToOctets(meAttributes["MacAddress"])
				if omciMacAddressLen == len(macBytes) {
					oo.sOnuPersistentData.PersMacAddress = hex.EncodeToString(macBytes[:])
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for IpHostConfigData - MacAddress", log.Fields{"device-id": oo.deviceID,
						"macAddress": oo.sOnuPersistentData.PersMacAddress})
				} else {
					logger.Infow(ctx, "MibSync FSM - MacAddress wrong length - fill macAddress with zeros", log.Fields{"device-id": oo.deviceID, "length": len(macBytes)})
					oo.sOnuPersistentData.PersMacAddress = cEmptyMacAddrString
				}
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			case "OnuData":
				oo.checkMdsValue(ctx, meAttributes["MibDataSync"].(uint8))
				return nil
			}
		} else {
			logger.Warnf(ctx, "MibSync FSM - Received GetResponse Data for %s with wrong classID or entityID ", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.EntityClass)
		}
	} else {
		if err = oo.handleOmciGetResponseErrors(ctx, msgObj); err == nil {
			return nil
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
	return err
}

func (oo *OnuDeviceEntry) handleSwImageIndications(ctx context.Context, entityID uint16, meAttributes me.AttributeValueMap) {
	imageIsCommitted := meAttributes["IsCommitted"].(uint8)
	imageIsActive := meAttributes["IsActive"].(uint8)
	imageVersion := trimStringFromInterface(meAttributes["Version"])
	logger.Infow(ctx, "MibSync FSM - GetResponse Data for SoftwareImage",
		log.Fields{"device-id": oo.deviceID, "entityID": entityID,
			"version": imageVersion, "isActive": imageIsActive, "isCommitted": imageIsCommitted, "SNR": oo.sOnuPersistentData.PersSerialNumber})
	if firstSwImageMeID == entityID {
		//always accept the state of the first image (2nd image info should not yet be available)
		if imageIsActive == swIsActive {
			oo.onuSwImageIndications.activeEntityEntry.entityID = entityID
			oo.onuSwImageIndications.activeEntityEntry.valid = true
			oo.onuSwImageIndications.activeEntityEntry.version = imageVersion
			oo.onuSwImageIndications.activeEntityEntry.isCommitted = imageIsCommitted
			//as the SW version indication may stem from some ONU Down/up event
			//the complementary image state is to be invalidated
			//  (state of the second image is always expected afterwards or just invalid)
			oo.onuSwImageIndications.inactiveEntityEntry.valid = false
		} else {
			oo.onuSwImageIndications.inactiveEntityEntry.entityID = entityID
			oo.onuSwImageIndications.inactiveEntityEntry.valid = true
			oo.onuSwImageIndications.inactiveEntityEntry.version = imageVersion
			oo.onuSwImageIndications.inactiveEntityEntry.isCommitted = imageIsCommitted
			//as the SW version indication may stem form some ONU Down/up event
			//the complementary image state is to be invalidated
			//  (state of the second image is always expected afterwards or just invalid)
			oo.onuSwImageIndications.activeEntityEntry.valid = false
		}
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetSecondSwVersion)
		return
	} else if secondSwImageMeID == entityID {
		//2nd image info might conflict with first image info, in which case we priorize first image info!
		if imageIsActive == swIsActive { //2nd image reported to be active
			if oo.onuSwImageIndications.activeEntityEntry.valid {
				//conflict exists - state of first image is left active
				logger.Warnw(ctx, "mibSync FSM - both ONU images are reported as active - assuming 2nd to be inactive",
					log.Fields{"device-id": oo.deviceID})
				oo.onuSwImageIndications.inactiveEntityEntry.entityID = entityID
				oo.onuSwImageIndications.inactiveEntityEntry.valid = true ////to indicate that at least something has been reported
				oo.onuSwImageIndications.inactiveEntityEntry.version = imageVersion
				oo.onuSwImageIndications.inactiveEntityEntry.isCommitted = imageIsCommitted
			} else { //first image inactive, this one active
				oo.onuSwImageIndications.activeEntityEntry.entityID = entityID
				oo.onuSwImageIndications.activeEntityEntry.valid = true
				oo.onuSwImageIndications.activeEntityEntry.version = imageVersion
				oo.onuSwImageIndications.activeEntityEntry.isCommitted = imageIsCommitted
			}
		} else { //2nd image reported to be inactive
			if oo.onuSwImageIndications.inactiveEntityEntry.valid {
				//conflict exists - both images inactive - regard it as ONU failure and assume first image to be active
				logger.Warnw(ctx, "mibSync FSM - both ONU images are reported as inactive, defining first to be active",
					log.Fields{"device-id": oo.deviceID})
				oo.onuSwImageIndications.activeEntityEntry.entityID = firstSwImageMeID
				oo.onuSwImageIndications.activeEntityEntry.valid = true //to indicate that at least something has been reported
				//copy active commit/version from the previously stored inactive position
				oo.onuSwImageIndications.activeEntityEntry.version = oo.onuSwImageIndications.inactiveEntityEntry.version
				oo.onuSwImageIndications.activeEntityEntry.isCommitted = oo.onuSwImageIndications.inactiveEntityEntry.isCommitted
			}
			//in any case we indicate (and possibly overwrite) the second image indications as inactive
			oo.onuSwImageIndications.inactiveEntityEntry.entityID = entityID
			oo.onuSwImageIndications.inactiveEntityEntry.valid = true
			oo.onuSwImageIndications.inactiveEntityEntry.version = imageVersion
			oo.onuSwImageIndications.inactiveEntityEntry.isCommitted = imageIsCommitted
		}
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMacAddress)
		return
	}
}

func (oo *OnuDeviceEntry) handleOmciMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "MibSync Msg", log.Fields{"OmciMessage received for device-id": oo.deviceID,
		"msgType": msg.OmciMsg.MessageType, "msg": msg})
	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		oo.handleOmciMibResetResponseMessage(ctx, msg)

	case omci.MibUploadResponseType:
		oo.handleOmciMibUploadResponseMessage(ctx, msg)

	case omci.MibUploadNextResponseType:
		oo.handleOmciMibUploadNextResponseMessage(ctx, msg)

	case omci.GetResponseType:
		//TODO: error handling
		_ = oo.handleOmciGetResponseMessage(ctx, msg)

	default:
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})

	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseErrors(ctx context.Context, msgObj *omci.GetResponse) error {
	var err error = nil
	logger.Debugf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
	// Up to now the following erroneous results have been seen for different ONU-types to indicate an unsupported ME
	if msgObj.Result == me.UnknownInstance || msgObj.Result == me.UnknownEntity || msgObj.Result == me.ProcessingError || msgObj.Result == me.NotSupported {
		entityID := oo.lastTxParamStruct.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.lastTxParamStruct.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meInstance := oo.lastTxParamStruct.pLastTxMeInstance.GetName()
			switch meInstance {
			case "IpHostConfigData":
				logger.Debugw(ctx, "MibSync FSM - erroneous result for IpHostConfigData received - ONU doesn't support ME - fill macAddress with zeros",
					log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				oo.sOnuPersistentData.PersMacAddress = cEmptyMacAddrString
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			default:
				logger.Warnf(ctx, "MibSync FSM - erroneous result for %s received - no exceptional treatment defined", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
				err = fmt.Errorf("erroneous result for %s received - no exceptional treatment defined: %s", meInstance, oo.deviceID)
			}
		}
	} else {
		logger.Errorf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
		err = fmt.Errorf("erroneous result in GetResponse Data: %s - %s", msgObj.Result, oo.deviceID)
	}
	return err
}

func (oo *OnuDeviceEntry) isNewOnu() bool {
	return oo.sOnuPersistentData.PersMibLastDbSync == 0
}

func isSupportedClassID(meClassID me.ClassID) bool {
	for _, v := range supportedClassIds {
		if v == meClassID {
			return true
		}
	}
	return false
}

func trimStringFromInterface(input interface{}) string {
	ifBytes, _ := me.InterfaceToOctets(input)
	return fmt.Sprintf("%s", bytes.Trim(ifBytes, "\x00"))
}

func (oo *OnuDeviceEntry) mibDbVolatileDict(ctx context.Context) error {
	logger.Debug(ctx, "MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// createAndPersistMibTemplate method creates a mib template for the device id when operator enables the ONU device for the first time.
// We are creating a placeholder for "SerialNumber" for ME Class ID 6 and 256 and "MacAddress" for ME Class ID 134 in the template
// and then storing the template into etcd "service/voltha/omci_mibs/go_templates/verdor_id/equipment_id/software_version" path.
func (oo *OnuDeviceEntry) createAndPersistMibTemplate(ctx context.Context) error {
	logger.Debugw(ctx, "MibSync - MibTemplate - path name", log.Fields{"path": oo.mibTemplatePath,
		"device-id": oo.deviceID})

	oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
	if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath]; exist {
		if mibTemplateIsGenerated {
			logger.Debugw(ctx, "MibSync - MibTemplate - another thread has already started to generate it - skip",
				log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
			oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
			return nil
		}
		logger.Debugw(ctx, "MibSync - MibTemplate - previous generation attempt seems to be failed - try again",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	} else {
		logger.Debugw(ctx, "MibSync - MibTemplate - first ONU-instance of this kind - start generation",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = true
	oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()

	currentTime := time.Now()
	templateMap := make(map[string]interface{})
	templateMap["TemplateName"] = oo.mibTemplatePath
	templateMap["TemplateCreated"] = currentTime.Format("2006-01-02 15:04:05.000000")

	firstLevelMap := oo.pOnuDB.meDb
	for firstLevelKey, firstLevelValue := range firstLevelMap {
		logger.Debugw(ctx, "MibSync - MibTemplate - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
		classID := strconv.Itoa(int(firstLevelKey))

		secondLevelMap := make(map[string]interface{})
		for secondLevelKey, secondLevelValue := range firstLevelValue {
			thirdLevelMap := make(map[string]interface{})
			entityID := strconv.Itoa(int(secondLevelKey))
			thirdLevelMap["Attributes"] = secondLevelValue
			thirdLevelMap["InstanceId"] = entityID
			secondLevelMap[entityID] = thirdLevelMap
			if classID == "6" || classID == "256" {
				forthLevelMap := map[string]interface{}(thirdLevelMap["Attributes"].(me.AttributeValueMap))
				delete(forthLevelMap, "SerialNumber")
				forthLevelMap["SerialNumber"] = "%SERIAL_NUMBER%"

			}
			if classID == "134" {
				forthLevelMap := map[string]interface{}(thirdLevelMap["Attributes"].(me.AttributeValueMap))
				delete(forthLevelMap, "MacAddress")
				forthLevelMap["MacAddress"] = "%MAC_ADDRESS%"
			}
		}
		secondLevelMap["ClassId"] = classID
		templateMap[classID] = secondLevelMap
	}
	mibTemplate, err := json.Marshal(&templateMap)
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to marshal mibTemplate", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	err = oo.mibTemplateKVStore.Put(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath, string(mibTemplate))
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to store template in etcd", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	logger.Debugw(ctx, "MibSync - MibTemplate - Stored the template to etcd", log.Fields{"device-id": oo.deviceID})
	return nil
}

func (oo *OnuDeviceEntry) requestMdsValue(ctx context.Context) {
	logger.Debugw(ctx, "Request MDS value", log.Fields{"device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"MibDataSync": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
		me.OnuDataClassID, onuDataMeID, requestedAttributes, oo.pOpenOnuAc.omciTimeout, true, oo.pMibUploadFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.lastTxParamStruct.repeatCount = 0
}

func (oo *OnuDeviceEntry) checkMdsValue(ctx context.Context, mibDataSyncOnu uint8) {
	logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-Data - MibDataSync", log.Fields{"device-id": oo.deviceID,
		"mibDataSyncOnu": mibDataSyncOnu, "PersMibDataSyncAdpt": oo.sOnuPersistentData.PersMibDataSyncAdpt})

	mdsValuesAreEqual := oo.sOnuPersistentData.PersMibDataSyncAdpt == mibDataSyncOnu
	if oo.pMibUploadFsm.pFsm.Is(ulStAuditing) {
		if mdsValuesAreEqual {
			logger.Debugw(ctx, "MibSync FSM - mib audit - MDS check ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		} else {
			logger.Warnw(ctx, "MibSync FSM - mib audit - MDS check failed for the first time!", log.Fields{"device-id": oo.deviceID})
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
		}
	} else if oo.pMibUploadFsm.pFsm.Is(ulStReAuditing) {
		if mdsValuesAreEqual {
			logger.Debugw(ctx, "MibSync FSM - mib reaudit - MDS check ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		} else {
			logger.Errorw(ctx, "MibSync FSM - mib audit - MDS check failed for the second time!", log.Fields{"device-id": oo.deviceID})
			//TODO: send new event notification "MDS counter mismatch" to the core
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
		}
	} else if oo.pMibUploadFsm.pFsm.Is(ulStExaminingMds) {
		if mdsValuesAreEqual && mibDataSyncOnu != 0 {
			logger.Debugw(ctx, "MibSync FSM - MDS examination ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		} else {
			logger.Debugw(ctx, "MibSync FSM - MDS examination failed - new provisioning", log.Fields{"device-id": oo.deviceID})
			_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
		}
	} else {
		logger.Warnw(ctx, "wrong state for MDS evaluation!", log.Fields{"state": oo.pMibUploadFsm.pFsm.Current(), "device-id": oo.deviceID})
	}
}

//GetActiveImageMeID returns the Omci MeId of the active ONU image together with error code for validity
func (oo *OnuDeviceEntry) GetActiveImageMeID(ctx context.Context) (uint16, error) {
	if oo.onuSwImageIndications.activeEntityEntry.valid {
		return oo.onuSwImageIndications.activeEntityEntry.entityID, nil
	}
	return 0xFFFF, fmt.Errorf("no valid active image found: %s", oo.deviceID)
}

//GetInactiveImageMeID returns the Omci MeId of the inactive ONU image together with error code for validity
func (oo *OnuDeviceEntry) GetInactiveImageMeID(ctx context.Context) (uint16, error) {
	if oo.onuSwImageIndications.inactiveEntityEntry.valid {
		return oo.onuSwImageIndications.inactiveEntityEntry.entityID, nil
	}
	return 0xFFFF, fmt.Errorf("no valid inactive image found: %s", oo.deviceID)
}

//IsImageToBeCommitted returns true if the active image is still uncommitted
func (oo *OnuDeviceEntry) IsImageToBeCommitted(ctx context.Context, aImageID uint16) bool {
	if oo.onuSwImageIndications.activeEntityEntry.valid {
		if oo.onuSwImageIndications.activeEntityEntry.entityID == aImageID {
			if oo.onuSwImageIndications.activeEntityEntry.isCommitted == swIsUncommitted {
				return true
			}
		}
	}
	return false //all other case are treated as 'nothing to commit
}
func (oo *OnuDeviceEntry) getMibFromTemplate(ctx context.Context) bool {

	oo.mibTemplatePath = oo.buildMibTemplatePath()
	logger.Debugw(ctx, "MibSync FSM - get Mib from template", log.Fields{"path": fmt.Sprintf("%s/%s", cBasePathMibTemplateKvStore, oo.mibTemplatePath),
		"device-id": oo.deviceID})

	restoredFromMibTemplate := false
	Value, err := oo.mibTemplateKVStore.Get(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath)
	if err == nil {
		if Value != nil {
			logger.Debugf(ctx, "MibSync FSM - Mib template read: Key: %s, Value: %s  %s", Value.Key, Value.Value)

			// swap out tokens with specific data
			mibTmpString, _ := kvstore.ToString(Value.Value)
			mibTmpString2 := strings.Replace(mibTmpString, "%SERIAL_NUMBER%", oo.sOnuPersistentData.PersSerialNumber, -1)
			mibTmpString = strings.Replace(mibTmpString2, "%MAC_ADDRESS%", oo.sOnuPersistentData.PersMacAddress, -1)
			mibTmpBytes := []byte(mibTmpString)
			logger.Debugf(ctx, "MibSync FSM - Mib template tokens swapped out: %s", mibTmpBytes)

			var firstLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &firstLevelMap); err != nil {
				logger.Errorw(ctx, "MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": oo.deviceID})
			} else {
				for firstLevelKey, firstLevelValue := range firstLevelMap {
					//logger.Debugw(ctx, "MibSync FSM - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
					if uint16ValidNumber, err := strconv.ParseUint(firstLevelKey, 10, 16); err == nil {
						meClassID := me.ClassID(uint16ValidNumber)
						//logger.Debugw(ctx, "MibSync FSM - firstLevelKey is a number in uint16-range", log.Fields{"uint16ValidNumber": uint16ValidNumber})
						if isSupportedClassID(meClassID) {
							//logger.Debugw(ctx, "MibSync FSM - firstLevelKey is a supported classID", log.Fields{"meClassID": meClassID})
							secondLevelMap := firstLevelValue.(map[string]interface{})
							for secondLevelKey, secondLevelValue := range secondLevelMap {
								//logger.Debugw(ctx, "MibSync FSM - secondLevelKey", log.Fields{"secondLevelKey": secondLevelKey})
								if uint16ValidNumber, err := strconv.ParseUint(secondLevelKey, 10, 16); err == nil {
									meEntityID := uint16(uint16ValidNumber)
									//logger.Debugw(ctx, "MibSync FSM - secondLevelKey is a number and a valid EntityId", log.Fields{"meEntityID": meEntityID})
									thirdLevelMap := secondLevelValue.(map[string]interface{})
									for thirdLevelKey, thirdLevelValue := range thirdLevelMap {
										if thirdLevelKey == "Attributes" {
											//logger.Debugw(ctx, "MibSync FSM - thirdLevelKey refers to attributes", log.Fields{"thirdLevelKey": thirdLevelKey})
											attributesMap := thirdLevelValue.(map[string]interface{})
											//logger.Debugw(ctx, "MibSync FSM - attributesMap", log.Fields{"attributesMap": attributesMap})
											oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, attributesMap)
											restoredFromMibTemplate = true
										}
									}
								}
							}
						}
					}
				}
			}
		} else {
			logger.Debugw(ctx, "No MIB template found", log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
		}
	} else {
		logger.Errorf(ctx, "Get from kvstore operation failed for path",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	return restoredFromMibTemplate
}

//CancelProcessing terminates potentially running reconciling processes and stops the FSM
func (oo *OnuDeviceEntry) CancelProcessing(ctx context.Context) {

	if oo.baseDeviceHandler.isReconcilingFlows() {
		oo.baseDeviceHandler.chReconcilingFlowsFinished <- false
	}
	if oo.baseDeviceHandler.isReconciling() {
		oo.baseDeviceHandler.chReconcilingFinished <- false
	}
	//the MibSync FSM might be active all the ONU-active time,
	// hence it must be stopped unconditionally
	pMibUlFsm := oo.pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		_ = pMibUlFsm.Event(ulEvStop)
	}
}
