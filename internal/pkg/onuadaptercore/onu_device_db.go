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
	"reflect"
	"sort"
	"sync"

	om "github.com/cevaris/ordered_map"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
)

type meDbMap map[me.ClassID]map[uint16]me.AttributeValueMap

//onuDeviceDB structure holds information about known ME's
type onuDeviceDB struct {
	ctx             context.Context
	pOnuDeviceEntry *OnuDeviceEntry
	meDb            meDbMap
	meDbLock        sync.RWMutex
}

//newOnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func newOnuDeviceDB(ctx context.Context, aPOnuDeviceEntry *OnuDeviceEntry) *onuDeviceDB {
	logger.Debugw(ctx, "Init OnuDeviceDB for:", log.Fields{"device-id": aPOnuDeviceEntry.deviceID})
	var onuDeviceDB onuDeviceDB
	onuDeviceDB.ctx = ctx
	onuDeviceDB.pOnuDeviceEntry = aPOnuDeviceEntry
	onuDeviceDB.meDb = make(meDbMap)

	return &onuDeviceDB
}

// AllocateMeInstance returns the  ME instanceID if it is allocated before or allocates a new one if not.
// The AttributeValueMap is the map of attributes and their values that need to be checked in DB for currrent class.
// The condition list is again the list of attributes and their values for an empty ME check.
// condition list is a list(not a map) because it may include same attribute with different values. e.g "alloc == 255" or "alloc == 65535".
// return values are instanceID, allreadyExist flag and error.
func (onuDeviceDB *onuDeviceDB) AllocateMeInstance(ctx context.Context, meClassID me.ClassID, allocatedAttributes me.AttributeValueMap, condForFreeAttr []om.KVPair) (uint16, bool, error) {
	onuDeviceDB.meDbLock.Lock()
	defer onuDeviceDB.meDbLock.Unlock()

	meInstMap := onuDeviceDB.meDb[meClassID]

	// FIXME: Ideally the ME configurations on the ONU should constantly be MIB Synced back to the ONU DB
	// So, as soon as we use up a TCONT Entity on the ONU, the DB at ONU adapter should know that the TCONT
	// entity is used up via MIB Sync procedure and it will not use it for subsequent TCONT on that ONU.
	// But, it seems, due to the absence of the constant mib-sync procedure, we feed the DB here an each TCONT allocation.
	//The lock here also ensures the concurent use of function by separate FSMs of the UNIs.
	var freeEntityInstanceID uint16
	foundFreeMeInstID := false
	for instanceID, attributeMap := range meInstMap {
		logger.Debugw(ctx, "checking-for-me-instance", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "instance-id": instanceID, "class-id": meClassID})
		//check if all requested attributes are equal to the attributes of current instance in DB.
		meFound := true
		for k, v := range allocatedAttributes {
			if attributeMap[k] != v {
				meFound = false
			}
		}
		if meFound {
			logger.Infow(ctx, "found-me-in-db-entries", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "instance-id": instanceID, "class-id": meClassID})
			return instanceID, true, nil
		}
		// If the attribute of the current ME instance matches with one of the free  attribute conditions
		if !foundFreeMeInstID {
			for _, pair := range condForFreeAttr {
				if attributeMap[pair.Key.(string)] == pair.Value {
					foundFreeMeInstID = true
					freeEntityInstanceID = instanceID
				}
			}

		}
	}
	if !foundFreeMeInstID {
		logger.Infow(ctx, "error-finding-free-entity-for-me", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "class-id": meClassID})
		return freeEntityInstanceID, false, fmt.Errorf(fmt.Sprintf("no-free-me-left-for-device-%s-meClass-%s", onuDeviceDB.pOnuDeviceEntry.deviceID, meClassID))
	}
	//Set the attributes of found free.
	if attributesinDb, present := onuDeviceDB.meDb[meClassID][freeEntityInstanceID]; present {
		for k, v := range allocatedAttributes {
			attributesinDb[k] = v
		}
		onuDeviceDB.meDb[meClassID][freeEntityInstanceID] = attributesinDb
	}
	logger.Infow(ctx, "me-allocated-first-time", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "class-id": meClassID, "instance-id": freeEntityInstanceID})
	return freeEntityInstanceID, false, nil
}

