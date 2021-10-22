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

//Package uniprt provides the utilities for uni port configuration
package uniprt

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/looplab/fsm"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
)

//LockStateFsm defines the structure for the state machine to lock/unlock the ONU UNI ports via OMCI
type LockStateFsm struct {
	deviceID                 string
	pDeviceHandler           cmn.IdeviceHandler
	pOnuDeviceEntry          cmn.IonuDeviceEntry
	pOmciCC                  *cmn.OmciCC
	mutexAdminState          sync.RWMutex
	adminState               bool
	requestEvent             cmn.OnuDeviceEvent
	omciLockResponseReceived chan bool //seperate channel needed for checking UNI port OMCi message responses
	PAdaptFsm                *cmn.AdapterFsm
	mutexPLastTxMeInstance   sync.RWMutex
	pLastTxMeInstance        *me.ManagedEntity
}

// events of lock/unlock UNI port FSM
const (
	UniEvStart         = "UniEvStart"
	UniEvStartAdmin    = "UniEvStartAdmin"
	UniEvRxUnisResp    = "UniEvRxUnisResp"
	UniEvRxOnugResp    = "UniEvRxOnugResp"
	UniEvTimeoutSimple = "UniEvTimeoutSimple"
	UniEvTimeoutUnis   = "UniEvTimeoutUnis"
	UniEvReset         = "UniEvReset"
	UniEvRestart       = "UniEvRestart"
)

// states of lock/unlock UNI port FSM
const (
	UniStDisabled    = "UniStDisabled"
	UniStStarting    = "UniStStarting"
	UniStSettingUnis = "UniStSettingUnis"
	UniStSettingOnuG = "UniStSettingOnuG"
	UniStAdminDone   = "UniStAdminDone"
	UniStResetting   = "UniStResetting"
)

// CUniFsmIdleState - TODO: add comment
const CUniFsmIdleState = UniStDisabled

