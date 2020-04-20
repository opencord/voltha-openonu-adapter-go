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
	"strconv"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	vc "github.com/opencord/voltha-protos/v3/go/common"
	"github.com/opencord/voltha-protos/v3/go/voltha"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

type UniPortType uint8

const (
	// Uni Interface type
	UniPPTP UniPortType = 0 // relates to PPTP
	UniVEIP UniPortType = 1 // relates to VEIP
)

//OntDeviceEntry structure holds information about the attached FSM'as and their communication
type OnuUniPort struct {
	enabled    bool
	name       string
	portNo     uint32
	portType   UniPortType
	ofpPortNo  string
	uniId      uint16
	macBpNo    uint16
	entityId   uint16
	adminState vc.AdminState_Types
	operState  vc.OperStatus_Types
	pPort      *voltha.Port
}

//NewOnuUniPort returns a new instance of a OnuUniPort
func NewOnuUniPort(a_uniId uint16, a_portNo uint32, a_InstNo uint16,
	a_portType UniPortType) *OnuUniPort {
	logger.Infow("init-onuUniPort", log.Fields{"uniId": a_uniId,
		"portNo": a_portNo, "InstNo": a_InstNo, "type": a_portType})
	var onuUniPort OnuUniPort
	onuUniPort.enabled = false
	onuUniPort.name = "uni-" + strconv.FormatUint(uint64(a_portNo), 10)
	onuUniPort.portNo = a_portNo
	onuUniPort.portType = a_portType
	// so far it seems as here ofpPortNo/Name ist the same as the original port name ...??
	onuUniPort.ofpPortNo = onuUniPort.name
	onuUniPort.uniId = a_uniId
	onuUniPort.macBpNo = a_uniId + 1 //ensure >0 instanceNo
	onuUniPort.entityId = a_InstNo
	onuUniPort.adminState = vc.AdminState_ENABLED //enabled per create
	onuUniPort.operState = vc.OperStatus_UNKNOWN
	onuUniPort.pPort = nil // to be set on create
	return &onuUniPort
}

//Start starts (logs) the omci agent
func (oo *OnuUniPort) CreateVolthaPort(a_pDeviceHandler *DeviceHandler) error {
	logger.Debug("adding-uni-port")
	pUniPort := &voltha.Port{
		PortNo:     oo.portNo,
		Label:      oo.name,
		Type:       voltha.Port_ETHERNET_UNI,
		AdminState: oo.adminState,
		OperStatus: oo.operState,
		// obviously empty peer setting
	}
	if pUniPort != nil {
		if err := a_pDeviceHandler.coreProxy.PortCreated(context.TODO(),
			a_pDeviceHandler.deviceID, pUniPort); err != nil {
			logger.Fatalf("adding-uni-port: create-VOLTHA-Port-failed-%s", err)
			return err
		}
		logger.Infow("Voltha onuUniPort-added", log.Fields{"for PortNo": oo.portNo})
		oo.pPort = pUniPort
		oo.operState = vc.OperStatus_DISCOVERED
	} else {
		logger.Warnw("could not create Voltha UniPort - nil pointer",
			log.Fields{"for PortNo": oo.portNo})
		return errors.New("create Voltha UniPort failed")
	}
	return nil
}
