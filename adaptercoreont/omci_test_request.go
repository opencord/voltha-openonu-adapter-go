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

//Package adaptercoreont provides the utility for onu devices, flows and statistics
package adaptercoreont

import (
	"context"
	"errors"

	//"sync"
	//"time"

	gp "github.com/google/gopacket"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

//OmciTestRequest structure holds the information for the OMCI test
type OmciTestRequest struct {
	deviceID     string
	pDevOmciCC   *OmciCC
	started      bool
	result       bool
	exclusive_cc bool
	allowFailure bool
	txSeqNo      uint16
	verifyDone   chan<- bool
}

//NewOmciTestRequest returns a new instance of OmciTestRequest
func NewOmciTestRequest(ctx context.Context,
	device_id string, omci_cc *OmciCC,
	exclusive bool, allow_failure bool) *OmciTestRequest {
	log.Debug("omciTestRequest-init")
	var omciTestRequest OmciTestRequest
	omciTestRequest.deviceID = device_id
	omciTestRequest.pDevOmciCC = omci_cc
	omciTestRequest.started = false
	omciTestRequest.result = false
	omciTestRequest.exclusive_cc = exclusive
	omciTestRequest.allowFailure = allow_failure

	return &omciTestRequest
}

//
func (oo *OmciTestRequest) PerformOmciTest(ctx context.Context, exec_Channel chan<- bool) {
	log.Debug("omciTestRequest-start-test")

	if oo.pDevOmciCC != nil {
		oo.verifyDone = exec_Channel
		// test functionality is limited to ONU-2G get request for the moment
		// without yet checking the received response automatically here (might be improved ??)
		tid := oo.pDevOmciCC.GetNextTid(false)
		onu2gBaseGet, _ := oo.CreateOnu2gBaseGet(tid)
		omciRxCallbackPair := CallbackPair{tid, oo.ReceiveOmciVerifyResponse}

		log.Debugw("performOmciTest-start sending frame", log.Fields{"for deviceId": oo.deviceID})
		// send with default timeout and normal prio
		go oo.pDevOmciCC.Send(ctx, onu2gBaseGet, ConstDefaultOmciTimeout, 0, false, omciRxCallbackPair)

	} else {
		log.Errorw("performOmciTest: Device does not exist", log.Fields{"for deviceId": oo.deviceID})
	}
}

// these are OMCI related functions, could/should be collected in a separate file? TODO!!!
// for a simple start just included in here
//basic approach copied from bbsim, cmp /devices/onu.go and /internal/common/omci/mibpackets.go
func (oo *OmciTestRequest) CreateOnu2gBaseGet(tid uint16) ([]byte, error) {

	request := &omci.GetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.Onu2GClassID,
			EntityInstance: 0, //there is only the 0 instance of ONU2-G (still hard-coded - TODO!!!)
		},
		AttributeMask: 0xE000, //example hardcoded (TODO!!!) request EquId, OmccVersion, VendorCode
	}

	oo.txSeqNo = tid
	pkt, err := serialize(omci.GetRequestType, request, tid)
	if err != nil {
		//omciLogger.WithFields(log.Fields{ ...
		log.Errorw("Cannot serialize Onu2-G GetRequest", log.Fields{"Err": err})
		return nil, err
	}
	// hexEncode would probably work as well, but not needed and leads to wrong logs on OltAdapter frame
	//	return hexEncode(pkt)
	return pkt, nil
}

//supply a response handler
func (oo *OmciTestRequest) ReceiveOmciVerifyResponse(omciMsg *omci.OMCI, packet *gp.Packet) error {

	log.Debugw("verify-omci-message-response received:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": omciMsg.TransactionID, "DeviceIdent": omciMsg.DeviceIdentifier})

	if omciMsg.TransactionID == oo.txSeqNo {
		log.Debugw("verify-omci-message-response", log.Fields{"correct TransCorrId": omciMsg.TransactionID})
	} else {
		log.Debugw("verify-omci-message-response error", log.Fields{"incorrect TransCorrId": omciMsg.TransactionID,
			"expected": oo.txSeqNo})
		oo.verifyDone <- false
		return errors.New("Unexpected TransCorrId")
	}
	if omciMsg.MessageType == omci.GetResponseType {
		log.Debugw("verify-omci-message-response", log.Fields{"correct RespType": omciMsg.MessageType})
	} else {
		log.Debugw("verify-omci-message-response error", log.Fields{"incorrect RespType": omciMsg.MessageType,
			"expected": omci.GetResponseType})
		oo.verifyDone <- false
		return errors.New("Unexpected MessageType")
	}

	//TODO!!! further tests on the payload should be done here ...

	oo.result = true
	oo.verifyDone <- true

	return nil
}
