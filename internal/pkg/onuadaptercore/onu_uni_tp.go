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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	tp "github.com/opencord/voltha-lib-go/v3/pkg/techprofile"
)

const cBasePathTechProfileKVStore = "%s/technology_profiles"

//definitions for TechProfileProcessing - copied from OltAdapter:openolt_flowmgr.go
//  could perhaps be defined more globally
const (
	// binaryStringPrefix is binary string prefix
	binaryStringPrefix = "0b"
	// binaryBit1 is binary bit 1 expressed as a character
	//binaryBit1 = '1'
)

type resourceEntry int

const (
	cResourceGemPort resourceEntry = 1
	cResourceTcont   resourceEntry = 2
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
	removeIndex      uint16
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

//onuUniTechProf structure holds information about the TechProfiles attached to Uni Ports of the ONU
type onuUniTechProf struct {
	baseDeviceHandler        *deviceHandler
	deviceID                 string
	tpProcMutex              sync.RWMutex
	techProfileKVStore       *db.Backend
	chTpConfigProcessingStep chan uint8
	mapUniTpIndication       map[uniTP]*tTechProfileIndication //use pointer values to ease assignments to the map
	mapPonAniConfig          map[uniTP]*tcontGemList           //per UNI: use pointer values to ease assignments to the map
	pAniConfigFsm            map[uniTP]*uniPonAniConfigFsm
	procResult               map[uniTP]error //error indication of processing
	mutexTPState             sync.Mutex
	tpProfileExists          map[uniTP]bool
	mapRemoveGemEntry        map[uniTP]*gemPortParamStruct //per UNI: pointer to GemEntry to be removed
}

//newOnuUniTechProf returns the instance of a OnuUniTechProf
//(one instance per ONU/deviceHandler for all possible UNI's)
func newOnuUniTechProf(ctx context.Context, aDeviceHandler *deviceHandler) *onuUniTechProf {
	logger.Debugw("init-OnuUniTechProf", log.Fields{"device-id": aDeviceHandler.deviceID})
	var onuTP onuUniTechProf
	onuTP.baseDeviceHandler = aDeviceHandler
	onuTP.deviceID = aDeviceHandler.deviceID
	onuTP.tpProcMutex = sync.RWMutex{}
	onuTP.chTpConfigProcessingStep = make(chan uint8)
	onuTP.mapUniTpIndication = make(map[uniTP]*tTechProfileIndication)
	onuTP.mapPonAniConfig = make(map[uniTP]*tcontGemList)
	onuTP.procResult = make(map[uniTP]error)
	onuTP.tpProfileExists = make(map[uniTP]bool)
	onuTP.mapRemoveGemEntry = make(map[uniTP]*gemPortParamStruct)
	baseKvStorePath := fmt.Sprintf(cBasePathTechProfileKVStore, aDeviceHandler.pOpenOnuAc.cm.Backend.PathPrefix)
	onuTP.techProfileKVStore = aDeviceHandler.setBackend(baseKvStorePath)
	if onuTP.techProfileKVStore == nil {
		logger.Errorw("Can't access techProfileKVStore - no backend connection to service",
			log.Fields{"device-id": aDeviceHandler.deviceID, "service": baseKvStorePath})
	}

	return &onuTP
}

// lockTpProcMutex locks OnuUniTechProf processing mutex
func (onuTP *onuUniTechProf) lockTpProcMutex() {
	onuTP.tpProcMutex.Lock()
}

// unlockTpProcMutex unlocks OnuUniTechProf processing mutex
func (onuTP *onuUniTechProf) unlockTpProcMutex() {
	onuTP.tpProcMutex.Unlock()
}

// resetTpProcessingErrorIndication resets the internal error indication
// need to be called before evaluation of any subsequent processing (given by waitForTpCompletion())
func (onuTP *onuUniTechProf) resetTpProcessingErrorIndication(aUniID uint8, aTpID uint8) {
	onuTP.procResult[uniTP{uniID: aUniID, tpID: aTpID}] = nil
}

func (onuTP *onuUniTechProf) getTpProcessingErrorIndication(aUniID uint8, aTpID uint8) error {
	return onuTP.procResult[uniTP{uniID: aUniID, tpID: aTpID}]
}

// configureUniTp checks existing tp resources to configure and starts the corresponding OMCI configuation of the UNI port
// all possibly blocking processing must be run in background to allow for deadline supervision!
// but take care on sequential background processing when needed (logical dependencies)
//   use waitForTimeoutOrCompletion(ctx, chTpConfigProcessingStep, processingStep) for internal synchronization
func (onuTP *onuUniTechProf) configureUniTp(ctx context.Context,
	aUniID uint8, aPathString string, wg *sync.WaitGroup) {
	defer wg.Done() //always decrement the waitGroup on return
	logger.Debugw("configure the Uni according to TpPath", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString})
	tpID, err := GetTpIDFromTpPath(aPathString)
	uniTpKey := uniTP{uniID: aUniID, tpID: tpID}
	if err != nil {
		logger.Errorw("error-extracting-tp-id-from-tp-path", log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString})
		return
	}
	if onuTP.techProfileKVStore == nil {
		logger.Errorw("techProfileKVStore not set - abort",
			log.Fields{"device-id": onuTP.deviceID})
		onuTP.procResult[uniTpKey] = errors.New("techProfile config aborted: techProfileKVStore not set")
		return
	}

	//ensure that the given uniID is available (configured) in the UniPort class (used for OMCI entities)
	var pCurrentUniPort *onuUniPort
	for _, uniPort := range onuTP.baseDeviceHandler.uniEntityMap {
		// only if this port is validated for operState transfer
		if uniPort.uniID == aUniID {
			pCurrentUniPort = uniPort
			break //found - end search loop
		}
	}
	if pCurrentUniPort == nil {
		logger.Errorw("TechProfile configuration aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: requested uniID not found %d on %s",
			aUniID, onuTP.deviceID)
		return
	}

	var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep

	//according to updateOnuUniTpPath() logic the assumption here is, that this configuration is only called
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
	go onuTP.readAniSideConfigFromTechProfile(ctx, aUniID, tpID, aPathString, processingStep)
	if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
		//timeout or error detected
		if onuTP.tpProfileExists[uniTpKey] {
			//ignore the internal error in case the new profile is already configured
			// and abort the processing here
			return
		}
		logger.Errorw("tech-profile related configuration aborted on read",
			log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: tech-profile read issue for %d on %s",
			aUniID, onuTP.deviceID)
		return
	}

	processingStep++

	valuePA, existPA := onuTP.mapPonAniConfig[uniTpKey]

	if existPA {
		if valuePA != nil {
			//Config data for this uni and and at least TCont Index 0 exist
			if err := onuTP.setAniSideConfigFromTechProfile(ctx, aUniID, tpID, pCurrentUniPort, processingStep); err != nil {
				logger.Errorw("tech-profile related FSM could not be started",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				onuTP.procResult[uniTpKey] = err
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				logger.Errorw("tech-profile related configuration aborted on set",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})

				onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: Omci AniSideConfig failed %d on %s",
					aUniID, onuTP.deviceID)
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				_ = onuTP.pAniConfigFsm[uniTpKey].pAdaptFsm.pFsm.Event(aniEvReset)
				return
			}
		} else {
			// strange: UNI entry exists, but no ANI data, maybe such situation should be cleared up (if observed)
			logger.Errorw("no Tcont/Gem data for this UNI found - abort", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID})

			onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: no Tcont/Gem data found for this UNI %d on %s",
				aUniID, onuTP.deviceID)
			return
		}
	} else {
		logger.Errorw("no PonAni data for this UNI found - abort", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID})

		onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: no AniSide data found for this UNI %d on %s",
			aUniID, onuTP.deviceID)
		return
	}
}