func (onuDeviceDB *onuDeviceDB) PutMe(ctx context.Context, meClassID me.ClassID, meEntityID uint16, meAttributes me.AttributeValueMap) {
	onuDeviceDB.meDbLock.Lock()
	defer onuDeviceDB.meDbLock.Unlock()
	//filter out the OnuData
	if me.OnuDataClassID == meClassID {
		return
	}

	//logger.Debugw(ctx,"Search for key data :", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID, "meClassID": meClassID, "meEntityID": meEntityID})
	meInstMap, ok := onuDeviceDB.meDb[meClassID]
	if !ok {
		logger.Debugw(ctx, "meClassID not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		meInstMap = make(map[uint16]me.AttributeValueMap)
		onuDeviceDB.meDb[meClassID] = meInstMap
		onuDeviceDB.meDb[meClassID][meEntityID] = meAttributes
	} else {
		meAttribs, ok := meInstMap[meEntityID]
		if !ok {
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"meEntityId not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
			*/
			meInstMap[meEntityID] = meAttributes
		} else {
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"ME-Instance exists already: merge attribute data :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})
			*/
			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			meInstMap[meEntityID] = meAttribs
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"ME-Instance updated :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})
			*/
		}
	}
}

func (onuDeviceDB *onuDeviceDB) GetMe(meClassID me.ClassID, meEntityID uint16) me.AttributeValueMap {
	onuDeviceDB.meDbLock.RLock()
	defer onuDeviceDB.meDbLock.RUnlock()
	if meAttributes, present := onuDeviceDB.meDb[meClassID][meEntityID]; present {
		/* verbose logging, avoid in >= debug level
		logger.Debugw(ctx,"ME found:", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttributes": meAttributes,
			"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		*/
		return meAttributes
	}
	return nil
}

func (onuDeviceDB *onuDeviceDB) getUint32Attrib(meAttribute interface{}) (uint32, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Float64:
		//JSON numbers by default are unmarshaled into values of float64 type if type information is not present
		return uint32(meAttribute.(float64)), nil
	case reflect.Uint32:
		return uint32(meAttribute.(uint32)), nil
	default:
		return uint32(0), fmt.Errorf(fmt.Sprintf("wrong interface-type received-%s", onuDeviceDB.pOnuDeviceEntry.deviceID))
	}
}

/*
func (onuDeviceDB *onuDeviceDB) getUint16Attrib(meAttribute interface{}) (uint16, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Uint16:
		return meAttribute.(uint16), nil
	default:
		return uint16(0), fmt.Errorf(fmt.Sprintf("wrong interface-type received-%s", onuDeviceDB.pOnuDeviceEntry.deviceID))
	}
}
*/

func (onuDeviceDB *onuDeviceDB) getSortedInstKeys(ctx context.Context, meClassID me.ClassID) []uint16 {

	var meInstKeys []uint16
	onuDeviceDB.meDbLock.RLock()
	defer onuDeviceDB.meDbLock.RUnlock()
	meInstMap := onuDeviceDB.meDb[meClassID]

	for k := range meInstMap {
		meInstKeys = append(meInstKeys, k)
	}
	logger.Debugw(ctx, "meInstKeys - input order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	sort.Slice(meInstKeys, func(i, j int) bool { return meInstKeys[i] < meInstKeys[j] })
	logger.Debugw(ctx, "meInstKeys - output order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	return meInstKeys
}

func (onuDeviceDB *onuDeviceDB) logMeDb(ctx context.Context) {
	logger.Debugw(ctx, "ME instances stored for :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
	for meClassID, meInstMap := range onuDeviceDB.meDb {
		for meEntityID, meAttribs := range meInstMap {
			logger.Debugw(ctx, "ME instance: ", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttribs": meAttribs, "device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		}
	}
}
