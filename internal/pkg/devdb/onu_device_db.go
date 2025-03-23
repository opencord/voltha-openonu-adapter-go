/*
 * Copyright 2020-2024 Open Networking Foundation (ONF) and the ONF Contributors
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

// Package devdb provides access to internal device ME DB
package devdb

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"sync"

	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
)

// MeDbMap type holds MEs that are known to the ONT.
type MeDbMap map[me.ClassID]map[uint16]me.AttributeValueMap

// UnknownMeAndAttribDbMap type holds MEs that are Unknown to the ONT.
type UnknownMeAndAttribDbMap map[UnknownMeOrAttribName]map[me.ClassID]map[uint16]unknownAttribs

// OnuMCmnMEDBMap type holds MEs that are classified as  Unknown to the ONT.
type OnuMCmnMEDBMap map[string]*OnuCmnMEDB

// UnknownMeOrAttribName type to be used for unknown ME and attribute names
type UnknownMeOrAttribName string

// names for unknown ME and attribute identifiers
const (
	CUnknownItuG988ManagedEntity        = "UnknownItuG988ManagedEntity"
	CUnknownVendorSpecificManagedEntity = "UnknownVendorSpecificManagedEntity"
	CUnknownAttributesManagedEntity     = "UnknownAttributesManagedEntity"
)

// CStartUnknownMeAttribsInBaseLayerPayload defines start of unknown ME attribs after ClassID, InstanceID and AttribMask
const CStartUnknownMeAttribsInBaseLayerPayload = 6

type unknownAttribs struct {
	AttribMask  string `json:"AttributeMask"`
	AttribBytes string `json:"AttributeBytes"`
}

// OnuDeviceDB structure holds information about ME's
type OnuDeviceDB struct {
	ctx                 context.Context
	CommonMeDb          *OnuCmnMEDB // Reference to OnuCmnMEDB
	OnuSpecificMeDb     MeDbMap
	deviceID            string
	OnuSpecificMeDbLock sync.RWMutex
}

// MIBUploadStatus represents the status of MIBUpload for a particular ONT.
type MIBUploadStatus int

// Values for Status of the ONT MIB Upload.
const (
	NotStarted MIBUploadStatus = iota // MIB upload has not started
	InProgress                        // MIB upload is in progress
	Failed                            // MIB upload has failed
	Completed                         // MIB upload is completed
)

// OnuCmnMEDB structure holds information about ME's common to ONT possessing same MIB Template.
type OnuCmnMEDB struct {
	MeDb                 MeDbMap
	UnknownMeAndAttribDb UnknownMeAndAttribDbMap
	MIBUploadStatus      MIBUploadStatus
	MeDbLock             sync.RWMutex
}

// NewOnuCmnMEDB returns a new instance of OnuCmnMEDB
func NewOnuCmnMEDB(ctx context.Context) *OnuCmnMEDB {

	return &OnuCmnMEDB{
		MeDbLock:             sync.RWMutex{},
		MeDb:                 make(MeDbMap),
		UnknownMeAndAttribDb: make(UnknownMeAndAttribDbMap),
		MIBUploadStatus:      NotStarted,
	}
}

// NewOnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func NewOnuDeviceDB(ctx context.Context, aDeviceID string) *OnuDeviceDB {
	logger.Debugw(ctx, "Init OnuDeviceDB for:", log.Fields{"device-id": aDeviceID})
	//nolint:govet
	var OnuDeviceDB OnuDeviceDB
	OnuDeviceDB.ctx = ctx
	OnuDeviceDB.deviceID = aDeviceID
	OnuDeviceDB.CommonMeDb = nil
	OnuDeviceDB.OnuSpecificMeDb = make(MeDbMap)

	return &OnuDeviceDB
}

// PutMe puts an ME instance into internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) PutMe(ctx context.Context, meClassID me.ClassID, meEntityID uint16, meAttributes me.AttributeValueMap) {

	//filter out the OnuData
	if me.OnuDataClassID == meClassID {
		return
	}

	// Dereference the MeDb pointer
	if OnuDeviceDB.CommonMeDb == nil || OnuDeviceDB.CommonMeDb.MeDb == nil {
		logger.Errorw(ctx, "MeDb is nil", log.Fields{"device-id": OnuDeviceDB.deviceID})
		return
	}
	meDb := OnuDeviceDB.CommonMeDb.MeDb
	_, ok := meDb[meClassID]
	if !ok {
		logger.Debugw(ctx, "meClassID not found - add to db :", log.Fields{"device-id": OnuDeviceDB.deviceID})
		meDb[meClassID] = make(map[uint16]me.AttributeValueMap)
		meDb[meClassID][meEntityID] = make(me.AttributeValueMap)
		meDb[meClassID][meEntityID] = meAttributes
	} else {
		meAttribs, ok := meDb[meClassID][meEntityID]
		if !ok {
			meDb[meClassID][meEntityID] = make(me.AttributeValueMap)
			meDb[meClassID][meEntityID] = meAttributes
		} else {
			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			meDb[meClassID][meEntityID] = meAttribs
		}
	}
}

// GetMe returns an ME instance from internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) GetMe(meClassID me.ClassID, meEntityID uint16) me.AttributeValueMap {
	OnuDeviceDB.CommonMeDb.MeDbLock.RLock()
	defer OnuDeviceDB.CommonMeDb.MeDbLock.RUnlock()

	// Check in the common MeDb
	if meAttributes, present := OnuDeviceDB.CommonMeDb.MeDb[meClassID][meEntityID]; present {
		return meAttributes
	}

	OnuDeviceDB.OnuSpecificMeDbLock.RLock()
	defer OnuDeviceDB.OnuSpecificMeDbLock.RUnlock()

	// Check in the ONU-specific MeDb
	if meAttributes, present := OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID]; present {
		return meAttributes
	}
	return nil
}

// GetUint32Attrib converts JSON numbers to uint32
func (OnuDeviceDB *OnuDeviceDB) GetUint32Attrib(meAttribute interface{}) (uint32, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Float64:
		//JSON numbers by default are unmarshaled into values of float64 type if type information is not present
		return uint32(meAttribute.(float64)), nil
	case reflect.Uint32:
		return meAttribute.(uint32), nil
	default:
		return uint32(0), fmt.Errorf("wrong-interface-type-%v-received-for-device-%s", reflect.TypeOf(meAttribute).Kind(), OnuDeviceDB.deviceID)
	}
}

// GetUint16Attrib converts JSON numbers to uint16
func (OnuDeviceDB *OnuDeviceDB) GetUint16Attrib(meAttribute interface{}) (uint16, error) {

	switch reflect.TypeOf(meAttribute).Kind() {
	case reflect.Float64:
		//JSON numbers by default are unmarshaled into values of float64 type if type information is not present
		return uint16(meAttribute.(float64)), nil
	case reflect.Uint16:
		return meAttribute.(uint16), nil
	default:
		return uint16(0), fmt.Errorf("wrong-interface-type-%v-received-for-device-%s", reflect.TypeOf(meAttribute).Kind(), OnuDeviceDB.deviceID)
	}
}

// GetSortedInstKeys returns a sorted list of all instances of an ME
func (OnuDeviceDB *OnuDeviceDB) GetSortedInstKeys(ctx context.Context, meClassID me.ClassID) []uint16 {

	var meInstKeys []uint16
	OnuDeviceDB.CommonMeDb.MeDbLock.RLock()
	defer OnuDeviceDB.CommonMeDb.MeDbLock.RUnlock()
	meDb := OnuDeviceDB.CommonMeDb.MeDb

	// Check if the class ID exists in the MeDb map
	if meInstMap, found := meDb[meClassID]; found {
		for k := range meInstMap {
			meInstKeys = append(meInstKeys, k)
		}
		logger.Debugw(ctx, "meInstKeys - input order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!
	}
	sort.Slice(meInstKeys, func(i, j int) bool { return meInstKeys[i] < meInstKeys[j] })
	logger.Debugw(ctx, "meInstKeys - output order :", log.Fields{"meInstKeys": meInstKeys}) //TODO: delete the line after test phase!

	// Return the sorted instance keys
	return meInstKeys
}

// GetNumberOfInst returns the number of instances of an ME
func (OnuDeviceDB *OnuDeviceDB) GetNumberOfInst(meClassID me.ClassID) int {
	var count int

	OnuDeviceDB.CommonMeDb.MeDbLock.RLock()
	defer OnuDeviceDB.CommonMeDb.MeDbLock.RUnlock()

	if meClassMap, found := OnuDeviceDB.CommonMeDb.MeDb[meClassID]; found {
		count = len(meClassMap)
	}

	OnuDeviceDB.OnuSpecificMeDbLock.RLock()
	defer OnuDeviceDB.OnuSpecificMeDbLock.RUnlock()

	if onuSpecificMap, found := OnuDeviceDB.OnuSpecificMeDb[meClassID]; found {
		count += len(onuSpecificMap)
	}

	return count
}

// LogMeDb logs content of internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) LogMeDb(ctx context.Context) {
	logger.Debugw(ctx, "Logging ME instances for device:", log.Fields{"device-id": OnuDeviceDB.deviceID})

	if OnuDeviceDB.CommonMeDb != nil && OnuDeviceDB.CommonMeDb.MeDb != nil {
		meDb := OnuDeviceDB.CommonMeDb.MeDb

		for meClassID, meInstMap := range meDb {
			for meEntityID, meAttribs := range meInstMap {
				logger.Debugw(ctx, "Common ME instance: ", log.Fields{"meClassID": meClassID, "meEntityID": meEntityID, "meAttribs": meAttribs,
					"device-id": OnuDeviceDB.deviceID})
			}
		}
	} else {
		logger.Warnw(ctx, "Common MeDb is nil", log.Fields{"device-id": OnuDeviceDB.deviceID})
	}

	// Log ONU-specific ME instances
	OnuDeviceDB.OnuSpecificMeDbLock.RLock()
	defer OnuDeviceDB.OnuSpecificMeDbLock.RUnlock()

	for meClassID, meInstMap := range OnuDeviceDB.OnuSpecificMeDb {
		for meEntityID, meAttribs := range meInstMap {
			logger.Debugw(ctx, "ONU Specific ME instance:", log.Fields{
				"meClassID":  meClassID,
				"meEntityID": meEntityID,
				"meAttribs":  meAttribs,
				"device-id":  OnuDeviceDB.deviceID,
			})
		}
	}

}

// PutUnknownMeOrAttrib puts an instance with unknown ME or attributes into internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) PutUnknownMeOrAttrib(ctx context.Context, aMeName UnknownMeOrAttribName, aMeClassID me.ClassID, aMeEntityID uint16,
	aMeAttributeMask uint16, aMePayload []byte) {

	meAttribMaskStr := fmt.Sprintf("0x%04x", aMeAttributeMask)
	attribs := unknownAttribs{meAttribMaskStr, hex.EncodeToString(aMePayload)}

	unknownMeDb := OnuDeviceDB.CommonMeDb.UnknownMeAndAttribDb

	_, ok := unknownMeDb[aMeName]
	if !ok {
		unknownMeDb[aMeName] = make(map[me.ClassID]map[uint16]unknownAttribs)
		unknownMeDb[aMeName][aMeClassID] = make(map[uint16]unknownAttribs)
		unknownMeDb[aMeName][aMeClassID][aMeEntityID] = attribs
	} else {
		_, ok := unknownMeDb[aMeName][aMeClassID]
		if !ok {
			unknownMeDb[aMeName][aMeClassID] = make(map[uint16]unknownAttribs)
			unknownMeDb[aMeName][aMeClassID][aMeEntityID] = attribs
		} else {
			_, ok := unknownMeDb[aMeName][aMeClassID][aMeEntityID]
			if !ok {
				unknownMeDb[aMeName][aMeClassID][aMeEntityID] = attribs
			}
		}
	}
}

// DeleteMe deletes an ME instance from internal ONU DB
func (OnuDeviceDB *OnuDeviceDB) DeleteMe(meClassID me.ClassID, meEntityID uint16) {
	OnuDeviceDB.CommonMeDb.MeDbLock.Lock()
	defer OnuDeviceDB.CommonMeDb.MeDbLock.Unlock()
	meDb := OnuDeviceDB.CommonMeDb.MeDb
	delete(meDb[meClassID], meEntityID)

	OnuDeviceDB.OnuSpecificMeDbLock.Lock()
	defer OnuDeviceDB.OnuSpecificMeDbLock.Unlock()
	delete(OnuDeviceDB.OnuSpecificMeDb[meClassID], meEntityID)
}

// PutOnuSpeficMe puts an ME instance into specifically to the ONU DB maintained per ONU.
func (OnuDeviceDB *OnuDeviceDB) PutOnuSpeficMe(ctx context.Context, meClassID me.ClassID, meEntityID uint16, meAttributes me.AttributeValueMap) {
	OnuDeviceDB.OnuSpecificMeDbLock.Lock()
	defer OnuDeviceDB.OnuSpecificMeDbLock.Unlock()
	//filter out the OnuData
	if me.OnuDataClassID == meClassID {
		return
	}
	_, ok := OnuDeviceDB.OnuSpecificMeDb[meClassID]
	if !ok {
		logger.Debugw(ctx, "meClassID not found - add to db :", log.Fields{"device-id": OnuDeviceDB.deviceID})
		OnuDeviceDB.OnuSpecificMeDb[meClassID] = make(map[uint16]me.AttributeValueMap)
		OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID] = make(me.AttributeValueMap)
		OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID] = meAttributes
	} else {
		meAttribs, ok := OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID]
		if !ok {
			OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID] = make(me.AttributeValueMap)
			OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID] = meAttributes
		} else {
			for k, v := range meAttributes {
				meAttribs[k] = v
			}
			OnuDeviceDB.OnuSpecificMeDb[meClassID][meEntityID] = meAttribs
		}
	}
}
