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
	"strconv"
	"time"

	"github.com/looplab/fsm"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

//UniPonAniConfigFsm defines the structure for the state machine to lock/unlock the ONU UNI ports via OMCI
type UniPonAniConfigFsm struct {
	pOmciCC                  *OmciCC
	pOnuUniPort              *OnuUniPort
	pUniTechProf             *OnuUniTechProf
	pOnuDB                   *OnuDeviceDB
	techProfileID            uint16
	requestEvent             OnuDeviceEvent
	omciMIdsResponseReceived chan bool //seperate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                *AdapterFsm
	chSuccess                chan<- uint8
	procStep                 uint8
	chanSet                  bool
	mapperSP0ID              uint16
	macBPCD0ID               uint16
	tcont0ID                 uint16
	alloc0ID                 uint16
	gemPortXID               []uint16
	upQueueXID               []uint16
	downQueueXID             []uint16
}

//NewUniPonAniConfigFsm is the 'constructor' for the state machine to lock/unlock the ONU UNI ports via OMCI
func NewUniPonAniConfigFsm(apDevOmciCC *OmciCC, apUniPort *OnuUniPort, apUniTechProf *OnuUniTechProf,
	apOnuDB *OnuDeviceDB, aTechProfileID uint16, aRequestEvent OnuDeviceEvent, aName string,
	aDeviceID string, aCommChannel chan Message) *UniPonAniConfigFsm {
	instFsm := &UniPonAniConfigFsm{
		pOmciCC:       apDevOmciCC,
		pOnuUniPort:   apUniPort,
		pUniTechProf:  apUniTechProf,
		pOnuDB:        apOnuDB,
		techProfileID: aTechProfileID,
		requestEvent:  aRequestEvent,
		chanSet:       false,
	}
	instFsm.pAdaptFsm = NewAdapterFsm(aName, aDeviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw("UniPonAniConfigFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": aDeviceID})
		return nil
	}

	instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
		"disabled",
		fsm.Events{

			{Name: "start", Src: []string{"disabled"}, Dst: "starting"},

			//Note: .1p-Mapper and MBPCD might also have multi instances (per T-Cont) - by now only one 1 T-Cont considered!
			{Name: "start_config", Src: []string{"starting"}, Dst: "creatingDot1PMapper"},
			{Name: "rx_dot1pmap_cresp", Src: []string{"creatingDot1PMapper"}, Dst: "creatingMBPCD"},
			{Name: "rx_mbpcd_resp", Src: []string{"creatingMBPCD"}, Dst: "settingTconts"},
			{Name: "rx_tconts_resp", Src: []string{"settingTconts"}, Dst: "creatingGemNCTPs"},
			// the creatingGemNCTPs state is used for multi ME config if required for all configured/available GemPorts
			{Name: "rx_gemntcps_resp", Src: []string{"creatingGemNCTPs"}, Dst: "creatingGemIWs"},
			// the creatingGemIWs state is used for multi ME config if required for all configured/available GemPorts
			{Name: "rx_gemiws_resp", Src: []string{"creatingGemIWs"}, Dst: "settingPQs"},
			// the settingPQs state is used for multi ME config if required for all configured/available upstream PriorityQueues
			{Name: "rx_prioqs_resp", Src: []string{"settingPQs"}, Dst: "settingDot1PMapper"},
			{Name: "rx_dot1pmap_sresp", Src: []string{"settingDot1PMapper"}, Dst: "aniConfigDone"},

			{Name: "timeout_simple", Src: []string{
				"creatingDot1PMapper", "creatingMBPCD", "settingTconts", "settingDot1PMapper"}, Dst: "starting"},
			{Name: "timeout_mids", Src: []string{
				"creatingGemNCTPs", "creatingGemIWs", "settingPQs"}, Dst: "starting"},

			// exceptional treatment for all states except "resetting"
			{Name: "reset", Src: []string{"starting", "creatingDot1PMapper", "creatingMBPCD",
				"settingTconts", "creatingGemNCTPs", "creatingGemIWs", "settingPQs", "settingDot1PMapper",
				"aniConfigDone"}, Dst: "resetting"},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: "restart", Src: []string{"resetting"}, Dst: "disabled"},
		},

		fsm.Callbacks{
			"enter_state":               func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(e) },
			"enter_starting":            func(e *fsm.Event) { instFsm.enterConfigStartingState(e) },
			"enter_creatingDot1PMapper": func(e *fsm.Event) { instFsm.enterCreatingDot1PMapper(e) },
			"enter_creatingMBPCD":       func(e *fsm.Event) { instFsm.enterCreatingMBPCD(e) },
			"enter_settingTconts":       func(e *fsm.Event) { instFsm.enterSettingTconts(e) },
			"enter_creatingGemNCTPs":    func(e *fsm.Event) { instFsm.enterCreatingGemNCTPs(e) },
			"enter_creatingGemIWs":      func(e *fsm.Event) { instFsm.enterCreatingGemIWs(e) },
			"enter_settingPQs":          func(e *fsm.Event) { instFsm.enterSettingPQs(e) },
			"enter_settingDot1PMapper":  func(e *fsm.Event) { instFsm.enterSettingDot1PMapper(e) },
			"enter_aniConfigDone":       func(e *fsm.Event) { instFsm.enterAniConfigDone(e) },
			"enter_resetting":           func(e *fsm.Event) { instFsm.enterResettingState(e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw("UniPonAniConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": aDeviceID})
		return nil
	}

	logger.Infow("UniPonAniConfigFsm created", log.Fields{"device-id": aDeviceID})
	return instFsm
}

