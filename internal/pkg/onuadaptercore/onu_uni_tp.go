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

const cBasePathTechProfileKVStore = "service/voltha/technology_profiles"

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
	isMulticast      string
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
}

//newOnuUniTechProf returns the instance of a OnuUniTechProf
//(one instance per ONU/deviceHandler for all possible UNI's)
func newOnuUniTechProf(ctx context.Context, aDeviceHandler *deviceHandler) *onuUniTechProf {
	logger.Infow("init-OnuUniTechProf", log.Fields{"device-id": aDeviceHandler.deviceID})
	var onuTP onuUniTechProf
	onuTP.baseDeviceHandler = aDeviceHandler
	onuTP.deviceID = aDeviceHandler.deviceID
	onuTP.tpProcMutex = sync.RWMutex{}
	onuTP.chTpConfigProcessingStep = make(chan uint8)
	onuTP.mapUniTpIndication = make(map[uniTP]*tTechProfileIndication)
	onuTP.mapPonAniConfig = make(map[uniTP]*tcontGemList)
	onuTP.procResult = make(map[uniTP]error)
	onuTP.tpProfileExists = make(map[uniTP]bool)
	onuTP.techProfileKVStore = aDeviceHandler.setBackend(cBasePathTechProfileKVStore)
	if onuTP.techProfileKVStore == nil {
		logger.Errorw("Can't access techProfileKVStore - no backend connection to service",
			log.Fields{"device-id": aDeviceHandler.deviceID, "service": cBasePathTechProfileKVStore})
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

// configureUniTp checks existing tp resources to delete and starts the corresponding OMCI configuation of the UNI port
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
		logger.Debug("techProfileKVStore not set - abort")
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
		logger.Debugw("tech-profile related configuration aborted on read",
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
			go onuTP.setAniSideConfigFromTechProfile(ctx, aUniID, tpID, pCurrentUniPort, processingStep)
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				logger.Debugw("tech-profile related configuration aborted on set",
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
			logger.Debugw("no Tcont/Gem data for this UNI found - abort", log.Fields{
				"device-id": onuTP.deviceID, "uni-id": aUniID})

			onuTP.procResult[uniTpKey] = fmt.Errorf("techProfile config aborted: no Tcont/Gem data found for this UNI %d on %s",
				aUniID, onuTP.deviceID)
			return
		}
	} else {
		logger.Debugw("no PonAni data for this UNI found - abort", log.Fields{
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

		if content.IsMulticast == "True" {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].gemPortID =
				uint16(content.McastGemID)
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].direction = 2 // for ANI to UNI as defined in G.988
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].isMulticast = content.IsMulticast
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].multicastGemPortID = uint16(content.McastGemID)
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].staticACL = content.SControlList
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].dynamicACL = content.DControlList

		} else {
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].gemPortID =
				uint16(content.GemportID)
			//direction can be correlated later with Downstream list,
			//  for now just assume bidirectional (upstream never exists alone)
			onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams[uint16(pos)].direction = 3 //as defined in G.988
		}

		// expected Prio-Queue values 0..7 with 7 for highest PrioQueue, QueueIndex=Prio = 0..7
		if content.PriorityQueue > 7 {
			logger.Errorw("PonAniConfig reject on GemPortList - PrioQueue value invalid",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "PrioQueue": content.PriorityQueue})
			//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
			delete(onuTP.mapPonAniConfig, uniTPKey)
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
		//TODO possibly these are superfluous for Multicast, need to check.
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
	//logger does not simply output the given structures, just give some example debug values
	logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
		"device-id": onuTP.deviceID,
		"AllocId":   onuTP.mapPonAniConfig[uniTPKey].tcontParams.allocID})
	for gemIndex, gemEntry := range onuTP.mapPonAniConfig[uniTPKey].mapGemPortParams {
		logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
			"GemIndex":        gemIndex,
			"GemPort":         gemEntry.gemPortID,
			"QueueScheduling": gemEntry.queueSchedPolicy})
	}

	onuTP.chTpConfigProcessingStep <- aProcessingStep //done
}

func (onuTP *onuUniTechProf) setAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aTpID uint8, apCurrentUniPort *onuUniPort, aProcessingStep uint8) {

	//OMCI transfer of ANI data acc. to mapPonAniConfig
	// also the FSM's are running in background,
	//   hence we have to make sure they indicate 'success' success on chTpConfigProcessingStep with aProcessingStep
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	if onuTP.pAniConfigFsm == nil {
		onuTP.createAniConfigFsm(aUniID, aTpID, apCurrentUniPort, OmciAniConfigDone, aProcessingStep)
	} else if _, ok := onuTP.pAniConfigFsm[uniTPKey]; !ok {
		onuTP.createAniConfigFsm(aUniID, aTpID, apCurrentUniPort, OmciAniConfigDone, aProcessingStep)
	} else { //AniConfigFsm already init
		onuTP.runAniConfigFsm(aProcessingStep, aUniID, aTpID)
	}
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
	apCurrentUniPort *onuUniPort, devEvent OnuDeviceEvent, aProcessingStep uint8) {
	logger.Debugw("createAniConfigFsm", log.Fields{"device-id": onuTP.deviceID})
	chAniConfigFsm := make(chan Message, 2048)
	uniTPKey := uniTP{uniID: aUniID, tpID: aTpID}
	pDevEntry := onuTP.baseDeviceHandler.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": onuTP.deviceID})
		return
	}
	pAniCfgFsm := newUniPonAniConfigFsm(pDevEntry.PDevOmciCC, apCurrentUniPort, onuTP,
		pDevEntry.pOnuDB, aTpID, devEvent,
		"AniConfigFsm", onuTP.baseDeviceHandler, chAniConfigFsm)
	if pAniCfgFsm != nil {
		if onuTP.pAniConfigFsm == nil {
			onuTP.pAniConfigFsm = make(map[uniTP]*uniPonAniConfigFsm)
		}
		onuTP.pAniConfigFsm[uniTPKey] = pAniCfgFsm
		onuTP.runAniConfigFsm(aProcessingStep, aUniID, aTpID)
	} else {
		logger.Errorw("AniConfigFSM could not be created - abort!!", log.Fields{"device-id": onuTP.deviceID})
	}
}

