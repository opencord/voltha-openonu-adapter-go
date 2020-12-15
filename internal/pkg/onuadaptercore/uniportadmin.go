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
)

//lockStateFsm defines the structure for the state machine to lock/unlock the ONU UNI ports via OMCI
type lockStateFsm struct {
	pDeviceHandler           *deviceHandler
	deviceID                 string
	pOmciCC                  *omciCC
	adminState               bool
	requestEvent             OnuDeviceEvent
	omciLockResponseReceived chan bool //seperate channel needed for checking UNI port OMCi message responses
	pAdaptFsm                *AdapterFsm
	pLastTxMeInstance        *me.ManagedEntity
}

const (
	// events of lock/unlock UNI port FSM
	uniEvStart         = "uniEvStart"
	uniEvStartAdmin    = "uniEvStartAdmin"
	uniEvRxUnisResp    = "uniEvRxUnisResp"
	uniEvRxOnugResp    = "uniEvRxOnugResp"
	uniEvTimeoutSimple = "uniEvTimeoutSimple"
	uniEvTimeoutUnis   = "uniEvTimeoutUnis"
	uniEvReset         = "uniEvReset"
	uniEvRestart       = "uniEvRestart"
)
const (
	// states of lock/unlock UNI port FSM
	uniStDisabled    = "uniStDisabled"
	uniStStarting    = "uniStStarting"
	uniStSettingUnis = "uniStSettingUnis"
	uniStSettingOnuG = "uniStSettingOnuG"
	uniStAdminDone   = "uniStAdminDone"
	uniStResetting   = "uniStResetting"
)

//newLockStateFsm is the 'constructor' for the state machine to lock/unlock the ONU UNI ports via OMCI
func newLockStateFsm(ctx context.Context, apDevOmciCC *omciCC, aAdminState bool, aRequestEvent OnuDeviceEvent,
	aName string, apDeviceHandler *deviceHandler, aCommChannel chan Message) *lockStateFsm {
	instFsm := &lockStateFsm{
		pDeviceHandler: apDeviceHandler,
		deviceID:       apDeviceHandler.deviceID,
		pOmciCC:        apDevOmciCC,
		adminState:     aAdminState,
		requestEvent:   aRequestEvent,
	}
	instFsm.pAdaptFsm = NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw(ctx, "LockStateFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}
	if aAdminState { //port locking requested
		instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
			uniStDisabled,
			fsm.Events{

				{Name: uniEvStart, Src: []string{uniStDisabled}, Dst: uniStStarting},

				{Name: uniEvStartAdmin, Src: []string{uniStStarting}, Dst: uniStSettingUnis},
				// the settingUnis state is used for multi ME config for all UNI related ports
				// maybe such could be reflected in the state machine as well (port number parametrized)
				// but that looks not straightforward here - so we keep it simple here for the beginning(?)
				{Name: uniEvRxUnisResp, Src: []string{uniStSettingUnis}, Dst: uniStSettingOnuG},
				{Name: uniEvRxOnugResp, Src: []string{uniStSettingOnuG}, Dst: uniStAdminDone},

				{Name: uniEvTimeoutSimple, Src: []string{uniStSettingOnuG}, Dst: uniStStarting},
				{Name: uniEvTimeoutUnis, Src: []string{uniStSettingUnis}, Dst: uniStStarting},

				{Name: uniEvReset, Src: []string{uniStStarting, uniStSettingOnuG, uniStSettingUnis,
					uniStAdminDone}, Dst: uniStResetting},
				// exceptional treatment for all states except uniStResetting
				{Name: uniEvRestart, Src: []string{uniStStarting, uniStSettingOnuG, uniStSettingUnis,
					uniStAdminDone, uniStResetting}, Dst: uniStDisabled},
			},

			fsm.Callbacks{
				"enter_state":                 func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(ctx, e) },
				("enter_" + uniStStarting):    func(e *fsm.Event) { instFsm.enterAdminStartingState(ctx, e) },
				("enter_" + uniStSettingOnuG): func(e *fsm.Event) { instFsm.enterSettingOnuGState(ctx, e) },
				("enter_" + uniStSettingUnis): func(e *fsm.Event) { instFsm.enterSettingUnisState(ctx, e) },
				("enter_" + uniStAdminDone):   func(e *fsm.Event) { instFsm.enterAdminDoneState(ctx, e) },
				("enter_" + uniStResetting):   func(e *fsm.Event) { instFsm.enterResettingState(ctx, e) },
			},
		)
	} else { //port unlocking requested
		instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
			uniStDisabled,
			fsm.Events{

				{Name: uniEvStart, Src: []string{uniStDisabled}, Dst: uniStStarting},

				{Name: uniEvStartAdmin, Src: []string{uniStStarting}, Dst: uniStSettingOnuG},
				{Name: uniEvRxOnugResp, Src: []string{uniStSettingOnuG}, Dst: uniStSettingUnis},
				// the settingUnis state is used for multi ME config for all UNI related ports
				// maybe such could be reflected in the state machine as well (port number parametrized)
				// but that looks not straightforward here - so we keep it simple here for the beginning(?)
				{Name: uniEvRxUnisResp, Src: []string{uniStSettingUnis}, Dst: uniStAdminDone},

				{Name: uniEvTimeoutSimple, Src: []string{uniStSettingOnuG}, Dst: uniStStarting},
				{Name: uniEvTimeoutUnis, Src: []string{uniStSettingUnis}, Dst: uniStStarting},

				{Name: uniEvReset, Src: []string{uniStStarting, uniStSettingOnuG, uniStSettingUnis,
					uniStAdminDone}, Dst: uniStResetting},
				// exceptional treatment for all states except uniStResetting
				{Name: uniEvRestart, Src: []string{uniStStarting, uniStSettingOnuG, uniStSettingUnis,
					uniStAdminDone, uniStResetting}, Dst: uniStDisabled},
			},

			fsm.Callbacks{
				"enter_state":                 func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(ctx, e) },
				("enter_" + uniStStarting):    func(e *fsm.Event) { instFsm.enterAdminStartingState(ctx, e) },
				("enter_" + uniStSettingOnuG): func(e *fsm.Event) { instFsm.enterSettingOnuGState(ctx, e) },
				("enter_" + uniStSettingUnis): func(e *fsm.Event) { instFsm.enterSettingUnisState(ctx, e) },
				("enter_" + uniStAdminDone):   func(e *fsm.Event) { instFsm.enterAdminDoneState(ctx, e) },
				("enter_" + uniStResetting):   func(e *fsm.Event) { instFsm.enterResettingState(ctx, e) },
			},
		)
	}
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw(ctx, "LockStateFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Debugw(ctx, "LockStateFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//setSuccessEvent modifies the requested event notified on success
//assumption is that this is only called in the disabled (idle) state of the FSM, hence no sem protection required
func (oFsm *lockStateFsm) setSuccessEvent(aEvent OnuDeviceEvent) {
	oFsm.requestEvent = aEvent
}

func (oFsm *lockStateFsm) enterAdminStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if oFsm.omciLockResponseReceived == nil {
		oFsm.omciLockResponseReceived = make(chan bool)
		logger.Debug(ctx, "LockStateFSM - OMCI UniLock RxChannel defined")
	} else {
		// as we may 're-use' this instance of FSM and the connected channel
		// make sure there is no 'lingering' request in the already existing channel:
		// (simple loop sufficient as we are the only receiver)
		for len(oFsm.omciLockResponseReceived) > 0 {
			<-oFsm.omciLockResponseReceived
		}
	}
	// start go routine for processing of LockState messages
	go oFsm.processOmciLockMessages(ctx)

	//let the state machine run forward from here directly
	pLockStateAFsm := oFsm.pAdaptFsm
	if pLockStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(uniEvStartAdmin)
			}
		}(pLockStateAFsm)
	}
}

