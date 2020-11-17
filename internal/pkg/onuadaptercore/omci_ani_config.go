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
	"strconv"
	"time"

	"github.com/cevaris/ordered_map"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

const (
	// events of config PON ANI port FSM
	aniEvStart           = "uniEvStart"
	aniEvStartConfig     = "aniEvStartConfig"
	aniEvRxDot1pmapCResp = "aniEvRxDot1pmapCResp"
	aniEvRxMbpcdResp     = "aniEvRxMbpcdResp"
	aniEvRxTcontsResp    = "aniEvRxTcontsResp"
	aniEvRxGemntcpsResp  = "aniEvRxGemntcpsResp"
	aniEvRxGemiwsResp    = "aniEvRxGemiwsResp"
	aniEvRxPrioqsResp    = "aniEvRxPrioqsResp"
	aniEvRxDot1pmapSResp = "aniEvRxDot1pmapSResp"
	aniEvTimeoutSimple   = "aniEvTimeoutSimple"
	aniEvTimeoutMids     = "aniEvTimeoutMids"
	aniEvReset           = "aniEvReset"
	aniEvRestart         = "aniEvRestart"
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
	aniStResetting           = "aniStResetting"
)

const (
	tpIDOffset = 64
)

type ponAniGemPortAttribs struct {
	gemPortID   uint16
	upQueueID   uint16
	downQueueID uint16
	direction   uint8
	qosPolicy   string
	weight      uint8
	pbitString  string
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
	requestEvent             OnuDeviceEvent
	omciMIdsResponseReceived chan bool //separate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                *AdapterFsm
	chSuccess                chan<- uint8
	procStep                 uint8
	chanSet                  bool
	mapperSP0ID              uint16
	macBPCD0ID               uint16
	tcont0ID                 uint16
	alloc0ID                 uint16
	gemPortAttribsSlice      []ponAniGemPortAttribs
	pLastTxMeInstance        *me.ManagedEntity
}