//NewLockStateFsm is the 'constructor' for the state machine to lock/unlock the ONU UNI ports via OMCI
func NewLockStateFsm(ctx context.Context, aAdminState bool, aRequestEvent cmn.OnuDeviceEvent,
	aName string, apDeviceHandler cmn.IdeviceHandler, apOnuDeviceEntry cmn.IonuDeviceEntry, aCommChannel chan cmn.Message) *LockStateFsm {
	instFsm := &LockStateFsm{
		deviceID:        apDeviceHandler.GetDeviceID(),
		pDeviceHandler:  apDeviceHandler,
		pOnuDeviceEntry: apOnuDeviceEntry,
		pOmciCC:         apOnuDeviceEntry.GetDevOmciCC(),
		adminState:      aAdminState,
		requestEvent:    aRequestEvent,
	}
	instFsm.PAdaptFsm = cmn.NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.PAdaptFsm == nil {
		logger.Errorw(ctx, "LockStateFsm's cmn.AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}
	if aAdminState { //port locking requested
		instFsm.PAdaptFsm.PFsm = fsm.NewFSM(
			UniStDisabled,
			fsm.Events{

				{Name: UniEvStart, Src: []string{UniStDisabled}, Dst: UniStStarting},

				{Name: UniEvStartAdmin, Src: []string{UniStStarting}, Dst: UniStSettingUnis},
				// the settingUnis state is used for multi ME config for all UNI related ports
				// maybe such could be reflected in the state machine as well (port number parametrized)
				// but that looks not straightforward here - so we keep it simple here for the beginning(?)
				{Name: UniEvRxUnisResp, Src: []string{UniStSettingUnis}, Dst: UniStSettingOnuG},
				{Name: UniEvRxOnugResp, Src: []string{UniStSettingOnuG}, Dst: UniStAdminDone},

				{Name: UniEvTimeoutSimple, Src: []string{UniStSettingOnuG}, Dst: UniStStarting},
				{Name: UniEvTimeoutUnis, Src: []string{UniStSettingUnis}, Dst: UniStStarting},

				{Name: UniEvReset, Src: []string{UniStStarting, UniStSettingOnuG, UniStSettingUnis,
					UniStAdminDone}, Dst: UniStResetting},
				// exceptional treatment for all states except UniStResetting
				{Name: UniEvRestart, Src: []string{UniStStarting, UniStSettingOnuG, UniStSettingUnis,
					UniStAdminDone, UniStResetting}, Dst: UniStDisabled},
			},

			fsm.Callbacks{
				"enter_state":                 func(e *fsm.Event) { instFsm.PAdaptFsm.LogFsmStateChange(ctx, e) },
				("enter_" + UniStStarting):    func(e *fsm.Event) { instFsm.enterAdminStartingState(ctx, e) },
				("enter_" + UniStSettingOnuG): func(e *fsm.Event) { instFsm.enterSettingOnuGState(ctx, e) },
				("enter_" + UniStSettingUnis): func(e *fsm.Event) { instFsm.enterSettingUnisState(ctx, e) },
				("enter_" + UniStAdminDone):   func(e *fsm.Event) { instFsm.enterAdminDoneState(ctx, e) },
				("enter_" + UniStResetting):   func(e *fsm.Event) { instFsm.enterResettingState(ctx, e) },
			},
		)
	} else { //port unlocking requested
		instFsm.PAdaptFsm.PFsm = fsm.NewFSM(
			UniStDisabled,
			fsm.Events{

				{Name: UniEvStart, Src: []string{UniStDisabled}, Dst: UniStStarting},

				{Name: UniEvStartAdmin, Src: []string{UniStStarting}, Dst: UniStSettingOnuG},
				{Name: UniEvRxOnugResp, Src: []string{UniStSettingOnuG}, Dst: UniStSettingUnis},
				// the settingUnis state is used for multi ME config for all UNI related ports
				// maybe such could be reflected in the state machine as well (port number parametrized)
				// but that looks not straightforward here - so we keep it simple here for the beginning(?)
				{Name: UniEvRxUnisResp, Src: []string{UniStSettingUnis}, Dst: UniStAdminDone},

				{Name: UniEvTimeoutSimple, Src: []string{UniStSettingOnuG}, Dst: UniStStarting},
				{Name: UniEvTimeoutUnis, Src: []string{UniStSettingUnis}, Dst: UniStStarting},

				{Name: UniEvReset, Src: []string{UniStStarting, UniStSettingOnuG, UniStSettingUnis,
					UniStAdminDone}, Dst: UniStResetting},
				// exceptional treatment for all states except UniStResetting
				{Name: UniEvRestart, Src: []string{UniStStarting, UniStSettingOnuG, UniStSettingUnis,
					UniStAdminDone, UniStResetting}, Dst: UniStDisabled},
			},

			fsm.Callbacks{
				"enter_state":                 func(e *fsm.Event) { instFsm.PAdaptFsm.LogFsmStateChange(ctx, e) },
				("enter_" + UniStStarting):    func(e *fsm.Event) { instFsm.enterAdminStartingState(ctx, e) },
				("enter_" + UniStSettingOnuG): func(e *fsm.Event) { instFsm.enterSettingOnuGState(ctx, e) },
				("enter_" + UniStSettingUnis): func(e *fsm.Event) { instFsm.enterSettingUnisState(ctx, e) },
				("enter_" + UniStAdminDone):   func(e *fsm.Event) { instFsm.enterAdminDoneState(ctx, e) },
				("enter_" + UniStResetting):   func(e *fsm.Event) { instFsm.enterResettingState(ctx, e) },
			},
		)
	}
	if instFsm.PAdaptFsm.PFsm == nil {
		logger.Errorw(ctx, "LockStateFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Debugw(ctx, "LockStateFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//SetSuccessEvent modifies the requested event notified on success
//assumption is that this is only called in the disabled (idle) state of the FSM, hence no sem protection required
func (oFsm *LockStateFsm) SetSuccessEvent(aEvent cmn.OnuDeviceEvent) {
	oFsm.requestEvent = aEvent
}

func (oFsm *LockStateFsm) enterAdminStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if oFsm.omciLockResponseReceived == nil {
		oFsm.omciLockResponseReceived = make(chan bool)
		logger.Debug(ctx, "LockStateFSM - OMCI UniLock RxChannel defined")
	} else {
		// as we may 're-use' this instance of FSM and the connected channel
		// make sure there is no 'lingering' request in the already existing channels:
		// (simple loop sufficient as we are the only receiver)
		for len(oFsm.omciLockResponseReceived) > 0 {
			<-oFsm.omciLockResponseReceived
		}
		for len(oFsm.PAdaptFsm.CommChan) > 0 {
			<-oFsm.PAdaptFsm.CommChan
		}
	}
	// start go routine for processing of LockState messages
	go oFsm.processOmciLockMessages(ctx)

	//let the state machine run forward from here directly
	pLockStateAFsm := oFsm.PAdaptFsm
	if pLockStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *cmn.AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.PFsm != nil {
				_ = a_pAFsm.PFsm.Event(UniEvStartAdmin)
			}
		}(pLockStateAFsm)
	}
}

func (oFsm *LockStateFsm) enterSettingOnuGState(ctx context.Context, e *fsm.Event) {
	var omciAdminState uint8 = 1 //default locked
	oFsm.mutexAdminState.RLock()
	if !oFsm.adminState {
		omciAdminState = 0
	}
	oFsm.mutexAdminState.RUnlock()
	logger.Debugw(ctx, "LockStateFSM Tx Set::ONU-G:admin", log.Fields{
		"omciAdmin": omciAdminState, "in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	requestedAttributes := me.AttributeValueMap{"AdministrativeState": omciAdminState}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendSetOnuGLS(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
		requestedAttributes, oFsm.PAdaptFsm.CommChan)
	if err != nil {
		//Indicate the failure in UnLock case
		if omciAdminState == 0 {
			oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
		}
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "OnuGLS set failed, aborting LockStateFSM", log.Fields{"device-id": oFsm.deviceID})
		pLockStateAFsm := oFsm.PAdaptFsm
		if pLockStateAFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				if a_pAFsm != nil && a_pAFsm.PFsm != nil {
					_ = a_pAFsm.PFsm.Event(UniEvReset)
				}
			}(pLockStateAFsm)
		}
		return
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	if oFsm.pLastTxMeInstance == nil {
		//Indicate the failure in UnLock case
		if omciAdminState == 0 {
			oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
		}
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "could not send OMCI message from LockStateFsm", log.Fields{
			"device-id": oFsm.deviceID})
		//some more sophisticated approach is possible, e.g. repeating once, by now let's reset the state machine in order to release all resources now
		pLockStateAFsm := oFsm.PAdaptFsm
		if pLockStateAFsm != nil {

			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(a_pAFsm *cmn.AdapterFsm) {
				if a_pAFsm != nil && a_pAFsm.PFsm != nil {
					_ = a_pAFsm.PFsm.Event(UniEvReset)
				}
			}(pLockStateAFsm)
		}
		return
	}
	oFsm.mutexPLastTxMeInstance.Unlock()
}

