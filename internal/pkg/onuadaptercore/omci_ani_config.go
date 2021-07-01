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
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/cevaris/ordered_map"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v5/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

const (
	// events of config PON ANI port FSM
	aniEvStart             = "aniEvStart"
	aniEvStartConfig       = "aniEvStartConfig"
	aniEvRxDot1pmapCResp   = "aniEvRxDot1pmapCResp"
	aniEvRxMbpcdResp       = "aniEvRxMbpcdResp"
	aniEvRxTcontsResp      = "aniEvRxTcontsResp"
	aniEvRxGemntcpsResp    = "aniEvRxGemntcpsResp"
	aniEvRxGemiwsResp      = "aniEvRxGemiwsResp"
	aniEvRxPrioqsResp      = "aniEvRxPrioqsResp"
	aniEvRxDot1pmapSResp   = "aniEvRxDot1pmapSResp"
	aniEvRemGemiw          = "aniEvRemGemiw"
	aniEvWaitFlowRem       = "aniEvWaitFlowRem"
	aniEvFlowRemDone       = "aniEvFlowRemDone"
	aniEvRxRemGemiwResp    = "aniEvRxRemGemiwResp"
	aniEvRxRemGemntpResp   = "aniEvRxRemGemntpResp"
	aniEvRxRemTdResp       = "aniEvRxRemTdResp"
	aniEvRemTcontPath      = "aniEvRemTcontPath"
	aniEvRxResetTcontResp  = "aniEvRxResetTcontResp"
	aniEvRxRem1pMapperResp = "aniEvRxRem1pMapperResp"
	aniEvRxRemAniBPCDResp  = "aniEvRxRemAniBPCDResp"
	aniEvTimeoutSimple     = "aniEvTimeoutSimple"
	aniEvTimeoutMids       = "aniEvTimeoutMids"
	aniEvReset             = "aniEvReset"
	aniEvRestart           = "aniEvRestart"
	aniEvSkipOmciConfig    = "aniEvSkipOmciConfig"
)
const (
	// states of config PON ANI port FSM
	aniStDisabled            = "aniStDisabled"
	aniStStarting            = "aniStStarting"
	aniStCreatingDot1PMapper = "aniStCreatingDot1PMapper"
	aniStCreatingMBPCD       = "aniStCreatingMBPCD"
	aniStSettingTconts       = "aniStSettingTconts"
	aniStCreatingGemNCTPs    = "aniStCreatingGemNCTPs"
	aniStCreatingGemIWs      = "aniStCreatingGemIWs"
	aniStSettingPQs          = "aniStSettingPQs"
	aniStSettingDot1PMapper  = "aniStSettingDot1PMapper"
	aniStConfigDone          = "aniStConfigDone"
	aniStRemovingGemIW       = "aniStRemovingGemIW"
	aniStWaitingFlowRem      = "aniStWaitingFlowRem"
	aniStRemovingGemNCTP     = "aniStRemovingGemNCTP"
	aniStRemovingTD          = "aniStRemovingTD"
	aniStResetTcont          = "aniStResetTcont"
	aniStRemDot1PMapper      = "aniStRemDot1PMapper"
	aniStRemAniBPCD          = "aniStRemAniBPCD"
	aniStRemoveDone          = "aniStRemoveDone"
	aniStResetting           = "aniStResetting"
)
const cAniFsmIdleState = aniStConfigDone

type ponAniGemPortAttribs struct {
	gemPortID      uint16
	upQueueID      uint16
	downQueueID    uint16
	direction      uint8
	qosPolicy      string
	weight         uint8
	pbitString     string
	isMulticast    bool
	multicastGemID uint16
	staticACL      string
	dynamicACL     string
}

//uniPonAniConfigFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type uniPonAniConfigFsm struct {
	pDeviceHandler           *deviceHandler
	deviceID                 string
	pOmciCC                  *omciCC
	pOnuUniPort              *onuUniPort
	pUniTechProf             *onuUniTechProf
	pOnuDB                   *onuDeviceDB
	techProfileID            uint8
	uniTpKey                 uniTP
	requestEvent             OnuDeviceEvent
	mutexIsAwaitingResponse  sync.RWMutex
	isCanceled               bool
	isAwaitingResponse       bool
	omciMIdsResponseReceived chan bool //separate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                *AdapterFsm
	chSuccess                chan<- uint8
	procStep                 uint8
	mutexChanSet             sync.RWMutex
	chanSet                  bool
	mapperSP0ID              uint16
	macBPCD0ID               uint16
	tcont0ID                 uint16
	alloc0ID                 uint16
	gemPortAttribsSlice      []ponAniGemPortAttribs
	mutexPLastTxMeInstance   sync.RWMutex
	pLastTxMeInstance        *me.ManagedEntity
	requestEventOffset       uint8 //used to indicate ConfigDone or Removed using successor (enum)
	isWaitingForFlowDelete   bool
	waitFlowDeleteChannel    chan bool
	tcontSetBefore           bool
}