//newUniPonAniConfigFsm is the 'constructor' for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
func newUniPonAniConfigFsm(apDevOmciCC *omciCC, apUniPort *onuUniPort, apUniTechProf *onuUniTechProf,
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
	}
	instFsm.pAdaptFsm = NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw("uniPonAniConfigFsm's AdapterFsm could not be instantiated!!", log.Fields{
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

			{Name: aniEvTimeoutSimple, Src: []string{
				aniStCreatingDot1PMapper, aniStCreatingMBPCD, aniStSettingTconts, aniStSettingDot1PMapper}, Dst: aniStStarting},
			{Name: aniEvTimeoutMids, Src: []string{
				aniStCreatingGemNCTPs, aniStCreatingGemIWs, aniStSettingPQs}, Dst: aniStStarting},

			// exceptional treatment for all states except aniStResetting
			{Name: aniEvReset, Src: []string{aniStStarting, aniStCreatingDot1PMapper, aniStCreatingMBPCD,
				aniStSettingTconts, aniStCreatingGemNCTPs, aniStCreatingGemIWs, aniStSettingPQs, aniStSettingDot1PMapper,
				aniStConfigDone}, Dst: aniStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: aniEvRestart, Src: []string{aniStResetting}, Dst: aniStDisabled},
		},

		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(e) },
			("enter_" + aniStStarting):            func(e *fsm.Event) { instFsm.enterConfigStartingState(e) },
			("enter_" + aniStCreatingDot1PMapper): func(e *fsm.Event) { instFsm.enterCreatingDot1PMapper(e) },
			("enter_" + aniStCreatingMBPCD):       func(e *fsm.Event) { instFsm.enterCreatingMBPCD(e) },
			("enter_" + aniStSettingTconts):       func(e *fsm.Event) { instFsm.enterSettingTconts(e) },
			("enter_" + aniStCreatingGemNCTPs):    func(e *fsm.Event) { instFsm.enterCreatingGemNCTPs(e) },
			("enter_" + aniStCreatingGemIWs):      func(e *fsm.Event) { instFsm.enterCreatingGemIWs(e) },
			("enter_" + aniStSettingPQs):          func(e *fsm.Event) { instFsm.enterSettingPQs(e) },
			("enter_" + aniStSettingDot1PMapper):  func(e *fsm.Event) { instFsm.enterSettingDot1PMapper(e) },
			("enter_" + aniStConfigDone):          func(e *fsm.Event) { instFsm.enterAniConfigDone(e) },
			("enter_" + aniStResetting):           func(e *fsm.Event) { instFsm.enterResettingState(e) },
			("enter_" + aniStDisabled):            func(e *fsm.Event) { instFsm.enterDisabledState(e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw("uniPonAniConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Infow("uniPonAniConfigFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//setFsmCompleteChannel sets the requested channel and channel result for transfer on success
func (oFsm *uniPonAniConfigFsm) setFsmCompleteChannel(aChSuccess chan<- uint8, aProcStep uint8) {
	oFsm.chSuccess = aChSuccess
	oFsm.procStep = aProcStep
	oFsm.chanSet = true
}

func (oFsm *uniPonAniConfigFsm) prepareAndEnterConfigState(aPAFsm *AdapterFsm) {
	if aPAFsm != nil && aPAFsm.pFsm != nil {
		uniTpKey := uniTP{uniID: oFsm.pOnuUniPort.uniID, tpID: oFsm.techProfileID}
		//stick to pythonAdapter numbering scheme
		//index 0 in naming refers to possible usage of multiple instances (later)
		oFsm.mapperSP0ID = ieeeMapperServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo) + uint16(oFsm.techProfileID)
		oFsm.macBPCD0ID = macBridgePortAniEID + uint16(oFsm.pOnuUniPort.entityID) + uint16(oFsm.techProfileID)

		/*
			// Find a free TCONT Instance ID and use it
			foundFreeTcontInstID := false
		*/
		if tcontInstKeys := oFsm.pOnuDB.getSortedInstKeys(me.TContClassID); len(tcontInstKeys) > 0 {

			// FIXME: Ideally the ME configurations on the ONU should constantly be MIB Synced back to the ONU DB
			// So, as soon as we use up a TCONT Entity on the ONU, the DB at ONU adapter should know that the TCONT
			// entity is used up via MIB Sync procedure and it will not use it for subsequent TCONT on that ONU.
			// But, it seems, due to the absence of the constant mib-sync procedure, the TCONT Entities show up as
			// free even though they are already reserved on the ONU. It seems the mib is synced only once, initially
			// when the ONU is discovered.
			/*
				for _, tcontInstID := range tcontInstKeys {
					tconInst := oFsm.pOnuDB.GetMe(me.TContClassID, tcontInstID)
					returnVal := tconInst["AllocId"]
					if returnVal != nil {
						if allocID, err := oFsm.pOnuDB.getUint16Attrib(returnVal); err == nil {
							// If the TCONT Instance ID is set to 0xff or 0xffff, it means it is free to use.
							if allocID == 0xff || allocID == 0xffff {
								foundFreeTcontInstID = true
								oFsm.tcont0ID = uint16(tcontInstID)
								logger.Debugw("Used TcontId:", log.Fields{"TcontId": strconv.FormatInt(int64(oFsm.tcont0ID), 16),
									"device-id": oFsm.deviceID})
								break
							}
						} else {
							logger.Errorw("error-converting-alloc-id-to-uint16", log.Fields{"device-id": oFsm.deviceID, "tcont-inst": tcontInstID})
						}
					} else {
						logger.Errorw("error-extracting-alloc-id-attribute", log.Fields{"device-id": oFsm.deviceID, "tcont-inst": tcontInstID})
					}
				}
			*/

			// Ensure that the techProfileID is in a valid range so that we can allocate a free Tcont for it.
			if oFsm.techProfileID >= tpIDOffset && oFsm.techProfileID < uint8(tpIDOffset+len(tcontInstKeys)) {
				// For now, as a dirty workaround, use the tpIDOffset to index the TcontEntityID to be used.
				// The first TP ID for the ONU will get the first TcontEntityID, the next will get second and so on.
				// Here the assumption is TP ID will always start from 64 (this is also true to Technology Profile Specification) and the
				// TP ID will increment in single digit
				oFsm.tcont0ID = tcontInstKeys[oFsm.techProfileID-tpIDOffset]
				logger.Debugw("Used TcontId:", log.Fields{"TcontId": strconv.FormatInt(int64(oFsm.tcont0ID), 16),
					"device-id": oFsm.deviceID})
			} else {
				logger.Errorw("tech profile id not in valid range", log.Fields{"device-id": oFsm.deviceID, "tp-id": oFsm.techProfileID, "num-tcont": len(tcontInstKeys)})
				return
			}
		} else {
			logger.Errorw("No TCont instances found", log.Fields{"device-id": oFsm.deviceID})
			return
		}
		/*
			if !foundFreeTcontInstID {
				// This should never happen. If it does, the behavior is unpredictable.
				logger.Warnw("No free TCONT instances found", log.Fields{"device-id": oFsm.deviceID})
			}*/

		// Access critical state with lock
		oFsm.pUniTechProf.mutexTPState.Lock()
		oFsm.alloc0ID = oFsm.pUniTechProf.mapPonAniConfig[uniTpKey].tcontParams.allocID
		mapGemPortParams := oFsm.pUniTechProf.mapPonAniConfig[uniTpKey].mapGemPortParams
		oFsm.pUniTechProf.mutexTPState.Unlock()

		loGemPortAttribs := ponAniGemPortAttribs{}
		//for all TechProfile set GemIndices

		for _, gemEntry := range mapGemPortParams {
			//collect all GemConfigData in a separate Fsm related slice (needed also to avoid mix-up with unsorted mapPonAniConfig)

			if queueInstKeys := oFsm.pOnuDB.getSortedInstKeys(me.PriorityQueueClassID); len(queueInstKeys) > 0 {

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
									logger.Debugw("UpQueue for GemPort found:", log.Fields{"gemPortID": loGemPortAttribs.gemPortID,
										"upQueueID": strconv.FormatInt(int64(loGemPortAttribs.upQueueID), 16), "device-id": oFsm.deviceID})
									usQueueFound = true
								} else if (relatedPort&0xFFFFFF) == dsQrelPortMask && mgmtEntityID < 0x8000 {
									loGemPortAttribs.downQueueID = mgmtEntityID
									logger.Debugw("DownQueue for GemPort found:", log.Fields{"gemPortID": loGemPortAttribs.gemPortID,
										"downQueueID": strconv.FormatInt(int64(loGemPortAttribs.downQueueID), 16), "device-id": oFsm.deviceID})
									dsQueueFound = true
								}
								if usQueueFound && dsQueueFound {
									break
								}
							} else {
								logger.Warnw("Could not convert attribute value", log.Fields{"device-id": oFsm.deviceID})
							}
						} else {
							logger.Warnw("'RelatedPort' not found in meAttributes:", log.Fields{"device-id": oFsm.deviceID})
						}
					} else {
						logger.Warnw("No attributes available in DB:", log.Fields{"meClassID": me.PriorityQueueClassID,
							"mgmtEntityID": mgmtEntityID, "device-id": oFsm.deviceID})
					}
				}
			} else {
				logger.Warnw("No PriorityQueue instances found", log.Fields{"device-id": oFsm.deviceID})
			}
			loGemPortAttribs.direction = gemEntry.direction
			loGemPortAttribs.qosPolicy = gemEntry.queueSchedPolicy
			loGemPortAttribs.weight = gemEntry.queueWeight
			loGemPortAttribs.pbitString = gemEntry.pbitString

			logger.Debugw("prio-related GemPort attributes:", log.Fields{
				"gemPortID":      loGemPortAttribs.gemPortID,
				"upQueueID":      loGemPortAttribs.upQueueID,
				"downQueueID":    loGemPortAttribs.downQueueID,
				"pbitString":     loGemPortAttribs.pbitString,
				"prioQueueIndex": gemEntry.prioQueueIndex,
			})

			oFsm.gemPortAttribsSlice = append(oFsm.gemPortAttribsSlice, loGemPortAttribs)
		}
		_ = aPAFsm.pFsm.Event(aniEvStartConfig)
	}
}

