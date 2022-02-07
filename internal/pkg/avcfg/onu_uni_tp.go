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
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-protos/v5/go/tech_profile"
)

//definitions for TechProfileProcessing - copied from OltAdapter:openolt_flowmgr.go
//  could perhaps be defined more globally
const (
	// binaryStringPrefix is binary string prefix
	binaryStringPrefix = "0b"
	// binaryBit1 is binary bit 1 expressed as a character
	//binaryBit1 = '1'
)

// ResourceEntry - TODO: add comment
type ResourceEntry int

// TODO: add comment
const (
	CResourceGemPort ResourceEntry = 1
	CResourceTcont   ResourceEntry = 2
)

type tTechProfileIndication struct {
	techProfileType       string
	techProfileID         uint8
	techProfileConfigDone bool
	techProfileToDelete   bool
}

type tcontParamStruct struct {
	allocID     uint16
	schedPolicy uint8
}
type gemPortParamStruct struct {
	//ponOmciCC       bool
	gemPortID       uint16
	direction       uint8
	gemPortEncState uint8
	prioQueueIndex  uint8
	pbitString      string
	discardPolicy   string
	//could also be a queue specific parameter, not used that way here
	//maxQueueSize     uint16
	queueSchedPolicy string
	queueWeight      uint8
	removeGemID      uint16
	isMulticast      bool
	//TODO check if this has any value/difference from gemPortId
	multicastGemPortID uint16
	staticACL          string
	dynamicACL         string
}

//refers to one tcont and its properties and all assigned GemPorts and their properties
type tcontGemList struct {
	tcontParams      tcontParamStruct
	mapGemPortParams map[uint16]*gemPortParamStruct
}

// refers a unique combination of uniID and tpID for a given ONU.
type uniTP struct {
	uniID uint8
	tpID  uint8
}

//OnuUniTechProf structure holds information about the TechProfiles attached to Uni Ports of the ONU
type OnuUniTechProf struct {
	deviceID                 string
	baseDeviceHandler        cmn.IdeviceHandler
	onuDevice                cmn.IonuDeviceEntry
	tpProcMutex              sync.RWMutex
	chTpConfigProcessingStep chan uint8
	mapUniTpIndication       map[uniTP]*tTechProfileIndication //use pointer values to ease assignments to the map
	mapPonAniConfig          map[uniTP]*tcontGemList           //per UNI: use pointer values to ease assignments to the map
	PAniConfigFsm            map[uniTP]*UniPonAniConfigFsm
	procResult               map[uniTP]error //error indication of processing
	mutexTPState             sync.RWMutex
	tpProfileExists          map[uniTP]bool
	tpProfileResetting       map[uniTP]bool
	mapRemoveGemEntry        map[uniTP]*gemPortParamStruct //per UNI: pointer to GemEntry to be removed
}

func (onuTP *OnuUniTechProf) multicastConfiguredForOtherUniTps(ctx context.Context, uniTpKey uniTP) bool {
	for _, aniFsm := range onuTP.PAniConfigFsm {
		if aniFsm.uniTpKey.uniID == uniTpKey.uniID && aniFsm.uniTpKey.tpID == uniTpKey.tpID {
			continue
		}
		if aniFsm.hasMulticastGem(ctx) {
			return true
		}
	}
	return false
}

//NewOnuUniTechProf returns the instance of a OnuUniTechProf
//(one instance per ONU/deviceHandler for all possible UNI's)
func NewOnuUniTechProf(ctx context.Context, aDeviceHandler cmn.IdeviceHandler, aOnuDev cmn.IonuDeviceEntry) *OnuUniTechProf {

	var onuTP OnuUniTechProf
	onuTP.deviceID = aDeviceHandler.GetDeviceID()
	logger.Debugw(ctx, "init-OnuUniTechProf", log.Fields{"device-id": onuTP.deviceID})
	onuTP.baseDeviceHandler = aDeviceHandler
	onuTP.onuDevice = aOnuDev
	onuTP.chTpConfigProcessingStep = make(chan uint8)
	onuTP.mapUniTpIndication = make(map[uniTP]*tTechProfileIndication)
	onuTP.mapPonAniConfig = make(map[uniTP]*tcontGemList)
	onuTP.procResult = make(map[uniTP]error)
	onuTP.tpProfileExists = make(map[uniTP]bool)
	onuTP.tpProfileResetting = make(map[uniTP]bool)
	onuTP.mapRemoveGemEntry = make(map[uniTP]*gemPortParamStruct)

	return &onuTP
}

// LockTpProcMutex locks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) LockTpProcMutex() {
	onuTP.tpProcMutex.Lock()
}

// UnlockTpProcMutex unlocks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) UnlockTpProcMutex() {
	onuTP.tpProcMutex.Unlock()
}

// ResetTpProcessingErrorIndication resets the internal error indication
// need to be called before evaluation of any subsequent processing (given by waitForTpCompletion())
func (onuTP *OnuUniTechProf) ResetTpProcessingErrorIndication(aUniID uint8, aTpID uint8) {
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	onuTP.procResult[uniTP{uniID: aUniID, tpID: aTpID}] = nil
}

// GetTpProcessingErrorIndication - TODO: add comment
func (onuTP *OnuUniTechProf) GetTpProcessingErrorIndication(aUniID uint8, aTpID uint8) error {
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	return onuTP.procResult[uniTP{uniID: aUniID, tpID: aTpID}]
}

