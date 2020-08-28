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
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	of "github.com/opencord/voltha-protos/v3/go/openflow_13"
)

const (
	// internal predefined values
	cDefaultDownstreamMode = 0
	cDefaultTpid           = 0x8100
)

const (
	// bit mask offsets for EVTOCD VlanTaggingOperationTable related to 32 bits (4 bytes)
	cFilterPrioOffset      = 28
	cFilterVidOffset       = 15
	cFilterTpidOffset      = 12
	cFilterEtherTypeOffset = 0
	cTreatTTROffset        = 30
	cTreatPrioOffset       = 16
	cTreatVidOffset        = 3
	cTreatTpidOffset       = 0
)
const (
	// byte offsets for EVTOCD VlanTaggingOperationTable related to overall 16 byte size with slice byte 0 as first Byte (MSB)
	cFilterOuterOffset = 0
	cFilterInnerOffset = 4
	cTreatOuterOffset  = 8
	cTreatInnerOffset  = 12
)
const (
	// basic values used within EVTOCD VlanTaggingOperationTable in respect to their bitfields
	cPrioIgnoreTag        uint32 = 15
	cPrioDefaultFilter    uint32 = 14
	cPrioDoNotFilter      uint32 = 8
	cDoNotFilterVid       uint32 = 4096
	cDoNotFilterTPID      uint32 = 0
	cDoNotFilterEtherType uint32 = 0
	cDoNotAddPrio         uint32 = 15
	cCopyPrioFromInner    uint32 = 8
	cDontCarePrio         uint32 = 0
	cDontCareVid          uint32 = 0
	cDontCareTpid         uint32 = 0
	cSetOutputTpidCopyDei uint32 = 4
)

const (
	// events of config PON ANI port FSM
	vlanEvStart          = "vlanEvStart"
	vlanEvWaitTechProf   = "vlanEvWaitTechProf"
	vlanEvContinueConfig = "vlanEvContinueConfig"
	vlanEvStartConfig    = "vlanEvStartConfig"
	vlanEvRxConfigVtfd   = "vlanEvRxConfigVtfd"
	vlanEvRxConfigEvtocd = "vlanEvRxConfigEvtocd"
	vlanEvCleanupConfig  = "vlanEvCleanupConfig"
	vlanEvRxCleanVtfd    = "vlanEvRxCleanVtfd"
	vlanEvRxCleanEvtocd  = "vlanEvRxCleanEvtocd"
	vlanEvTimeoutSimple  = "vlanEvTimeoutSimple"
	vlanEvTimeoutMids    = "vlanEvTimeoutMids"
	vlanEvReset          = "vlanEvReset"
	vlanEvRestart        = "vlanEvRestart"
)
const (
	// states of config PON ANI port FSM
	vlanStDisabled        = "vlanStDisabled"
	vlanStStarting        = "vlanStStarting"
	vlanStWaitingTechProf = "vlanStWaitingTechProf"
	vlanStConfigVtfd      = "vlanStConfigVtfd"
	vlanStConfigEvtocd    = "vlanStConfigEvtocd"
	vlanStConfigDone      = "vlanStConfigDone"
	vlanStCleanEvtocd     = "vlanStCleanEvtocd"
	vlanStCleanVtfd       = "vlanStCleanVtfd"
	vlanStCleanupDone     = "vlanStCleanupDone"
	vlanStResetting       = "vlanStResetting"
)

//UniVlanConfigFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type UniVlanConfigFsm struct {
	pDeviceHandler              *DeviceHandler
	pOmciCC                     *OmciCC
	pOnuUniPort                 *OnuUniPort
	pUniTechProf                *OnuUniTechProf
	pOnuDB                      *OnuDeviceDB
	techProfileID               uint16
	requestEvent                OnuDeviceEvent
	omciMIdsResponseReceived    chan bool //seperate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                   *AdapterFsm
	acceptIncrementalEvtoOption bool
	//use uint32 types for allowing immediate bitshifting
	matchVid     uint32
	matchPcp     uint32
	tagsToRemove uint32
	setVid       uint32
	setPcp       uint32
	vtfdID       uint16
	evtocdID     uint16
}