/* internal methods *********************/

func (onuTP *onuUniTechProf) readAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aTpID uint8, aPathString string, aProcessingStep uint8) {
	var tpInst tp.TechProfile

	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}

	onuTP.tpProfileExists[uniTP{uniID: aUniID, tpID: aTpID}] = false

	//store profile type and identifier for later usage within the OMCI identifier and possibly ME setup
	//pathstring is defined to be in the form of <ProfType>/<profID>/<Interface/../Identifier>
	subStringSlice := strings.Split(aPathString, "/")
	if len(subStringSlice) <= 2 {
		logger.Errorw("invalid path name format",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}

	//at this point it is assumed that a new TechProfile is assigned to the UNI
	//expectation is that no TPIndication entry exists here, if exists and with the same TPId
	//  then we throw a warning, set an internal error and abort with error,
	//  which is later re-defined to success response to OLT adapter
	//  if TPId has changed, current data is removed (note that the ONU config state may be
	// 	  ambivalent in such a case)
	if _, existTP := onuTP.mapUniTpIndication[uniTPKey]; existTP {
		logger.Warnw("Some active profile entry at reading new TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID,
				"uni-id": aUniID, "wrongProfile": onuTP.mapUniTpIndication[uniTPKey].techProfileID})
		if aTpID == onuTP.mapUniTpIndication[uniTPKey].techProfileID {
			// ProfId not changed - assume profile to be still the same
			// anyway this should not appear after full support of profile (Gem/TCont) removal
			logger.Warnw("New TechProfile already exists - aborting configuration",
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
	logger.Debugw("tech-profile path indications",
		log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID,
			"profType": onuTP.mapUniTpIndication[uniTPKey].techProfileType,
			"profID":   onuTP.mapUniTpIndication[uniTPKey].techProfileID})

	Value, err := onuTP.techProfileKVStore.Get(ctx, aPathString)
	if err == nil {
		if Value != nil {
			logger.Debugw("tech-profile read",
				log.Fields{"Key": Value.Key, "device-id": onuTP.deviceID})
			tpTmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tpTmpBytes, &tpInst); err != nil {
				logger.Errorw("TechProf - Failed to unmarshal tech-profile into tpInst",
					log.Fields{"error": err, "device-id": onuTP.deviceID})
				onuTP.chTpConfigProcessingStep <- 0 //error indication
				return
			}
			logger.Debugw("TechProf - tpInst", log.Fields{"tpInst": tpInst})
			// access examples
			logger.Debugw("TechProf content", log.Fields{"Name": tpInst.Name,
				"MaxGemPayloadSize":                tpInst.InstanceCtrl.MaxGemPayloadSize,
				"DownstreamGemDiscardmaxThreshold": tpInst.DownstreamGemPortAttributeList[0].DiscardConfig.MaxThreshold})
		} else {
			logger.Errorw("No tech-profile found",
				log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
	} else {
		logger.Errorw("kvstore-get failed for path",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}

	//default start with 1Tcont profile, later perhaps extend to MultiTcontMultiGem
	localMapGemPortParams := make(map[uint16]*gemPortParamStruct)
	localMapGemPortParams[0] = &gemPortParamStruct{}
	onuTP.mapPonAniConfig[uniTPKey] = &tcontGemList{tcontParamStruct{}, localMapGemPortParams}

	//note: the code is currently restricted to one TCcont per Onu (index [0])
	//get the relevant values from the profile and store to mapPonAniConfig
	onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID = uint16(tpInst.UsScheduler.AllocID)
	//maybe tCont scheduling not (yet) needed - just to basically have it for future
	//  (would only be relevant in case of ONU-2G QOS configuration flexibility)
	if tpInst.UsScheduler.QSchedPolicy == "StrictPrio" {
		onuTP.mapPonAniConfig[uniTPKey].tcontParams.schedPolicy = 1 //for the moment fixed value acc. G.988 //TODO: defines!
	} else {
		//default profile defines "Hybrid" - which probably comes down to WRR with some weigthts for SP
		onuTP.mapPonAniConfig[uniTPKey].tcontParams.schedPolicy = 2 //for G.988 WRR
	}
	loNumGemPorts := tpInst.NumGemPorts
	loGemPortRead := false
	for pos, content := range tpInst.UpstreamGemPortAttributeList {
		if uint32(pos) == loNumGemPorts {
			logger.Debugw("PonAniConfig abort GemPortList - GemList exceeds set NumberOfGemPorts",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "NumGem": loNumGemPorts})
			break
		}
		if pos == 0 {
			//at least one upstream GemPort should always exist (else traffic profile makes no sense)
			loGemPortRead = true
		} else {
			//for all further GemPorts we need to extend the mapGemPortParams
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)] = &gemPortParamStruct{}
		}
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].gemPortID =
			uint16(content.GemportID)
		//direction can be correlated later with Downstream list,
		//  for now just assume bidirectional (upstream never exists alone)
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].direction = 3 //as defined in G.988
		// expected Prio-Queue values 0..7 with 7 for highest PrioQueue, QueueIndex=Prio = 0..7
		if 7 < content.PriorityQueue {
			logger.Errorw("PonAniConfig reject on GemPortList - PrioQueue value invalid",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "PrioQueue": content.PriorityQueue})
			//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
			delete(onuTP.mapPonAniConfig, uniTPKey)
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].prioQueueIndex =
			uint8(content.PriorityQueue)
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].pbitString =
			strings.TrimPrefix(content.PbitMap, binaryStringPrefix)
		if content.AesEncryption == "True" {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].gemPortEncState = 1
		} else {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].gemPortEncState = 0
		}
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].discardPolicy =
			content.DiscardPolicy
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].queueSchedPolicy =
			content.SchedulingPolicy
		//'GemWeight' looks strange in default profile, for now we just copy the weight to first queue
		onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].queueWeight =
			uint8(content.Weight)
	}
	if !loGemPortRead {
		logger.Errorw("PonAniConfig reject - no GemPort could be read from TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
		delete(onuTP.mapPonAniConfig, uniTPKey)
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}
	//TODO!! MC (downstream) GemPorts can be set using DownstreamGemPortAttributeList separately

	//logger does not simply output the given structures, just give some example debug values
	logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID,
		"AllocId": onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID})
	for gemIndex, gemEntry := range onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams {
		logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
			"GemIndex":        gemIndex,
			"GemPort":         gemEntry.gemPortID,
			"QueueScheduling": gemEntry.queueSchedPolicy})
	}

	onuTP.chTpConfigProcessingStep <- aProcessingStep //done
}

