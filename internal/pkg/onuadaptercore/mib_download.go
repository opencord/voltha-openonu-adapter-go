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
	"time"

	"github.com/looplab/fsm"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

func (onuDeviceEntry *OnuDeviceEntry) enterDLStartingState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"Start downloading OMCI MIB in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if onuDeviceEntry.omciMessageReceived == nil {
		onuDeviceEntry.omciMessageReceived = make(chan bool)
		logger.Debug("MibDownload FSM - defining the BridgeInit RxChannel")
	}
	// start go routine for processing of MibDownload messages
	go onuDeviceEntry.ProcessMibDownloadMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterCreatingGalState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"Tx create::GAL Ethernet Profile in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	meInstance := onuDeviceEntry.PDevOmciCC.sendCreateGalEthernetProfile(context.TODO(), ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterSettingOnu2gState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"Tx Set::ONU2-G in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	meInstance := onuDeviceEntry.PDevOmciCC.sendSetOnu2g(context.TODO(), ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterBridgeInitState(e *fsm.Event) {
	logger.Infow("MibDownload FSM - starting bridge config port loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	go onuDeviceEntry.performInitialBridgeSetup()
}

func (onuDeviceEntry *OnuDeviceEntry) enterDownloadedState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.transferSystemEvent(MibDownloadDone)
	//let's reset the state machine in order to release all resources now
	pMibDlFsm := onuDeviceEntry.pMibDownloadFsm
	if pMibDlFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event("reset")
			}
		}(pMibDlFsm)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) enterResettingState(e *fsm.Event) {
	logger.Debugw("MibDownload FSM resetting", log.Fields{"device-id": onuDeviceEntry.deviceID})
	pMibDlFsm := onuDeviceEntry.pMibDownloadFsm
	if pMibDlFsm != nil {
		// abort running message processing
		fsmAbortMsg := Message{
			Type: TestMsg,
			Data: TestMessage{
				TestMessageVal: AbortMessageProcessing,
			},
		}
		pMibDlFsm.commChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled'
		// see DownloadedState: decouple event transfer
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event("restart")
			}
		}(pMibDlFsm)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) ProcessMibDownloadMessages( /*ctx context.Context*/ ) {
	logger.Debugw("Start MibDownload Msg processing", log.Fields{"for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		case message, ok := <-onuDeviceEntry.pMibDownloadFsm.commChan:
			if !ok {
				logger.Info("MibDownload Rx Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
				// but then we have to ensure a restart of the FSM as well - as exceptional procedure
				onuDeviceEntry.pMibDownloadFsm.pFsm.Event("restart")
				break loop
			}
			logger.Debugw("MibDownload Rx Msg", log.Fields{"Received message for device-id": onuDeviceEntry.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				if msg.TestMessageVal == AbortMessageProcessing {
					logger.Infow("MibDownload abort ProcessMsg", log.Fields{"for device-id": onuDeviceEntry.deviceID})
					break loop
				}
				logger.Warnw("MibDownload unknown TestMessage", log.Fields{"device-id": onuDeviceEntry.deviceID, "MessageVal": msg.TestMessageVal})
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				onuDeviceEntry.handleOmciMibDownloadMessage(msg)
			default:
				logger.Warn("MibDownload Rx Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID,
					"message.Type": message.Type})
			}
		}
	}
	logger.Infow("End MibDownload Msg processing", log.Fields{"for device-id": onuDeviceEntry.deviceID})
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibDownloadMessage(msg OmciMessage) {
	logger.Debugw("Rx OMCI MibDownload Msg", log.Fields{"device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
			if msgLayer == nil {
				logger.Error("Omci Msg layer could not be detected for CreateResponse")
				return
			}
			msgObj, msgOk := msgLayer.(*omci.CreateResponse)
			if !msgOk {
				logger.Error("Omci Msg layer could not be assigned for CreateResponse")
				return
			}
			logger.Debugw("CreateResponse Data", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			// maybe there is a way of pushing the specific create response type generally to the FSM
			//   and let the FSM verify, if the response was according to current state
			//   and possibly store the element to DB and progress - maybe some future option ...
			// but as that is not straightforward to me I insert the type checkes manually here
			//   and feed the FSM with only 'pre-defined' events ...
			if msgObj.EntityClass == onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetEntityID() {
				//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
				// if, then something like:
				//onuDeviceEntry.pOnuDB.StoreMe(msgObj)

				// maybe we can use just the same eventName for different state transitions like "forward"
				//   - might be checked, but so far I go for sure and have to inspect the concrete state events ...
				switch onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetName() {
				case "GalEthernetProfile":
					{ // let the FSM proceed ...
						onuDeviceEntry.pMibDownloadFsm.pFsm.Event("rx_gal_resp")
					}
				case "MacBridgeServiceProfile",
					"MacBridgePortConfigurationData",
					"ExtendedVlanTaggingOperationConfigurationData":
					{ // let bridge init proceed by stopping the wait function
						onuDeviceEntry.omciMessageReceived <- true
					}
				}
			}
		} //CreateResponseType
	//TODO
	//	onuDeviceEntry.pMibDownloadFsm.pFsm.Event("rx_evtocd_resp")

	case omci.SetResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
			if msgLayer == nil {
				logger.Error("Omci Msg layer could not be detected for SetResponse")
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Error("Omci Msg layer could not be assigned for SetResponse")
				return
			}
			logger.Debugw("SetResponse Data", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("Omci SetResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			// compare comments above for CreateResponse (apply also here ...)
			if msgObj.EntityClass == onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetEntityID() {
				//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
				// if, then something like:
				//onuDeviceEntry.pOnuDB.StoreMe(msgObj)

				switch onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetName() {
				case "Onu2G":
					{ // let the FSM proceed ...
						onuDeviceEntry.pMibDownloadFsm.pFsm.Event("rx_onu2g_resp")
					}
					//so far that was the only MibDownlad Set Element ...
				}
			}
		} //SetResponseType
	default:
		{
			logger.Errorw("Rx OMCI MibDownload unhandled MsgType", log.Fields{"omciMsgType": msg.OmciMsg.MessageType})
			return
		}
	} // switch msg.OmciMsg.MessageType
}

func (onuDeviceEntry *OnuDeviceEntry) performInitialBridgeSetup() {
	for uniNo, uniPort := range onuDeviceEntry.baseDeviceHandler.uniEntityMap {
		logger.Debugw("Starting IntialBridgeSetup", log.Fields{
			"deviceId": onuDeviceEntry.deviceID, "for PortNo": uniNo})

		//create MBSP
		meInstance := onuDeviceEntry.PDevOmciCC.sendCreateMBServiceProfile(
			context.TODO(), uniPort, ConstDefaultOmciTimeout, true)
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err := onuDeviceEntry.WaitforOmciResponse(meInstance)
		if err != nil {
			logger.Error("InitialBridgeSetup failed at MBSP, aborting MIB Download!")
			onuDeviceEntry.pMibDownloadFsm.pFsm.Event("reset")
			return
		}

		//create MBPCD
		meInstance = onuDeviceEntry.PDevOmciCC.sendCreateMBPConfigData(
			context.TODO(), uniPort, ConstDefaultOmciTimeout, true)
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err = onuDeviceEntry.WaitforOmciResponse(meInstance)
		if err != nil {
			logger.Error("InitialBridgeSetup failed at MBPCD, aborting MIB Download!")
			onuDeviceEntry.pMibDownloadFsm.pFsm.Event("reset")
			return
		}

		//create EVTOCD
		meInstance = onuDeviceEntry.PDevOmciCC.sendCreateEVTOConfigData(
			context.TODO(), uniPort, ConstDefaultOmciTimeout, true)
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err = onuDeviceEntry.WaitforOmciResponse(meInstance)
		if err != nil {
			logger.Error("InitialBridgeSetup failed at EVTOCD, aborting MIB Download!")
			onuDeviceEntry.pMibDownloadFsm.pFsm.Event("reset")
			return
		}
	}
	// if Config has been done for all UNI related instances let the FSM proceed
	// while we did not check here, if there is some port at all - !?
	logger.Infow("IntialBridgeSetup finished", log.Fields{"deviceId": onuDeviceEntry.deviceID})
	onuDeviceEntry.pMibDownloadFsm.pFsm.Event("rx_bridge_resp")
	return
}

func (onuDeviceEntry *OnuDeviceEntry) WaitforOmciResponse(a_pMeInstance *me.ManagedEntity) error {
	select {
	// maybe be also some outside cancel (but no context modelled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Info("MibDownload-bridge-init message reception canceled", log.Fields{"for device-id": onuDeviceEntry.deviceID})
	case <-time.After(3 * time.Second):
		logger.Warnw("MibDownload-bridge-init timeout", log.Fields{"for device-id": onuDeviceEntry.deviceID})
		return errors.New("MibDownloadBridgeInit timeout")
	case success := <-onuDeviceEntry.omciMessageReceived:
		if success == true {
			logger.Debug("MibDownload-bridge-init response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("MibDownload-bridge-init response error", log.Fields{"for device-id": onuDeviceEntry.deviceID})
		return errors.New("MibDownloadBridgeInit responseError")
	}
}
