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

	gp "github.com/google/gopacket"
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
	cVtfdTableSize         = 12             //as per G.988
	cMaxAllowedFlows       = cVtfdTableSize //which might be under discussion, for the moment connected to limit of VLAN's within VTFD
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
	vlanEvStart           = "vlanEvStart"
	vlanEvWaitTechProf    = "vlanEvWaitTechProf"
	vlanEvContinueConfig  = "vlanEvContinueConfig"
	vlanEvStartConfig     = "vlanEvStartConfig"
	vlanEvRxConfigVtfd    = "vlanEvRxConfigVtfd"
	vlanEvRxConfigEvtocd  = "vlanEvRxConfigEvtocd"
	vlanEvIncrFlowConfig  = "vlanEvIncrFlowConfig"
	vlanEvRenew           = "vlanEvRenew"
	vlanEvRemFlowConfig   = "vlanEvRemFlowConfig"
	vlanEvRemFlowDone     = "vlanEvRemFlowDone"
	vlanEvFlowDataRemoved = "vlanEvFlowDataRemoved"
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
	vlanStRemoveFlow      = "vlanStRemoveFlow"
	vlanStCleanupDone     = "vlanStCleanupDone"
	vlanStResetting       = "vlanStResetting"
)

type uniVlanRuleParams struct {
	TpID         uint16 `json:"tp_id"`
	MatchVid     uint32 `json:"match_vid"` //use uint32 types for allowing immediate bitshifting
	MatchPcp     uint32 `json:"match_pcp"`
	TagsToRemove uint32 `json:"tags_to_remove"`
	SetVid       uint32 `json:"set_vid"`
	SetPcp       uint32 `json:"set_pcp"`
}

type uniVlanFlowParams struct {
	CookieSlice    []uint64          `json:"cookie_slice"`
	VlanRuleParams uniVlanRuleParams `json:"vlan_rule_params"`
}

type uniRemoveVlanFlowParams struct {
	cookie         uint64 //just the last cookie valid for removal
	vlanRuleParams uniVlanRuleParams
}

//UniVlanConfigFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type UniVlanConfigFsm struct {
	pDeviceHandler              *deviceHandler
	deviceID                    string
	pOmciCC                     *omciCC
	pOnuUniPort                 *onuUniPort
	pUniTechProf                *onuUniTechProf
	pOnuDB                      *onuDeviceDB
	techProfileID               uint16
	requestEvent                OnuDeviceEvent
	omciMIdsResponseReceived    chan bool //seperate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                   *AdapterFsm
	acceptIncrementalEvtoOption bool
	clearPersistency            bool
	mutexFlowParams             sync.Mutex
	uniVlanFlowParamsSlice      []uniVlanFlowParams
	uniRemoveFlowsSlice         []uniRemoveVlanFlowParams
	numUniFlows                 uint8 // expected number of flows should be less than 12
	configuredUniFlow           uint8
	numRemoveFlows              uint8
	numVlanFilterEntries        uint8
	vlanFilterList              [cVtfdTableSize]uint16
	vtfdID                      uint16
	evtocdID                    uint16
	pLastTxMeInstance           *me.ManagedEntity
	requestEventOffset          uint8
}

