package adaptercoreonu

import (
	"context"
	"fmt"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/extension"
	"sync"
)

const (
	// events of ME Test FSM
	meTestEventTestRequest         = "meTestEventTestRequest"
	meTestEventTestResponseSuccess = "meTestEventTestResponseSuccess"
	meTestEventTestResponseFailure = "meTestEventTestResponseFailure"
	meTestEventTestResultSuccess   = "meTestEventTestResultSuccess"
	meTestEventTestResultTimeout   = "meTestEventTestResultTimeout"
	meTestEventAbort               = "meTestEventAbort"
)
const (
	// states of ME Test FSM
	meTestStNull               = "meTestStNull"
	meTestStWaitTestResp       = "meTestStWaitResp"
	meTestStWaitTestResult     = "meTestStWaitTestResult"
	meTestStHandleTestComplete = "meTestHandleTestComplete"
)

// We initiate an fsmCb per ME Test Request
type fsmCb struct {
	fsm      *AdapterFsm
	reqMsg   extension.GetValueRequest
	respChan chan extension.GetValueResponse
}

type meTestControlBlock struct {
	pDeviceHandler *deviceHandler

	testFsmMap  map[uint16]*fsmCb // The fsmCb is indexed by Transaction Identifier of the Test Action procedure
	testFsmLock sync.RWMutex
}

func NewMeTestMsgHandlerCb(dh *deviceHandler) *meTestControlBlock {
	meTestCb := meTestControlBlock{pDeviceHandler: dh}
	meTestCb.testFsmMap = make(map[uint16]*fsmCb)
	return &meTestCb
}

func (meTestCb *meTestControlBlock) initiateNewMeTestFsm(ctx context.Context, reqMsg extension.GetValueRequest, commChan chan Message, tid uint16, respChan chan extension.GetValueResponse) error {
	aFsm := NewAdapterFsm("MeTestFsm", meTestCb.pDeviceHandler.deviceID, commChan)

	if aFsm == nil {
		logger.Errorw(ctx, "MeTestFsm AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": meTestCb.pDeviceHandler.deviceID})
		return fmt.Errorf("nil-adapter-fsm")
	}
	// ME Test FSM related state machine
	aFsm.pFsm = fsm.NewFSM(

		meTestStNull,
		fsm.Events{
			{Name: meTestEventTestRequest, Src: []string{meTestStNull}, Dst: meTestStWaitTestResp},
			{Name: meTestEventTestResponseSuccess, Src: []string{meTestStWaitTestResp}, Dst: meTestStWaitTestResult},
			{Name: meTestEventTestResponseFailure, Src: []string{meTestStWaitTestResp}, Dst: meTestStHandleTestComplete},
			{Name: meTestEventTestResultSuccess, Src: []string{meTestStWaitTestResult}, Dst: meTestStHandleTestComplete},
			{Name: meTestEventTestResultTimeout, Src: []string{meTestStWaitTestResult}, Dst: meTestStHandleTestComplete},
			{Name: meTestEventAbort, Src: []string{meTestStWaitTestResp, meTestStWaitTestResult, meTestStHandleTestComplete}, Dst: meTestStNull},
		},
		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { aFsm.logFsmStateChange(ctx, e) },
			"enter_" + meTestStWaitTestResp:       func(e *fsm.Event) { meTestCb.meTestFsmWaitTestResp(ctx, e) },
			"enter_" + meTestStWaitTestResult:     func(e *fsm.Event) { meTestCb.meTestFsmWaitTestResult(ctx, e) },
			"enter_" + meTestStHandleTestComplete: func(e *fsm.Event) { meTestCb.meTestFsmTestComplete(ctx, e) },
		},
	)
	meTestCb.testFsmLock.Lock()
	meTestCb.testFsmMap[tid] = &fsmCb{fsm: aFsm, reqMsg: reqMsg, respChan: respChan}
	// Initiate the meTestEventTestRequest on the FSM. Also pass the additional argument - tid.
	// This is useful for the the FSM handler function to pull out fsmCb from the meTestCb.testFsmMap map.
	go func(p_meTestFsm *AdapterFsm) {
		_ = p_meTestFsm.pFsm.Event(meTestEventTestRequest, tid)
	}(meTestCb.testFsmMap[tid].fsm)
	meTestCb.testFsmLock.Unlock()

	return nil
}

func (meTestCb *meTestControlBlock) meTestFsmWaitTestResp(ctx context.Context, e *fsm.Event) {
	tid := e.Args[0].(uint16)
	meTestCb.testFsmLock.RLock()
	pFsmCb, ok := meTestCb.testFsmMap[tid]
	meTestCb.testFsmLock.RUnlock()
	if !ok {
		// This case is impossible. Would be curious to see if this happens
		logger.Fatalw(ctx, "tid-not-found", log.Fields{"device-id":meTestCb.pDeviceHandler.deviceID, "tid": tid})
	}

	classID := meTestCb.getMeClassId(pFsmCb.reqMsg)
	if err := meTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.sendSelfTestReq(ctx, classID, tid, 5 /*TODO: remove hardcode*/, pFsmCb.fsm.commChan); err != nil {
		// TODO : handle error
	}
	// TODO: proceed next
}

func (meTestCb *meTestControlBlock) meTestFsmWaitTestResult(ctx context.Context, e *fsm.Event) {
}

func (meTestCb *meTestControlBlock) meTestFsmTestComplete(ctx context.Context, e *fsm.Event) {
}

func (meTestCb *meTestControlBlock) getMeClassId (req extension.GetValueRequest) generated.ClassID{
	// TODO: fill here
	return 0
}
// MeTestRequest initiate Test Request handling procedure. The results are asynchronously conveyed on the respChan.
// If the return from MeTestRequest is NOT nil, the caller shall not wait for async response.
func (meTestCb *meTestControlBlock) MeTestRequest(ctx context.Context, reqMsg extension.GetValueRequest, commChan chan Message, respChan chan extension.GetValueResponse) error {
	tid := meTestCb.pDeviceHandler.pOnuOmciDevice.PDevOmciCC.getNextTid(false) // generate tid with low priority for Test Action handling
	// indicates only if the FSM was initiated correctly. Response is asynchronous on respChan.
	// If the return from here is NOT nil, the caller shall not wait for async response.
	return meTestCb.initiateNewMeTestFsm(ctx, reqMsg, commChan, tid, respChan)
}