//NewUniVlanConfigFsm is the 'constructor' for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
func NewUniVlanConfigFsm(apDeviceHandler *DeviceHandler, apDevOmciCC *OmciCC, apUniPort *OnuUniPort, apUniTechProf *OnuUniTechProf,
	apOnuDB *OnuDeviceDB, aTechProfileID uint16, aRequestEvent OnuDeviceEvent, aName string,
	aDeviceID string, aCommChannel chan Message,
	aAcceptIncrementalEvto bool, aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8) *UniVlanConfigFsm {
	instFsm := &UniVlanConfigFsm{
		pDeviceHandler:              apDeviceHandler,
		pOmciCC:                     apDevOmciCC,
		pOnuUniPort:                 apUniPort,
		pUniTechProf:                apUniTechProf,
		pOnuDB:                      apOnuDB,
		techProfileID:               aTechProfileID,
		requestEvent:                aRequestEvent,
		acceptIncrementalEvtoOption: aAcceptIncrementalEvto,
		matchVid:                    uint32(aMatchVlan),
		setVid:                      uint32(aSetVlan),
		setPcp:                      uint32(aSetPcp),
	}
	// some automatic adjustments on the filter/treat parameters as not specifically configured/ensured by flow configuration parameters
	instFsm.tagsToRemove = 1            //one tag to remove as default setting
	instFsm.matchPcp = cPrioDoNotFilter // do not Filter on prio as default
	if instFsm.matchVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// no prio/vid filtering requested
		instFsm.tagsToRemove = 0          //no tag pop action
		instFsm.matchPcp = cPrioIgnoreTag // no vlan tag filtering
		if instFsm.setPcp == cCopyPrioFromInner {
			//in case of no filtering and configured PrioCopy ensure default prio setting to 0
			// which is required for stacking of untagged, but obviously also ensures prio setting for prio/singletagged
			// might collide with NoMatchVid/CopyPrio(/setVid) setting
			// this was some precondition setting taken over from py adapter ..
			instFsm.setPcp = 0
		}
	}

	instFsm.pAdaptFsm = NewAdapterFsm(aName, aDeviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw("UniVlanConfigFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": aDeviceID})
		return nil
	}

	instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
		vlanStDisabled,
		fsm.Events{
			{Name: vlanEvStart, Src: []string{vlanStDisabled}, Dst: vlanStStarting},
			{Name: vlanEvWaitTechProf, Src: []string{vlanStStarting}, Dst: vlanStWaitingTechProf},
			{Name: vlanEvContinueConfig, Src: []string{vlanStWaitingTechProf}, Dst: vlanStConfigVtfd},
			{Name: vlanEvStartConfig, Src: []string{vlanStStarting}, Dst: vlanStConfigVtfd},
			{Name: vlanEvRxConfigVtfd, Src: []string{vlanStConfigVtfd}, Dst: vlanStConfigEvtocd},
			{Name: vlanEvRxConfigEvtocd, Src: []string{vlanStConfigEvtocd}, Dst: vlanStConfigDone},
			//TODO:!!! Also define state transitions for cleanup states and timeouts
			/*
				{Name: vlanEvTimeoutSimple, Src: []string{
					vlanStCreatingDot1PMapper, vlanStCreatingMBPCD, vlanStSettingTconts, vlanStSettingDot1PMapper}, Dst: vlanStStarting},
				{Name: vlanEvTimeoutMids, Src: []string{
					vlanStCreatingGemNCTPs, vlanStCreatingGemIWs, vlanStSettingPQs}, Dst: vlanStStarting},
			*/
			// exceptional treatment for all states except vlanStResetting
			{Name: vlanEvReset, Src: []string{vlanStStarting, vlanStWaitingTechProf,
				vlanStConfigVtfd, vlanStConfigEvtocd, vlanStConfigDone,
				vlanStCleanEvtocd, vlanStCleanVtfd, vlanStCleanupDone},
				Dst: vlanStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: vlanEvRestart, Src: []string{vlanStResetting}, Dst: vlanStDisabled},
		},

		fsm.Callbacks{
			"enter_state":                   func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(e) },
			("enter_" + vlanStStarting):     func(e *fsm.Event) { instFsm.enterConfigStarting(e) },
			("enter_" + vlanStConfigVtfd):   func(e *fsm.Event) { instFsm.enterConfigVtfd(e) },
			("enter_" + vlanStConfigEvtocd): func(e *fsm.Event) { instFsm.enterConfigEvtocd(e) },
			("enter_" + vlanStConfigDone):   func(e *fsm.Event) { instFsm.enterVlanConfigDone(e) },
			("enter_" + vlanStCleanVtfd):    func(e *fsm.Event) { instFsm.enterCleanVtfd(e) },
			("enter_" + vlanStCleanEvtocd):  func(e *fsm.Event) { instFsm.enterCleanEvtocd(e) },
			("enter_" + vlanStCleanupDone):  func(e *fsm.Event) { instFsm.enterVlanCleanupDone(e) },
			("enter_" + vlanStResetting):    func(e *fsm.Event) { instFsm.enterResetting(e) },
			("enter_" + vlanStDisabled):     func(e *fsm.Event) { instFsm.enterDisabled(e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw("UniVlanConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": aDeviceID})
		return nil
	}

	logger.Infow("UniVlanConfigFsm created", log.Fields{"device-id": aDeviceID,
		"accIncrEvto": instFsm.acceptIncrementalEvtoOption,
		"matchVid":    strconv.FormatInt(int64(instFsm.matchVid), 16),
		"setVid":      strconv.FormatInt(int64(instFsm.setVid), 16),
		"setPcp":      instFsm.setPcp})
	return instFsm
}

