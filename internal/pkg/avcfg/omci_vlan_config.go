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

//Package avcfg provides anig and vlan configuration functionality
package avcfg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	meters "github.com/opencord/voltha-lib-go/v7/pkg/meters"
	"github.com/opencord/voltha-protos/v5/go/voltha"

	gp "github.com/google/gopacket"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb"
	of "github.com/opencord/voltha-protos/v5/go/openflow_13"
)

const (
	// internal predefined values
	cDefaultDownstreamMode = 0
	cDefaultTpid           = 0x8100
	cVtfdTableSize         = 12             //as per G.988
	cMaxAllowedFlows       = cVtfdTableSize //which might be under discussion, for the moment connected to limit of VLAN's within VTFD
)

const (
	//  internal offsets for requestEvent according to definition in onu_device_entry::cmn.OnuDeviceEvent
	cDeviceEventOffsetAddWithKvStore    = 0 //OmciVlanFilterAddDone - OmciVlanFilterAddDone cannot use because of lint
	cDeviceEventOffsetAddNoKvStore      = cmn.OmciVlanFilterAddDoneNoKvStore - cmn.OmciVlanFilterAddDone
	cDeviceEventOffsetRemoveWithKvStore = cmn.OmciVlanFilterRemDone - cmn.OmciVlanFilterAddDone
	cDeviceEventOffsetRemoveNoKvStore   = cmn.OmciVlanFilterRemDoneNoKvStore - cmn.OmciVlanFilterAddDone
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

// events of config UNI port VLAN FSM
const (
	VlanEvStart                   = "VlanEvStart"
	VlanEvPrepareDone             = "VlanEvPrepareDone"
	VlanEvWaitTechProf            = "VlanEvWaitTechProf"
	VlanEvCancelOutstandingConfig = "VlanEvCancelOutstandingConfig"
	VlanEvContinueConfig          = "VlanEvContinueConfig"
	VlanEvStartConfig             = "VlanEvStartConfig"
	VlanEvRxConfigVtfd            = "VlanEvRxConfigVtfd"
	VlanEvRxConfigEvtocd          = "VlanEvRxConfigEvtocd"
	VlanEvWaitTPIncr              = "VlanEvWaitTPIncr"
	VlanEvIncrFlowConfig          = "VlanEvIncrFlowConfig"
	VlanEvRenew                   = "VlanEvRenew"
	VlanEvRemFlowConfig           = "VlanEvRemFlowConfig"
	VlanEvRemFlowDone             = "VlanEvRemFlowDone"
	VlanEvFlowDataRemoved         = "VlanEvFlowDataRemoved"
	//VlanEvTimeoutSimple  = "VlanEvTimeoutSimple"
	//VlanEvTimeoutMids    = "VlanEvTimeoutMids"
	VlanEvReset             = "VlanEvReset"
	VlanEvRestart           = "VlanEvRestart"
	VlanEvSkipOmciConfig    = "VlanEvSkipOmciConfig"
	VlanEvSkipIncFlowConfig = "VlanEvSkipIncFlowConfig"
)

// states of config UNI port VLAN FSM
const (
	VlanStDisabled        = "VlanStDisabled"
	VlanStPreparing       = "VlanStPreparing"
	VlanStStarting        = "VlanStStarting"
	VlanStWaitingTechProf = "VlanStWaitingTechProf"
	VlanStConfigVtfd      = "VlanStConfigVtfd"
	VlanStConfigEvtocd    = "VlanStConfigEvtocd"
	VlanStConfigDone      = "VlanStConfigDone"
	VlanStIncrFlowWaitTP  = "VlanStIncrFlowWaitTP"
	VlanStConfigIncrFlow  = "VlanStConfigIncrFlow"
	VlanStRemoveFlow      = "VlanStRemoveFlow"
	VlanStCleanupDone     = "VlanStCleanupDone"
	VlanStResetting       = "VlanStResetting"
)

// CVlanFsmIdleState - TODO: add comment
const CVlanFsmIdleState = VlanStConfigDone // state where no OMCI activity is done (for a longer time)
// CVlanFsmConfiguredState - TODO: add comment
const CVlanFsmConfiguredState = VlanStConfigDone // state that indicates that at least some valid user related VLAN configuration should exist

type uniRemoveVlanFlowParams struct {
	isSuspendedOnAdd bool
	removeChannel    chan bool
	cookie           uint64 //just the last cookie valid for removal
	vlanRuleParams   cmn.UniVlanRuleParams
	respChan         *chan error
}

//UniVlanConfigFsm defines the structure for the state machine for configuration of the VLAN related setting via OMCI
//  builds upon 'VLAN rules' that are derived from multiple flows
type UniVlanConfigFsm struct {
	pDeviceHandler              cmn.IdeviceHandler
	pOnuDeviceEntry             cmn.IonuDeviceEntry
	deviceID                    string
	pOmciCC                     *cmn.OmciCC
	pOnuUniPort                 *cmn.OnuUniPort
	pUniTechProf                *OnuUniTechProf
	pOnuDB                      *devdb.OnuDeviceDB
	requestEvent                cmn.OnuDeviceEvent
	omciMIdsResponseReceived    chan bool //seperate channel needed for checking multiInstance OMCI message responses
	PAdaptFsm                   *cmn.AdapterFsm
	acceptIncrementalEvtoOption bool
	isCanceled                  bool
	isAwaitingResponse          bool
	mutexIsAwaitingResponse     sync.RWMutex
	mutexFlowParams             sync.RWMutex
	chCookieDeleted             chan bool //channel to indicate that a specific cookie (related to the active rule) was deleted
	actualUniFlowParam          cmn.UniVlanFlowParams
	uniVlanFlowParamsSlice      []cmn.UniVlanFlowParams
	uniRemoveFlowsSlice         []uniRemoveVlanFlowParams
	NumUniFlows                 uint8 // expected number of flows should be less than 12
	ConfiguredUniFlow           uint8
	numRemoveFlows              uint8
	numVlanFilterEntries        uint8
	vlanFilterList              [cVtfdTableSize]uint16
	evtocdID                    uint16
	mutexPLastTxMeInstance      sync.RWMutex
	pLastTxMeInstance           *me.ManagedEntity
	requestEventOffset          uint8
	TpIDWaitingFor              uint8
	signalOnFlowDelete          bool
	flowDeleteChannel           chan<- bool
	//cookie value that indicates that a rule to add is delayed by waiting for deletion of some other existing rule with the same cookie
	delayNewRuleCookie uint64
	// Used to indicate if the FSM is for a reconciling flow and if it's the last flow to be reconciled
	// thus notification needs to be sent on chan.
	lastFlowToReconcile bool
}

//NewUniVlanConfigFsm is the 'constructor' for the state machine to config the PON ANI ports
//  of ONU UNI ports via OMCI
func NewUniVlanConfigFsm(ctx context.Context, apDeviceHandler cmn.IdeviceHandler, apOnuDeviceEntry cmn.IonuDeviceEntry, apDevOmciCC *cmn.OmciCC, apUniPort *cmn.OnuUniPort,
	apUniTechProf *OnuUniTechProf, apOnuDB *devdb.OnuDeviceDB, aTechProfileID uint8,
	aRequestEvent cmn.OnuDeviceEvent, aName string, aCommChannel chan cmn.Message, aAcceptIncrementalEvto bool,
	aCookieSlice []uint64, aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8, lastFlowToRec bool, aMeter *voltha.OfpMeterConfig, respChan *chan error) *UniVlanConfigFsm {
	instFsm := &UniVlanConfigFsm{
		pDeviceHandler:              apDeviceHandler,
		pOnuDeviceEntry:             apOnuDeviceEntry,
		deviceID:                    apDeviceHandler.GetDeviceID(),
		pOmciCC:                     apDevOmciCC,
		pOnuUniPort:                 apUniPort,
		pUniTechProf:                apUniTechProf,
		pOnuDB:                      apOnuDB,
		requestEvent:                aRequestEvent,
		acceptIncrementalEvtoOption: aAcceptIncrementalEvto,
		NumUniFlows:                 0,
		ConfiguredUniFlow:           0,
		numRemoveFlows:              0,
		lastFlowToReconcile:         lastFlowToRec,
	}

	instFsm.PAdaptFsm = cmn.NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.PAdaptFsm == nil {
		logger.Errorw(ctx, "UniVlanConfigFsm's cmn.AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		// Push response on the response channel
		instFsm.pushReponseOnFlowResponseChannel(ctx, respChan, fmt.Errorf("adapter-fsm-could-not-be-instantiated"))
		return nil
	}
	instFsm.PAdaptFsm.PFsm = fsm.NewFSM(
		VlanStDisabled,
		fsm.Events{
			{Name: VlanEvStart, Src: []string{VlanStDisabled}, Dst: VlanStPreparing},
			{Name: VlanEvPrepareDone, Src: []string{VlanStPreparing}, Dst: VlanStStarting},
			{Name: VlanEvWaitTechProf, Src: []string{VlanStStarting}, Dst: VlanStWaitingTechProf},
			{Name: VlanEvCancelOutstandingConfig, Src: []string{VlanStWaitingTechProf}, Dst: VlanStConfigDone},
			{Name: VlanEvContinueConfig, Src: []string{VlanStWaitingTechProf}, Dst: VlanStConfigVtfd},
			{Name: VlanEvStartConfig, Src: []string{VlanStStarting}, Dst: VlanStConfigVtfd},
			{Name: VlanEvRxConfigVtfd, Src: []string{VlanStConfigVtfd}, Dst: VlanStConfigEvtocd},
			{Name: VlanEvRxConfigEvtocd, Src: []string{VlanStConfigEvtocd, VlanStConfigIncrFlow},
				Dst: VlanStConfigDone},
			{Name: VlanEvRenew, Src: []string{VlanStConfigDone}, Dst: VlanStStarting},
			{Name: VlanEvWaitTPIncr, Src: []string{VlanStConfigDone}, Dst: VlanStIncrFlowWaitTP},
			{Name: VlanEvIncrFlowConfig, Src: []string{VlanStConfigDone, VlanStIncrFlowWaitTP},
				Dst: VlanStConfigIncrFlow},
			{Name: VlanEvRemFlowConfig, Src: []string{VlanStConfigDone}, Dst: VlanStRemoveFlow},
			{Name: VlanEvRemFlowDone, Src: []string{VlanStRemoveFlow}, Dst: VlanStCleanupDone},
			{Name: VlanEvFlowDataRemoved, Src: []string{VlanStCleanupDone}, Dst: VlanStConfigDone},
			/*
				{Name: VlanEvTimeoutSimple, Src: []string{
					VlanStCreatingDot1PMapper, VlanStCreatingMBPCD, VlanStSettingTconts, VlanStSettingDot1PMapper}, Dst: VlanStStarting},
				{Name: VlanEvTimeoutMids, Src: []string{
					VlanStCreatingGemNCTPs, VlanStCreatingGemIWs, VlanStSettingPQs}, Dst: VlanStStarting},
			*/
			// exceptional treatment for all states except VlanStResetting
			{Name: VlanEvReset, Src: []string{VlanStStarting, VlanStWaitingTechProf,
				VlanStConfigVtfd, VlanStConfigEvtocd, VlanStConfigDone, VlanStConfigIncrFlow,
				VlanStRemoveFlow, VlanStCleanupDone},
				Dst: VlanStResetting},
			// the only way to get to resource-cleared disabled state again is via "resseting"
			{Name: VlanEvRestart, Src: []string{VlanStResetting}, Dst: VlanStDisabled},
			// transitions for reconcile handling according to VOL-3834
			{Name: VlanEvSkipOmciConfig, Src: []string{VlanStPreparing}, Dst: VlanStConfigDone},
			{Name: VlanEvSkipOmciConfig, Src: []string{VlanStConfigDone}, Dst: VlanStConfigIncrFlow},
			{Name: VlanEvSkipIncFlowConfig, Src: []string{VlanStConfigIncrFlow}, Dst: VlanStConfigDone},
		},
		fsm.Callbacks{
			"enter_state":                   func(e *fsm.Event) { instFsm.PAdaptFsm.LogFsmStateChange(ctx, e) },
			"enter_" + VlanStPreparing:      func(e *fsm.Event) { instFsm.enterPreparing(ctx, e) },
			"enter_" + VlanStStarting:       func(e *fsm.Event) { instFsm.enterConfigStarting(ctx, e) },
			"enter_" + VlanStConfigVtfd:     func(e *fsm.Event) { instFsm.enterConfigVtfd(ctx, e) },
			"enter_" + VlanStConfigEvtocd:   func(e *fsm.Event) { instFsm.enterConfigEvtocd(ctx, e) },
			"enter_" + VlanStConfigDone:     func(e *fsm.Event) { instFsm.enterVlanConfigDone(ctx, e) },
			"enter_" + VlanStConfigIncrFlow: func(e *fsm.Event) { instFsm.enterConfigIncrFlow(ctx, e) },
			"enter_" + VlanStRemoveFlow:     func(e *fsm.Event) { instFsm.enterRemoveFlow(ctx, e) },
			"enter_" + VlanStCleanupDone:    func(e *fsm.Event) { instFsm.enterVlanCleanupDone(ctx, e) },
			"enter_" + VlanStResetting:      func(e *fsm.Event) { instFsm.enterResetting(ctx, e) },
			"enter_" + VlanStDisabled:       func(e *fsm.Event) { instFsm.enterDisabled(ctx, e) },
		},
	)
	if instFsm.PAdaptFsm.PFsm == nil {
		logger.Errorw(ctx, "UniVlanConfigFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		// Push response on the response channel
		instFsm.pushReponseOnFlowResponseChannel(ctx, respChan, fmt.Errorf("adapter-base-fsm-could-not-be-instantiated"))
		return nil
	}

	_ = instFsm.initUniFlowParams(ctx, aTechProfileID, aCookieSlice, aMatchVlan, aSetVlan, aSetPcp, aMeter, respChan)

	logger.Debugw(ctx, "UniVlanConfigFsm created", log.Fields{"device-id": instFsm.deviceID,
		"accIncrEvto": instFsm.acceptIncrementalEvtoOption})
	return instFsm
}

//initUniFlowParams is a simplified form of SetUniFlowParams() used for first flow parameters configuration
func (oFsm *UniVlanConfigFsm) initUniFlowParams(ctx context.Context, aTpID uint8, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8, aMeter *voltha.OfpMeterConfig, respChan *chan error) error {
	loRuleParams := cmn.UniVlanRuleParams{
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

	loFlowParams := cmn.UniVlanFlowParams{VlanRuleParams: loRuleParams, RespChan: respChan}
	loFlowParams.CookieSlice = make([]uint64, 0)
	loFlowParams.CookieSlice = append(loFlowParams.CookieSlice, aCookieSlice...)
	if aMeter != nil {
		loFlowParams.Meter = aMeter
	}

	//no mutex protection is required for initial access and adding the first flow is always possible
	oFsm.uniVlanFlowParamsSlice = make([]cmn.UniVlanFlowParams, 0)
	oFsm.uniVlanFlowParamsSlice = append(oFsm.uniVlanFlowParamsSlice, loFlowParams)
	logger.Debugw(ctx, "first UniVlanConfigFsm flow added", log.Fields{
		"Cookies":   oFsm.uniVlanFlowParamsSlice[0].CookieSlice,
		"MatchVid":  strconv.FormatInt(int64(loRuleParams.MatchVid), 16),
		"SetVid":    strconv.FormatInt(int64(loRuleParams.SetVid), 16),
		"SetPcp":    loRuleParams.SetPcp,
		"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID})

	if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
		oFsm.reconcileVlanFilterList(ctx, uint16(loRuleParams.SetVid))
	}
	//cmp also usage in EVTOCDE create in omci_cc
	oFsm.evtocdID = cmn.MacBridgeServiceProfileEID + uint16(oFsm.pOnuUniPort.MacBpNo)
	oFsm.NumUniFlows = 1
	oFsm.uniRemoveFlowsSlice = make([]uniRemoveVlanFlowParams, 0) //initially nothing to remove

	//permanently store flow config for reconcile case
	if err := oFsm.pDeviceHandler.StorePersUniFlowConfig(ctx, oFsm.pOnuUniPort.UniID,
		&oFsm.uniVlanFlowParamsSlice, true); err != nil {
		logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oFsm.deviceID})
		return err
	}

	return nil
}

//CancelProcessing ensures that suspended processing at waiting on some response is aborted and reset of FSM
func (oFsm *UniVlanConfigFsm) CancelProcessing(ctx context.Context) {
	if oFsm == nil {
		logger.Error(ctx, "no valid UniVlanConfigFsm!")
		return
	}
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

	// in any case (even if it might be automatically requested by above cancellation of waiting) ensure resetting the FSM
	PAdaptFsm := oFsm.PAdaptFsm
	if PAdaptFsm != nil {
		if fsmErr := PAdaptFsm.PFsm.Event(VlanEvReset); fsmErr != nil {
			logger.Errorw(ctx, "reset-event failed in UniVlanConfigFsm!",
				log.Fields{"fsmState": oFsm.PAdaptFsm.PFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
		}
	}
}

//GetWaitingTpID returns the TpId that the FSM might be waiting for continuation (0 if none)
func (oFsm *UniVlanConfigFsm) GetWaitingTpID(ctx context.Context) uint8 {
	if oFsm == nil {
		logger.Error(ctx, "no valid UniVlanConfigFsm!")
		return 0
	}
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.RLock()
	defer oFsm.mutexFlowParams.RUnlock()
	return oFsm.TpIDWaitingFor
}

//SetUniFlowParams verifies on existence of flow parameters to be configured,
// optionally udates the cookie list or appends a new flow if there is space
// if possible the FSM is trigggerd to start with the processing
// ignore complexity by now
// nolint: gocyclo
func (oFsm *UniVlanConfigFsm) SetUniFlowParams(ctx context.Context, aTpID uint8, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8, lastFlowToReconcile bool, aMeter *voltha.OfpMeterConfig, respChan *chan error) error {
	if oFsm == nil {
		logger.Error(ctx, "no valid UniVlanConfigFsm!")
		return fmt.Errorf("no-valid-UniVlanConfigFsm")
	}
	loRuleParams := cmn.UniVlanRuleParams{
		TpID:     aTpID,
		MatchVid: uint32(aMatchVlan),
		SetVid:   uint32(aSetVlan),
		SetPcp:   uint32(aSetPcp),
	}
	var err error
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

	//check if there is some ongoing delete-request running for this flow. If so, block here until this is finished.
	//  might be accordingly rwCore processing runs into timeout in specific situations - needs to be observed ...
	//  this is to protect uniVlanFlowParams from adding new or re-writing the same cookie to the rule currently under deletion
	oFsm.mutexFlowParams.RLock()
	if len(oFsm.uniRemoveFlowsSlice) > 0 {
		for flow, removeUniFlowParams := range oFsm.uniRemoveFlowsSlice {
			if removeUniFlowParams.vlanRuleParams == loRuleParams {
				// the flow to add is the same as the one already in progress of deleting
				logger.Infow(ctx, "UniVlanConfigFsm flow setting - suspending rule-add due to ongoing removal", log.Fields{
					"device-id": oFsm.deviceID, "cookie": removeUniFlowParams.cookie, "remove-index": flow})
				if flow >= len(oFsm.uniRemoveFlowsSlice) {
					logger.Errorw(ctx, "abort UniVlanConfigFsm flow add - inconsistent RemoveFlowsSlice", log.Fields{
						"device-id": oFsm.deviceID, "slice length": len(oFsm.uniRemoveFlowsSlice)})
					oFsm.mutexFlowParams.RUnlock()
					err = fmt.Errorf("abort UniVlanConfigFsm flow add - inconsistent RemoveFlowsSlice %s", oFsm.deviceID)
					oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
					return err

				}
				pRemoveParams := &oFsm.uniRemoveFlowsSlice[flow] //wants to modify the uniRemoveFlowsSlice element directly!
				oFsm.mutexFlowParams.RUnlock()
				if err := oFsm.suspendAddRule(ctx, pRemoveParams); err != nil {
					logger.Errorw(ctx, "UniVlanConfigFsm suspension on add aborted - abort complete add-request", log.Fields{
						"device-id": oFsm.deviceID, "cookie": removeUniFlowParams.cookie})
					err = fmt.Errorf("abort UniVlanConfigFsm suspension on add %s", oFsm.deviceID)
					oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
					return err
				}
				oFsm.mutexFlowParams.RLock()
				break //this specific rule should only exist once per uniRemoveFlowsSlice
			}
		}
	}
	oFsm.mutexFlowParams.RUnlock()

	flowEntryMatch := false
	flowCookieModify := false
	requestAppendRule := false
	oFsm.lastFlowToReconcile = lastFlowToReconcile
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		//TODO: Verify if using e.g. hashes for the structures here for comparison may generate
		//  countable run time optimization (perhaps with including the hash in kvStore storage?)
		if storedUniFlowParams.VlanRuleParams == loRuleParams {
			flowEntryMatch = true
			logger.Debugw(ctx, "UniVlanConfigFsm flow setting - rule already exists", log.Fields{
				"MatchVid":  strconv.FormatInt(int64(loRuleParams.MatchVid), 16),
				"SetVid":    strconv.FormatInt(int64(loRuleParams.SetVid), 16),
				"SetPcp":    loRuleParams.SetPcp,
				"device-id": oFsm.deviceID, " uni-id": oFsm.pOnuUniPort.UniID})
			var cookieMatch bool
			for _, newCookie := range aCookieSlice { // for all cookies available in the arguments
				cookieMatch = false
				for _, cookie := range storedUniFlowParams.CookieSlice {
					if cookie == newCookie {
						logger.Debugw(ctx, "UniVlanConfigFsm flow setting - and cookie already exists", log.Fields{
							"device-id": oFsm.deviceID, "cookie": cookie})
						cookieMatch = true
						break //found new cookie - no further search for this requested cookie
					}
				}
				if !cookieMatch {
					delayedCookie := oFsm.delayNewRuleForCookie(ctx, aCookieSlice)
					if delayedCookie != 0 {
						//a delay for adding the cookie to this rule is requested
						// take care of the mutex which is already locked here, need to unlock/lock accordingly to prevent deadlock in suspension
						oFsm.mutexFlowParams.Unlock()
						if deleteSuccess := oFsm.suspendNewRule(ctx); !deleteSuccess {
							logger.Errorw(ctx, "UniVlanConfigFsm suspended add-cookie-to-rule aborted", log.Fields{
								"device-id": oFsm.deviceID, "cookie": delayedCookie})
							err = fmt.Errorf(" UniVlanConfigFsm suspended add-cookie-to-rule aborted %s", oFsm.deviceID)
							oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
							return err
						}
						flowCookieModify, requestAppendRule = oFsm.reviseFlowConstellation(ctx, delayedCookie, loRuleParams)
						oFsm.mutexFlowParams.Lock()
					} else {
						logger.Debugw(ctx, "UniVlanConfigFsm flow setting -adding new cookie", log.Fields{
							"device-id": oFsm.deviceID, "cookie": newCookie})
						//as range works with copies of the slice we have to write to the original slice!!
						oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = append(oFsm.uniVlanFlowParamsSlice[flow].CookieSlice,
							newCookie)
						flowCookieModify = true
					}
				}
			} //for all new cookies
			break // found rule - no further rule search
		}
	}
	oFsm.mutexFlowParams.Unlock()

	if !flowEntryMatch { //it is (was) a new rule
		delayedCookie, deleteSuccess := oFsm.suspendIfRequiredNewRule(ctx, aCookieSlice)
		if !deleteSuccess {
			logger.Errorw(ctx, "UniVlanConfigFsm suspended add-new-rule aborted", log.Fields{
				"device-id": oFsm.deviceID, "cookie": delayedCookie})
			err = fmt.Errorf(" UniVlanConfigFsm suspended add-new-rule aborted %s", oFsm.deviceID)
			oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
			return err
		}
		requestAppendRule = true //default assumption here is that rule is to be appended
		flowCookieModify = true  //and that the the flow data base is to be updated
		if delayedCookie != 0 {  //it was suspended
			flowCookieModify, requestAppendRule = oFsm.reviseFlowConstellation(ctx, delayedCookie, loRuleParams)
		}
	}
	kvStoreWrite := false //default setting is to not write to kvStore immediately - will be done on FSM execution finally
	if requestAppendRule {
		oFsm.mutexFlowParams.Lock()
		if oFsm.NumUniFlows < cMaxAllowedFlows {
			loFlowParams := cmn.UniVlanFlowParams{VlanRuleParams: loRuleParams, RespChan: respChan}
			loFlowParams.CookieSlice = make([]uint64, 0)
			loFlowParams.CookieSlice = append(loFlowParams.CookieSlice, aCookieSlice...)
			if aMeter != nil {
				loFlowParams.Meter = aMeter
			}
			oFsm.uniVlanFlowParamsSlice = append(oFsm.uniVlanFlowParamsSlice, loFlowParams)
			logger.Debugw(ctx, "UniVlanConfigFsm flow add", log.Fields{
				"Cookies":  oFsm.uniVlanFlowParamsSlice[oFsm.NumUniFlows].CookieSlice,
				"MatchVid": strconv.FormatInt(int64(loRuleParams.MatchVid), 16),
				"SetVid":   strconv.FormatInt(int64(loRuleParams.SetVid), 16),
				"SetPcp":   loRuleParams.SetPcp, "numberofFlows": oFsm.NumUniFlows + 1,
				"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID})

			if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
				oFsm.reconcileVlanFilterList(ctx, uint16(loRuleParams.SetVid))
			}
			oFsm.NumUniFlows++
			pConfigVlanStateBaseFsm := oFsm.PAdaptFsm.PFsm

			if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
				logger.Debugw(ctx, "reconciling - skip omci-config of additional vlan rule",
					log.Fields{"fsmState": oFsm.PAdaptFsm.PFsm.Current(), "device-id": oFsm.deviceID})
				//attention: take care to release the mutexFlowParams when calling the FSM directly -
				//  synchronous FSM 'event/state' functions may rely on this mutex
				oFsm.mutexFlowParams.Unlock()
				if pConfigVlanStateBaseFsm.Is(VlanStConfigDone) {
					if fsmErr := pConfigVlanStateBaseFsm.Event(VlanEvSkipOmciConfig); fsmErr != nil {
						logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
							log.Fields{"fsmState": oFsm.PAdaptFsm.PFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
						err = fsmErr
						oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
						return err
					}
				}
				return nil
			}
			// note: theoretical it would be possible to clear the same rule from the remove slice
			//  (for entries that have not yet been started with removal)
			//  but that is getting quite complicated - maybe a future optimization in case it should prove reasonable
			// anyway the precondition here is that the FSM checks for rules to delete first and adds new rules afterwards

			if pConfigVlanStateBaseFsm.Is(VlanStConfigDone) {
				//have to re-trigger the FSM to proceed with outstanding incremental flow configuration
				if oFsm.ConfiguredUniFlow == 0 {
					// this is a restart with a complete new flow, we can re-use the initial flow config control
					// including the check, if the related techProfile is (still) available (probably also removed in between)
					//attention: take care to release the mutexFlowParams when calling the FSM directly -
					//  synchronous FSM 'event/state' functions may rely on this mutex
					oFsm.mutexFlowParams.Unlock()
					if fsmErr := pConfigVlanStateBaseFsm.Event(VlanEvRenew); fsmErr != nil {
						logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
							log.Fields{"fsmState": pConfigVlanStateBaseFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
					}
				} else {
					//some further flows are to be configured
					//store the actual rule that shall be worked upon in the following transient states
					if len(oFsm.uniVlanFlowParamsSlice) < int(oFsm.ConfiguredUniFlow) {
						//check introduced after having observed some panic here
						logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm - inconsistent counter",
							log.Fields{"ConfiguredUniFlow": oFsm.ConfiguredUniFlow,
								"sliceLen": len(oFsm.uniVlanFlowParamsSlice), "device-id": oFsm.deviceID})
						oFsm.mutexFlowParams.Unlock()
						err = fmt.Errorf("abort UniVlanConfigFsm on add due to internal counter mismatch %s", oFsm.deviceID)
						oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
						return err
					}

					oFsm.actualUniFlowParam = oFsm.uniVlanFlowParamsSlice[oFsm.ConfiguredUniFlow]
					//tpId of the next rule to be configured
					tpID := oFsm.actualUniFlowParam.VlanRuleParams.TpID
					oFsm.TpIDWaitingFor = tpID
					loSetVlan := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
					//attention: take care to release the mutexFlowParams when calling the FSM directly -
					//  synchronous FSM 'event/state' functions may rely on this mutex
					//  but it must be released already before calling getTechProfileDone() as it may already be locked
					//  by the techProfile processing call to VlanFsm.IsFlowRemovePending() (see VOL-4207)
					oFsm.mutexFlowParams.Unlock()
					loTechProfDone := oFsm.pUniTechProf.getTechProfileDone(ctx, oFsm.pOnuUniPort.UniID, tpID)
					logger.Debugw(ctx, "UniVlanConfigFsm - incremental config request (on setConfig)", log.Fields{
						"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID,
						"set-Vlan": loSetVlan, "tp-id": tpID, "ProfDone": loTechProfDone})

					var fsmErr error
					if loTechProfDone {
						// let the vlan processing continue with next rule
						fsmErr = pConfigVlanStateBaseFsm.Event(VlanEvIncrFlowConfig)
					} else {
						// set to waiting for Techprofile
						fsmErr = pConfigVlanStateBaseFsm.Event(VlanEvWaitTPIncr)
					}
					if fsmErr != nil {
						logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
							log.Fields{"fsmState": pConfigVlanStateBaseFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
						oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, fsmErr)
						return fsmErr
					}
				}
			} else {
				// if not in the appropriate state a new entry will be automatically considered later
				//   when the configDone state is reached
				oFsm.mutexFlowParams.Unlock()
			}
		} else {
			logger.Errorw(ctx, "UniVlanConfigFsm flow limit exceeded", log.Fields{
				"device-id": oFsm.deviceID, "flow-number": oFsm.NumUniFlows})
			oFsm.mutexFlowParams.Unlock()
			err = fmt.Errorf(" UniVlanConfigFsm flow limit exceeded %s", oFsm.deviceID)
			oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, err)
			return err
		}
	} else {
		// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
		kvStoreWrite = true // ensure actual data write to kvStore immediately (no FSM activity)
		// push response on response channel as there is nothing to be done for this flow
		oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, nil)

		oFsm.mutexFlowParams.RLock()
		if oFsm.NumUniFlows == oFsm.ConfiguredUniFlow {
			//all requested rules really have been configured
			// state transition notification is checked in deviceHandler
			oFsm.mutexFlowParams.RUnlock()
			if oFsm.pDeviceHandler != nil {
				//also the related TechProfile was already configured
				logger.Debugw(ctx, "UniVlanConfigFsm rule already set - send immediate add-success event for reason update", log.Fields{
					"device-id": oFsm.deviceID})
				// success indication without the need to write to kvStore (done already below with updated data from StorePersUniFlowConfig())
				go oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, cmn.OnuDeviceEvent(oFsm.requestEvent+cDeviceEventOffsetAddNoKvStore))
			}
		} else {
			//  avoid device reason update as the rule config connected to this flow may still be in progress
			//  and the device reason should only be updated on success of rule config
			logger.Debugw(ctx, "UniVlanConfigFsm rule already set but configuration ongoing, suppress early add-success event for reason update",
				log.Fields{"device-id": oFsm.deviceID,
					"NumberofRules": oFsm.NumUniFlows, "Configured rules": oFsm.ConfiguredUniFlow})
			oFsm.mutexFlowParams.RUnlock()
		}
	}

	if flowCookieModify { // some change was done to the flow entries
		//permanently store flow config for reconcile case
		oFsm.mutexFlowParams.RLock()
		if err := oFsm.pDeviceHandler.StorePersUniFlowConfig(ctx, oFsm.pOnuUniPort.UniID,
			&oFsm.uniVlanFlowParamsSlice, kvStoreWrite); err != nil {
			oFsm.mutexFlowParams.RUnlock()
			logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oFsm.deviceID})
			return err
		}
		oFsm.mutexFlowParams.RUnlock()
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) suspendAddRule(ctx context.Context, apRemoveFlowParams *uniRemoveVlanFlowParams) error {
	oFsm.mutexFlowParams.Lock()
	deleteChannel := apRemoveFlowParams.removeChannel
	apRemoveFlowParams.isSuspendedOnAdd = true
	oFsm.mutexFlowParams.Unlock()

	// isSuspendedOnAdd is not reset here-after as the assumption is, that after
	select {
	case success := <-deleteChannel:
		//no need to reset isSuspendedOnAdd as in this case the removeElement will be deleted completely
		if success {
			logger.Infow(ctx, "resume adding this rule after having completed deletion", log.Fields{
				"device-id": oFsm.deviceID})
			return nil
		}
		return fmt.Errorf("suspend aborted, also aborting add-activity: %s", oFsm.deviceID)
	case <-time.After(oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second):
		oFsm.mutexFlowParams.Lock()
		if apRemoveFlowParams != nil {
			apRemoveFlowParams.isSuspendedOnAdd = false
		}
		oFsm.mutexFlowParams.Unlock()
		logger.Errorw(ctx, "timeout waiting for deletion of rule, also aborting add-activity", log.Fields{
			"device-id": oFsm.deviceID})
		return fmt.Errorf("suspend aborted on timeout, also aborting add-activity: %s", oFsm.deviceID)
	}
}

// VOL-3828 flow config sequence workaround ###########  start ##########
func (oFsm *UniVlanConfigFsm) delayNewRuleForCookie(ctx context.Context, aCookieSlice []uint64) uint64 {
	//assumes mutexFlowParams.Lock() protection from caller!
	if oFsm.delayNewRuleCookie == 0 && len(aCookieSlice) == 1 {
		// if not already waiting, limitation for this workaround is to just have one overlapping cookie/rule
		// suspend check is done only if there is only one cookie in the request
		//  background: more elements only expected in reconcile use case, where no conflicting sequence is to be expected
		newCookie := aCookieSlice[0]
		for _, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
			for _, cookie := range storedUniFlowParams.CookieSlice {
				if cookie == newCookie {
					logger.Debugw(ctx, "UniVlanConfigFsm flow setting - new cookie still exists for some rule", log.Fields{
						"device-id": oFsm.deviceID, "cookie": cookie, "exists with SetVlan": storedUniFlowParams.VlanRuleParams.SetVid})
					oFsm.delayNewRuleCookie = newCookie
					return newCookie //found new cookie in some existing rule
				}
			} // for all stored cookies of the actual inspected rule
		} //for all rules
	}
	return 0 //no delay requested
}
func (oFsm *UniVlanConfigFsm) suspendNewRule(ctx context.Context) bool {
	oFsm.mutexFlowParams.RLock()
	logger.Infow(ctx, "Need to suspend adding this rule as long as the cookie is still connected to some other rule", log.Fields{
		"device-id": oFsm.deviceID, "cookie": oFsm.delayNewRuleCookie})
	oFsm.mutexFlowParams.RUnlock()
	cookieDeleted := true //default assumption also for timeout (just try to continue as if removed)
	select {
	case cookieDeleted = <-oFsm.chCookieDeleted:
		logger.Infow(ctx, "resume adding this rule after having deleted cookie in some other rule or abort", log.Fields{
			"device-id": oFsm.deviceID, "cookie": oFsm.delayNewRuleCookie, "deleted": cookieDeleted})
	case <-time.After(oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "timeout waiting for deletion of cookie in some other rule, just try to continue", log.Fields{
			"device-id": oFsm.deviceID, "cookie": oFsm.delayNewRuleCookie})
	}
	oFsm.mutexFlowParams.Lock()
	oFsm.delayNewRuleCookie = 0
	oFsm.mutexFlowParams.Unlock()
	return cookieDeleted
}
func (oFsm *UniVlanConfigFsm) suspendIfRequiredNewRule(ctx context.Context, aCookieSlice []uint64) (uint64, bool) {
	oFsm.mutexFlowParams.Lock()
	delayedCookie := oFsm.delayNewRuleForCookie(ctx, aCookieSlice)
	oFsm.mutexFlowParams.Unlock()

	deleteSuccess := true
	if delayedCookie != 0 {
		deleteSuccess = oFsm.suspendNewRule(ctx)
	}
	return delayedCookie, deleteSuccess
}

