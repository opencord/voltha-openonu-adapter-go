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
	"strconv"
	"strings"
	"sync"

	"github.com/looplab/fsm"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	tp "github.com/opencord/voltha-lib-go/v3/pkg/techprofile"
)

const cBasePathTechProfileKVStore = "service/voltha/technology_profiles"
const cBasePathOnuKVStore = "service/voltha/openonu"

//definitions for TechProfileProcessing - copied from OltAdapter:openolt_flowmgr.go
//  could perhaps be defined more globally
const (
	// BinaryStringPrefix is binary string prefix
	BinaryStringPrefix = "0b"
	// BinaryBit1 is binary bit 1 expressed as a character
	BinaryBit1 = '1'
)

type resourceEntry int

const (
	cResourceGemPort resourceEntry = 1
	cResourceTcont   resourceEntry = 2
)

type uniPersData struct {
	PersUniId  uint32 `json:"uni_id"`
	PersTpPath string `json:"tp_path"`
}

type onuPersistentData struct {
	PersOnuID      uint32        `json:"onu_id"`
	PersIntfID     uint32        `json:"intf_id"`
	PersSnr        string        `json:"serial_number"`
	PersAdminState string        `json:"admin_state"`
	PersOperState  string        `json:"oper_state"`
	PersUniTpPath  []uniPersData `json:"uni_config"`
}

type tTechProfileIndication struct {
	techProfileType       string
	techProfileID         uint16
	techProfileConfigDone bool
}

type tcontParamStruct struct {
	allocID     uint16
	schedPolicy uint8
}
type gemPortParamStruct struct {
	ponOmciCC       bool
	gemPortID       uint16
	direction       uint8
	gemPortEncState uint8
	prioQueueIndex  uint8
	pbitString      string
	discardPolicy   string
	//could also be a queue specific paramter, not used that way here
	maxQueueSize     uint16
	queueSchedPolicy string
	queueWeight      uint8
}

//refers to one tcont and its properties and all assigned GemPorts and their properties
type tcontGemList struct {
	tcontParams      tcontParamStruct
	mapGemPortParams map[uint16]*gemPortParamStruct
}

//refers to all tcont and their Tcont/GemPort Parameters
type tMapPonAniConfig map[uint16]*tcontGemList

//OnuUniTechProf structure holds information about the TechProfiles attached to Uni Ports of the ONU
type OnuUniTechProf struct {
	deviceID                 string
	baseDeviceHandler        *DeviceHandler
	tpProcMutex              sync.RWMutex
	mapUniTpPath             map[uint32]string
	sOnuPersistentData       onuPersistentData
	techProfileKVStore       *db.Backend
	onuKVStore               *db.Backend
	onuKVStorePath           string
	chTpConfigProcessingStep chan uint8
	chTpKvProcessingStep     chan uint8
	mapUniTpIndication       map[uint8]*tTechProfileIndication //use pointer values to ease assignments to the map
	mapPonAniConfig          map[uint8]*tMapPonAniConfig       //per UNI: use pointer values to ease assignments to the map
	pAniConfigFsm            *UniPonAniConfigFsm
	procResult               error //error indication of processing
	mutexTPState             sync.Mutex
}