//newUniPonAniConfigFsm is the 'constructor' for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
func newUniPonAniConfigFsm(ctx context.Context, apDevOmciCC *omciCC, apUniPort *onuUniPort, apUniTechProf *onuUniTechProf,
	apOnuDB *onuDeviceDB, aTechProfileID uint8, aRequestEvent OnuDeviceEvent, aName string,
	apDeviceHandler *deviceHandler, aCommChannel chan Message) *uniPonAniConfigFsm {
	instFsm := &uniPonAniConfigFsm{
		pDeviceHandler: apDeviceHandler,
		deviceID:       apDeviceHandler.deviceID,
		pOmciCC:        apDevOmciCC,
		pOnuUniPort:    apUniPort,
		pUniTechProf:   apUniTechProf,
		pOnuDB:         apOnuDB,
		techProfileID:  aTechProfileID,
		requestEvent:   aRequestEvent,
		chanSet:        false,
		tcontSetBefore: false,
	}
	instFsm.uniTpKey = uniTP{uniID: apUniPort.uniID, tpID: aTechProfileID}
	instFsm.waitFlowDeleteChannel = make(chan bool)

	instFsm.pAdaptFsm = NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw(ctx, "uniPonAniConfigFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
		aniStDisabled,
		fsm.Events{

			{Name: aniEvStart, Src: []string{aniStDisabled}, Dst: aniStStarting},

			//Note: .1p-Mapper and MBPCD might also have multi instances (per T-Cont) - by now only one 1 T-Cont considered!
			{Name: aniEvStartConfig, Src: []string{aniStStarting}, Dst: aniStCreatingDot1PMapper},
			{Name: aniEvRxDot1pmapCResp, Src: []string{aniStCreatingDot1PMapper}, Dst: aniStCreatingMBPCD},
			{Name: aniEvRxMbpcdResp, Src: []string{aniStCreatingMBPCD}, Dst: aniStSettingTconts},
			{Name: aniEvRxTcontsResp, Src: []string{aniStSettingTconts}, Dst: aniStCreatingGemNCTPs},
			// the creatingGemNCTPs state is used for multi ME config if required for all configured/available GemPorts
			{Name: aniEvRxGemntcpsResp, Src: []string{aniStCreatingGemNCTPs}, Dst: aniStCreatingGemIWs},
			// the creatingGemIWs state is used for multi ME config if required for all configured/available GemPorts
			{Name: aniEvRxGemiwsResp, Src: []string{aniStCreatingGemIWs}, Dst: aniStSettingPQs},
			// the settingPQs state is used for multi ME config if required for all configured/available upstream PriorityQueues
			{Name: aniEvRxPrioqsResp, Src: []string{aniStSettingPQs}, Dst: aniStSettingDot1PMapper},
			{Name: aniEvRxDot1pmapSResp, Src: []string{aniStSettingDot1PMapper}, Dst: aniStConfigDone},

			//for removing Gem related resources
			{Name: aniEvRemGemiw, Src: []string{aniStConfigDone}, Dst: aniStRemovingGemIW},
			{Name: aniEvWaitFlowRem, Src: []string{aniStRemovingGemIW}, Dst: aniStWaitingFlowRem},
			{Name: aniEvFlowRemDone, Src: []string{aniStWaitingFlowRem}, Dst: aniStRemovingGemIW},
			{Name: aniEvRxRemGemiwResp, Src: []string{aniStRemovingGemIW}, Dst: aniStRemovingGemNCTP},
			{Name: aniEvRxRemGemntpResp, Src: []string{aniStRemovingGemNCTP}, Dst: aniStRemovingTD},
			{Name: aniEvRxRemTdResp, Src: []string{aniStRemovingTD}, Dst: aniStConfigDone},

			//for removing TCONT related resources
			{Name: aniEvRemTcontPath, Src: []string{aniStConfigDone}, Dst: aniStResetTcont},
			{Name: aniEvRxResetTcontResp, Src: []string{aniStResetTcont}, Dst: aniStRemDot1PMapper},
			{Name: aniEvRxRem1pMapperResp, Src: []string{aniStRemDot1PMapper}, Dst: aniStRemAniBPCD},
			{Name: aniEvRxRemAniBPCDResp, Src: []string{aniStRemAniBPCD}, Dst: aniStRemoveDone},

			{Name: aniEvTimeoutSimple, Src: []string{aniStCreatingDot1PMapper, aniStCreatingMBPCD, aniStSettingTconts, aniStSettingDot1PMapper,
				aniStRemovingGemIW, aniStRemovingGemNCTP, aniStRemovingTD,
				aniStResetTcont, aniStRemDot1PMapper, aniStRemAniBPCD, aniStRemoveDone}, Dst: aniStStarting},
			{Name: aniEvTimeoutMids, Src: []string{
				aniStCreatingGemNCTPs, aniStCreatingGemIWs, aniStSettingPQs}, Dst: aniStStarting},

			// exceptional treatment for all states except aniStResetting
			{Name: aniEvReset, Src: []string{aniStStarting, aniStCreatingDot1PMapper, aniStCreatingMBPCD,
				aniStSettingTconts, aniStCreatingGemNCTPs, aniStCreatingGemIWs, aniStSettingPQs, aniStSettingDot1PMapper,
				aniStConfigDone, aniStRemovingGemIW, aniStWaitingFlowRem, aniStRemovingGemNCTP, aniStRemovingTD,
				aniStResetTcont, aniStRemDot1PMapper, aniStRemAniBPCD, aniStRemoveDone}, Dst: aniStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: aniEvRestart, Src: []string{aniStResetting}, Dst: aniStDisabled},
			{Name: aniEvSkipOmciConfig, Src: []string{aniStStarting}, Dst: aniStConfigDone},
		},

		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(ctx, e) },
			("enter_" + aniStStarting):            func(e *fsm.Event) { instFsm.enterConfigStartingState(ctx, e) },
			("enter_" + aniStCreatingDot1PMapper): func(e *fsm.Event) { instFsm.enterCreatingDot1PMapper(ctx, e) },
			("enter_" + aniStCreatingMBPCD):       func(e *fsm.Event) { instFsm.enterCreatingMBPCD(ctx, e) },
			("enter_" + aniStSettingTconts):       func(e *fsm.Event) { instFsm.enterSettingTconts(ctx, e) },
			("enter_" + aniStCreatingGemNCTPs):    func(e *fsm.Event) { instFsm.enterCreatingGemNCTPs(ctx, e) },
			("enter_" + aniStCreatingGemIWs):      func(e *fsm.Event) { instFsm.enterCreatingGemIWs(ctx, e) },
			("enter_" + aniStSettingPQs):          func(e *fsm.Event) { instFsm.enterSettingPQs(ctx, e) },
			("enter_" + aniStSettingDot1PMapper):  func(e *fsm.Event) { instFsm.enterSettingDot1PMapper(ctx, e) },
			("enter_" + aniStConfigDone):          func(e *fsm.Event) { instFsm.enterAniConfigDone(ctx, e) },
			("enter_" + aniStRemovingGemIW):       func(e *fsm.Event) { instFsm.enterRemovingGemIW(ctx, e) },
			("enter_" + aniStWaitingFlowRem):      func(e *fsm.Event) { instFsm.enterWaitingFlowRem(ctx, e) },
			("enter_" + aniStRemovingGemNCTP):     func(e *fsm.Event) { instFsm.enterRemovingGemNCTP(ctx, e) },
			("enter_" + aniStRemovingTD):          func(e *fsm.Event) { instFsm.enterRemovingTD(ctx, e) },
			("enter_" + aniStResetTcont):          func(e *fsm.Event) { instFsm.enterResettingTcont(ctx, e) },
			("enter_" + aniStRemDot1PMapper):      func(e *fsm.Event) { instFsm.enterRemoving1pMapper(ctx, e) },
			("enter_" + aniStRemAniBPCD):          func(e *fsm.Event) { instFsm.enterRemovingAniBPCD(ctx, e) },
			("enter_" + aniStRemoveDone):          func(e *fsm.Event) { instFsm.enterAniRemoveDone(ctx, e) },
			("enter_" + aniStResetting):           func(e *fsm.Event) { instFsm.enterResettingState(ctx, e) },
			("enter_" + aniStDisabled):            func(e *fsm.Event) { instFsm.enterDisabledState(ctx, e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw(ctx, "uniPonAniConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Debugw(ctx, "uniPonAniConfigFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//setFsmCompleteChannel sets the requested channel and channel result for transfer on success
func (oFsm *uniPonAniConfigFsm) setFsmCompleteChannel(aChSuccess chan<- uint8, aProcStep uint8) {
	oFsm.chSuccess = aChSuccess
	oFsm.procStep = aProcStep
	oFsm.setChanSet(true)
}

//CancelProcessing ensures that suspended processing at waiting on some response is aborted and reset of FSM
func (oFsm *uniPonAniConfigFsm) CancelProcessing(ctx context.Context) {
	//early indication about started reset processing
	oFsm.pUniTechProf.setProfileResetting(ctx, oFsm.pOnuUniPort.uniID, oFsm.techProfileID, true)
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexIsAwaitingResponse.Lock()
	oFsm.isCanceled = true
	if oFsm.isAwaitingResponse {
		//attention: for an unbuffered channel the sender is blocked until the value is received (processed)!
		// accordingly the mutex must be released before sending to channel here (mutex acquired in receiver)
		oFsm.mutexIsAwaitingResponse.Unlock()
		//use channel to indicate that the response waiting shall be aborted
		oFsm.omciMIdsResponseReceived <- false
	} else {
		oFsm.mutexIsAwaitingResponse.Unlock()
	}

	oFsm.mutexIsAwaitingResponse.Lock()
	if oFsm.isWaitingForFlowDelete {
		oFsm.mutexIsAwaitingResponse.Unlock()
		//use channel to indicate that the response waiting shall be aborted
		oFsm.waitFlowDeleteChannel <- false
	} else {
		oFsm.mutexIsAwaitingResponse.Unlock()
	}

	// in any case (even if it might be automatically requested by above cancellation of waiting) ensure resetting the FSM
	pAdaptFsm := oFsm.pAdaptFsm
	if pAdaptFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(aPAFsm *AdapterFsm) {
			if aPAFsm.pFsm != nil {
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			}
		}(pAdaptFsm)
	}

	//wait for completion of possibly ongoing techprofile config/remove requests to avoid
	// access conflicts on internal data by next needed data clearance
	//activity should be aborted in short time if running with FSM due to above FSM reset
	//  or finished without FSM dependency in short time
	oFsm.pUniTechProf.lockTpProcMutex()
	defer oFsm.pUniTechProf.unlockTpProcMutex()
	//remove all TechProf related internal data to allow for new configuration
	oFsm.pUniTechProf.clearAniSideConfig(ctx, oFsm.pOnuUniPort.uniID, oFsm.techProfileID)
}

//nolint: gocyclo
//TODO:visit here for refactoring for gocyclo
func (oFsm *uniPonAniConfigFsm) prepareAndEnterConfigState(ctx context.Context, aPAFsm *AdapterFsm) {
	if aPAFsm != nil && aPAFsm.pFsm != nil {
		var err error
		oFsm.mapperSP0ID, err = generateIeeMaperServiceProfileEID(uint16(oFsm.pOnuUniPort.macBpNo), uint16(oFsm.techProfileID))
		if err != nil {
			logger.Errorw(ctx, "error generating maper id", log.Fields{"device-id": oFsm.deviceID,
				"techProfileID": oFsm.techProfileID, "error": err})
			return
		}
		oFsm.macBPCD0ID, err = generateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.macBpNo), uint16(oFsm.techProfileID))
		if err != nil {
			logger.Errorw(ctx, "error generating mbpcd id", log.Fields{"device-id": oFsm.deviceID,
				"techProfileID": oFsm.techProfileID, "error": err})
			return
		}
		logger.Debugw(ctx, "generated ids for ani config", log.Fields{"mapperSP0ID": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
			"macBPCD0ID": strconv.FormatInt(int64(oFsm.macBPCD0ID), 16), "device-id": oFsm.deviceID,
			"macBpNo": oFsm.pOnuUniPort.macBpNo, "techProfileID": oFsm.techProfileID})
		pDevEntry := oFsm.pDeviceHandler.getOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": oFsm.deviceID})
			return
		}
		tcontInstID, tcontAlreadyExist, err := pDevEntry.allocateFreeTcont(ctx, oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].tcontParams.allocID)
		if err != nil {
			logger.Errorw(ctx, "No TCont instances found", log.Fields{"device-id": oFsm.deviceID, "err": err})
			if oFsm.chanSet {
				// indicate processing error/abort to the caller
				oFsm.chSuccess <- 0
				oFsm.chanSet = false //reset the internal channel state
			}
			//reset the state machine to enable usage on subsequent requests
			_ = aPAFsm.pFsm.Event(aniEvReset)
			return
		}
		oFsm.tcont0ID = tcontInstID
		oFsm.tcontSetBefore = tcontAlreadyExist
		logger.Debugw(ctx, "used-tcont-instance-id", log.Fields{"tcont-inst-id": oFsm.tcont0ID,
			"alloc-id":          oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].tcontParams.allocID,
			"tcontAlreadyExist": tcontAlreadyExist,
			"device-id":         oFsm.deviceID})

		// Access critical state with lock
		oFsm.pUniTechProf.mutexTPState.Lock()
		oFsm.alloc0ID = oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].tcontParams.allocID
		mapGemPortParams := oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].mapGemPortParams
		oFsm.pUniTechProf.mutexTPState.Unlock()

		//for all TechProfile set GemIndices
		for _, gemEntry := range mapGemPortParams {
			loGemPortAttribs := ponAniGemPortAttribs{}

			//collect all GemConfigData in a separate Fsm related slice (needed also to avoid mix-up with unsorted mapPonAniConfig)

			if queueInstKeys := oFsm.pOnuDB.getSortedInstKeys(ctx, me.PriorityQueueClassID); len(queueInstKeys) > 0 {

				loGemPortAttribs.gemPortID = gemEntry.gemPortID
				// MibDb usage: upstream PrioQueue.RelatedPort = xxxxyyyy with xxxx=TCont.Entity(incl. slot) and yyyy=prio
				// i.e.: search PrioQueue list with xxxx=actual T-Cont.Entity,
				// from that list use the PrioQueue.Entity with  gemEntry.prioQueueIndex == yyyy (expect 0..7)
				usQrelPortMask := uint32((((uint32)(oFsm.tcont0ID)) << 16) + uint32(gemEntry.prioQueueIndex))

				// MibDb usage: downstream PrioQueue.RelatedPort = xxyyzzzz with xx=slot, yy=UniPort and zzzz=prio
				// i.e.: search PrioQueue list with yy=actual pOnuUniPort.uniID,
				// from that list use the PrioQueue.Entity with gemEntry.prioQueueIndex == zzzz (expect 0..7)
				// Note: As we do not maintain any slot numbering, slot number will be excluded from seatch pattern.
				//       Furthermore OMCI Onu port-Id is expected to start with 1 (not 0).
				dsQrelPortMask := uint32((((uint32)(oFsm.pOnuUniPort.uniID + 1)) << 16) + uint32(gemEntry.prioQueueIndex))

				usQueueFound := false
				dsQueueFound := false
				for _, mgmtEntityID := range queueInstKeys {
					if meAttributes := oFsm.pOnuDB.GetMe(me.PriorityQueueClassID, mgmtEntityID); meAttributes != nil {
						returnVal := meAttributes["RelatedPort"]
						if returnVal != nil {
							if relatedPort, err := oFsm.pOnuDB.getUint32Attrib(returnVal); err == nil {
								if relatedPort == usQrelPortMask {
									loGemPortAttribs.upQueueID = mgmtEntityID
									logger.Debugw(ctx, "UpQueue for GemPort found:", log.Fields{"gemPortID": loGemPortAttribs.gemPortID,
										"upQueueID": strconv.FormatInt(int64(loGemPortAttribs.upQueueID), 16), "device-id": oFsm.deviceID})
									usQueueFound = true
								} else if (relatedPort&0xFFFFFF) == dsQrelPortMask && mgmtEntityID < 0x8000 {
									loGemPortAttribs.downQueueID = mgmtEntityID
									logger.Debugw(ctx, "DownQueue for GemPort found:", log.Fields{"gemPortID": loGemPortAttribs.gemPortID,
										"downQueueID": strconv.FormatInt(int64(loGemPortAttribs.downQueueID), 16), "device-id": oFsm.deviceID})
									dsQueueFound = true
								}
								if usQueueFound && dsQueueFound {
									break
								}
							} else {
								logger.Warnw(ctx, "Could not convert attribute value", log.Fields{"device-id": oFsm.deviceID})
							}
						} else {
							logger.Warnw(ctx, "'RelatedPort' not found in meAttributes:", log.Fields{"device-id": oFsm.deviceID})
						}
					} else {
						logger.Warnw(ctx, "No attributes available in DB:", log.Fields{"meClassID": me.PriorityQueueClassID,
							"mgmtEntityID": mgmtEntityID, "device-id": oFsm.deviceID})
					}
				}
			} else {
				logger.Warnw(ctx, "No PriorityQueue instances found", log.Fields{"device-id": oFsm.deviceID})
			}
			loGemPortAttribs.direction = gemEntry.direction
			loGemPortAttribs.qosPolicy = gemEntry.queueSchedPolicy
			loGemPortAttribs.weight = gemEntry.queueWeight
			loGemPortAttribs.pbitString = gemEntry.pbitString

			if gemEntry.isMulticast {
				//TODO this might effectively ignore the for loop starting at line 316
				loGemPortAttribs.gemPortID = gemEntry.multicastGemPortID
				loGemPortAttribs.isMulticast = true
				loGemPortAttribs.multicastGemID = gemEntry.multicastGemPortID
				loGemPortAttribs.staticACL = gemEntry.staticACL
				loGemPortAttribs.dynamicACL = gemEntry.dynamicACL

				logger.Debugw(ctx, "Multicast GemPort attributes:", log.Fields{
					"gemPortID":      loGemPortAttribs.gemPortID,
					"isMulticast":    loGemPortAttribs.isMulticast,
					"multicastGemID": loGemPortAttribs.multicastGemID,
					"staticACL":      loGemPortAttribs.staticACL,
					"dynamicACL":     loGemPortAttribs.dynamicACL,
					"device-id":      oFsm.deviceID,
				})

			} else {
				logger.Debugw(ctx, "Upstream GemPort attributes:", log.Fields{
					"gemPortID":      loGemPortAttribs.gemPortID,
					"upQueueID":      loGemPortAttribs.upQueueID,
					"downQueueID":    loGemPortAttribs.downQueueID,
					"pbitString":     loGemPortAttribs.pbitString,
					"prioQueueIndex": gemEntry.prioQueueIndex,
					"device-id":      oFsm.deviceID,
				})
			}

			oFsm.gemPortAttribsSlice = append(oFsm.gemPortAttribsSlice, loGemPortAttribs)
		}
		if !oFsm.pDeviceHandler.isSkipOnuConfigReconciling() {
			_ = aPAFsm.pFsm.Event(aniEvStartConfig)
		} else {
			logger.Debugw(ctx, "reconciling - skip omci-config of ANI side ", log.Fields{"device-id": oFsm.deviceID})
			_ = aPAFsm.pFsm.Event(aniEvSkipOmciConfig)
		}
	}
}