func (oFsm *LockStateFsm) enterSettingUnisState(ctx context.Context, e *fsm.Event) {
	oFsm.mutexAdminState.RLock()
	logger.Debugw(ctx, "LockStateFSM - starting UniTP adminState loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID, "LockState": oFsm.adminState})
	oFsm.mutexAdminState.RUnlock()
	go oFsm.performUniPortAdminSet(ctx)
}

func (oFsm *LockStateFsm) enterAdminDoneState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oFsm.deviceID})
	//use DeviceHandler event notification directly, no need/support to update DeviceEntryState for lock/unlock
	oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, oFsm.requestEvent)

	//let's reset the state machine in order to release all resources now
	pLockStateAFsm := oFsm.PAdaptFsm
	if pLockStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *cmn.AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.PFsm != nil {
				_ = a_pAFsm.PFsm.Event(UniEvReset)
			}
		}(pLockStateAFsm)
	}
}

func (oFsm *LockStateFsm) enterResettingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "LockStateFSM resetting", log.Fields{"device-id": oFsm.deviceID})
	//If the fsm is reseted because of a failure during reenable, then issue the fail event.
	if oFsm.requestEvent == cmn.UniEnableStateFailed {
		logger.Debugw(ctx, "LockStateFSM send notification to core", log.Fields{"state": e.FSM.Current(), "device-id": oFsm.deviceID})
		//use DeviceHandler event notification directly, no need/support to update DeviceEntryState for lock/unlock
		oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, oFsm.requestEvent)
	}

	pLockStateAFsm := oFsm.PAdaptFsm
	if pLockStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := cmn.Message{
			Type: cmn.TestMsg,
			Data: cmn.TestMessage{
				TestMessageVal: cmn.AbortMessageProcessing,
			},
		}
		pLockStateAFsm.CommChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled'
		// see DownloadedState: decouple event transfer
		go func(a_pAFsm *cmn.AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.PFsm != nil {
				_ = a_pAFsm.PFsm.Event(UniEvRestart)
			}
		}(pLockStateAFsm)
		oFsm.mutexPLastTxMeInstance.Lock()
		oFsm.pLastTxMeInstance = nil
		oFsm.mutexPLastTxMeInstance.Unlock()
	}
}