func (oFsm *lockStateFsm) enterSettingOnuGState(ctx context.Context, e *fsm.Event) {
	var omciAdminState uint8 = 1 //default locked
	if !oFsm.adminState {
		omciAdminState = 0
	}
	logger.Debugw(ctx, "LockStateFSM Tx Set::ONU-G:admin", log.Fields{
		"omciAdmin": omciAdminState, "in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	requestedAttributes := me.AttributeValueMap{"AdministrativeState": omciAdminState}
	meInstance := oFsm.pOmciCC.sendSetOnuGLS(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true,
		requestedAttributes, oFsm.pAdaptFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	//  we might already abort the processing with nil here, but maybe some auto-recovery may be tried
	//  - may be improved later, for now we just handle it with the Rx timeout or missing next event (stick in state)
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *lockStateFsm) enterSettingUnisState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM - starting PPTP config loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID, "LockState": oFsm.adminState})
	go oFsm.performUniPortAdminSet(ctx)
}

func (oFsm *lockStateFsm) enterAdminDoneState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oFsm.deviceID})
	//use DeviceHandler event notification directly, no need/support to update DeviceEntryState for lock/unlock
	oFsm.pDeviceHandler.deviceProcStatusUpdate(ctx, oFsm.requestEvent)

	//let's reset the state machine in order to release all resources now
	pLockStateAFsm := oFsm.pAdaptFsm
	if pLockStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(uniEvReset)
			}
		}(pLockStateAFsm)
	}
}

func (oFsm *lockStateFsm) enterResettingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM resetting", log.Fields{"device-id": oFsm.deviceID})
	pLockStateAFsm := oFsm.pAdaptFsm
	if pLockStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := Message{
			Type: TestMsg,
			Data: TestMessage{
				TestMessageVal: AbortMessageProcessing,
			},
		}
		pLockStateAFsm.commChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled'
		// see DownloadedState: decouple event transfer
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(uniEvRestart)
			}
		}(pLockStateAFsm)
		oFsm.pLastTxMeInstance = nil
	}
}