//NewOnuUniTechProf returns the instance of a OnuUniTechProf
//(one instance per ONU/deviceHandler for all possible UNI's)
func NewOnuUniTechProf(ctx context.Context, aDeviceID string, aDeviceHandler *DeviceHandler) *OnuUniTechProf {
	logger.Infow("init-OnuUniTechProf", log.Fields{"device-id": aDeviceID})
	var onuTP OnuUniTechProf
	onuTP.deviceID = aDeviceID
	onuTP.baseDeviceHandler = aDeviceHandler
	onuTP.tpProcMutex = sync.RWMutex{}
	onuTP.mapUniTpPath = make(map[uint32]string)
	onuTP.sOnuPersistentData.PersUniTpPath = make([]uniPersData, 1)
	onuTP.chTpConfigProcessingStep = make(chan uint8)
	onuTP.chTpKvProcessingStep = make(chan uint8)
	onuTP.mapUniTpIndication = make(map[uint8]*tTechProfileIndication)
	onuTP.mapPonAniConfig = make(map[uint8]*tMapPonAniConfig)
	onuTP.procResult = nil //default assumption processing done with success

	onuTP.techProfileKVStore = aDeviceHandler.SetBackend(cBasePathTechProfileKVStore)
	if onuTP.techProfileKVStore == nil {
		logger.Errorw("Can't access techProfileKVStore - no backend connection to service",
			log.Fields{"device-id": aDeviceID, "service": cBasePathTechProfileKVStore})
	}

	onuTP.onuKVStorePath = onuTP.deviceID
	onuTP.onuKVStore = aDeviceHandler.SetBackend(cBasePathOnuKVStore)
	if onuTP.onuKVStore == nil {
		logger.Errorw("Can't access onuKVStore - no backend connection to service",
			log.Fields{"device-id": aDeviceID, "service": cBasePathOnuKVStore})
	}
	return &onuTP
}

// lockTpProcMutex locks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) lockTpProcMutex() {
	onuTP.tpProcMutex.Lock()
}

// unlockTpProcMutex unlocks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) unlockTpProcMutex() {
	onuTP.tpProcMutex.Unlock()
}

// resetProcessingErrorIndication resets the internal error indication
// need to be called before evaluation of any subsequent processing (given by waitForTpCompletion())
func (onuTP *OnuUniTechProf) resetProcessingErrorIndication() {
	onuTP.procResult = nil
}