func (oFsm *uniPonAniConfigFsm) enterConfigStartingState(e *fsm.Event) {
	logger.Debugw("UniPonAniConfigFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})
	// in case the used channel is not yet defined (can be re-used after restarts)
	if oFsm.omciMIdsResponseReceived == nil {
		oFsm.omciMIdsResponseReceived = make(chan bool)
		logger.Debug("uniPonAniConfigFsm - OMCI multiInstance RxChannel defined")
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

	// start go routine for processing of LockState messages
	go oFsm.processOmciAniMessages()

	//let the state machine run forward from here directly
	pConfigAniStateAFsm := oFsm.pAdaptFsm
	if pConfigAniStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go oFsm.prepareAndEnterConfigState(pConfigAniStateAFsm)

	}
}

func (oFsm *uniPonAniConfigFsm) enterCreatingDot1PMapper(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm Tx Create::Dot1PMapper", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
	meInstance := oFsm.pOmciCC.sendCreateDot1PMapper(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.mapperSP0ID, oFsm.pAdaptFsm.commChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *uniPonAniConfigFsm) enterCreatingMBPCD(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm Tx Create::MBPCD", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.macBPCD0ID), 16),
		"TPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
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
	oFsm.pLastTxMeInstance = meInstance

}

func (oFsm *uniPonAniConfigFsm) enterSettingTconts(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm Tx Set::Tcont", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.tcont0ID), 16),
		"AllocId":   strconv.FormatInt(int64(oFsm.alloc0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
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
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *uniPonAniConfigFsm) enterCreatingGemNCTPs(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm - start creating GemNWCtp loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	go oFsm.performCreatingGemNCTPs()
}

func (oFsm *uniPonAniConfigFsm) enterCreatingGemIWs(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm - start creating GemIwTP loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	go oFsm.performCreatingGemIWs()
}

func (oFsm *uniPonAniConfigFsm) enterSettingPQs(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm - start setting PrioQueue loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	go oFsm.performSettingPQs()
}

func (oFsm *uniPonAniConfigFsm) enterSettingDot1PMapper(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm Tx Set::.1pMapper with all PBits set", log.Fields{"EntitytId": 0x8042, /*cmp above*/
		"toGemIw": 1024 /* cmp above */, "in state": e.FSM.Current(), "device-id": oFsm.deviceID})

	logger.Debugw("uniPonAniConfigFsm Tx Set::1pMapper", log.Fields{
		"EntitytId": strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
		"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})

	meParams := me.ParamData{
		EntityID:   oFsm.mapperSP0ID,
		Attributes: make(me.AttributeValueMap),
	}

	//assign the GemPorts according to the configured Prio
	var loPrioGemPortArray [8]uint16
	for _, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		for i := 0; i < 8; i++ {
			// "lenOfPbitMap(8) - i + 1" will give i-th pbit value from LSB position in the pbit map string
			if prio, err := strconv.Atoi(string(gemPortAttribs.pbitString[7-i])); err == nil {
				if prio == 1 { // Check this p-bit is set
					if loPrioGemPortArray[i] == 0 {
						loPrioGemPortArray[i] = gemPortAttribs.gemPortID //gemPortId=EntityID and unique
					} else {
						logger.Warnw("uniPonAniConfigFsm PrioString not unique", log.Fields{
							"device-id": oFsm.deviceID, "IgnoredGemPort": gemPortAttribs.gemPortID,
							"SetGemPort": loPrioGemPortArray[i]})
					}
				}
			} else {
				logger.Warnw("uniPonAniConfigFsm PrioString evaluation error", log.Fields{
					"device-id": oFsm.deviceID, "GemPort": gemPortAttribs.gemPortID,
					"prioString": gemPortAttribs.pbitString, "position": i})
			}

		}
	}
	var foundIwPtr bool = false
	for index, value := range loPrioGemPortArray {
		if value != 0 {
			foundIwPtr = true
			meAttribute := fmt.Sprintf("InterworkTpPointerForPBitPriority%d", index)
			meParams.Attributes[meAttribute] = value
			logger.Debugw("UniPonAniConfigFsm Set::1pMapper", log.Fields{
				"for Prio":  index,
				"IwPtr":     strconv.FormatInt(int64(value), 16),
				"device-id": oFsm.deviceID})
		}
	}

	if !foundIwPtr {
		logger.Errorw("UniPonAniConfigFsm no GemIwPtr found for .1pMapper - abort", log.Fields{
			"device-id": oFsm.deviceID})
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

	meInstance := oFsm.pOmciCC.sendSetDot1PMapperVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *uniPonAniConfigFsm) enterAniConfigDone(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm send dh event notification recheck pending flow config", log.Fields{
		"from_State": e.FSM.Current(), "device-id": oFsm.deviceID})
	//use DeviceHandler event notification directly
	oFsm.pDeviceHandler.deviceProcStatusUpdate(oFsm.requestEvent)
	//store that the UNI related techProfile processing is done for the given Profile and Uni
	oFsm.pUniTechProf.setConfigDone(oFsm.pOnuUniPort.uniID, oFsm.techProfileID, true)
	//if techProfile processing is done it must be checked, if some prior/parallel flow configuration is pending
	go oFsm.pDeviceHandler.verifyUniVlanConfigRequest(oFsm.pOnuUniPort)

	if oFsm.chanSet {
		// indicate processing done to the caller
		logger.Debugw("uniPonAniConfigFsm processingDone on channel", log.Fields{
			"ProcessingStep": oFsm.procStep, "from_State": e.FSM.Current(), "device-id": oFsm.deviceID})
		oFsm.chSuccess <- oFsm.procStep
		oFsm.chanSet = false //reset the internal channel state
	}

	//the FSM is left active in this state as long as no specific reset or remove is requested from outside
}

func (oFsm *uniPonAniConfigFsm) enterResettingState(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm resetting", log.Fields{"device-id": oFsm.deviceID})

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

func (oFsm *uniPonAniConfigFsm) enterDisabledState(e *fsm.Event) {
	logger.Debugw("uniPonAniConfigFsm enters disabled state", log.Fields{"device-id": oFsm.deviceID})
	oFsm.pLastTxMeInstance = nil

	//remove all TechProf related internal data to allow for new configuration (e.g. with disable/enable procedure)
	oFsm.pUniTechProf.clearAniSideConfig(oFsm.pOnuUniPort.uniID, oFsm.techProfileID)
}

func (oFsm *uniPonAniConfigFsm) processOmciAniMessages( /*ctx context.Context*/ ) {
	logger.Debugw("Start uniPonAniConfigFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info("UniPonAniConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			break loop
		}
		logger.Debugw("UniPonAniConfigFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Infow("UniPonAniConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw("UniPonAniConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oFsm.handleOmciAniConfigMessage(msg)
		default:
			logger.Warn("UniPonAniConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}

	}
	logger.Infow("End uniPonAniConfigFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigCreateResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw("Omci Msg layer could not be detected for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw("Omci Msg layer could not be assigned for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Debugw("CreateResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success || msgObj.Result == me.InstanceExists {
		//if the result is ok or Instance already exists (latest needed at least as long as we do not clear the OMCI techProfile data)
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			// maybe we can use just the same eventName for different state transitions like "forward"
			//   - might be checked, but so far I go for sure and have to inspect the concrete state events ...
			switch oFsm.pLastTxMeInstance.GetName() {
			case "Ieee8021PMapperServiceProfile":
				{ // let the FSM proceed ...
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxDot1pmapCResp)
				}
			case "MacBridgePortConfigurationData":
				{ // let the FSM proceed ...
					_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxMbpcdResp)
				}
			case "GemPortNetworkCtp", "GemInterworkingTerminationPoint":
				{ // let aniConfig Multi-Id processing proceed by stopping the wait function
					oFsm.omciMIdsResponseReceived <- true
				}
			}
		}
	} else {
		logger.Errorw("Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return
	}
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigSetResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
	if msgLayer == nil {
		logger.Errorw("UniPonAniConfigFsm - Omci Msg layer could not be detected for SetResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.SetResponse)
	if !msgOk {
		logger.Errorw("UniPonAniConfigFsm - Omci Msg layer could not be assigned for SetResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Debugw("UniPonAniConfigFsm SetResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw("UniPonAniConfigFsm - Omci SetResponse Error - later: drive FSM to abort state ?",
			log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return
	}
	if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
		msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
		//store the created ME into DB //TODO??? obviously the Python code does not store the config ...
		// if, then something like:
		//oFsm.pOnuDB.StoreMe(msgObj)

		switch oFsm.pLastTxMeInstance.GetName() {
		case "TCont":
			{ // let the FSM proceed ...
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxTcontsResp)
			}
		case "PriorityQueue":
			{ // let the PrioQueue init proceed by stopping the wait function
				oFsm.omciMIdsResponseReceived <- true
			}
		case "Ieee8021PMapperServiceProfile":
			{ // let the FSM proceed ...
				_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxDot1pmapSResp)
			}
		}
	}
}

func (oFsm *uniPonAniConfigFsm) handleOmciAniConfigMessage(msg OmciMessage) {
	logger.Debugw("Rx OMCI UniPonAniConfigFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{
			oFsm.handleOmciAniConfigCreateResponseMessage(msg)

		} //CreateResponseType
	case omci.SetResponseType:
		{
			oFsm.handleOmciAniConfigSetResponseMessage(msg)

		} //SetResponseType
	default:
		{
			logger.Errorw("uniPonAniConfigFsm - Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

func (oFsm *uniPonAniConfigFsm) performCreatingGemNCTPs() {
	// for all GemPorts of this T-Cont as given by the size of set gemPortAttribsSlice
	for gemIndex, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		logger.Debugw("uniPonAniConfigFsm Tx Create::GemNWCtp", log.Fields{
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
		meInstance := oFsm.pOmciCC.sendCreateGemNCTPVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("GemNWCtp create failed, aborting AniConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID, "GemIndex": gemIndex})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
	} //for all GemPorts of this T-Cont

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("GemNWCtp create loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxGemntcpsResp)
}

func (oFsm *uniPonAniConfigFsm) performCreatingGemIWs() {
	// for all GemPorts of this T-Cont as given by the size of set gemPortAttribsSlice
	for gemIndex, gemPortAttribs := range oFsm.gemPortAttribsSlice {
		logger.Debugw("uniPonAniConfigFsm Tx Create::GemIwTp", log.Fields{
			"EntitytId": strconv.FormatInt(int64(gemPortAttribs.gemPortID), 16),
			"SPPtr":     strconv.FormatInt(int64(oFsm.mapperSP0ID), 16),
			"device-id": oFsm.deviceID})
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
		meInstance := oFsm.pOmciCC.sendCreateGemIWTPVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("GemIwTp create failed, aborting AniConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID, "GemIndex": gemIndex})
			_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
			return
		}
	} //for all GemPort's of this T-Cont

	// if Config has been done for all GemPort instances let the FSM proceed
	logger.Debugw("GemIwTp create loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxGemiwsResp)
}

func (oFsm *uniPonAniConfigFsm) performSettingPQs() {
	const cu16StrictPrioWeight uint16 = 0xFFFF
	//find all upstream PrioQueues related to this T-Cont
	loQueueMap := ordered_map.NewOrderedMap()
	for _, gemPortAttribs := range oFsm.gemPortAttribsSlice {
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
			logger.Debugw("uniPonAniConfigFsm Tx Set::PrioQueue to StrictPrio", log.Fields{
				"EntitytId": strconv.FormatInt(int64(queueIndex), 16),
				"device-id": oFsm.deviceID})
			meParams.Attributes["TrafficSchedulerPointer"] = 0 //ensure T-Cont defined StrictPrio scheduling
		} else {
			//WRR indication
			logger.Debugw("uniPonAniConfigFsm Tx Set::PrioQueue to WRR", log.Fields{
				"EntitytId": strconv.FormatInt(int64(queueIndex), 16),
				"Weight":    kv.Value,
				"device-id": oFsm.deviceID})
			meParams.Attributes["TrafficSchedulerPointer"] = loTrafficSchedulerEID //ensure assignment of the relevant trafficScheduler
			meParams.Attributes["Weight"] = uint8(kv.Value.(uint16))
		}
		meInstance := oFsm.pOmciCC.sendSetPrioQueueVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("PrioQueue set failed, aborting AniConfig FSM!",
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
	logger.Debugw("PrioQueue set loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(aniEvRxPrioqsResp)
}

func (oFsm *uniPonAniConfigFsm) waitforOmciResponse() error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(30 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw("UniPonAniConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("uniPonAniConfigFsm multi entity timeout %s", oFsm.deviceID)
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debug("uniPonAniConfigFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("uniPonAniConfigFsm multi entity response error", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("uniPonAniConfigFsm multi entity responseError %s", oFsm.deviceID)
	}
}
