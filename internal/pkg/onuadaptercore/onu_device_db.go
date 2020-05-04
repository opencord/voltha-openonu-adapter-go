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
	"errors"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
)

//OnuDeviceDB structure holds information about known ME's
type OnuDeviceDB struct {
	ctx               context.Context
	pOnuDeviceEntry   *OnuDeviceEntry
	unigMeCount       uint16
	unigMe            []*me.ManagedEntity
	pptpEthUniMeCount uint16
	pptpEthUniMe      []*me.ManagedEntity
	AnigMe            *me.ManagedEntity
	VeipMe            *me.ManagedEntity
}

//OnuDeviceDB returns a new instance for a specific ONU_Device_Entry
func NewOnuDeviceDB(ctx context.Context, a_pOnuDeviceEntry *OnuDeviceEntry) *OnuDeviceDB {
	logger.Debugw("Init OnuDeviceDB for:", log.Fields{"deviceId": a_pOnuDeviceEntry.deviceID})
	var onuDeviceDB OnuDeviceDB
	onuDeviceDB.ctx = ctx
	onuDeviceDB.pOnuDeviceEntry = a_pOnuDeviceEntry
	onuDeviceDB.unigMeCount = 0
	onuDeviceDB.unigMe = make([]*me.ManagedEntity, 4, MaxUnisPerOnu)
	onuDeviceDB.pptpEthUniMeCount = 0
	onuDeviceDB.pptpEthUniMe = make([]*me.ManagedEntity, 4, MaxUnisPerOnu)
	onuDeviceDB.AnigMe = nil
	onuDeviceDB.VeipMe = nil
	return &onuDeviceDB
}

func (onuDeviceDB *OnuDeviceDB) UnigAdd(meParamData me.ParamData) error {
	var omciErr me.OmciErrors
	onuDeviceDB.unigMe[onuDeviceDB.unigMeCount], omciErr = me.NewUniG(meParamData)
	if omciErr.StatusCode() != me.Success {
		logger.Errorw("UniG could not be parsed for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("UniG could not be parsed")
	}
	logger.Debugw("UniG instance stored for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID,
		"UnigMe ": onuDeviceDB.unigMe[onuDeviceDB.unigMeCount], "unigMeCount": onuDeviceDB.unigMeCount})
	if onuDeviceDB.unigMeCount < MaxUnisPerOnu {
		onuDeviceDB.unigMeCount++
	} else {
		logger.Errorw("Max number of UniGs exceeded for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("Max number of UniGs exceeded")
	}
	return nil
}

func (onuDeviceDB *OnuDeviceDB) PptpEthUniAdd(meParamData me.ParamData) error {
	var omciErr me.OmciErrors
	onuDeviceDB.pptpEthUniMe[onuDeviceDB.pptpEthUniMeCount], omciErr = me.NewPhysicalPathTerminationPointEthernetUni(meParamData)
	if omciErr.StatusCode() != me.Success {
		logger.Errorw("pptpEthUni could not be parsed for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("pptpEthUni could not be parsed")
	}
	logger.Debugw("pptpEthUni instance stored for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID,
		"pptpEthUniMe ": onuDeviceDB.pptpEthUniMe[onuDeviceDB.pptpEthUniMeCount], "pptpEthUniMeCount": onuDeviceDB.pptpEthUniMeCount})
	if onuDeviceDB.pptpEthUniMeCount < MaxUnisPerOnu {
		onuDeviceDB.pptpEthUniMeCount++
	} else {
		logger.Errorw("Max number of pptpEthUnis exceeded for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("Max number of pptpEthUnis exceeded")
	}
	return nil
}

func (onuDeviceDB *OnuDeviceDB) AnigAdd(meParamData me.ParamData) error {
	var omciErr me.OmciErrors
	onuDeviceDB.AnigMe, omciErr = me.NewAniG(meParamData)
	if omciErr.StatusCode() != me.Success {
		logger.Errorw("AniG could not be parsed for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("AniG could not be parsed")
	}
	logger.Debugw("AniG instance stored for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID, "AnigMe ": onuDeviceDB.AnigMe})
	return nil
}

func (onuDeviceDB *OnuDeviceDB) VeipAdd(meParamData me.ParamData) error {
	var omciErr me.OmciErrors
	onuDeviceDB.VeipMe, omciErr = me.NewVirtualEthernetInterfacePoint(meParamData)
	if omciErr.StatusCode() != me.Success {
		logger.Errorw("VEIP could not be parsed for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID})
		return errors.New("VEIP could not be parsed")
	}
	logger.Debugw("VEIP instance stored for:", log.Fields{"deviceId": onuDeviceDB.pOnuDeviceEntry.deviceID, "VeipMe ": onuDeviceDB.VeipMe})
	return nil
}

func (onuDeviceDB *OnuDeviceDB) StoreMe(a_pMibUpResp *omci.MibUploadNextResponse) error {

	meParamData := me.ParamData{
		EntityID:   a_pMibUpResp.ReportedME.GetEntityID(),
		Attributes: a_pMibUpResp.ReportedME.GetAttributeValueMap(),
	}

	switch a_pMibUpResp.ReportedME.GetClassID() {
	case me.UniGClassID:
		onuDeviceDB.UnigAdd(meParamData)
	case me.PhysicalPathTerminationPointEthernetUniClassID:
		onuDeviceDB.PptpEthUniAdd(meParamData)
	case me.AniGClassID:
		onuDeviceDB.AnigAdd(meParamData)
	case me.VirtualEthernetInterfacePointClassID:
		onuDeviceDB.VeipAdd(meParamData)
	default:
		//ME won't be stored currently
	}
	return nil
}