// ConfigureUniTp checks existing tp resources to configure and starts the corresponding OMCI configuation of the UNI port
// all possibly blocking processing must be run in background to allow for deadline supervision!
// but take care on sequential background processing when needed (logical dependencies)
//   use waitForTimeoutOrCompletion(ctx, chTpConfigProcessingStep, processingStep) for internal synchronization
func (onuTP *OnuUniTechProf) ConfigureUniTp(ctx context.Context,
	aUniID uint8, aPathString string, tpInst tech_profile.TechProfileInstance, wg *sync.WaitGroup) {
	defer wg.Done() //always decrement the waitGroup on return
	logger.Debugw(ctx, "configure the Uni according to TpPath", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString})
	tpID, err := cmn.GetTpIDFromTpPath(aPathString)
	uniTpKey := uniTP{uniID: aUniID, tpID: tpID}
	if err != nil {
		logger.Errorw(ctx, "error-extracting-tp-id-from-tp-path", log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString})
		return
	}

	//ensure that the given uniID is available (configured) in the UniPort class (used for OMCI entities)
	var pCurrentUniPort *cmn.OnuUniPort
	for _, uniPort := range *onuTP.baseDeviceHandler.GetUniEntityMap() {
		// only if this port is validated for operState transfer
		if uniPort.UniID == aUniID {
			pCurrentUniPort = uniPort
			break //found - end search loop
		}
	}
	if pCurrentUniPort == nil {
		logger.Errorw(ctx, "TechProfile configuration aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": uniTpKey.tpID})
		onuTP.mutexTPState.Lock()
		defer onuTP.mutexTPState.Unlock()
		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: requested uniID not found %d on %s",
			aUniID, onuTP.deviceID)
		return
	}

	if onuTP.getProfileResetting(uniTpKey) {
		logger.Debugw(ctx, "aborting TP configuration, reset requested in parallel", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": uniTpKey.tpID})
		onuTP.mutexTPState.Lock()
		defer onuTP.mutexTPState.Unlock()
		onuTP.procResult[uniTpKey] = fmt.Errorf(
			"techProfile config aborted - reset requested in parallel - for uniID %d on %s",
			aUniID, onuTP.deviceID)
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep

	//according to UpdateOnuUniTpPath() logic the assumption here is, that this configuration is only called
	//  in case the KVPath has changed for the given UNI,
	//  as T-Cont and Gem-Id's are dependent on TechProfile-Id this means, that possibly previously existing
	//  (ANI) configuration of this port has to be removed first
	//  (moreover in this case a possibly existing flow configuration is also not valid anymore and needs clean-up as well)
	//  existence of configuration can be detected based on tp stored TCONT's
	//TODO:
	/* if tcontMap  not empty {
		go onuTP.deleteAniSideConfig(ctx, aUniID, processingStep)
		if !onuTP.waitForTimeoutOrCompletion(ctx, chTpConfigProcessingStep, processingStep) {
			//timeout or error detected
			return
		}
		clear tcontMap
	}

	processingStep++
	*/
	go onuTP.readAniSideConfigFromTechProfile(ctx, aUniID, tpID, aPathString, tpInst, processingStep)
	if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
		//timeout or error detected
		onuTP.mutexTPState.RLock()
		ok := onuTP.tpProfileExists[uniTpKey]
		onuTP.mutexTPState.RUnlock()
		if ok {
			//ignore the internal error in case the new profile is already configured
			// and abort the processing here
			return
		}
		logger.Errorw(ctx, "tech-profile related configuration aborted on read",
			log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
		onuTP.mutexTPState.Lock()
		defer onuTP.mutexTPState.Unlock()
		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: tech-profile read issue for %d on %s",
			aUniID, onuTP.deviceID)
		return
	}
	if onuTP.getProfileResetting(uniTpKey) {
		logger.Debugw(ctx, "aborting TP configuration, reset requested in parallel", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": uniTpKey.tpID})
		onuTP.mutexTPState.Lock()
		defer onuTP.mutexTPState.Unlock()
		onuTP.procResult[uniTpKey] = fmt.Errorf(
			"techProfile config aborted - reset requested in parallel - for uniID %d on %s",
			aUniID, onuTP.deviceID)
		return
	}
	processingStep++

	//ensure read protection for access to mapPonAniConfig
	onuTP.mutexTPState.RLock()
	valuePA, existPA := onuTP.mapPonAniConfig[uniTpKey]
	onuTP.mutexTPState.RUnlock()
	if existPA {
		if valuePA != nil {
			//Config data for this uni and and at least TCont Index 0 exist
			if err := onuTP.setAniSideConfigFromTechProfile(ctx, aUniID, tpID, pCurrentUniPort, processingStep); err != nil {
				logger.Errorw(ctx, "tech-profile related FSM could not be started",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				onuTP.mutexTPState.Lock()
				defer onuTP.mutexTPState.Unlock()
				onuTP.procResult[uniTpKey] = err
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected (included wanted cancellation after e.g. disable device (FsmReset))
				logger.Warnw(ctx, "tech-profile related configuration aborted on set",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})

				onuTP.mutexTPState.Lock()
				defer onuTP.mutexTPState.Unlock()
				onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: Omci AniSideConfig failed %d on %s",
					aUniID, onuTP.deviceID)
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				if _, ok := onuTP.PAniConfigFsm[uniTpKey]; ok {
					_ = onuTP.PAniConfigFsm[uniTpKey].PAdaptFsm.PFsm.Event(aniEvReset)
				}
				return
			}
		} else {
			// strange: UNI entry exists, but no ANI data, maybe such situation should be cleared up (if observed)
			logger.Errorw(ctx, "no Tcont/Gem data for this UNI found - abort", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID})
			onuTP.mutexTPState.Lock()
			defer onuTP.mutexTPState.Unlock()
			onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: no Tcont/Gem data found for this UNI %d on %s",
				aUniID, onuTP.deviceID)
			return
		}
	} else {
		logger.Errorw(ctx, "no PonAni data for this UNI found - abort", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID})

		onuTP.mutexTPState.Lock()
		defer onuTP.mutexTPState.Unlock()
		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: no AniSide data found for this UNI %d on %s",
			aUniID, onuTP.deviceID)
		return
	}
}

