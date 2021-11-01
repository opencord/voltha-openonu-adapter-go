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

//Package omcitst provides the omci test functionality
package omcitst

import (
	"context"
	"fmt"

	gp "github.com/google/gopacket"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"

	"github.com/opencord/voltha-lib-go/v7/pkg/log"
)

//OmciTestRequest structure holds the information for the OMCI test
type OmciTestRequest struct {
	deviceID     string
	pDevOmciCC   *cmn.OmciCC
	started      bool
	result       bool
	exclusiveCc  bool
	allowFailure bool
	txSeqNo      uint16
	verifyDone   chan<- bool
}

//NewOmciTestRequest returns a new instance of OmciTestRequest
func NewOmciTestRequest(ctx context.Context,
	deviceID string, omciCc *cmn.OmciCC,
	exclusive bool, allowFailure bool) *OmciTestRequest {
	logger.Debug(ctx, "OmciTestRequest-init")
	var OmciTestRequest OmciTestRequest
	OmciTestRequest.deviceID = deviceID
	OmciTestRequest.pDevOmciCC = omciCc
	OmciTestRequest.started = false
	OmciTestRequest.result = false
	OmciTestRequest.exclusiveCc = exclusive
	OmciTestRequest.allowFailure = allowFailure

	return &OmciTestRequest
}

// PerformOmciTest - TODO: add comment
func (oo *OmciTestRequest) PerformOmciTest(ctx context.Context, execChannel chan<- bool) {
	logger.Debug(ctx, "OmciTestRequest-start-test")

	if oo.pDevOmciCC != nil {
		oo.verifyDone = execChannel
		// test functionality is limited to ONU-2G get request for the moment
		// without yet checking the received response automatically here (might be improved ??)
		tid := oo.pDevOmciCC.GetNextTid(false)
		onu2gBaseGet, _ := oo.createOnu2gBaseGet(ctx, tid)
		omciRxCallbackPair := cmn.CallbackPair{
			CbKey: tid,
			CbEntry: cmn.CallbackPairEntry{
				CbRespChannel: nil,
				CbFunction:    oo.ReceiveOmciVerifyResponse,
				FramePrint:    true,
			},
		}

		logger.Debugw(ctx, "performOmciTest-start sending frame", log.Fields{"for device-id": oo.deviceID})
		// send with default timeout and normal prio
		// Note: No reference to fetch the OMCI timeout value from configuration, so hardcode it to 10s
		go oo.pDevOmciCC.Send(ctx, onu2gBaseGet, 10, cmn.CDefaultRetries, false, omciRxCallbackPair)

	} else {
		logger.Errorw(ctx, "performOmciTest: Device does not exist", log.Fields{"for device-id": oo.deviceID})
	}
}

// these are OMCI related functions, could/should be collected in a separate file? TODO!!!
// for a simple start just included in here
//basic approach copied from bbsim, cmp /devices/onu.go and /internal/common/omci/mibpackets.go
func (oo *OmciTestRequest) createOnu2gBaseGet(ctx context.Context, tid uint16) ([]byte, error) {

	request := &omci.GetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.Onu2GClassID,
			EntityInstance: 0, //there is only the 0 instance of ONU2-G (still hard-coded - TODO!!!)
		},
		AttributeMask: 0xE000, //example hardcoded (TODO!!!) request EquId, OmccVersion, VendorCode
	}

	oo.txSeqNo = tid
	pkt, err := cmn.Serialize(ctx, omci.GetRequestType, request, tid)
	if err != nil {
		//omciLogger.WithFields(log.Fields{ ...
		logger.Errorw(ctx, "Cannot serialize Onu2-G GetRequest", log.Fields{"device-id": oo.deviceID, "Err": err})
		return nil, err
	}
	// hexEncode would probably work as well, but not needed and leads to wrong logs on OltAdapter frame
	//	return hexEncode(pkt)
	return pkt, nil
}

//ReceiveOmciVerifyResponse supply a response handler - in this testobject the message is evaluated directly, no response channel used
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