//NewUniVlanConfigFsm is the 'constructor' for the state machine to config the PON ANI ports
//  of ONU UNI ports via OMCI
func NewUniVlanConfigFsm(apDeviceHandler *deviceHandler, apDevOmciCC *omciCC, apUniPort *onuUniPort,
	apUniTechProf *onuUniTechProf, apOnuDB *onuDeviceDB, aTechProfileID uint16,
	aRequestEvent OnuDeviceEvent, aName string, aCommChannel chan Message, aAcceptIncrementalEvto bool,
	aCookieSlice []uint64, aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8) *UniVlanConfigFsm {
	instFsm := &UniVlanConfigFsm{
		pDeviceHandler:              apDeviceHandler,
		deviceID:                    apDeviceHandler.deviceID,
		pOmciCC:                     apDevOmciCC,
		pOnuUniPort:                 apUniPort,
		pUniTechProf:                apUniTechProf,
		pOnuDB:                      apOnuDB,
		techProfileID:               aTechProfileID,
		requestEvent:                aRequestEvent,
		acceptIncrementalEvtoOption: aAcceptIncrementalEvto,
		numUniFlows:                 0,
		configuredUniFlow:           0,
		numRemoveFlows:              0,
		clearPersistency:            true,
	}

	instFsm.pAdaptFsm = NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw("UniVlanConfigFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
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
			{Name: vlanEvRenew, Src: []string{vlanStConfigIncrFlow}, Dst: vlanStStarting},
			{Name: vlanEvRemFlowConfig, Src: []string{vlanStConfigDone}, Dst: vlanStRemoveFlow},
			{Name: vlanEvRemFlowDone, Src: []string{vlanStRemoveFlow}, Dst: vlanStCleanupDone},
			{Name: vlanEvFlowDataRemoved, Src: []string{vlanStCleanupDone}, Dst: vlanStConfigDone},
			/*
				{Name: vlanEvTimeoutSimple, Src: []string{
					vlanStCreatingDot1PMapper, vlanStCreatingMBPCD, vlanStSettingTconts, vlanStSettingDot1PMapper}, Dst: vlanStStarting},
				{Name: vlanEvTimeoutMids, Src: []string{
					vlanStCreatingGemNCTPs, vlanStCreatingGemIWs, vlanStSettingPQs}, Dst: vlanStStarting},
			*/
			// exceptional treatment for all states except vlanStResetting
			{Name: vlanEvReset, Src: []string{vlanStStarting, vlanStWaitingTechProf,
				vlanStConfigVtfd, vlanStConfigEvtocd, vlanStConfigDone, vlanStConfigIncrFlow,
				vlanStRemoveFlow, vlanStCleanupDone},
				Dst: vlanStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: vlanEvRestart, Src: []string{vlanStResetting}, Dst: vlanStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                   func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(e) },
			"enter_" + vlanStStarting:       func(e *fsm.Event) { instFsm.enterConfigStarting(e) },
			"enter_" + vlanStConfigVtfd:     func(e *fsm.Event) { instFsm.enterConfigVtfd(e) },
			"enter_" + vlanStConfigEvtocd:   func(e *fsm.Event) { instFsm.enterConfigEvtocd(e) },
			"enter_" + vlanStConfigDone:     func(e *fsm.Event) { instFsm.enterVlanConfigDone(e) },
			"enter_" + vlanStConfigIncrFlow: func(e *fsm.Event) { instFsm.enterConfigIncrFlow(e) },
			"enter_" + vlanStRemoveFlow:     func(e *fsm.Event) { instFsm.enterRemoveFlow(e) },
			"enter_" + vlanStCleanupDone:    func(e *fsm.Event) { instFsm.enterVlanCleanupDone(e) },
			"enter_" + vlanStResetting:      func(e *fsm.Event) { instFsm.enterResetting(e) },
			"enter_" + vlanStDisabled:       func(e *fsm.Event) { instFsm.enterDisabled(e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw("UniVlanConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	_ = instFsm.initUniFlowParams(aTechProfileID, aCookieSlice, aMatchVlan, aSetVlan, aSetPcp)

	logger.Debugw("UniVlanConfigFsm created", log.Fields{"device-id": instFsm.deviceID,
		"accIncrEvto": instFsm.acceptIncrementalEvtoOption})
	return instFsm
}

//initUniFlowParams is a simplified form of SetUniFlowParams() used for first flow parameters configuration
func (oFsm *UniVlanConfigFsm) initUniFlowParams(aTpID uint16, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8) error {
	loRuleParams := uniVlanRuleParams{
		TpID:     aTpID,
		MatchVid: uint32(aMatchVlan),
		SetVid:   uint32(aSetVlan),
		SetPcp:   uint32(aSetPcp),
	}
	// some automatic adjustments on the filter/treat parameters as not specifically configured/ensured by flow configuration parameters
	loRuleParams.TagsToRemove = 1            //one tag to remove as default setting
	loRuleParams.MatchPcp = cPrioDoNotFilter // do not Filter on prio as default

	if loRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
		loRuleParams.MatchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		//TODO!!: maybe be needed to be re-checked at flow deletion (but assume all flows are always deleted togehther)
	} else {
		if !oFsm.acceptIncrementalEvtoOption {
			//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
			loRuleParams.MatchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		}
	}

	if loRuleParams.MatchVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// no prio/vid filtering requested
		loRuleParams.TagsToRemove = 0          //no tag pop action
		loRuleParams.MatchPcp = cPrioIgnoreTag // no vlan tag filtering
		if loRuleParams.SetPcp == cCopyPrioFromInner {
			//in case of no filtering and configured PrioCopy ensure default prio setting to 0
			// which is required for stacking of untagged, but obviously also ensures prio setting for prio/singletagged
			// might collide with NoMatchVid/CopyPrio(/setVid) setting
			// this was some precondition setting taken over from py adapter ..
			loRuleParams.SetPcp = 0
		}
	}

	loFlowParams := uniVlanFlowParams{VlanRuleParams: loRuleParams}
	loFlowParams.CookieSlice = make([]uint64, 0)
	loFlowParams.CookieSlice = append(loFlowParams.CookieSlice, aCookieSlice...)

	//no mutex protection is required for initial access and adding the first flow is always possible
	oFsm.uniVlanFlowParamsSlice = make([]uniVlanFlowParams, 0)
	oFsm.uniVlanFlowParamsSlice = append(oFsm.uniVlanFlowParamsSlice, loFlowParams)
	logger.Debugw("first UniVlanConfigFsm flow added", log.Fields{
		"Cookies":   oFsm.uniVlanFlowParamsSlice[0].CookieSlice,
		"MatchVid":  strconv.FormatInt(int64(loRuleParams.MatchVid), 16),
		"SetVid":    strconv.FormatInt(int64(loRuleParams.SetVid), 16),
		"SetPcp":    loRuleParams.SetPcp,
		"device-id": oFsm.deviceID})
	oFsm.numUniFlows = 1
	oFsm.uniRemoveFlowsSlice = make([]uniRemoveVlanFlowParams, 0) //initially nothing to remove

	//permanently store flow config for reconcile case
	if err := oFsm.pDeviceHandler.storePersUniFlowConfig(oFsm.pOnuUniPort.uniID,
		&oFsm.uniVlanFlowParamsSlice); err != nil {
		logger.Errorw(err.Error(), log.Fields{"device-id": oFsm.deviceID})
		return err
	}

	return nil
}

//RequestClearPersistency sets the internal flag to not clear persistency data (false=NoClear)
func (oFsm *UniVlanConfigFsm) RequestClearPersistency(aClear bool) {
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	oFsm.clearPersistency = aClear
}

//SetUniFlowParams verifies on existence of flow parameters to be configured,
// optionally udates the cookie list or appends a new flow if there is space
// if possible the FSM is trigggerd to start with the processing
func (oFsm *UniVlanConfigFsm) SetUniFlowParams(aTpID uint16, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8) error {
	loRuleParams := uniVlanRuleParams{
		TpID:     aTpID,
		MatchVid: uint32(aMatchVlan),
		SetVid:   uint32(aSetVlan),
		SetPcp:   uint32(aSetPcp),
	}
	// some automatic adjustments on the filter/treat parameters as not specifically configured/ensured by flow configuration parameters
	loRuleParams.TagsToRemove = 1            //one tag to remove as default setting
	loRuleParams.MatchPcp = cPrioDoNotFilter // do not Filter on prio as default

	if loRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
		loRuleParams.MatchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		//TODO!!: maybe be needed to be re-checked at flow deletion (but assume all flows are always deleted togehther)
	} else {
		if !oFsm.acceptIncrementalEvtoOption {
			//then matchVlan is don't care and should be overwritten to 'transparent' here to avoid unneeded multiple flow entries
			loRuleParams.MatchVid = uint32(of.OfpVlanId_OFPVID_PRESENT)
		}
	}

	if loRuleParams.MatchVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// no prio/vid filtering requested
		loRuleParams.TagsToRemove = 0          //no tag pop action
		loRuleParams.MatchPcp = cPrioIgnoreTag // no vlan tag filtering
		if loRuleParams.SetPcp == cCopyPrioFromInner {
			//in case of no filtering and configured PrioCopy ensure default prio setting to 0
			// which is required for stacking of untagged, but obviously also ensures prio setting for prio/singletagged
			// might collide with NoMatchVid/CopyPrio(/setVid) setting
			// this was some precondition setting taken over from py adapter ..
			loRuleParams.SetPcp = 0
		}
	}

	flowEntryMatch := false
	flowCookieModify := false
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		//TODO: Verify if using e.g. hashes for the structures here for comparison may generate
		//  countable run time optimization (perhaps with including the hash in kvStore storage?)
		if storedUniFlowParams.VlanRuleParams == loRuleParams {
			flowEntryMatch = true
			logger.Debugw("UniVlanConfigFsm flow setting - rule already exists", log.Fields{
				"device-id": oFsm.deviceID})
			var cookieMatch bool
			for _, newCookie := range aCookieSlice { // for all cookies available in the arguments
				cookieMatch = false
				for _, cookie := range storedUniFlowParams.CookieSlice {
					if cookie == newCookie {
						logger.Debugw("UniVlanConfigFsm flow setting - and cookie already exists", log.Fields{
							"device-id": oFsm.deviceID, "cookie": cookie})
						cookieMatch = true
						break //found new cookie - no further search for this requested cookie
					}
				}
				if !cookieMatch {
					logger.Debugw("UniVlanConfigFsm flow setting -adding new cookie", log.Fields{
						"device-id": oFsm.deviceID, "cookie": newCookie})
					//as range works with copies of the slice we have to write to the original slice!!
					oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = append(oFsm.uniVlanFlowParamsSlice[flow].CookieSlice,
						newCookie)
					flowCookieModify = true
				}
			} //for all new cookies
			break // found rule - no further rule search
		}
	}
	if !flowEntryMatch { //it is a new rule
		if oFsm.numUniFlows < cMaxAllowedFlows {
			loFlowParams := uniVlanFlowParams{VlanRuleParams: loRuleParams}
			loFlowParams.CookieSlice = make([]uint64, 0)
			loFlowParams.CookieSlice = append(loFlowParams.CookieSlice, aCookieSlice...)
			oFsm.uniVlanFlowParamsSlice = append(oFsm.uniVlanFlowParamsSlice, loFlowParams)
			logger.Debugw("UniVlanConfigFsm flow add", log.Fields{
				"Cookies":  oFsm.uniVlanFlowParamsSlice[oFsm.numUniFlows].CookieSlice,
				"MatchVid": strconv.FormatInt(int64(loRuleParams.MatchVid), 16),
				"SetVid":   strconv.FormatInt(int64(loRuleParams.SetVid), 16),
				"SetPcp":   loRuleParams.SetPcp, "numberofFlows": oFsm.numUniFlows + 1,
				"device-id": oFsm.deviceID})

			oFsm.numUniFlows++
			// note: theoretical it would be possible to clear the same rule from the remove slice
			//  (for entries that have not yet been started with removal)
			//  but that is getting quite complicated - maybe a future optimization in case it should prove reasonable
			// anyway the precondition here is that the FSM checks for rules to delete first and adds new rules afterwards

			pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
			if pConfigVlanStateBaseFsm.Is(vlanStConfigDone) {
				//have to re-trigger the FSM to proceed with outstanding incremental flow configuration
				// calling some FSM event must be decoupled
				go func(a_pBaseFsm *fsm.FSM) {
					_ = a_pBaseFsm.Event(vlanEvIncrFlowConfig)
				}(pConfigVlanStateBaseFsm)
			} // if not in the appropriate state a new entry will be automatically considered later
			//   when the configDone state is reached
		} else {
			logger.Errorw("UniVlanConfigFsm flow limit exceeded", log.Fields{
				"device-id": oFsm.deviceID, "flow-number": oFsm.numUniFlows})
			return fmt.Errorf(" UniVlanConfigFsm flow limit exceeded %s", oFsm.deviceID)
		}
	} else {
		// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
		if oFsm.numUniFlows == oFsm.configuredUniFlow {
			//all requested rules really have been configured
			// state transition notification is checked in deviceHandler
			if oFsm.pDeviceHandler != nil {
				//also the related TechProfile was already configured
				logger.Debugw("UniVlanConfigFsm rule already set - send immediate add-success event for reason update", log.Fields{
					"device-id": oFsm.deviceID})
				go oFsm.pDeviceHandler.deviceProcStatusUpdate(oFsm.requestEvent)
			}
		} else {
			//  avoid device reason update as the rule config connected to this flow may still be in progress
			//  and the device reason should only be updated on success of rule config
			logger.Debugw("UniVlanConfigFsm rule already set but configuration ongoing, suppress early add-success event for reason update",
				log.Fields{"device-id": oFsm.deviceID,
					"NumberofRules": oFsm.numUniFlows, "Configured rules": oFsm.configuredUniFlow})
		}
	}

	if !flowEntryMatch || flowCookieModify { // some change was done to the flow entries
		//permanently store flow config for reconcile case
		if err := oFsm.pDeviceHandler.storePersUniFlowConfig(oFsm.pOnuUniPort.uniID, &oFsm.uniVlanFlowParamsSlice); err != nil {
			logger.Errorw(err.Error(), log.Fields{"device-id": oFsm.deviceID})
			return err
		}
	}
	return nil
}

