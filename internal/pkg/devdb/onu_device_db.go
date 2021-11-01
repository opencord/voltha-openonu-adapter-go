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

//Package devdb provides access to internal device ME DB
package devdb

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
)

type meDbMap map[me.ClassID]map[uint16]me.AttributeValueMap

//OnuDeviceDB structure holds information about known ME's
type OnuDeviceDB struct {
	ctx      context.Context
	deviceID string
	MeDb     meDbMap
	meDbLock sync.RWMutex
}

//NewOnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func NewOnuDeviceDB(ctx context.Context, aDeviceID string) *OnuDeviceDB {
	logger.Debugw(ctx, "Init OnuDeviceDB for:", log.Fields{"device-id": aDeviceID})
	var OnuDeviceDB OnuDeviceDB
	OnuDeviceDB.ctx = ctx
	OnuDeviceDB.deviceID = aDeviceID
	OnuDeviceDB.MeDb = make(meDbMap)

	return &OnuDeviceDB
}

//PutMe puts an ME instance into internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) PutMe(ctx context.Context, meClassID me.ClassID, meEntityID uint16, meAttributes me.AttributeValueMap) {
	OnuDeviceDB.meDbLock.Lock()
	defer OnuDeviceDB.meDbLock.Unlock()
	//filter out the OnuData
	if me.OnuDataClassID == meClassID {
		return
	}

	//logger.Debugw(ctx,"Search for key data :", log.Fields{"deviceId": OnuDeviceDB.deviceID, "meClassID": meClassID, "meEntityID": meEntityID})
	meInstMap, ok := OnuDeviceDB.MeDb[meClassID]
	if !ok {
		logger.Debugw(ctx, "meClassID not found - add to db :", log.Fields{"device-id": OnuDeviceDB.deviceID})
		meInstMap = make(map[uint16]me.AttributeValueMap)
		OnuDeviceDB.MeDb[meClassID] = meInstMap
		OnuDeviceDB.MeDb[meClassID][meEntityID] = meAttributes
	} else {
		meAttribs, ok := meInstMap[meEntityID]
		if !ok {
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"meEntityId not found - add to db :", log.Fields{"device-id": OnuDeviceDB.deviceID})
			*/
			meInstMap[meEntityID] = meAttributes
		} else {
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"ME-Instance exists already: merge attribute data :", log.Fields{"device-id": OnuDeviceDB.deviceID, "meAttribs": meAttribs})
			*/
			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			meInstMap[meEntityID] = meAttribs
			/* verbose logging, avoid in >= debug level
			logger.Debugw(ctx,"ME-Instance updated :", log.Fields{"device-id": OnuDeviceDB.deviceID, "meAttribs": meAttribs})
			*/
		}
	}
}

//GetMe returns an ME instance from internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) GetMe(meClassID me.ClassID, meEntityID uint16) me.AttributeValueMap {
	OnuDeviceDB.meDbLock.RLock()
	defer OnuDeviceDB.meDbLock.RUnlock()
	if meAttributes, present := OnuDeviceDB.MeDb[meClassID][meEntityID]; present {
		/* verbose logging, avoid in >= debug level
		logger.Debugw(ctx,"ME found:", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttributes": meAttributes,
			"device-id": OnuDeviceDB.deviceID})
		*/
		return meAttributes
	}
	return nil
}

//GetUint32Attrib converts JSON numbers to uint32
func (OnuDeviceDB *OnuDeviceDB) GetUint32Attrib(meAttribute interface{}) (uint32, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Float64:
		//JSON numbers by default are unmarshaled into values of float64 type if type information is not present
		return uint32(meAttribute.(float64)), nil
	case reflect.Uint32:
		return meAttribute.(uint32), nil
	default:
		return uint32(0), fmt.Errorf(fmt.Sprintf("wrong-interface-type-%v-received-for-device-%s", reflect.TypeOf(meAttribute).Kind(), OnuDeviceDB.deviceID))
	}
}

//GetUint16Attrib converts JSON numbers to uint16
func (OnuDeviceDB *OnuDeviceDB) GetUint16Attrib(meAttribute interface{}) (uint16, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Float64:
		//JSON numbers by default are unmarshaled into values of float64 type if type information is not present
		return uint16(meAttribute.(float64)), nil
	case reflect.Uint16:
		return meAttribute.(uint16), nil
	default:
		return uint16(0), fmt.Errorf(fmt.Sprintf("wrong-interface-type-%v-received-for-device-%s", reflect.TypeOf(meAttribute).Kind(), OnuDeviceDB.deviceID))
	}
}

//GetSortedInstKeys returns a sorted list of all instances of an ME
func (OnuDeviceDB *OnuDeviceDB) GetSortedInstKeys(ctx context.Context, meClassID me.ClassID) []uint16 {

	var meInstKeys []uint16
	OnuDeviceDB.meDbLock.RLock()
	defer OnuDeviceDB.meDbLock.RUnlock()
	meInstMap := OnuDeviceDB.MeDb[meClassID]

	for k := range meInstMap {
		meInstKeys = append(meInstKeys, k)
	}
	logger.Debugw(ctx, "meInstKeys - input order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	sort.Slice(meInstKeys, func(i, j int) bool { return meInstKeys[i] < meInstKeys[j] })
	logger.Debugw(ctx, "meInstKeys - output order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	return meInstKeys
}

//LogMeDb logs content of internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) LogMeDb(ctx context.Context) {
	logger.Debugw(ctx, "ME instances stored for :", log.Fields{"device-id": OnuDeviceDB.deviceID})
	for meClassID, meInstMap := range OnuDeviceDB.MeDb {
		for meEntityID, meAttribs := range meInstMap {
			logger.Debugw(ctx, "ME instance: ", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttribs": meAttribs, "device-id": OnuDeviceDB.deviceID})
		}
	}
}