// updateOnuUniTpPath verifies and updates changes in the kvStore onuUniTpPath
func (onuTP *OnuUniTechProf) updateOnuUniTpPath(aUniID uint32, aPathString string) bool {
	/* within some specific InterAdapter processing request write/read access to data is ensured to be sequentially,
	   as also the complete sequence is ensured to 'run to  completion' before some new request is accepted
	   no specific concurrency protection to sOnuPersistentData is required here
	*/
	if existingPath, present := onuTP.mapUniTpPath[aUniID]; present {
		// uni entry already exists
		//logger.Debugw(" already exists", log.Fields{"for InstanceId": a_uniInstNo})
		if existingPath != aPathString {
			if aPathString == "" {
				//existing entry to be deleted
				logger.Debugw("UniTp path delete", log.Fields{
					"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
				delete(onuTP.mapUniTpPath, aUniID)
			} else {
				//existing entry to be modified
				logger.Debugw("UniTp path modify", log.Fields{
					"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
				onuTP.mapUniTpPath[aUniID] = aPathString
			}
			return true
		}
		//entry already exists
		logger.Debugw("UniTp path already exists", log.Fields{
			"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
		return false
	}
	//uni entry does not exist
	if aPathString == "" {
		//delete request in non-existing state , accept as no change
		logger.Debugw("UniTp path already removed", log.Fields{
			"device-id": onuTP.deviceID, "uniID": aUniID})
		return false
	}
	//new entry to be set
	logger.Debugw("New UniTp path set", log.Fields{
		"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
	onuTP.mapUniTpPath[aUniID] = aPathString
	return true
}

func (onuTP *OnuUniTechProf) waitForTpCompletion(cancel context.CancelFunc, wg *sync.WaitGroup) error {
	defer cancel() //ensure termination of context (may be pro forma)
	wg.Wait()
	logger.Debug("some TechProfile Processing completed")
	onuTP.tpProcMutex.Unlock() //allow further TP related processing
	return onuTP.procResult
}

// configureUniTp checks existing tp resources to delete and starts the corresponding OMCI configuation of the UNI port
// all possibly blocking processing must be run in background to allow for deadline supervision!
// but take care on sequential background processing when needed (logical dependencies)
//   use waitForTimeoutOrCompletion(ctx, chTpConfigProcessingStep, processingStep) for internal synchronisation
func (onuTP *OnuUniTechProf) configureUniTp(ctx context.Context,
	aUniID uint8, aPathString string, wg *sync.WaitGroup) {
	defer wg.Done() //always decrement the waitGroup on return
	logger.Debugw("configure the Uni according to TpPath", log.Fields{
		"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString})

	if onuTP.techProfileKVStore == nil {
		logger.Debug("techProfileKVStore not set - abort")
		onuTP.procResult = errors.New("TechProfile config aborted: techProfileKVStore not set")
		return
	}

	//ensure that the given uniID is available (configured) in the UniPort class (used for OMCI entities)
	var pCurrentUniPort *OnuUniPort
	for _, uniPort := range onuTP.baseDeviceHandler.uniEntityMap {
		// only if this port is validated for operState transfer
		if uniPort.uniId == uint8(aUniID) {
			pCurrentUniPort = uniPort
			break //found - end search loop
		}
	}
	if pCurrentUniPort == nil {
		logger.Errorw("TechProfile configuration aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": onuTP.deviceID, "uniID": aUniID})
		onuTP.procResult = errors.New("TechProfile config aborted: requested uniID not found")
		return
	}

	var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpConfigProcessingStep

	//according to updateOnuUniTpPath() logic the assumption here is, that this configuration is only called
	//  in case the KVPath has changed for the given UNI,
	//  as T-Cont and Gem-Id's are dependent on TechProfile-Id this means, that possibly previously existing
	//  (ANI) configuration of this port has to be removed first
	//  (moreover in this case a possibly existing flow configuration is also not valid anymore and needs clean-up as well)
	//  existence of configuration can be detected based on tp stored TCONT's
	//TODO!!!:
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
	go onuTP.readAniSideConfigFromTechProfile(ctx, aUniID, aPathString, processingStep)
	if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw("tech-profile related configuration aborted on read",
			log.Fields{"device-id": onuTP.deviceID, "UniId": aUniID})
		onuTP.procResult = errors.New("TechProfile config aborted: tech-profile read issue")
		return
	}

	processingStep++
	if valuePA, existPA := onuTP.mapPonAniConfig[aUniID]; existPA {
		if _, existTG := (*valuePA)[0]; existTG {
			//Config data for this uni and and at least TCont Index 0 exist
			go onuTP.setAniSideConfigFromTechProfile(ctx, aUniID, pCurrentUniPort, processingStep)
			if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpConfigProcessingStep, processingStep) {
				//timeout or error detected
				logger.Debugw("tech-profile related configuration aborted on set",
					log.Fields{"device-id": onuTP.deviceID, "UniId": aUniID})
				onuTP.procResult = errors.New("TechProfile config aborted: Omci AniSideConfig failed")
				//this issue here means that the AniConfigFsm has not finished succesfully
				//which requires to reset it to allow for new usage, e.g. also on a different UNI
				//(without that it would be reset on device down indication latest)
				onuTP.pAniConfigFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				return
			}
		} else {
			// strange: UNI entry exists, but no ANI data, maybe such situation should be cleared up (if observed)
			logger.Debugw("no Tcont/Gem data for this UNI found - abort", log.Fields{
				"device-id": onuTP.deviceID, "uniID": aUniID})
			onuTP.procResult = errors.New("TechProfile config aborted: no Tcont/Gem data found for this UNI")
			return
		}
	} else {
		logger.Debugw("no PonAni data for this UNI found - abort", log.Fields{
			"device-id": onuTP.deviceID, "uniID": aUniID})
		onuTP.procResult = errors.New("TechProfile config aborted: no AniSide data found for this UNI")
		return
	}
}

func (onuTP *OnuUniTechProf) updateOnuTpPathKvStore(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if onuTP.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": onuTP.deviceID})
		onuTP.procResult = errors.New("ONU/TP-data update aborted: onuKVStore not set")
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chTpKvProcessingStep
	go onuTP.storePersistentData(ctx, processingStep)
	if !onuTP.waitForTimeoutOrCompletion(ctx, onuTP.chTpKvProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw("ONU/TP-data not written - abort", log.Fields{"device-id": onuTP.deviceID})
		onuTP.procResult = errors.New("ONU/TP-data update aborted: during writing process")
		return
	}
}

func (onuTP *OnuUniTechProf) restoreFromOnuTpPathKvStore(ctx context.Context) error {
	if onuTP.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf(fmt.Sprintf("onuKVStore-not-set-abort-%s", onuTP.deviceID))
	}
	if err := onuTP.restorePersistentData(ctx); err != nil {
		logger.Debugw("ONU/TP-data not read - abort", log.Fields{"device-id": onuTP.deviceID})
		return err
	}
	return nil
}

func (onuTP *OnuUniTechProf) deleteOnuTpPathKvStore(ctx context.Context) error {
	if onuTP.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf(fmt.Sprintf("onuKVStore-not-set-abort-%s", onuTP.deviceID))
	}
	if err := onuTP.deletePersistentData(ctx); err != nil {
		logger.Debugw("ONU/TP-data not read - abort", log.Fields{"device-id": onuTP.deviceID})
		return err
	}
	return nil
}

// deleteTpResource removes Resources from the ONU's specified Uni
func (onuTP *OnuUniTechProf) deleteTpResource(ctx context.Context,
	aUniID uint32, aPathString string, aResource resourceEntry, aEntryID uint32,
	wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("this would remove TP resources from ONU's UNI", log.Fields{
		"device-id": onuTP.deviceID, "uniID": aUniID, "path": aPathString, "Resource": aResource})
	//TODO!!!
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

/* internal methods *********************/

func (onuTP *OnuUniTechProf) storePersistentData(ctx context.Context, aProcessingStep uint8) {

	onuTP.sOnuPersistentData.PersOnuID = onuTP.baseDeviceHandler.pOnuIndication.OnuId
	onuTP.sOnuPersistentData.PersIntfID = onuTP.baseDeviceHandler.pOnuIndication.IntfId
	onuTP.sOnuPersistentData.PersSnr = onuTP.baseDeviceHandler.pOnuOmciDevice.serialNumber
	//TODO: verify usage of these values during restart UC
	onuTP.sOnuPersistentData.PersAdminState = "up"
	onuTP.sOnuPersistentData.PersOperState = "active"

	onuTP.sOnuPersistentData.PersUniTpPath = onuTP.sOnuPersistentData.PersUniTpPath[:0]

	for k, v := range onuTP.mapUniTpPath {
		onuTP.sOnuPersistentData.PersUniTpPath =
			append(onuTP.sOnuPersistentData.PersUniTpPath, uniPersData{PersUniId: k, PersTpPath: v})
	}
	logger.Debugw("Update ONU/TP-data in KVStore", log.Fields{"device-id": onuTP.deviceID, "onuTP.sOnuPersistentData": onuTP.sOnuPersistentData})

	Value, err := json.Marshal(onuTP.sOnuPersistentData)
	if err != nil {
		logger.Errorw("unable to marshal ONU/TP-data", log.Fields{"onuTP.sOnuPersistentData": onuTP.sOnuPersistentData,
			"device-id": onuTP.deviceID, "err": err})
		onuTP.chTpKvProcessingStep <- 0 //error indication
		return
	}
	err = onuTP.onuKVStore.Put(ctx, onuTP.onuKVStorePath, Value)
	if err != nil {
		logger.Errorw("unable to write ONU/TP-data into KVstore", log.Fields{"device-id": onuTP.deviceID, "err": err})
		onuTP.chTpKvProcessingStep <- 0 //error indication
		return
	}
	onuTP.chTpKvProcessingStep <- aProcessingStep //done
}

func (onuTP *OnuUniTechProf) restorePersistentData(ctx context.Context) error {

	onuTP.mapUniTpPath = make(map[uint32]string)
	onuTP.sOnuPersistentData = onuPersistentData{0, 0, "", "", "", make([]uniPersData, 0)}

	Value, err := onuTP.onuKVStore.Get(ctx, onuTP.onuKVStorePath)
	if err == nil {
		if Value != nil {
			logger.Debugw("ONU/TP-data read",
				log.Fields{"Key": Value.Key, "device-id": onuTP.deviceID})
			tpTmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tpTmpBytes, &onuTP.sOnuPersistentData); err != nil {
				logger.Errorw("unable to unmarshal ONU/TP-data", log.Fields{"error": err, "device-id": onuTP.deviceID})
				return fmt.Errorf(fmt.Sprintf("unable-to-unmarshal-ONU/TP-data-%s", onuTP.deviceID))
			}
			logger.Debugw("ONU/TP-data", log.Fields{"onuTP.sOnuPersistentData": onuTP.sOnuPersistentData,
				"device-id": onuTP.deviceID})

			for _, uniData := range onuTP.sOnuPersistentData.PersUniTpPath {
				onuTP.mapUniTpPath[uniData.PersUniId] = uniData.PersTpPath
			}
			logger.Debugw("TpPath map", log.Fields{"onuTP.mapUniTpPath": onuTP.mapUniTpPath,
				"device-id": onuTP.deviceID})
		} else {
			logger.Errorw("no ONU/TP-data found", log.Fields{"path": onuTP.onuKVStorePath, "device-id": onuTP.deviceID})
			return fmt.Errorf(fmt.Sprintf("no-ONU/TP-data-found-%s", onuTP.deviceID))
		}
	} else {
		logger.Errorw("unable to read from KVstore", log.Fields{"device-id": onuTP.deviceID})
		return fmt.Errorf(fmt.Sprintf("unable-to-read-from-KVstore-%s", onuTP.deviceID))
	}
	return nil
}

func (onuTP *OnuUniTechProf) deletePersistentData(ctx context.Context) error {

	logger.Debugw("delete ONU/TP-data in KVStore", log.Fields{"device-id": onuTP.deviceID})
	err := onuTP.onuKVStore.Delete(ctx, onuTP.onuKVStorePath)
	if err != nil {
		logger.Errorw("unable to delete in KVstore", log.Fields{"device-id": onuTP.deviceID, "err": err})
		return fmt.Errorf(fmt.Sprintf("unable-delete-in-KVstore-%s", onuTP.deviceID))
	}
	return nil
}

func (onuTP *OnuUniTechProf) readAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, aPathString string, aProcessingStep uint8) {
	var tpInst tp.TechProfile

	//store profile type and identifier for later usage within the OMCI identifier and possibly ME setup
	//pathstring is defined to be in the form of <ProfType>/<profID>/<Interface/../Identifier>
	subStringSlice := strings.Split(aPathString, "/")
	if len(subStringSlice) <= 2 {
		logger.Errorw("invalid path name format",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}

	//just some logical check to avoid unexpected behavior
	//at this point it is assumed that a new TechProfile is assigned to the UNI
	//expectation is that no TPIndication entry exists here, if yes,
	//  then we throw a warning and remove it (and the possible ANIConfig) simply
	//  note that the ONU config state may be ambivalent in such a case
	//  also note, that the PonAniConfig map is not checked additionally
	//    consistency to TPIndication is assumed
	if _, existTP := onuTP.mapUniTpIndication[aUniID]; existTP {
		logger.Warnw("Some active profile entry at reading new TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID,
				"UniId": aUniID, "wrongProfile": onuTP.mapUniTpIndication[aUniID].techProfileID})
		//delete on the mapUniTpIndication map not needed, just overwritten later
		//delete on the PonAniConfig map should be safe, even if not existing
		delete(onuTP.mapPonAniConfig, aUniID)
	} else {
		// this is normal processing
		onuTP.mapUniTpIndication[aUniID] = &tTechProfileIndication{} //need to assign some (empty) struct memory first!
	}

	onuTP.mapUniTpIndication[aUniID].techProfileType = subStringSlice[0]
	profID, err := strconv.ParseUint(subStringSlice[1], 10, 32)
	if err != nil {
		logger.Errorw("invalid ProfileId from path",
			log.Fields{"ParseErr": err})
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}

	//note the limitation on ID range (probably even more limited) - based on usage within OMCI EntityID
	onuTP.mapUniTpIndication[aUniID].techProfileID = uint16(profID)
	logger.Debugw("tech-profile path indications",
		log.Fields{"device-id": onuTP.deviceID, "UniId": aUniID,
			"profType": onuTP.mapUniTpIndication[aUniID].techProfileType,
			"profID":   onuTP.mapUniTpIndication[aUniID].techProfileID})

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

	//default start with 1Tcont1Gem profile, later extend for multi GemPerTcont and perhaps even  MultiTcontMultiGem
	localMapGemPortParams := make(map[uint16]*gemPortParamStruct)
	localMapGemPortParams[0] = &gemPortParamStruct{}
	localMapPonAniConfig := make(map[uint16]*tcontGemList)
	localMapPonAniConfig[0] = &tcontGemList{tcontParamStruct{}, localMapGemPortParams}
	onuTP.mapPonAniConfig[aUniID] = (*tMapPonAniConfig)(&localMapPonAniConfig)

	//note: the code is currently restricted to one TCcont per Onu (index [0])
	//get the relevant values from the profile and store to mapPonAniConfig
	(*(onuTP.mapPonAniConfig[aUniID]))[0].tcontParams.allocID = uint16(tpInst.UsScheduler.AllocID)
	//maybe tCont scheduling not (yet) needed - just to basicaly have it for future
	//  (would only be relevant in case of ONU-2G QOS configuration flexibility)
	if tpInst.UsScheduler.QSchedPolicy == "StrictPrio" {
		(*(onuTP.mapPonAniConfig[aUniID]))[0].tcontParams.schedPolicy = 1 //for the moment fixed value acc. G.988 //TODO: defines!
	} else {
		//default profile defines "Hybrid" - which probably comes down to WRR with some weigthts for SP
		(*(onuTP.mapPonAniConfig[aUniID]))[0].tcontParams.schedPolicy = 2 //for G.988 WRR
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
			(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)] = &gemPortParamStruct{}
		}
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].gemPortID =
			uint16(content.GemportID)
		//direction can be correlated later with Downstream list,
		//  for now just assume bidirectional (upstream never exists alone)
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].direction = 3 //as defined in G.988
		// expected Prio-Queue values 0..7 with 7 for highest PrioQueue, QueueIndex=Prio = 0..7
		if 7 < content.PriorityQueue {
			logger.Errorw("PonAniConfig reject on GemPortList - PrioQueue value invalid",
				log.Fields{"device-id": onuTP.deviceID, "index": pos, "PrioQueue": content.PriorityQueue})
			//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
			delete(onuTP.mapPonAniConfig, aUniID)
			onuTP.chTpConfigProcessingStep <- 0 //error indication
			return
		}
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].prioQueueIndex =
			uint8(content.PriorityQueue)
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].pbitString =
			strings.TrimPrefix(content.PbitMap, BinaryStringPrefix)
		if content.AesEncryption == "True" {
			(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].gemPortEncState = 1
		} else {
			(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].gemPortEncState = 0
		}
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].discardPolicy =
			content.DiscardPolicy
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].queueSchedPolicy =
			content.SchedulingPolicy
		//'GemWeight' looks strange in default profile, for now we just copy the weight to first queue
		(*(onuTP.mapPonAniConfig[aUniID]))[0].mapGemPortParams[uint16(pos)].queueWeight =
			uint8(content.Weight)
	}
	if loGemPortRead == false {
		logger.Errorw("PonAniConfig reject - no GemPort could be read from TechProfile",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		//remove PonAniConfig  as done so far, delete map should be safe, even if not existing
		delete(onuTP.mapPonAniConfig, aUniID)
		onuTP.chTpConfigProcessingStep <- 0 //error indication
		return
	}
	//TODO!! MC (downstream) GemPorts can be set using DownstreamGemPortAttributeList seperately

	//logger does not simply output the given structures, just give some example debug values
	logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
		"device-id": onuTP.deviceID,
		"AllocId":   (*(onuTP.mapPonAniConfig[aUniID]))[0].tcontParams.allocID})
	for gemIndex, gemEntry := range (*(onuTP.mapPonAniConfig[0]))[0].mapGemPortParams {
		logger.Debugw("PonAniConfig read from TechProfile", log.Fields{
			"GemIndex":        gemIndex,
			"GemPort":         gemEntry.gemPortID,
			"QueueScheduling": gemEntry.queueSchedPolicy})
	}

	onuTP.chTpConfigProcessingStep <- aProcessingStep //done
}

