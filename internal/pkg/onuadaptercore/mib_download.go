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
	"fmt"
	"time"

	"github.com/looplab/fsm"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

func (onuDeviceEntry *OnuDeviceEntry) enterDLStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM", log.Fields{"Start downloading OMCI MIB in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if onuDeviceEntry.omciMessageReceived == nil {
		onuDeviceEntry.omciMessageReceived = make(chan bool)
		logger.Debug(ctx, "MibDownload FSM - defining the BridgeInit RxChannel")
	}
	// start go routine for processing of MibDownload messages
	go onuDeviceEntry.processMibDownloadMessages(ctx)
}

func (onuDeviceEntry *OnuDeviceEntry) enterCreatingGalState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM", log.Fields{"Tx create::GAL Ethernet Profile in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	meInstance, err := onuDeviceEntry.PDevOmciCC.sendCreateGalEthernetProfile(log.WithSpanFromContext(context.TODO(), ctx), onuDeviceEntry.pOpenOnuAc.omciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		logger.Errorw(ctx, "GalEthernetProfile create failed, aborting MibDownload FSM!",
			log.Fields{"device-id": onuDeviceEntry.deviceID})
		pMibDlFsm := onuDeviceEntry.pMibDownloadFsm
		if pMibDlFsm != nil {
			go func(a_pAFsm *AdapterFsm) {
				_ = a_pAFsm.pFsm.Event(dlEvReset)
			}(pMibDlFsm)
		}
		return
	}
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterSettingOnu2gState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM", log.Fields{"Tx Set::ONU2-G in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	meInstance, err := onuDeviceEntry.PDevOmciCC.sendSetOnu2g(log.WithSpanFromContext(context.TODO(), ctx),
		onuDeviceEntry.pOpenOnuAc.omciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		logger.Errorw(ctx, "ONU2-G set failed, aborting MibDownload FSM!",
			log.Fields{"device-id": onuDeviceEntry.deviceID})
		pMibDlFsm := onuDeviceEntry.pMibDownloadFsm
		if pMibDlFsm != nil {
			go func(a_pAFsm *AdapterFsm) {
				_ = a_pAFsm.pFsm.Event(dlEvReset)
			}(pMibDlFsm)
		}
		return
	}
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterBridgeInitState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM - starting bridge config port loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	go onuDeviceEntry.performInitialBridgeSetup(ctx)
}

func (onuDeviceEntry *OnuDeviceEntry) enterDownloadedState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.transferSystemEvent(ctx, MibDownloadDone)
	//let's reset the state machine in order to release all resources now
	pMibDlFsm := onuDeviceEntry.pMibDownloadFsm
	if pMibDlFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(dlEvReset)
			}
		}(pMibDlFsm)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) enterResettingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibDownload FSM resetting", log.Fields{"device-id": onuDeviceEntry.deviceID})
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
				_ = a_pAFsm.pFsm.Event(dlEvRestart)
			}
		}(pMibDlFsm)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) processMibDownloadMessages(ctx context.Context) {
	logger.Debugw(ctx, "Start MibDownload Msg processing", log.Fields{"for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		// unless multiple channels are not involved, we should not use select
		message, ok := <-onuDeviceEntry.pMibDownloadFsm.commChan
		if !ok {
			logger.Info(ctx, "MibDownload Rx Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvRestart)
			break loop
		}
		logger.Debugw(ctx, "MibDownload Rx Msg", log.Fields{"Received message for device-id": onuDeviceEntry.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Debugw(ctx, "MibDownload abort ProcessMsg", log.Fields{"for device-id": onuDeviceEntry.deviceID})
				break loop
			}
			logger.Warnw(ctx, "MibDownload unknown TestMessage", log.Fields{"device-id": onuDeviceEntry.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			onuDeviceEntry.handleOmciMibDownloadMessage(ctx, msg)
		default:
			logger.Warn(ctx, "MibDownload Rx Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID,
				"message.Type": message.Type})
		}

	}
	logger.Debugw(ctx, "End MibDownload Msg processing", log.Fields{"for device-id": onuDeviceEntry.deviceID})
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibDownloadMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI MibDownload Msg", log.Fields{"device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for CreateResponse", log.Fields{"device-id": onuDeviceEntry.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.CreateResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for CreateResponse", log.Fields{"device-id": onuDeviceEntry.deviceID})
				return
			}
			logger.Debugw(ctx, "CreateResponse Data", log.Fields{"device-id": onuDeviceEntry.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success && msgObj.Result != me.InstanceExists {
				logger.Errorw(ctx, "Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"device-id": onuDeviceEntry.deviceID, "Error": msgObj.Result})
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
						_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvRxGalResp)
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
				logger.Errorw(ctx, "Omci Msg layer could not be detected for SetResponse", log.Fields{"device-id": onuDeviceEntry.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for SetResponse", log.Fields{"device-id": onuDeviceEntry.deviceID})
				return
			}
			logger.Debugw(ctx, "SetResponse Data", log.Fields{"device-id": onuDeviceEntry.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "Omci SetResponse Error - later: drive FSM to abort state ?", log.Fields{"device-id": onuDeviceEntry.deviceID,
					"Error": msgObj.Result})
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
						_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvRxOnu2gResp)
					}
					//so far that was the only MibDownlad Set Element ...
				}
			}
		} //SetResponseType
	default:
		{
			logger.Errorw(ctx, "Rx OMCI MibDownload unhandled MsgType", log.Fields{"device-id": onuDeviceEntry.deviceID,
				"omciMsgType": msg.OmciMsg.MessageType})
			return
		}
	} // switch msg.OmciMsg.MessageType
}