//returns flowModified, RuleAppendRequest
func (oFsm *UniVlanConfigFsm) reviseFlowConstellation(ctx context.Context, aCookie uint64, aUniVlanRuleParams cmn.UniVlanRuleParams) (bool, bool) {
	flowEntryMatch := false
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		if storedUniFlowParams.VlanRuleParams == aUniVlanRuleParams {
			flowEntryMatch = true
			logger.Debugw(ctx, "UniVlanConfigFsm flow revise - rule already exists", log.Fields{
				"device-id": oFsm.deviceID})
			cookieMatch := false
			for _, cookie := range storedUniFlowParams.CookieSlice {
				if cookie == aCookie {
					logger.Debugw(ctx, "UniVlanConfigFsm flow revise - and cookie already exists", log.Fields{
						"device-id": oFsm.deviceID, "cookie": cookie})
					cookieMatch = true
					break //found new cookie - no further search for this requested cookie
				}
			}
			if !cookieMatch {
				logger.Debugw(ctx, "UniVlanConfigFsm flow revise -adding new cookie", log.Fields{
					"device-id": oFsm.deviceID, "cookie": aCookie})
				//as range works with copies of the slice we have to write to the original slice!!
				oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = append(oFsm.uniVlanFlowParamsSlice[flow].CookieSlice,
					aCookie)
				return true, false //flowModified, NoRuleAppend
			}
			break // found rule - no further rule search
		}
	}
	if !flowEntryMatch { //it is a new rule
		return true, true //flowModified, RuleAppend
	}
	return false, false //flowNotModified, NoRuleAppend
}