func (oFsm *lockStateFsm) processOmciLockMessages(ctx context.Context) {
	logger.Debugw(ctx, "Start LockStateFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info(ctx,"MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info(ctx, "LockStateFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(uniEvRestart)
			break loop
		}
		logger.Debugw(ctx, "LockStateFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Debugw(ctx, "LockStateFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "LockStateFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oFsm.handleOmciLockStateMessage(ctx, msg)
		default:
			logger.Warn(ctx, "LockStateFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Debugw(ctx, "End LockStateFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *lockStateFsm) handleOmciLockStateMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI LockStateFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	if msg.OmciMsg.MessageType == omci.SetResponseType {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
		if msgLayer == nil {
			logger.Errorw(ctx, "LockStateFsm - Omci Msg layer could not be detected for SetResponse",
				log.Fields{"device-id": oFsm.deviceID})
			return
		}
		msgObj, msgOk := msgLayer.(*omci.SetResponse)
		if !msgOk {
			logger.Errorw(ctx, "LockStateFsm - Omci Msg layer could not be assigned for SetResponse",
				log.Fields{"device-id": oFsm.deviceID})
			return
		}
		logger.Debugw(ctx, "LockStateFsm SetResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
		if msgObj.Result != me.Success {
			logger.Errorw(ctx, "LockStateFsm - Omci SetResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
			// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
			return
		}
		// compare comments above for CreateResponse (apply also here ...)
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
			// if, then something like:
			//oFsm.pOnuDB.StoreMe(msgObj)

			switch oFsm.pLastTxMeInstance.GetName() {
			case "OnuG":
				{ // let the FSM proceed ...
					_ = oFsm.pAdaptFsm.pFsm.Event(uniEvRxOnugResp)
				}
			case "PhysicalPathTerminationPointEthernetUni", "VirtualEthernetInterfacePoint":
				{ // let the PPTP init proceed by stopping the wait function
					oFsm.omciLockResponseReceived <- true
				}
			}
		}
	} else {
		logger.Errorw(ctx, "LockStateFsm - Rx OMCI unhandled MsgType", log.Fields{"omciMsgType": msg.OmciMsg.MessageType})
		return
	}
}

func (oFsm *lockStateFsm) performUniPortAdminSet(ctx context.Context) {
	var omciAdminState uint8 = 1 //default locked
	if !oFsm.adminState {
		omciAdminState = 0
	}
	//set PPTPEthUni or VEIP AdminState
	requestedAttributes := me.AttributeValueMap{"AdministrativeState": omciAdminState}

	for uniNo, uniPort := range oFsm.pOmciCC.pBaseDeviceHandler.uniEntityMap {
		logger.Debugw(ctx, "Setting PPTP admin state", log.Fields{
			"device-id": oFsm.deviceID, "for PortNo": uniNo})

		var meInstance *me.ManagedEntity
		if uniPort.portType == uniPPTP {
			meInstance = oFsm.pOmciCC.sendSetPptpEthUniLS(log.WithSpanFromContext(context.TODO(), ctx), uniPort.entityID, ConstDefaultOmciTimeout,
				true, requestedAttributes, oFsm.pAdaptFsm.commChan)
			oFsm.pLastTxMeInstance = meInstance
		} else if uniPort.portType == uniVEIP {
			meInstance = oFsm.pOmciCC.sendSetVeipLS(log.WithSpanFromContext(context.TODO(), ctx), uniPort.entityID, ConstDefaultOmciTimeout,
				true, requestedAttributes, oFsm.pAdaptFsm.commChan)
			oFsm.pLastTxMeInstance = meInstance
		} else {
			logger.Warnw(ctx, "Unsupported PPTP type - skip",
				log.Fields{"device-id": oFsm.deviceID, "Port": uniNo})
			continue
		}

		//verify response
		err := oFsm.waitforOmciResponse(ctx, meInstance)
		if err != nil {
			logger.Errorw(ctx, "PPTP Admin State set failed, aborting LockState set!",
				log.Fields{"device-id": oFsm.deviceID, "Port": uniNo})
			_ = oFsm.pAdaptFsm.pFsm.Event(uniEvReset)
			return
		}
	} //for all UNI ports
	// if Config has been done for all UNI related instances let the FSM proceed
	// while we did not check here, if there is some port at all - !?
	logger.Infow(ctx, "PPTP config loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(uniEvRxUnisResp)
}

func (oFsm *lockStateFsm) waitforOmciResponse(ctx context.Context, apMeInstance *me.ManagedEntity) error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow(ctx,"LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(30 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "LockStateFSM uni-set timeout", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("lockStateFsm uni-set timeout for device-id %s", oFsm.deviceID)
	case success := <-oFsm.omciLockResponseReceived:
		if success {
			logger.Debug(ctx, "LockStateFSM uni-set response received")
			return nil
		}
		// should not happen so far
		logger.Warnw(ctx, "LockStateFSM uni-set response error", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("lockStateFsm uni-set responseError for device-id %s", oFsm.deviceID)
	}
}
