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

type meDbMap map[me.ClassID]map[uint16]me.AttributeValueMap

//onuDeviceDB structure holds information about known ME's
type onuDeviceDB struct {
	ctx             context.Context
	pOnuDeviceEntry *OnuDeviceEntry
	meDb            meDbMap
}

//newOnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func newOnuDeviceDB(ctx context.Context, aPOnuDeviceEntry *OnuDeviceEntry) *onuDeviceDB {
	logger.Debugw("Init OnuDeviceDB for:", log.Fields{"device-id": aPOnuDeviceEntry.deviceID})
	var onuDeviceDB onuDeviceDB
	onuDeviceDB.ctx = ctx
	onuDeviceDB.pOnuDeviceEntry = aPOnuDeviceEntry
	onuDeviceDB.meDb = make(meDbMap)

	return &onuDeviceDB
}

func (onuDeviceDB *onuDeviceDB) PutMe(meClassID me.ClassID, meEntityID uint16, meAttributes me.AttributeValueMap) {

	//filter out the OnuData
	if me.OnuDataClassID == meClassID {
		return
	}

	//logger.Debugw("Search for key data :", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID, "meClassID": meClassID, "meEntityID": meEntityID})
	meInstMap, ok := onuDeviceDB.meDb[meClassID]
	if !ok {
		logger.Debugw("meClassID not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		meInstMap = make(map[uint16]me.AttributeValueMap)
		onuDeviceDB.meDb[meClassID] = meInstMap
		onuDeviceDB.meDb[meClassID][meEntityID] = meAttributes
	} else {
		meAttribs, ok := meInstMap[meEntityID]
		if !ok {
			/* verbose logging, avoid in >= debug level
			logger.Debugw("meEntityId not found - add to db :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
			*/
			meInstMap[meEntityID] = meAttributes
		} else {
			/* verbose logging, avoid in >= debug level
			logger.Debugw("ME-Instance exists already: merge attribute data :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})
			*/
			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			meInstMap[meEntityID] = meAttribs
			/* verbose logging, avoid in >= debug level
			logger.Debugw("ME-Instance updated :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID, "meAttribs": meAttribs})
			*/
		}
	}
}

func (onuDeviceDB *onuDeviceDB) GetMe(meClassID me.ClassID, meEntityID uint16) me.AttributeValueMap {

	if meAttributes, present := onuDeviceDB.meDb[meClassID][meEntityID]; present {
		/* verbose logging, avoid in >= debug level
		logger.Debugw("ME found:", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttributes": meAttributes,
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

func (onuDeviceDB *onuDeviceDB) getSortedInstKeys(meClassID me.ClassID) []uint16 {

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

func (onuDeviceDB *onuDeviceDB) logMeDb() {
	logger.Debugw("ME instances stored for :", log.Fields{"device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
	for meClassID, meInstMap := range onuDeviceDB.meDb {
		for meEntityID, meAttribs := range meInstMap {
			logger.Debugw("ME instance: ", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttribs": meAttribs, "device-id": onuDeviceDB.pOnuDeviceEntry.deviceID})
		}
	}
}