//RemoveUniFlowParams verifies on existence of flow cookie,
// if found removes cookie from flow cookie list and if this is empty
// initiates removal of the flow related configuration from the ONU (via OMCI)
func (oFsm *UniVlanConfigFsm) RemoveUniFlowParams(aCookie uint64) error {
	flowCookieMatch := false
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		for i, cookie := range storedUniFlowParams.CookieSlice {
			if cookie == aCookie {
				logger.Debugw("UniVlanConfigFsm flow removal - cookie found", log.Fields{
					"device-id": oFsm.deviceID, "cookie": cookie})
				flowCookieMatch = true

				//remove the cookie from the cookie slice and verify it is getting empty
				if len(storedUniFlowParams.CookieSlice) == 1 {
					logger.Debugw("UniVlanConfigFsm flow removal - full flow removal", log.Fields{
						"device-id": oFsm.deviceID})
					oFsm.numUniFlows--

					//create a new element for the removeVlanFlow slice
					loRemoveParams := uniRemoveVlanFlowParams{
						vlanRuleParams: storedUniFlowParams.VlanRuleParams,
						cookie:         storedUniFlowParams.CookieSlice[0],
					}
					oFsm.uniRemoveFlowsSlice = append(oFsm.uniRemoveFlowsSlice, loRemoveParams)

					//and remove the actual element from the addVlanFlow slice
					// oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = nil //automatically done by garbage collector
					if len(oFsm.uniVlanFlowParamsSlice) <= 1 {
						oFsm.numUniFlows = 0              //no more flows
						oFsm.configuredUniFlow = 0        //no more flows configured
						oFsm.uniVlanFlowParamsSlice = nil //reset the slice
						//at this point it is evident that no flow anymore refers to a still possibly active Techprofile
						//request that this profile gets deleted before a new flow add is allowed
						oFsm.pUniTechProf.setProfileToDelete(oFsm.pOnuUniPort.uniID, uint8(oFsm.techProfileID), true)
						logger.Debugw("UniVlanConfigFsm flow removal - no more flows", log.Fields{
							"device-id": oFsm.deviceID})
					} else {
						oFsm.numUniFlows--
						if oFsm.configuredUniFlow > 0 {
							oFsm.configuredUniFlow--
							//TODO!! might be needed to consider still outstanding configure requests ..
							//  so a flow at removal might still not be configured !?!
						}
						usedTpID := storedUniFlowParams.VlanRuleParams.TpID
						//cut off the requested flow by slicing out this element
						oFsm.uniVlanFlowParamsSlice = append(
							oFsm.uniVlanFlowParamsSlice[:flow], oFsm.uniVlanFlowParamsSlice[flow+1:]...)
						//here we have to check, if there are still other flows referencing to the actual ProfileId
						//  before we can request that this profile gets deleted before a new flow add is allowed
						tpIDInOtherFlows := false
						for _, tpUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
							if tpUniFlowParams.VlanRuleParams.TpID == usedTpID {
								tpIDInOtherFlows = true
								break // search loop can be left
							}
						}
						if tpIDInOtherFlows {
							logger.Debugw("UniVlanConfigFsm tp-id used in deleted flow is still used in other flows", log.Fields{
								"device-id": oFsm.deviceID, "tp-id": usedTpID})
						} else {
							logger.Debugw("UniVlanConfigFsm tp-id used in deleted flow is not used anymore", log.Fields{
								"device-id": oFsm.deviceID, "tp-id": usedTpID})
							//request that this profile gets deleted before a new flow add is allowed
							oFsm.pUniTechProf.setProfileToDelete(oFsm.pOnuUniPort.uniID, uint8(oFsm.techProfileID), true)
						}
						logger.Debugw("UniVlanConfigFsm flow removal - specific flow removed from data", log.Fields{
							"device-id": oFsm.deviceID})
					}
					//trigger the FSM to remove the relevant rule
					pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
					if pConfigVlanStateBaseFsm.Is(vlanStConfigDone) {
						//have to re-trigger the FSM to proceed with outstanding incremental flow configuration
						// calling some FSM event must be decoupled
						go func(a_pBaseFsm *fsm.FSM) {
							_ = a_pBaseFsm.Event(vlanEvRemFlowConfig)
						}(pConfigVlanStateBaseFsm)
					} // if not in the appropriate state a new entry will be automatically considered later
					//   when the configDone state is reached
				} else {
					//cut off the requested cookie by slicing out this element
					oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = append(
						oFsm.uniVlanFlowParamsSlice[flow].CookieSlice[:i],
						oFsm.uniVlanFlowParamsSlice[flow].CookieSlice[i+1:]...)
					// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
					// state transition notification is checked in deviceHandler
					if oFsm.pDeviceHandler != nil {
						//making use of the add->remove successor enum assumption/definition
						go oFsm.pDeviceHandler.deviceProcStatusUpdate(OnuDeviceEvent(uint8(oFsm.requestEvent) + 1))
					}
					logger.Debugw("UniVlanConfigFsm flow removal - rule persists with still valid cookies", log.Fields{
						"device-id": oFsm.deviceID, "cookies": oFsm.uniVlanFlowParamsSlice[flow].CookieSlice})
				}

				//permanently store the modified flow config for reconcile case
				if oFsm.pDeviceHandler != nil {
					if err := oFsm.pDeviceHandler.storePersUniFlowConfig(oFsm.pOnuUniPort.uniID, &oFsm.uniVlanFlowParamsSlice); err != nil {
						logger.Errorw(err.Error(), log.Fields{"device-id": oFsm.deviceID})
						return err
					}
				}

				break //found the cookie - no further search for this requested cookie
			}
		}
		if flowCookieMatch { //cookie already found: no need for further search in other flows
			break
		}
	} //search all flows
	if !flowCookieMatch { //some cookie remove-request for a cookie that does not exist in the FSM data
		logger.Warnw("UniVlanConfigFsm flow removal - remove-cookie not found", log.Fields{
			"device-id": oFsm.deviceID, "remove-cookie": aCookie})
		// but accept the request with success as no such cookie (flow) does exist
		// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
		// state transition notification is checked in deviceHandler
		if oFsm.pDeviceHandler != nil {
			//making use of the add->remove successor enum assumption/definition
			go oFsm.pDeviceHandler.deviceProcStatusUpdate(OnuDeviceEvent(uint8(oFsm.requestEvent) + 1))
		}
		return nil
	} //unknown cookie

	return nil
}

