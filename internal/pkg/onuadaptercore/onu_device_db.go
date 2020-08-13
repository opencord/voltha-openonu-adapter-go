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

	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
)

type MeDbMap map[me.ClassID]map[uint16]me.AttributeValueMap

//OnuDeviceDB structure holds information about known ME's
type OnuDeviceDB struct {
	ctx             context.Context
	pOnuDeviceEntry *OnuDeviceEntry
	meDb            MeDbMap
}

//OnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func NewOnuDeviceDB(ctx context.Context, a_pOnuDeviceEntry *OnuDeviceEntry) *OnuDeviceDB {
	logger.Debugw("Init OnuDeviceDB for:", log.Fields{"device-id": a_pOnuDeviceEntry.deviceID})
	var onuDeviceDB OnuDeviceDB
	onuDeviceDB.ctx = ctx
	onuDeviceDB.pOnuDeviceEntry = a_pOnuDeviceEntry
	onuDeviceDB.meDb = make(MeDbMap)

	return &onuDeviceDB
}

func (onuDeviceDB *OnuDeviceDB) PutMe(meClassId me.ClassID, meEntityId uint16, meAttributes me.AttributeValueMap) {

	//filter out the OnuData
	if me.OnuDataClassID == meClassId {
		return
	}

	//logger.Debugw("Search for key data :", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID, "meClassId": meClassId, "meEntityId": meEntityId})
	meInstMap, ok := onuDeviceDB.meDb[meClassId]
	if !ok {
		logger.Debugw("meClassId not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		meInstMap = make(map[uint16]me.AttributeValueMap)
		onuDeviceDB.meDb[meClassId] = meInstMap
		onuDeviceDB.meDb[meClassId][meEntityId] = meAttributes
	} else {
		meAttribs, ok := onuDeviceDB.meDb[meClassId][meEntityId]
		if !ok {
			logger.Debugw("meEntityId not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
			onuDeviceDB.meDb[meClassId][meEntityId] = meAttributes
		} else {
			logger.Debugw("ME-Instance exists already: merge attribute data :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})

			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			onuDeviceDB.meDb[meClassId][meEntityId] = meAttribs
			logger.Debugw("ME-Instance updated :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})
		}
	}
}

func (onuDeviceDB *OnuDeviceDB) GetMe(meClassId me.ClassID, meEntityId uint16) me.AttributeValueMap {

	if meAttributes, present := onuDeviceDB.meDb[meClassId][meEntityId]; present {
		logger.Debugw("ME found:", log.Fields{"meClassId": meClassId, "meEntityId": meEntityId, "meAttributes": meAttributes,
			"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return meAttributes
	} else {
		return nil
	}
}

func (onuDeviceDB *OnuDeviceDB) GetUint32Attrib(meAttribute interface{}) (uint32, error) {

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

func (onuDeviceDB *OnuDeviceDB) GetSortedInstKeys(meClassID me.ClassID) []uint16 {

	var meInstKeys []uint16

	meInstMap := onuDeviceDB.meDb[meClassID]

	for k := range meInstMap {
		meInstKeys = append(meInstKeys, k)
	}
	logger.Debugw("meInstKeys - input order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	sort.Slice(meInstKeys, func(i, j int) bool { return meInstKeys[i] < meInstKeys[j] })
	logger.Debugw("meInstKeys - output order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	return meInstKeys
}

func (onuDeviceDB *OnuDeviceDB) LogMeDb() {
	logger.Debugw("ME instances stored for :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
	for meClassId, meInstMap := range onuDeviceDB.meDb {
		for meEntityId, meAttribs := range meInstMap {
			logger.Debugw("ME instance: ", log.Fields{"meClassId": meClassId, "meEntityId": meEntityId, "meAttribs": meAttribs, "device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		}
	}
}