func (onuTP *OnuUniTechProf) setAniSideConfigFromTechProfile(
	ctx context.Context, aUniID uint8, apCurrentUniPort *OnuUniPort, aProcessingStep uint8) {

	//OMCI transfer of ANI data acc. to mapPonAniConfig
	// also the FSM's are running in background,
	//   hence we have to make sure they indicate 'success' success on chTpConfigProcessingStep with aProcessingStep
	if onuTP.pAniConfigFsm == nil {
		onuTP.createAniConfigFsm(aUniID, apCurrentUniPort, OmciAniConfigDone, aProcessingStep)
	} else { //AniConfigFsm already init
		onuTP.runAniConfigFsm(aProcessingStep)
	}
}

func (onuTP *OnuUniTechProf) waitForTimeoutOrCompletion(
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

// createUniLockFsm initialises and runs the AniConfig FSM to transfer the OMCI related commands for ANI side configuration
func (onuTP *OnuUniTechProf) createAniConfigFsm(aUniID uint8,
	apCurrentUniPort *OnuUniPort, devEvent OnuDeviceEvent, aProcessingStep uint8) {
	logger.Debugw("createAniConfigFsm", log.Fields{"device-id": onuTP.deviceID})
	chAniConfigFsm := make(chan Message, 2048)
	pDevEntry := onuTP.baseDeviceHandler.GetOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": onuTP.deviceID})
		return
	}
	pAniCfgFsm := NewUniPonAniConfigFsm(pDevEntry.PDevOmciCC, apCurrentUniPort, onuTP,
		pDevEntry.pOnuDB, onuTP.mapUniTpIndication[aUniID].techProfileID, devEvent,
		"AniConfigFsm", onuTP.deviceID, chAniConfigFsm)
	if pAniCfgFsm != nil {
		onuTP.pAniConfigFsm = pAniCfgFsm
		onuTP.runAniConfigFsm(aProcessingStep)
	} else {
		logger.Errorw("AniConfigFSM could not be created - abort!!", log.Fields{"device-id": onuTP.deviceID})
	}
}