/* internal methods *********************/
// nolint: gocyclo
func (onuTP *OnuUniTechProf) readAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aTpID uint8, aPathString string, tpInst tech_profile.TechProfileInstance, aProcessingStep uint8) {
	var err error
	//store profile type and identifier for later usage within the OMCI identifier and possibly ME setup
	//pathstring is defined to be in the form of <ProfType>/<profID>/<Interface/../Identifier>
	subStringSlice := strings.Split(aPathString, "/")
	if len(subStringSlice) <= 2 {
		logger.Errorw(ctx, "invalid path name format",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}

	//ensure write protection for access to used maps
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()

	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.tpProfileExists[uniTP{uniID: aUniID, tpID: aTpID}] = false

	//at this point it is assumed that a new TechProfile is assigned to the UNI
	//expectation is that no TPIndication entry exists here, if exists and with the same TPId
	//  then we throw a warning, set an internal error and abort with error,
	//  which is later re-defined to success response to OLT adapter
	//  if TPId has changed, current data is removed (note that the ONU config state may be
	// 	  ambivalent in such a case)
	if _, existTP := onuTP.mapUniTpIndication[uniTPKey]; existTP {
		logger.Warnw(ctx, "Some active profile entry at reading new TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID,
				"uni-id": aUniID, "wrongProfile": onuTP.mapUniTpIndication[uniTPKey].techProfileID})
		if aTpID == onuTP.mapUniTpIndication[uniTPKey].techProfileID {
			// ProfId not changed - assume profile to be still the same
			// anyway this should not appear after full support of profile (Gem/TCont) removal
			logger.Warnw(ctx, "New TechProfile already exists - aborting configuration",
				log.Fields{"device-id": onuTP.deviceID})
			onuTP.tpProfileExists[uniTPKey] = true
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
		//delete on the mapUniTpIndication map not needed, just overwritten later
		//delete on the PonAniConfig map should be safe, even if not existing
		delete(onuTP.mapPonAniConfig, uniTPKey)
	} else {
		// this is normal processing
		onuTP.mapUniTpIndication[uniTPKey] = &tTechProfileIndication{} //need to assign some (empty) struct memory first!
	}

	onuTP.mapUniTpIndication[uniTPKey].techProfileType = subStringSlice[0]
	//note the limitation on ID range (probably even more limited) - based on usage within OMCI EntityID
	onuTP.mapUniTpIndication[uniTPKey].techProfileID = aTpID
	onuTP.mapUniTpIndication[uniTPKey].techProfileConfigDone = false
	onuTP.mapUniTpIndication[uniTPKey].techProfileToDelete = false
	logger.Debugw(ctx, "tech-profile path indications",
		log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID,
			"profType": onuTP.mapUniTpIndication[uniTPKey].techProfileType,
			"profID":   onuTP.mapUniTpIndication[uniTPKey].techProfileID})

	//default start with 1Tcont profile, later perhaps extend to MultiTcontMultiGem
	localMapGemPortParams := make(map[uint16]*gemPortParamStruct)
	onuTP.mapPonAniConfig[uniTPKey] = &tcontGemList{tcontParamStruct{}, localMapGemPortParams}

	//note: the code is currently restricted to one TCcont per Onu (index [0])
	//get the relevant values from the profile and store to mapPonAniConfig
	onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID = uint16(tpInst.UsScheduler.AllocId)
	//maybe tCont scheduling not (yet) needed - just to basically have it for future
	//  (would only be relevant in case of ONU-2G QOS configuration flexibility)
	if tpInst.UsScheduler.QSchedPolicy == tech_profile.SchedulingPolicy_StrictPriority {
		onuTP.mapPonAniConfig[uniTPKey].tcontParams.schedPolicy = 1 //for the moment fixed value acc. G.988 //TODO: defines!
	} else {
		//default profile defines "Hybrid" - which probably comes down to WRR with some weigthts for SP
		onuTP.mapPonAniConfig[uniTPKey].tcontParams.schedPolicy = 2 //for G.988 WRR
	}
	loNumGemPorts := tpInst.NumGemPorts
	loGemPortRead := false
	for pos, content := range tpInst.UpstreamGemPortAttributeList {
		if uint32(pos) == loNumGemPorts {
			logger.Debugw(ctx, "PonAniConfig abort GemPortList - GemList exceeds set NumberOfGemPorts",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "NumGem": loNumGemPorts})
			break
		}
		if pos == 0 {
			//at least one upstream GemPort should always exist (else traffic profile makes no sense)
			loGemPortRead = true
		}
		//for all GemPorts we need to extend the mapGemPortParams
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)] = &gemPortParamStruct{}

		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].gemPortID =
			uint16(content.GemportId)
		//direction can be correlated later with Downstream list,
		//  for now just assume bidirectional (upstream never exists alone)
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].direction = 3 //as defined in G.988
		// expected Prio-Queue values 0..7 with 7 for highest PrioQueue, QueueIndex=Prio = 0..7
		if content.PriorityQ > 7 {
			logger.Errorw(ctx, "PonAniConfig reject on GemPortList - PrioQueue value invalid",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "PrioQueue": content.PriorityQ})
			//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
			delete(onuTP.mapPonAniConfig, uniTPKey)
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].prioQueueIndex =
			uint8(content.PriorityQ)
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].pbitString =
			strings.TrimPrefix(content.PbitMap, binaryStringPrefix)
		if content.AesEncryption == "True" {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].gemPortEncState = 1
		} else {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].gemPortEncState = 0
		}
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].discardPolicy =
			content.DiscardPolicy.String()
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].queueSchedPolicy =
			content.SchedulingPolicy.String()
		//'GemWeight' looks strange in default profile, for now we just copy the weight to first queue
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(content.GemportId)].queueWeight =
			uint8(content.Weight)
	}

	for _, downstreamContent := range tpInst.DownstreamGemPortAttributeList {
		logger.Debugw(ctx, "Operating on Downstream Gem Port", log.Fields{"downstream-gem": downstreamContent})
		//Commenting this out due to faliure, needs investigation
		//if uint32(pos) == loNumGemPorts {
		//	logger.Debugw("PonAniConfig abort GemPortList - GemList exceeds set NumberOfGemPorts",
		//		log.Fields{"device-id": onuTP.deviceID, "index": pos, "NumGem": loNumGemPorts})
		//	break
		//}
		isMulticast := false
		//Flag is defined as string in the TP in voltha-lib-go, parsing it from string
		if downstreamContent.IsMulticast != "" {
			isMulticast, err = strconv.ParseBool(downstreamContent.IsMulticast)
			if err != nil {
				logger.Errorw(ctx, "multicast-error-config-unknown-flag-in-technology-profile",
					log.Fields{"UniTpKey": uniTPKey, "downstream-gem": downstreamContent, "error": err})
				continue
			}
		}
		logger.Infow(ctx, "Gem Port is multicast", log.Fields{"isMulticast": isMulticast})
		if isMulticast {
			mcastGemID := uint16(downstreamContent.MulticastGemId)
			_, existing := onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID]
			if existing {
				//GEM port was previously configured, avoid setting multicast attributes
				logger.Errorw(ctx, "multicast-error-config-existing-gem-port-config", log.Fields{"UniTpKey": uniTPKey,
					"downstream-gem": downstreamContent, "key": mcastGemID})
				continue
			} else {
				//GEM port is not configured, setting multicast attributes
				logger.Infow(ctx, "creating-multicast-gem-port", log.Fields{"uniTpKey": uniTPKey,
					"gemPortId": mcastGemID, "key": mcastGemID})

				//for all further GemPorts we need to extend the mapGemPortParams
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID] = &gemPortParamStruct{}

				//Separate McastGemId is derived from OMCI-lib-go, if not needed first needs to be removed there.
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].gemPortID = mcastGemID
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].direction = 2 // for ANI to UNI as defined in G.988

				if downstreamContent.AesEncryption == "True" {
					onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].gemPortEncState = 1
				} else {
					onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].gemPortEncState = 0
				}

				// expected Prio-Queue values 0..7 with 7 for highest PrioQueue, QueueIndex=Prio = 0..7
				if downstreamContent.PriorityQ > 7 {
					logger.Errorw(ctx, "PonAniConfig reject on GemPortList - PrioQueue value invalid",
						log.Fields{"device-id": onuTP.deviceID, "index": mcastGemID, "PrioQueue": downstreamContent.PriorityQ})
					//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
					delete(onuTP.mapPonAniConfig, uniTPKey)
					onuTP.chTpConfigProcessingStep <- 0 //error indication
					return
				}
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].prioQueueIndex =
					uint8(downstreamContent.PriorityQ)
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].pbitString =
					strings.TrimPrefix(downstreamContent.PbitMap, binaryStringPrefix)

				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].discardPolicy =
					downstreamContent.DiscardPolicy.String()
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].queueSchedPolicy =
					downstreamContent.SchedulingPolicy.String()
				//'GemWeight' looks strange in default profile, for now we just copy the weight to first queue
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].queueWeight =
					uint8(downstreamContent.Weight)

				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].isMulticast = isMulticast
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].multicastGemPortID =
					uint16(downstreamContent.MulticastGemId)
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].staticACL = downstreamContent.StaticAccessControlList
				onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[mcastGemID].dynamicACL = downstreamContent.DynamicAccessControlList
			}
		}
	}

	if !loGemPortRead {
		logger.Errorw(ctx, "PonAniConfig reject - no GemPort could be read from TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
		delete(onuTP.mapPonAniConfig, uniTPKey)
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}
	//logger does not simply output the given structures, just give some example debug values
	logger.Debugw(ctx, "PonAniConfig read from TechProfile", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID,
		"AllocId": onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID})
	for gemPortID, gemEntry := range onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams {
		logger.Debugw(ctx, "PonAniConfig read from TechProfile", log.Fields{
			"GemPort":         gemPortID,
			"QueueScheduling": gemEntry.queueSchedPolicy})
	}

	onuTP.chTpConfigProcessingStep <- aProcessingStep //done
}