//SetFsmCompleteChannel sets the requested channel and channel result for transfer on success
func (oFsm *UniPonAniConfigFsm) SetFsmCompleteChannel(aChSuccess chan<- uint8, aProcStep uint8) {
	oFsm.chSuccess = aChSuccess
	oFsm.procStep = aProcStep
	oFsm.chanSet = true
}

func (oFsm *UniPonAniConfigFsm) enterConfigStartingState(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.pAdaptFsm.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if oFsm.omciMIdsResponseReceived == nil {
		oFsm.omciMIdsResponseReceived = make(chan bool)
		logger.Debug("UniPonAniConfigFsm - OMCI multiInstance RxChannel defined")
	} else {
		// as we may 're-use' this instance of FSM and the connected channel
		// make sure there is no 'lingering' request in the already existing channel:
		// (simple loop sufficient as we are the only receiver)
		for len(oFsm.omciMIdsResponseReceived) > 0 {
			<-oFsm.omciMIdsResponseReceived
		}
	}
	// start go routine for processing of LockState messages
	go oFsm.ProcessOmciAniMessages()

	//let the state machine run forward from here directly
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				//stick to pythonAdapter numbering scheme
				//index 0 in naming refers to possible usage of multiple instances (later)
				oFsm.mapperSP0ID = ieeeMapperServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo) + oFsm.techProfileID
				oFsm.macBPCD0ID = macBridgePortAniEID + uint16(oFsm.pOnuUniPort.entityId) + oFsm.techProfileID
				oFsm.tcont0ID = 0x8001 //TODO!: for now fixed, but target is to use value from MibUpload (mibDB)
				oFsm.alloc0ID = (*(oFsm.pUniTechProf.mapPonAniConfig[uint32(oFsm.pOnuUniPort.uniId)]))[0].tcontParams.allocID
				//TODO!! this is just for the first GemPort right now - needs update
				oFsm.gemPortXID = append(oFsm.gemPortXID,
					(*(oFsm.pUniTechProf.mapPonAniConfig[uint32(oFsm.pOnuUniPort.uniId)]))[0].mapGemPortParams[0].gemPortID)
				oFsm.upQueueXID = append(oFsm.upQueueXID, 0x8001) //TODO!: for now fixed, but target is to use value from MibUpload (mibDB)
				//TODO!: for now fixed, but target is to use value from MibUpload (mibDB), also TechProf setting dependency may exist!
				oFsm.downQueueXID = append(oFsm.downQueueXID, 1)

				a_pAFsm.pFsm.Event("start_config")
			}
		}(pConfigAniStateAFsm)
	}
}

func (oFsm *UniPonAniConfigFsm) enterCreatingDot1PMapper(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm Tx Create::Dot1PMapper", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	meInstance := oFsm.pOmciCC.sendCreateDot1PMapper(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.mapperSP0ID, oFsm.pAdaptFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance
}

