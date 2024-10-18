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

// Package omcitst provides the omci test functionality
package omcitst

import (
	"context"
	"encoding/hex"
	"fmt"

	gp "github.com/google/gopacket"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/omci-lib-go/v2/meframe"
	oframe "github.com/opencord/omci-lib-go/v2/meframe"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
)

// OmciTestRequest structure holds the information for the OMCI test
type OmciTestRequest struct {
	pDevOmciCC   *cmn.OmciCC
	verifyDone   chan<- bool
	deviceID     string
	txSeqNo      uint16
	extended     bool
	started      bool
	result       bool
	exclusiveCc  bool
	allowFailure bool
}

// CTestRequestOmciTimeout - Special OMCI timeout for low prio test request
const CTestRequestOmciTimeout = 5

// NewOmciTestRequest returns a new instance of OmciTestRequest
func NewOmciTestRequest(ctx context.Context,
	deviceID string, omciCc *cmn.OmciCC, extended bool,
	exclusive bool, allowFailure bool) *OmciTestRequest {
	logger.Debug(ctx, "OmciTestRequest-init")
	var OmciTestReq OmciTestRequest
	OmciTestReq.deviceID = deviceID
	OmciTestReq.pDevOmciCC = omciCc
	OmciTestReq.extended = extended
	OmciTestReq.started = false
	OmciTestReq.result = false
	OmciTestReq.exclusiveCc = exclusive
	OmciTestReq.allowFailure = allowFailure

	return &OmciTestReq
}

// PerformOmciTest - TODO: add comment
func (oo *OmciTestRequest) PerformOmciTest(ctx context.Context, execChannel chan<- bool) {
	logger.Debug(ctx, "OmciTestRequest-start-test")

	if oo.pDevOmciCC != nil {
		oo.verifyDone = execChannel
		// test functionality is limited to ONU-2G get request for the moment
		// without yet checking the received response automatically here (might be improved ??)
		tid := oo.pDevOmciCC.GetNextTid(false)
		onu2gGet, _ := oo.createOnu2gGet(ctx, tid)
		omciRxCallbackPair := cmn.CallbackPair{
			CbKey: tid,
			CbEntry: cmn.CallbackPairEntry{
				CbRespChannel: nil,
				CbFunction:    oo.ReceiveOmciVerifyResponse,
				FramePrint:    true,
			},
		}
		logger.Debugw(ctx, "performOmciTest-start sending frame", log.Fields{"for device-id": oo.deviceID, "onu2gGet": hex.EncodeToString(onu2gGet)})
		// send with default timeout and normal prio
		// Note: No reference to fetch the OMCI timeout value from configuration, so hardcode it to 10s
		go oo.pDevOmciCC.Send(ctx, onu2gGet, CTestRequestOmciTimeout, cmn.CDefaultRetries, false, omciRxCallbackPair)

	} else {
		logger.Errorw(ctx, "performOmciTest: Device does not exist", log.Fields{"for device-id": oo.deviceID})
	}
}

// these are OMCI related functions, could/should be collected in a separate file? TODO!!!
// for a simple start just included in here
// basic approach copied from bbsim, cmp /devices/onu.go and /internal/common/omci/mibpackets.go
func (oo *OmciTestRequest) createOnu2gGet(ctx context.Context, tid uint16) ([]byte, error) {

	meParams := me.ParamData{
		EntityID: 0,
		Attributes: me.AttributeValueMap{
			me.Onu2G_EquipmentId: "",
			me.Onu2G_OpticalNetworkUnitManagementAndControlChannelOmccVersion: 0},
	}
	meInstance, omciErr := me.NewOnu2G(meParams)
	if omciErr.GetError() == nil {
		var messageSet omci.DeviceIdent = omci.BaselineIdent
		if oo.extended {
			messageSet = omci.ExtendedIdent
		}
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.GetRequestType, oframe.TransactionID(tid),
			meframe.FrameFormat(messageSet))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ONU2-G instance for get", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		oo.txSeqNo = tid

		pkt, err := cmn.SerializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ONU2-G get", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		return pkt, nil
	}
	logger.Errorw(ctx, "Cannot generate ONU2-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// ReceiveOmciVerifyResponse supply a response handler - in this testobject the message is evaluated directly, no response channel used
func (oo *OmciTestRequest) ReceiveOmciVerifyResponse(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet, respChan chan cmn.Message) error {

	logger.Debugw(ctx, "verify-omci-message-response received:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": omciMsg.TransactionID, "DeviceIdent": omciMsg.DeviceIdentifier})

	if omciMsg.TransactionID == oo.txSeqNo {
		logger.Debugw(ctx, "verify-omci-message-response", log.Fields{"correct TransCorrId": omciMsg.TransactionID})
	} else {
		logger.Debugw(ctx, "verify-omci-message-response error", log.Fields{"incorrect TransCorrId": omciMsg.TransactionID,
			"expected": oo.txSeqNo})
		oo.verifyDone <- false
		return fmt.Errorf("unexpected TransCorrId %s", oo.deviceID)
	}
	if omciMsg.MessageType == omci.GetResponseType {
		logger.Debugw(ctx, "verify-omci-message-response", log.Fields{"correct RespType": omciMsg.MessageType})
	} else {
		logger.Debugw(ctx, "verify-omci-message-response error", log.Fields{"incorrect RespType": omciMsg.MessageType,
			"expected": omci.GetResponseType})
		oo.verifyDone <- false
		return fmt.Errorf("unexpected MessageType %s", oo.deviceID)
	}
	if oo.extended {
		if omciMsg.DeviceIdentifier == omci.ExtendedIdent {
			logger.Debugw(ctx, "verify-omci-message-response", log.Fields{"correct DeviceIdentifier": omciMsg.DeviceIdentifier})
		} else {
			logger.Debugw(ctx, "verify-omci-message-response error", log.Fields{"incorrect DeviceIdentifier": omciMsg.DeviceIdentifier,
				"expected": omci.ExtendedIdent})
			oo.verifyDone <- false
			return fmt.Errorf("unexpected DeviceIdentifier %s", oo.deviceID)
		}
	}

	//TODO!!! further tests on the payload should be done here ...

	oo.pDevOmciCC.RLockMutexMonReq()
	if _, exist := oo.pDevOmciCC.GetMonitoredRequest(omciMsg.TransactionID); exist {
		oo.pDevOmciCC.SetChMonitoredRequest(omciMsg.TransactionID, true)
	} else {
		logger.Infow(ctx, "reqMon: map entry does not exist!",
			log.Fields{"tid": omciMsg.TransactionID, "device-id": oo.deviceID})
	}
	oo.pDevOmciCC.RUnlockMutexMonReq()

	oo.result = true
	oo.verifyDone <- true

	return nil
}
