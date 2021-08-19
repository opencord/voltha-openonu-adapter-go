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

//Package common provides global definitions
package common

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v5/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v5/pkg/log"
	vc "github.com/opencord/voltha-protos/v4/go/common"
	of "github.com/opencord/voltha-protos/v4/go/openflow_13"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

//NewOnuUniPort returns a new instance of a OnuUniPort
func NewOnuUniPort(ctx context.Context, aUniID uint8, aPortNo uint32, aInstNo uint16,
	aPortType UniPortType) *OnuUniPort {
	logger.Infow(ctx, "init-onuUniPort", log.Fields{"uniID": aUniID,
		"portNo": aPortNo, "InstNo": aInstNo, "type": aPortType})
	var OnuUniPort OnuUniPort
	OnuUniPort.Enabled = false
	OnuUniPort.Name = "uni-" + strconv.FormatUint(uint64(aPortNo), 10)
	OnuUniPort.PortNo = aPortNo
	OnuUniPort.PortType = aPortType
	// so far it seems as here ofpPortNo/Name ist the same as the original port name ...??
	OnuUniPort.OfpPortNo = OnuUniPort.Name
	OnuUniPort.UniID = aUniID
	OnuUniPort.MacBpNo = aUniID + 1 //ensure >0 instanceNo
	OnuUniPort.EntityID = aInstNo
	OnuUniPort.AdminState = vc.AdminState_ENABLED //enabled per create
	OnuUniPort.OperState = vc.OperStatus_UNKNOWN
	OnuUniPort.PPort = nil // to be set on create
	return &OnuUniPort
}

//CreateVolthaPort creates the Voltha port based on ONU UNI Port and informs the core about it
func (oo *OnuUniPort) CreateVolthaPort(ctx context.Context, apDeviceHandler IdeviceHandler) error {
	logger.Debugw(ctx, "creating-voltha-uni-port", log.Fields{
		"device-id": apDeviceHandler.GetDevice().Id, "portNo": oo.PortNo})
	//200630: per [VOL-3202] OF port info is now to be delivered within UniPort create
	//  not doing so crashes rw_core processing (at least still in 200630 version)
	name := apDeviceHandler.GetDevice().SerialNumber + "-" + strconv.FormatUint(uint64(oo.MacBpNo), 10)
	var macOctets [6]uint8
	macOctets[5] = 0x08
	macOctets[4] = uint8(*apDeviceHandler.GetPonPortNumber() >> 8)
	macOctets[3] = uint8(*apDeviceHandler.GetPonPortNumber())
	macOctets[2] = uint8(oo.PortNo >> 16)
	macOctets[1] = uint8(oo.PortNo >> 8)
	macOctets[0] = uint8(oo.PortNo)
	hwAddr := genMacFromOctets(macOctets)
	ofHwAddr := macAddressToUint32Array(hwAddr)
	capacity := uint32(of.OfpPortFeatures_OFPPF_1GB_FD | of.OfpPortFeatures_OFPPF_FIBER)
	ofUniPortState := of.OfpPortState_OFPPS_LINK_DOWN
	/* as the VOLTHA port create is only called directly after Uni Port create
	   the OfPortOperState is always Down
	   Note: this way the OfPortOperState won't ever change (directly in adapter)
	   maybe that was already always the case, but looks a bit weird - to be kept in mind ...
		if pUniPort.operState == vc.OperStatus_ACTIVE {
			ofUniPortState = of.OfpPortState_OFPPS_LIVE
		}
	*/
	logger.Debugw(ctx, "ofPort values", log.Fields{
		"forUniPortName": oo.Name, "forMacBase": hwAddr,
		"name": name, "hwAddr": ofHwAddr, "OperState": ofUniPortState})

	pUniPort := &voltha.Port{
		PortNo:     oo.PortNo,
		Label:      oo.Name,
		Type:       voltha.Port_ETHERNET_UNI,
		AdminState: oo.AdminState,
		OperStatus: oo.OperState,
		// obviously empty peer setting
		OfpPort: &of.OfpPort{
			Name:       name,
			HwAddr:     ofHwAddr,
			Config:     0,
			State:      uint32(ofUniPortState),
			Curr:       capacity,
			Advertised: capacity,
			Peer:       capacity,
			CurrSpeed:  1000,
			MaxSpeed:   1000,
		},
	}
	maxRetry := 3
	retryCnt := 0
	var err error
	for retryCnt = 0; retryCnt < maxRetry; retryCnt++ {
		if err = apDeviceHandler.GetCoreProxy().PortCreated(log.WithSpanFromContext(context.TODO(), ctx),
			apDeviceHandler.GetDeviceID(), pUniPort); err != nil {
			logger.Errorf(ctx, "Device FSM: PortCreated-failed-%s, retrying after a delay", err)
			// retry after a sleep
			time.Sleep(2 * time.Second)
		} else {
			// success, break from retry loop
			break
		}
	}
	if retryCnt == maxRetry { // maxed out..
		logger.Errorf(ctx, "Device FSM: PortCreated-failed-%s", err)
		return fmt.Errorf("device-fsm-port-create-failed-%s", err)
	}
	logger.Infow(ctx, "Voltha OnuUniPort-added", log.Fields{
		"device-id": apDeviceHandler.GetDevice().Id, "PortNo": oo.PortNo})
	oo.PPort = pUniPort
	oo.OperState = vc.OperStatus_DISCOVERED

	return nil
}

//SetOperState modifies OperState of the the UniPort
func (oo *OnuUniPort) SetOperState(aNewOperState vc.OperStatus_Types) {
	oo.OperState = aNewOperState
}

// uni port related utility functions (so far only used here)
func genMacFromOctets(aOctets [6]uint8) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		aOctets[5], aOctets[4], aOctets[3],
		aOctets[2], aOctets[1], aOctets[0])
}

//copied from OLT Adapter: unify centrally ?
func macAddressToUint32Array(mac string) []uint32 {
	slist := strings.Split(mac, ":")
	result := make([]uint32, len(slist))
	var err error
	var tmp int64
	for index, val := range slist {
		if tmp, err = strconv.ParseInt(val, 16, 32); err != nil {
			return []uint32{1, 2, 3, 4, 5, 6}
		}
		result[index] = uint32(tmp)
	}
	return result
}
