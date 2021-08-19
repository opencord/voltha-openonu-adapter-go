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

//Package uniprt provides the utilities for uni port configuration
package uniprt

import (
	"context"
	"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-protos/v5/go/extension"
)

const (
	uniStatusTimeout = 3
	adminState       = "AdministrativeState"
	operationalState = "OperationalState"
	configInd        = "ConfigurationInd"
)

//UniPortStatus implements methods to get uni port status info
type UniPortStatus struct {
	pDeviceHandler    cmn.IdeviceHandler
	pOmiCC            *cmn.OmciCC
	omciRespChn       chan cmn.Message
	pLastTxMeInstance *me.ManagedEntity
}

//NewUniPortStatus creates a new instance of UniPortStatus
func NewUniPortStatus(apDeviceHandler cmn.IdeviceHandler, apOmicc *cmn.OmciCC) *UniPortStatus {
	return &UniPortStatus{
		pDeviceHandler: apDeviceHandler,
		pOmiCC:         apOmicc,
		omciRespChn:    make(chan cmn.Message),
	}

}

// GetUniPortStatus - TODO: add comment
func (portStatus *UniPortStatus) GetUniPortStatus(ctx context.Context, uniIdx uint32) *extension.SingleGetValueResponse {
	for _, uniPort := range *portStatus.pDeviceHandler.GetUniEntityMap() {

		if uniPort.UniID == uint8(uniIdx) && uniPort.PortType == cmn.UniPPTP {

			requestedAttributes := me.AttributeValueMap{adminState: 0, operationalState: 0, configInd: 0}
			// Note: No reference to fetch the OMCI timeout configuration value, so hard code it to 10s
			meInstance, err := portStatus.pOmiCC.SendGetMe(ctx, me.PhysicalPathTerminationPointEthernetUniClassID, uniPort.EntityID, requestedAttributes, 10, true, portStatus.omciRespChn)
			if err != nil {
				return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)
			}
			if meInstance != nil {
				portStatus.pLastTxMeInstance = meInstance

				//verify response
				return portStatus.waitforGetUniPortStatus(ctx, meInstance)
			}
		}
	}
	logger.Errorw(ctx, "GetUniPortStatus uniIdx is not valid", log.Fields{"uniIdx": uniIdx})
	return PostUniStatusErrResponse(extension.GetValueResponse_INVALID_PORT_TYPE)
}

func (portStatus *UniPortStatus) waitforGetUniPortStatus(ctx context.Context, apMeInstance *me.ManagedEntity) *extension.SingleGetValueResponse {

	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	case <-ctx.Done():
		logger.Errorf(ctx, "waitforGetUniPortStatus Context done")
		return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)
	case <-time.After(uniStatusTimeout * time.Second):
		logger.Errorf(ctx, "waitforGetUniPortStatus  timeout")
		return PostUniStatusErrResponse(extension.GetValueResponse_TIMEOUT)

	case omciMsg := <-portStatus.omciRespChn:
		if omciMsg.Type != cmn.OMCI {
			return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)
		}
		msg, _ := omciMsg.Data.(cmn.OmciMessage)
		return portStatus.processGetUnitStatusResp(ctx, msg)
	}

}

func (portStatus *UniPortStatus) processGetUnitStatusResp(ctx context.Context, msg cmn.OmciMessage) *extension.SingleGetValueResponse {
	logger.Debugw(ctx, "processGetUniStatusResp:", log.Fields{"msg.Omci.MessageType": msg.OmciMsg.MessageType,
		"msg.OmciMsg.TransactionID": msg.OmciMsg.TransactionID, "DeviceIdentfier": msg.OmciMsg.DeviceIdentifier})

	if msg.OmciMsg.MessageType != omci.GetResponseType {
		logger.Debugw(ctx, "processGetUniStatusResp error", log.Fields{"incorrect RespType": msg.OmciMsg.MessageType,
			"expected": omci.GetResponseType})
		return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)
	}

	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorf(ctx, "processGetUniStatusResp omci Msg layer not found - ")
		return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)

	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorf(ctx, "processGetUniStatusResp omci msgObj layer could not be found ")
		return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)

	}

	if msgObj.Result != me.Success {
		return PostUniStatusErrResponse(extension.GetValueResponse_INTERNAL_ERROR)
	}
	meAttributes := msgObj.Attributes

	singleValResp := extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status: extension.GetValueResponse_OK,
			Response: &extension.GetValueResponse_UniInfo{
				UniInfo: &extension.GetOnuUniInfoResponse{},
			},
		},
	}
	if meAttributes[operationalState].(uint8) == 0 {
		singleValResp.Response.GetUniInfo().OperState = extension.GetOnuUniInfoResponse_ENABLED
	} else if meAttributes[operationalState].(uint8) == 1 {
		singleValResp.Response.GetUniInfo().OperState = extension.GetOnuUniInfoResponse_DISABLED
	} else {
		singleValResp.Response.GetUniInfo().OperState = extension.GetOnuUniInfoResponse_OPERSTATE_UNDEFINED
	}

	if meAttributes[adminState].(uint8) == 0 {
		singleValResp.Response.GetUniInfo().AdmState = extension.GetOnuUniInfoResponse_UNLOCKED
	} else if meAttributes[adminState].(uint8) == 1 {
		singleValResp.Response.GetUniInfo().AdmState = extension.GetOnuUniInfoResponse_LOCKED
	} else {
		singleValResp.Response.GetUniInfo().AdmState = extension.GetOnuUniInfoResponse_ADMSTATE_UNDEFINED
	}
	configIndMap := map[uint8]extension.GetOnuUniInfoResponse_ConfigurationInd{
		0:  0,
		1:  extension.GetOnuUniInfoResponse_TEN_BASE_T_FDX,
		2:  extension.GetOnuUniInfoResponse_HUNDRED_BASE_T_FDX,
		3:  extension.GetOnuUniInfoResponse_GIGABIT_ETHERNET_FDX,
		4:  extension.GetOnuUniInfoResponse_TEN_G_ETHERNET_FDX,
		17: extension.GetOnuUniInfoResponse_TEN_BASE_T_HDX,
		18: extension.GetOnuUniInfoResponse_HUNDRED_BASE_T_HDX,
		19: extension.GetOnuUniInfoResponse_GIGABIT_ETHERNET_HDX,
	}
	configInd := meAttributes[configInd].(uint8)
	singleValResp.Response.GetUniInfo().ConfigInd = configIndMap[configInd]
	return &singleValResp
}

// PostUniStatusErrResponse - TODO: add comment
func PostUniStatusErrResponse(reason extension.GetValueResponse_ErrorReason) *extension.SingleGetValueResponse {
	return &extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status:    extension.GetValueResponse_ERROR,
			ErrReason: reason,
		},
	}
}