// VOL-3828 flow config sequence workaround ###########  end ##########

//RemoveUniFlowParams verifies on existence of flow cookie,
// if found removes cookie from flow cookie list and if this is empty
// initiates removal of the flow related configuration from the ONU (via OMCI)
func (oFsm *UniVlanConfigFsm) RemoveUniFlowParams(ctx context.Context, aCookie uint64, respChan *chan error) error {
	if oFsm == nil {
		logger.Error(ctx, "no valid UniVlanConfigFsm!")
		return fmt.Errorf("no-valid-UniVlanConfigFsm")
	}
	var deletedCookie uint64
	flowCookieMatch := false
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
remove_loop:
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		for i, cookie := range storedUniFlowParams.CookieSlice {
			if cookie == aCookie {
				logger.Debugw(ctx, "UniVlanConfigFsm flow removal - cookie found", log.Fields{
					"device-id": oFsm.deviceID, "cookie": cookie})
				deletedCookie = aCookie
				//remove the cookie from the cookie slice and verify it is getting empty
				if len(storedUniFlowParams.CookieSlice) == 1 {
					// had to shift content to function due to sca complexity
					flowCookieMatch = oFsm.removeRuleComplete(ctx, storedUniFlowParams, aCookie, respChan)
					//persistencyData write is now part of removeRuleComplete() (on success)
				} else {
					flowCookieMatch = true
					//cut off the requested cookie by slicing out this element
					oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = append(
						oFsm.uniVlanFlowParamsSlice[flow].CookieSlice[:i],
						oFsm.uniVlanFlowParamsSlice[flow].CookieSlice[i+1:]...)
					// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
					// state transition notification is checked in deviceHandler
					if oFsm.pDeviceHandler != nil {
						// success indication without the need to write to kvStore (done already below with updated data from StorePersUniFlowConfig())
						go oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, cmn.OnuDeviceEvent(oFsm.requestEvent+cDeviceEventOffsetRemoveNoKvStore))
					}
					logger.Debugw(ctx, "UniVlanConfigFsm flow removal - rule persists with still valid cookies", log.Fields{
						"device-id": oFsm.deviceID, "cookies": oFsm.uniVlanFlowParamsSlice[flow].CookieSlice})
					if deletedCookie == oFsm.delayNewRuleCookie {
						//the delayedNewCookie is the one that is currently deleted, but the rule still exist with other cookies
						//as long as there are further cookies for this rule indicate there is still some cookie to be deleted
						//simply use the first one
						oFsm.delayNewRuleCookie = oFsm.uniVlanFlowParamsSlice[flow].CookieSlice[0]
						logger.Debugw(ctx, "UniVlanConfigFsm remaining cookie awaited for deletion before new rule add", log.Fields{
							"device-id": oFsm.deviceID, "cookie": oFsm.delayNewRuleCookie})
					}
					// Push response on the response channel
					oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, nil)
					//permanently store the modified flow config for reconcile case and immediately write to KvStore
					if oFsm.pDeviceHandler != nil {
						if err := oFsm.pDeviceHandler.StorePersUniFlowConfig(ctx, oFsm.pOnuUniPort.UniID,
							&oFsm.uniVlanFlowParamsSlice, true); err != nil {
							logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oFsm.deviceID})
							return err
						}
					}
				}
				break remove_loop //found the cookie - no further search for this requested cookie
			}
		}
	} //search all flows
	if !flowCookieMatch { //some cookie remove-request for a cookie that does not exist in the FSM data
		logger.Warnw(ctx, "UniVlanConfigFsm flow removal - remove-cookie not found", log.Fields{
			"device-id": oFsm.deviceID, "remove-cookie": aCookie})
		// but accept the request with success as no such cookie (flow) does exist
		// no activity within the FSM for OMCI processing, the deviceReason may be updated immediately
		// state transition notification is checked in deviceHandler
		if oFsm.pDeviceHandler != nil {
			// success indication without the need to write to kvStore (no change)
			go oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, cmn.OnuDeviceEvent(oFsm.requestEvent+cDeviceEventOffsetRemoveNoKvStore))
		}
		// Push response on the response channel
		oFsm.pushReponseOnFlowResponseChannel(ctx, respChan, nil)
		return nil
	} //unknown cookie

	return nil
}

// removeRuleComplete initiates the complete removal of a VLAN rule (from single cookie element)
// requires mutexFlowParams to be locked at call
func (oFsm *UniVlanConfigFsm) removeRuleComplete(ctx context.Context,
	aUniFlowParams cmn.UniVlanFlowParams, aCookie uint64, respChan *chan error) bool {
	pConfigVlanStateBaseFsm := oFsm.PAdaptFsm.PFsm
	var cancelPendingConfig bool = false
	var loRemoveParams uniRemoveVlanFlowParams = uniRemoveVlanFlowParams{}
	logger.Debugw(ctx, "UniVlanConfigFsm flow removal - full flow removal", log.Fields{
		"device-id": oFsm.deviceID})
	//rwCore flow recovery may be the reason for this delete, in which case the flowToBeDeleted may be the same
	//  as the one still waiting in the FSM as toAdd but waiting for TechProfileConfig
	//  so we have to check if we have to abort the outstanding AddRequest and regard the current DelRequest as done
	//  if the Fsm is in some other transient (config) state, we will reach the DelRequest later and correctly process it then
	if pConfigVlanStateBaseFsm.Is(VlanStWaitingTechProf) {
		logger.Debugw(ctx, "UniVlanConfigFsm was waiting for TechProf config with add-request, just aborting the outstanding add",
			log.Fields{"device-id": oFsm.deviceID})
		cancelPendingConfig = true
	} else {
		//create a new element for the removeVlanFlow slice
		loRemoveParams = uniRemoveVlanFlowParams{
			vlanRuleParams: aUniFlowParams.VlanRuleParams,
			cookie:         aCookie,
			respChan:       respChan,
		}
		loRemoveParams.removeChannel = make(chan bool)
		oFsm.uniRemoveFlowsSlice = append(oFsm.uniRemoveFlowsSlice, loRemoveParams)
	}

	usedTpID := aUniFlowParams.VlanRuleParams.TpID
	if len(oFsm.uniVlanFlowParamsSlice) <= 1 {
		//at this point it is evident that no flow anymore will refer to a still possibly active Techprofile
		//request that this profile gets deleted before a new flow add is allowed (except for some aborted add)
		if !cancelPendingConfig {
			// ensure mutexFlowParams not locked before calling some TPProcessing activity (that might already be pending on it)
			oFsm.mutexFlowParams.Unlock()
			logger.Debugw(ctx, "UniVlanConfigFsm flow removal requested - set TechProfile to-delete", log.Fields{
				"device-id": oFsm.deviceID})
			if oFsm.pUniTechProf != nil {
				oFsm.pUniTechProf.SetProfileToDelete(oFsm.pOnuUniPort.UniID, usedTpID, true)
			}
			oFsm.mutexFlowParams.Lock()
		}
	} else {
		if !cancelPendingConfig {
			oFsm.updateTechProfileToDelete(ctx, usedTpID)
		}
	}
	//trigger the FSM to remove the relevant rule
	if cancelPendingConfig {
		//as the uniFlow parameters are already stored (for add) but no explicit removal is done anymore
		//  the paramSlice has to be updated with rule-removal, which also then updates NumUniFlows
		//call from 'non-configured' state of the rules
		if err := oFsm.removeFlowFromParamsSlice(ctx, aCookie, false); err != nil {
			//something quite inconsistent detected, perhaps just try to recover with FSM reset
			oFsm.mutexFlowParams.Unlock()
			if fsmErr := pConfigVlanStateBaseFsm.Event(VlanEvReset); fsmErr != nil {
				logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
					log.Fields{"fsmState": pConfigVlanStateBaseFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
			}
			return false //data base update could not be done, return like cookie not found
		}

		oFsm.requestEventOffset = uint8(cDeviceEventOffsetRemoveWithKvStore) //offset for last flow-remove activity (with kvStore request)
		//attention: take care to release and re-take the mutexFlowParams when calling the FSM directly -
		//  synchronous FSM 'event/state' functions may rely on this mutex
		oFsm.mutexFlowParams.Unlock()
		if fsmErr := pConfigVlanStateBaseFsm.Event(VlanEvCancelOutstandingConfig); fsmErr != nil {
			logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
				log.Fields{"fsmState": pConfigVlanStateBaseFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
		}
		oFsm.mutexFlowParams.Lock()
		return true
	}
	if pConfigVlanStateBaseFsm.Is(VlanStConfigDone) {
		logger.Debugw(ctx, "UniVlanConfigFsm rule removal request", log.Fields{
			"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID,
			"tp-id":    loRemoveParams.vlanRuleParams.TpID,
			"set-Vlan": loRemoveParams.vlanRuleParams.SetVid})
		//have to re-trigger the FSM to proceed with outstanding incremental flow configuration
		//attention: take care to release and re-take the mutexFlowParams when calling the FSM directly -
		//  synchronous FSM 'event/state' functions may rely on this mutex
		oFsm.mutexFlowParams.Unlock()
		if fsmErr := pConfigVlanStateBaseFsm.Event(VlanEvRemFlowConfig); fsmErr != nil {
			logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm!",
				log.Fields{"fsmState": pConfigVlanStateBaseFsm.Current(), "error": fsmErr, "device-id": oFsm.deviceID})
		}
		oFsm.mutexFlowParams.Lock()
	} // if not in the appropriate state a new entry will be automatically considered later
	//   when the configDone state is reached
	return true
}

//removeFlowFromParamsSlice removes a flow from stored  uniVlanFlowParamsSlice based on the cookie
//  it assumes that adding cookies for this flow (including the actual one to delete) was prevented
//  from the start of the deletion request to avoid to much interference
//  so when called, there can only be one cookie active for this flow
// requires mutexFlowParams to be locked at call
func (oFsm *UniVlanConfigFsm) removeFlowFromParamsSlice(ctx context.Context, aCookie uint64, aWasConfigured bool) error {
	logger.Debugw(ctx, "UniVlanConfigFsm flow removal from ParamsSlice", log.Fields{
		"device-id": oFsm.deviceID, "cookie": aCookie})
	cookieFound := false
removeFromSlice_loop:
	for flow, storedUniFlowParams := range oFsm.uniVlanFlowParamsSlice {
		// if UniFlowParams exists, cookieSlice should always have at least one element
		cookieSliceLen := len(storedUniFlowParams.CookieSlice)
		if cookieSliceLen == 1 {
			if storedUniFlowParams.CookieSlice[0] == aCookie {
				cookieFound = true
			}
		} else if cookieSliceLen == 0 {
			errStr := "UniVlanConfigFsm unexpected cookie slice length 0  - removal in uniVlanFlowParamsSlice aborted"
			logger.Errorw(ctx, errStr, log.Fields{"device-id": oFsm.deviceID})
			return errors.New(errStr)
		} else {
			errStr := "UniVlanConfigFsm flow removal unexpected cookie slice length, but rule removal continued"
			logger.Errorw(ctx, errStr, log.Fields{
				"cookieSliceLen": len(oFsm.uniVlanFlowParamsSlice), "device-id": oFsm.deviceID})
			for _, cookie := range storedUniFlowParams.CookieSlice {
				if cookie == aCookie {
					cookieFound = true
					break
				}
			}
		}
		if cookieFound {
			logger.Debugw(ctx, "UniVlanConfigFsm flow removal from ParamsSlice - cookie found", log.Fields{
				"device-id": oFsm.deviceID, "cookie": aCookie})
			//remove the actual element from the addVlanFlow slice
			// oFsm.uniVlanFlowParamsSlice[flow].CookieSlice = nil //automatically done by garbage collector
			if len(oFsm.uniVlanFlowParamsSlice) <= 1 {
				oFsm.NumUniFlows = 0              //no more flows
				oFsm.ConfiguredUniFlow = 0        //no more flows configured
				oFsm.uniVlanFlowParamsSlice = nil //reset the slice
				//at this point it is evident that no flow anymore refers to a still possibly active Techprofile
				//request that this profile gets deleted before a new flow add is allowed
				logger.Debugw(ctx, "UniVlanConfigFsm flow removal from ParamsSlice - no more flows", log.Fields{
					"device-id": oFsm.deviceID})
			} else {
				oFsm.NumUniFlows--
				if aWasConfigured && oFsm.ConfiguredUniFlow > 0 {
					oFsm.ConfiguredUniFlow--
				}
				if !aWasConfigured {
					// We did not actually process this flow but was removed before that.
					// Indicate success response for the flow to caller who is blocking on a response
					oFsm.pushReponseOnFlowResponseChannel(ctx, storedUniFlowParams.RespChan, nil)
				}

				//cut off the requested flow by slicing out this element
				oFsm.uniVlanFlowParamsSlice = append(
					oFsm.uniVlanFlowParamsSlice[:flow], oFsm.uniVlanFlowParamsSlice[flow+1:]...)
				logger.Debugw(ctx, "UniVlanConfigFsm flow removal - specific flow removed from data", log.Fields{
					"device-id": oFsm.deviceID})
			}
			break removeFromSlice_loop //found the cookie - no further search for this requested cookie
		}
	} //search all flows
	if !cookieFound {
		errStr := "UniVlanConfigFsm cookie for removal not found, internal counter not updated"
		logger.Errorw(ctx, errStr, log.Fields{"device-id": oFsm.deviceID})
		return errors.New(errStr)
	}
	//if the cookie was found and removed from uniVlanFlowParamsSlice above now write the modified persistency data
	//  KVStore update will be done after reaching the requested FSM end state (not immediately here)
	if err := oFsm.pDeviceHandler.StorePersUniFlowConfig(ctx, oFsm.pOnuUniPort.UniID,
		&oFsm.uniVlanFlowParamsSlice, false); err != nil {
		logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	return nil
}

// requires mutexFlowParams to be locked at call
func (oFsm *UniVlanConfigFsm) updateTechProfileToDelete(ctx context.Context, usedTpID uint8) {
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
		logger.Debugw(ctx, "UniVlanConfigFsm tp-id used in deleted flow is still used in other flows", log.Fields{
			"device-id": oFsm.deviceID, "tp-id": usedTpID})
	} else {
		logger.Debugw(ctx, "UniVlanConfigFsm tp-id used in deleted flow is not used anymore - set TechProfile to-delete", log.Fields{
			"device-id": oFsm.deviceID, "tp-id": usedTpID})
		// ensure mutexFlowParams not locked before calling some TPProcessing activity (that might already be pending on it)
		oFsm.mutexFlowParams.Unlock()
		if oFsm.pUniTechProf != nil {
			//request that this profile gets deleted before a new flow add is allowed
			oFsm.pUniTechProf.SetProfileToDelete(oFsm.pOnuUniPort.UniID, usedTpID, true)
		}
		oFsm.mutexFlowParams.Lock()
	}
}

func (oFsm *UniVlanConfigFsm) enterPreparing(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniVlanConfigFsm preparing", log.Fields{"device-id": oFsm.deviceID})

	// this FSM is not intended for re-start, needs always new creation for a new run
	// (self-destroying - compare enterDisabled())
	oFsm.omciMIdsResponseReceived = make(chan bool)
	oFsm.chCookieDeleted = make(chan bool)
	// start go routine for processing of LockState messages
	go oFsm.processOmciVlanMessages(ctx)
	//let the state machine run forward from here directly
	pConfigVlanStateAFsm := oFsm.PAdaptFsm
	if pConfigVlanStateAFsm != nil {
		if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
			logger.Debugw(ctx, "reconciling - skip omci-config of vlan rule",
				log.Fields{"fsmState": oFsm.PAdaptFsm.PFsm.Current(), "device-id": oFsm.deviceID})
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(VlanEvSkipOmciConfig)
			}(pConfigVlanStateAFsm)
			return
		}
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(VlanEvPrepareDone)
		}(pConfigVlanStateAFsm)
		return
	}
	logger.Errorw(ctx, "UniVlanConfigFsm abort: invalid FSM pointer", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	//should never happen, else: recovery would be needed from outside the FSM
}

