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
	"sync"
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
	cMaxAllowedFlows       = 12 //which might be under discussion, for the moment connected to limit of VLAN's within VTFD
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
	//cDontCarePrio         uint32 = 0
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
	vlanEvIncrFlowConfig = "vlanEvIncrFlowConfig"
	//vlanEvCleanupConfig  = "vlanEvCleanupConfig"
	//vlanEvRxCleanVtfd    = "vlanEvRxCleanVtfd"
	//vlanEvRxCleanEvtocd  = "vlanEvRxCleanEvtocd"
	//vlanEvTimeoutSimple  = "vlanEvTimeoutSimple"
	//vlanEvTimeoutMids    = "vlanEvTimeoutMids"
	vlanEvReset   = "vlanEvReset"
	vlanEvRestart = "vlanEvRestart"
)
const (
	// states of config PON ANI port FSM
	vlanStDisabled        = "vlanStDisabled"
	vlanStStarting        = "vlanStStarting"
	vlanStWaitingTechProf = "vlanStWaitingTechProf"
	vlanStConfigVtfd      = "vlanStConfigVtfd"
	vlanStConfigEvtocd    = "vlanStConfigEvtocd"
	vlanStConfigDone      = "vlanStConfigDone"
	vlanStConfigIncrFlow  = "vlanStConfigIncrFlow"
	vlanStCleanEvtocd     = "vlanStCleanEvtocd"
	vlanStCleanVtfd       = "vlanStCleanVtfd"
	vlanStCleanupDone     = "vlanStCleanupDone"
	vlanStResetting       = "vlanStResetting"
)

type uniVlanFlowParameter struct {
	//use uint32 types for allowing immediate bitshifting
	matchVid     uint32
	matchPcp     uint32
	tagsToRemove uint32
	setVid       uint32
	setPcp       uint32
}

//UniVlanConfigFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type UniVlanConfigFsm struct {
	pDeviceHandler              *deviceHandler
	pOmciCC                     *omciCC
	pOnuUniPort                 *onuUniPort
	pUniTechProf                *onuUniTechProf
	pOnuDB                      *onuDeviceDB
	techProfileID               uint16
	requestEvent                OnuDeviceEvent
	omciMIdsResponseReceived    chan bool //seperate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                   *AdapterFsm
	acceptIncrementalEvtoOption bool
	mutexFlowParams             sync.Mutex
	uniFlowParamsSlice          []uniVlanFlowParameter
	numUniFlows                 uint8 // expected number of flows should be less than 12
	configuredUniFlow           uint8
	numVlanFilterEntries        uint8
	vlanFilterList              [12]uint16
	vtfdID                      uint16
	evtocdID                    uint16
}

//NewUniVlanConfigFsm is the 'constructor' for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
func NewUniVlanConfigFsm(apDeviceHandler *deviceHandler, apDevOmciCC *omciCC, apUniPort *onuUniPort, apUniTechProf *onuUniTechProf,
	apOnuDB *onuDeviceDB, aTechProfileID uint16, aRequestEvent OnuDeviceEvent, aName string,
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
		numUniFlows:                 0,
		configuredUniFlow:           0,
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
			{Name: vlanEvRxConfigEvtocd, Src: []string{vlanStConfigEvtocd, vlanStConfigIncrFlow},
				Dst: vlanStConfigDone},
			{Name: vlanEvIncrFlowConfig, Src: []string{vlanStConfigDone}, Dst: vlanStConfigIncrFlow},
			/*
				{Name: vlanEvTimeoutSimple, Src: []string{
					vlanStCreatingDot1PMapper, vlanStCreatingMBPCD, vlanStSettingTconts, vlanStSettingDot1PMapper}, Dst: vlanStStarting},
				{Name: vlanEvTimeoutMids, Src: []string{
					vlanStCreatingGemNCTPs, vlanStCreatingGemIWs, vlanStSettingPQs}, Dst: vlanStStarting},
			*/
			// exceptional treatment for all states except vlanStResetting
			{Name: vlanEvReset, Src: []string{vlanStStarting, vlanStWaitingTechProf,
				vlanStConfigVtfd, vlanStConfigEvtocd, vlanStConfigDone, vlanStConfigIncrFlow,
				vlanStCleanEvtocd, vlanStCleanVtfd, vlanStCleanupDone},
				Dst: vlanStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: vlanEvRestart, Src: []string{vlanStResetting}, Dst: vlanStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                     func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(e) },
			("enter_" + vlanStStarting):       func(e *fsm.Event) { instFsm.enterConfigStarting(e) },
			("enter_" + vlanStConfigVtfd):     func(e *fsm.Event) { instFsm.enterConfigVtfd(e) },
			("enter_" + vlanStConfigEvtocd):   func(e *fsm.Event) { instFsm.enterConfigEvtocd(e) },
			("enter_" + vlanStConfigDone):     func(e *fsm.Event) { instFsm.enterVlanConfigDone(e) },
			("enter_" + vlanStConfigIncrFlow): func(e *fsm.Event) { instFsm.enterConfigIncrFlow(e) },
			("enter_" + vlanStCleanVtfd):      func(e *fsm.Event) { instFsm.enterCleanVtfd(e) },
			("enter_" + vlanStCleanEvtocd):    func(e *fsm.Event) { instFsm.enterCleanEvtocd(e) },
			("enter_" + vlanStCleanupDone):    func(e *fsm.Event) { instFsm.enterVlanCleanupDone(e) },
			("enter_" + vlanStResetting):      func(e *fsm.Event) { instFsm.enterResetting(e) },
			("enter_" + vlanStDisabled):       func(e *fsm.Event) { instFsm.enterDisabled(e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw("UniVlanConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": aDeviceID})
		return nil
	}

	_ = instFsm.SetUniFlowParams(aMatchVlan, aSetVlan, aSetPcp)

	logger.Infow("UniVlanConfigFsm created", log.Fields{"device-id": aDeviceID,
		"accIncrEvto": instFsm.acceptIncrementalEvtoOption})
	return instFsm
}

