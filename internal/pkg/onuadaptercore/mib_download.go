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
	"github.com/looplab/fsm"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"

	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

func (onuDeviceEntry *OnuDeviceEntry) enterDLStartingState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"Start downloading OMCI MIB in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})

	// start go routine for processing of MibDownload messages
	go onuDeviceEntry.ProcessMibDownloadMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterDownloadingState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"GAL Ethernet Profile set in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	//onuDeviceEntry.PDevOmciCC.sendGalEthernetProfileSet(context.TODO(), ConstDefaultOmciTimeout, true)
}

func (onuDeviceEntry *OnuDeviceEntry) enterDownloadedState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.transferSystemEvent(MibDownloadDone)
}

func (onuDeviceEntry *OnuDeviceEntry) ProcessMibDownloadMessages( /*ctx context.Context*/ ) {
	logger.Debugw("MibDownload Msg", log.Fields{"Start routine to process OMCI-messages for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		case message, ok := <-onuDeviceEntry.pMibDownloadFsm.commChan:
			if !ok {
				logger.Info("MibDownload Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
				break loop
			}
			logger.Debugw("MibDownload Msg", log.Fields{"Received OMCI message for device-id": onuDeviceEntry.deviceID})

			if message.Type != OMCI {
				logger.Warn("MibDownload Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID,
					"message.Type": message.Type})
			} else {
				msg, _ := message.Data.(OmciMessage)
				onuDeviceEntry.handleOmciMibDownloadMessage(msg)
			}
		}
	}
	logger.Info("MibDownload Msg", log.Fields{"Stop receiving messages for device-id": onuDeviceEntry.deviceID})
	// TODO: only this action?
	onuDeviceEntry.pMibDownloadFsm.pFsm.Event("restart")
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibDownloadMessage(msg OmciMessage) {

	logger.Debugw("Rx OMCI MibDownload Msg", log.Fields{"device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	/*
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
			// to be verified / reworked !!!
			onuDeviceEntry.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
			if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
				onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
			} else {
				logger.Error("Invalid number of commands received for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "uploadNoOfCmds": onuDeviceEntry.PDevOmciCC.uploadNoOfCmds})
				//TODO right action?
				onuDeviceEntry.MibSyncFsm.Event("timeout")
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
				onuDeviceEntry.MibSyncFsm.Event("success")
			}
		}
	*/
}