func (oFsm *UniPonAniConfigFsm) enterCreatingMBPCD(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm Tx Create::MBPCD", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.macBPCD0ID), 16),
		"TPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	bridgePtr := macBridgeServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo) //cmp also omci_cc.go::sendCreateMBServiceProfile
	meParams := me.ParamData{
		EntityID: oFsm.macBPCD0ID,
		Attributes: me.AttributeValueMap{
			"BridgeIdPointer": bridgePtr,
			"PortNum":         0xFF, //fixed unique ANI side indication
			"TpType":          3,    //for .1PMapper
			"TpPointer":       oFsm.mapperSP0ID,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateMBPConfigDataVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance

}

func (oFsm *UniPonAniConfigFsm) enterSettingTconts(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm Tx Set::Tcont", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.tcont0ID), 16),
		"AllocId":   strconv.FormatInt(int64(oFsm.alloc0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	meParams := me.ParamData{
		EntityID: oFsm.tcont0ID,
		Attributes: me.AttributeValueMap{
			"AllocId": oFsm.alloc0ID,
		},
	}
	meInstance := oFsm.pOmciCC.sendSetTcontVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance
}

func (oFsm *UniPonAniConfigFsm) enterCreatingGemNCTPs(e *fsm.Event) {
	//TODO!! this is just for the first GemPort right now - needs update
	logger.Debugw("UniPonAniConfigFsm - start creating GemNWCtp loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	go oFsm.performCreatingGemNCTPs()
}

func (oFsm *UniPonAniConfigFsm) enterCreatingGemIWs(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm - start creating GemIwTP loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	go oFsm.performCreatingGemIWs()
}

func (oFsm *UniPonAniConfigFsm) enterSettingPQs(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm - start setting PrioQueue loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	go oFsm.performSettingPQs()
}

func (oFsm *UniPonAniConfigFsm) enterSettingDot1PMapper(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm Tx Set::.1pMapper with all PBits set", log.Fields{"EntitytId": 0x8042, /*cmp above*/
		"toGemIw": 1024 /* cmp above */, "in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})

	//TODO!! in MultiGemPort constellation the IwTpPtr setting will get variable -f(Prio) based on pUniTechProf
	logger.Debugw("UniPonAniConfigFsm Tx Set::1pMapper SingleGem", log.Fields{
		"EntitytId":  strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"GemIwTpPtr": strconv.FormatInt(int64(oFsm.gemPortXID[0]), 16),
		"in state":   e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	meParams := me.ParamData{
		EntityID: oFsm.mapperSP0ID,
		Attributes: me.AttributeValueMap{
			"InterworkTpPointerForPBitPriority0": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority1": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority2": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority3": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority4": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority5": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority6": oFsm.gemPortXID[0],
			"InterworkTpPointerForPBitPriority7": oFsm.gemPortXID[0],
		},
	}
	meInstance := oFsm.pOmciCC.sendSetDot1PMapperVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance
}

func (oFsm *UniPonAniConfigFsm) enterAniConfigDone(e *fsm.Event) {

	//mirror the chanSet state as it will be reset by FSM reset
	loChanSet := oFsm.chanSet

	//let's reset the state machine in order to release all resources now
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event("reset")
			}
		}(pConfigAniStateAFsm)
	}

	logger.Debugw("UniPonAniConfigFsm send dh event notification", log.Fields{
		"from_State": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	//use DeviceHandler event notification directly
	oFsm.pOmciCC.pBaseDeviceHandler.DeviceProcStatusUpdate(oFsm.requestEvent)

	if loChanSet {
		// indicate processing done to the caller
		logger.Debugw("UniPonAniConfigFsm processingDone on channel", log.Fields{
			"ProcessingStep": oFsm.procStep, "from_State": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
		oFsm.chSuccess <- oFsm.procStep
	}
}

func (oFsm *UniPonAniConfigFsm) enterResettingState(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm resetting", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
	oFsm.chanSet = false //reset the internal channel state
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := Message{
			Type: TestMsg,
			Data: TestMessage{
				TestMessageVal: AbortMessageProcessing,
			},
		}
		pConfigAniStateAFsm.commChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled', decouple event transfer
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event("restart")
			}
		}(pConfigAniStateAFsm)
	}
}

func (oFsm *UniPonAniConfigFsm) ProcessOmciAniMessages( /*ctx context.Context*/ ) {
	logger.Debugw("Start UniPonAniConfigFsm Msg processing", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.pAdaptFsm.deviceID})
		// 	break loop
		case message, ok := <-oFsm.pAdaptFsm.commChan:
			if !ok {
				logger.Info("UniPonAniConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
				// but then we have to ensure a restart of the FSM as well - as exceptional procedure
				oFsm.pAdaptFsm.pFsm.Event("reset")
				break loop
			}
			logger.Debugw("UniPonAniConfigFsm Rx Msg", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				if msg.TestMessageVal == AbortMessageProcessing {
					logger.Infow("UniPonAniConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
					break loop
				}
				logger.Warnw("UniPonAniConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID, "MessageVal": msg.TestMessageVal})
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				oFsm.handleOmciAniConfigMessage(msg)
			default:
				logger.Warn("UniPonAniConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID,
					"message.Type": message.Type})
			}
		}
	}
	logger.Infow("End UniPonAniConfigFsm Msg processing", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
}

func (oFsm *UniPonAniConfigFsm) handleOmciAniConfigMessage(msg OmciMessage) {
	logger.Debugw("Rx OMCI UniPonAniConfigFsm Msg", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID,
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
			logger.Debugw("CreateResponse Data", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityClass == oFsm.pOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pOmciCC.pLastTxMeInstance.GetEntityID() {
				//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
				// if, then something like:
				//oFsm.pOnuDB.StoreMe(msgObj)

				// maybe we can use just the same eventName for different state transitions like "forward"
				//   - might be checked, but so far I go for sure and have to inspect the concrete state events ...
				switch oFsm.pOmciCC.pLastTxMeInstance.GetName() {
				case "Ieee8021PMapperServiceProfile":
					{ // let the FSM proceed ...
						oFsm.pAdaptFsm.pFsm.Event("rx_dot1pmap_cresp")
					}
				case "MacBridgePortConfigurationData":
					{ // let the FSM proceed ...
						oFsm.pAdaptFsm.pFsm.Event("rx_mbpcd_resp")
					}
				case "GemPortNetworkCtp", "GemInterworkingTerminationPoint":
					{ // let aniConfig Multi-Id processing proceed by stopping the wait function
						oFsm.omciMIdsResponseReceived <- true
					}
				}
			}
		} //CreateResponseType
	case omci.SetResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
			if msgLayer == nil {
				logger.Error("UniPonAniConfigFsm - Omci Msg layer could not be detected for SetResponse")
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Error("UniPonAniConfigFsm - Omci Msg layer could not be assigned for SetResponse")
				return
			}
			logger.Debugw("UniPonAniConfigFsm SetResponse Data", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("UniPonAniConfigFsm - Omci SetResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityClass == oFsm.pOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pOmciCC.pLastTxMeInstance.GetEntityID() {
				//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
				// if, then something like:
				//oFsm.pOnuDB.StoreMe(msgObj)

				switch oFsm.pOmciCC.pLastTxMeInstance.GetName() {
				case "TCont":
					{ // let the FSM proceed ...
						oFsm.pAdaptFsm.pFsm.Event("rx_tconts_resp")
					}
				case "PriorityQueue":
					{ // let the PrioQueue init proceed by stopping the wait function
						oFsm.omciMIdsResponseReceived <- true
					}
				case "Ieee8021PMapperServiceProfile":
					{ // let the FSM proceed ...
						oFsm.pAdaptFsm.pFsm.Event("rx_dot1pmap_sresp")
					}
				}
			}
		} //SetResponseType
	default:
		{
			logger.Errorw("UniPonAniConfigFsm - Rx OMCI unhandled MsgType", log.Fields{"omciMsgType": msg.OmciMsg.MessageType})
			return
		}
	}
}

func (oFsm *UniPonAniConfigFsm) performCreatingGemNCTPs() {
	//TODO!! this is just for the first GemPort right now - needs update
	//   .. for gemPort in range gemPortXID
	logger.Infow("UniPonAniConfigFsm Tx Create::GemNWCtp", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.gemPortXID[0]), 16),
		"TcontId":   strconv.FormatInt(int64(oFsm.tcont0ID), 16),
		"device-id": oFsm.pAdaptFsm.deviceID})
	meParams := me.ParamData{
		EntityID: oFsm.gemPortXID[0],
		Attributes: me.AttributeValueMap{
			"PortId":       oFsm.gemPortXID[0], //same as EntityID
			"TContPointer": oFsm.tcont0ID,
			"Direction":    (*(oFsm.pUniTechProf.mapPonAniConfig[uint32(oFsm.pOnuUniPort.uniId)]))[0].mapGemPortParams[0].direction,
			//ONU-G.TrafficManagementOption dependency ->PrioQueue or TCont
			//  TODO!! verify dependency and QueueId in case of Multi-GemPort setup!
			"TrafficManagementPointerForUpstream": oFsm.upQueueXID[0], //might be different in wrr-only Setup - tcont0ID
			"PriorityQueuePointerForDownStream":   oFsm.downQueueXID[0],
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateGemNCTPVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance

	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("GemNWCtp create failed, aborting AniConfig FSM!",
			log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "GemIndex": 0}) //running index in loop later!
		oFsm.pAdaptFsm.pFsm.Event("reset")
		return
	}
	//for all GemPortID's ports - later

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("GemNWCtp create loop finished", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID})
	oFsm.pAdaptFsm.pFsm.Event("rx_gemntcps_resp")
	return
}

func (oFsm *UniPonAniConfigFsm) performCreatingGemIWs() {
	//TODO!! this is just for the first GemPort right now - needs update
	//   .. for gemPort in range gemPortXID
	logger.Infow("UniPonAniConfigFsm Tx Create::GemIwTp", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.gemPortXID[0]), 16),
		"SPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"device-id": oFsm.pAdaptFsm.deviceID})
	meParams := me.ParamData{
		EntityID: oFsm.gemPortXID[0],
		Attributes: me.AttributeValueMap{
			"GemPortNetworkCtpConnectivityPointer": oFsm.gemPortXID[0], //same as EntityID, see above
			"InterworkingOption":                   5,                  //fixed model:: G.998 .1pMapper
			"ServiceProfilePointer":                oFsm.mapperSP0ID,
			"InterworkingTerminationPointPointer":  0, //not used with .1PMapper Mac bridge
			"GalProfilePointer":                    galEthernetEID,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateGemIWTPVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance

	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("GemIwTp create failed, aborting AniConfig FSM!",
			log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "GemIndex": 0}) //running index in loop later!
		oFsm.pAdaptFsm.pFsm.Event("reset")
		return
	}
	//for all GemPortID's ports - later

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("GemIwTp create loop finished", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID})
	oFsm.pAdaptFsm.pFsm.Event("rx_gemiws_resp")
	return
}