func (onuTP *OnuUniTechProf) setAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aTpID uint8, apCurrentUniPort *cmn.OnuUniPort, aProcessingStep uint8) error {

	//OMCI transfer of ANI data acc. to mapPonAniConfig
	// also the FSM's are running in background,
	//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	if onuTP.PAniConfigFsm == nil {
		return onuTP.createAniConfigFsm(ctx, aUniID, aTpID, apCurrentUniPort, cmn.OmciAniConfigDone, aProcessingStep)
	} else if _, ok := onuTP.PAniConfigFsm[uniTPKey]; !ok {
		return onuTP.createAniConfigFsm(ctx, aUniID, aTpID, apCurrentUniPort, cmn.OmciAniConfigDone, aProcessingStep)
	}
	//AniConfigFsm already init
	return onuTP.runAniConfigFsm(ctx, aniEvStart, aProcessingStep, aUniID, aTpID)
}

// DeleteTpResource removes Resources from the ONU's specified Uni
// nolint: gocyclo
func (onuTP *OnuUniTechProf) DeleteTpResource(ctx context.Context,
	aUniID uint8, aTpID uint8, aPathString string, aResource ResourceEntry, aEntryID uint32,
	wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw(ctx, "will remove TP resources from ONU's UNI", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "Resource": aResource})
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}

	if CResourceGemPort == aResource {
		logger.Debugw(ctx, "remove GemPort from the list of existing ones of the TP", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "GemPort": aEntryID})

		//ensure read protection for access to mapPonAniConfig
		onuTP.mutexTPState.RLock()
		// check if the requested GemPort exists in the DB, indicate it to the FSM
		// store locally to remove it from DB later on success
		pLocAniConfigOnUni := onuTP.mapPonAniConfig[uniTPKey]
		if pLocAniConfigOnUni == nil {
			onuTP.mutexTPState.RUnlock()
			// No relevant entry exists anymore - acknowledge success
			logger.Debugw(ctx, "AniConfig or GemEntry do not exists in DB", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			return
		}
		onuTP.mutexTPState.RUnlock()

		for gemPortID, gemEntry := range pLocAniConfigOnUni.mapGemPortParams {
			logger.Debugw(ctx, "gemEntry info when remove gem port", log.Fields{"device-id": onuTP.deviceID,
				"gemEntry": gemEntry})
			if gemPortID == uint16(aEntryID) {
				//GemEntry to be deleted found
				gemEntry.removeGemID = gemPortID //store the index for later removal
				onuTP.mapRemoveGemEntry[uniTPKey] = pLocAniConfigOnUni.mapGemPortParams[gemPortID]
				logger.Debugw(ctx, "Remove-GemEntry stored", log.Fields{
					"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "GemPort": aEntryID})

				break //abort loop, always only one GemPort to remove
			}
		}
		if onuTP.mapRemoveGemEntry[uniTPKey] == nil {
			logger.Errorw(ctx, "GemPort removal aborted - GemPort not found",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "GemPort": aEntryID})
			/* Do not set some error indication to the outside system interface on delete
			   assume there is nothing to be deleted internally and hope a new config request will recover the situation
			 onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: GemPort not found %d for %d on %s",
				aEntryID, aUniID, onuTP.deviceID)
			*/
			return
		}
		if onuTP.baseDeviceHandler.IsReadyForOmciConfig() {
			// check that the TpConfigRequest was done before
			//   -> that is implicitly done using the AniConfigFsm,
			//      which must be in the according state to remove something
			if onuTP.PAniConfigFsm == nil {
				logger.Errorw(ctx, "abort GemPort removal - no AniConfigFsm available",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				/* Do not set some error indication to the outside system interface on delete (see above)
				onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: no AniConfigFsm available %d on %s",
					aUniID, onuTP.deviceID)
				*/
				//if the FSM is not valid, also TP related remove data should not be valid:
				// remove GemPort from config DB
				//ensure write protection for access to mapPonAniConfig
				onuTP.mutexTPState.Lock()
				delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeGemID)
				// remove the removeEntry
				delete(onuTP.mapRemoveGemEntry, uniTPKey)
				onuTP.mutexTPState.Unlock()
				return
			}
			if _, ok := onuTP.PAniConfigFsm[uniTPKey]; !ok {
				logger.Errorw(ctx, "abort GemPort removal - no AniConfigFsm available for this uni/tp",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
				/* Do not set some error indication to the outside system interface on delete (see above)
				onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: no AniConfigFsm available %d on %s for tpid",
					aUniID, onuTP.deviceID, aTpID)
				*/
				//if the FSM is not valid, also TP related remove data should not be valid:
				// remove GemPort from config DB
				//ensure write protection for access to mapPonAniConfig
				onuTP.mutexTPState.Lock()
				delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeGemID)
				// remove the removeEntry
				delete(onuTP.mapRemoveGemEntry, uniTPKey)
				onuTP.mutexTPState.Unlock()
				return
			}
			if onuTP.getProfileResetting(uniTPKey) {
				logger.Debugw(ctx, "aborting GemRemoval on FSM, reset requested in parallel", log.Fields{
					"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
				//ensure write protection for access to mapPonAniConfig
				onuTP.mutexTPState.Lock()
				delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeGemID)
				// remove the removeEntry
				delete(onuTP.mapRemoveGemEntry, uniTPKey)
				onuTP.mutexTPState.Unlock()
				return
			}
			// initiate OMCI GemPort related removal
			var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep
			//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
			if nil != onuTP.runAniConfigFsm(ctx, aniEvRemGemiw, processingStep, aUniID, aTpID) {
				//even if the FSM invocation did not work we don't indicate a problem within procResult
				//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected (included wanted cancellation after e.g. disable device (FsmReset))
				logger.Warnw(ctx, "GemPort removal aborted - Omci AniSideConfig failed",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				//even if the FSM delete execution did not work we don't indicate a problem within procResult
				//we should never respond to delete with error ...
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				if _, ok := onuTP.PAniConfigFsm[uniTPKey]; ok {
					_ = onuTP.PAniConfigFsm[uniTPKey].PAdaptFsm.PFsm.Event(aniEvReset)
				}
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
		} else {
			//if we can't do the OMCI processing we also suppress the ProcStatusUpdate
			//this is needed as in the device-down case where all FSM's are getting reset and internal data gets cleared
			//as a consequence a possible remove-flow does not see any dependency on the TechProfile anymore and is executed (pro forma) directly
			//a later TechProfile removal would cause the device-reason to be updated to 'techProfile-delete-success' which is not the expected state
			// and anyway is no real useful information at that stage
			logger.Debugw(ctx, "UniPonAniConfigFsm delete Gem on OMCI skipped based on device state", log.Fields{
				"device-id": onuTP.deviceID, "device-state": onuTP.baseDeviceHandler.GetDeviceReasonString()})
		}
		// remove GemPort from config DB
		//ensure write protection for access to mapPonAniConfig
		logger.Debugw(ctx, "UniPonAniConfigFsm removing gem from config data and clearing ani FSM", log.Fields{
			"device-id": onuTP.deviceID, "gem-id": onuTP.mapRemoveGemEntry[uniTPKey].removeGemID, "uniTPKey": uniTPKey})
		onuTP.mutexTPState.Lock()
		delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeGemID)
		// remove the removeEntry
		delete(onuTP.mapRemoveGemEntry, uniTPKey)
		onuTP.mutexTPState.Unlock()
	} else { //if CResourceTcont == aResource {
		logger.Debugw(ctx, "reset TCont with AllocId", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "allocId": aEntryID})

		//ensure read protection for access to mapPonAniConfig
		onuTP.mutexTPState.RLock()
		// check if the TCont with the indicated AllocId exists in the DB, indicate its EntityId to the FSM
		pLocAniConfigOnUni := onuTP.mapPonAniConfig[uniTPKey]
		if pLocAniConfigOnUni == nil {
			// No relevant entry exists anymore - acknowledge success
			onuTP.mutexTPState.RUnlock()
			logger.Debugw(ctx, "AniConfig or TCont entry do not exists in DB", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			return
		}
		onuTP.mutexTPState.RUnlock()

		if pLocAniConfigOnUni.tcontParams.allocID != uint16(aEntryID) {
			logger.Errorw(ctx, "TCont removal aborted - indicated AllocId not found",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "AllocId": aEntryID})
			/* Do not set some error indication to the outside system interface on delete
			   assume there is nothing to be deleted internally and hope a new config request will recover the situation
			 onuTP.procResult[uniTpKey] = fmt.Errorf("TCont removal aborted: AllocId not found %d for %d on %s",
				aEntryID, aUniID, onuTP.deviceID)
			*/
			return
		}
		//T-Cont to be reset found
		logger.Debugw(ctx, "Reset-T-Cont AllocId found - valid", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "AllocId": aEntryID})
		if onuTP.PAniConfigFsm == nil {
			logger.Errorw(ctx, "no TCont removal on OMCI - no AniConfigFsm available",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
			/* Do not set some error indication to the outside system interface on delete (see above)
			onuTP.procResult[uniTpKey] = fmt.Errorf("TCont cleanup aborted: no AniConfigFsm available %d on %s",
				aUniID, onuTP.deviceID)
			*/
			return
		}
		if _, ok := onuTP.PAniConfigFsm[uniTPKey]; !ok {
			logger.Errorw(ctx, "no TCont removal on OMCI - no AniConfigFsm available for this uni/tp",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			//even if the FSM invocation did not work we don't indicate a problem within procResult
			//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
			//if the FSM is not valid, also TP related data should not be valid - clear the internal store profile data
			return
		}
		if onuTP.baseDeviceHandler.IsReadyForOmciConfig() {
			// check that the TpConfigRequest was done before
			//   -> that is implicitly done using the AniConfigFsm,
			//      which must be in the according state to remove something
			if onuTP.getProfileResetting(uniTPKey) {
				logger.Debugw(ctx, "aborting TCont removal on FSM, reset requested in parallel", log.Fields{
					"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
				return
			}
			// initiate OMCI TCont related cleanup
			var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep
			//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
			if nil != onuTP.runAniConfigFsm(ctx, aniEvRemTcontPath, processingStep, aUniID, aTpID) {
				//even if the FSM invocation did not work we don't indicate a problem within procResult
				//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected (included wanted cancellation after e.g. disable device (FsmReset))
				logger.Warnw(ctx, "TCont cleanup aborted - Omci AniSideConfig failed",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				//even if the FSM delete execution did not work we don't indicate a problem within procResult
				//we should never respond to delete with error ...
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				if _, ok := onuTP.PAniConfigFsm[uniTPKey]; ok {
					_ = onuTP.PAniConfigFsm[uniTPKey].PAdaptFsm.PFsm.Event(aniEvReset)
				}
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
		} else {
			//see gemPort comments
			logger.Debugw(ctx, "UniPonAniConfigFsm TCont cleanup on OMCI skipped based on device state", log.Fields{
				"device-id": onuTP.deviceID, "device-state": onuTP.baseDeviceHandler.GetDeviceReasonString()})
		}
	}

}

// IsTechProfileConfigCleared - TODO: add comment
func (onuTP *OnuUniTechProf) IsTechProfileConfigCleared(ctx context.Context, uniID uint8, tpID uint8) bool {
	uniTPKey := uniTP{uniID: uniID, tpID: tpID}
	logger.Debugw(ctx, "IsTechProfileConfigCleared", log.Fields{"device-id": onuTP.deviceID})
	if onuTP.mapPonAniConfig[uniTPKey] != nil {
		mapGemPortParams := onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams
		unicastGemCount := 0
		for _, gemEntry := range mapGemPortParams {
			if !gemEntry.isMulticast {
				unicastGemCount++
			}
		}
		if unicastGemCount == 0 || onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID == 0 {
			logger.Debugw(ctx, "clearing-ani-side-config", log.Fields{
				"device-id": onuTP.deviceID, "uniTpKey": uniTPKey})
			onuTP.clearAniSideConfig(ctx, uniID, tpID)
			if _, ok := onuTP.PAniConfigFsm[uniTPKey]; ok {
				_ = onuTP.PAniConfigFsm[uniTPKey].PAdaptFsm.PFsm.Event(aniEvReset)
			}
			go onuTP.baseDeviceHandler.DeviceProcStatusUpdate(ctx, cmn.OmciAniResourceRemoved)
			return true
		}
	}
	return false
}

func (onuTP *OnuUniTechProf) waitForTimeoutOrCompletion(
	ctx context.Context, aChTpProcessingStep <-chan uint8, aProcessingStep uint8) bool {
	select {
	case <-ctx.Done():
		logger.Warnw(ctx, "processing not completed in-time: force release of TpProcMutex!",
			log.Fields{"device-id": onuTP.deviceID, "error": ctx.Err()})
		return false
	case rxStep := <-aChTpProcessingStep:
		if rxStep == aProcessingStep {
			return true
		}
		//all other values are not accepted - including 0 for error indication
		logger.Warnw(ctx, "Invalid processing step received: abort and force release of TpProcMutex!",
			log.Fields{"device-id": onuTP.deviceID,
				"wantedStep": aProcessingStep, "haveStep": rxStep})
		return false
	}
}

// createAniConfigFsm initializes and runs the AniConfig FSM to transfer the OMCI related commands for ANI side configuration
func (onuTP *OnuUniTechProf) createAniConfigFsm(ctx context.Context, aUniID uint8, aTpID uint8,
	apCurrentUniPort *cmn.OnuUniPort, devEvent cmn.OnuDeviceEvent, aProcessingStep uint8) error {
	logger.Debugw(ctx, "createAniConfigFsm", log.Fields{"device-id": onuTP.deviceID})
	chAniConfigFsm := make(chan cmn.Message, 2048)
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	if onuTP.onuDevice == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", onuTP.deviceID)
	}
	pAniCfgFsm := NewUniPonAniConfigFsm(ctx, onuTP.onuDevice.GetDevOmciCC(), apCurrentUniPort, onuTP,
		onuTP.onuDevice.GetOnuDB(), aTpID, devEvent,
		"AniConfigFsm", onuTP.baseDeviceHandler, onuTP.onuDevice, chAniConfigFsm)
	if pAniCfgFsm == nil {
		logger.Errorw(ctx, "AniConfigFSM could not be created - abort!!", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf("could not create AniConfigFSM: %s", onuTP.deviceID)
	}
	if onuTP.PAniConfigFsm == nil {
		onuTP.PAniConfigFsm = make(map[uniTP]*UniPonAniConfigFsm)
	}
	onuTP.PAniConfigFsm[uniTPKey] = pAniCfgFsm
	return onuTP.runAniConfigFsm(ctx, aniEvStart, aProcessingStep, aUniID, aTpID)
}

// runAniConfigFsm starts the AniConfig FSM to transfer the OMCI related commands for  ANI side configuration
func (onuTP *OnuUniTechProf) runAniConfigFsm(ctx context.Context, aEvent string, aProcessingStep uint8, aUniID uint8, aTpID uint8) error {
	/*  Uni related ANI config procedure -
	 ***** should run via 'aniConfigDone' state and generate the argument requested event *****
	 */
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}

	pACStatemachine := onuTP.PAniConfigFsm[uniTpKey].PAdaptFsm.PFsm
	if pACStatemachine != nil {
		if aEvent == aniEvStart {
			if !pACStatemachine.Is(aniStDisabled) {
				logger.Errorw(ctx, "wrong state of AniConfigFSM to start - want: Disabled", log.Fields{
					"have": pACStatemachine.Current(), "device-id": onuTP.deviceID})
				// maybe try a FSM reset and then again ... - TODO: add comment!!!
				return fmt.Errorf("wrong state of AniConfigFSM to start: %s", onuTP.deviceID)
			}
		} else if !pACStatemachine.Is(aniStConfigDone) {
			logger.Errorw(ctx, "wrong state of AniConfigFSM to remove - want: ConfigDone", log.Fields{
				"have": pACStatemachine.Current(), "device-id": onuTP.deviceID})
			return fmt.Errorf("wrong state of AniConfigFSM to remove: %s", onuTP.deviceID)
		}
		//FSM init requirement to get informed about FSM completion! (otherwise timeout of the TechProf config)
		onuTP.PAniConfigFsm[uniTpKey].setFsmCompleteChannel(onuTP.chTpConfigProcessingStep, aProcessingStep)
		if err := pACStatemachine.Event(aEvent); err != nil {
			logger.Errorw(ctx, "AniConfigFSM: can't trigger event", log.Fields{"err": err})
			return fmt.Errorf("can't trigger event in AniConfigFSM: %s", onuTP.deviceID)
		}
		/***** AniConfigFSM event notified */
		logger.Debugw(ctx, "AniConfigFSM event notified", log.Fields{
			"state": pACStatemachine.Current(), "device-id": onuTP.deviceID, "event": aEvent})
		return nil
	}
	logger.Errorw(ctx, "AniConfigFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": onuTP.deviceID})
	// maybe try a FSM reset and then again ... - TODO: add comment!!!
	return fmt.Errorf("stateMachine AniConfigFSM invalid: %s", onuTP.deviceID)
}

// clearAniSideConfig deletes internal TechProfile related data connected to the requested UniPort and TpID
func (onuTP *OnuUniTechProf) clearAniSideConfig(ctx context.Context, aUniID uint8, aTpID uint8) {
	logger.Debugw(ctx, "removing TpIndication and PonAniConfig data", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID})
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}

	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	//deleting a map entry should be safe, even if not existing
	delete(onuTP.mapUniTpIndication, uniTpKey)
	delete(onuTP.mapPonAniConfig, uniTpKey)
	delete(onuTP.procResult, uniTpKey)
	delete(onuTP.tpProfileExists, uniTpKey)
	delete(onuTP.tpProfileResetting, uniTpKey)
}

// setConfigDone sets the requested techProfile config state (if possible)
func (onuTP *OnuUniTechProf) setConfigDone(aUniID uint8, aTpID uint8, aState bool) {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		onuTP.mapUniTpIndication[uniTpKey].techProfileConfigDone = aState
	} //else: the state is just ignored (does not exist)
}

// getTechProfileDone checks if the Techprofile processing with the requested TechProfile ID was done
func (onuTP *OnuUniTechProf) getTechProfileDone(ctx context.Context, aUniID uint8, aTpID uint8) bool {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		if onuTP.mapUniTpIndication[uniTpKey].techProfileID == aTpID {
			if onuTP.mapUniTpIndication[uniTpKey].techProfileToDelete {
				logger.Debugw(ctx, "TechProfile not relevant for requested flow config - waiting on delete",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				return false //still waiting for removal of this techProfile first
			}
			return onuTP.mapUniTpIndication[uniTpKey].techProfileConfigDone
		}
	}
	//for all other constellations indicate false = Config not done
	return false
}

// SetProfileToDelete sets the requested techProfile toDelete state (if possible)
func (onuTP *OnuUniTechProf) SetProfileToDelete(aUniID uint8, aTpID uint8, aState bool) {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		onuTP.mapUniTpIndication[uniTpKey].techProfileToDelete = aState
	} //else: the state is just ignored (does not exist)
}