//SetUniFlowParams verifies on existence of flow parameters to be configured
// and appends a new flow if there is space
func (oFsm *UniVlanConfigFsm) SetUniFlowParams(aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8) error {
	loFlowParams := uniVlanFlowParameter{
		matchVid: uint32(aMatchVlan),
		setVid:   uint32(aSetVlan),
		setPcp:   uint32(aSetPcp),
	}
	// some automatic adjustments on the filter/treat parameters as not specifically configured/ensured by flow configuration parameters
	loFlowParams.tagsToRemove = 1            //one tag to remove as default setting
	loFlowParams.matchPcp = cPrioDoNotFilter // do not Filter on prio as default

	if loFlowParams.setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
		loFlowParams.matchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		//TODO!!: maybe be needed to be re-checked at flow deletion (but assume all flows are always deleted togehther)
	} else {
		if !oFsm.acceptIncrementalEvtoOption {
			//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
			loFlowParams.matchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		}
	}

	if loFlowParams.matchVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// no prio/vid filtering requested
		loFlowParams.tagsToRemove = 0          //no tag pop action
		loFlowParams.matchPcp = cPrioIgnoreTag // no vlan tag filtering
		if loFlowParams.setPcp == cCopyPrioFromInner {
			//in case of no filtering and configured PrioCopy ensure default prio setting to 0
			// which is required for stacking of untagged, but obviously also ensures prio setting for prio/singletagged
			// might collide with NoMatchVid/CopyPrio(/setVid) setting
			// this was some precondition setting taken over from py adapter ..
			loFlowParams.setPcp = 0
		}
	}
	flowEntryMatch := false
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	for _, storedUniFlowParams := range oFsm.uniFlowParamsSlice {
		if storedUniFlowParams == loFlowParams {
			flowEntryMatch = true
			break
		}
	}
	if flowEntryMatch {
		logger.Debugw("UniVlanConfigFsm flow setting - flow already exists (ignore)", log.Fields{
			"device-id": oFsm.pAdaptFsm.deviceID})
	} else {
		if oFsm.numUniFlows < cMaxAllowedFlows {
			oFsm.uniFlowParamsSlice = append(oFsm.uniFlowParamsSlice, loFlowParams)
			oFsm.numUniFlows++
			logger.Debugw("UniVlanConfigFsm flow added", log.Fields{
				"matchVid": strconv.FormatInt(int64(loFlowParams.matchVid), 16),
				"setVid":   strconv.FormatInt(int64(loFlowParams.setVid), 16),
				"setPcp":   loFlowParams.setPcp, "numberofFlows": oFsm.numUniFlows,
				"device-id": oFsm.pAdaptFsm.deviceID})
			pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
			if pConfigVlanStateBaseFsm.Is(vlanStConfigDone) {
				//have to re-trigger the FSM to proceed with outstanding incremental flow configuration
				// calling some FSM event must be decoupled
				go func(a_pBaseFsm *fsm.FSM) {
					_ = a_pBaseFsm.Event(vlanEvIncrFlowConfig)
				}(pConfigVlanStateBaseFsm)
			} // in all other states a new entry will be automatically considered later in that state or
			// ignored as not anymore relevant
		} else {
			logger.Errorw("UniVlanConfigFsm flow limit exceeded", log.Fields{
				"device-id": oFsm.pAdaptFsm.deviceID})
			return errors.New(" UniVlanConfigFsm flow limit exceeded")
		}
	}
	return nil
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
				oFsm.vtfdID = macBridgePortAniEID + oFsm.pOnuUniPort.entityID + oFsm.techProfileID
				//cmp also usage in EVTOCDE create in omci_cc
				oFsm.evtocdID = macBridgeServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo)

				if oFsm.pUniTechProf.getTechProfileDone(oFsm.pOnuUniPort.uniID, oFsm.techProfileID) {
					// let the vlan processing begin
					_ = a_pAFsm.pFsm.Event(vlanEvStartConfig)
				} else {
					// set to waiting for Techprofile
					_ = a_pAFsm.pFsm.Event(vlanEvWaitTechProf)
				}
			}
		}(pConfigVlanStateAFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigVtfd(e *fsm.Event) {
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	if oFsm.uniFlowParamsSlice[0].setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw("UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
		// let the FSM proceed ... (from within this state all internal pointers may be expected to be correct)
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		pConfigVlanStateAFsm := oFsm.pAdaptFsm
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvRxConfigVtfd)
		}(pConfigVlanStateAFsm)
	} else {
		logger.Debugw("UniVlanConfigFsm create VTFD", log.Fields{
			"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
			"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
		oFsm.vlanFilterList[0] = uint16(oFsm.uniFlowParamsSlice[0].setVid) // setVid is assumed to be masked already by the caller to 12 bit
		oFsm.mutexFlowParams.Unlock()
		vtfdFilterList := make([]uint16, 12) //needed for parameter serialization
		vtfdFilterList[0] = oFsm.vlanFilterList[0]
		oFsm.numVlanFilterEntries = 1
		meParams := me.ParamData{
			EntityID: oFsm.vtfdID,
			Attributes: me.AttributeValueMap{
				"VlanFilterList":   vtfdFilterList, //omci lib wants a slice for serialization
				"ForwardOperation": uint8(0x10),    //VID investigation
				"NumberOfEntries":  oFsm.numVlanFilterEntries,
			},
		}
		logger.Debugw("UniVlanConfigFsm sendcreate VTFD", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
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
	go oFsm.performConfigEvtocdEntries(0)
}

func (oFsm *UniVlanConfigFsm) enterVlanConfigDone(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - checking on more flows", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	oFsm.configuredUniFlow++ // one (more) flow configured
	if oFsm.numUniFlows > oFsm.configuredUniFlow {
		//some further flows are to be configured
		// calling some FSM event must be decoupled
		pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(vlanEvIncrFlowConfig)
		}(pConfigVlanStateBaseFsm)
		return
	}

	logger.Debugw("UniVlanConfigFsm - VLAN config done: send dh event notification", log.Fields{
		"device-id": oFsm.pAdaptFsm.deviceID})
	// it might appear that some flows are requested also after 'flowPushed' event has been generated ...
	// state transition notification is checked in deviceHandler
	if oFsm.pDeviceHandler != nil {
		oFsm.pDeviceHandler.deviceProcStatusUpdate(oFsm.requestEvent)
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigIncrFlow(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - start config further incremental flow", log.Fields{
		"in state": e.FSM.Current(), "recent flow-number": (oFsm.configuredUniFlow),
		"device-id": oFsm.pAdaptFsm.deviceID})
	oFsm.mutexFlowParams.Lock()

	if oFsm.uniFlowParamsSlice[oFsm.configuredUniFlow].setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw("UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
	} else {
		if oFsm.numVlanFilterEntries == 0 {
			//no VTFD yet created
			logger.Debugw("UniVlanConfigFsm create VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
				"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
			oFsm.vlanFilterList[0] = uint16(oFsm.uniFlowParamsSlice[oFsm.configuredUniFlow].setVid) // setVid is assumed to be masked already by the caller to 12 bit
			oFsm.mutexFlowParams.Unlock()
			vtfdFilterList := make([]uint16, 12) //needed for parameter serialization
			vtfdFilterList[0] = oFsm.vlanFilterList[0]
			oFsm.numVlanFilterEntries = 1
			meParams := me.ParamData{
				EntityID: oFsm.vtfdID,
				Attributes: me.AttributeValueMap{
					"VlanFilterList":   vtfdFilterList,
					"ForwardOperation": uint8(0x10), //VID investigation
					"NumberOfEntries":  oFsm.numVlanFilterEntries,
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
		} else {
			//VTFD already exists - just modify by 'set'
			//TODO!!: but only if the VID is not already present, skipped by now to test basic working
			logger.Debugw("UniVlanConfigFsm set VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
				"in state":  e.FSM.Current(), "device-id": oFsm.pAdaptFsm.deviceID})
			// setVid is assumed to be masked already by the caller to 12 bit
			oFsm.vlanFilterList[oFsm.numVlanFilterEntries] =
				uint16(oFsm.uniFlowParamsSlice[oFsm.configuredUniFlow].setVid)
			oFsm.mutexFlowParams.Unlock()
			vtfdFilterList := make([]uint16, 12) //needed for parameter serialization
			for i := uint8(0); i <= oFsm.numVlanFilterEntries; i++ {
				vtfdFilterList[i] = oFsm.vlanFilterList[i]
			}

			oFsm.numVlanFilterEntries++
			meParams := me.ParamData{
				EntityID: oFsm.vtfdID,
				Attributes: me.AttributeValueMap{
					"VlanFilterList":  vtfdFilterList,
					"NumberOfEntries": oFsm.numVlanFilterEntries,
				},
			}
			meInstance := oFsm.pOmciCC.sendSetVtfdVar(context.TODO(), ConstDefaultOmciTimeout, true,
				oFsm.pAdaptFsm.commChan, meParams)
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  send shall return (dual format) error code that can be used here for immediate error treatment
			//  (relevant to all used sendXX() methods in this (and other) FSM's)
			oFsm.pOmciCC.pLastTxMeInstance = meInstance
		}
		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("VTFD create/set failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return
		}
	}
	go oFsm.performConfigEvtocdEntries(oFsm.configuredUniFlow)
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
				_ = a_pAFsm.pFsm.Event(vlanEvReset)
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
				_ = a_pAFsm.pFsm.Event(vlanEvRestart)
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
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.pAdaptFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info("UniVlanConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
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
					{
						if oFsm.configuredUniFlow == 0 {
							// Only if CreateResponse is received from first flow entry - let the FSM proceed ...
							_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigVtfd)
						} else { // let the MultiEntity config proceed by stopping the wait function
							oFsm.omciMIdsResponseReceived <- true
						}
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
				case "VlanTaggingFilterData",
					"ExtendedVlanTaggingOperationConfigurationData":
					{ // let the MultiEntity config proceed by stopping the wait function
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

func (oFsm *UniVlanConfigFsm) performConfigEvtocdEntries(aFlowEntryNo uint8) {
	if aFlowEntryNo == 0 {
		// EthType set only at first flow element
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
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return
		}
	} //first flow element

	oFsm.mutexFlowParams.Lock()
	if oFsm.uniFlowParamsSlice[aFlowEntryNo].setVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission required
		oFsm.mutexFlowParams.Unlock()
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
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
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
				oFsm.uniFlowParamsSlice[aFlowEntryNo].matchPcp<<cFilterPrioOffset| // either DNFonPrio or ignore tag (default) on innerVLAN
					oFsm.uniFlowParamsSlice[aFlowEntryNo].matchVid<<cFilterVidOffset| // either DNFonVid or real filter VID
					cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
					cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
				oFsm.uniFlowParamsSlice[aFlowEntryNo].tagsToRemove<<cTreatTTROffset| // either 1 or 0
					cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
					cDontCareVid<<cTreatVidOffset| // Outer VID don't care
					cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
				oFsm.uniFlowParamsSlice[aFlowEntryNo].setPcp<<cTreatPrioOffset| // as configured in flow
					oFsm.uniFlowParamsSlice[aFlowEntryNo].setVid<<cTreatVidOffset| //as configured in flow
					cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100
			oFsm.mutexFlowParams.Unlock()

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
				_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
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
						oFsm.uniFlowParamsSlice[aFlowEntryNo].setVid<<cTreatVidOffset| // Outer VID don't care
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

				oFsm.mutexFlowParams.Unlock()
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
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} // just for local var's
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

				oFsm.mutexFlowParams.Lock()
				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
					cCopyPrioFromInner<<cTreatPrioOffset| // vlan copy from PrioTag
						//   (as done in Py code, maybe better option would be setPcp here, which still could be PrioCopy?)
						oFsm.uniFlowParamsSlice[aFlowEntryNo].setVid<<cTreatVidOffset| // Outer VID as configured
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100
				oFsm.mutexFlowParams.Unlock()

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
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} //just for local var's
		}
	}

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("EVTOCD set loop finished", log.Fields{"device-id": oFsm.pAdaptFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigEvtocd)
}

func (oFsm *UniVlanConfigFsm) waitforOmciResponse() error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
	case <-time.After(30 * time.Second): //AS FOR THE OTHER OMCI FSM's
		logger.Warnw("UniVlanConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("uniVlanConfigFsm multi entity timeout")
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debug("UniVlanConfigFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("UniVlanConfigFsm multi entity response error", log.Fields{"for device-id": oFsm.pAdaptFsm.deviceID})
		return errors.New("uniVlanConfigFsm multi entity responseError")
	}
}