func (onuTP *onuUniTechProf) setAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aTpID uint8, apCurrentUniPort *onuUniPort, aProcessingStep uint8) error {

	//OMCI transfer of ANI data acc. to mapPonAniConfig
	// also the FSM's are running in background,
	//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	if onuTP.pAniConfigFsm == nil {
		return onuTP.createAniConfigFsm(aUniID, aTpID, apCurrentUniPort, OmciAniConfigDone, aProcessingStep)
	} else if _, ok := onuTP.pAniConfigFsm[uniTPKey]; !ok {
		return onuTP.createAniConfigFsm(aUniID, aTpID, apCurrentUniPort, OmciAniConfigDone, aProcessingStep)
	}
	//AniConfigFsm already init
	return onuTP.runAniConfigFsm(aniEvStart, aProcessingStep, aUniID, aTpID)
}

// deleteTpResource removes Resources from the ONU's specified Uni
func (onuTP *onuUniTechProf) deleteTpResource(ctx context.Context,
	aUniID uint8, aTpID uint8, aPathString string, aResource resourceEntry, aEntryID uint32,
	wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("will remove TP resources from ONU's UNI", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "Resource": aResource})
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}

	if cResourceGemPort == aResource {
		logger.Debugw("remove GemPort from the list of existing ones of the TP", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "GemPort": aEntryID})

		// check if the requested GemPort exists in the DB, indicate it to the FSM
		// store locally to remove it from DB later on success
		pLocAniConfigOnUni := onuTP.mapPonAniConfig[uniTPKey]
		if pLocAniConfigOnUni == nil {
			// No relevant entry exists anymore - acknowledge success
			logger.Debugw("AniConfig or GemEntry do not exists in DB", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			return
		}
		for gemIndex, gemEntry := range pLocAniConfigOnUni.mapGemPortParams {
			if gemEntry.gemPortID == uint16(aEntryID) {
				//GemEntry to be deleted found
				gemEntry.removeIndex = gemIndex //store the index for later removal
				onuTP.mapRemoveGemEntry[uniTPKey] = pLocAniConfigOnUni.mapGemPortParams[gemIndex]
				logger.Debugw("Remove-GemEntry stored", log.Fields{
					"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "GemIndex": gemIndex})
				break //abort loop, always only one GemPort to remove
			}
		}
		if onuTP.mapRemoveGemEntry[uniTPKey] == nil {
			logger.Errorw("GemPort removal aborted - GemPort not found",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "GemPort": aEntryID})
			/* Do not set some error indication to the outside system interface on delete
			   assume there is nothing to be deleted internally and hope a new config request will recover the situation
			 onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: GemPort not found %d for %d on %s",
				aEntryID, aUniID, onuTP.deviceID)
			*/
			return
		}
		if onuTP.baseDeviceHandler.ReadyForSpecificOmciConfig {
			// check that the TpConfigRequest was done before
			//   -> that is implicitly done using the AniConfigFsm,
			//      which must be in the according state to remove something
			// initiate OMCI GemPort related removal
			var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep
			//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
			if onuTP.pAniConfigFsm == nil {
				logger.Errorw("abort GemPort removal - no AniConfigFsm available",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				/* Do not set some error indication to the outside system interface on delete (see above)
				onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: no AniConfigFsm available %d on %s",
					aUniID, onuTP.deviceID)
				*/
				//if the FSM is not valid, also TP related remove data should not be valid:
				// remove GemPort from config DB
				delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeIndex)
				// remove the removeEntry
				delete(onuTP.mapRemoveGemEntry, uniTPKey)
				return
			}
			if _, ok := onuTP.pAniConfigFsm[uniTPKey]; !ok {
				logger.Errorw("abort GemPort removal - no AniConfigFsm available for this uni/tp",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
				/* Do not set some error indication to the outside system interface on delete (see above)
				onuTP.procResult[uniTpKey] = fmt.Errorf("GemPort removal aborted: no AniConfigFsm available %d on %s for tpid",
					aUniID, onuTP.deviceID, aTpID)
				*/
				//if the FSM is not valid, also TP related remove data should not be valid:
				// remove GemPort from config DB
				delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeIndex)
				// remove the removeEntry
				delete(onuTP.mapRemoveGemEntry, uniTPKey)
				return
			}
			if nil != onuTP.runAniConfigFsm(aniEvRemGemiw, processingStep, aUniID, aTpID) {
				//even if the FSM invocation did not work we don't indicate a problem within procResult
				//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				logger.Errorw("GemPort removal aborted - Omci AniSideConfig failed",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				//even if the FSM delete execution did not work we don't indicate a problem within procResult
				//we should never respond to delete with error ...
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				_ = onuTP.pAniConfigFsm[uniTPKey].pAdaptFsm.pFsm.Event(aniEvReset)
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
		} else {
			logger.Debugw("uniPonAniConfigFsm delete Gem on OMCI skipped based on device state", log.Fields{
				"device-id": onuTP.deviceID, "device-state": onuTP.baseDeviceHandler.deviceReason})
		}
		// remove GemPort from config DB
		delete(onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams, onuTP.mapRemoveGemEntry[uniTPKey].removeIndex)
		// remove the removeEntry
		delete(onuTP.mapRemoveGemEntry, uniTPKey)
		//  deviceHandler StatusEvent (reason update) (see end of function) is only generated in case some element really was removed
	} else { //if cResourceTcont == aResource {
		logger.Debugw("reset TCont with AllocId", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString, "allocId": aEntryID})

		// check if the TCont with the indicated AllocId exists in the DB, indicate its EntityId to the FSM
		pLocAniConfigOnUni := onuTP.mapPonAniConfig[uniTPKey]
		if pLocAniConfigOnUni == nil {
			// No relevant entry exists anymore - acknowledge success
			logger.Debugw("AniConfig or TCont entry do not exists in DB", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			return
		}
		if pLocAniConfigOnUni.tcontParams.allocID != uint16(aEntryID) {
			logger.Errorw("TCont removal aborted - indicated AllocId not found",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID, "AllocId": aEntryID})
			/* Do not set some error indication to the outside system interface on delete
			   assume there is nothing to be deleted internally and hope a new config request will recover the situation
			 onuTP.procResult[uniTpKey] = fmt.Errorf("TCont removal aborted: AllocId not found %d for %d on %s",
				aEntryID, aUniID, onuTP.deviceID)
			*/
			return
		}
		//T-Cont to be reset found
		logger.Debugw("Reset-T-Cont AllocId found - valid", log.Fields{
			"device-id": onuTP.deviceID, "uni-id": aUniID, "AllocId": aEntryID})
		if onuTP.pAniConfigFsm == nil {
			logger.Errorw("no TCont removal on OMCI - no AniConfigFsm available",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
			/* Do not set some error indication to the outside system interface on delete (see above)
			onuTP.procResult[uniTpKey] = fmt.Errorf("TCont cleanup aborted: no AniConfigFsm available %d on %s",
				aUniID, onuTP.deviceID)
			*/
			//if the FSM is not valid, also TP related data should not be valid - clear the internal store profile data
			onuTP.clearAniSideConfig(aUniID, aTpID)
			return
		}
		if _, ok := onuTP.pAniConfigFsm[uniTPKey]; !ok {
			logger.Errorw("no TCont removal on OMCI - no AniConfigFsm available for this uni/tp",
				log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "tp-id": aTpID})
			//even if the FSM invocation did not work we don't indicate a problem within procResult
			//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
			//if the FSM is not valid, also TP related data should not be valid - clear the internal store profile data
			onuTP.clearAniSideConfig(aUniID, aTpID)
			return
		}
		if onuTP.baseDeviceHandler.ReadyForSpecificOmciConfig {
			// check that the TpConfigRequest was done before
			//   -> that is implicitly done using the AniConfigFsm,
			//      which must be in the according state to remove something
			// initiate OMCI TCont related cleanup
			var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep
			//   hence we have to make sure they indicate 'success' on chTpConfigProcessingStep with aProcessingStep
			if nil != onuTP.runAniConfigFsm(aniEvRemTcontPath, processingStep, aUniID, aTpID) {
				//even if the FSM invocation did not work we don't indicate a problem within procResult
				//errors could exist also because there was nothing to delete - so we just accept that as 'deleted'
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				logger.Errorw("TCont cleanup aborted - Omci AniSideConfig failed",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				//even if the FSM delete execution did not work we don't indicate a problem within procResult
				//we should never respond to delete with error ...
				//this issue here means that the AniConfigFsm has not finished successfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				_ = onuTP.pAniConfigFsm[uniTPKey].pAdaptFsm.pFsm.Event(aniEvReset)
				//TP related data cleared by FSM error treatment or re-used by FSM error-recovery (if implemented)
				return
			}
		} else {
			logger.Debugw("uniPonAniConfigFsm TCont cleanup on OMCI skipped based on device state", log.Fields{
				"device-id": onuTP.deviceID, "device-state": onuTP.baseDeviceHandler.deviceReason})
		}
		//clear the internal store profile data
		onuTP.clearAniSideConfig(aUniID, aTpID)
		// reset also the FSM in order to admit a new OMCI configuration in case a new profile is created
		// FSM stop maybe encapsulated as OnuTP method - perhaps later in context of module splitting
		_ = onuTP.pAniConfigFsm[uniTPKey].pAdaptFsm.pFsm.Event(aniEvReset)
	}
	// generate deviceHandler StatusEvent in case the FSM was not invoked
	go onuTP.baseDeviceHandler.deviceProcStatusUpdate(OmciAniResourceRemoved)
}

func (onuTP *onuUniTechProf) waitForTimeoutOrCompletion(
	ctx context.Context, aChTpProcessingStep <-chan uint8, aProcessingStep uint8) bool {
	select {
	case <-ctx.Done():
		logger.Warnw("processing not completed in-time: force release of TpProcMutex!",
			log.Fields{"device-id": onuTP.deviceID, "error": ctx.Err()})
		return false
	case rxStep := <-aChTpProcessingStep:
		if rxStep == aProcessingStep {
			return true
		}
		//all other values are not accepted - including 0 for error indication
		logger.Warnw("Invalid processing step received: abort and force release of TpProcMutex!",
			log.Fields{"device-id": onuTP.deviceID,
				"wantedStep": aProcessingStep, "haveStep": rxStep})
		return false
	}
}

// createUniLockFsm initializes and runs the AniConfig FSM to transfer the OMCI related commands for ANI side configuration
func (onuTP *onuUniTechProf) createAniConfigFsm(aUniID uint8, aTpID uint8,
	apCurrentUniPort *onuUniPort, devEvent OnuDeviceEvent, aProcessingStep uint8) error {
	logger.Debugw("createAniConfigFsm", log.Fields{"device-id": onuTP.deviceID})
	chAniConfigFsm := make(chan Message, 2048)
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	pDevEntry := onuTP.baseDeviceHandler.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", onuTP.deviceID)
	}
	pAniCfgFsm := newUniPonAniConfigFsm(pDevEntry.PDevOmciCC, apCurrentUniPort, onuTP,
		pDevEntry.pOnuDB, aTpID, devEvent,
		"AniConfigFsm", onuTP.baseDeviceHandler, chAniConfigFsm)
	if pAniCfgFsm == nil {
		logger.Errorw("AniConfigFSM could not be created - abort!!", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf("could not create AniConfigFSM: %s", onuTP.deviceID)
	}
	if onuTP.pAniConfigFsm == nil {
		onuTP.pAniConfigFsm = make(map[uniTP]*uniPonAniConfigFsm)
	}
	onuTP.pAniConfigFsm[uniTPKey] = pAniCfgFsm
	return onuTP.runAniConfigFsm(aniEvStart, aProcessingStep, aUniID, aTpID)
}

// runAniConfigFsm starts the AniConfig FSM to transfer the OMCI related commands for  ANI side configuration
func (onuTP *onuUniTechProf) runAniConfigFsm(aEvent string, aProcessingStep uint8, aUniID uint8, aTpID uint8) error {
	/*  Uni related ANI config procedure -
	 ***** should run via 'aniConfigDone' state and generate the argument requested event *****
	 */
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}

	pACStatemachine := onuTP.pAniConfigFsm[uniTpKey].pAdaptFsm.pFsm
	if pACStatemachine != nil {
		if aEvent == aniEvStart {
			if !pACStatemachine.Is(aniStDisabled) {
				logger.Errorw("wrong state of AniConfigFSM to start - want: Disabled", log.Fields{
					"have": pACStatemachine.Current(), "device-id": onuTP.deviceID})
				// maybe try a FSM reset and then again ... - TODO!!!
				return fmt.Errorf("wrong state of AniConfigFSM to start: %s", onuTP.deviceID)
			}
		} else if !pACStatemachine.Is(aniStConfigDone) {
			logger.Errorw("wrong state of AniConfigFSM to remove - want: ConfigDone", log.Fields{
				"have": pACStatemachine.Current(), "device-id": onuTP.deviceID})
			return fmt.Errorf("wrong state of AniConfigFSM to remove: %s", onuTP.deviceID)
		}
		//FSM init requirement to get informed about FSM completion! (otherwise timeout of the TechProf config)
		onuTP.pAniConfigFsm[uniTpKey].setFsmCompleteChannel(onuTP.chTpConfigProcessingStep, aProcessingStep)
		if err := pACStatemachine.Event(aEvent); err != nil {
			logger.Errorw("AniConfigFSM: can't trigger event", log.Fields{"err": err})
			return fmt.Errorf("can't trigger event in AniConfigFSM: %s", onuTP.deviceID)
		}
		/***** AniConfigFSM event notified */
		logger.Debugw("AniConfigFSM event notified", log.Fields{
			"state": pACStatemachine.Current(), "device-id": onuTP.deviceID, "event": aEvent})
		return nil
	}
	logger.Errorw("AniConfigFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": onuTP.deviceID})
	// maybe try a FSM reset and then again ... - TODO!!!
	return fmt.Errorf("stateMachine AniConfigFSM invalid: %s", onuTP.deviceID)
}