func (oFsm *uniPonAniConfigFsm) enterConfigStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniPonAniConfigFsm start", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if oFsm.omciMIdsResponseReceived == nil {
		oFsm.omciMIdsResponseReceived = make(chan bool)
		logger.Debug(ctx, "uniPonAniConfigFsm - OMCI multiInstance RxChannel defined")
	} else {
		// as we may 're-use' this instance of FSM and the connected channel
		// make sure there is no 'lingering' request in the already existing channel:
		// (simple loop sufficient as we are the only receiver)
		for len(oFsm.omciMIdsResponseReceived) > 0 {
			<-oFsm.omciMIdsResponseReceived
		}
	}
	//ensure internal slices are empty (which might be set from previous run) - release memory
	oFsm.gemPortAttribsSlice = nil
	oFsm.mutexIsAwaitingResponse.Lock()
	//reset the canceled state possibly existing from previous reset
	oFsm.isCanceled = false
	oFsm.mutexIsAwaitingResponse.Unlock()

	// start go routine for processing of ANI config messages
	go oFsm.processOmciAniMessages(ctx)

	//let the state machine run forward from here directly
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go oFsm.prepareAndEnterConfigState(ctx, pConfigAniStateAFsm)

	}
}

func (oFsm *uniPonAniConfigFsm) enterCreatingDot1PMapper(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm Tx Create::Dot1PMapper", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	oFsm.requestEventOffset = 0 //0 offset for last config request activity
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendCreateDot1PMapper(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.mapperSP0ID, oFsm.pAdaptFsm.commChan)
	if err != nil {
		logger.Errorw(ctx, "Dot1PMapper create failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

}

func (oFsm *uniPonAniConfigFsm) enterCreatingMBPCD(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm Tx Create::MBPCD", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.macBPCD0ID), 16),
		"TPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
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
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendCreateMBPConfigDataVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	if err != nil {
		logger.Errorw(ctx, "MBPConfigDataVar create failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

}

func (oFsm *uniPonAniConfigFsm) enterSettingTconts(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm Tx Set::Tcont", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.tcont0ID), 16),
		"AllocId":   strconv.FormatInt(int64(oFsm.alloc0ID), 16),
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID,
		"tcontExist": oFsm.tcontSetBefore})
	//If tcont was set before, then no need to set it again. Let state machine to proceed.
	if oFsm.tcontSetBefore {
		go func(aPAFsm *AdapterFsm) {
			if aPAFsm != nil && aPAFsm.pFsm != nil {
				_ = aPAFsm.pFsm.Event(aniEvRxTcontsResp)
			}
		}(oFsm.pAdaptFsm)
		return
	}
	meParams := me.ParamData{
		EntityID: oFsm.tcont0ID,
		Attributes: me.AttributeValueMap{
			"AllocId": oFsm.alloc0ID,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendSetTcontVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	if err != nil {
		logger.Errorw(ctx, "TcontVar set failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

}

func (oFsm *uniPonAniConfigFsm) enterCreatingGemNCTPs(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start creating GemNWCtp loop", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	go oFsm.performCreatingGemNCTPs(ctx)
}

func (oFsm *uniPonAniConfigFsm) enterCreatingGemIWs(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start creating GemIwTP loop", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	go oFsm.performCreatingGemIWs(ctx)
}

func (oFsm *uniPonAniConfigFsm) enterSettingPQs(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start setting PrioQueue loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	go oFsm.performSettingPQs(ctx)
}

func (oFsm *uniPonAniConfigFsm) enterSettingDot1PMapper(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "uniPonAniConfigFsm Tx Set::.1pMapper with all PBits set", log.Fields{"EntitytId": 0x8042, /*cmp above*/
		"toGemIw":   1024, /* cmp above */
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})

	logger.Debugw(ctx, "uniPonAniConfigFsm Tx Set::1pMapper", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})

	meParams := me.ParamData{
		EntityID:   oFsm.mapperSP0ID,
		Attributes: make(me.AttributeValueMap),
	}

	//assign the GemPorts according to the configured Prio
	var loPrioGemPortArray [8]uint16
	for _, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		if gemPortAttribs.isMulticast {
			logger.Debugw(ctx, "uniPonAniConfigFsm Port is Multicast, ignoring .1pMapper", log.Fields{
				"device-id": oFsm.deviceID, "GemPort": gemPortAttribs.gemPortID,
				"prioString": gemPortAttribs.pbitString})
			continue
		}
		if gemPortAttribs.pbitString == "" {
			logger.Warnw(ctx, "uniPonAniConfigFsm PrioString empty string error", log.Fields{
				"device-id": oFsm.deviceID, "GemPort": gemPortAttribs.gemPortID,
				"prioString": gemPortAttribs.pbitString})
			continue
		}
		for i := 0; i < 8; i++ {
			// "lenOfPbitMap(8) - i + 1" will give i-th pbit value from LSB position in the pbit map string
			if prio, err := strconv.Atoi(string(gemPortAttribs.pbitString[7-i])); err == nil {
				if prio == 1 { // Check this p-bit is set
					if loPrioGemPortArray[i] == 0 {
						loPrioGemPortArray[i] = gemPortAttribs.gemPortID //gemPortId=EntityID and unique
					} else {
						logger.Warnw(ctx, "uniPonAniConfigFsm PrioString not unique", log.Fields{
							"device-id": oFsm.deviceID, "IgnoredGemPort": gemPortAttribs.gemPortID,
							"SetGemPort": loPrioGemPortArray[i]})
					}
				}
			} else {
				logger.Warnw(ctx, "uniPonAniConfigFsm PrioString evaluation error", log.Fields{
					"device-id": oFsm.deviceID, "GemPort": gemPortAttribs.gemPortID,
					"prioString": gemPortAttribs.pbitString, "position": i})
			}

		}
	}

	var foundIwPtr = false
	for index, value := range loPrioGemPortArray {
		meAttribute := fmt.Sprintf("InterworkTpPointerForPBitPriority%d", index)
		if value != 0 {
			foundIwPtr = true
			meParams.Attributes[meAttribute] = value
			logger.Debugw(ctx, "UniPonAniConfigFsm Set::1pMapper", log.Fields{
				"for Prio":  index,
				"IwPtr":     strconv.FormatInt(int64(value), 16),
				"device-id": oFsm.deviceID})
		} else {
			// The null pointer 0xFFFF specifies that frames with the associated priority are to be discarded.
			// setting this parameter is not strictly needed anymore with the ensured .1pMapper create default setting
			// but except for processing effort does not really harm - left to keep changes low
			meParams.Attributes[meAttribute] = 0xffff
		}
	}
	// The TP type value 0 also indicates bridging mapping, and the TP pointer should be set to 0xFFFF
	// setting this parameter is not strictly needed anymore with the ensured .1pMapper create default setting
	// but except for processing effort does not really harm - left to keep changes low
	meParams.Attributes["TpPointer"] = 0xffff

	if !foundIwPtr {
		logger.Debugw(ctx, "UniPonAniConfigFsm no GemIwPtr found for .1pMapper - abort", log.Fields{
			"device-id": oFsm.deviceID})
		//TODO With multicast is possible that no upstream gem ports are not present in the tech profile,
		// this reset needs to be performed only if the tech profile provides upstream gem ports but no priority is set
		//let's reset the state machine in order to release all resources now
		//pConfigAniStateAFsm := oFsm.pAdaptFsm
		//if pConfigAniStateAFsm != nil {
		//	// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		//	go func(aPAFsm *AdapterFsm) {
		//		if aPAFsm != nil && aPAFsm.pFsm != nil {
		//			_ = aPAFsm.pFsm.Event(aniEvReset)
		//		}
		//	}(pConfigAniStateAFsm)
		//}
		//Moving forward the FSM as if the response was received correctly.
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvRxDot1pmapSResp)
				}
			}(pConfigAniStateAFsm)
		}
	} else {
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.sendSetDot1PMapperVar(context.TODO(), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		if err != nil {
			logger.Errorw(ctx, "Dot1PMapperVar set failed, aborting uniPonAniConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			pConfigAniStateAFsm := oFsm.pAdaptFsm
			if pConfigAniStateAFsm != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				// obviously calling some FSM event here directly does not work - so trying to decouple it ...
				go func(aPAFsm *AdapterFsm) {
					if aPAFsm != nil && aPAFsm.pFsm != nil {
						_ = aPAFsm.pFsm.Event(aniEvReset)
					}
				}(pConfigAniStateAFsm)
				return
			}
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()
	}
}

func (oFsm *uniPonAniConfigFsm) enterAniConfigDone(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm ani config done", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
	//store that the UNI related techProfile processing is done for the given Profile and Uni
	oFsm.pUniTechProf.setConfigDone(oFsm.pOnuUniPort.uniID, oFsm.techProfileID, true)
	if !oFsm.pDeviceHandler.isSkipOnuConfigReconciling() {
		//use DeviceHandler event notification directly
		oFsm.pDeviceHandler.deviceProcStatusUpdate(ctx, OnuDeviceEvent((uint8(oFsm.requestEvent) + oFsm.requestEventOffset)))
		//if techProfile processing is done it must be checked, if some prior/parallel flow configuration is pending
		//  but only in case the techProfile was configured (not deleted)
		if oFsm.requestEventOffset == 0 {
			go oFsm.pDeviceHandler.verifyUniVlanConfigRequest(ctx, oFsm.pOnuUniPort, oFsm.techProfileID)
		}
	} else {
		logger.Debugw(ctx, "reconciling - skip AniConfigDone processing", log.Fields{"device-id": oFsm.deviceID})
	}
	if oFsm.isChanSet() {
		// indicate processing done to the caller
		logger.Debugw(ctx, "uniPonAniConfigFsm processingDone on channel", log.Fields{
			"ProcessingStep": oFsm.procStep, "from_State": e.FSM.Current(), "device-id": oFsm.deviceID})
		oFsm.chSuccess <- oFsm.procStep
		oFsm.setChanSet(false) //reset the internal channel state
	}

	//the FSM is left active in this state as long as no specific reset or remove is requested from outside
}

func (oFsm *uniPonAniConfigFsm) enterRemovingGemIW(ctx context.Context, e *fsm.Event) {
	oFsm.pUniTechProf.mutexTPState.Lock()
	//flush the waitFlowDeleteChannel - possibly already/still set by some previous activity
	select {
	case <-oFsm.waitFlowDeleteChannel:
		logger.Debug(ctx, "flushed waitFlowDeleteChannel")
	default:
	}

	if oFsm.pDeviceHandler.UniVlanConfigFsmMap[oFsm.pOnuUniPort.uniID] != nil {
		if oFsm.pDeviceHandler.UniVlanConfigFsmMap[oFsm.pOnuUniPort.uniID].IsFlowRemovePending(oFsm.waitFlowDeleteChannel) {
			oFsm.pUniTechProf.mutexTPState.Unlock()
			logger.Debugw(ctx, "flow remove pending - wait before processing gem port delete",
				log.Fields{"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
			// if flow remove is pending then wait for flow remove to finish first before proceeding with gem port delete
			pConfigAniStateAFsm := oFsm.pAdaptFsm
			if pConfigAniStateAFsm != nil {
				// obviously calling some FSM event here directly does not work - so trying to decouple it ...
				go func(aPAFsm *AdapterFsm) {
					if aPAFsm != nil && aPAFsm.pFsm != nil {
						_ = aPAFsm.pFsm.Event(aniEvWaitFlowRem)
					}
				}(pConfigAniStateAFsm)
			} else {
				logger.Errorw(ctx, "pConfigAniStateAFsm is nil", log.Fields{"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
			}
			return
		}
	} else {
		logger.Debugw(ctx, "uni vlan config doesn't exist - no flow remove could be pending",
			log.Fields{"device-id": oFsm.deviceID, "techProfile-id": oFsm.techProfileID})
	}

	// get the related GemPort entity Id from pUniTechProf, OMCI Gem* entityID is set to be equal to GemPortId!
	loGemPortID := (*(oFsm.pUniTechProf.mapRemoveGemEntry[oFsm.uniTpKey])).gemPortID
	oFsm.pUniTechProf.mutexTPState.Unlock()
	logger.Debugw(ctx, "uniPonAniConfigFsm - start removing one GemIwTP", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID,
		"GemIwTp-entity-id": loGemPortID})
	oFsm.requestEventOffset = 1 //offset 1 to indicate last activity = remove

	// this state entry is only expected in a suitable state (checked outside in onu_uni_tp)
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendDeleteGemIWTP(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, loGemPortID)
	if err != nil {
		logger.Errorw(ctx, "GemIWTP delete failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
}

func (oFsm *uniPonAniConfigFsm) enterWaitingFlowRem(ctx context.Context, e *fsm.Event) {
	oFsm.mutexIsAwaitingResponse.Lock()
	oFsm.isWaitingForFlowDelete = true
	oFsm.mutexIsAwaitingResponse.Unlock()
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(2 * oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //give flow processing enough time to finish (but try to be less than rwCore flow timeouts)
		logger.Warnw(ctx, "uniPonAniConfigFsm WaitingFlowRem timeout", log.Fields{
			"for device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isWaitingForFlowDelete = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		//if the flow is not removed as expected we just try to continue with GemPort removal and hope things are clearing up afterwards
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvFlowRemDone)
				}
			}(pConfigAniStateAFsm)
		} else {
			logger.Errorw(ctx, "pConfigAniStateAFsm is nil", log.Fields{
				"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
		}
		return

	case success := <-oFsm.waitFlowDeleteChannel:
		if success {
			logger.Debugw(ctx, "uniPonAniConfigFsm flow removed info received", log.Fields{
				"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
			oFsm.mutexIsAwaitingResponse.Lock()
			oFsm.isWaitingForFlowDelete = false
			oFsm.mutexIsAwaitingResponse.Unlock()
			pConfigAniStateAFsm := oFsm.pAdaptFsm
			if pConfigAniStateAFsm != nil {
				// obviously calling some FSM event here directly does not work - so trying to decouple it ...
				go func(aPAFsm *AdapterFsm) {
					if aPAFsm != nil && aPAFsm.pFsm != nil {
						_ = aPAFsm.pFsm.Event(aniEvFlowRemDone)
					}
				}(pConfigAniStateAFsm)
			} else {
				logger.Errorw(ctx, "pConfigAniStateAFsm is nil", log.Fields{
					"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
			}
			return
		}
		// waiting was aborted (probably on external request)
		logger.Debugw(ctx, "uniPonAniConfigFsm WaitingFlowRem aborted", log.Fields{
			"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID, "techProfile-id": oFsm.techProfileID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isWaitingForFlowDelete = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		//to be sure we can just generate the reset-event to ensure leaving this state towards 'reset'
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
		}
		return
	}
}

func (oFsm *uniPonAniConfigFsm) enterRemovingGemNCTP(ctx context.Context, e *fsm.Event) {
	oFsm.pUniTechProf.mutexTPState.Lock()
	loGemPortID := (*(oFsm.pUniTechProf.mapRemoveGemEntry[oFsm.uniTpKey])).gemPortID
	oFsm.pUniTechProf.mutexTPState.Unlock()
	logger.Debugw(ctx, "uniPonAniConfigFsm - start removing one GemNCTP", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID,
		"GemNCTP-entity-id": loGemPortID})
	// this state entry is only expected in a suitable state (checked outside in onu_uni_tp)
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendDeleteGemNCTP(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, loGemPortID)
	if err != nil {
		logger.Errorw(ctx, "GemNCTP delete failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

	// Mark the gem port to be removed for Performance History monitoring
	if oFsm.pDeviceHandler.pOnuMetricsMgr != nil {
		oFsm.pDeviceHandler.pOnuMetricsMgr.RemoveGemPortForPerfMonitoring(ctx, loGemPortID)
	}
}
func (oFsm *uniPonAniConfigFsm) enterRemovingTD(ctx context.Context, e *fsm.Event) {
	oFsm.pUniTechProf.mutexTPState.Lock()
	loGemPortID := (*(oFsm.pUniTechProf.mapRemoveGemEntry[oFsm.uniTpKey])).gemPortID
	oFsm.pUniTechProf.mutexTPState.Unlock()
	logger.Debugw(ctx, "uniPonAniConfigFsm - start removing Traffic Descriptor", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID,
		"TD-entity-id": loGemPortID})

	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendDeleteTD(log.WithSpanFromContext(context.TODO(), ctx),
		oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true, oFsm.pAdaptFsm.commChan, loGemPortID)

	if err != nil {
		logger.Errorw(ctx, "TD delete failed - proceed fsm",
			log.Fields{"device-id": oFsm.deviceID, "gemPortID": loGemPortID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
}

func (oFsm *uniPonAniConfigFsm) enterResettingTcont(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start resetting the TCont", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})

	oFsm.requestEventOffset = 1 //offset 1 for last remove activity
	// this state entry is only expected in a suitable state (checked outside in onu_uni_tp)
	meParams := me.ParamData{
		EntityID: oFsm.tcont0ID,
		Attributes: me.AttributeValueMap{
			"AllocId": unusedTcontAllocID,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendSetTcontVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	if err != nil {
		logger.Errorw(ctx, "TcontVar set failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

}

func (oFsm *uniPonAniConfigFsm) enterRemoving1pMapper(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start deleting the .1pMapper", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})

	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendDeleteDot1PMapper(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, oFsm.mapperSP0ID)
	if err != nil {
		logger.Errorw(ctx, "Dot1Mapper delete failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()

}

func (oFsm *uniPonAniConfigFsm) enterRemovingAniBPCD(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm - start deleting the ANI MBCD", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})

	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.sendDeleteMBPConfigData(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
		oFsm.pAdaptFsm.commChan, oFsm.macBPCD0ID)
	if err != nil {
		logger.Errorw(ctx, "MBPConfigData delete failed, aborting uniPonAniConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		pConfigAniStateAFsm := oFsm.pAdaptFsm
		if pConfigAniStateAFsm != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			// obviously calling some FSM event here directly does not work - so trying to decouple it ...
			go func(aPAFsm *AdapterFsm) {
				if aPAFsm != nil && aPAFsm.pFsm != nil {
					_ = aPAFsm.pFsm.Event(aniEvReset)
				}
			}(pConfigAniStateAFsm)
			return
		}
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
}

func (oFsm *uniPonAniConfigFsm) enterAniRemoveDone(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm ani removal done", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	//use DeviceHandler event notification directly
	oFsm.pDeviceHandler.deviceProcStatusUpdate(ctx, OnuDeviceEvent((uint8(oFsm.requestEvent) + oFsm.requestEventOffset)))
	if oFsm.isChanSet() {
		// indicate processing done to the caller
		logger.Debugw(ctx, "uniPonAniConfigFsm processingDone on channel", log.Fields{
			"ProcessingStep": oFsm.procStep, "from_State": e.FSM.Current(), "device-id": oFsm.deviceID})
		oFsm.chSuccess <- oFsm.procStep
		oFsm.setChanSet(false) //reset the internal channel state
	}

	//let's reset the state machine in order to release all resources now
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(aPAFsm *AdapterFsm) {
			if aPAFsm != nil && aPAFsm.pFsm != nil {
				_ = aPAFsm.pFsm.Event(aniEvReset)
			}
		}(pConfigAniStateAFsm)
	}
}

func (oFsm *uniPonAniConfigFsm) enterResettingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm resetting", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})

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
		go func(aPAFsm *AdapterFsm) {
			if aPAFsm != nil && aPAFsm.pFsm != nil {
				_ = aPAFsm.pFsm.Event(aniEvRestart)
			}
		}(pConfigAniStateAFsm)
	}
}

func (oFsm *uniPonAniConfigFsm) enterDisabledState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "uniPonAniConfigFsm enters disabled state", log.Fields{
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.uniID})
	oFsm.mutexPLastTxMeInstance.Lock()
	defer oFsm.mutexPLastTxMeInstance.Unlock()
	oFsm.pLastTxMeInstance = nil
}

func (oFsm *uniPonAniConfigFsm) processOmciAniMessages(ctx context.Context) {
	logger.Debugw(ctx, "Start uniPonAniConfigFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info(ctx, "UniPonAniConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			break loop
		}
		logger.Debugw(ctx, "UniPonAniConfigFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Infow(ctx, "UniPonAniConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "UniPonAniConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oFsm.handleOmciAniConfigMessage(ctx, msg)
		default:
			logger.Warn(ctx, "UniPonAniConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}

	}
	logger.Infow(ctx, "End uniPonAniConfigFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigCreateResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Debugw(ctx, "CreateResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success || msgObj.Result == me.InstanceExists {
		//if the result is ok or Instance already exists (latest needed at least as long as we do not clear the OMCI techProfile data)
		oFsm.mutexPLastTxMeInstance.RLock()
		if oFsm.pLastTxMeInstance != nil {
			if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
				// maybe we can use just the same eventName for different state transitions like "forward"
				//   - might be checked, but so far I go for sure and have to inspect the concrete state events ...
				switch oFsm.pLastTxMeInstance.GetName() {
				case "Ieee8021PMapperServiceProfile":
					{ // let the FSM proceed ...
						oFsm.mutexPLastTxMeInstance.RUnlock()
						_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxDot1pmapCResp)
					}
				case "MacBridgePortConfigurationData":
					{ // let the FSM proceed ...
						oFsm.mutexPLastTxMeInstance.RUnlock()
						_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxMbpcdResp)
					}
				case "GemPortNetworkCtp", "GemInterworkingTerminationPoint", "MulticastGemInterworkingTerminationPoint":
					{ // let aniConfig Multi-Id processing proceed by stopping the wait function
						oFsm.mutexPLastTxMeInstance.RUnlock()
						oFsm.omciMIdsResponseReceived <- true
					}
				default:
					{
						oFsm.mutexPLastTxMeInstance.RUnlock()
						logger.Warnw(ctx, "Unsupported ME name received!",
							log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
					}
				}
			} else {
				oFsm.mutexPLastTxMeInstance.RUnlock()
			}
		} else {
			oFsm.mutexPLastTxMeInstance.RUnlock()
			logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
		}
	} else {
		logger.Errorw(ctx, "Omci CreateResponse Error - later: drive FSM to abort state ?",
			log.Fields{"Error": msgObj.Result, "device-id": oFsm.deviceID})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return
	}
}
func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigSetFailResponseMessage(ctx context.Context, msgObj *omci.SetResponse) {
	//If TCONT fails, then we need to revert the allocated TCONT in DB.
	//Because FSMs are running sequentially, we don't expect the same TCONT hit by another tech-profile FSM while this FSM is running.
	oFsm.mutexPLastTxMeInstance.RLock()
	defer oFsm.mutexPLastTxMeInstance.RUnlock()
	if oFsm.pLastTxMeInstance != nil && msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
		msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
		switch oFsm.pLastTxMeInstance.GetName() {
		case "TCont":
			//If this is for TCONT creation(requestEventOffset=0) and this is the first allocation of TCONT(so noone else is using the same TCONT)
			//We should revert DB
			if oFsm.requestEventOffset == 0 && !oFsm.tcontSetBefore && oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey] != nil {
				logger.Debugw(ctx, "UniPonAniConfigFsm TCONT creation failed on device. Freeing alloc id", log.Fields{"device-id": oFsm.deviceID,
					"alloc-id": oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].tcontParams.allocID, "uni-tp": oFsm.uniTpKey})
				if pDevEntry := oFsm.pDeviceHandler.getOnuDeviceEntry(ctx, false); pDevEntry != nil {
					pDevEntry.freeTcont(ctx, oFsm.pUniTechProf.mapPonAniConfig[oFsm.uniTpKey].tcontParams.allocID)
				} else {
					logger.Warnw(ctx, "Unable to get device entry! couldn't free tcont",
						log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
				}
			}
		default:
			logger.Warnw(ctx, "Unsupported ME name received with error!",
				log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "result": msgObj.Result, "device-id": oFsm.deviceID})
		}
	}
}
func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigSetResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci Msg layer could not be detected for SetResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.SetResponse)
	if !msgOk {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci Msg layer could not be assigned for SetResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Debugw(ctx, "UniPonAniConfigFsm SetResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci SetResponse Error - later: drive FSM to abort state ?",
			log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?

		oFsm.handleOmciAniConfigSetFailResponseMessage(ctx, msgObj)
		return
	}
	oFsm.mutexPLastTxMeInstance.RLock()
	if oFsm.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
			// if, then something like:
			//oFsm.pOnuDB.StoreMe(msgObj)

			switch oFsm.pLastTxMeInstance.GetName() {
			case "TCont":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					if oFsm.requestEventOffset == 0 { //from TCont config request
						_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxTcontsResp)
					} else { // from T-Cont reset request
						_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxResetTcontResp)
					}
				}
			case "PriorityQueue", "MulticastGemInterworkingTerminationPoint":
				{ // let the PrioQueue init proceed by stopping the wait function
					oFsm.mutexPLastTxMeInstance.RUnlock()
					oFsm.omciMIdsResponseReceived <- true
				}
			case "Ieee8021PMapperServiceProfile":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxDot1pmapSResp)
				}
			default:
				{
					oFsm.mutexPLastTxMeInstance.RUnlock()
					logger.Warnw(ctx, "Unsupported ME name received!",
						log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
				}
			}
		} else {
			oFsm.mutexPLastTxMeInstance.RUnlock()
		}
	} else {
		oFsm.mutexPLastTxMeInstance.RUnlock()
		logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
	}
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigDeleteResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeDeleteResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci Msg layer could not be detected for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.DeleteResponse)
	if !msgOk {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci Msg layer could not be assigned for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Debugw(ctx, "UniPonAniConfigFsm DeleteResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw(ctx, "UniPonAniConfigFsm - Omci DeleteResponse Error",
			log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
		//TODO:  - later: possibly force FSM into abort or ignore some errors for some messages?
		//         store error for mgmt display?
		return
	}
	oFsm.mutexPLastTxMeInstance.RLock()
	if oFsm.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			//remove ME from DB //TODO??? obviously the Python code does not store/remove the config ...
			// if, then something like: oFsm.pOnuDB.XyyMe(msgObj)

			switch oFsm.pLastTxMeInstance.GetName() {
			case "GemInterworkingTerminationPoint":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxRemGemiwResp)
				}
			case "GemPortNetworkCtp":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxRemGemntpResp)
				}
			case "TrafficDescriptor":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxRemTdResp)
				}
			case "Ieee8021PMapperServiceProfile":
				{ // let the FSM proceed ...
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxRem1pMapperResp)
				}
			case "MacBridgePortConfigurationData":
				{ // this is the last event of the T-Cont cleanup procedure, FSM may be reset here
					oFsm.mutexPLastTxMeInstance.RUnlock()
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxRemAniBPCDResp)
				}
			default:
				{
					oFsm.mutexPLastTxMeInstance.RUnlock()
					logger.Warnw(ctx, "Unsupported ME name received!",
						log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
				}
			}
		} else {
			oFsm.mutexPLastTxMeInstance.RUnlock()
		}
	} else {
		oFsm.mutexPLastTxMeInstance.RUnlock()
		logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
	}
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI UniPonAniConfigFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{
			oFsm.handleOmciAniConfigCreateResponseMessage(ctx, msg)

		} //CreateResponseType
	case omci.SetResponseType:
		{
			oFsm.handleOmciAniConfigSetResponseMessage(ctx, msg)

		} //SetResponseType
	case omci.DeleteResponseType:
		{
			oFsm.handleOmciAniConfigDeleteResponseMessage(ctx, msg)

		} //DeleteResponseType
	default:
		{
			logger.Errorw(ctx, "uniPonAniConfigFsm - Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

func (oFsm *uniPonAniConfigFsm) performCreatingGemNCTPs(ctx context.Context) {
	// for all GemPorts of this T-Cont as given by the size of set gemPortAttribsSlice
	for gemIndex, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		logger.Debugw(ctx, "uniPonAniConfigFsm Tx Create::GemNWCtp", log.Fields{
			"EntitytId": strconv.FormatInt(int64(gemPortAttribs.gemPortID), 16),
			"TcontId":   strconv.FormatInt(int64(oFsm.tcont0ID), 16),
			"device-id": oFsm.deviceID})
		meParams := me.ParamData{
			EntityID: gemPortAttribs.gemPortID, //unique, same as PortId
			Attributes: me.AttributeValueMap{
				"PortId":       gemPortAttribs.gemPortID,
				"TContPointer": oFsm.tcont0ID,
				"Direction":    gemPortAttribs.direction,
				//ONU-G.TrafficManagementOption dependency ->PrioQueue or TCont
				//  TODO!! verify dependency and QueueId in case of Multi-GemPort setup!
				"TrafficManagementPointerForUpstream": gemPortAttribs.upQueueID, //might be different in wrr-only Setup - tcont0ID
				"PriorityQueuePointerForDownStream":   gemPortAttribs.downQueueID,
			},
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.sendCreateGemNCTPVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "GemNCTPVar create failed, aborting uniPonAniConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()
		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "GemNWCtp create failed, aborting AniConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID, "GemIndex": gemIndex})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
		// Mark the gem port to be added for Performance History monitoring
		if oFsm.pDeviceHandler.pOnuMetricsMgr != nil {
			oFsm.pDeviceHandler.pOnuMetricsMgr.AddGemPortForPerfMonitoring(ctx, gemPortAttribs.gemPortID)
		}
	} //for all GemPorts of this T-Cont

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw(ctx, "GemNWCtp create loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxGemntcpsResp)
}
func (oFsm *uniPonAniConfigFsm) hasMulticastGem(ctx context.Context) bool {
	for _, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		if gemPortAttribs.isMulticast {
			logger.Debugw(ctx, "Found multicast gem", log.Fields{"device-id": oFsm.deviceID})
			return true
		}
	}
	return false
}

func (oFsm *uniPonAniConfigFsm) performCreatingGemIWs(ctx context.Context) {
	// for all GemPorts of this T-Cont as given by the size of set gemPortAttribsSlice
	for gemIndex, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		logger.Debugw(ctx, "uniPonAniConfigFsm Tx Create::GemIwTp", log.Fields{
			"EntitytId": strconv.FormatInt(int64(gemPortAttribs.gemPortID), 16),
			"SPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
			"device-id": oFsm.deviceID})

		//TODO if the port has only downstream direction the isMulticast flag can be removed.
		if gemPortAttribs.isMulticast {

			meParams := me.ParamData{
				EntityID: gemPortAttribs.multicastGemID,
				Attributes: me.AttributeValueMap{
					"GemPortNetworkCtpConnectivityPointer": gemPortAttribs.multicastGemID,
					"InterworkingOption":                   0, // Don't Care
					"ServiceProfilePointer":                0, // Don't Care
					"GalProfilePointer":                    galEthernetEID,
				},
			}
			if oFsm.pUniTechProf.multicastConfiguredForOtherUniTps(ctx, oFsm.uniTpKey) {
				logger.Debugw(ctx, "MulticastGemInterworkingTP already exist", log.Fields{"device-id": oFsm.deviceID, "multicast-gem-id": gemPortAttribs.multicastGemID})
				continue
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.sendCreateMulticastGemIWTPVar(context.TODO(), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout,
				true, oFsm.pAdaptFsm.commChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				logger.Errorw(ctx, "MulticastGemIWTPVar create failed, aborting uniPonAniConfigFsm!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				return

			}
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()
			//verify response
			err = oFsm.waitforOmciResponse(ctx)
			if err != nil {
				logger.Errorw(ctx, "MulticastGemIWTP create failed, aborting AniConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID, "GemIndex": gemIndex})
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				return
			}
			ipv4MulticastTable := make([]uint8, 12)
			//Gem Port ID
			binary.BigEndian.PutUint16(ipv4MulticastTable[0:], gemPortAttribs.multicastGemID)
			//Secondary Key
			binary.BigEndian.PutUint16(ipv4MulticastTable[2:], 0)
			// Multicast IP range start This is the 224.0.0.1 address
			binary.BigEndian.PutUint32(ipv4MulticastTable[4:], IPToInt32(net.IPv4(224, 0, 0, 0)))
			// MulticastIp range stop
			binary.BigEndian.PutUint32(ipv4MulticastTable[8:], IPToInt32(net.IPv4(239, 255, 255, 255)))

			meIPV4MCTableParams := me.ParamData{
				EntityID: gemPortAttribs.multicastGemID,
				Attributes: me.AttributeValueMap{
					"Ipv4MulticastAddressTable": ipv4MulticastTable,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meIPV4MCTableInstance, err := oFsm.pOmciCC.sendSetMulticastGemIWTPVar(context.TODO(), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout,
				true, oFsm.pAdaptFsm.commChan, meIPV4MCTableParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				logger.Errorw(ctx, "MulticastGemIWTPVar set failed, aborting uniPonAniConfigFsm!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				return
			}
			oFsm.pLastTxMeInstance = meIPV4MCTableInstance
			oFsm.mutexPLastTxMeInstance.Unlock()

		} else {
			meParams := me.ParamData{
				EntityID: gemPortAttribs.gemPortID,
				Attributes: me.AttributeValueMap{
					"GemPortNetworkCtpConnectivityPointer": gemPortAttribs.gemPortID, //same as EntityID, see above
					"InterworkingOption":                   5,                        //fixed model:: G.998 .1pMapper
					"ServiceProfilePointer":                oFsm.mapperSP0ID,
					"InterworkingTerminationPointPointer":  0, //not used with .1PMapper Mac bridge
					"GalProfilePointer":                    galEthernetEID,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.sendCreateGemIWTPVar(context.TODO(), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
				oFsm.pAdaptFsm.commChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				logger.Errorw(ctx, "GEMIWTPVar create failed, aborting uniPonAniConfigFsm!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				return
			}
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()
		}
		//verify response
		err := oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "GemTP create failed, aborting AniConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID, "GemIndex": gemIndex})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
	} //for all GemPort's of this T-Cont

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw(ctx, "GemIwTp create loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxGemiwsResp)
}

func (oFsm *uniPonAniConfigFsm) performSettingPQs(ctx context.Context) {
	//If upstream PQs were set before, then no need to set them again. Let state machine to proceed.
	if oFsm.tcontSetBefore {
		logger.Debugw(ctx, "No need to set PQs again.", log.Fields{
			"device-id": oFsm.deviceID, "tcont": oFsm.alloc0ID,
			"uni-id":         oFsm.pOnuUniPort.uniID,
			"techProfile-id": oFsm.techProfileID})
		go func(aPAFsm *AdapterFsm) {
			if aPAFsm != nil && aPAFsm.pFsm != nil {
				_ = aPAFsm.pFsm.Event(aniEvRxPrioqsResp)
			}
		}(oFsm.pAdaptFsm)
		return
	}
	const cu16StrictPrioWeight uint16 = 0xFFFF
	//find all upstream PrioQueues related to this T-Cont
	loQueueMap := ordered_map.NewOrderedMap()
	for _, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		if gemPortAttribs.isMulticast {
			logger.Debugw(ctx, "uniPonAniConfigFsm Port is Multicast, ignoring PQs", log.Fields{
				"device-id": oFsm.deviceID, "GemPort": gemPortAttribs.gemPortID,
				"prioString": gemPortAttribs.pbitString})
			continue
		}
		if gemPortAttribs.qosPolicy == "WRR" {
			if _, ok := loQueueMap.Get(gemPortAttribs.upQueueID); !ok {
				//key does not yet exist
				loQueueMap.Set(gemPortAttribs.upQueueID, uint16(gemPortAttribs.weight))
			}
		} else {
			loQueueMap.Set(gemPortAttribs.upQueueID, cu16StrictPrioWeight) //use invalid weight value to indicate SP
		}
	}

	//TODO: assumption here is that ONU data uses SP setting in the T-Cont and WRR in the TrafficScheduler
	//  if that is not the case, the reverse case could be checked and reacted accordingly or if the
	//  complete chain is not valid, then some error should be thrown and configuration can be aborted
	//  or even be finished without correct SP/WRR setting

	//TODO: search for the (WRR)trafficScheduler related to the T-Cont of this queue
	//By now assume fixed value 0x8000, which is the only announce BBSIM TrafficScheduler,
	//  even though its T-Cont seems to be wrong ...
	loTrafficSchedulerEID := 0x8000
	//for all found queues
	iter := loQueueMap.IterFunc()
	for kv, ok := iter(); ok; kv, ok = iter() {
		queueIndex := (kv.Key).(uint16)
		meParams := me.ParamData{
			EntityID:   queueIndex,
			Attributes: make(me.AttributeValueMap),
		}
		if (kv.Value).(uint16) == cu16StrictPrioWeight {
			//StrictPrio indication
			logger.Debugw(ctx, "uniPonAniConfigFsm Tx Set::PrioQueue to StrictPrio", log.Fields{
				"EntitytId": strconv.FormatInt(int64(queueIndex), 16),
				"device-id": oFsm.deviceID})
			meParams.Attributes["TrafficSchedulerPointer"] = 0 //ensure T-Cont defined StrictPrio scheduling
		} else {
			//WRR indication
			logger.Debugw(ctx, "uniPonAniConfigFsm Tx Set::PrioQueue to WRR", log.Fields{
				"EntitytId": strconv.FormatInt(int64(queueIndex), 16),
				"Weight":    kv.Value,
				"device-id": oFsm.deviceID})
			meParams.Attributes["TrafficSchedulerPointer"] = loTrafficSchedulerEID //ensure assignment of the relevant trafficScheduler
			meParams.Attributes["Weight"] = uint8(kv.Value.(uint16))
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.sendSetPrioQueueVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.pOpenOnuAc.omciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "PrioQueueVar set failed, aborting uniPonAniConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()

		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "PrioQueue set failed, aborting AniConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID, "QueueId": strconv.FormatInt(int64(queueIndex), 16)})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}

		//TODO: In case of WRR setting of the GemPort/PrioQueue it might further be necessary to
		//  write the assigned trafficScheduler with the requested Prio to be considered in the StrictPrio scheduling
		//  of the (next upstream) assigned T-Cont, which is f(prioQueue[priority]) - in relation to other SP prioQueues
		//  not yet done because of BBSIM TrafficScheduler issues (and not done in py code as well)

	} //for all upstream prioQueues

	// if Config has been done for all PrioQueue instances let the FSM proceed
	logger.Debugw(ctx, "PrioQueue set loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxPrioqsResp)
}

func (oFsm *uniPonAniConfigFsm) waitforOmciResponse(ctx context.Context) error {
	oFsm.mutexIsAwaitingResponse.Lock()
	if oFsm.isCanceled {
		// FSM already canceled before entering wait
		logger.Debugw(ctx, "uniPonAniConfigFsm wait-for-multi-entity-response aborted (on enter)", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf(cErrWaitAborted)
	}
	oFsm.isAwaitingResponse = true
	oFsm.mutexIsAwaitingResponse.Unlock()
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "UniPonAniConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isAwaitingResponse = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf("uniPonAniConfigFsm multi entity timeout %s", oFsm.deviceID)
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debugw(ctx, "uniPonAniConfigFsm multi entity response received", log.Fields{"for device-id": oFsm.deviceID})
			oFsm.mutexIsAwaitingResponse.Lock()
			oFsm.isAwaitingResponse = false
			oFsm.mutexIsAwaitingResponse.Unlock()
			return nil
		}
		// waiting was aborted (probably on external request)
		logger.Debugw(ctx, "uniPonAniConfigFsm wait-for-multi-entity-response aborted", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isAwaitingResponse = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf(cErrWaitAborted)
	}
}

func (oFsm *uniPonAniConfigFsm) setChanSet(flagValue bool) {
	oFsm.mutexChanSet.Lock()
	oFsm.chanSet = flagValue
	oFsm.mutexChanSet.Unlock()
}

func (oFsm *uniPonAniConfigFsm) isChanSet() bool {
	oFsm.mutexChanSet.RLock()
	flagValue := oFsm.chanSet
	oFsm.mutexChanSet.RUnlock()
	return flagValue
}
