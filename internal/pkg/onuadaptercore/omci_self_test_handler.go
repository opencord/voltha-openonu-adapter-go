/*
 * Copyright 2021-present Open Networking Foundation
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
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	"github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/extension"
	"sync"
	"time"
)

const (
	// events of Self Test FSM
	selfTestEventTestRequest         = "selfTestEventTestRequest"
	selfTestEventTestResponseSuccess = "selfTestEventTestResponseSuccess"
	selfTestEventTestResultSuccess   = "selfTestEventTestResultSuccess"
	selfTestEventAbort               = "selfTestEventAbort"
)
const (
	// states of Self Test FSM
	selfTestStNull               = "selfTestStNull"
	selfTestStHandleSelfTestReq  = "selfTestStHandleSelfTestReq"
	selfTestStHandleSelfTestResp = "selfTestStHandleSelfTestResp"
	selfTestStHandleTestResult   = "selfTestStHandleTestResult"
)

const (
	//SelfTestResponseWaitTimeout specifies timeout value waiting for self test response. Unit in seconds
	SelfTestResponseWaitTimeout = 2
)

// We initiate an fsmCb per Self Test Request
type fsmCb struct {
	fsm          *AdapterFsm
	reqMsg       extension.SingleGetValueRequest
	respChan     chan extension.SingleGetValueResponse
	stopOmciChan chan bool
}

type selfTestControlBlock struct {
	pDeviceHandler *deviceHandler
	deviceID       string

	selfTestFsmMap  map[generated.ClassID]*fsmCb // The fsmCb is indexed by ME Class ID of the Test Action procedure
	selfTestFsmLock sync.RWMutex

	stopSelfTestModule chan bool
}

// newSelfTestMsgHandlerCb creates the selfTestControlBlock
// Self Test Handler module supports sending SelfTestRequest and handling of SelfTestResponse/SelfTestResults
// An ephemeral Self Test FSM is initiated for every Self Test request and multiple Self Tests on different
// MEs (that support it) can be handled in parallel.
// At the time of creating this module, only ANI-G self-test is supported.
func newSelfTestMsgHandlerCb(dh *deviceHandler) *selfTestControlBlock {
	selfTestCb := selfTestControlBlock{pDeviceHandler: dh}
	selfTestCb.selfTestFsmMap = make(map[generated.ClassID]*fsmCb)
	selfTestCb.deviceID = selfTestCb.pDeviceHandler.deviceID
	selfTestCb.stopSelfTestModule = make(chan bool)
	return &selfTestCb
}

func (selfTestCb *selfTestControlBlock) initiateNewSelfTestFsm(ctx context.Context, reqMsg extension.SingleGetValueRequest, commChan chan Message, classID generated.ClassID, respChan chan extension.SingleGetValueResponse) error {
	aFsm := NewAdapterFsm("selfTestFsm", selfTestCb.deviceID, commChan)

	if aFsm == nil {
		logger.Errorw(ctx, "selfTestFsm AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": selfTestCb.deviceID})
		return fmt.Errorf("nil-adapter-fsm")
	}
	// Self Test FSM related state machine
	aFsm.pFsm = fsm.NewFSM(

		selfTestStNull,
		fsm.Events{
			{Name: selfTestEventTestRequest, Src: []string{selfTestStNull}, Dst: selfTestStHandleSelfTestReq},
			{Name: selfTestEventTestResponseSuccess, Src: []string{selfTestStHandleSelfTestReq}, Dst: selfTestStHandleSelfTestResp},
			{Name: selfTestEventTestResultSuccess, Src: []string{selfTestStHandleSelfTestResp}, Dst: selfTestStNull},
			{Name: selfTestEventAbort, Src: []string{selfTestStHandleSelfTestReq, selfTestStHandleSelfTestReq, selfTestStHandleTestResult, selfTestStNull}, Dst: selfTestStNull},
		},
		fsm.Callbacks{
			"enter_state":                           func(e *fsm.Event) { aFsm.logFsmStateChange(ctx, e) },
			"enter_" + selfTestStHandleSelfTestReq:  func(e *fsm.Event) { selfTestCb.selfTestFsmHandleSelfTestRequest(ctx, e) },
			"enter_" + selfTestStHandleSelfTestResp: func(e *fsm.Event) { selfTestCb.selfTestFsmHandleSelfTestResponse(ctx, e) },
		},
	)
	selfTestCb.selfTestFsmLock.Lock()
	selfTestCb.selfTestFsmMap[classID] = &fsmCb{fsm: aFsm, reqMsg: reqMsg, respChan: respChan, stopOmciChan: make(chan bool)}
	// Initiate the selfTestEventTestRequest on the FSM. Also pass the additional argument - classID.
	// This is useful for the the FSM handler function to pull out fsmCb from the selfTestCb.selfTestFsmMap map.
	selfTestCb.triggerFsmEvent(aFsm, selfTestEventTestRequest, classID)
	selfTestCb.selfTestFsmLock.Unlock()

	go selfTestCb.waitForStopSelfTestModuleSignal(ctx)

	return nil
}

///// FSM Handlers

func (selfTestCb *selfTestControlBlock) selfTestFsmHandleSelfTestRequest(ctx context.Context, e *fsm.Event) {
	classID := e.Args[0].(generated.ClassID)
	selfTestCb.selfTestFsmLock.RLock()
	pFsmCb, ok := selfTestCb.selfTestFsmMap[classID]
	selfTestCb.selfTestFsmLock.RUnlock()
	if !ok {
		// This case is impossible. Would be curious to see if this happens
		logger.Fatalw(ctx, "class-id-not-found", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	}
	instKeys := selfTestCb.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, classID)

	// TODO: Choosing the first index from the instance keys. For ANI-G, this is fine as there is only one ANI-G instance. How do we handle and report self test for multiple instances?
	if err := selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendSelfTestReq(ctx, classID, instKeys[0], selfTestCb.pDeviceHandler.pOpenOnuAc.omciTimeout, false, pFsmCb.fsm.commChan); err != nil {
		logger.Errorw(ctx, "error sending self test request", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}

	go selfTestCb.handleOmciResponse(ctx, classID)
}

func (selfTestCb *selfTestControlBlock) selfTestFsmHandleSelfTestResponse(ctx context.Context, e *fsm.Event) {
	classID := e.Args[0].(generated.ClassID)
	// Pass the test result processing to another routine
	go selfTestCb.handleOmciResponse(ctx, classID)

}

///// Utility functions

func (selfTestCb *selfTestControlBlock) getMeClassID(ctx context.Context, reqMsg extension.SingleGetValueRequest) (generated.ClassID, error) {
	switch reqMsg.GetRequest().GetRequest().(type) {
	case *extension.GetValueRequest_OnuOpticalInfo:
		return aniGClassID, nil
	default:
		logger.Warnw(ctx, "unsupported me class id for self test", log.Fields{"device-id": selfTestCb.deviceID})
		return 0, fmt.Errorf("unsupported me class id for self test %v", selfTestCb.deviceID)
	}
}

func (selfTestCb *selfTestControlBlock) triggerFsmEvent(pSelfTestFsm *AdapterFsm, event string, args ...generated.ClassID) {
	go func() {
		if len(args) > 0 {
			_ = pSelfTestFsm.pFsm.Event(event, args[0])
		} else {
			_ = pSelfTestFsm.pFsm.Event(event)
		}
	}()
}

func (selfTestCb *selfTestControlBlock) submitFailureGetValueResponse(ctx context.Context, respChan chan extension.SingleGetValueResponse, errorCode extension.GetValueResponse_ErrorReason, statusCode extension.GetValueResponse_Status) {
	singleValResp := extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status:    statusCode,
			ErrReason: errorCode,
		},
	}
	logger.Infow(ctx, "OMCI test response failure - pushing failure response", log.Fields{"device-id": selfTestCb.deviceID})
	respChan <- singleValResp
	logger.Infow(ctx, "OMCI test response failure - pushing failure response complete", log.Fields{"device-id": selfTestCb.deviceID})
}

func (selfTestCb *selfTestControlBlock) handleOmciMessage(ctx context.Context, msg OmciMessage, cb *fsmCb, classID generated.ClassID) {
	logger.Debugw(ctx, "omci Msg", log.Fields{"device-id": selfTestCb.deviceID, "msgType": msg.OmciMsg.MessageType, "msg": msg})
	switch msg.OmciMsg.MessageType {
	case omci.TestResponseType:
		selfTestCb.handleOmciTestResponse(ctx, msg, cb, classID)
	case omci.TestResultType:
		selfTestCb.handleOmciTestResult(ctx, msg, cb, classID)
	default:
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_UNSUPPORTED, extension.GetValueResponse_ERROR)
	}
}

func (selfTestCb *selfTestControlBlock) handleOmciTestResponse(ctx context.Context, msg OmciMessage, cb *fsmCb, classID generated.ClassID) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeTestResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer nil self test response", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.ReleaseTid(ctx, msg.OmciMsg.TransactionID)
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	msgObj, msgOk := msgLayer.(*omci.TestResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be detected for self test response", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.ReleaseTid(ctx, msg.OmciMsg.TransactionID)
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	logger.Debugw(ctx, "OMCI test response Data", log.Fields{"device-id": selfTestCb.deviceID, "data-fields": msgObj})
	if msgObj.Result == generated.Success && msgObj.EntityClass == classID {
		logger.Infow(ctx, "OMCI test response success", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventTestResponseSuccess, classID)
		return
	}

	logger.Infow(ctx, "OMCI test response failure", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.ReleaseTid(ctx, msg.OmciMsg.TransactionID)
	selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
	selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_UNSUPPORTED, extension.GetValueResponse_ERROR)
}

func (selfTestCb *selfTestControlBlock) handleOmciTestResult(ctx context.Context, msg OmciMessage, cb *fsmCb, classID generated.ClassID) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeTestResult)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer nil self test result", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	var msgObj *omci.OpticalLineSupervisionTestResult
	var msgOk bool
	switch classID {
	case aniGClassID:
		msgObj, msgOk = msgLayer.(*omci.OpticalLineSupervisionTestResult)
	default:
		// We should not really land here
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be detected for self test result", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	logger.Debugw(ctx, "raw omci values of ani-g test result",
		log.Fields{"device-id": selfTestCb.deviceID,
			"power-feed-voltage": msgObj.PowerFeedVoltage,
			"rx-power":           msgObj.ReceivedOpticalPower,
			"tx-power":           msgObj.MeanOpticalLaunch,
			"laser-bias-current": msgObj.LaserBiasCurrent,
			"temperature":        msgObj.Temperature})
	singleValResp := extension.SingleGetValueResponse{
		Response: &extension.GetValueResponse{
			Status: extension.GetValueResponse_OK,
			Response: &extension.GetValueResponse_OnuOpticalInfo{
				OnuOpticalInfo: &extension.GetOnuPonOpticalInfoResponse{
					// OMCI representation is Volts, 2s compliment, 20mV resolution
					PowerFeedVoltage: float32(TwosComplementToSignedInt16(msgObj.PowerFeedVoltage)) * 0.02,
					// OMCI representation is Decibel-microwatts, 2s compliment, 0.002dB resolution
					// Note: The OMCI table A.3.39.5 seems to be wrong about resolution. While it says the units are
					// in Decibel-microwatts, 2s complement, 0.002 dB resolution, it actually seems to be
					// Decibel-milliwatts, 2s complement, 0.002 dB resolution after analyzing the results from the ONUs
					ReceivedOpticalPower: float32(TwosComplementToSignedInt16(msgObj.ReceivedOpticalPower)) * 0.002,
					// OMCI representation is Decibel-microwatts, 2s compliment, 0.002dB resolution
					// Same comments as in the case of ReceivedOpticalPower about resolution.
					MeanOpticalLaunchPower: float32(TwosComplementToSignedInt16(msgObj.MeanOpticalLaunch)) * 0.002,
					// OMCI representation is unsigned int, 2uA resolution
					// units of gRPC interface is mA.
					LaserBiasCurrent: float32(msgObj.LaserBiasCurrent) * 0.000002 * 1000, // multiply by 1000 to get units in mA
					// OMCI representation is 2s complement, 1/256 degree Celsius resolution
					Temperature: float32(TwosComplementToSignedInt16(msgObj.Temperature)) / 256.0,
				},
			},
		},
	}
	logger.Debugw(ctx, "ani-g test result after type/value conversion",
		log.Fields{"device-id": selfTestCb.deviceID,
			"power-feed-voltage": singleValResp.Response.GetOnuOpticalInfo().PowerFeedVoltage,
			"rx-power":           singleValResp.Response.GetOnuOpticalInfo().ReceivedOpticalPower,
			"tx-power":           singleValResp.Response.GetOnuOpticalInfo().MeanOpticalLaunchPower,
			"laser-bias-current": singleValResp.Response.GetOnuOpticalInfo().LaserBiasCurrent,
			"temperature":        singleValResp.Response.GetOnuOpticalInfo().Temperature})
	selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventTestResultSuccess)
	logger.Infow(ctx, "OMCI test result success - pushing results", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	cb.respChan <- singleValResp
	selfTestCb.selfTestRequestComplete(ctx, cb.reqMsg)
	logger.Infow(ctx, "OMCI test result success - pushing results complete", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
}

func (selfTestCb *selfTestControlBlock) handleOmciResponse(ctx context.Context, classID generated.ClassID) {
	selfTestCb.selfTestFsmLock.RLock()
	pFsmCb, ok := selfTestCb.selfTestFsmMap[classID]
	selfTestCb.selfTestFsmLock.RUnlock()
	if !ok {
		logger.Errorw(ctx, "fsb control block unavailable", log.Fields{"device-id": selfTestCb.deviceID, "class-id": classID})
		return
	}
	select {
	case <-pFsmCb.stopOmciChan:
		logger.Infow(ctx, "omci processing stopped", log.Fields{"device-id": selfTestCb.deviceID, "class-id": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_REASON_UNDEFINED, extension.GetValueResponse_ERROR)
	case message, ok := <-pFsmCb.fsm.commChan:
		if !ok {
			logger.Errorw(ctx, "Message couldn't be read from channel", log.Fields{"device-id": selfTestCb.deviceID})
			selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
			selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		}
		logger.Debugw(ctx, "Received message on self test result channel", log.Fields{"device-id": selfTestCb.deviceID})

		switch message.Type {
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			selfTestCb.handleOmciMessage(ctx, msg, pFsmCb, classID)
		default:
			logger.Errorw(ctx, "Unknown message type received", log.Fields{"device-id": selfTestCb.deviceID, "message.Type": message.Type})
			selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_UNSUPPORTED, extension.GetValueResponse_ERROR)
		}
	case <-time.After(time.Duration(SelfTestResponseWaitTimeout) * time.Second):
		logger.Errorw(ctx, "timeout waiting for test result", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_TIMEOUT, extension.GetValueResponse_ERROR)
	}
}

// selfTestRequestComplete removes the fsmCb from the local cache if found
func (selfTestCb *selfTestControlBlock) selfTestRequestComplete(ctx context.Context, reqMsg extension.SingleGetValueRequest) {
	meClassID, err := selfTestCb.getMeClassID(ctx, reqMsg)
	if err != nil {
		return
	}
	logger.Infow(ctx, "self test req handling complete", log.Fields{"device-id": selfTestCb.deviceID, "meClassID": meClassID})
	// Clear the fsmCb from the map
	delete(selfTestCb.selfTestFsmMap, meClassID)
}

func (selfTestCb *selfTestControlBlock) waitForStopSelfTestModuleSignal(ctx context.Context) {

	<-selfTestCb.stopSelfTestModule // block on stop signal

	logger.Infow(ctx, "received stop signal - clean up start", log.Fields{"device-id": selfTestCb.deviceID})
	selfTestCb.selfTestFsmLock.Lock()
	for classID, fsmCb := range selfTestCb.selfTestFsmMap {
		select {
		case fsmCb.stopOmciChan <- true: // stop omci processing routine if one was active. It eventually aborts the fsm
			logger.Debugw(ctx, "stopped omci processing", log.Fields{"device-id": selfTestCb.deviceID, "meClassID": classID})
		default:
			selfTestCb.triggerFsmEvent(fsmCb.fsm, selfTestEventAbort)
			selfTestCb.submitFailureGetValueResponse(ctx, fsmCb.respChan, extension.GetValueResponse_REASON_UNDEFINED, extension.GetValueResponse_ERROR)
		}
	}
	selfTestCb.selfTestFsmMap = make(map[generated.ClassID]*fsmCb) // reset map
	selfTestCb.selfTestFsmLock.Unlock()
	logger.Infow(ctx, "received stop signal - clean up end", log.Fields{"device-id": selfTestCb.deviceID})
}

//// Exported functions

// selfTestRequest initiate Test Request handling procedure. The results are asynchronously conveyed on the respChan.
// If the return from selfTestRequest is NOT nil, the caller shall not wait for async response.
func (selfTestCb *selfTestControlBlock) SelfTestRequestStart(ctx context.Context, reqMsg extension.SingleGetValueRequest, commChan chan Message, respChan chan extension.SingleGetValueResponse) error {
	meClassID, err := selfTestCb.getMeClassID(ctx, reqMsg)
	if err != nil {
		return err
	}
	if _, ok := selfTestCb.selfTestFsmMap[meClassID]; ok {
		logger.Errorw(ctx, "self test already in progress for class id", log.Fields{"device-id": selfTestCb.deviceID, "class-id": meClassID})
		return fmt.Errorf("self-test-already-in-progress-for-class-id-%v-device-id-%v", meClassID, selfTestCb.deviceID)
	}
	logger.Infow(ctx, "self test request initiated", log.Fields{"device-id": selfTestCb.deviceID, "meClassID": meClassID})
	// indicates only if the FSM was initiated correctly. Response is asynchronous on respChan.
	// If the return from here is NOT nil, the caller shall not wait for async response.
	return selfTestCb.initiateNewSelfTestFsm(ctx, reqMsg, commChan, meClassID, respChan)
}