// deleteTpResource removes Resources from the ONU's specified Uni
func (onuTP *onuUniTechProf) deleteTpResource(ctx context.Context,
	aUniID uint8, aPathString string, aResource resourceEntry, aEntryID uint32,
	wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("this would remove TP resources from ONU's UNI", log.Fields{
		"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString, "Resource": aResource})
	tpID, err := GetTpIDFromTpPath(aPathString)
	if err != nil {
		logger.Errorw("error-extracting-tp-id-from-tp-path", log.Fields{"device-id": onuTP.deviceID, "uni-id": aUniID, "path": aPathString})
		return
	}
	uniTpKey := uniTP{uniID: aUniID, tpID: tpID}
	if cResourceGemPort == aResource {
		logger.Debugw("remove GemPort from the list of existing ones of the TP", log.Fields{
			"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString, "entry": aEntryID})
		// check if the requested GemPort exists in the DB
		// check that the TpConfigRequest was done
		// initiate OMCI GemPort related removal
		// remove GemPort from config DB
		// dev reason update? (for the moment not yet done here!)
	} else { //if cResourceTcont == aResource {
		//the TechProfile indicated by path is considered for removal
		//  by now we do not clear the OMCI related configuration (to be done later)
		//  so we use this position here just to remove the internal stored profile data
		//    (needed for checking the existence of the TechProfile after some profile delete)
		//  at the oment we only admit 1 TechProfile (T-Cont), so by now we can just remove the only existing TechProfile
		//  TODO: To be updated with multi-T-Cont implementation
		logger.Debugw("DeleteTcont clears the existing internal profile", log.Fields{
			"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString, "Resource": aResource})

		onuTP.clearAniSideConfig(aUniID, tpID)
		// reset also the FSM in order to admit a new OMCI configuration in case a new profile is created
		// FSM stop maybe encapsulated as OnuTP method - perhaps later in context of module splitting
		if onuTP.pAniConfigFsm != nil {
			_ = onuTP.pAniConfigFsm[uniTpKey].pAdaptFsm.pFsm.Event(aniEvReset)
		}

		//TODO!!! - the real processing could look like that (for starting the removal, where the clearAniSideConfig is done implicitly):
		//delete the given resource from ONU OMCI config and data base - as background routine
		/*
			var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep
			go onuTp.deleteAniResource(ctx, processingStep)
			if !onuTP.waitForTimeoutOrCompletion(ctx, chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				return
			}
		*/
	}
	//if implemented, the called FSM would generate an adequate event if config has been done,
	//  by now we just stimulate this event here as 'done' - TODO!!: to be removed after full implementation
	go onuTP.baseDeviceHandler.deviceProcStatusUpdate(OmciAniResourceRemoved)
}

// runAniConfigFsm starts the AniConfig FSM to transfer the OMCI related commands for  ANI side configuration
func (onuTP *onuUniTechProf) runAniConfigFsm(aProcessingStep uint8, aUniID uint8, aTpID uint8) {
	/*  Uni related ANI config procedure -
	 ***** should run via 'aniConfigDone' state and generate the argument requested event *****
	 */
	uniTpKey := uniTP{uniID: aUniID, tpID: aTpID}

	pACStatemachine := onuTP.pAniConfigFsm[uniTpKey].pAdaptFsm.pFsm
	if pACStatemachine != nil {
		if pACStatemachine.Is(aniStDisabled) {
			//FSM init requirement to get informed abou FSM completion! (otherwise timeout of the TechProf config)
			onuTP.pAniConfigFsm[uniTpKey].setFsmCompleteChannel(onuTP.chTpConfigProcessingStep, aProcessingStep)
			if err := pACStatemachine.Event(aniEvStart); err != nil {
				logger.Warnw("AniConfigFSM: can't start", log.Fields{"err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				/***** AniConfigFSM started */
				logger.Debugw("AniConfigFSM started", log.Fields{
					"state": pACStatemachine.Current(), "device-id": onuTP.deviceID})
			}
		} else {
			logger.Warnw("wrong state of AniConfigFSM - want: disabled", log.Fields{
				"have": pACStatemachine.Current(), "device-id": onuTP.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
	} else {
		logger.Errorw("AniConfigFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": onuTP.deviceID})
		// maybe try a FSM reset and then again ... - TODO!!!
	}
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
