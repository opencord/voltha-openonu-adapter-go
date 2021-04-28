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
	SelfTestResponseWaitTimeout = 3
	SelfTestResultWaitTimeout   = 3
)

// We initiate an fsmCb per Self Test Request
type fsmCb struct {
	fsm      *AdapterFsm
	reqMsg   extension.SingleGetValueRequest
	respChan chan extension.SingleGetValueResponse
}

type selfTestControlBlock struct {
	pDeviceHandler *deviceHandler
	deviceID       string
	pOmciCC        *omciCC
	omciTimeout    int

	selfTestFsmMap  map[generated.ClassID]*fsmCb // The fsmCb is indexed by Transaction Identifier of the Test Action procedure
	selfTestFsmLock sync.RWMutex
}

func NewSelfTestMsgHandlerCb(dh *deviceHandler) *selfTestControlBlock {
	selfTestCb := selfTestControlBlock{pDeviceHandler: dh}
	selfTestCb.selfTestFsmMap = make(map[generated.ClassID]*fsmCb)
	selfTestCb.deviceID = selfTestCb.pDeviceHandler.deviceID
	selfTestCb.pOmciCC = selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC
	selfTestCb.omciTimeout = selfTestCb.pDeviceHandler.pOpenOnuAc.omciTimeout
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
			{Name: selfTestEventAbort, Src: []string{selfTestStHandleSelfTestReq, selfTestStHandleSelfTestReq, selfTestStHandleTestResult}, Dst: selfTestStNull},
		},
		fsm.Callbacks{
			"enter_state":                           func(e *fsm.Event) { aFsm.logFsmStateChange(ctx, e) },
			"enter_" + selfTestStHandleSelfTestReq:  func(e *fsm.Event) { selfTestCb.selfTestFsmHandleSelfTestRequest(ctx, e) },
			"enter_" + selfTestStHandleSelfTestResp: func(e *fsm.Event) { selfTestCb.selfTestFsmHandleSelfTestResponse(ctx, e) },
		},
	)
	selfTestCb.selfTestFsmLock.Lock()
	selfTestCb.selfTestFsmMap[classID] = &fsmCb{fsm: aFsm, reqMsg: reqMsg, respChan: respChan}
	// Initiate the selfTestEventTestRequest on the FSM. Also pass the additional argument - classID.
	// This is useful for the the FSM handler function to pull out fsmCb from the selfTestCb.selfTestFsmMap map.
	selfTestCb.triggerFsmEvent(aFsm, selfTestEventTestRequest, classID)
	selfTestCb.selfTestFsmLock.Unlock()

	return nil
}