func (onuDeviceEntry *OnuDeviceEntry) performInitialBridgeSetup(ctx context.Context) {
	for uniNo, uniPort := range onuDeviceEntry.baseDeviceHandler.uniEntityMap {
		logger.Debugw(ctx, "Starting IntialBridgeSetup", log.Fields{
			"device-id": onuDeviceEntry.deviceID, "for PortNo": uniNo})

		//create MBSP
		meInstance, err := onuDeviceEntry.PDevOmciCC.sendCreateMBServiceProfile(
			log.WithSpanFromContext(context.TODO(), ctx), uniPort, onuDeviceEntry.pOpenOnuAc.omciTimeout, true)
		if err != nil {
			logger.Errorw(ctx, "MBServiceProfile create failed, aborting MibDownload FSM!", log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err = onuDeviceEntry.waitforOmciResponse(ctx, meInstance)
		if err != nil {
			logger.Errorw(ctx, "InitialBridgeSetup failed at MBSP, aborting MIB Download!",
				log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}

		//create MBPCD
		meInstance, err = onuDeviceEntry.PDevOmciCC.sendCreateMBPConfigData(
			log.WithSpanFromContext(context.TODO(), ctx), uniPort, onuDeviceEntry.pOpenOnuAc.omciTimeout, true)
		if err != nil {
			logger.Errorw(ctx, "MBPConfigData create failed, aborting MibDownload FSM!",
				log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err = onuDeviceEntry.waitforOmciResponse(ctx, meInstance)
		if err != nil {
			logger.Errorw(ctx, "InitialBridgeSetup failed at MBPCD, aborting MIB Download!",
				log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}

		//create EVTOCD
		meInstance, err = onuDeviceEntry.PDevOmciCC.sendCreateEVTOConfigData(
			log.WithSpanFromContext(context.TODO(), ctx), uniPort, onuDeviceEntry.pOpenOnuAc.omciTimeout, true)
		if err != nil {
			logger.Errorw(ctx, "EVTOConfigData create failed, aborting MibDownload FSM!",
				log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}
		onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
		//verify response
		err = onuDeviceEntry.waitforOmciResponse(ctx, meInstance)
		if err != nil {
			logger.Errorw(ctx, "InitialBridgeSetup failed at EVTOCD, aborting MIB Download!",
				log.Fields{"device-id": onuDeviceEntry.deviceID})
			_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvReset)
			return
		}
	}
	// if Config has been done for all UNI related instances let the FSM proceed
	// while we did not check here, if there is some port at all - !?
	logger.Infow(ctx, "IntialBridgeSetup finished", log.Fields{"device-id": onuDeviceEntry.deviceID})
	_ = onuDeviceEntry.pMibDownloadFsm.pFsm.Event(dlEvRxBridgeResp)
}

func (onuDeviceEntry *OnuDeviceEntry) waitforOmciResponse(ctx context.Context, apMeInstance *me.ManagedEntity) error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Info("MibDownload-bridge-init message reception canceled", log.Fields{"for device-id": onuDeviceEntry.deviceID})
	case <-time.After(onuDeviceEntry.PDevOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "MibDownload-bridge-init timeout", log.Fields{"for device-id": onuDeviceEntry.deviceID})
		return fmt.Errorf("mibDownloadBridgeInit timeout %s", onuDeviceEntry.deviceID)
	case success := <-onuDeviceEntry.omciMessageReceived:
		if success {
			logger.Debug(ctx, "MibDownload-bridge-init response received")
			return nil
		}
		// should not happen so far
		logger.Warnw(ctx, "MibDownload-bridge-init response error", log.Fields{"for device-id": onuDeviceEntry.deviceID})
		return fmt.Errorf("mibDownloadBridgeInit responseError %s", onuDeviceEntry.deviceID)
	}
}