func (oFsm *UniVlanConfigFsm) enterConfigStarting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniVlanConfigFsm start vlan configuration", log.Fields{"device-id": oFsm.deviceID})
	pConfigVlanStateAFsm := oFsm.PAdaptFsm
	if pConfigVlanStateAFsm != nil {
		oFsm.mutexFlowParams.Lock()
		//possibly the entry is not valid anymore based on intermediate delete requests
		//just a basic protection ...
		if len(oFsm.uniVlanFlowParamsSlice) == 0 {
			oFsm.mutexFlowParams.Unlock()
			logger.Debugw(ctx, "UniVlanConfigFsm start: no rule entry anymore available", log.Fields{
				"device-id": oFsm.deviceID})
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(VlanEvReset)
			}(pConfigVlanStateAFsm)
			return
		}
		//access to uniVlanFlowParamsSlice is done on first element only here per definition
		//store the actual rule that shall be worked upon in the following transient states
		oFsm.actualUniFlowParam = oFsm.uniVlanFlowParamsSlice[0]
		tpID := oFsm.actualUniFlowParam.VlanRuleParams.TpID
		oFsm.TpIDWaitingFor = tpID
		loSetVlan := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
		//attention: take care to release the mutexFlowParams when calling the FSM directly -
		//  synchronous FSM 'event/state' functions may rely on this mutex
		//  but it must be released already before calling getTechProfileDone() as it may already be locked
		//  by the techProfile processing call to VlanFsm.IsFlowRemovePending() (see VOL-4207)
		oFsm.mutexFlowParams.Unlock()
		loTechProfDone := oFsm.pUniTechProf.getTechProfileDone(ctx, oFsm.pOnuUniPort.UniID, uint8(tpID))
		logger.Debugw(ctx, "UniVlanConfigFsm - start with first rule", log.Fields{
			"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID,
			"set-Vlan": loSetVlan, "tp-id": tpID, "ProfDone": loTechProfDone})

		// Can't call FSM Event directly, decoupling it
		go func(aPAFsm *cmn.AdapterFsm, aTechProfDone bool) {
			if aPAFsm != nil && aPAFsm.PFsm != nil {
				if aTechProfDone {
					// let the vlan processing begin
					_ = aPAFsm.PFsm.Event(VlanEvStartConfig)
				} else {
					// set to waiting for Techprofile
					_ = aPAFsm.PFsm.Event(VlanEvWaitTechProf)
				}
			}
		}(pConfigVlanStateAFsm, loTechProfDone)
	} else {
		logger.Errorw(ctx, "UniVlanConfigFsm abort: invalid FSM pointer", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		//should never happen, else: recovery would be needed from outside the FSM
		return
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigVtfd(ctx context.Context, e *fsm.Event) {
	//mutex protection is required for possible concurrent access to FSM members
	oFsm.mutexFlowParams.Lock()
	oFsm.TpIDWaitingFor = 0 //reset indication to avoid misinterpretation
	if oFsm.actualUniFlowParam.VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		oFsm.mutexFlowParams.Unlock()
		logger.Debugw(ctx, "UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		// let the FSM proceed ... (from within this state all internal pointers may be expected to be correct)
		pConfigVlanStateAFsm := oFsm.PAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(VlanEvRxConfigVtfd)
		}(pConfigVlanStateAFsm)
	} else {
		// This attribute uniquely identifies each instance of this managed entity. Through an identical ID,
		// this managed entity is implicitly linked to an instance of the MAC bridge port configuration data ME.
		vtfdID, _ := cmn.GenerateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo), uint16(oFsm.actualUniFlowParam.VlanRuleParams.TpID))
		logger.Debugw(ctx, "UniVlanConfigFsm create VTFD", log.Fields{
			"EntitytId": strconv.FormatInt(int64(vtfdID), 16),
			"in state":  e.FSM.Current(), "device-id": oFsm.deviceID,
			"macBpNo": oFsm.pOnuUniPort.MacBpNo, "TpID": oFsm.actualUniFlowParam.VlanRuleParams.TpID})
		// setVid is assumed to be masked already by the caller to 12 bit
		oFsm.vlanFilterList[0] = uint16(oFsm.actualUniFlowParam.VlanRuleParams.SetVid)
		oFsm.mutexFlowParams.Unlock()
		vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization
		vtfdFilterList[0] = oFsm.vlanFilterList[0]
		oFsm.numVlanFilterEntries = 1
		meParams := me.ParamData{
			EntityID: vtfdID,
			Attributes: me.AttributeValueMap{
				"VlanFilterList":   vtfdFilterList, //omci lib wants a slice for serialization
				"ForwardOperation": uint8(0x10),    //VID investigation
				"NumberOfEntries":  oFsm.numVlanFilterEntries,
			},
		}
		logger.Debugw(ctx, "UniVlanConfigFsm sendcreate VTFD", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.SendCreateVtfdVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
			oFsm.PAdaptFsm.CommChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "VTFD create failed, aborting UniVlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			pConfigVlanStateAFsm := oFsm.PAdaptFsm
			if pConfigVlanStateAFsm != nil {
				go func(a_pAFsm *cmn.AdapterFsm) {
					_ = a_pAFsm.PFsm.Event(VlanEvReset)
				}(pConfigVlanStateAFsm)
			}
			return
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
		//  send shall return (dual format) error code that can be used here for immediate error treatment
		//  (relevant to all used sendXX() methods in this (and other) FSM's)
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigEvtocd(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniVlanConfigFsm - start config EVTOCD loop", log.Fields{
		"device-id": oFsm.deviceID})
	oFsm.requestEventOffset = uint8(cDeviceEventOffsetAddWithKvStore) //0 offset for last flow-add activity
	go func() {
		//using the first element in the slice because it's the first flow per definition here
		errEvto := oFsm.performConfigEvtocdEntries(ctx, 0)
		//This is correct passing scenario
		if errEvto == nil {
			oFsm.mutexFlowParams.RLock()
			tpID := oFsm.actualUniFlowParam.VlanRuleParams.TpID
			vlanID := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
			configuredUniFlows := oFsm.ConfiguredUniFlow
			// ensure mutexFlowParams not locked before calling some TPProcessing activity (that might already be pending on it)
			oFsm.mutexFlowParams.RUnlock()
			for _, gemPort := range oFsm.pUniTechProf.getMulticastGemPorts(ctx, oFsm.pOnuUniPort.UniID, uint8(tpID)) {
				logger.Infow(ctx, "Setting multicast MEs, with first flow", log.Fields{"deviceID": oFsm.deviceID,
					"techProfile": tpID, "gemPort": gemPort, "vlanID": vlanID, "ConfiguredUniFlow": configuredUniFlows})
				errCreateAllMulticastME := oFsm.performSettingMulticastME(ctx, tpID, gemPort,
					vlanID)
				if errCreateAllMulticastME != nil {
					logger.Errorw(ctx, "Multicast ME create failed, aborting AniConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				}
			}
			//If this first flow contains a meter, then create TD for related gems.
			if oFsm.actualUniFlowParam.Meter != nil {
				logger.Debugw(ctx, "Creating Traffic Descriptor", log.Fields{"device-id": oFsm.deviceID, "meter": oFsm.actualUniFlowParam.Meter})
				for _, gemPort := range oFsm.pUniTechProf.getBidirectionalGemPortIDsForTP(ctx, oFsm.pOnuUniPort.UniID, tpID) {
					logger.Debugw(ctx, "Creating Traffic Descriptor for gem", log.Fields{"device-id": oFsm.deviceID, "meter": oFsm.actualUniFlowParam.Meter, "gem": gemPort})
					errCreateTrafficDescriptor := oFsm.createTrafficDescriptor(ctx, oFsm.actualUniFlowParam.Meter, tpID,
						oFsm.pOnuUniPort.UniID, gemPort)
					if errCreateTrafficDescriptor != nil {
						logger.Errorw(ctx, "Create Traffic Descriptor create failed, aborting Ani Config FSM!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					}
				}
			}

			//TODO Possibly insert new state for multicast --> possibly another jira/later time.
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvRxConfigEvtocd)
		}
	}()
}

func (oFsm *UniVlanConfigFsm) enterVlanConfigDone(ctx context.Context, e *fsm.Event) {

	oFsm.mutexFlowParams.Lock()

	logger.Infow(ctx, "UniVlanConfigFsm config done - checking on more flows", log.Fields{
		"device-id":         oFsm.deviceID,
		"overall-uni-rules": oFsm.NumUniFlows, "configured-uni-rules": oFsm.ConfiguredUniFlow})
	if len(oFsm.uniVlanFlowParamsSlice) > 0 {
		oFsm.pushReponseOnFlowResponseChannel(ctx, oFsm.actualUniFlowParam.RespChan, nil)
	}

	pConfigVlanStateAFsm := oFsm.PAdaptFsm
	if pConfigVlanStateAFsm == nil {
		oFsm.mutexFlowParams.Unlock()
		logger.Errorw(ctx, "UniVlanConfigFsm abort: invalid FSM pointer", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
		//should never happen, else: recovery would be needed from outside the FSM
		return
	}
	pConfigVlanStateBaseFsm := pConfigVlanStateAFsm.PFsm
	if len(oFsm.uniRemoveFlowsSlice) > 0 {
		//some further flows are to be removed, removal always starts with the first element
		logger.Debugw(ctx, "UniVlanConfigFsm rule removal from ConfigDone", log.Fields{
			"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID,
			"tp-id":    oFsm.uniRemoveFlowsSlice[0].vlanRuleParams.TpID,
			"set-Vlan": oFsm.uniRemoveFlowsSlice[0].vlanRuleParams.SetVid})
		oFsm.mutexFlowParams.Unlock()
		// Can't call FSM Event directly, decoupling it
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(VlanEvRemFlowConfig)
		}(pConfigVlanStateBaseFsm)
		return
	}
	if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
		oFsm.ConfiguredUniFlow = oFsm.NumUniFlows
		if oFsm.lastFlowToReconcile {
			logger.Debugw(ctx, "reconciling - flow processing finished", log.Fields{"device-id": oFsm.deviceID})
			oFsm.pOnuDeviceEntry.SetReconcilingFlows(false)
			oFsm.pOnuDeviceEntry.SetChReconcilingFlowsFinished(true)
		}
		logger.Debugw(ctx, "reconciling - skip enterVlanConfigDone processing",
			log.Fields{"NumUniFlows": oFsm.NumUniFlows, "ConfiguredUniFlow": oFsm.ConfiguredUniFlow, "device-id": oFsm.deviceID})
		oFsm.mutexFlowParams.Unlock()
		return
	}
	if oFsm.NumUniFlows > oFsm.ConfiguredUniFlow {
		if oFsm.ConfiguredUniFlow == 0 {
			oFsm.mutexFlowParams.Unlock()
			// this is a restart with a complete new flow, we can re-use the initial flow config control
			// including the check, if the related techProfile is (still) available (probably also removed in between)
			// Can't call FSM Event directly, decoupling it
			go func(a_pBaseFsm *fsm.FSM) {
				_ = a_pBaseFsm.Event(VlanEvRenew)
			}(pConfigVlanStateBaseFsm)
			return
		}

		//some further flows are to be configured
		//store the actual rule that shall be worked upon in the following transient states
		if len(oFsm.uniVlanFlowParamsSlice) < int(oFsm.ConfiguredUniFlow) {
			//check introduced after having observed some panic in this processing
			logger.Errorw(ctx, "error in FsmEvent handling UniVlanConfigFsm in ConfigDone - inconsistent counter",
				log.Fields{"ConfiguredUniFlow": oFsm.ConfiguredUniFlow,
					"sliceLen": len(oFsm.uniVlanFlowParamsSlice), "device-id": oFsm.deviceID})
			oFsm.mutexFlowParams.Unlock()
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(VlanEvReset)
			}(pConfigVlanStateAFsm)
			return
		}
		oFsm.actualUniFlowParam = oFsm.uniVlanFlowParamsSlice[oFsm.ConfiguredUniFlow]
		//tpId of the next rule to be configured
		tpID := oFsm.actualUniFlowParam.VlanRuleParams.TpID
		oFsm.TpIDWaitingFor = tpID
		loSetVlan := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
		//attention: take care to release the mutexFlowParams when calling the FSM directly -
		//  synchronous FSM 'event/state' functions may rely on this mutex
		//  but it must be released already before calling getTechProfileDone() as it may already be locked
		//  by the techProfile processing call to VlanFsm.IsFlowRemovePending() (see VOL-4207)
		oFsm.mutexFlowParams.Unlock()
		loTechProfDone := oFsm.pUniTechProf.getTechProfileDone(ctx, oFsm.pOnuUniPort.UniID, tpID)
		logger.Debugw(ctx, "UniVlanConfigFsm - incremental config request", log.Fields{
			"device-id": oFsm.deviceID, "uni-id": oFsm.pOnuUniPort.UniID,
			"set-Vlan": loSetVlan, "tp-id": tpID, "ProfDone": loTechProfDone})

		// Can't call FSM Event directly, decoupling it
		go func(aPBaseFsm *fsm.FSM, aTechProfDone bool) {
			if aTechProfDone {
				// let the vlan processing continue with next rule
				_ = aPBaseFsm.Event(VlanEvIncrFlowConfig)
			} else {
				// set to waiting for Techprofile
				_ = aPBaseFsm.Event(VlanEvWaitTPIncr)
			}
		}(pConfigVlanStateBaseFsm, loTechProfDone)
		return
	}
	oFsm.mutexFlowParams.Unlock()
	logger.Debugw(ctx, "UniVlanConfigFsm - VLAN config done: send dh event notification", log.Fields{
		"device-id": oFsm.deviceID})
	// it might appear that some flows are requested also after 'flowPushed' event has been generated ...
	// state transition notification is checked in deviceHandler
	// note: 'flowPushed' event is only generated if all 'pending' rules are configured
	if oFsm.pDeviceHandler != nil {
		//making use of the add->remove successor enum assumption/definition
		go oFsm.pDeviceHandler.DeviceProcStatusUpdate(ctx, cmn.OnuDeviceEvent(uint8(oFsm.requestEvent)+oFsm.requestEventOffset))
	}
}

func (oFsm *UniVlanConfigFsm) enterConfigIncrFlow(ctx context.Context, e *fsm.Event) {

	if oFsm.pDeviceHandler.IsSkipOnuConfigReconciling() {
		logger.Debugw(ctx, "reconciling - skip further processing for incremental flow",
			log.Fields{"fsmState": oFsm.PAdaptFsm.PFsm.Current(), "device-id": oFsm.deviceID})
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(VlanEvSkipIncFlowConfig)
		}(oFsm.PAdaptFsm.PFsm)
		return
	}
	oFsm.mutexFlowParams.Lock()
	logger.Debugw(ctx, "UniVlanConfigFsm - start config further incremental flow", log.Fields{
		"recent flow-number": oFsm.ConfiguredUniFlow,
		"device-id":          oFsm.deviceID})
	oFsm.TpIDWaitingFor = 0 //reset indication to avoid misinterpretation

	if oFsm.actualUniFlowParam.VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		logger.Debugw(ctx, "UniVlanConfigFsm: no VTFD config required", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	} else {
		//TODO!!!: it was not really intended to keep this enter* FSM method waiting on OMCI response (preventing other state transitions)
		// so it would be conceptually better to wait for the response in background like for the other multi-entity processing
		// but as the OMCI sequence must be ensured, a separate new state would be required - perhaps later
		// in practice should have no influence by now as no other state transition is currently accepted (while cancel() is ensured)
		if oFsm.numVlanFilterEntries == 0 {
			// This attribute uniquely identifies each instance of this managed entity. Through an identical ID,
			// this managed entity is implicitly linked to an instance of the MAC bridge port configuration data ME.
			vtfdID, _ := cmn.GenerateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo), uint16(oFsm.actualUniFlowParam.VlanRuleParams.TpID))
			//no VTFD yet created
			logger.Debugw(ctx, "UniVlanConfigFsm create VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(vtfdID), 16),
				"device-id": oFsm.deviceID,
				"macBpNo":   oFsm.pOnuUniPort.MacBpNo, "TpID": oFsm.actualUniFlowParam.VlanRuleParams.TpID})
			// 'SetVid' below is assumed to be masked already by the caller to 12 bit
			oFsm.vlanFilterList[0] = uint16(oFsm.actualUniFlowParam.VlanRuleParams.SetVid)

			vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization
			vtfdFilterList[0] = oFsm.vlanFilterList[0]
			oFsm.numVlanFilterEntries = 1
			meParams := me.ParamData{
				EntityID: vtfdID,
				Attributes: me.AttributeValueMap{
					"VlanFilterList":   vtfdFilterList,
					"ForwardOperation": uint8(0x10), //VID investigation
					"NumberOfEntries":  oFsm.numVlanFilterEntries,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.SendCreateVtfdVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
				oFsm.PAdaptFsm.CommChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				oFsm.mutexFlowParams.Unlock()
				logger.Errorw(ctx, "VTFD create failed, aborting UniVlanConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID})
				pConfigVlanStateAFsm := oFsm.PAdaptFsm
				if pConfigVlanStateAFsm != nil {
					go func(a_pAFsm *cmn.AdapterFsm) {
						_ = a_pAFsm.PFsm.Event(VlanEvReset)
					}(pConfigVlanStateAFsm)
				}
				return
			}
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  send shall return (dual format) error code that can be used here for immediate error treatment
			//  (relevant to all used sendXX() methods in this (and other) FSM's)
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()
		} else {
			// This attribute uniquely identifies each instance of this managed entity. Through an identical ID,
			// this managed entity is implicitly linked to an instance of the MAC bridge port configuration data ME.
			vtfdID, _ := cmn.GenerateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo), uint16(oFsm.actualUniFlowParam.VlanRuleParams.TpID))

			logger.Debugw(ctx, "UniVlanConfigFsm set VTFD", log.Fields{
				"EntitytId": strconv.FormatInt(int64(vtfdID), 16),
				"device-id": oFsm.deviceID,
				"macBpNo":   oFsm.pOnuUniPort.MacBpNo, "TpID": oFsm.actualUniFlowParam.VlanRuleParams.TpID})
			// setVid is assumed to be masked already by the caller to 12 bit
			oFsm.vlanFilterList[oFsm.numVlanFilterEntries] =
				uint16(oFsm.actualUniFlowParam.VlanRuleParams.SetVid)
			vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization

			// FIXME: VOL-3685: Issues with resetting a table entry in EVTOCD ME
			// VTFD has to be created afresh with a new entity ID that has the same entity ID as the MBPCD ME for every
			// new vlan associated with a different TP.
			vtfdFilterList[0] = uint16(oFsm.actualUniFlowParam.VlanRuleParams.SetVid)

			oFsm.numVlanFilterEntries++
			meParams := me.ParamData{
				EntityID: vtfdID,
				Attributes: me.AttributeValueMap{
					"VlanFilterList":   vtfdFilterList,
					"ForwardOperation": uint8(0x10), //VID investigation
					"NumberOfEntries":  oFsm.numVlanFilterEntries,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.SendCreateVtfdVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
				oFsm.PAdaptFsm.CommChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				oFsm.mutexFlowParams.Unlock()
				logger.Errorw(ctx, "UniVlanFsm create Vlan Tagging Filter ME result error",
					log.Fields{"device-id": oFsm.deviceID, "Error": err})
				_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				return
			}
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  send shall return (dual format) error code that can be used here for immediate error treatment
			//  (relevant to all used sendXX() methods in this (and other) FSM's)
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()
		}
		//verify response
		err := oFsm.waitforOmciResponse(ctx)
		if err != nil {
			oFsm.mutexFlowParams.Unlock()
			logger.Errorw(ctx, "VTFD create/set failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			pConfigVlanStateBaseFsm := oFsm.PAdaptFsm.PFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pBaseFsm *fsm.FSM) {
				_ = a_pBaseFsm.Event(VlanEvReset)
			}(pConfigVlanStateBaseFsm)
			return
		}
	}

	oFsm.requestEventOffset = uint8(cDeviceEventOffsetAddWithKvStore) //0 offset for last flow-add activity
	oFsm.mutexFlowParams.Unlock()
	go func() {
		oFsm.mutexFlowParams.RLock()
		tpID := oFsm.actualUniFlowParam.VlanRuleParams.TpID
		configuredUniFlow := oFsm.ConfiguredUniFlow
		// ensure mutexFlowParams not locked before calling some TPProcessing activity (that might already be pending on it)
		oFsm.mutexFlowParams.RUnlock()
		errEvto := oFsm.performConfigEvtocdEntries(ctx, configuredUniFlow)
		//This is correct passing scenario
		if errEvto == nil {
			//TODO Possibly insert new state for multicast --> possibly another jira/later time.
			for _, gemPort := range oFsm.pUniTechProf.getMulticastGemPorts(ctx, oFsm.pOnuUniPort.UniID, uint8(tpID)) {
				oFsm.mutexFlowParams.RLock()
				vlanID := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
				logger.Infow(ctx, "Setting multicast MEs for additional flows", log.Fields{"deviceID": oFsm.deviceID,
					"techProfile": tpID, "gemPort": gemPort,
					"vlanID": vlanID, "ConfiguredUniFlow": configuredUniFlow})
				oFsm.mutexFlowParams.RUnlock()
				errCreateAllMulticastME := oFsm.performSettingMulticastME(ctx, tpID, gemPort, vlanID)
				if errCreateAllMulticastME != nil {
					logger.Errorw(ctx, "Multicast ME create failed, aborting AniConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				}
			}
			//If this incremental flow contains a meter, then create TD for related gems.
			if oFsm.actualUniFlowParam.Meter != nil {
				for _, gemPort := range oFsm.pUniTechProf.getBidirectionalGemPortIDsForTP(ctx, oFsm.pOnuUniPort.UniID, tpID) {
					logger.Debugw(ctx, "Creating Traffic Descriptor for gem", log.Fields{"device-id": oFsm.deviceID, "meter": oFsm.actualUniFlowParam.Meter, "gem": gemPort})
					errCreateTrafficDescriptor := oFsm.createTrafficDescriptor(ctx, oFsm.actualUniFlowParam.Meter, tpID,
						oFsm.pOnuUniPort.UniID, gemPort)
					if errCreateTrafficDescriptor != nil {
						logger.Errorw(ctx, "Create Traffic Descriptor create failed, aborting Ani Config FSM!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					}
				}
			}
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvRxConfigEvtocd)
		}
	}()
}

func (oFsm *UniVlanConfigFsm) enterRemoveFlow(ctx context.Context, e *fsm.Event) {
	oFsm.mutexFlowParams.RLock()
	logger.Debugw(ctx, "UniVlanConfigFsm - start removing the top remove-flow", log.Fields{
		"with last cookie": oFsm.uniRemoveFlowsSlice[0].cookie,
		"device-id":        oFsm.deviceID})

	pConfigVlanStateBaseFsm := oFsm.PAdaptFsm.PFsm
	loAllowSpecificOmciConfig := oFsm.pDeviceHandler.IsReadyForOmciConfig()
	loVlanEntryClear := uint8(0)
	loVlanEntryRmPos := uint8(0x80) //with indication 'invalid' in bit 7
	//shallow copy is sufficient as no reference variables are used within struct
	loRuleParams := oFsm.uniRemoveFlowsSlice[0].vlanRuleParams
	oFsm.mutexFlowParams.RUnlock()
	logger.Debugw(ctx, "UniVlanConfigFsm - remove-flow parameters are", log.Fields{
		"match vid": loRuleParams.MatchVid, "match Pcp": loRuleParams.MatchPcp,
		"set vid":   strconv.FormatInt(int64(loRuleParams.SetVid), 16),
		"device-id": oFsm.deviceID})

	if loRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		// meaning transparent setup - no specific VTFD setting required
		logger.Debugw(ctx, "UniVlanConfigFsm: no VTFD removal required for transparent flow", log.Fields{
			"in state": e.FSM.Current(), "device-id": oFsm.deviceID})
	} else {
		vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization and 're-copy'
		if oFsm.numVlanFilterEntries == 1 {
			vtfdID, _ := cmn.GenerateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo), uint16(loRuleParams.TpID))
			//only one active VLAN entry (hopefully the SetVID we want to remove - should be, but not verified ..)
			//  so we can just delete the VTFD entry
			logger.Debugw(ctx, "UniVlanConfigFsm: VTFD delete (no more vlan filters)",
				log.Fields{"current vlan list": oFsm.vlanFilterList, "EntitytId": strconv.FormatInt(int64(vtfdID), 16),
					"device-id": oFsm.deviceID,
					"macBpNo":   oFsm.pOnuUniPort.MacBpNo, "TpID": loRuleParams.TpID})
			loVlanEntryClear = 1           //full VlanFilter clear request
			if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendDeleteVtfd(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
					oFsm.PAdaptFsm.CommChan, vtfdID)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "UniVlanFsm delete Vlan Tagging Filter ME result error",
						log.Fields{"device-id": oFsm.deviceID, "Error": err})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return
				}
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()
			} else {
				logger.Debugw(ctx, "UniVlanConfigFsm delete VTFD OMCI handling skipped based on device state", log.Fields{
					"device-id": oFsm.deviceID, "device-state": oFsm.pDeviceHandler.GetDeviceReasonString()})
			}
		} else {
			//many VTFD already should exists - find and remove the one concerned by the actual remove rule
			//  by updating the VTFD per set command with new valid list
			logger.Debugw(ctx, "UniVlanConfigFsm: VTFD removal of requested VLAN from the list on OMCI",
				log.Fields{"current vlan list": oFsm.vlanFilterList,
					"set-vlan": loRuleParams.SetVid, "device-id": oFsm.deviceID})
			for i := uint8(0); i < oFsm.numVlanFilterEntries; i++ {
				if loRuleParams.SetVid == uint32(oFsm.vlanFilterList[i]) {
					loVlanEntryRmPos = i
					break //abort search
				}
			}
			if loVlanEntryRmPos < cVtfdTableSize {
				vtfdID, _ := cmn.GenerateANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo), uint16(loRuleParams.TpID))
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
				logger.Debugw(ctx, "UniVlanConfigFsm set VTFD", log.Fields{
					"EntitytId":     strconv.FormatInt(int64(vtfdID), 16),
					"new vlan list": vtfdFilterList, "device-id": oFsm.deviceID,
					"macBpNo": oFsm.pOnuUniPort.MacBpNo, "TpID": loRuleParams.TpID})

				if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
					// FIXME: VOL-3685: Issues with resetting a table entry in EVTOCD ME
					oFsm.mutexPLastTxMeInstance.Lock()
					meInstance, err := oFsm.pOmciCC.SendDeleteVtfd(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), true,
						oFsm.PAdaptFsm.CommChan, vtfdID)
					if err != nil {
						oFsm.mutexPLastTxMeInstance.Unlock()
						logger.Errorw(ctx, "UniVlanFsm delete Vlan Tagging Filter ME result error",
							log.Fields{"device-id": oFsm.deviceID, "Error": err})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
						return
					}
					oFsm.pLastTxMeInstance = meInstance
					oFsm.mutexPLastTxMeInstance.Unlock()
				} else {
					logger.Debugw(ctx, "UniVlanConfigFsm set VTFD OMCI handling skipped based on device state", log.Fields{
						"device-id": oFsm.deviceID, "device-state": oFsm.pDeviceHandler.GetDeviceReasonString()})
				}
			} else {
				logger.Warnw(ctx, "UniVlanConfigFsm: requested VLAN for removal not found in list - ignore and continue (no VTFD set)",
					log.Fields{"device-id": oFsm.deviceID})
			}
		}
		if loVlanEntryClear > 0 {
			if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
				//waiting on response
				err := oFsm.waitforOmciResponse(ctx)
				if err != nil {
					logger.Errorw(ctx, "VTFD delete/reset failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					// Can't call FSM Event directly, decoupling it
					go func(a_pBaseFsm *fsm.FSM) {
						_ = a_pBaseFsm.Event(VlanEvReset)
					}(pConfigVlanStateBaseFsm)
					return
				}
			}

			oFsm.mutexFlowParams.Lock()
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
			oFsm.mutexFlowParams.Unlock()
		}
	}

	if loAllowSpecificOmciConfig { //specific OMCI config is expected to work acc. to the device state
		go oFsm.removeEvtocdEntries(ctx, loRuleParams)
	} else {
		// OMCI processing is not done, expectation is to have the ONU in some basic config state accordingly
		logger.Debugw(ctx, "UniVlanConfigFsm remove EVTOCD OMCI handling skipped based on device state", log.Fields{
			"device-id": oFsm.deviceID})
		// Can't call FSM Event directly, decoupling it
		go func(a_pBaseFsm *fsm.FSM) {
			_ = a_pBaseFsm.Event(VlanEvRemFlowDone, loRuleParams.TpID)
		}(pConfigVlanStateBaseFsm)
	}
}