func (onuTP *OnuUniTechProf) getMulticastGemPorts(ctx context.Context, aUniID uint8, aTpID uint8) []uint16 {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	gemPortIds := make([]uint16, 0)
	if techProfile, existTP := onuTP.mapPonAniConfig[uniTpKey]; existTP {
		for _, gemPortParam := range techProfile.mapGemPortParams {
			if gemPortParam.isMulticast {
				logger.Debugw(ctx, "Detected multicast gemPort", log.Fields{"device-id": onuTP.deviceID,
					"aUniID": aUniID, "aTPID": aTpID, "uniTPKey": uniTpKey,
					"mcastGemId": gemPortParam.multicastGemPortID})
				gemPortIds = append(gemPortIds, gemPortParam.multicastGemPortID)
			}
		}
	} //else: the state is just ignored (does not exist)
	return gemPortIds
}

func (onuTP *OnuUniTechProf) getBidirectionalGemPortIDsForTP(ctx context.Context, aUniID uint8, aTpID uint8) []uint16 {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	gemPortIds := make([]uint16, 0)
	if techProfile, existTP := onuTP.mapPonAniConfig[uniTpKey]; existTP {
		logger.Debugw(ctx, "TechProfile exist", log.Fields{"device-id": onuTP.deviceID})
		for _, gemPortParam := range techProfile.mapGemPortParams {
			if !gemPortParam.isMulticast {
				logger.Debugw(ctx, "Detected unicast gemPort", log.Fields{"device-id": onuTP.deviceID,
					"aUniID": aUniID, "aTPID": aTpID, "uniTPKey": uniTpKey,
					"GemId": gemPortParam.multicastGemPortID})
				gemPortIds = append(gemPortIds, gemPortParam.gemPortID)
			}
		}
	} else {
		logger.Debugw(ctx, "TechProfile doesn't exist", log.Fields{"device-id": onuTP.deviceID})
	} //else: the state is just ignored (does not exist)
	logger.Debugw(ctx, "Gem PortID list", log.Fields{"device-id": onuTP.deviceID, "gemportList": gemPortIds})
	return gemPortIds
}