func (oFsm *LockStateFsm) processOmciLockMessages(ctx context.Context) {
	logger.Debugw(ctx, "Start LockStateFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info(ctx,"MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.PAdaptFsm.CommChan
		if !ok {
			logger.Info(ctx, "LockStateFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.PAdaptFsm.PFsm.Event(UniEvRestart)
			break loop
		}
		logger.Debugw(ctx, "LockStateFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case cmn.TestMsg:
			msg, _ := message.Data.(cmn.TestMessage)
			if msg.TestMessageVal == cmn.AbortMessageProcessing {
				logger.Debugw(ctx, "LockStateFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "LockStateFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case cmn.OMCI:
			msg, _ := message.Data.(cmn.OmciMessage)
			oFsm.handleOmciLockStateMessage(ctx, msg)
		default:
			logger.Warn(ctx, "LockStateFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Debugw(ctx, "End LockStateFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *LockStateFsm) handleOmciLockStateMessage(ctx context.Context, msg cmn.OmciMessage) {
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

		//should never appear, left here for robustness
		oFsm.mutexPLastTxMeInstance.RLock()
		if oFsm.pLastTxMeInstance != nil {
			// compare comments above for CreateResponse (apply also here ...)
			if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
				//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
				// if, then something like:
				//oFsm.pOnuDB.StoreMe(msgObj)

				switch oFsm.pLastTxMeInstance.GetName() {
				case "OnuG":
					{ // let the FSM proceed ...
						oFsm.mutexPLastTxMeInstance.RUnlock()
						_ = oFsm.PAdaptFsm.PFsm.Event(UniEvRxOnugResp)
					}
				case "PhysicalPathTerminationPointEthernetUni", "VirtualEthernetInterfacePoint":
					{ // let the PPTP init proceed by stopping the wait function
						oFsm.mutexPLastTxMeInstance.RUnlock()
						oFsm.omciLockResponseReceived <- true
					}
				default:
					{
						logger.Warnw(ctx, "Unsupported ME name received!",
							log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
						oFsm.mutexPLastTxMeInstance.RUnlock()
					}
				}
			} else {
				oFsm.mutexPLastTxMeInstance.RUnlock()
				logger.Warnf(ctx, "LockStateFsm - Received SetResponse Data for %s with wrong classID or entityID ",
					log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj}, msgObj.EntityClass)
			}
		} else {
			oFsm.mutexPLastTxMeInstance.RUnlock()
			logger.Errorw(ctx, "pLastTxMeInstance is nil", log.Fields{"device-id": oFsm.deviceID})
			return
		}
	} else {
		logger.Errorw(ctx, "LockStateFsm - Rx OMCI unhandled MsgType", log.Fields{"omciMsgType": msg.OmciMsg.MessageType})
		return
	}
}

func (oFsm *LockStateFsm) performUniPortAdminSet(ctx context.Context) {
	var omciAdminState uint8 = 1 //default locked
	oFsm.mutexAdminState.RLock()
	if !oFsm.adminState {
		omciAdminState = 0
	}
	oFsm.mutexAdminState.RUnlock()
	//set PPTPEthUni or VEIP AdminState
	requestedAttributes := me.AttributeValueMap{"AdministrativeState": omciAdminState}

	for uniNo, uniPort := range *oFsm.pDeviceHandler.GetUniEntityMap() {
		// only unlock the UniPort in case it is defined for usage (R2.6 limit only one port),
		// compare also limitation for logical voltha port in dh.EnableUniPortStateUpdate()

		if (omciAdminState == 1) || (1<<uniPort.UniID)&oFsm.pDeviceHandler.GetUniPortMask() == (1<<uniPort.UniID) {
			var meInstance *me.ManagedEntity
			if uniPort.PortType == cmn.UniPPTP {
				logger.Debugw(ctx, "Setting PPTP admin state", log.Fields{
					"device-id": oFsm.deviceID, "for PortNo": uniNo, "state (0-unlock)": omciAdminState})
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendSetPptpEthUniLS(log.WithSpanFromContext(context.TODO(), ctx),
					uniPort.EntityID, oFsm.pDeviceHandler.GetOmciTimeout(),
					true, requestedAttributes, oFsm.PAdaptFsm.CommChan)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "SetPptpEthUniLS set failed, aborting LockStateFsm!",
						log.Fields{"device-id": oFsm.deviceID})
					//Indicate the failure in UnLock case
					if omciAdminState == 0 {
						oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
					}
					_ = oFsm.PAdaptFsm.PFsm.Event(UniEvReset)
					return
				}
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()
			} else if uniPort.PortType == cmn.UniVEIP {
				logger.Debugw(ctx, "Setting VEIP admin state", log.Fields{
					"device-id": oFsm.deviceID, "for PortNo": uniNo, "state (0-unlock)": omciAdminState})
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendSetVeipLS(log.WithSpanFromContext(context.TODO(), ctx),
					uniPort.EntityID, oFsm.pDeviceHandler.GetOmciTimeout(),
					true, requestedAttributes, oFsm.PAdaptFsm.CommChan)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "SetVeipLS set failed, aborting LockStateFsm!",
						log.Fields{"device-id": oFsm.deviceID})
					//Indicate the failure in UnLock case
					if omciAdminState == 0 {
						oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
					}
					_ = oFsm.PAdaptFsm.PFsm.Event(UniEvReset)
					return
				}
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()
			} else {
				//TODO: Discuss on the uni port type POTS .
				logger.Warnw(ctx, "Unsupported UniTP type - skip",
					log.Fields{"device-id": oFsm.deviceID, "Port": uniNo})
				continue
			}
			oFsm.mutexPLastTxMeInstance.RLock()
			if oFsm.pLastTxMeInstance == nil {
				oFsm.mutexPLastTxMeInstance.RUnlock()
				logger.Errorw(ctx, "could not send PortAdmin OMCI message from LockStateFsm", log.Fields{
					"device-id": oFsm.deviceID, "Port": uniNo})
				//Indicate the failure in UnLock case
				if omciAdminState == 0 {
					oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
				}
				//some more sophisticated approach is possible, e.g. repeating once, by now let's reset the state machine in order to release all resources now
				_ = oFsm.PAdaptFsm.PFsm.Event(UniEvReset)
				return
			}
			oFsm.mutexPLastTxMeInstance.RUnlock()

			//verify response
			err := oFsm.waitforOmciResponse(ctx, meInstance)
			if err != nil {
				logger.Errorw(ctx, "UniTP Admin State set failed, aborting LockState set!",
					log.Fields{"device-id": oFsm.deviceID, "Port": uniNo})
				//Indicate the failure in UnLock case
				if omciAdminState == 0 {
					oFsm.SetSuccessEvent(cmn.UniEnableStateFailed)
				}
				_ = oFsm.PAdaptFsm.PFsm.Event(UniEvReset)
				return
			}
		}
	} //for all UNI ports
	// if Config has been done for all UNI related instances let the FSM proceed
	// while we did not check here, if there is some port at all - !?
	logger.Infow(ctx, "UniTP adminState loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.PAdaptFsm.PFsm.Event(UniEvRxUnisResp)
}

func (oFsm *LockStateFsm) waitforOmciResponse(ctx context.Context, apMeInstance *me.ManagedEntity) error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow(ctx,"LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "lockStateFSM uni-set timeout", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("lockStateFsm uni-set timeout for device-id %s", oFsm.deviceID)
	case success := <-oFsm.omciLockResponseReceived:
		if success {
			logger.Debug(ctx, "LockStateFSM uni-set response received")
			return nil
		}
		// should not happen so far
		logger.Warnw(ctx, "lockStateFSM uni-set response error", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("lockStateFsm uni-set responseError for device-id %s", oFsm.deviceID)
	}
}