func (oFsm *UniVlanConfigFsm) enterVlanCleanupDone(ctx context.Context, e *fsm.Event) {
	var tpID uint8
	// Extract the tpID
	if len(e.Args) > 0 {
		tpID = e.Args[0].(uint8)
		logger.Debugw(ctx, "UniVlanConfigFsm - flow removed for tp id", log.Fields{"device-id": oFsm.deviceID, "tpID": e.Args[0].(uint8)})
	} else {
		logger.Warnw(ctx, "UniVlanConfigFsm - tp id not available", log.Fields{"device-id": oFsm.deviceID})
	}
	oFsm.mutexFlowParams.Lock()
	deletedCookie := oFsm.uniRemoveFlowsSlice[0].cookie

	pConfigVlanStateAFsm := oFsm.PAdaptFsm
	if pConfigVlanStateAFsm == nil {
		logger.Errorw(ctx, "invalid Fsm pointer - unresolvable - abort",
			log.Fields{"device-id": oFsm.deviceID})
		//would have to be fixed from outside somehow
		return
	}

	// here we need o finally remove the removed data also from uniVlanFlowParamsSlice and possibly have to
	//  stop the suspension of a add-activity waiting for the end of removal
	//call from 'configured' state of the rule
	if err := oFsm.removeFlowFromParamsSlice(ctx, deletedCookie, true); err != nil {
		//something quite inconsistent detected, perhaps just try to recover with FSM reset
		oFsm.mutexFlowParams.Unlock()
		logger.Errorw(ctx, "UniVlanConfigFsm - could not clear database - abort", log.Fields{"device-id": oFsm.deviceID})
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(VlanEvReset)
		}(pConfigVlanStateAFsm)
		return
	}
	if oFsm.uniRemoveFlowsSlice[0].isSuspendedOnAdd {
		removeChannel := oFsm.uniRemoveFlowsSlice[0].removeChannel
		oFsm.mutexFlowParams.Unlock()
		removeChannel <- true
		oFsm.mutexFlowParams.Lock()
	}

	logger.Debugw(ctx, "UniVlanConfigFsm - removing the removal data", log.Fields{
		"in state": e.FSM.Current(), "device-id": oFsm.deviceID,
		"removed cookie": deletedCookie, "waitForDeleteCookie": oFsm.delayNewRuleCookie})

	// Store the reference to the flow response channel before this entry in the slice is deleted
	flowRespChan := oFsm.uniRemoveFlowsSlice[0].respChan

	if len(oFsm.uniRemoveFlowsSlice) <= 1 {
		oFsm.uniRemoveFlowsSlice = nil //reset the slice
		logger.Debugw(ctx, "UniVlanConfigFsm flow removal - last remove-flow deleted", log.Fields{
			"device-id": oFsm.deviceID})
	} else {
		//cut off the actual flow by slicing out the first element
		oFsm.uniRemoveFlowsSlice = append(
			oFsm.uniRemoveFlowsSlice[:0],
			oFsm.uniRemoveFlowsSlice[1:]...)
		logger.Debugw(ctx, "UniVlanConfigFsm flow removal - specific flow deleted from data", log.Fields{
			"device-id": oFsm.deviceID})
	}
	oFsm.mutexFlowParams.Unlock()

	oFsm.requestEventOffset = uint8(cDeviceEventOffsetRemoveWithKvStore) //offset for last flow-remove activity (with kvStore request)
	//return to the basic config verification state
	// Can't call FSM Event directly, decoupling it
	go func(a_pAFsm *cmn.AdapterFsm) {
		_ = a_pAFsm.PFsm.Event(VlanEvFlowDataRemoved)
	}(pConfigVlanStateAFsm)

	oFsm.mutexFlowParams.Lock()
	noOfFlowRem := len(oFsm.uniRemoveFlowsSlice)
	if deletedCookie == oFsm.delayNewRuleCookie {
		// flush the channel CookieDeleted to ensure it is not lingering from some previous (aborted) activity
		select {
		case <-oFsm.chCookieDeleted:
			logger.Debug(ctx, "flushed CookieDeleted")
		default:
		}
		oFsm.chCookieDeleted <- true // let the waiting AddFlow thread continue
	}
	// If all pending flow-removes are completed and TP ID is valid go on processing any pending TP delete
	if oFsm.signalOnFlowDelete && noOfFlowRem == 0 && tpID > 0 {
		logger.Debugw(ctx, "signal flow removal for pending TP delete", log.Fields{"device-id": oFsm.deviceID, "tpID": tpID})
		// If we are here then all flows are removed.
		if len(oFsm.flowDeleteChannel) == 0 { //channel not yet in use
			oFsm.flowDeleteChannel <- true
			oFsm.signalOnFlowDelete = false
		}
	}
	oFsm.mutexFlowParams.Unlock()

	// send response on the response channel for the removed flow.
	oFsm.pushReponseOnFlowResponseChannel(ctx, flowRespChan, nil)
}

