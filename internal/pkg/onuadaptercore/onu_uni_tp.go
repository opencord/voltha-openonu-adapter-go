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
	"sync"

	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	tp "github.com/opencord/voltha-lib-go/v3/pkg/techprofile"
)

const cBasePathTechProfileKVStore = "service/voltha/technology_profiles"

type resourceEntry int

const (
	cResourceGemPort resourceEntry = 1
	cResourceTcont   resourceEntry = 2
)

type onuSerialNumber struct {
	sliceVendorID       []byte
	sliceVendorSpecific []byte
}

type onuPersistentData struct {
	persOnuID      uint32
	persIntfID     uint32
	persSnr        onuSerialNumber
	persAdminState string
	persOperState  string
	persUniTpPath  map[uint32]string
	persUniTpData  map[uint32]tp.TechProfile
}

//OnuUniTechProf structure holds information about the TechProfiles attached to Uni Ports of the ONU
type OnuUniTechProf struct {
	deviceID           string
	baseDeviceHandler  *DeviceHandler
	tpProcMutex        sync.RWMutex
	sOnuPersistentData onuPersistentData
	techProfileKVStore *db.Backend
}

//NewOnuUniTechProf returns the instance of a OnuUniTechProf
//(one instance per ONU/deviceHandler for all possible UNI's)
func NewOnuUniTechProf(ctx context.Context, aDeviceID string, aDeviceHandler *DeviceHandler) *OnuUniTechProf {
	logger.Infow("init-OnuUniTechProf", log.Fields{"deviceId": aDeviceID})
	var onuTP OnuUniTechProf
	onuTP.deviceID = aDeviceID
	onuTP.baseDeviceHandler = aDeviceHandler
	onuTP.tpProcMutex = sync.RWMutex{}
	onuTP.sOnuPersistentData.persUniTpPath = make(map[uint32]string)
	onuTP.sOnuPersistentData.persUniTpData = make(map[uint32]tp.TechProfile)

	onuTP.techProfileKVStore = aDeviceHandler.SetBackend(cBasePathTechProfileKVStore)
	if onuTP.techProfileKVStore == nil {
		logger.Errorw("Can't access techProfileKVStore - no backend connection to service",
                              log.Fields{"deviceID": aDeviceID, "service": cBasePathTechProfileKVStore})
	}
	return &onuTP
}

// lockTpProcMutex locks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) lockTpProcMutex() {
	onuTP.tpProcMutex.Lock()
}

// lockTpProcMutex unlocks OnuUniTechProf processing mutex
func (onuTP *OnuUniTechProf) unlockTpProcMutex() {
	onuTP.tpProcMutex.Unlock()
}