func (oFsm *UniPonAniConfigFsm) performSettingPQs() {
	//TODO!! this is just for the first upstream PrioQueue right now - needs update
	//TODO!! implementation is restricted to WRR setting on the TrafficScheduler/Tcont
	//  SP setting would allow relatedPort(Prio) setting in case ONU supports config (ONU-2G QOS)

	//   .. for prioQueu in range upQueueXID
	weight := (*(oFsm.pUniTechProf.mapPonAniConfig[uint32(oFsm.pOnuUniPort.uniId)]))[0].mapGemPortParams[0].queueWeight
	logger.Infow("UniPonAniConfigFsm Tx Set::PrioQueue", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.upQueueXID[0]), 16),
		"Weight":    weight,
		"device-id": oFsm.pAdaptFsm.deviceID})
	meParams := me.ParamData{
		EntityID: oFsm.upQueueXID[0],
		Attributes: me.AttributeValueMap{
			"Weight": weight,
		},
	}
	meInstance := oFsm.pOmciCC.sendSetPrioQueueVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pOmciCC.pLastTxMeInstance = meInstance

	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("PrioQueue set failed, aborting AniConfig FSM!",
			log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "QueueIndex": 0}) //running index in loop later!
		oFsm.pAdaptFsm.pFsm.Event("reset")
		return
	}
	//for all upstream prioQueus - later

	// if Config has been done for all PrioQueue instances let the FSM proceed
	logger.Debugw("PrioQueue set loop finished", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID})
	oFsm.pAdaptFsm.pFsm.Event("rx_prioqs_resp")
	return
}

func (oFsm *UniPonAniConfigFsm) waitforOmciResponse() error {
	select {
	// maybe be also some outside cancel (but no context modelled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
	case <-time.After(30 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw("UniPonAniConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("UniPonAniConfigFsm multi entity timeout")
	case success := <-oFsm.omciMIdsResponseReceived:
		if success == true {
			logger.Debug("UniPonAniConfigFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("UniPonAniConfigFsm multi entity response error", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("UniPonAniConfigFsm multi entity responseError")
	}
}