func (oFsm *UniVlanConfigFsm) enterResetting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniVlanConfigFsm resetting", log.Fields{"device-id": oFsm.deviceID})

	oFsm.mutexPLastTxMeInstance.Lock()
	oFsm.pLastTxMeInstance = nil //to avoid misinterpretation in case of some lingering frame reception processing
	oFsm.mutexPLastTxMeInstance.Unlock()

	pConfigVlanStateAFsm := oFsm.PAdaptFsm
	if pConfigVlanStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := cmn.Message{
			Type: cmn.TestMsg,
			Data: cmn.TestMessage{
				TestMessageVal: cmn.AbortMessageProcessing,
			},
		}
		pConfigVlanStateAFsm.CommChan <- fsmAbortMsg

		//internal data is not explicitly removed, this is left to garbage collection after complete FSM removal
		//  but some channels have to be cleared to avoid unintended waiting for events, that have no meaning anymore now

		oFsm.mutexFlowParams.RLock()
		if oFsm.delayNewRuleCookie != 0 {
			// looks like the waiting AddFlow is stuck
			oFsm.mutexFlowParams.RUnlock()
			//use asynchronous channel sending to avoid stucking on non-waiting receiver
			select {
			case oFsm.chCookieDeleted <- false: // let the waiting AddFlow thread terminate
			default:
			}
			oFsm.mutexFlowParams.RLock()
		}
		if len(oFsm.uniRemoveFlowsSlice) > 0 {
			for _, removeUniFlowParams := range oFsm.uniRemoveFlowsSlice {
				if removeUniFlowParams.isSuspendedOnAdd {
					removeChannel := removeUniFlowParams.removeChannel
					logger.Debugw(ctx, "UniVlanConfigFsm flow clear-up - abort suspended rule-add", log.Fields{
						"device-id": oFsm.deviceID, "cookie": removeUniFlowParams.cookie})
					oFsm.mutexFlowParams.RUnlock()
					//use asynchronous channel sending to avoid stucking on non-waiting receiver
					select {
					case removeChannel <- false:
					default:
					}
					oFsm.mutexFlowParams.RLock()
				}
				// Send response on response channel if the caller is waiting on it.
				var err error = nil
				if !oFsm.isCanceled {
					//only if the FSM is not canceled on external request use some error indication for the respChan
					//  so only at real internal FSM abortion some error code is sent back
					//  on the deleteFlow with the hope the system may handle such error situation (possibly retrying)
					err = fmt.Errorf("internal-error")
				}
				//if the FSM was cancelled on external request the assumption is, that all processing has to be stopped
				//  assumed in connection with some ONU down/removal indication in which case all flows can be considered as removed
				oFsm.pushReponseOnFlowResponseChannel(ctx, removeUniFlowParams.respChan, err)
			}
		}

		if oFsm.pDeviceHandler != nil {
			if len(oFsm.uniVlanFlowParamsSlice) > 0 {
				if !oFsm.isCanceled {
					//if the FSM is not canceled on external request use "internal-error" for the respChan
					for _, vlanRule := range oFsm.uniVlanFlowParamsSlice {
						// Send response on response channel if the caller is waiting on it with according error indication.
						oFsm.pushReponseOnFlowResponseChannel(ctx, vlanRule.RespChan, fmt.Errorf("internal-error"))
					}
					//permanently remove possibly stored persistent data
					var emptySlice = make([]cmn.UniVlanFlowParams, 0)
					_ = oFsm.pDeviceHandler.StorePersUniFlowConfig(ctx, oFsm.pOnuUniPort.UniID, &emptySlice, true) //ignore errors
				} else {
					// reset (cancel) of all Fsm is always accompanied by global persistency data removal
					//  no need to remove specific data in this case here
					//  TODO: cancelation may also abort a running flowAdd activity in which case it would be better
					//     to also resopnd on the respChan with some error ("config canceled"), but that is a bit hard to decide here
					//     so just left open by now
					logger.Debugw(ctx, "UniVlanConfigFsm persistency data not cleared", log.Fields{"device-id": oFsm.deviceID})
				}
			}
			oFsm.mutexFlowParams.RUnlock()

			//try to let the FSM proceed to 'disabled'
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *cmn.AdapterFsm) {
				if a_pAFsm != nil && a_pAFsm.PFsm != nil {
					_ = a_pAFsm.PFsm.Event(VlanEvRestart)
				}
			}(pConfigVlanStateAFsm)
			return
		}
		oFsm.mutexFlowParams.RUnlock()
		logger.Warnw(ctx, "UniVlanConfigFsm - device handler already vanished",
			log.Fields{"device-id": oFsm.deviceID})
		return
	}
	logger.Warnw(ctx, "UniVlanConfigFsm - FSM pointer already vanished",
		log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) enterDisabled(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "UniVlanConfigFsm enters disabled state", log.Fields{"device-id": oFsm.deviceID})

	if oFsm.pDeviceHandler != nil {
		//request removal of 'reference' in the Handler (completely clear the FSM and its data)
		go oFsm.pDeviceHandler.RemoveVlanFilterFsm(ctx, oFsm.pOnuUniPort)
		return
	}
	logger.Warnw(ctx, "UniVlanConfigFsm - device handler already vanished",
		log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) processOmciVlanMessages(ctx context.Context) { //ctx context.Context?
	logger.Debugw(ctx, "Start UniVlanConfigFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info(ctx,"MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.PAdaptFsm.CommChan
		if !ok {
			logger.Info(ctx, "UniVlanConfigFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			break loop
		}
		logger.Debugw(ctx, "UniVlanConfigFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case cmn.TestMsg:
			msg, _ := message.Data.(cmn.TestMessage)
			if msg.TestMessageVal == cmn.AbortMessageProcessing {
				logger.Infow(ctx, "UniVlanConfigFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "UniVlanConfigFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case cmn.OMCI:
			msg, _ := message.Data.(cmn.OmciMessage)
			oFsm.handleOmciVlanConfigMessage(ctx, msg)
		default:
			logger.Warn(ctx, "UniVlanConfigFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Infow(ctx, "End UniVlanConfigFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

func (oFsm *UniVlanConfigFsm) handleOmciVlanConfigMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI UniVlanConfigFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.CreateResponseType:
		{ // had to shift that to a method to cope with StaticCodeAnalysis restrictions :-(
			if err := oFsm.handleOmciCreateResponseMessage(ctx, msg.OmciPacket); err != nil {
				logger.Warnw(ctx, "CreateResponse handling aborted", log.Fields{"err": err})
				return
			}
		} //CreateResponseType
	case omci.SetResponseType:
		{ //leave that here as direct code as most often used
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeSetResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for SetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.SetResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for SetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw(ctx, "UniVlanConfigFsm SetResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "UniVlanConfigFsm Omci SetResponse Error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			oFsm.mutexPLastTxMeInstance.RLock()
			if oFsm.pLastTxMeInstance != nil {
				if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
					msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
					switch oFsm.pLastTxMeInstance.GetName() {
					case "VlanTaggingFilterData", "ExtendedVlanTaggingOperationConfigurationData", "MulticastOperationsProfile", "GemPortNetworkCtp":
						{ // let the MultiEntity config proceed by stopping the wait function
							oFsm.mutexPLastTxMeInstance.RUnlock()
							oFsm.omciMIdsResponseReceived <- true
							return
						}
					default:
						{
							logger.Warnw(ctx, "Unsupported ME name received!",
								log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
						}
					}
				}
			} else {
				logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
			}
			oFsm.mutexPLastTxMeInstance.RUnlock()
		} //SetResponseType
	case omci.DeleteResponseType:
		{ // had to shift that to a method to cope with StaticCodeAnalysis restrictions :-(
			if err := oFsm.handleOmciDeleteResponseMessage(ctx, msg.OmciPacket); err != nil {
				logger.Warnw(ctx, "DeleteResponse handling aborted", log.Fields{"err": err})
				return
			}
		} //DeleteResponseType
	default:
		{
			logger.Errorw(ctx, "Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

func (oFsm *UniVlanConfigFsm) handleOmciCreateResponseMessage(ctx context.Context, apOmciPacket *gp.Packet) error {
	msgLayer := (*apOmciPacket).Layer(omci.LayerTypeCreateResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be detected for CreateResponse for device-id %x",
			oFsm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.CreateResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned for CreateResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be assigned for CreateResponse for device-id %x",
			oFsm.deviceID)
	}
	logger.Debugw(ctx, "UniVlanConfigFsm CreateResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success && msgObj.Result != me.InstanceExists {
		logger.Errorw(ctx, "Omci CreateResponse Error - later: drive FSM to abort state ?", log.Fields{"device-id": oFsm.deviceID,
			"Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return fmt.Errorf("omci CreateResponse Error for device-id %x",
			oFsm.deviceID)
	}
	oFsm.mutexPLastTxMeInstance.RLock()
	if oFsm.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			// to satisfy StaticCodeAnalysis I had to move the small processing into a separate method :-(
			switch oFsm.pLastTxMeInstance.GetName() {
			case "VlanTaggingFilterData", "MulticastOperationsProfile",
				"MulticastSubscriberConfigInfo", "MacBridgePortConfigurationData",
				"ExtendedVlanTaggingOperationConfigurationData", "TrafficDescriptor":
				{
					oFsm.mutexPLastTxMeInstance.RUnlock()
					if oFsm.PAdaptFsm.PFsm.Current() == VlanStConfigVtfd {
						// Only if CreateResponse is received from first flow entry - let the FSM proceed ...
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvRxConfigVtfd)
					} else { // let the MultiEntity config proceed by stopping the wait function
						oFsm.omciMIdsResponseReceived <- true
					}
					return nil
				}
			default:
				{
					logger.Warnw(ctx, "Unsupported ME name received!",
						log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
				}
			}
		}
	} else {
		logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
	}
	oFsm.mutexPLastTxMeInstance.RUnlock()
	return nil
}

func (oFsm *UniVlanConfigFsm) handleOmciDeleteResponseMessage(ctx context.Context, apOmciPacket *gp.Packet) error {
	msgLayer := (*apOmciPacket).Layer(omci.LayerTypeDeleteResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "UniVlanConfigFsm - Omci Msg layer could not be detected for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be detected for DeleteResponse for device-id %x",
			oFsm.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.DeleteResponse)
	if !msgOk {
		logger.Errorw(ctx, "UniVlanConfigFsm - Omci Msg layer could not be assigned for DeleteResponse",
			log.Fields{"device-id": oFsm.deviceID})
		return fmt.Errorf("omci msg layer could not be assigned for DeleteResponse for device-id %x",
			oFsm.deviceID)
	}
	logger.Debugw(ctx, "UniVlanConfigFsm DeleteResponse Data", log.Fields{"device-id": oFsm.deviceID, "data-fields": msgObj})
	if msgObj.Result != me.Success {
		logger.Errorw(ctx, "UniVlanConfigFsm - Omci DeleteResponse Error - later: drive FSM to abort state ?",
			log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
		// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
		return fmt.Errorf("omci DeleteResponse Error for device-id %x",
			oFsm.deviceID)
	}
	oFsm.mutexPLastTxMeInstance.RLock()
	if oFsm.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
			switch oFsm.pLastTxMeInstance.GetName() {
			case "VlanTaggingFilterData", "ExtendedVlanTaggingOperationConfigurationData", "TrafficDescriptor":
				{ // let the MultiEntity config proceed by stopping the wait function
					oFsm.mutexPLastTxMeInstance.RUnlock()
					oFsm.omciMIdsResponseReceived <- true
					return nil
				}
			default:
				{
					logger.Warnw(ctx, "Unsupported ME name received!",
						log.Fields{"ME name": oFsm.pLastTxMeInstance.GetName(), "device-id": oFsm.deviceID})
				}
			}
		}
	} else {
		logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oFsm.deviceID})
	}
	oFsm.mutexPLastTxMeInstance.RUnlock()
	return nil
}

func (oFsm *UniVlanConfigFsm) performConfigEvtocdEntries(ctx context.Context, aFlowEntryNo uint8) error {
	oFsm.mutexFlowParams.RLock()
	evtocdID := oFsm.evtocdID
	oFsm.mutexFlowParams.RUnlock()

	if aFlowEntryNo == 0 {
		// EthType set only at first flow element
		// EVTOCD ME is expected to exist at this point already from MIB-Download (with AssociationType/Pointer)
		// we need to extend the configuration by EthType definition and, to be sure, downstream 'inverse' mode
		logger.Debugw(ctx, "UniVlanConfigFsm Tx Create::EVTOCD", log.Fields{
			"EntitytId":  strconv.FormatInt(int64(evtocdID), 16),
			"i/oEthType": strconv.FormatInt(int64(cDefaultTpid), 16),
			"device-id":  oFsm.deviceID})
		associationType := 2 // default to UniPPTP
		if oFsm.pOnuUniPort.PortType == cmn.UniVEIP {
			associationType = 10
		}
		// Create the EVTOCD ME
		meParams := me.ParamData{
			EntityID: evtocdID,
			Attributes: me.AttributeValueMap{
				"AssociationType":     uint8(associationType),
				"AssociatedMePointer": oFsm.pOnuUniPort.EntityID,
			},
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.SendCreateEvtocdVar(context.TODO(), oFsm.pDeviceHandler.GetOmciTimeout(),
			true, oFsm.PAdaptFsm.CommChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "CreateEvtocdVar create failed, aborting UniVlanConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd instance create failed %s, error %s", oFsm.deviceID, err)
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()

		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "Evtocd create failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd create failed %s, error %s", oFsm.deviceID, err)
		}

		// Set the EVTOCD ME default params
		meParams = me.ParamData{
			EntityID: evtocdID,
			Attributes: me.AttributeValueMap{
				"InputTpid":      uint16(cDefaultTpid), //could be possibly retrieved from flow config one day, by now just like py-code base
				"OutputTpid":     uint16(cDefaultTpid), //could be possibly retrieved from flow config one day, by now just like py-code base
				"DownstreamMode": uint8(cDefaultDownstreamMode),
			},
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err = oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
			oFsm.pDeviceHandler.GetOmciTimeout(), true,
			oFsm.PAdaptFsm.CommChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd instance set failed %s, error %s", oFsm.deviceID, err)
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()

		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "Evtocd set TPID failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd set TPID failed %s, error %s", oFsm.deviceID, err)
		}
	} //first flow element

	oFsm.mutexFlowParams.RLock()
	if oFsm.actualUniFlowParam.VlanRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission required
		oFsm.mutexFlowParams.RUnlock()
		logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD single tagged transparent rule", log.Fields{
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
			EntityID: evtocdID,
			Attributes: me.AttributeValueMap{
				"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
			},
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
			oFsm.pDeviceHandler.GetOmciTimeout(), true,
			oFsm.PAdaptFsm.CommChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd instance set failed %s, error %s", oFsm.deviceID, err)
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()

		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "Evtocd set transparent singletagged rule failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return fmt.Errorf("evtocd set transparent singletagged rule failed %s, error %s", oFsm.deviceID, err)

		}
	} else {
		// according to py-code acceptIncrementalEvto program option decides upon stacking or translation scenario
		if oFsm.acceptIncrementalEvtoOption {
			matchPcp := oFsm.actualUniFlowParam.VlanRuleParams.MatchPcp
			matchVid := oFsm.actualUniFlowParam.VlanRuleParams.MatchVid
			setPcp := oFsm.actualUniFlowParam.VlanRuleParams.SetPcp
			setVid := oFsm.actualUniFlowParam.VlanRuleParams.SetVid
			// this defines VID translation scenario: singletagged->singletagged (if not transparent)
			logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD single tagged translation rule", log.Fields{
				"match-pcp": matchPcp, "match-vid": matchVid, "set-pcp": setPcp, "set-vid:": setVid, "device-id": oFsm.deviceID})
			sliceEvtocdRule := make([]uint8, 16)
			// fill vlan tagging operation table bit fields using network=bigEndian order and using slice offset 0 as highest 'word'
			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterOuterOffset:],
				cPrioIgnoreTag<<cFilterPrioOffset| // Not an outer-tag rule
					cDoNotFilterVid<<cFilterVidOffset| // Do not filter on outer vid
					cDoNotFilterTPID<<cFilterTpidOffset) // Do not filter on outer TPID field

			binary.BigEndian.PutUint32(sliceEvtocdRule[cFilterInnerOffset:],
				oFsm.actualUniFlowParam.VlanRuleParams.MatchPcp<<cFilterPrioOffset| // either DNFonPrio or ignore tag (default) on innerVLAN
					oFsm.actualUniFlowParam.VlanRuleParams.MatchVid<<cFilterVidOffset| // either DNFonVid or real filter VID
					cDoNotFilterTPID<<cFilterTpidOffset| // Do not filter on inner TPID field
					cDoNotFilterEtherType<<cFilterEtherTypeOffset) // Do not filter of EtherType

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatOuterOffset:],
				oFsm.actualUniFlowParam.VlanRuleParams.TagsToRemove<<cTreatTTROffset| // either 1 or 0
					cDoNotAddPrio<<cTreatPrioOffset| // do not add outer tag
					cDontCareVid<<cTreatVidOffset| // Outer VID don't care
					cDontCareTpid<<cTreatTpidOffset) // Outer TPID field don't care

			binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
				oFsm.actualUniFlowParam.VlanRuleParams.SetPcp<<cTreatPrioOffset| // as configured in flow
					oFsm.actualUniFlowParam.VlanRuleParams.SetVid<<cTreatVidOffset| //as configured in flow
					cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100
			oFsm.mutexFlowParams.RUnlock()

			meParams := me.ParamData{
				EntityID: evtocdID,
				Attributes: me.AttributeValueMap{
					"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
				oFsm.pDeviceHandler.GetOmciTimeout(), true,
				oFsm.PAdaptFsm.CommChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				return fmt.Errorf("evtocd instance set failed %s, error %s", oFsm.deviceID, err)
			}
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()

			//verify response
			err = oFsm.waitforOmciResponse(ctx)
			if err != nil {
				logger.Errorw(ctx, "Evtocd set singletagged translation rule failed, aborting VlanConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				return fmt.Errorf("evtocd set singletagged translation rule failed %s, error %s", oFsm.deviceID, err)
			}
		} else {
			//not transparent and not acceptIncrementalEvtoOption untagged/priotagged->singletagged
			{ // just for local var's
				// this defines stacking scenario: untagged->singletagged
				logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD untagged->singletagged rule", log.Fields{
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
						oFsm.actualUniFlowParam.VlanRuleParams.SetVid<<cTreatVidOffset| // Outer VID don't care
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100

				oFsm.mutexFlowParams.RUnlock()
				meParams := me.ParamData{
					EntityID: evtocdID,
					Attributes: me.AttributeValueMap{
						"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
					},
				}
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
					oFsm.pDeviceHandler.GetOmciTimeout(), true,
					oFsm.PAdaptFsm.CommChan, meParams)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return fmt.Errorf("evtocd instance set failed %s, error %s", oFsm.deviceID, err)
				}
				//accept also nil as (error) return value for writing to LastTx
				//  - this avoids misinterpretation of new received OMCI messages
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()

				//verify response
				err = oFsm.waitforOmciResponse(ctx)
				if err != nil {
					logger.Errorw(ctx, "Evtocd set untagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return fmt.Errorf("evtocd set untagged->singletagged rule failed %s, error %s", oFsm.deviceID, err)

				}
			} // just for local var's
			{ // just for local var's
				// this defines 'stacking' scenario: priotagged->singletagged
				logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD priotagged->singletagged rule", log.Fields{
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

				oFsm.mutexFlowParams.RLock()
				binary.BigEndian.PutUint32(sliceEvtocdRule[cTreatInnerOffset:],
					cCopyPrioFromInner<<cTreatPrioOffset| // vlan copy from PrioTag
						//   (as done in Py code, maybe better option would be setPcp here, which still could be PrioCopy?)
						oFsm.actualUniFlowParam.VlanRuleParams.SetVid<<cTreatVidOffset| // Outer VID as configured
						cSetOutputTpidCopyDei<<cTreatTpidOffset) // Set TPID = 0x8100
				oFsm.mutexFlowParams.RUnlock()

				meParams := me.ParamData{
					EntityID: evtocdID,
					Attributes: me.AttributeValueMap{
						"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
					},
				}
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
					oFsm.pDeviceHandler.GetOmciTimeout(), true,
					oFsm.PAdaptFsm.CommChan, meParams)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return fmt.Errorf("evtocd instance set failed %s, error %s", oFsm.deviceID, err)
				}
				//accept also nil as (error) return value for writing to LastTx
				//  - this avoids misinterpretation of new received OMCI messages
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()

				//verify response
				err = oFsm.waitforOmciResponse(ctx)
				if err != nil {
					logger.Errorw(ctx, "Evtocd set priotagged->singletagged rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return fmt.Errorf("evtocd set priotagged->singletagged rule failed %s, error %s", oFsm.deviceID, err)

				}
			} //just for local var's
		}
	}

	// if Config has been done for all EVTOCD entries let the FSM proceed
	logger.Debugw(ctx, "EVTOCD set loop finished", log.Fields{"device-id": oFsm.deviceID})
	oFsm.mutexFlowParams.Lock()
	oFsm.ConfiguredUniFlow++ // one (more) flow configured
	oFsm.mutexFlowParams.Unlock()
	return nil
}