// updateOnuUniTpPath verifies and updates changes in the kvStore onuUniTpPath
func (onuTP *OnuUniTechProf) updateOnuUniTpPath(aUniID uint32, aPathString string) bool {
	/* within some specific InterAdapter processing request write/read access to data is ensured to be sequentially,
	   as also the complete sequence is ensured to 'run to  completion' before some new request is accepted
	   no specific concurrency protection to sOnuPersistentData is required here
	*/
	if existingPath, present := onuTP.sOnuPersistentData.persUniTpPath[aUniID]; present {
		// uni entry already exists
		//logger.Debugw(" already exists", log.Fields{"for InstanceId": a_uniInstNo})
		if existingPath != aPathString {
			if aPathString == "" {
				//existing entry to be deleted
				logger.Debugw("UniTp path delete", log.Fields{
					"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
				delete(onuTP.sOnuPersistentData.persUniTpPath, aUniID)
			} else {
				//existing entry to be modified
				logger.Debugw("UniTp path modify", log.Fields{
					"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
				onuTP.sOnuPersistentData.persUniTpPath[aUniID] = aPathString
			}
			return true
		}
		//entry already exists
		logger.Debugw("UniTp path already exists", log.Fields{
			"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
		return false
	}
	//uni entry does not exist
	if aPathString == "" {
		//delete request in non-existing state , accept as no change
		logger.Debugw("UniTp path already removed", log.Fields{
			"deviceID": onuTP.deviceID, "uniID": aUniID})
		return false
	}
	//new entry to be set
	logger.Debugw("New UniTp path set", log.Fields{
		"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString})
	onuTP.sOnuPersistentData.persUniTpPath[aUniID] = aPathString
	return true
}

func (onuTP *OnuUniTechProf) configureUniTp(aUniID uint32, aPathString string, wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("configure the Uni according to TpPath", log.Fields{
		"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString})

	//TODO!!!
	// reaction on existing tp, deletion of tp, start the corresponding OMCI configuation of the UNI port

	if onuTP.techProfileKVStore == nil {
		logger.Debug("techProfileKVStore not set - abort")
		return
        }

  	Value, err := onuTP.techProfileKVStore.Get(context.TODO(), aPathString)
	if err == nil {
		if Value != nil {
			logger.Debugw("tech-profile read",
                                      log.Fields{"Key": Value.Key, "Value": Value.Value})
			tpTmpBytes, _ := kvstore.ToByte(Value.Value)

			var tpInst tp.TechProfile
			if err = json.Unmarshal(tpTmpBytes, &tpInst); err != nil {
				logger.Errorw("TechProf - Failed to unmarshal tech-profile into tpInst",
					log.Fields{"error": err, "device-id": onuTP.deviceID})
			} else {
				logger.Debugw("TechProf - tpInst", log.Fields{"tpInst": tpInst})
				onuTP.sOnuPersistentData.persUniTpData[aUniID] = tpInst

				// access examples
				logger.Debugw("TechProf - name",
					log.Fields{"onuTP.sOnuPersistentData.persUniTpData[aUniID].Name": onuTP.sOnuPersistentData.persUniTpData[aUniID].Name})
				//
				logger.Debugw("TechProf - instance_control.max_gem_payload_size",
					log.Fields{"onuTP.sOnuPersistentData.persUniTpData[aUniID].InstanceCtrl.MaxGemPayloadSize": onuTP.sOnuPersistentData.persUniTpData[aUniID].InstanceCtrl.MaxGemPayloadSize})
				//
				logger.Debugw("TechProf - downstream_gem_port_attribute_list.discard_config.max_threshold",
					log.Fields{"onuTP.sOnuPersistentData.persUniTpData[aUniID].DownstreamGemPortAttributeList[0].DiscardConfig.MaxThreshold": onuTP.sOnuPersistentData.persUniTpData[aUniID].DownstreamGemPortAttributeList[0].DiscardConfig.MaxThreshold})
			}
		} else {
			logger.Debugw("No tech-profile found", log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
		}
	} else {
		logger.Errorw("kvstore-get failed for path",
			log.Fields{"path": aPathString, "device-id": onuTP.deviceID})
	}
}

func (onuTP *OnuUniTechProf) updateOnuTpPathKvStore(wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("this would update the ONU's TpPath in KVStore", log.Fields{
		"deviceID": onuTP.deviceID})
	//TODO!!!
	//make use of onuTP.sOnuPersistentData to store the TpPath to KVStore
}

// deleteTpRessource removes ressources from the ONU's specified Uni
func (onuTP *OnuUniTechProf) deleteTpRessource(aUniID uint32, aPathString string,
	aRessource resourceEntry, aEntryID uint32, wg *sync.WaitGroup) {
	defer wg.Done()
	logger.Debugw("this would remove TP resources from ONU's UNI", log.Fields{
		"deviceID": onuTP.deviceID, "uniID": aUniID, "path": aPathString, "ressource": aRessource})
	//TODO!!!
}

func (onuTP *OnuUniTechProf) waitForTpCompletion(wg *sync.WaitGroup) {
	wg.Wait()
	logger.Debug("some TechProfile Processing completed")
	onuTP.tpProcMutex.Unlock() //allow further TP related processing
}