func (oFsm *UniVlanConfigFsm) enterConfigStarting(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})

	// this FSM is not intended for re-start, needs always new creation for a new run
	// (self-destroying - compare enterDisabled())
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

				if oFsm.pUniTechProf.getTechProfileDone(oFsm.pOnuUniPort.uniID, uint8(oFsm.techProfileID)) {
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
	if oFsm.uniVlanFlowParamsSlice[0].VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw("UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		// let the FSM proceed ... (from within this state all internal pointers may be expected to be correct)
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		pConfigVlanStateAFsm := oFsm.pAdaptFsm
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvRxConfigVtfd)
		}(pConfigVlanStateAFsm)
	} else {
		logger.Debugw("UniVlanConfigFsm create VTFD", log.Fields{
			"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
			"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
		// setVid is assumed to be masked already by the caller to 12 bit
		oFsm.vlanFilterList[0] = uint16(oFsm.uniVlanFlowParamsSlice[0].VlanRuleParams.SetVid)
		oFsm.mutexFlowParams.Unlock()
		vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization
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
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		meInstance := oFsm.pOmciCC.sendCreateVtfdVar(context.TODO(), ConstDefaultOmciTimeout, true,
			oFsm.pAdaptFsm.commChan, meParams)
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
		//  send shall return (dual format) error code that can be used here for immediate error treatment
		//  (relevant to all used sendXX() methods in this (and other) FSM's)
		oFsm.pLastTxMeInstance = meInstance
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigEvtocd(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - start config EVTOCD loop", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	oFsm.requestEventOffset = 0 //0 offset for last flow-add activity
	go func() {

		errEvto := oFsm.performConfigEvtocdEntries(0)

		// FIXME operating under the assumption that the uniID == gemPortID, might be wrong.
		if oFsm.pUniTechProf.getMulticastFlag(oFsm.pOnuUniPort.uniID, uint8(oFsm.techProfileID)) {
			//can Use the first elements in the slice because it's the first flow.
			errCreateAllMulticastME := oFsm.performSettingMulticastME(uint16(oFsm.pOnuUniPort.uniID),
				oFsm.uniVlanFlowParamsSlice[0].VlanRuleParams.SetVid)
			if errCreateAllMulticastME != nil {
				logger.Errorw("Multicast ME create failed, aborting AniConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
		}
		if errEvto == nil {
			//TODO Possibly insert new state for multicast --> possibly another jira/later time.
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigEvtocd)
		}
	}()
}

func (oFsm *UniVlanConfigFsm) enterVlanConfigDone(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - checking on more flows", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
	if len(oFsm.uniRemoveFlowsSlice) > 0 {
		//some further flows are to be removed, removal always starts with the first element
		// calling some FSM event must be decoupled
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(vlanEvRemFlowConfig)
		}(pConfigVlanStateBaseFsm)
		return
	}
	if oFsm.numUniFlows > oFsm.configuredUniFlow {
		//some further flows are to be configured
		// calling some FSM event must be decoupled
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(vlanEvIncrFlowConfig)
		}(pConfigVlanStateBaseFsm)
		return
	}

	logger.Debugw("UniVlanConfigFsm - VLAN config done: send dh event notification", log.Fields{
		"device-id": oFsm.deviceID})
	// it might appear that some flows are requested also after 'flowPushed' event has been generated ...
	// state transition notification is checked in deviceHandler
	if oFsm.pDeviceHandler != nil {
		//making use of the add->remove successor enum assumption/definition
		go oFsm.pDeviceHandler.deviceProcStatusUpdate(OnuDeviceEvent(uint8(oFsm.requestEvent) + oFsm.requestEventOffset))
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigIncrFlow(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - start config further incremental flow", log.Fields{
		"in state": e.FSM.Current(), "recent flow-number": oFsm.configuredUniFlow,
		"device-id": oFsm.deviceID})
	oFsm.mutexFlowParams.Lock()

	if oFsm.configuredUniFlow == 0 {
		oFsm.mutexFlowParams.Unlock()
		// this is a restart with a complete new flow, we can re-use the initial flow config control
		// including the check, if the related techProfile is (still) available (probably also removed in between)
		// calling some FSM event must be decoupled
		pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(vlanEvRenew)
		}(pConfigVlanStateBaseFsm)
		return
	}

	if oFsm.uniVlanFlowParamsSlice[oFsm.configuredUniFlow].VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw("UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	} else {
		if oFsm.numVlanFilterEntries == 0 {
			//no VTFD yet created
			logger.Debugw("UniVlanConfigFsm create VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
				"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
			// setVid is assumed to be masked already by the caller to 12 bit
			oFsm.vlanFilterList[0] = uint16(oFsm.uniVlanFlowParamsSlice[oFsm.configuredUniFlow].VlanRuleParams.SetVid)
			oFsm.mutexFlowParams.Unlock()
			vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization
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
			oFsm.pLastTxMeInstance = meInstance
		} else {
			//VTFD already exists - just modify by 'set'
			//TODO!!: but only if the VID is not already present, skipped by now to test basic working
			logger.Debugw("UniVlanConfigFsm set VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(oFsm.vtfdID), 16),
				"in state":  e.FSM.Current(), "device-id": oFsm.deviceID})
			// setVid is assumed to be masked already by the caller to 12 bit
			oFsm.vlanFilterList[oFsm.numVlanFilterEntries] =
				uint16(oFsm.uniVlanFlowParamsSlice[oFsm.configuredUniFlow].VlanRuleParams.SetVid)
			oFsm.mutexFlowParams.Unlock()
			vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization
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
			oFsm.pLastTxMeInstance = meInstance
		}
		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("VTFD create/set failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
			go func(a_pBaseFsm *fsm.FSM) {
				_ = a_pBaseFsm.Event(vlanEvReset)
			}(pConfigVlanStateBaseFsm)
			return
		}
	}
	oFsm.requestEventOffset = 0 //0 offset for last flow-add activity
	go func() {

		errEvto := oFsm.performConfigEvtocdEntries(0)

		// FIXME operating under the assumption that the uniID == gemPortID, might be wrong.
		if oFsm.pUniTechProf.getMulticastFlag(oFsm.pOnuUniPort.uniID, uint8(oFsm.techProfileID)) {
			//can Use the first elements in the slice because it's the first flow.
			errCreateAllMulticastME := oFsm.performSettingMulticastME(uint16(oFsm.pOnuUniPort.uniID),
				oFsm.uniVlanFlowParamsSlice[oFsm.configuredUniFlow].VlanRuleParams.SetVid)
			if errCreateAllMulticastME != nil {
				logger.Errorw("Multicast ME create failed, aborting AniConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
		}
		if errEvto == nil {
			//TODO Possibly insert new state for multicast --> possibly another jira/later time.
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigEvtocd)
		}
	}()
}

func (oFsm *UniVlanConfigFsm) enterRemoveFlow(e *fsm.Event) {
	oFsm.mutexFlowParams.Lock()
	logger.Debugw("UniVlanConfigFsm - start removing the top remove-flow", log.Fields{
		"in state": e.FSM.Current(), "with last cookie": oFsm.uniRemoveFlowsSlice[0].cookie,
		"device-id": oFsm.deviceID})

	pConfigVlanStateBaseFsm := oFsm.pAdaptFsm.pFsm
	loAllowSpecificOmciConfig := oFsm.pDeviceHandler.ReadyForSpecificOmciConfig
	loVlanEntryClear := uint8(0)
	loVlanEntryRmPos := uint8(0x80) //with indication 'invalid' in bit 7
	//shallow copy is sufficient as no reference variables are used within struct
	loRuleParams := oFsm.uniRemoveFlowsSlice[0].vlanRuleParams
	oFsm.mutexFlowParams.Unlock()
	logger.Debugw("UniVlanConfigFsm - remove-flow parameters are", log.Fields{
		"match vid": loRuleParams.MatchVid, "match Pcp": loRuleParams.MatchPcp,
		"set vid":   strconv.FormatInt(int64(loRuleParams.SetVid), 16),
		"device-id": oFsm.deviceID})

	if loRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		logger.Debugw("UniVlanConfigFsm: no VTFD removal required for transparent flow", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	} else {
		vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization and 're-copy'
		if oFsm.numVlanFilterEntries == 1 {
			//only one active VLAN entry (hopefully the SetVID we want to remove - should be, but not verified ..)
			//  so we can just delete the VTFD entry
			logger.Debugw("UniVlanConfigFsm: VTFD delete (no more vlan filters)",
				log.Fields{"current vlan list": oFsm.vlanFilterList,
					"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
			loVlanEntryClear = 1           //full VlanFilter clear request
			if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
				meInstance := oFsm.pOmciCC.sendDeleteVtfd(context.TODO(), ConstDefaultOmciTimeout, true,
					oFsm.pAdaptFsm.commChan, oFsm.vtfdID)
				oFsm.pLastTxMeInstance = meInstance
			} else {
				logger.Debugw("UniVlanConfigFsm delete VTFD OMCI handling skipped based on device state", log.Fields{
					"device-id": oFsm.deviceID, "device-state": oFsm.pDeviceHandler.deviceReason})
			}
		} else {
			//many VTFD already should exists - find and remove the one concerned by the actual remove rule
			//  by updating the VTFD per set command with new valid list
			logger.Debugw("UniVlanConfigFsm: VTFD removal of requested VLAN from the list on OMCI",
				log.Fields{"current vlan list": oFsm.vlanFilterList,
					"set-vlan": loRuleParams.SetVid, "device-id": oFsm.deviceID})
			for i := uint8(0); i < oFsm.numVlanFilterEntries; i++ {
				if loRuleParams.SetVid == uint32(oFsm.vlanFilterList[i]) {
					loVlanEntryRmPos = i
					break //abort search
				}
			}
			if loVlanEntryRmPos < cVtfdTableSize {
				//valid entry was found - to be eclipsed
				loVlanEntryClear = 2 //VlanFilter remove request for a specific entry
				for i := uint8(0); i < oFsm.numVlanFilterEntries; i++ {
					if i < loVlanEntryRmPos {
						vtfdFilterList[i] = oFsm.vlanFilterList[i] //copy original
					} else if i < (cVtfdTableSize - 1) {
						vtfdFilterList[i] = oFsm.vlanFilterList[i+1] //copy successor (including 0 elements)
					} else {
						vtfdFilterList[i] = 0 //set last byte if needed
					}
				}
				logger.Debugw("UniVlanConfigFsm set VTFD", log.Fields{
					"EntitytId":     strconv.FormatInt(int64(oFsm.vtfdID), 16),
					"new vlan list": vtfdFilterList, "device-id": oFsm.deviceID})

				if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
					meParams := me.ParamData{
						EntityID: oFsm.vtfdID,
						Attributes: me.AttributeValueMap{
							"VlanFilterList":  vtfdFilterList,
							"NumberOfEntries": oFsm.numVlanFilterEntries - 1, //one element less
						},
					}
					meInstance := oFsm.pOmciCC.sendSetVtfdVar(context.TODO(), ConstDefaultOmciTimeout, true,
						oFsm.pAdaptFsm.commChan, meParams)
					oFsm.pLastTxMeInstance = meInstance
				} else {
					logger.Debugw("UniVlanConfigFsm set VTFD OMCI handling skipped based on device state", log.Fields{
						"device-id": oFsm.deviceID, "device-state": oFsm.pDeviceHandler.deviceReason})
				}
			} else {
				logger.Warnw("UniVlanConfigFsm: requested VLAN for removal not found in list - ignore and continue (no VTFD set)",
					log.Fields{"device-id": oFsm.deviceID})
			}
		}
		if loVlanEntryClear > 0 {
			if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
				//waiting on response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("VTFD delete/reset failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					// calling some FSM event must be decoupled
					go func(a_pBaseFsm *fsm.FSM) {
						_ = a_pBaseFsm.Event(vlanEvReset)
					}(pConfigVlanStateBaseFsm)
					return
				}
			}

			if loVlanEntryClear == 1 {
				oFsm.vlanFilterList[0] = 0 //first entry is the only that can contain the previous only-one element
				oFsm.numVlanFilterEntries = 0
			} else if loVlanEntryClear == 2 {
				// new VlanFilterList should be one entry smaller now - copy from last configured entry
				// this loop now includes the 0 element on previous last valid entry
				for i := uint8(0); i <= oFsm.numVlanFilterEntries; i++ {
					oFsm.vlanFilterList[i] = vtfdFilterList[i]
				}
				oFsm.numVlanFilterEntries--
			}
		}
	}

	if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
		go oFsm.removeEvtocdEntries(loRuleParams)
	} else {
		// OMCI processing is not done, expectation is to have the ONU in some basic config state accordingly
		logger.Debugw("UniVlanConfigFsm remove EVTOCD OMCI handling skipped based on device state", log.Fields{
			"device-id": oFsm.deviceID})
		// calling some FSM event must be decoupled
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(vlanEvRemFlowDone)
		}(pConfigVlanStateBaseFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterVlanCleanupDone(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm - removing the removal data", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})

	oFsm.mutexFlowParams.Lock()
	if len(oFsm.uniRemoveFlowsSlice) <= 1 {
		oFsm.uniRemoveFlowsSlice = nil //reset the slice
		logger.Debugw("UniVlanConfigFsm flow removal - last remove-flow deleted", log.Fields{
			"device-id": oFsm.deviceID})
	} else {
		//cut off the actual flow by slicing out the first element
		oFsm.uniRemoveFlowsSlice = append(
			oFsm.uniRemoveFlowsSlice[:0],
			oFsm.uniRemoveFlowsSlice[1:]...)
		logger.Debugw("UniVlanConfigFsm flow removal - specific flow deleted from data", log.Fields{
			"device-id": oFsm.deviceID})
	}
	oFsm.mutexFlowParams.Unlock()

	oFsm.requestEventOffset = 1 //offset 1 for last flow-remove activity
	//return to the basic config verification state
	pConfigVlanStateAFsm := oFsm.pAdaptFsm
	if pConfigVlanStateAFsm != nil {
		// obviously calling some FSM event here directly does not work - so trying to decouple it ...
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(vlanEvFlowDataRemoved)
			}
		}(pConfigVlanStateAFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterResetting(e *fsm.Event) {
	logger.Debugw("UniVlanConfigFsm resetting", log.Fields{"device-id": oFsm.deviceID})

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
	logger.Debugw("UniVlanConfigFsm enters disabled state", log.Fields{"device-id": oFsm.deviceID})
	oFsm.pLastTxMeInstance = nil
	if oFsm.pDeviceHandler != nil {
		//TODO: to clarify with improved error treatment for VlanConfigFsm (timeout,reception) errors
		//  current code removes the complete FSM including all flow/rule configuration done so far
		//  this might be a bit to much, it would require fully new flow config from rwCore (at least on OnuDown/up)
		//  maybe a more sophisticated approach is possible without clearing the data
		if oFsm.clearPersistency {
			//permanently remove possibly stored persistent data
			if len(oFsm.uniVlanFlowParamsSlice) > 0 {
				var emptySlice = make([]uniVlanFlowParams, 0)
				_ = oFsm.pDeviceHandler.storePersUniFlowConfig(oFsm.pOnuUniPort.uniID, &emptySlice) //ignore errors
			}
		} else {
			logger.Debugw("UniVlanConfigFsm persistency data not cleared", log.Fields{"device-id": oFsm.deviceID})
		}
		//request removal of 'reference' in the Handler (completely clear the FSM and its data)
		go oFsm.pDeviceHandler.RemoveVlanFilterFsm(oFsm.pOnuUniPort)
	}
}

func (oFsm *UniVlanConfigFsm) processOmciVlanMessages() { //ctx context.Context?
	logger.Debugw("Start UniVlanConfigFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info("UniVlanConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			break loop
		}
		logger.Debugw("UniVlanConfigFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Infow("UniVlanConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw("UniVlanConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oFsm.handleOmciVlanConfigMessage(msg)
		default:
			logger.Warn("UniVlanConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Infow("End UniVlanConfigFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) handleOmciVlanConfigMessage(msg OmciMessage) {
	logger.Debugw("Rx OMCI UniVlanConfigFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{ // had to shift that to a method to cope with StaticCodeAnalysis restrictions :-(
			if err := oFsm.handleOmciCreateResponseMessage(msg.OmciPacket); err != nil {
				logger.Warnw("CreateResponse handling aborted", log.Fields{"err": err})
				return
			}
		} //CreateResponseType
	case omci.SetResponseType:
		{ //leave that here as direct code as most often used
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
			if msgLayer == nil {
				logger.Errorw("Omci Msg layer could not be detected for SetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Errorw("Omci Msg layer could not be assigned for SetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw("UniVlanConfigFsm SetResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw("UniVlanConfigFsm Omci SetResponse Error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
				switch oFsm.pLastTxMeInstance.GetName() {
				case "VlanTaggingFilterData",
					"ExtendedVlanTaggingOperationConfigurationData":
					{ // let the MultiEntity config proceed by stopping the wait function
						oFsm.omciMIdsResponseReceived <- true
					}
				}
			}
		} //SetResponseType
	case omci.DeleteResponseType:
		{ // had to shift that to a method to cope with StaticCodeAnalysis restrictions :-(
			if err := oFsm.handleOmciDeleteResponseMessage(msg.OmciPacket); err != nil {
				logger.Warnw("DeleteResponse handling aborted", log.Fields{"err": err})
				return
			}
		} //DeleteResponseType
	default:
		{
			logger.Errorw("Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

func (oFsm *UniVlanConfigFsm) handleOmciCreateResponseMessage(apOmciPacket *gp.Packet) error {
	msgLayer := (*apOmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw("Omci Msg layer could not be detected for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be detected for CreateResponse for device-id %x",
			oFsm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw("Omci Msg layer could not be assigned for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be assigned for CreateResponse for device-id %x",
			oFsm.deviceID)
	}
	logger.Debugw("UniVlanConfigFsm CreateResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw("Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"device-id": oFsm.deviceID,
			"Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return fmt.Errorf("omci CreateResponse Error for device-id %x",
			oFsm.deviceID)
	}
	if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
		msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
		// to satisfy StaticCodeAnalysis I had to move the small processing into a separate method :-(
		switch oFsm.pLastTxMeInstance.GetName() {
		case "VlanTaggingFilterData":
			{
				if oFsm.pAdaptFsm.pFsm.Current() == vlanStConfigVtfd {
					// Only if CreateResponse is received from first flow entry - let the FSM proceed ...
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRxConfigVtfd)
				} else { // let the MultiEntity config proceed by stopping the wait function
					oFsm.omciMIdsResponseReceived <- true
				}
			}
		}
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) handleOmciDeleteResponseMessage(apOmciPacket *gp.Packet) error {
	msgLayer := (*apOmciPacket).Layer(omci.LayerTypeDeleteResponse)
	if msgLayer == nil {
		logger.Errorw("UniVlanConfigFsm - Omci Msg layer could not be detected for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be detected for DeleteResponse for device-id %x",
			oFsm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.DeleteResponse)
	if !msgOk {
		logger.Errorw("UniVlanConfigFsm - Omci Msg layer could not be assigned for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be assigned for DeleteResponse for device-id %x",
			oFsm.deviceID)
	}
	logger.Debugw("UniVlanConfigFsm DeleteResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw("UniVlanConfigFsm - Omci DeleteResponse Error - later: drive FSM to abort state ?",
			log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return fmt.Errorf("omci DeleteResponse Error for device-id %x",
			oFsm.deviceID)
	}
	if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
		msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
		switch oFsm.pLastTxMeInstance.GetName() {
		case "VlanTaggingFilterData":
			{ // let the MultiEntity config proceed by stopping the wait function
				oFsm.omciMIdsResponseReceived <- true
			}
		}
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) performConfigEvtocdEntries(aFlowEntryNo uint8) error {
	if aFlowEntryNo == 0 {
		// EthType set only at first flow element
		// EVTOCD ME is expected to exist at this point already from MIB-Download (with AssociationType/Pointer)
		// we need to extend the configuration by EthType definition and, to be sure, downstream 'inverse' mode
		logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD", log.Fields{
			"EntitytId":  strconv.FormatInt(int64(oFsm.evtocdID), 16),
			"i/oEthType": strconv.FormatInt(int64(cDefaultTpid), 16),
			"device-id":  oFsm.deviceID})
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
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("Evtocd set TPID failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return fmt.Errorf("evtocd set TPID failed %s, error %s", oFsm.deviceID, err)
		}
	} //first flow element

	oFsm.mutexFlowParams.Lock()
	if oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD single tagged transparent rule", log.Fields{
			"device-id": oFsm.deviceID})
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
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("Evtocd set transparent singletagged rule failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return fmt.Errorf("evtocd set transparent singletagged rule failed %s, error %s", oFsm.deviceID, err)

		}
	} else {
		// according to py-code acceptIncrementalEvto program option decides upon stacking or translation scenario
		if oFsm.acceptIncrementalEvtoOption {
			// this defines VID translation scenario: singletagged->singletagged (if not transparent)
			logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD single tagged translation rule", log.Fields{
				"device-id": oFsm.deviceID})
			sliceEvtocdRule := make([]uint8, 16)
			// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
				cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
					cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
					cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
				oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.MatchPcp<<cFilterPrioOffset| // either DNFonPrio or ignore tag (default) on innerVLAN
					oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.MatchVid<<cFilterVidOffset| // either DNFonVid or real filter VID
					cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
					cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
				oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.TagsToRemove<<cTreatTTROffset| // either 1 or 0
					cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
					cDontCareVid<<cTreatVidOffset| // Outer VID don't care
					cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
				oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.SetPcp<<cTreatPrioOffset| // as configured in flow
					oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.SetVid<<cTreatVidOffset| //as configured in flow
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
			oFsm.pLastTxMeInstance = meInstance

			//verify response
			err := oFsm.waitforOmciResponse()
			if err != nil {
				logger.Errorw("Evtocd set singletagged translation rule failed, aborting VlanConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
				return fmt.Errorf("evtocd set singletagged translation rule failed %s, error %s", oFsm.deviceID, err)
			}
		} else {
			//not transparent and not acceptIncrementalEvtoOption untagged/priotagged->singletagged
			{ // just for local var's
				// this defines stacking scenario: untagged->singletagged
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD untagged->singletagged rule", log.Fields{
					"device-id": oFsm.deviceID})
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
						oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.SetVid<<cTreatVidOffset| // Outer VID don't care
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
				oFsm.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd set untagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return fmt.Errorf("evtocd set untagged->singletagged rule failed %s, error %s", oFsm.deviceID, err)

				}
			} // just for local var's
			{ // just for local var's
				// this defines 'stacking' scenario: priotagged->singletagged
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD priotagged->singletagged rule", log.Fields{
					"device-id": oFsm.deviceID})
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
						oFsm.uniVlanFlowParamsSlice[aFlowEntryNo].VlanRuleParams.SetVid<<cTreatVidOffset| // Outer VID as configured
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
				oFsm.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd set priotagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return fmt.Errorf("evtocd set priotagged->singletagged rule failed %s, error %s", oFsm.deviceID, err)

				}
			} //just for local var's
		}
	}

	// if Config has been done for all EVTOCD entries let the FSM proceed
	logger.Debugw("EVTOCD set loop finished", log.Fields{"device-id": oFsm.deviceID})
	oFsm.configuredUniFlow++ // one (more) flow configured
	return nil
}