func (oFsm *UniVlanConfigFsm) removeEvtocdEntries(ctx context.Context, aRuleParams cmn.UniVlanRuleParams) {
	oFsm.mutexFlowParams.RLock()
	evtocdID := oFsm.evtocdID
	oFsm.mutexFlowParams.RUnlock()

	// configured Input/Output TPID is not modified again - no influence if no filter is applied
	if aRuleParams.SetVid == uint32(of.OfpVlanId_OFPVID_PRESENT) {
		//transparent transmission was set
		//perhaps the config is not needed for removal,
		//  but the specific InnerTpid setting is removed in favor of the real default forwarding rule
		logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD reset to default single tagged rule", log.Fields{
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
			EntityID: evtocdID,
			Attributes: me.AttributeValueMap{
				"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
			},
		}
		oFsm.mutexPLastTxMeInstance.Lock()
		meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
			oFsm.pDeviceHandler.GetOmciTimeout(), true,
			oFsm.PAdaptFsm.CommChan, meParams)
		if err != nil {
			oFsm.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return
		}
		//accept also nil as (error) return value for writing to LastTx
		//  - this avoids misinterpretation of new received OMCI messages
		oFsm.pLastTxMeInstance = meInstance
		oFsm.mutexPLastTxMeInstance.Unlock()

		//verify response
		err = oFsm.waitforOmciResponse(ctx)
		if err != nil {
			logger.Errorw(ctx, "Evtocd reset singletagged rule failed, aborting VlanConfig FSM!",
				log.Fields{"device-id": oFsm.deviceID})
			_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
			return
		}
	} else {
		// according to py-code acceptIncrementalEvto program option decides upon stacking or translation scenario
		oFsm.mutexFlowParams.RLock()
		if oFsm.acceptIncrementalEvtoOption {
			oFsm.mutexFlowParams.RUnlock()
			// this defines VID translation scenario: singletagged->singletagged (if not transparent)
			logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD clear single tagged translation rule", log.Fields{
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
				EntityID: evtocdID,
				Attributes: me.AttributeValueMap{
					"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
				},
			}
			oFsm.mutexPLastTxMeInstance.Lock()
			meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
				oFsm.pDeviceHandler.GetOmciTimeout(), true,
				oFsm.PAdaptFsm.CommChan, meParams)
			if err != nil {
				oFsm.mutexPLastTxMeInstance.Unlock()
				logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
					log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				return
			}
			//accept also nil as (error) return value for writing to LastTx
			//  - this avoids misinterpretation of new received OMCI messages
			oFsm.pLastTxMeInstance = meInstance
			oFsm.mutexPLastTxMeInstance.Unlock()

			//verify response
			err = oFsm.waitforOmciResponse(ctx)
			if err != nil {
				logger.Errorw(ctx, "Evtocd clear singletagged translation rule failed, aborting VlanConfig FSM!",
					log.Fields{"device-id": oFsm.deviceID, "match-vlan": aRuleParams.MatchVid})
				_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
				return
			}
		} else {
			// VOL-3685
			// NOTE: With ALPHA ONUs it was seen that just resetting a particular entry in the EVTOCD table
			// and re-configuring a new entry would not work. The old entry is removed and new entry is created
			// indeed, but the traffic landing upstream would carry old vlan sometimes.
			// This is only a WORKAROUND which basically deletes the entire EVTOCD ME and re-creates it again
			// later when the flow is being re-installed.
			// Of course this is applicable to case only where single service (or single tcont) is in use and
			// there is only one service vlan (oFsm.acceptIncrementalEvtoOption is false in this case).
			// Interstingly this problem has not been observed in multi-tcont (or multi-service) scenario (in
			// which case the oFsm.acceptIncrementalEvtoOption is set to true).
			if oFsm.ConfiguredUniFlow == 1 && !oFsm.acceptIncrementalEvtoOption {
				oFsm.mutexFlowParams.RUnlock()
				logger.Debugw(ctx, "UniVlanConfigFsm Tx Remove::EVTOCD", log.Fields{"device-id": oFsm.deviceID})
				// When there are no more EVTOCD vlan configurations on the ONU and acceptIncrementalEvtoOption
				// is not enabled, delete the EVTOCD ME.
				meParams := me.ParamData{
					EntityID: evtocdID,
				}
				oFsm.mutexPLastTxMeInstance.Lock()
				meInstance, err := oFsm.pOmciCC.SendDeleteEvtocd(log.WithSpanFromContext(context.TODO(), ctx),
					oFsm.pDeviceHandler.GetOmciTimeout(), true,
					oFsm.PAdaptFsm.CommChan, meParams)
				if err != nil {
					oFsm.mutexPLastTxMeInstance.Unlock()
					logger.Errorw(ctx, "DeleteEvtocdVar delete failed, aborting UniVlanConfigFsm!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return
				}
				//accept also nil as (error) return value for writing to LastTx
				//  - this avoids misinterpretation of new received OMCI messages
				oFsm.pLastTxMeInstance = meInstance
				oFsm.mutexPLastTxMeInstance.Unlock()

				//verify response
				err = oFsm.waitforOmciResponse(ctx)
				if err != nil {
					logger.Errorw(ctx, "Evtocd delete rule failed, aborting VlanConfig FSM!",
						log.Fields{"device-id": oFsm.deviceID})
					_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
					return
				}
			} else {
				// NOTE : We should ideally never ether this section when oFsm.acceptIncrementalEvtoOption is set to false
				// This is true for only ATT/DT workflow
				logger.Debugw(ctx, "UniVlanConfigFsm: Remove EVTOCD set operation",
					log.Fields{"configured-flow": oFsm.ConfiguredUniFlow, "incremental-evto": oFsm.acceptIncrementalEvtoOption})
				oFsm.mutexFlowParams.RUnlock()
				//not transparent and not acceptIncrementalEvtoOption: untagged/priotagged->singletagged
				{ // just for local var's
					// this defines stacking scenario: untagged->singletagged
					//TODO!! in theory there could be different rules running in setting different PCP/VID'S
					//  for untagged/priotagged, last rule wins (and remains the only one), maybe that should be
					//  checked already at flow-add (and rejected) - to be observed if such is possible in Voltha
					//  delete now assumes there is only one such rule!
					logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD reset untagged rule to default", log.Fields{
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
						EntityID: evtocdID,
						Attributes: me.AttributeValueMap{
							"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
						},
					}
					oFsm.mutexPLastTxMeInstance.Lock()
					meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(context.TODO(),
						oFsm.pDeviceHandler.GetOmciTimeout(), true,
						oFsm.PAdaptFsm.CommChan, meParams)
					if err != nil {
						oFsm.mutexPLastTxMeInstance.Unlock()
						logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
						return
					}
					//accept also nil as (error) return value for writing to LastTx
					//  - this avoids misinterpretation of new received OMCI messages
					oFsm.pLastTxMeInstance = meInstance
					oFsm.mutexPLastTxMeInstance.Unlock()

					//verify response
					err = oFsm.waitforOmciResponse(ctx)
					if err != nil {
						logger.Errorw(ctx, "Evtocd reset untagged rule to default failed, aborting VlanConfig FSM!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
						return
					}
				} // just for local var's
				{ // just for local var's
					// this defines 'stacking' scenario: priotagged->singletagged
					logger.Debugw(ctx, "UniVlanConfigFsm Tx Set::EVTOCD delete priotagged rule", log.Fields{
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
						EntityID: evtocdID,
						Attributes: me.AttributeValueMap{
							"ReceivedFrameVlanTaggingOperationTable": sliceEvtocdRule,
						},
					}
					oFsm.mutexPLastTxMeInstance.Lock()
					meInstance, err := oFsm.pOmciCC.SendSetEvtocdVar(log.WithSpanFromContext(context.TODO(), ctx),
						oFsm.pDeviceHandler.GetOmciTimeout(), true,
						oFsm.PAdaptFsm.CommChan, meParams)
					if err != nil {
						oFsm.mutexPLastTxMeInstance.Unlock()
						logger.Errorw(ctx, "SetEvtocdVar set failed, aborting UniVlanConfigFsm!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
						return
					}
					//accept also nil as (error) return value for writing to LastTx
					//  - this avoids misinterpretation of new received OMCI messages
					oFsm.pLastTxMeInstance = meInstance
					oFsm.mutexPLastTxMeInstance.Unlock()

					//verify response
					err = oFsm.waitforOmciResponse(ctx)
					if err != nil {
						logger.Errorw(ctx, "Evtocd delete priotagged rule failed, aborting VlanConfig FSM!",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
						return
					}
				}
			} //just for local var's
		}
	}
	// if Config has been done for all EVTOCD entries let the FSM proceed
	logger.Debugw(ctx, "EVTOCD filter remove loop finished", log.Fields{"device-id": oFsm.deviceID})
	_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvRemFlowDone, aRuleParams.TpID)
}

func (oFsm *UniVlanConfigFsm) waitforOmciResponse(ctx context.Context) error {
	oFsm.mutexIsAwaitingResponse.Lock()
	if oFsm.isCanceled {
		// FSM already canceled before entering wait
		logger.Debugw(ctx, "UniVlanConfigFsm wait-for-multi-entity-response aborted (on enter)", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf(cmn.CErrWaitAborted)
	}
	oFsm.isAwaitingResponse = true
	oFsm.mutexIsAwaitingResponse.Unlock()
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow(ctx,"LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(oFsm.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //AS FOR THE OTHER OMCI FSM's
		logger.Warnw(ctx, "UniVlanConfigFsm multi entity timeout", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isAwaitingResponse = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf("uniVlanConfigFsm multi entity timeout %s", oFsm.deviceID)
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debugw(ctx, "UniVlanConfigFsm multi entity response received", log.Fields{"for device-id": oFsm.deviceID})
			oFsm.mutexIsAwaitingResponse.Lock()
			oFsm.isAwaitingResponse = false
			oFsm.mutexIsAwaitingResponse.Unlock()
			return nil
		}
		// waiting was aborted (probably on external request)
		logger.Debugw(ctx, "UniVlanConfigFsm wait-for-multi-entity-response aborted", log.Fields{"for device-id": oFsm.deviceID})
		oFsm.mutexIsAwaitingResponse.Lock()
		oFsm.isAwaitingResponse = false
		oFsm.mutexIsAwaitingResponse.Unlock()
		return fmt.Errorf(cmn.CErrWaitAborted)
	}
}

func (oFsm *UniVlanConfigFsm) performSettingMulticastME(ctx context.Context, tpID uint8, multicastGemPortID uint16, vlanID uint32) error {
	logger.Debugw(ctx, "Setting Multicast MEs", log.Fields{"device-id": oFsm.deviceID, "tpID": tpID,
		"multicastGemPortID": multicastGemPortID, "vlanID": vlanID})
	errCreateMOP := oFsm.performCreatingMulticastOperationProfile(ctx)
	if errCreateMOP != nil {
		logger.Errorw(ctx, "MulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errCreateMOP)
	}

	errSettingMOP := oFsm.performSettingMulticastOperationProfile(ctx, multicastGemPortID, vlanID)
	if errSettingMOP != nil {
		logger.Errorw(ctx, "MulticastOperationProfile setting failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errSettingMOP)
	}

	errCreateMSCI := oFsm.performCreatingMulticastSubscriberConfigInfo(ctx)
	if errCreateMSCI != nil {
		logger.Errorw(ctx, "MulticastOperationProfile setting failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, errCreateMSCI)
	}
	macBpCdEID, errMacBpCdEID := cmn.GenerateMcastANISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo))
	if errMacBpCdEID != nil {
		logger.Errorw(ctx, "MulticastMacBridgePortConfigData entity id generation failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("generateMcastANISideMBPCDEID responseError %s, error %s", oFsm.deviceID, errMacBpCdEID)

	}
	logger.Debugw(ctx, "UniVlanConfigFsm set macBpCdEID for mcast", log.Fields{
		"EntitytId": strconv.FormatInt(int64(macBpCdEID), 16), "macBpNo": oFsm.pOnuUniPort.MacBpNo,
		"in state": oFsm.PAdaptFsm.PFsm.Current(), "device-id": oFsm.deviceID})
	meParams := me.ParamData{
		EntityID: macBpCdEID,
		Attributes: me.AttributeValueMap{
			"BridgeIdPointer": cmn.MacBridgeServiceProfileEID + uint16(oFsm.pOnuUniPort.MacBpNo),
			"PortNum":         0xf0, //fixed unique ANI side indication
			"TpType":          6,    //MCGemIWTP
			"TpPointer":       multicastGemPortID,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendCreateMBPConfigDataVar(context.TODO(),
		oFsm.pDeviceHandler.GetOmciTimeout(), true, oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "MBPConfigDataVar create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo createError #{oFsm.deviceID}, error #{err}")
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "CreateMBPConfigData failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MBPConfigDataID": cmn.MacBridgeServiceProfileEID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s, error %s", oFsm.deviceID, err)
	}

	// ==> Start creating VTFD for mcast vlan

	// This attribute uniquely identifies each instance of this managed entity. Through an identical ID,
	// this managed entity is implicitly linked to an instance of the MAC bridge port configuration data ME.
	mcastVtfdID := macBpCdEID

	logger.Debugw(ctx, "UniVlanConfigFsm set VTFD for mcast", log.Fields{
		"EntitytId": strconv.FormatInt(int64(mcastVtfdID), 16), "mcastVlanID": vlanID,
		"in state": oFsm.PAdaptFsm.PFsm.Current(), "device-id": oFsm.deviceID})
	vtfdFilterList := make([]uint16, cVtfdTableSize) //needed for parameter serialization

	// FIXME: VOL-3685: Issues with resetting a table entry in EVTOCD ME
	// VTFD has to be created afresh with a new entity ID that has the same entity ID as the MBPCD ME for every
	// new vlan associated with a different TP.
	vtfdFilterList[0] = uint16(vlanID)

	meParams = me.ParamData{
		EntityID: mcastVtfdID,
		Attributes: me.AttributeValueMap{
			"VlanFilterList":   vtfdFilterList,
			"ForwardOperation": uint8(0x10), //VID investigation
			"NumberOfEntries":  oFsm.numVlanFilterEntries,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err = oFsm.pOmciCC.SendCreateVtfdVar(context.TODO(),
		oFsm.pDeviceHandler.GetOmciTimeout(), true, oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "CreateVtfdVar create failed, aborting UniVlanConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("createMcastVlanFilterData creationError %s, error %s", oFsm.deviceID, err)
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "CreateMcastVlanFilterData failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "mcastVtfdID": mcastVtfdID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("createMcastVlanFilterData responseError %s, error %s", oFsm.deviceID, err)
	}

	return nil
}

func (oFsm *UniVlanConfigFsm) performCreatingMulticastSubscriberConfigInfo(ctx context.Context) error {
	instID, err := cmn.GenerateUNISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo))
	if err != nil {
		logger.Errorw(ctx, "error generrating me instance id",
			log.Fields{"device-id": oFsm.deviceID, "error": err})
		return err
	}
	logger.Debugw(ctx, "UniVlanConfigFsm create MulticastSubscriberConfigInfo",
		log.Fields{"device-id": oFsm.deviceID, "EntityId": instID})
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"MeType": 0,
			//Direct reference to the Operation profile
			//TODO ANI side used on UNI side, not the clearest option.
			"MulticastOperationsProfilePointer": instID,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendCreateMulticastSubConfigInfoVar(context.TODO(),
		oFsm.pDeviceHandler.GetOmciTimeout(), true,
		oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "CreateMulticastSubConfigInfoVar create failed, aborting UniVlanConfigFSM!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo interface creationError %s, error %s",
			oFsm.deviceID, err)
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	//verify response
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "CreateMulticastSubConfigInfo create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastSubConfigInfo": instID})
		return fmt.Errorf("creatingMulticastSubscriberConfigInfo responseError %s", oFsm.deviceID)
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) performCreatingMulticastOperationProfile(ctx context.Context) error {
	instID, err := cmn.GenerateUNISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo))
	if err != nil {
		logger.Errorw(ctx, "error generating me instance id",
			log.Fields{"device-id": oFsm.deviceID, "error": err})
		return err
	}
	logger.Debugw(ctx, "UniVlanConfigFsm create MulticastOperationProfile",
		log.Fields{"device-id": oFsm.deviceID, "EntityId": instID})
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"IgmpVersion":  2,
			"IgmpFunction": 0,
			//0 means false
			"ImmediateLeave":         0,
			"Robustness":             2,
			"QuerierIp":              0,
			"QueryInterval":          125,
			"QuerierMaxResponseTime": 100,
			"LastMemberResponseTime": 10,
			//0 means false
			"UnauthorizedJoinBehaviour": 0,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendCreateMulticastOperationProfileVar(context.TODO(),
		oFsm.pDeviceHandler.GetOmciTimeout(), true,
		oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "CreateMulticastOperationProfileVar create failed, aborting UniVlanConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("createMulticastOperationProfileVar responseError %s, error %s", oFsm.deviceID, err)
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	//verify response
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "CreateMulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastOperationProfileID": instID})
		return fmt.Errorf("createMulticastOperationProfile responseError %s, error %s", oFsm.deviceID, err)
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) performSettingMulticastOperationProfile(ctx context.Context, multicastGemPortID uint16, vlanID uint32) error {
	instID, err := cmn.GenerateUNISideMBPCDEID(uint16(oFsm.pOnuUniPort.MacBpNo))
	if err != nil {
		logger.Errorw(ctx, "error generating me instance id",
			log.Fields{"device-id": oFsm.deviceID, "error": err})
		return err
	}
	logger.Debugw(ctx, "UniVlanConfigFsm set MulticastOperationProfile",
		log.Fields{"device-id": oFsm.deviceID, "EntityId": instID})
	//TODO check that this is correct
	// Table control
	//setCtrl = 1
	//rowPartId = 0
	//test = 0
	//rowKey = 0
	tableCtrlStr := "0100000000000000"
	tableCtrl := cmn.AsByteSlice(tableCtrlStr)
	dynamicAccessCL := make([]uint8, 24)
	copy(dynamicAccessCL, tableCtrl)
	//Multicast GemPortId
	binary.BigEndian.PutUint16(dynamicAccessCL[2:], multicastGemPortID)
	// python version waits for installation of flows, see line 723 onward of
	// brcm_openomci_onu_handler.py
	binary.BigEndian.PutUint16(dynamicAccessCL[4:], uint16(vlanID))
	//Source IP all to 0
	binary.BigEndian.PutUint32(dynamicAccessCL[6:], cmn.IPToInt32(net.IPv4(0, 0, 0, 0)))
	//TODO start and end are hardcoded, get from TP
	// Destination IP address start of range
	binary.BigEndian.PutUint32(dynamicAccessCL[10:], cmn.IPToInt32(net.IPv4(225, 0, 0, 0)))
	// Destination IP address end of range
	binary.BigEndian.PutUint32(dynamicAccessCL[14:], cmn.IPToInt32(net.IPv4(239, 255, 255, 255)))
	//imputed group bandwidth
	binary.BigEndian.PutUint16(dynamicAccessCL[18:], 0)

	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"DynamicAccessControlListTable": dynamicAccessCL,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendSetMulticastOperationProfileVar(context.TODO(),
		oFsm.pDeviceHandler.GetOmciTimeout(), true,
		oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "SetMulticastOperationProfileVar set failed, aborting UniVlanConfigFsm!",
			log.Fields{"device-id": oFsm.deviceID})
		_ = oFsm.PAdaptFsm.PFsm.Event(VlanEvReset)
		return fmt.Errorf("setMulticastOperationProfile responseError %s, error %s", oFsm.deviceID, err)
	}
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	//verify response
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "CreateMulticastOperationProfile create failed, aborting AniConfig FSM!",
			log.Fields{"device-id": oFsm.deviceID, "MulticastOperationProfileID": instID})
		return fmt.Errorf("createMulticastOperationProfile responseError %s, error %s", oFsm.deviceID, err)
	}
	return nil
}