func (selfTestCb *selfTestControlBlock) selfTestFsmHandleSelfTestRequest(ctx context.Context, e *fsm.Event) {
	classID := e.Args[0].(generated.ClassID)
	selfTestCb.selfTestFsmLock.RLock()
	pFsmCb, ok := selfTestCb.selfTestFsmMap[classID]
	selfTestCb.selfTestFsmLock.RUnlock()
	if !ok {
		// This case is impossible. Would be curious to see if this happens
		logger.Fatalw(ctx, "tid-not-found", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	}
	instKeys := selfTestCb.pDeviceHandler.pOnuOmciDevice.pOnuDB.getSortedInstKeys(ctx, classID)

	// TODO: Choosing the first index from the instance keys. For ANI-G, this is fine as there is only one ANI-G instance. How do we handle and report self test for multiple instances?
	if err := selfTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendSelfTestReq(ctx, classID, instKeys[0], selfTestCb.omciTimeout, false, pFsmCb.fsm.commChan); err != nil {
		logger.Errorw(ctx, "error sending self test request", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}

	select {
	case message, ok := <-pFsmCb.fsm.commChan:
		if !ok {
			logger.Errorw(ctx, "Message couldn't be read from channel", log.Fields{"device-id": selfTestCb.deviceID})
			selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
			selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		}
		logger.Debugw(ctx, "Received message on self test response channel", log.Fields{"device-id": selfTestCb.deviceID})

		switch message.Type {
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			selfTestCb.handleOmciMessage(ctx, msg, pFsmCb, classID)
		default:
			logger.Errorw(ctx, "Unknown message type received", log.Fields{"device-id": selfTestCb.deviceID, "message.Type": message.Type})
			selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_UNSUPPORTED, extension.GetValueResponse_ERROR)
		}
	case <-time.After(time.Duration(SelfTestResponseWaitTimeout) * time.Second):
		logger.Errorw(ctx, "timeout waiting for test response", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
	}
}

func (selfTestCb *selfTestControlBlock) selfTestFsmHandleSelfTestResponse(ctx context.Context, e *fsm.Event) {
	classID := e.Args[0].(generated.ClassID)
	selfTestCb.selfTestFsmLock.RLock()
	pFsmCb, ok := selfTestCb.selfTestFsmMap[classID]
	selfTestCb.selfTestFsmLock.RUnlock()
	if !ok {
		// This case is impossible. Would be curious to see if this happens
		logger.Fatalw(ctx, "tid-not-found", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	}

	select {
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
	case <-time.After(time.Duration(SelfTestResultWaitTimeout) * time.Second):
		logger.Errorw(ctx, "timeout waiting for test result", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(pFsmCb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, pFsmCb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
	}
}

func (selfTestCb *selfTestControlBlock) getMeClassId(ctx context.Context, reqMsg extension.SingleGetValueRequest) (generated.ClassID, error) {
	switch reqMsg.GetRequest().GetRequest().(type) {
	case *extension.GetValueRequest_OnuOpticalInfo:
		return aniGClassID, nil
	default:
		logger.Warnw(ctx, "unsupported me class id for self test", log.Fields{"device-id": selfTestCb.deviceID})
		return 0, fmt.Errorf("unsupported me class id for self test %v", selfTestCb.deviceID)
	}
}

func (selfTestCb *selfTestControlBlock) triggerFsmEvent(p_selfTestFsm *AdapterFsm, event string, args ...interface{}) {
	go func() {
		_ = p_selfTestFsm.pFsm.Event(event, args)
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
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	msgObj, msgOk := msgLayer.(*omci.TestResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be detected for self test response", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_INTERNAL_ERROR, extension.GetValueResponse_ERROR)
		return
	}
	logger.Debugw(ctx, "OMCI test response Data", log.Fields{"device-id": selfTestCb.deviceID, "data-fields": msgObj})
	if msgObj.Result == generated.Success && msgObj.EntityClass == classID {
		logger.Infow(ctx, "OMCI test response success", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventTestResponseSuccess, classID)
		return
	} else {
		logger.Infow(ctx, "OMCI test response failure", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventAbort)
		selfTestCb.submitFailureGetValueResponse(ctx, cb.respChan, extension.GetValueResponse_UNSUPPORTED, extension.GetValueResponse_ERROR)
		return
	}
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
	} else {
		// TODO: Convert OMCI values to human readable unit
		singleValResp := extension.SingleGetValueResponse{
			Response: &extension.GetValueResponse{
				Status: extension.GetValueResponse_OK,
				Response: &extension.GetValueResponse_OnuOpticalInfo{
					OnuOpticalInfo: &extension.GetOnuPonOpticalInfoResponse{
						PowerFeedVoltage:       int32(msgObj.PowerFeedVoltage),
						ReceivedOpticalPower:   int32(msgObj.ReceivedOpticalPower),
						MeanOpticalLaunchPower: int32(msgObj.MeanOpticalLaunch),
						LaserBiasCurrent:       int32(msgObj.LaserBiasCurrent),
						Temperature:            int32(msgObj.Temperature),
					},
				},
			},
		}
		selfTestCb.triggerFsmEvent(cb.fsm, selfTestEventTestResultSuccess)
		logger.Infow(ctx, "OMCI test result success - pushing results", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
		cb.respChan <- singleValResp
		selfTestCb.selfTestRequestComplete(ctx, cb.reqMsg)
		logger.Infow(ctx, "OMCI test result success - pushing results complete", log.Fields{"device-id": selfTestCb.deviceID, "classID": classID})
	}
}

// selfTestRequest initiate Test Request handling procedure. The results are asynchronously conveyed on the respChan.
// If the return from selfTestRequest is NOT nil, the caller shall not wait for async response.
func (selfTestCb *selfTestControlBlock) SelfTestRequestStart(ctx context.Context, reqMsg extension.SingleGetValueRequest, commChan chan Message, respChan chan extension.SingleGetValueResponse) error {
	meClassID, err := selfTestCb.getMeClassId(ctx, reqMsg)
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

// selfTestRequestComplete removes the fsmCb from the local cache if found
func (selfTestCb *selfTestControlBlock) selfTestRequestComplete(ctx context.Context, reqMsg extension.SingleGetValueRequest) {
	meClassID, err := selfTestCb.getMeClassId(ctx, reqMsg)
	if err != nil {
		return
	}
	// Clear the fsmCb from the map
	delete(selfTestCb.selfTestFsmMap, meClassID)
	logger.Infow(ctx, "self test req handling complete", log.Fields{"device-id": selfTestCb.deviceID, "meClassID": meClassID})
}
