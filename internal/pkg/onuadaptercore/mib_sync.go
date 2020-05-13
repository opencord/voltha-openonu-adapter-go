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
	"context"
	"errors"

	"github.com/looplab/fsm"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

func (onuDeviceEntry *OnuDeviceEntry) enterStartingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})

	onuDeviceEntry.pOnuDB = NewOnuDeviceDB(context.TODO(), onuDeviceEntry)

	go onuDeviceEntry.ProcessMibSyncMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterLoadingMibTemplateState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterUploadingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.PDevOmciCC.sendMibReset(context.TODO(), ConstDefaultOmciTimeout, true)
}

func (onuDeviceEntry *OnuDeviceEntry) enterInSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.transferSystemEvent(MibDatabaseSync)
}

func (onuDeviceEntry *OnuDeviceEntry) enterExaminingMdsState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterResynchronizingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterAuditingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterOutOfSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) ProcessMibSyncMessages( /*ctx context.Context*/ ) {
	logger.Debugw("MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		case message, ok := <-onuDeviceEntry.pMibUploadFsm.commChan:
			if !ok {
				logger.Info("MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
				break loop
			}
			logger.Debugw("MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": onuDeviceEntry.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				onuDeviceEntry.handleTestMsg(msg)
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				onuDeviceEntry.handleOmciMessage(msg)
			default:
				logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "message.Type": message.Type})
			}
		}
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	// TODO: only this action?
	onuDeviceEntry.pMibUploadFsm.pFsm.Event("stop")
}

func (onuDeviceEntry *OnuDeviceEntry) handleTestMsg(msg TestMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"TestMessage received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case AnyTriggerForMibSyncUploadMib:
		onuDeviceEntry.pMibUploadFsm.pFsm.Event("upload_mib")
		logger.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMessage(msg OmciMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"OmciMessage received for device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibResetResponse Data", log.Fields{"data-fields": msgObj})
		if msgObj.Result != me.Success {
			logger.Errorw("Omci MibResetResponse Error - strange - what to do?", log.Fields{"Error": msgObj.Result})
			return
		}
		onuDeviceEntry.PDevOmciCC.sendMibUpload(context.TODO(), ConstDefaultOmciTimeout, true)
	case omci.MibUploadResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibUploadResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
		/* to be verified / reworked !!! */
		onuDeviceEntry.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
		if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
			onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
		} else {
			logger.Error("Invalid number of commands received for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "uploadNoOfCmds": onuDeviceEntry.PDevOmciCC.uploadNoOfCmds})
			//TODO right action?
			onuDeviceEntry.pMibUploadFsm.pFsm.Event("timeout")
		}
	case omci.MibUploadNextResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibUploadNextResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})

		onuDeviceEntry.pOnuDB.StoreMe(msgObj)

		if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
			onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
		} else {
			//TODO
			onuDeviceEntry.pOnuDB.LogMeDb()
			onuDeviceEntry.pMibUploadFsm.pFsm.Event("success")
		}
	}
}

func (onuDeviceEntry *OnuDeviceEntry) MibDbVolatileDict() error {
	logger.Debug("MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// func (onuDeviceEntry *OnuDeviceEntry) MibTemplateTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibUploadTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) GetMdsTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibResyncTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibReconcileTask() error {
// 	return errors.New("not_implemented")
// }