func (oFsm *UniVlanConfigFsm) createTrafficDescriptor(ctx context.Context, aMeter *voltha.OfpMeterConfig,
	tpID uint8, uniID uint8, gemPortID uint16) error {
	logger.Infow(ctx, "Starting create traffic descriptor", log.Fields{"device-id": oFsm.deviceID, "uniID": uniID, "tpID": tpID})
	// uniTPKey  generate id to Traffic Descriptor ME. We need to create two of them. They should be unique. Because of that
	// I created unique TD ID by flow direction.
	// TODO! Traffic descriptor ME ID will check
	trafficDescriptorID := gemPortID
	if aMeter == nil {
		return fmt.Errorf("meter not found %s", oFsm.deviceID)
	}
	trafficShapingInfo, err := meters.GetTrafficShapingInfo(ctx, aMeter)
	if err != nil {
		logger.Errorw(ctx, "Traffic Shaping Info get failed", log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	cir := trafficShapingInfo.Cir + trafficShapingInfo.Gir
	cbs := trafficShapingInfo.Cbs
	pir := trafficShapingInfo.Pir
	pbs := trafficShapingInfo.Pbs

	logger.Infow(ctx, "cir-pir-cbs-pbs", log.Fields{"device-id": oFsm.deviceID, "cir": cir, "pir": pir, "cbs": cbs, "pbs": pbs})
	meParams := me.ParamData{
		EntityID: trafficDescriptorID,
		Attributes: me.AttributeValueMap{
			"Cir":                  cir,
			"Pir":                  pir,
			"Cbs":                  cbs,
			"Pbs":                  pbs,
			"ColourMode":           1,
			"IngressColourMarking": 3,
			"EgressColourMarking":  3,
			"MeterType":            1,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, errCreateTD := oFsm.pOmciCC.SendCreateTDVar(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(),
		true, oFsm.PAdaptFsm.CommChan, meParams)
	if errCreateTD != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "Traffic Descriptor create failed", log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "Traffic Descriptor create failed, aborting VlanConfig FSM!", log.Fields{"device-id": oFsm.deviceID})
		return err
	}

	// Note: in the below request the gemport entity id is same as the gemport id and the traffic descriptor entity id is also same as gemport id
	err = oFsm.setTrafficDescriptorToGemPortNWCTP(ctx, gemPortID, gemPortID)
	if err != nil {
		logger.Errorw(ctx, "Traffic Descriptor set failed to Gem Port Network CTP, aborting VlanConfig FSM!", log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	logger.Infow(ctx, "Set TD Info to GemPortNWCTP successfully", log.Fields{"device-id": oFsm.deviceID, "gem-port-id": gemPortID, "td-id": trafficDescriptorID})

	return nil
}

func (oFsm *UniVlanConfigFsm) setTrafficDescriptorToGemPortNWCTP(ctx context.Context, gemPortEntityID uint16, trafficDescriptorEntityID uint16) error {
	logger.Debugw(ctx, "Starting Set Traffic Descriptor to GemPortNWCTP",
		log.Fields{"device-id": oFsm.deviceID, "gem-port-entity-id": gemPortEntityID, "traffic-descriptor-entity-id": trafficDescriptorEntityID})
	meParams := me.ParamData{
		EntityID: gemPortEntityID,
		Attributes: me.AttributeValueMap{
			"TrafficDescriptorProfilePointerForUpstream": trafficDescriptorEntityID,
		},
	}
	oFsm.mutexPLastTxMeInstance.Lock()
	meInstance, err := oFsm.pOmciCC.SendSetGemNCTPVar(log.WithSpanFromContext(context.TODO(), ctx),
		oFsm.pDeviceHandler.GetOmciTimeout(), true, oFsm.PAdaptFsm.CommChan, meParams)
	if err != nil {
		oFsm.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "GemNCTP set failed", log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	oFsm.pLastTxMeInstance = meInstance
	oFsm.mutexPLastTxMeInstance.Unlock()
	err = oFsm.waitforOmciResponse(ctx)
	if err != nil {
		logger.Errorw(ctx, "Upstream Traffic Descriptor set failed, aborting VlanConfig FSM!", log.Fields{"device-id": oFsm.deviceID})
		return err
	}
	return nil
}

// IsFlowRemovePending returns true if there are pending flows to remove, else false.
func (oFsm *UniVlanConfigFsm) IsFlowRemovePending(ctx context.Context, aFlowDeleteChannel chan<- bool) bool {
	if oFsm == nil {
		logger.Error(ctx, "no valid UniVlanConfigFsm!")
		return false
	}
	oFsm.mutexFlowParams.Lock()
	defer oFsm.mutexFlowParams.Unlock()
	if len(oFsm.uniRemoveFlowsSlice) > 0 {
		//flow removal is still ongoing/pending
		oFsm.signalOnFlowDelete = true
		oFsm.flowDeleteChannel = aFlowDeleteChannel
		return true
	}
	return false
}

func (oFsm *UniVlanConfigFsm) reconcileVlanFilterList(ctx context.Context, aSetVid uint16) {
	// VOL-4342 - reconcile vlanFilterList[] for possible later flow removal
	if aSetVid == uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Debugw(ctx, "reconciling - transparent setup: no VTFD config was required",
			log.Fields{"device-id": oFsm.deviceID})
	} else {
		oFsm.vlanFilterList[oFsm.numVlanFilterEntries] = aSetVid
		logger.Debugw(ctx, "reconciling - Vid of VTFD stored in list", log.Fields{
			"index":     oFsm.numVlanFilterEntries,
			"vid":       strconv.FormatInt(int64(oFsm.vlanFilterList[oFsm.numVlanFilterEntries]), 16),
			"device-id": oFsm.deviceID})
		oFsm.numVlanFilterEntries++
	}
}

// pushReponseOnFlowResponseChannel pushes response on the response channel if available
func (oFsm *UniVlanConfigFsm) pushReponseOnFlowResponseChannel(ctx context.Context, respChan *chan error, err error) {
	if respChan != nil {
		// Do it in a non blocking fashion, so that in case the flow handler routine has shutdown for any reason, we do not block here
		select {
		case *respChan <- err:
			logger.Debugw(ctx, "submitted-response-for-flow", log.Fields{"device-id": oFsm.deviceID, "err": err})
		default:
		}
	}
}