func (oFsm *UniVlanConfigFsm) removeEvtocdEntries(aRuleParams uniVlanRuleParams) {
	// configured Input/Output TPID is not modified again - no influence if no filter is applied
	if aRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission was set
		//perhaps the config is not needed for removal,
		//  but the specific InnerTpid setting is removed in favor of the real default forwarding rule
		logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD reset to default single tagged rule", log.Fields{
			"device-id": oFsm.deviceID})
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
				cDontCareTpid<<cTreatTpidOffset) // copy TPID and DEI

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
		oFsm.pLastTxMeInstance = meInstance

		//verify response
		err := oFsm.waitforOmciResponse()
		if err != nil {
			logger.Errorw("Evtocd reset singletagged rule failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
			return
		}
	} else {
		// according to py-code acceptIncrementalEvto program option decides upon stacking or translation scenario
		if oFsm.acceptIncrementalEvtoOption {
			// this defines VID translation scenario: singletagged->singletagged (if not transparent)
			logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD clear single tagged translation rule", log.Fields{
				"device-id": oFsm.deviceID, "match-vlan": aRuleParams.MatchVid})
			sliceEvtocdRule := make([]uint8, 16)
			// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
				cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
					cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
					cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
				aRuleParams.MatchPcp<<cFilterPrioOffset| // either DNFonPrio or ignore tag (default) on innerVLAN
					aRuleParams.MatchVid<<cFilterVidOffset| // either DNFonVid or real filter VID
					cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
					cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

			// delete indication for the indicated Filter
			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:], 0xFFFFFFFF)
			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:], 0xFFFFFFFF)

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
			oFsm.pLastTxMeInstance = meInstance

			//verify response
			err := oFsm.waitforOmciResponse()
			if err != nil {
				logger.Errorw("Evtocd clear singletagged translation rule failed, aborting VlanConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID, "match-vlan": aRuleParams.MatchVid})
				_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
				return
			}
		} else {
			//not transparent and not acceptIncrementalEvtoOption: untagged/priotagged->singletagged
			{ // just for local var's
				// this defines stacking scenario: untagged->singletagged
				//TODO!! in theory there could be different rules running in setting different PCP/VID'S
				//  for untagged/priotagged, last rule wins (and remains the only one), maybe that should be
				//  checked already at flow-add (and rejected) - to be observed if such is possible in Voltha
				//  delete now assumes there is only one such rule!
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD reset untagged rule to default", log.Fields{
					"device-id": oFsm.deviceID})
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
					cDoNotAddPrio<<cTreatPrioOffset| // do not add inner tag
						cDontCareVid<<cTreatVidOffset| // Outer VID don't care
						cDontCareTpid<<cTreatTpidOffset) // copy TPID and DEI

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
				oFsm.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd reset untagged rule to default failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} // just for local var's
			{ // just for local var's
				// this defines 'stacking' scenario: priotagged->singletagged
				logger.Debugw("UniVlanConfigFsm Tx Set::EVTOCD delete priotagged rule", log.Fields{
					"device-id": oFsm.deviceID})
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

				// delete indication for the indicated Filter
				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:], 0xFFFFFFFF)
				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:], 0xFFFFFFFF)

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
				oFsm.pLastTxMeInstance = meInstance

				//verify response
				err := oFsm.waitforOmciResponse()
				if err != nil {
					logger.Errorw("Evtocd delete priotagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvReset)
					return
				}
			} //just for local var's
		}
	}

	// if Config has been done for all EVTOCD entries let the FSM proceed
	logger.Debugw("EVTOCD filter remove loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.pAdaptFsm.pFsm.Event(vlanEvRemFlowDone)
}