// clearAniSideConfig deletes internal TechProfile related data connected to the requested UniPort and TpID
func (onuTP *onuUniTechProf) clearAniSideConfig(aUniID uint8, aTpID uint8) {
	logger.Debugw("removing TpIndication and PonAniConfig data", log.Fields{
		"device-id": onuTP.deviceID, "uni-id": aUniID})
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}

	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	delete(onuTP.mapUniTpIndication, uniTpKey)
	//delete on the PonAniConfig map of this UNI should be safe, even if not existing
	delete(onuTP.mapPonAniConfig, uniTpKey)
}

// setConfigDone sets the requested techProfile config state (if possible)
func (onuTP *onuUniTechProf) setConfigDone(aUniID uint8, aTpID uint8, aState bool) {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		onuTP.mapUniTpIndication[uniTpKey].techProfileConfigDone = aState
	} //else: the state is just ignored (does not exist)
}

// getTechProfileDone checks if the Techprofile processing with the requested TechProfile ID was done
func (onuTP *onuUniTechProf) getTechProfileDone(aUniID uint8, aTpID uint8) bool {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		if onuTP.mapUniTpIndication[uniTpKey].techProfileID == aTpID {
			if onuTP.mapUniTpIndication[uniTpKey].techProfileToDelete {
				logger.Debugw("TechProfile not relevant for requested flow config - waiting on delete",
					log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID})
				return false //still waiting for removal of this techProfile first
			}
			return onuTP.mapUniTpIndication[uniTpKey].techProfileConfigDone
		}
	}
	//for all other constellations indicate false = Config not done
	return false
}

// setProfileToDelete sets the requested techProfile toDelete state (if possible)
func (onuTP *onuUniTechProf) setProfileToDelete(aUniID uint8, aTpID uint8, aState bool) {
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}
	onuTP.mutexTPState.Lock()
	defer onuTP.mutexTPState.Unlock()
	if _, existTP := onuTP.mapUniTpIndication[uniTpKey]; existTP {
		onuTP.mapUniTpIndication[uniTpKey].techProfileToDelete = aState
	} //else: the state is just ignored (does not exist)
}