// GetAllBidirectionalGemPortIDsForOnu - TODO: add comment
func (onuTP *OnuUniTechProf) GetAllBidirectionalGemPortIDsForOnu() []uint16 {
	var gemPortInstIDs []uint16
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	for _, tcontGemList := range onuTP.mapPonAniConfig {
		for gemPortID, gemPortData := range tcontGemList.mapGemPortParams {
			if gemPortData != nil && !gemPortData.isMulticast { // only if not multicast gem port
				gemPortInstIDs = append(gemPortInstIDs, gemPortID)
			}
		}
	}
	return gemPortInstIDs
}

// setProfileResetting sets/resets the indication, that a reset of the TechProfileConfig/Removal is ongoing
func (onuTP *OnuUniTechProf) setProfileResetting(ctx context.Context, aUniID uint8, aTpID uint8, aState bool) {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	onuTP.tpProfileResetting[uniTpKey] = aState
}

// getProfileResetting returns true, if the the according indication for started reset procedure is set
func (onuTP *OnuUniTechProf) getProfileResetting(aUniTpKey uniTP) bool {
	onuTP.mutexTPState.RLock()
	defer onuTP.mutexTPState.RUnlock()
	if isResetting, exist := onuTP.tpProfileResetting[aUniTpKey]; exist {
		return isResetting
	}
	return false
}

// PrepareForGarbageCollection - remove references to prepare for garbage collection
func (onuTP *OnuUniTechProf) PrepareForGarbageCollection(ctx context.Context, aDeviceID string) {
	logger.Debugw(ctx, "prepare for garbage collection", log.Fields{"device-id": aDeviceID})
	onuTP.baseDeviceHandler = nil
	onuTP.onuDevice = nil
	for k, v := range onuTP.PAniConfigFsm {
		v.PrepareForGarbageCollection(ctx, aDeviceID)
		delete(onuTP.PAniConfigFsm, k)
	}
}