func (oFsm *UniVlanConfigFsm) enterConfigStarting(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.pAdaptFsm.deviceID})

	// this FSM is not intended for re-start, needs always new creation for a new run
	oFsm.omciMIdsResponseReceived = make(chan bool)
	// start go routine for processing of LockState messages
	go oFsm.processOmciVlanMessages()
	//let the state machine run forward from here directly
	pConfigVlanStateAFsm := oFsm.pAdaptFsm
	if pConfigVlanStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				//stick to pythonAdapter numbering scheme
				oFsm.vtfdID = macBridgePortAniEID + oFsm.pOnuUniPort.entityId + oFsm.techProfileID
				//cmp also usage in EVTOCDE create in omci_cc
				oFsm.evtocdID = macBridgeServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo)

				if oFsm.pUniTechProf.getTechProfileDone(oFsm.pOnuUniPort.uniId, oFsm.techProfileID) {
					// let the vlan processing begin
					a_pAFsm.pFsm.Event(vlanEvStartConfig)
				} else {
					// set to waiting for Techprofile
					a_pAFsm.pFsm.Event(vlanEvWaitTechProf)
				}
			}
		}(pConfigVlanStateAFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigVtfd(e *fsm.Event) {
	if oFsm.setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		logger.Debugw("UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
		// let the FSM proceed ... (from within this state all internal pointers may be expected to be correct)
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		pConfigVlanStateAFsm := oFsm.pAdaptFsm
		go func(a_pAFsm *AdapterFsm) {
			a_pAFsm.pFsm.Event(vlanEvRxConfigVtfd)
		}(pConfigVlanStateAFsm)
	} else {
		logger.Debugw("UniVlanConfigFsm create VTFD", log.Fields{
			"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
			"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
		vlanFilterList := make([]uint16, 12)
		vlanFilterList[0] = uint16(oFsm.setVid) // setVid is assumed to be masked already by the caller to 12 bit
		meParams := me.ParamData{
			EntityID: oFsm.vtfdID,
			Attributes: me.AttributeValueMap{
				"VlanFilterList":   vlanFilterList,
				"ForwardOperation": uint8(0x10), //VID investigation
				"NumberOfEntries":  uint8(1),
			},
		}
		meInstance := oFsm.pOmciCC.sendCreateVtfdVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
		//  send shall return (dual format) error code that can be used here for immediate error treatment
		//  (relevant to all used sendXX() methods in this (and other) FSM's)
		oFsm.pOmciCC.pLastTxMeInstance = meInstance
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigEvtocd(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - start config EVTOCD loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	go oFsm.performConfigEvtocdEntries()
}

func (oFsm *UniVlanConfigFsm) enterVlanConfigDone(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - VLAN config done: send dh event notification", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	if oFsm.pDeviceHandler != nil {
		oFsm.pDeviceHandler.DeviceProcStatusUpdate(oFsm.requestEvent)
	}
}

func (oFsm *UniVlanConfigFsm) enterCleanVtfd(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm Tx Delete::VTFD", log.Fields{
		/*"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),*/
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) enterCleanEvtocd(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm  cleanup EVTOCD", log.Fields{
		/*"EntitytId": strconv.FormatInt(int64(oFsm.macBPCD0ID), 16),
		"TPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),*/
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) enterVlanCleanupDone(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - VLAN cleanup done", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})

	//let's reset the state machine in order to release all resources now
	pConfigVlanStateAFsm := oFsm.pAdaptFsm
	if pConfigVlanStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event(vlanEvReset)
			}
		}(pConfigVlanStateAFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterResetting(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm resetting", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})

	pConfigVlanStateAFsm := oFsm.pAdaptFsm
	if pConfigVlanStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := Message{
			Type: TestMsg,
			Data: TestMessage{
				TestMessageVal: AbortMessageProcessing,
			},
		}
		pConfigVlanStateAFsm.commChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled', decouple event transfer
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				a_pAFsm.pFsm.Event(vlanEvRestart)
			}
		}(pConfigVlanStateAFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterDisabled(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm enters disabled state", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
	if oFsm.pDeviceHandler != nil {
		//request removal of 'reference' in the Handler (completely clear the FSM)
		go oFsm.pDeviceHandler.RemoveVlanFilterFsm(oFsm.pOnuUniPort)
	}
}

func (oFsm *UniVlanConfigFsm) processOmciVlanMessages() { //ctx context.Context?
	logger.Debugw("Start UniVlanConfigFsm Msg processing", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.pAdaptFsm.deviceID})
		// 	break loop
		case message, ok := <-oFsm.pAdaptFsm.commChan:
			if !ok {
				logger.Info("UniVlanConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
				// but then we have to ensure a restart of the FSM as well - as exceptional procedure
				oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
				break loop
			}
			logger.Debugw("UniVlanConfigFsm Rx Msg", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				if msg.TestMessageVal == AbortMessageProcessing {
					logger.Infow("UniVlanConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
					break loop
				}
				logger.Warnw("UniVlanConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID, "MessageVal": msg.TestMessageVal})
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				oFsm.handleOmciVlanConfigMessage(msg)
			default:
				logger.Warn("UniVlanConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID,
					"message.Type": message.Type})
			}
		}
	}
	logger.Infow("End UniVlanConfigFsm Msg processing", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) handleOmciVlanConfigMessage(msg OmciMessage) {
	logger.Debugw("Rx OMCI UniVlanConfigFsm Msg", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID,
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
			logger.Debugw("CreateResponse Data", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityClass == oFsm.pOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pOmciCC.pLastTxMeInstance.GetEntityID() {
				// maybe we can use just the same eventName for different state transitions like "forward"
				//   - might be checked, but so far I go for sure and have to inspect the concrete state events ...
				switch oFsm.pOmciCC.pLastTxMeInstance.GetName() {
				case "VlanTaggingFilterData":
					{ // let the FSM proceed ...
						oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigVtfd)
					}
				}
			}
		} //CreateResponseType
	case omci.SetResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
			if msgLayer == nil {
				logger.Error("UniVlanConfigFsm - Omci Msg layer could not be detected for SetResponse")
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Error("UniVlanConfigFsm - Omci Msg layer could not be assigned for SetResponse")
				return
			}
			logger.Debugw("UniVlanConfigFsm SetResponse Data", log.Fields{"deviceId": oFsm.pAdaptFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("UniVlanConfigFsm - Omci SetResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityClass == oFsm.pOmciCC.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pOmciCC.pLastTxMeInstance.GetEntityID() {
				switch oFsm.pOmciCC.pLastTxMeInstance.GetName() {
				case "ExtendedVlanTaggingOperationConfigurationData":
					{ // let the EVTO config proceed by stopping the wait function
						oFsm.omciMIdsResponseReceived <- true
					}
				}
			}
		} //SetResponseType
	default:
		{
			logger.Errorw("UniVlanConfigFsm - Rx OMCI unhandled MsgType", log.Fields{"omciMsgType": msg.OmciMsg.MessageType})
			return
		}
	}
}

func (oFsm *UniVlanConfigFsm) performConfigEvtocdEntries() {
	{ // for local var
		// EVTOCD ME is expected to exist at this point already from MIB-Download (with AssociationType/Pointer)
		// we need to extend the configuration by EthType definition and, to be sure, downstream 'inverse' mode
		logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD", log.Fields{
			"EntitytId":  strconv.FormatInt(int64(oFsm.evtocdID), 16),
			"i/oEthType": strconv.FormatInt(int64(cDefaultTpid), 16),
			"device-id":  oFsm.pAdaptFsm.deviceID})
		meParams := me.ParamData{
			EntityID: oFsm.evtocdID,
			Attributes: me.AttributeValueMap{
				"InputTpid":      uint16(cDefaultTpid), //could be possibly retrieved from flow config one day, by now just like py-code base
				"OutputTpid":     uint16(cDefaultTpid), //could be possibly retrieved from flow config one day, by now just like py-code base
				"DownstreamMode": uint8(cDefaultDownstreamMode),
			},
		}
		meInstance := oFsm.pOmciCC.sendSetEvtocdVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pOmciCC.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("Evtocd set TPID failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
			oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return
		}
	} //for local var

	if oFsm.setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission required
		logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD single tagged transparent rule", log.Fields{
			"device-id": oFsm.pAdaptFsm.deviceID})
		sliceEvtocdRule := make([]uint8, 16)
		// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
		binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
			cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
				cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
				cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

		binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
			cPrioDefaultFilter<<cFilterPrioOffset| // default inner-tag rule
				cDoNotFilterVid<<cFilterVidOffset| // Do not filter on inner vid
				cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
				cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

		binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
			0<<cTreatTTROffset| // Do not pop any tags
				cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
				cDontCareVid<<cTreatVidOffset| // Outer VID don't care
				cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

		binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
			cDoNotAddPrio<<cTreatPrioOffset| // do not add inner tag
				cDontCareVid<<cTreatVidOffset| // Outer VID don't care
				cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

		meParams := me.ParamData{
			EntityID: oFsm.evtocdID,
			Attributes: me.AttributeValueMap{
				"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
			},
		}
		meInstance := oFsm.pOmciCC.sendSetEvtocdVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pOmciCC.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("Evtocd set transparent singletagged rule failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
			oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return
		}
	} else {
		// according to py-code acceptIncrementalEvto program option decides upon stacking or translation scenario
		if oFsm.acceptIncrementalEvtoOption {
			// this defines VID translation scenario: singletagged->singletagged (if not transparent)
			logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD single tagged translation rule", log.Fields{
				"device-id": oFsm.pAdaptFsm.deviceID})
			sliceEvtocdRule := make([]uint8, 16)
			// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
				cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
					cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
					cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
				oFsm.matchPcp<<cFilterPrioOffset| // either DNFonPrio or ignore tag (default) on innerVLAN
					oFsm.matchVid<<cFilterVidOffset| // either DNFonVid or real filter VID
					cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
					cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
				oFsm.tagsToRemove<<cTreatTTROffset| // either 1 or 0
					cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
					cDontCareVid<<cTreatVidOffset| // Outer VID don't care
					cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
				oFsm.setPcp<<cTreatPrioOffset| // as configured in flow
					oFsm.setVid<<cTreatVidOffset| //as configured in flow
					cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

			meParams := me.ParamData{
				EntityID: oFsm.evtocdID,
				Attributes: me.AttributeValueMap{
					"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
				},
			}
			meInstance := oFsm.pOmciCC.sendSetEvtocdVar(context.TODO(), ConstDefaultOmciTimeout, true,
				oFsm.pAdaptFsm.commChan, meParams)
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			oFsm.pOmciCC.pLastTxMeInstance = meInstance

			//verify response
			err := oFsm.waitforOmciResponse()
			if err != nil {
				logger.Errorw("Evtocd set singletagged translation rule failed, aborting VlanConfig FSM!",
					log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
				oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
				return
			}
		} else {
			//not transparent and not acceptIncrementalEvtoOption untagged/priotagged->singletagged
			{ // just for local var's
				// this defines stacking scenario: untagged->singletagged
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD untagged->singletagged rule", log.Fields{
					"device-id": oFsm.pAdaptFsm.deviceID})
				sliceEvtocdRule := make([]uint8, 16)
				// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
				binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
					cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
						cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
						cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

				binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
					cPrioIgnoreTag<<cFilterPrioOffset| // Not an inner-tag rule
						cDoNotFilterVid<<cFilterVidOffset| // Do not filter on inner vid
						cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
						cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
					0<<cTreatTTROffset| // Do not pop any tags
						cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
						cDontCareVid<<cTreatVidOffset| // Outer VID don't care
						cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
					0<<cTreatPrioOffset| // vlan prio set to 0
						//   (as done in Py code, maybe better option would be setPcp here, which still could be 0?)
						oFsm.setVid<<cTreatVidOffset| // Outer VID don't care
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

				meParams := me.ParamData{
					EntityID: oFsm.evtocdID,
					Attributes: me.AttributeValueMap{
						"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
					},
				}
				meInstance := oFsm.pOmciCC.sendSetEvtocdVar(context.TODO(), ConstDefaultOmciTimeout, true,
					oFsm.pAdaptFsm.commChan, meParams)
				//accept also nil as (error) return value for writing to LastTx
				//  - this avoids misinterpretation of new received OMCI messages
				oFsm.pOmciCC.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd set untagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
					oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} //just for local var's
			{ // just for local var's
				// this defines 'stacking' scenario: priotagged->singletagged
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD priotagged->singletagged rule", log.Fields{
					"device-id": oFsm.pAdaptFsm.deviceID})
				sliceEvtocdRule := make([]uint8, 16)
				// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
				binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
					cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
						cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
						cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

				binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
					cPrioDoNotFilter<<cFilterPrioOffset| // Do not Filter on innerprio
						0<<cFilterVidOffset| // filter on inner vid 0 (prioTagged)
						cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
						cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
					1<<cTreatTTROffset| // pop the prio-tag
						cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
						cDontCareVid<<cTreatVidOffset| // Outer VID don't care
						cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
					cCopyPrioFromInner<<cTreatPrioOffset| // vlan copy from PrioTag
						//   (as done in Py code, maybe better option would be setPcp here, which still could be PrioCopy?)
						oFsm.setVid<<cTreatVidOffset| // Outer VID as configured
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

				meParams := me.ParamData{
					EntityID: oFsm.evtocdID,
					Attributes: me.AttributeValueMap{
						"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
					},
				}
				meInstance := oFsm.pOmciCC.sendSetEvtocdVar(context.TODO(), ConstDefaultOmciTimeout, true,
					oFsm.pAdaptFsm.commChan, meParams)
				//accept also nil as (error) return value for writing to LastTx
				//  - this avoids misinterpretation of new received OMCI messages
				oFsm.pOmciCC.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd set priotagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
					oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} //just for local var's
		}
	}

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("EVTOCD set loop finished", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
	oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigEvtocd)
	return
}

func (oFsm *UniVlanConfigFsm) waitforOmciResponse() error {
	select {
	// maybe be also some outside cancel (but no context modelled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
	case <-time.After(30 * time.Second): //AS FOR THE OTHER OMCI FSM's
		logger.Warnw("UniVlanConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("UniVlanConfigFsm multi entity timeout")
	case success := <-oFsm.omciMIdsResponseReceived:
		if success == true {
			logger.Debug("UniVlanConfigFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("UniVlanConfigFsm multi entity response error", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("UniVlanConfigFsm multi entity responseError")
	}
}