func (oFsm *UniVlanConfigFsm) waitforOmciResponse() error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(30 * time.Second): //AS FOR THE OTHER OMCI FSM's
		logger.Warnw("UniVlanConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("uniVlanConfigFsm multi entity timeout %s", oFsm.deviceID)
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debug("UniVlanConfigFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw("UniVlanConfigFsm multi entity response error", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("uniVlanConfigFsm multi entity responseError %s", oFsm.deviceID)
	}
}

func (oFsm *UniVlanConfigFsm) performSettingMulticastME(multicastGemPortID uint16, vlanID uint32) error {
	meParams := me.ParamData{
		EntityID: macBridgeServiceProfileEID,
		Attributes: me.AttributeValueMap{
			"BridgeIdPointer": macBridgeServiceProfileEID,
			"PortNum":         0xFF, //fixed unique ANI side indication
			"TpType":          3,    //for .1PMapper
			"TpPointer":       ieeeMapperServiceProfileEID + uint16(oFsm.pOnuUniPort.macBpNo) + oFsm.techProfileID,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateMBPConfigDataVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("CreateMBPConfigData failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MBPConfigDataID": macBridgeServiceProfileEID})
		_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, err)
	}
	errCreateMOP := oFsm.performCreatingMulticastOperationProfile()
	if errCreateMOP != nil {
		logger.Errorw("MulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errCreateMOP)
	}

	errSettingMOP := oFsm.performSettingMulticastOperationProfile(multicastGemPortID, vlanID)
	if errSettingMOP != nil {
		logger.Errorw("MulticastOperationProfile setting failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errSettingMOP)
	}

	errCreateMSCI := oFsm.performCreatingMulticastSubscriberConfigInfo()
	if errCreateMSCI != nil {
		logger.Errorw("MulticastOperationProfile setting failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.pAdaptFsm.pFsm.Event(aniEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errCreateMSCI)
	}

	return nil
}

func (oFsm *UniVlanConfigFsm) performCreatingMulticastSubscriberConfigInfo() error {
	//Direct reference to the Operation profile
	instID := macBridgePortAniEID + uint16(oFsm.pOnuUniPort.macBpNo)
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"MeType":                            0,
			"MulticastOperationsProfilePointer": instID,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateMulticastSubConfigInfoVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("CreateMulticastSubConfigInfo create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastSubConfigInfo": instID})
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s", oFsm.deviceID)
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) performCreatingMulticastOperationProfile() error {
	instID := macBridgePortAniEID + uint16(oFsm.pOnuUniPort.macBpNo)
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"IgmpVersion":               2,
			"IgmpFunction":              0,
			"ImmediateLeave":            false,
			"Robustness":                2,
			"QuerierIp":                 0,
			"QueryInterval":             125,
			"QuerierMaxResponseTime":    100,
			"LastMemberResponseTime":    10,
			"UnauthorizedJoinBehaviour": false,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateMulticastOperationProfileVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("CreateMulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastOperationProfileID": instID})
		return fmt.Errorf("createMulticastOperationProfile responseError %s", oFsm.deviceID)
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) performSettingMulticastOperationProfile(multicastGemPortID uint16, vlanID uint32) error {
	instID := macBridgePortAniEID + uint16(oFsm.pOnuUniPort.macBpNo)
	//TODO check that this is correct
	// Table control
	//setCtrl = 1
	//rowPartId = 0
	//test = 0
	//rowKey = 0
	tableCtrlStr := "0100000000000000"
	tableCtrl := AsByteSlice(tableCtrlStr)
	//TODO Building it as a Table, even though the attribute is `StringAttributeType`
	// see line 56 of multicastoperationsprofileframe.go,
	// need to discuss with Chip, most likely and error in the conversion
	dynamicAccessCL := make([]uint8, 24)
	copy(dynamicAccessCL, tableCtrl)
	//Multicast GemPortId
	binary.BigEndian.PutUint16(dynamicAccessCL[2:], multicastGemPortID)
	// python version waits for installation of flows, see line 723 onward of
	// brcm_openomci_onu_handler.py
	binary.BigEndian.PutUint16(dynamicAccessCL[4:], uint16(vlanID))
	//Source IP all to 0
	binary.BigEndian.PutUint32(dynamicAccessCL[6:], IPToInt32(net.IPv4(0, 0, 0, 0)))
	// This is the 224.0.0.1 address
	binary.BigEndian.PutUint32(dynamicAccessCL[10:], IPToInt32(net.IPv4allsys))
	// this is the 255.255.255.255 address
	binary.BigEndian.PutUint32(dynamicAccessCL[14:], IPToInt32(net.IPv4bcast))
	//imputed group bandwidth
	binary.BigEndian.PutUint16(dynamicAccessCL[18:], 0)
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"DynamicAccessControlListTable": dynamicAccessCL,
		},
	}
	meInstance := oFsm.pOmciCC.sendCreateMulticastOperationProfileVar(context.TODO(), ConstDefaultOmciTimeout, true,
		oFsm.pAdaptFsm.commChan, meParams)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	//verify response
	err := oFsm.waitforOmciResponse()
	if err != nil {
		logger.Errorw("CreateMulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastOperationProfileID": instID})
		return fmt.Errorf("createMulticastOperationProfile responseError %s", oFsm.deviceID)
	}
	return nil
}
