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

	//"sync"
	//"time"

	gp "github.com/google/gopacket"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"

	//"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

//omciTestRequest structure holds the information for the OMCI test
type omciTestRequest struct {
	deviceID     string
	pDevOmciCC   *omciCC
	started      bool
	result       bool
	exclusiveCc  bool
	allowFailure bool
	txSeqNo      uint16
	verifyDone   chan<- bool
}

//newOmciTestRequest returns a new instance of OmciTestRequest
func newOmciTestRequest(ctx context.Context,
	deviceID string, omciCc *omciCC,
	exclusive bool, allowFailure bool) *omciTestRequest {
	logger.Debug(ctx, "omciTestRequest-init")
	var omciTestRequest omciTestRequest
	omciTestRequest.deviceID = deviceID
	omciTestRequest.pDevOmciCC = omciCc
	omciTestRequest.started = false
	omciTestRequest.result = false
	omciTestRequest.exclusiveCc = exclusive
	omciTestRequest.allowFailure = allowFailure

	return &omciTestRequest
}

//
func (oo *omciTestRequest) performOmciTest(ctx context.Context, execChannel chan<- bool) {
	logger.Debug(ctx, "omciTestRequest-start-test")

	if oo.pDevOmciCC != nil {
		oo.verifyDone = execChannel
		// test functionality is limited to ONU-2G get request for the moment
		// without yet checking the received response automatically here (might be improved ??)
		tid := oo.pDevOmciCC.getNextTid(false)
		onu2gBaseGet, _ := oo.createOnu2gBaseGet(ctx, tid)
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{nil, oo.receiveOmciVerifyResponse},
		}

		logger.Debugw(ctx, "performOmciTest-start sending frame", log.Fields{"for device-id": oo.deviceID})
		// send with default timeout and normal prio
		go oo.pDevOmciCC.send(ctx, onu2gBaseGet, ConstDefaultOmciTimeout, 0, false, omciRxCallbackPair)

	} else {
		logger.Errorw(ctx, "performOmciTest: Device does not exist", log.Fields{"for device-id": oo.deviceID})
	}
}

// these are OMCI related functions, could/should be collected in a separate file? TODO!!!
// for a simple start just included in here
//basic approach copied from bbsim, cmp /devices/onu.go and /internal/common/omci/mibpackets.go
func (oo *omciTestRequest) createOnu2gBaseGet(ctx context.Context, tid uint16) ([]byte, error) {

	request := &omci.GetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.Onu2GClassID,
			EntityInstance: 0, //there is only the 0 instance of ONU2-G (still hard-coded - TODO!!!)
		},
		AttributeMask: 0xE000, //example hardcoded (TODO!!!) request EquId, OmccVersion, VendorCode
	}

	oo.txSeqNo = tid
	pkt, err := serialize(ctx, omci.GetRequestType, request, tid)
	if err != nil {
		//omciLogger.WithFields(log.Fields{ ...
		logger.Errorw(ctx, "Cannot serialize Onu2-G GetRequest", log.Fields{"device-id": oo.deviceID, "Err": err})
		return nil, err
	}
	// hexEncode would probably work as well, but not needed and leads to wrong logs on OltAdapter frame
	//	return hexEncode(pkt)
	return pkt, nil
}

//supply a response handler - in this testobject the message is evaluated directly, no response channel used
func (oo *omciTestRequest) receiveOmciVerifyResponse(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet, respChan chan Message) error {

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

	oo.result = true
	oo.verifyDone <- true

	return nil
}