// runAniConfigFsm starts the AniConfig FSM to transfer the OMCI related commands for  ANI side configuration
func (onuTP *OnuUniTechProf) runAniConfigFsm(aProcessingStep uint8) {
	/*  Uni related ANI config procedure -
	 ***** should run via 'aniConfigDone' state and generate the argument requested event *****
	 */
	var pACStatemachine *fsm.FSM
	pACStatemachine = onuTP.pAniConfigFsm.pAdaptFsm.pFsm
	if pACStatemachine != nil {
		if pACStatemachine.Is(aniStDisabled) {
			//FSM init requirement to get informed abou FSM completion! (otherwise timeout of the TechProf config)
			onuTP.pAniConfigFsm.SetFsmCompleteChannel(onuTP.chTpConfigProcessingStep, aProcessingStep)
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

// setConfigDone sets the requested techProfile config state (if possible)
func (onuTP *OnuUniTechProf) setConfigDone(aUniID uint8, aState bool) {
	if _, existTP := onuTP.mapUniTpIndication[aUniID]; existTP {
		onuTP.mutexTPState.Lock()
		onuTP.mapUniTpIndication[aUniID].techProfileConfigDone = aState
		onuTP.mutexTPState.Unlock()
	} //else: the state is just ignored (does not exist)
}

// getTechProfileDone checks if the Techprofile processing with the requested TechProfile ID was done
func (onuTP *OnuUniTechProf) getTechProfileDone(aUniID uint8, aTpID uint16) bool {
	if _, existTP := onuTP.mapUniTpIndication[aUniID]; existTP {
		if onuTP.mapUniTpIndication[aUniID].techProfileID == aTpID {
			onuTP.mutexTPState.Lock()
			defer onuTP.mutexTPState.Unlock()
			return onuTP.mapUniTpIndication[aUniID].techProfileConfigDone
		}
	}
	//for all other constellations indicate false = Config not done
	return false
}
