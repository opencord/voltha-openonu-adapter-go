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

// Package core provides the utility for onu devices, flows and statistics
package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/config"

	"github.com/gogo/protobuf/proto"
	"github.com/looplab/fsm"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/db"
	"github.com/opencord/voltha-lib-go/v7/pkg/events/eventif"
	flow "github.com/opencord/voltha-lib-go/v7/pkg/flows"
	vgrpc "github.com/opencord/voltha-lib-go/v7/pkg/grpc"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	platform "github.com/opencord/voltha-lib-go/v7/pkg/platform"
	almgr "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/almgr"
	avcfg "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/avcfg"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	mib "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/mib"
	otst "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/omcitst"
	pmmgr "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/pmmgr"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/swupg"
	uniprt "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/uniprt"
	"github.com/opencord/voltha-protos/v5/go/common"
	vc "github.com/opencord/voltha-protos/v5/go/common"
	ca "github.com/opencord/voltha-protos/v5/go/core_adapter"
	"github.com/opencord/voltha-protos/v5/go/extension"
	"github.com/opencord/voltha-protos/v5/go/inter_adapter"
	ia "github.com/opencord/voltha-protos/v5/go/inter_adapter"
	of "github.com/opencord/voltha-protos/v5/go/openflow_13"
	"github.com/opencord/voltha-protos/v5/go/openolt"
	oop "github.com/opencord/voltha-protos/v5/go/openolt"
	"github.com/opencord/voltha-protos/v5/go/tech_profile"
	"github.com/opencord/voltha-protos/v5/go/voltha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	//constants for reconcile flow check channel
	cWaitReconcileFlowAbortOnSuccess = 0xFFFD
	cWaitReconcileFlowAbortOnError   = 0xFFFE
	cWaitReconcileFlowNoActivity     = 0xFFFF
)

const (
	// constants for timeouts
	cTimeOutRemoveUpgrade = 1 //for usage in seconds
)

const (
	// dummy constant - irregular value for ConnState - used to avoiding setting this state in the updateDeviceState()
	// should better be defined in voltha protobuf or best solution would be to define an interface to just set the OperState
	// as long as such is not available by the libraries - use this workaround
	connectStatusINVALID = 255 // as long as not used as key in voltha.ConnectStatus_Types_name
)

const (
	// events of Device FSM
	devEvDeviceInit       = "devEvDeviceInit"
	devEvGrpcConnected    = "devEvGrpcConnected"
	devEvGrpcDisconnected = "devEvGrpcDisconnected"
	devEvDeviceUpInd      = "devEvDeviceUpInd"
	devEvDeviceDownInd    = "devEvDeviceDownInd"
)
const (
	// states of Device FSM
	devStNull      = "devStNull"
	devStDown      = "devStDown"
	devStInit      = "devStInit"
	devStConnected = "devStConnected"
	devStUp        = "devStUp"
)

// Event category and subcategory definitions - same as defiend for OLT in eventmgr.go  - should be done more centrally
const (
	pon = voltha.EventSubCategory_PON
	//olt           = voltha.EventSubCategory_OLT
	//ont           = voltha.EventSubCategory_ONT
	//onu           = voltha.EventSubCategory_ONU
	//nni           = voltha.EventSubCategory_NNI
	//service       = voltha.EventCategory_SERVICE
	//security      = voltha.EventCategory_SECURITY
	equipment = voltha.EventCategory_EQUIPMENT
	//processing    = voltha.EventCategory_PROCESSING
	//environment   = voltha.EventCategory_ENVIRONMENT
	//communication = voltha.EventCategory_COMMUNICATION
)

const (
	cEventObjectType = "ONU"
)
const (
	cOnuActivatedEvent = "ONU_ACTIVATED"
)

type omciIdleCheckStruct struct {
	omciIdleCheckFunc func(*deviceHandler, context.Context, cmn.UsedOmciConfigFsms, string) bool
	omciIdleState     string
}

var fsmOmciIdleStateFuncMap = map[cmn.UsedOmciConfigFsms]omciIdleCheckStruct{
	cmn.CUploadFsm:        {(*deviceHandler).isFsmInOmciIdleStateDefault, mib.CMibUlFsmIdleState},
	cmn.CDownloadFsm:      {(*deviceHandler).isFsmInOmciIdleStateDefault, mib.CMibDlFsmIdleState},
	cmn.CUniLockFsm:       {(*deviceHandler).isFsmInOmciIdleStateDefault, uniprt.CUniFsmIdleState},
	cmn.CUniUnLockFsm:     {(*deviceHandler).isFsmInOmciIdleStateDefault, uniprt.CUniFsmIdleState},
	cmn.CAniConfigFsm:     {(*deviceHandler).isAniConfigFsmInOmciIdleState, avcfg.CAniFsmIdleState},
	cmn.CUniVlanConfigFsm: {(*deviceHandler).isUniVlanConfigFsmInOmciIdleState, avcfg.CVlanFsmIdleState},
	cmn.CL2PmFsm:          {(*deviceHandler).isFsmInOmciIdleStateDefault, pmmgr.CL2PmFsmIdleState},
	cmn.COnuUpgradeFsm:    {(*deviceHandler).isFsmInOmciIdleStateDefault, swupg.COnuUpgradeFsmIdleState},
}

const (
	cNoReconciling = iota
	cOnuConfigReconciling
	cSkipOnuConfigReconciling
)

// FlowCb is the flow control block containing flow add/delete information along with a response channel
type FlowCb struct {
	ctx          context.Context // Flow handler context
	flowItem     *of.OfpFlowStats
	uniPort      *cmn.OnuUniPort
	flowMetaData *of.FlowMetadata
	respChan     *chan error // channel to report the Flow handling error
	addFlow      bool        // if true flow to be added, else removed
}

// deviceHandler will interact with the ONU ? device.
type deviceHandler struct {
	EventProxy eventif.EventProxy

	device *voltha.Device

	coreClient *vgrpc.Client

	pmConfigs *voltha.PmConfigs
	config    *config.AdapterFlags

	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	//pPonPort        *voltha.Port
	deviceEntrySet  chan bool //channel for DeviceEntry set event
	pOnuOmciDevice  *mib.OnuDeviceEntry
	pOnuTP          *avcfg.OnuUniTechProf
	pOnuMetricsMgr  *pmmgr.OnuMetricsManager
	pAlarmMgr       *almgr.OnuAlarmManager
	pSelfTestHdlr   *otst.SelfTestControlBlock
	exitChannel     chan int
	pOnuIndication  *oop.OnuIndication
	pLockStateFsm   *uniprt.LockStateFsm
	pUnlockStateFsm *uniprt.LockStateFsm

	stopCollector                  chan bool
	stopAlarmManager               chan bool
	stopHeartbeatCheck             chan bool
	uniEntityMap                   cmn.OnuUniPortMap
	UniVlanConfigFsmMap            map[uint8]*avcfg.UniVlanConfigFsm
	pOnuUpradeFsm                  *swupg.OnuUpgradeFsm
	chUniVlanConfigReconcilingDone chan uint16 //channel to indicate that VlanConfig reconciling for a specific UNI has been finished
	chReconcilingFinished          chan bool   //channel to indicate that reconciling has been finished
	pLastUpgradeImageState         *voltha.ImageState
	upgradeFsmChan                 chan struct{}

	deviceDeleteCommChan chan bool
	DeviceID             string
	DeviceType           string
	adminState           string
	logicalDeviceID      string
	ProxyAddressID       string
	ProxyAddressType     string
	parentID             string

	flowCbChan                     []chan FlowCb
	stopFlowMonitoringRoutine      []chan bool // length of slice equal to number of uni ports
	isFlowMonitoringRoutineActive  []bool      // length of slice equal to number of uni ports
	reconcileExpiryComplete        time.Duration
	reconcileExpiryVlanConfig      time.Duration
	lockDevice                     sync.RWMutex
	mutexDeviceReason              sync.RWMutex
	mutexCollectorFlag             sync.RWMutex
	mutextAlarmManagerFlag         sync.RWMutex
	lockVlanConfig                 sync.RWMutex
	lockVlanAdd                    sync.RWMutex
	lockUpgradeFsm                 sync.RWMutex
	mutexReconcilingFlag           sync.RWMutex
	mutexReconcilingFirstPassFlag  sync.RWMutex
	mutexReconcilingReasonUpdate   sync.RWMutex
	mutexReadyForOmciConfig        sync.RWMutex
	mutexDeletionInProgressFlag    sync.RWMutex
	mutexFlowMonitoringRoutineFlag sync.RWMutex
	mutexForDisableDeviceRequested sync.RWMutex
	mutexOltAvailable              sync.RWMutex
	mutexKvStoreContext            sync.Mutex
	ponPortNumber                  uint32

	deviceReason uint8

	//flowMgr       *OpenOltFlowMgr
	//eventMgr      *OpenOltEventMgr
	//resourceMgr   *rsrcMgr.OpenOltResourceMgr

	//discOnus sync.Map
	//onus     sync.Map
	//portStats          *OpenOltStatisticsMgr
	collectorIsRunning      bool
	alarmManagerIsRunning   bool
	upgradeCanceled         bool
	reconciling             uint8
	reconcilingFirstPass    bool
	reconcilingReasonUpdate bool
	readyForOmciConfig      bool
	deletionInProgress      bool
	disableDeviceRequested  bool // this flag identify ONU received disable request or not
	oltAvailable            bool
}

// newDeviceHandler creates a new device handler
func newDeviceHandler(ctx context.Context, cc *vgrpc.Client, ep eventif.EventProxy, device *voltha.Device, adapter *OpenONUAC) *deviceHandler {
	var dh deviceHandler
	dh.coreClient = cc
	dh.EventProxy = ep
	dh.config = adapter.config
	cloned := (proto.Clone(device)).(*voltha.Device)
	dh.DeviceID = cloned.Id
	dh.DeviceType = cloned.Type
	dh.adminState = "up"
	dh.device = cloned
	dh.pOpenOnuAc = adapter
	dh.exitChannel = make(chan int, 1)
	dh.lockDevice = sync.RWMutex{}
	dh.deviceEntrySet = make(chan bool, 1)
	dh.collectorIsRunning = false
	dh.stopCollector = make(chan bool, 2)
	dh.alarmManagerIsRunning = false
	dh.stopAlarmManager = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	//TODO initialize the support classes.
	dh.uniEntityMap = make(map[uint32]*cmn.OnuUniPort)
	dh.lockVlanConfig = sync.RWMutex{}
	dh.lockVlanAdd = sync.RWMutex{}
	dh.lockUpgradeFsm = sync.RWMutex{}
	dh.mutexForDisableDeviceRequested = sync.RWMutex{}
	dh.UniVlanConfigFsmMap = make(map[uint8]*avcfg.UniVlanConfigFsm)
	dh.reconciling = cNoReconciling
	dh.reconcilingReasonUpdate = false
	dh.reconcilingFirstPass = true
	dh.disableDeviceRequested = false
	dh.oltAvailable = false
	dh.chReconcilingFinished = make(chan bool)
	dh.reconcileExpiryComplete = adapter.maxTimeoutReconciling //assumption is to have it as duration in s!
	rECSeconds := int(dh.reconcileExpiryComplete / time.Second)
	if rECSeconds < 2 {
		dh.reconcileExpiryComplete = time.Duration(2) * time.Second //ensure a minimum expiry time of 2s for complete reconciling
		rECSeconds = 2
	}
	rEVCSeconds := rECSeconds / 2
	dh.reconcileExpiryVlanConfig = time.Duration(rEVCSeconds) * time.Second //set this duration to some according lower value
	dh.readyForOmciConfig = false
	dh.deletionInProgress = false
	dh.pLastUpgradeImageState = &voltha.ImageState{
		DownloadState: voltha.ImageState_DOWNLOAD_UNKNOWN,
		Reason:        voltha.ImageState_UNKNOWN_ERROR,
		ImageState:    voltha.ImageState_IMAGE_UNKNOWN,
	}
	dh.upgradeFsmChan = make(chan struct{})
	dh.deviceDeleteCommChan = make(chan bool, 2)

	if dh.device.PmConfigs != nil { // can happen after onu adapter restart
		dh.pmConfigs = cloned.PmConfigs
	} /* else {
		// will be populated when onu_metrics_mananger is initialized.
	}*/

	// Device related state machine
	dh.pDeviceStateFsm = fsm.NewFSM(
		devStNull,
		fsm.Events{
			{Name: devEvDeviceInit, Src: []string{devStNull, devStDown}, Dst: devStInit},
			{Name: devEvGrpcConnected, Src: []string{devStInit}, Dst: devStConnected},
			{Name: devEvGrpcDisconnected, Src: []string{devStConnected, devStDown}, Dst: devStInit},
			{Name: devEvDeviceUpInd, Src: []string{devStConnected, devStDown}, Dst: devStUp},
			{Name: devEvDeviceDownInd, Src: []string{devStUp}, Dst: devStDown},
		},
		fsm.Callbacks{
			"before_event":                      func(e *fsm.Event) { dh.logStateChange(ctx, e) },
			("before_" + devEvDeviceInit):       func(e *fsm.Event) { dh.doStateInit(ctx, e) },
			("after_" + devEvDeviceInit):        func(e *fsm.Event) { dh.postInit(ctx, e) },
			("before_" + devEvGrpcConnected):    func(e *fsm.Event) { dh.doStateConnected(ctx, e) },
			("before_" + devEvGrpcDisconnected): func(e *fsm.Event) { dh.doStateInit(ctx, e) },
			("after_" + devEvGrpcDisconnected):  func(e *fsm.Event) { dh.postInit(ctx, e) },
			("before_" + devEvDeviceUpInd):      func(e *fsm.Event) { dh.doStateUp(ctx, e) },
			("before_" + devEvDeviceDownInd):    func(e *fsm.Event) { dh.doStateDown(ctx, e) },
		},
	)

	return &dh
}

// start save the device to the data model
func (dh *deviceHandler) start(ctx context.Context) {
	logger.Debugw(ctx, "starting-device-handler", log.Fields{"device": dh.device, "device-id": dh.DeviceID})
	// Add the initial device to the local model
	logger.Debugw(ctx, "device-handler-started", log.Fields{"device": dh.device})
}

/*
// stop stops the device dh.  Not much to do for now
func (dh *deviceHandler) stop(ctx context.Context) {
	logger.Debug("stopping-device-handler")
	dh.exitChannel <- 1
}
*/

// ##########################################################################################
// deviceHandler methods that implement the adapters interface requests ##### begin #########

// adoptOrReconcileDevice adopts the ONU device
func (dh *deviceHandler) adoptOrReconcileDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "adopt_or_reconcile_device", log.Fields{"device-id": device.Id, "Address": device.GetHostAndPort()})

	logger.Debugw(ctx, "Device FSM: ", log.Fields{"device-id": device.Id, "state": string(dh.pDeviceStateFsm.Current())})

	if dh.pDeviceStateFsm.Is(devStNull) {
		if err := dh.pDeviceStateFsm.Event(devEvDeviceInit); err != nil {
			logger.Errorw(ctx, "Device FSM: Can't go to state DeviceInit", log.Fields{"device-id": device.Id, "err": err})
		}
		logger.Debugw(ctx, "Device FSM: ", log.Fields{"device-id": device.Id, "state": string(dh.pDeviceStateFsm.Current())})
		// device.PmConfigs is not nil in cases when adapter restarts. We should not re-set the core again.
		if device.PmConfigs == nil {
			// Now, set the initial PM configuration for that device
			if err := dh.updatePMConfigInCore(ctx, dh.pmConfigs); err != nil {
				logger.Errorw(ctx, "error updating pm config to core", log.Fields{"device-id": dh.DeviceID, "err": err})
			}
		}
	} else {
		logger.Debugw(ctx, "AdoptOrReconcileDevice: Agent/device init already done", log.Fields{"device-id": device.Id})
	}

}

func (dh *deviceHandler) handleOMCIIndication(ctx context.Context, msg *ia.OmciMessage) error {
	/* msg print moved symmetrically to omci_cc, if wanted here as additional debug, than perhaps only based on additional debug setting!
	//assuming omci message content is hex coded!
	// with restricted output of 16(?) bytes would be ...omciMsg.Message[:16]
	logger.Debugw(ctx, "inter-adapter-recv-omci", log.Fields{
		"device-id": dh.DeviceID, "RxOmciMessage": hex.EncodeToString(omciMsg.Message)})
	*/
	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry != nil {
		if pDevEntry.PDevOmciCC != nil {
			return pDevEntry.PDevOmciCC.ReceiveMessage(log.WithSpanFromContext(context.TODO(), ctx), msg.Message)
		}
		logger.Debugw(ctx, "omciCC not ready to receive omci messages - incoming omci message ignored", log.Fields{"device-id": dh.DeviceID,
			"rxMsg": msg.Message})
	}
	logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.DeviceID})
	return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
}

func (dh *deviceHandler) handleTechProfileDownloadRequest(ctx context.Context, techProfMsg *ia.TechProfileDownloadMessage) error {
	logger.Infow(ctx, "tech-profile-download-request", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Errorw(ctx, "onuTechProf instance not set up for DLMsg request - ignoring request",
			log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("techProfile DLMsg request while onuTechProf instance not setup: %s", dh.DeviceID)
	}
	if !dh.IsReadyForOmciConfig() {
		logger.Errorw(ctx, "TechProf-set rejected: improper device state", log.Fields{"device-id": dh.DeviceID,
			"device-state": dh.GetDeviceReasonString()})
		return fmt.Errorf("improper device state %s on device %s", dh.GetDeviceReasonString(), dh.DeviceID)
	}
	//previous state test here was just this one, now extended for more states to reject the SetRequest:
	// at least 'mib-downloaded' should be reached for processing of this specific ONU configuration
	//  if (dh.deviceReason == "stopping-openomci") || (dh.deviceReason == "omci-admin-lock")

	// we have to lock access to TechProfile processing based on different messageType calls or
	// even to fast subsequent calls of the same messageType as well as OnuKVStore processing due
	// to possible concurrent access by flow processing
	dh.pOnuTP.LockTpProcMutex()
	defer dh.pOnuTP.UnlockTpProcMutex()

	if techProfMsg.UniId >= platform.MaxUnisPerOnu {
		return fmt.Errorf("received UniId value exceeds range: %d, device-id: %s",
			techProfMsg.UniId, dh.DeviceID)
	}
	uniID := uint8(techProfMsg.UniId)
	tpID, err := cmn.GetTpIDFromTpPath(techProfMsg.TpInstancePath)
	if err != nil {
		logger.Errorw(ctx, "error-parsing-tpid-from-tppath",
			log.Fields{"device-id": dh.DeviceID, "err": err, "tp-path": techProfMsg.TpInstancePath})
		return err
	}
	logger.Debugw(ctx, "unmarshal-techprof-msg-body", log.Fields{"device-id": dh.DeviceID,
		"uniID": uniID, "tp-path": techProfMsg.TpInstancePath, "tpID": tpID})

	if bTpModify := pDevEntry.UpdateOnuUniTpPath(ctx, uniID, uint8(tpID), techProfMsg.TpInstancePath); bTpModify {

		switch tpInst := techProfMsg.TechTpInstance.(type) {
		case *ia.TechProfileDownloadMessage_TpInstance:
			logger.Debugw(ctx, "onu-uni-tp-path-modified", log.Fields{"device-id": dh.DeviceID,
				"uniID": uniID, "tp-path": techProfMsg.TpInstancePath, "tpID": tpID})

			err = dh.CheckAvailableOnuCapabilities(ctx, pDevEntry, *tpInst.TpInstance)
			if err != nil {
				logger.Errorw(ctx, "error-checking-available-onu-capabilities-stopping-device",
					log.Fields{"device-id": dh.DeviceID, "err": err, "tp-path": techProfMsg.TpInstancePath})
				// stopping all further processing
				_ = dh.UpdateInterface(ctx)
				return err
			}
			//	if there has been some change for some uni TechProfilePath
			//in order to allow concurrent calls to other dh instances we do not wait for execution here
			//but doing so we can not indicate problems to the caller (who does what with that then?)
			//by now we just assume straightforward successful execution
			//TODO!!! Generally: In this scheme it would be good to have some means to indicate
			//  possible problems to the caller later autonomously

			// deadline context to ensure completion of background routines waited for
			//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
			deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
			dctx, cancel := context.WithDeadline(context.Background(), deadline)

			dh.pOnuTP.ResetTpProcessingErrorIndication(uniID, tpID)

			var wg sync.WaitGroup
			wg.Add(1) // for the 1 go routine to finish
			// attention: deadline completion check and wg.Done is to be done in both routines
			go dh.pOnuTP.ConfigureUniTp(log.WithSpanFromContext(dctx, ctx), uniID, techProfMsg.TpInstancePath, *tpInst.TpInstance, &wg)
			dh.waitForCompletion(ctx, cancel, &wg, "TechProfDwld") //wait for background process to finish
			if tpErr := dh.pOnuTP.GetTpProcessingErrorIndication(uniID, tpID); tpErr != nil {
				logger.Errorw(ctx, "error-processing-tp", log.Fields{"device-id": dh.DeviceID, "err": tpErr, "tp-path": techProfMsg.TpInstancePath})
				return tpErr
			}
			deadline = time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
			dctx2, cancel2 := context.WithDeadline(context.Background(), deadline)
			defer cancel2()
			err1 := pDevEntry.UpdateOnuKvStore(log.WithSpanFromContext(dctx2, ctx))
			if err1 != nil {
				logger.Errorf(ctx, "UpdateOnuKvStore-failed", log.Fields{"device-id": dh.DeviceID, "error": err1})
				return err
			}
			return nil
		default:
			logger.Errorw(ctx, "unsupported-tp-instance-type", log.Fields{"device-id": dh.DeviceID, "tp-path": techProfMsg.TpInstancePath})
			return fmt.Errorf("unsupported-tp-instance-type--tp-id-%v", techProfMsg.TpInstancePath)
		}
	}
	// no change, nothing really to do - return success
	logger.Debugw(ctx, "onu-uni-tp-path-not-modified", log.Fields{"device-id": dh.DeviceID,
		"uniID": uniID, "tp-path": techProfMsg.TpInstancePath, "tpID": tpID})
	return nil
}

func (dh *deviceHandler) handleDeleteGemPortRequest(ctx context.Context, delGemPortMsg *ia.DeleteGemPortMessage) error {
	logger.Infow(ctx, "delete-gem-port-request start", log.Fields{"device-id": dh.DeviceID})

	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw(ctx, "onuTechProf instance not set up for DelGem request - ignoring request",
			log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("techProfile DelGem request while onuTechProf instance not setup: %s", dh.DeviceID)
	}
	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.LockTpProcMutex()
	defer dh.pOnuTP.UnlockTpProcMutex()

	if delGemPortMsg.UniId >= platform.MaxUnisPerOnu {
		logger.Errorw(ctx, "delete-gem-port UniId exceeds range", log.Fields{
			"device-id": dh.DeviceID, "uni-id": delGemPortMsg.UniId})
		return fmt.Errorf("received UniId value exceeds range: %d, device-id: %s",
			delGemPortMsg.UniId, dh.DeviceID)
	}
	uniID := uint8(delGemPortMsg.UniId)
	tpID, err := cmn.GetTpIDFromTpPath(delGemPortMsg.TpInstancePath)
	if err != nil {
		logger.Errorw(ctx, "error-extracting-tp-id-from-tp-path", log.Fields{
			"device-id": dh.DeviceID, "err": err, "tp-path": delGemPortMsg.TpInstancePath})
		return err
	}
	logger.Infow(ctx, "delete-gem-port-request", log.Fields{
		"device-id": dh.DeviceID, "uni-id": uniID, "tpID": tpID, "gem": delGemPortMsg.GemPortId})
	//a removal of some GemPort would never remove the complete TechProfile entry (done on T-Cont)

	return dh.deleteTechProfileResource(ctx, uniID, tpID, delGemPortMsg.TpInstancePath,
		avcfg.CResourceGemPort, delGemPortMsg.GemPortId)

}

func (dh *deviceHandler) handleDeleteTcontRequest(ctx context.Context, delTcontMsg *ia.DeleteTcontMessage) error {
	logger.Infow(ctx, "delete-tcont-request start", log.Fields{"device-id": dh.DeviceID, "uni-id": delTcontMsg.UniId, "tcont": delTcontMsg.AllocId})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw(ctx, "onuTechProf instance not set up for DelTcont request - ignoring request",
			log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("techProfile DelTcont request while onuTechProf instance not setup: %s", dh.DeviceID)
	}

	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.LockTpProcMutex()
	defer dh.pOnuTP.UnlockTpProcMutex()

	if delTcontMsg.UniId >= platform.MaxUnisPerOnu {
		logger.Errorw(ctx, "delete-tcont UniId exceeds range", log.Fields{
			"device-id": dh.DeviceID, "uni-id": delTcontMsg.UniId})
		return fmt.Errorf("received UniId value exceeds range: %d, device-id: %s",
			delTcontMsg.UniId, dh.DeviceID)
	}
	uniID := uint8(delTcontMsg.UniId)
	tpPath := delTcontMsg.TpInstancePath
	tpID, err := cmn.GetTpIDFromTpPath(tpPath)
	if err != nil {
		logger.Errorw(ctx, "error-extracting-tp-id-from-tp-path", log.Fields{
			"device-id": dh.DeviceID, "err": err, "tp-path": tpPath})
		return err
	}
	pDevEntry.FreeTcont(ctx, uint16(delTcontMsg.AllocId))

	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	logger.Debugw(ctx, "remove-tcont-in-kv", log.Fields{"device-id": dh.DeviceID, "uni-id": uniID, "tpID": tpID, "tcont": delTcontMsg.AllocId})
	err1 := pDevEntry.UpdateOnuKvStore(log.WithSpanFromContext(dctx, ctx))
	if err1 != nil {
		logger.Errorw(ctx, "UpdateOnuKvStore-failed", log.Fields{"device-id": dh.DeviceID, "err": err1})
		return err1
	}

	return dh.deleteTechProfileResource(ctx, uniID, tpID, delTcontMsg.TpInstancePath,
		avcfg.CResourceTcont, delTcontMsg.AllocId)

}

func (dh *deviceHandler) deleteTechProfileResource(ctx context.Context,
	uniID uint8, tpID uint8, pathString string, resource avcfg.ResourceEntry, entryID uint32) error {
	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}
	var resourceName string
	if avcfg.CResourceGemPort == resource {
		resourceName = "Gem"
	} else {
		resourceName = "Tcont"
	}

	// deadline context to ensure completion of background routines waited for
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	dh.pOnuTP.ResetTpProcessingErrorIndication(uniID, tpID)

	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish
	go dh.pOnuTP.DeleteTpResource(log.WithSpanFromContext(dctx, ctx), uniID, tpID, pathString,
		resource, entryID, &wg)
	dh.waitForCompletion(ctx, cancel, &wg, resourceName+"Delete") //wait for background process to finish
	if err := dh.pOnuTP.GetTpProcessingErrorIndication(uniID, tpID); err != nil {
		logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.DeviceID})
		return err
	}

	if dh.pOnuTP.IsTechProfileConfigCleared(ctx, uniID, tpID) {
		logger.Debugw(ctx, "techProfile-config-cleared", log.Fields{"device-id": dh.DeviceID, "uni-id": uniID, "tpID": tpID})
		if bTpModify := pDevEntry.UpdateOnuUniTpPath(ctx, uniID, tpID, ""); bTpModify {
			pDevEntry.ResetKvProcessingErrorIndication()
			dctx2, cancel2 := context.WithDeadline(context.Background(), deadline)
			defer cancel2()
			// Removal of the gem id mapping represents the removal of the tech profile
			logger.Debugw(ctx, "remove-techProfile-indication-in-kv", log.Fields{"device-id": dh.DeviceID, "uni-id": uniID, "tpID": tpID})
			err := pDevEntry.UpdateOnuKvStore(log.WithSpanFromContext(dctx2, ctx))
			if err != nil {
				logger.Errorw(ctx, "UpdateOnuKvStore-failed", log.Fields{"device-id": dh.DeviceID, "err": err})
				return err
			}
		}
	}
	logger.Debugw(ctx, "delete-tech-profile-resource-completed", log.Fields{"device-id": dh.DeviceID,
		"uni-id": uniID, "tpID": tpID, "resource-type": resourceName, "resource-id": entryID})
	return nil
}

// FlowUpdateIncremental removes and/or adds the flow changes on a given device
func (dh *deviceHandler) FlowUpdateIncremental(ctx context.Context,
	apOfFlowChanges *of.FlowChanges,
	apOfGroupChanges *of.FlowGroupChanges, apFlowMetaData *of.FlowMetadata) error {
	logger.Debugw(ctx, "FlowUpdateIncremental started", log.Fields{"device-id": dh.DeviceID, "flow": apOfFlowChanges, "metadata": apFlowMetaData})
	var errorsList []error
	var retError error
	//Remove flows (always remove flows first - remove old and add new with same cookie may be part of the same request)
	if apOfFlowChanges.ToRemove != nil {
		for _, flowItem := range apOfFlowChanges.ToRemove.Items {
			if flowItem.GetCookie() == 0 {
				logger.Warnw(ctx, "flow-remove no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.DeviceID})
				retError = fmt.Errorf("flow-remove no cookie, device-id %s", dh.DeviceID)
				errorsList = append(errorsList, retError)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw(ctx, "flow-remove inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.DeviceID})
				retError = fmt.Errorf("flow-remove inPort invalid, device-id %s", dh.DeviceID)
				errorsList = append(errorsList, retError)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.DeviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow, not regarded as error, just ignored
				logger.Debugw(ctx, "flow-remove for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.DeviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *cmn.OnuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw(ctx, "flow-remove inPort not found in UniPorts: ignore and continuing on checking further flows",
						log.Fields{"device-id": dh.DeviceID, "inPort": flowInPort})
					retError = fmt.Errorf("flow-remove inPort not found in UniPorts, inPort %d, device-id %s",
						flowInPort, dh.DeviceID)
					errorsList = append(errorsList, retError)
					continue
				}
				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw(ctx, "flow-remove port indications", log.Fields{
					"device-id": dh.DeviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.Name})

				if dh.GetFlowMonitoringIsRunning(loUniPort.UniID) {
					// Step1 : Fill flowControlBlock
					// Step2 : Push the flowControlBlock to ONU channel
					// Step3 : Wait on response channel for response
					// Step4 : Return error value
					startTime := time.Now()
					respChan := make(chan error)
					flowCb := FlowCb{
						ctx:          ctx,
						addFlow:      false,
						flowItem:     flowItem,
						flowMetaData: nil,
						uniPort:      loUniPort,
						respChan:     &respChan,
					}
					dh.flowCbChan[loUniPort.UniID] <- flowCb
					logger.Infow(ctx, "process-flow-remove-start", log.Fields{"device-id": dh.DeviceID})
					// Wait on the channel for flow handlers return value
					retError = <-respChan
					logger.Infow(ctx, "process-flow-remove-end", log.Fields{"device-id": dh.DeviceID, "err": retError, "totalTimeSeconds": time.Since(startTime).Seconds()})
					if retError != nil {
						logger.Warnw(ctx, "flow-delete processing error: continuing on checking further flows",
							log.Fields{"device-id": dh.DeviceID, "error": retError})
						errorsList = append(errorsList, retError)
						continue
					}
				} else {
					retError = fmt.Errorf("flow-handler-routine-not-active-for-onu--device-id-%v", dh.DeviceID)
					errorsList = append(errorsList, retError)
				}
			}
		}
	}
	if apOfFlowChanges.ToAdd != nil {
		for _, flowItem := range apOfFlowChanges.ToAdd.Items {
			if flowItem.GetCookie() == 0 {
				logger.Debugw(ctx, "incremental flow-add no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.DeviceID})
				retError = fmt.Errorf("flow-add no cookie, device-id %s", dh.DeviceID)
				errorsList = append(errorsList, retError)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw(ctx, "flow-add inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.DeviceID})
				retError = fmt.Errorf("flow-add inPort invalid, device-id %s", dh.DeviceID)
				errorsList = append(errorsList, retError)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.DeviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow
				logger.Debugw(ctx, "flow-add for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.DeviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *cmn.OnuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw(ctx, "flow-add inPort not found in UniPorts: ignore and continuing on checking further flows",
						log.Fields{"device-id": dh.DeviceID, "inPort": flowInPort})
					retError = fmt.Errorf("flow-add inPort not found in UniPorts, inPort %d, device-id %s",
						flowInPort, dh.DeviceID)
					errorsList = append(errorsList, retError)
					continue
				}
				// let's still assume that we receive the flow-add only in some 'active' device state (as so far observed)
				// if not, we just throw some error here to have an indication about that, if we really need to support that
				//   then we would need to create some means to activate the internal stored flows
				//   after the device gets active automatically (and still with its dependency to the TechProfile)
				// for state checking compare also code here: processInterAdapterTechProfileDownloadReqMessage
				// also abort for the other still possible flows here
				if !dh.IsReadyForOmciConfig() {
					logger.Errorw(ctx, "flow-add rejected: improper device state", log.Fields{"device-id": dh.DeviceID,
						"last device-reason": dh.GetDeviceReasonString()})
					retError = fmt.Errorf("improper device state on device %s", dh.DeviceID)
					errorsList = append(errorsList, retError)
					continue
				}

				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw(ctx, "flow-add port indications", log.Fields{
					"device-id": dh.DeviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.Name})
				if dh.GetFlowMonitoringIsRunning(loUniPort.UniID) {
					// Step1 : Fill flowControlBlock
					// Step2 : Push the flowControlBlock to ONU channel
					// Step3 : Wait on response channel for response
					// Step4 : Return error value
					startTime := time.Now()
					respChan := make(chan error)
					flowCb := FlowCb{
						ctx:          ctx,
						addFlow:      true,
						flowItem:     flowItem,
						flowMetaData: apFlowMetaData,
						uniPort:      loUniPort,
						respChan:     &respChan,
					}
					dh.flowCbChan[loUniPort.UniID] <- flowCb
					logger.Infow(ctx, "process-flow-add-start", log.Fields{"device-id": dh.DeviceID})
					// Wait on the channel for flow handlers return value
					retError = <-respChan
					logger.Infow(ctx, "process-flow-add-end", log.Fields{"device-id": dh.DeviceID, "err": retError, "totalTimeSeconds": time.Since(startTime).Seconds()})
					if retError != nil {
						logger.Warnw(ctx, "flow-add processing error: continuing on checking further flows",
							log.Fields{"device-id": dh.DeviceID, "error": retError})
						errorsList = append(errorsList, retError)
						continue
					}
				} else {
					retError = fmt.Errorf("flow-handler-routine-not-active-for-onu--device-id-%v", dh.DeviceID)
					errorsList = append(errorsList, retError)
				}
			}
		}
	}
	if len(errorsList) > 0 {
		logger.Errorw(ctx, "error-processing-flow", log.Fields{"device-id": dh.DeviceID, "errList": errorsList})
		return fmt.Errorf("errors-installing-one-or-more-flows-groups, errors:%v", errorsList)
	}
	return nil
}

// disableDevice locks the ONU and its UNI/VEIP ports (admin lock via OMCI)
// following are the expected device states after this activity:
// Device Admin-State : down (on rwCore), Port-State: UNKNOWN, Conn-State: REACHABLE, Reason: omci-admin-lock
// (Conn-State: REACHABLE might conflict with some previous ONU Down indication - maybe to be resolved later)
func (dh *deviceHandler) disableDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "disable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
	dh.mutexForDisableDeviceRequested.Lock()
	dh.disableDeviceRequested = true
	dh.mutexForDisableDeviceRequested.Unlock()
	//admin-lock reason can also be used uniquely for setting the DeviceState accordingly
	//note that disableDevice sequences in some 'ONU active' state may yield also
	// "tech...delete-success" or "omci-flow-deleted" according to further received requests in the end
	// - inblock state checking to prevent possibly unneeded processing (on command repitition)
	if dh.getDeviceReason() != cmn.DrOmciAdminLock {
		//disable-device shall be just a UNi/ONU-G related admin state setting
		//all other configurations/FSM's shall not be impacted and shall execute as required by the system

		if dh.IsReadyForOmciConfig() {
			// disable UNI ports/ONU
			// *** should generate UniDisableStateDone event - used to disable the port(s) on success
			if dh.pLockStateFsm == nil {
				dh.createUniLockFsm(ctx, true, cmn.UniDisableStateDone)
			} else { //LockStateFSM already init
				dh.pLockStateFsm.SetSuccessEvent(cmn.UniDisableStateDone)
				dh.runUniLockFsm(ctx, true)
			}
		} else {
			logger.Debugw(ctx, "DeviceStateUpdate upon disable", log.Fields{
				"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.DeviceID})
			// disable device should have no impact on ConnStatus
			if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
				DeviceId:   dh.DeviceID,
				ConnStatus: connectStatusINVALID, //use some dummy value to prevent modification of the ConnStatus
				OperStatus: voltha.OperStatus_UNKNOWN,
			}); err != nil {
				//TODO with VOL-3045/VOL-3046: return the error and stop further processing
				logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
			}
			// DeviceReason to update acc.to modified py code as per beginning of Sept 2020

			//TODO with VOL-3045/VOL-3046: catch and return error, valid for all occurrences in the codebase
			_ = dh.ReasonUpdate(ctx, cmn.DrOmciAdminLock, true)
		}
	}
}

// reEnableDevice unlocks the ONU and its UNI/VEIP ports (admin unlock via OMCI)
func (dh *deviceHandler) reEnableDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "reenable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})

	//setting readyForOmciConfig here is just a workaround for BBSIM testing in the sequence
	//  OnuSoftReboot-disable-enable, because BBSIM does not generate a new OnuIndication-Up event after SoftReboot
	//  which is the assumption for real ONU's, where the ready-state is then set according to the following MibUpload/Download
	//  for real ONU's that should have nearly no influence
	//  Note that for real ONU's there is anyway a problematic situation with following sequence:
	//		OnuIndication-Dw (or not active at all) (- disable) - enable: here already the LockFsm may run into timeout (no OmciResponse)
	//      but that anyway is hopefully resolved by some OnuIndication-Up event (maybe to be tested)
	//      one could also argue, that a device-enable should also enable attempts for specific omci configuration
	dh.SetReadyForOmciConfig(true) //needed to allow subsequent flow/techProf config (on BBSIM)

	// enable ONU/UNI ports
	// *** should generate cmn.UniEnableStateDone event - used to disable the port(s) on success
	dh.mutexForDisableDeviceRequested.Lock()
	dh.disableDeviceRequested = false
	dh.mutexForDisableDeviceRequested.Unlock()
	if dh.pUnlockStateFsm == nil {
		dh.createUniLockFsm(ctx, false, cmn.UniEnableStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.SetSuccessEvent(cmn.UniEnableStateDone)
		dh.runUniLockFsm(ctx, false)
	}
}

func (dh *deviceHandler) reconcileDeviceOnuInd(ctx context.Context) {
	logger.Info(ctx, "reconciling - simulate onu indication", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	if err := pDevEntry.RestoreDataFromOnuKvStore(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		if err == fmt.Errorf("no-ONU-data-found") {
			logger.Debugw(ctx, "no persistent data found - abort reconciling", log.Fields{"device-id": dh.DeviceID})
		} else {
			logger.Errorw(ctx, "reconciling - restoring OnuTp-data failed - abort", log.Fields{"err": err, "device-id": dh.DeviceID})
		}
		dh.stopReconciling(ctx, false, cWaitReconcileFlowNoActivity)
		return
	}
	var onuIndication oop.OnuIndication
	pDevEntry.MutexPersOnuConfig.RLock()
	onuIndication.IntfId = pDevEntry.SOnuPersistentData.PersIntfID
	onuIndication.OnuId = pDevEntry.SOnuPersistentData.PersOnuID
	onuIndication.OperState = pDevEntry.SOnuPersistentData.PersOperState
	onuIndication.AdminState = pDevEntry.SOnuPersistentData.PersAdminState
	pDevEntry.MutexPersOnuConfig.RUnlock()
	_ = dh.createInterface(ctx, &onuIndication)
}

func (dh *deviceHandler) ReconcileDeviceTechProf(ctx context.Context) bool {
	logger.Info(ctx, "reconciling - trigger tech profile config", log.Fields{"device-id": dh.DeviceID})

	continueWithFlowConfig := false

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "reconciling - no valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, false, cWaitReconcileFlowNoActivity)
		return continueWithFlowConfig
	}
	dh.pOnuTP.LockTpProcMutex()
	defer dh.pOnuTP.UnlockTpProcMutex()

	pDevEntry.MutexPersOnuConfig.RLock()
	persMutexLock := true
	if len(pDevEntry.SOnuPersistentData.PersUniConfig) == 0 {
		pDevEntry.MutexPersOnuConfig.RUnlock()
		logger.Info(ctx, "reconciling - no uni-configs have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)
		return continueWithFlowConfig
	}
	flowsFound := false
	techProfsFound := false
	techProfInstLoadFailed := false
outerLoop:
	for _, uniData := range pDevEntry.SOnuPersistentData.PersUniConfig {
		uniID := uniData.PersUniID
		//TODO: check for uni-port specific reconcilement in case of multi-uni-port-per-onu-support
		if !dh.anyTpPathExists(uniData.PersTpPathMap) {
			logger.Debugw(ctx, "reconciling - no TPs stored for uniID",
				log.Fields{"uni-id": uniID, "device-id": dh.DeviceID})
			continue
		}
		//release MutexPersOnuConfig before TechProfile (ANIConfig) processing as otherwise the reception of
		//  OMCI frames may get completely stuck due to lock request within IncrementMibDataSync() at OMCI
		//  frame reception may also lock the complete OMCI reception processing based on mutexRxSchedMap
		pDevEntry.MutexPersOnuConfig.RUnlock()
		persMutexLock = false
		techProfsFound = true // set to true if we found TP once for any UNI port
		var iaTechTpInst ia.TechProfileDownloadMessage
		var ok bool
		for tpID := range uniData.PersTpPathMap {
			pDevEntry.MutexReconciledTpInstances.RLock()
			if iaTechTpInst, ok = pDevEntry.ReconciledTpInstances[uniID][tpID]; !ok {
				logger.Errorw(ctx, "reconciling - no reconciled tp instance available",
					log.Fields{"tp-id": tpID, "tpPath": uniData.PersTpPathMap[tpID], "uni-id": uniData.PersUniID,
						"device-id": dh.DeviceID})
				techProfInstLoadFailed = true // stop loading tp instance as soon as we hit failure
				pDevEntry.MutexReconciledTpInstances.RUnlock()
				break outerLoop
			}
			pDevEntry.MutexReconciledTpInstances.RUnlock()
			continueWithFlowConfig = true // valid TP found - try flow configuration later
			var tpInst tech_profile.TechProfileInstance
			switch techTpInst := iaTechTpInst.TechTpInstance.(type) {
			case *ia.TechProfileDownloadMessage_TpInstance: // supports only GPON, XGPON, XGS-PON
				tpInst = *techTpInst.TpInstance
				logger.Debugw(ctx, "reconciling - received-tp-instance-successfully-after-reconcile", log.Fields{
					"tp-id": tpID, "tpPath": uniData.PersTpPathMap[tpID], "uni-id": uniData.PersUniID, "device-id": dh.DeviceID})
			default: // do not support epon or other tech
				logger.Errorw(ctx, "reconciling - unsupported-tech-profile", log.Fields{
					"tp-id": tpID, "tpPath": uniData.PersTpPathMap[tpID], "uni-id": uniData.PersUniID, "device-id": dh.DeviceID})
				techProfInstLoadFailed = true // stop loading tp instance as soon as we hit failure
				break outerLoop
			}

			// deadline context to ensure completion of background routines waited for
			//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
			deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
			dctx, cancel := context.WithDeadline(ctx, deadline)

			dh.pOnuTP.ResetTpProcessingErrorIndication(uniData.PersUniID, tpID)
			var wg sync.WaitGroup
			wg.Add(1) // for the 1 go routine to finish
			go dh.pOnuTP.ConfigureUniTp(log.WithSpanFromContext(dctx, ctx), uniData.PersUniID, uniData.PersTpPathMap[tpID], tpInst, &wg)
			dh.waitForCompletion(ctx, cancel, &wg, "TechProfReconcile") //wait for background process to finish
			if err := dh.pOnuTP.GetTpProcessingErrorIndication(uniData.PersUniID, tpID); err != nil {
				logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.DeviceID})
				techProfInstLoadFailed = true // stop loading tp instance as soon as we hit failure
				break outerLoop
			}
		} // for all TpPath entries for this UNI
		if len(uniData.PersFlowParams) != 0 {
			flowsFound = true
		}
		pDevEntry.MutexPersOnuConfig.RLock() //set protection again for loop test on SOnuPersistentData
		persMutexLock = true
	} // for all UNI entries from SOnuPersistentData
	if persMutexLock { // if loop was left with MutexPersOnuConfig still set
		pDevEntry.MutexPersOnuConfig.RUnlock()
	}

	//had to move techProf/flow result evaluation into separate function due to SCA complexity limit
	dh.updateReconcileStates(ctx, techProfsFound, techProfInstLoadFailed, flowsFound)

	return continueWithFlowConfig
}

func (dh *deviceHandler) updateReconcileStates(ctx context.Context,
	abTechProfsFound bool, abTechProfInstLoadFailed bool, abFlowsFound bool) {
	if !abTechProfsFound {
		logger.Warn(ctx, "reconciling - no TPs have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)
		return
	}
	if abTechProfInstLoadFailed {
		_ = dh.ReasonUpdate(ctx, cmn.DrTechProfileConfigDownloadFailed, dh.IsReconcilingReasonUpdate())
		dh.stopReconciling(ctx, false, cWaitReconcileFlowNoActivity)
		return
	} else if dh.IsSkipOnuConfigReconciling() {
		_ = dh.ReasonUpdate(ctx, cmn.DrTechProfileConfigDownloadSuccess, dh.IsReconcilingReasonUpdate())
	}
	if !abFlowsFound {
		logger.Warn(ctx, "reconciling - no flows have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)
	}
}

func (dh *deviceHandler) ReconcileDeviceFlowConfig(ctx context.Context) {
	logger.Debugw(ctx, "reconciling - trigger flow config", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "reconciling - no valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, false, cWaitReconcileFlowNoActivity)
		return
	}

	pDevEntry.MutexPersOnuConfig.RLock()
	if len(pDevEntry.SOnuPersistentData.PersUniConfig) == 0 {
		pDevEntry.MutexPersOnuConfig.RUnlock()
		logger.Warn(ctx, "reconciling - no uni-configs have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)
		return
	}
	flowsFound := false
	var uniVlanConfigEntries []uint8
	var loWaitGroupWTO cmn.WaitGroupWithTimeOut

	for _, uniData := range pDevEntry.SOnuPersistentData.PersUniConfig {
		//TODO: check for uni-port specific reconcilement in case of multi-uni-port-per-onu-support
		if len(uniData.PersFlowParams) == 0 {
			logger.Debugw(ctx, "reconciling - no flows stored for uniID",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.DeviceID})
			continue
		}
		if !dh.anyTpPathExists(uniData.PersTpPathMap) {
			logger.Warnw(ctx, "reconciling flows - but no TPs stored for uniID, abort",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.DeviceID})
			// It doesn't make sense to configure any flows if no TPs are available
			continue
		}
		//release MutexPersOnuConfig before VlanConfig processing as otherwise the reception of
		//  OMCI frames may get completely stuck due to lock request within IncrementMibDataSync() at OMCI
		//  frame reception may also lock the complete OMCI reception processing based on mutexRxSchedMap
		pDevEntry.MutexPersOnuConfig.RUnlock()

		var uniPort *cmn.OnuUniPort
		var exist bool
		uniNo := platform.MkUniPortNum(ctx, dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(), uint32(uniData.PersUniID))
		if uniPort, exist = dh.uniEntityMap[uniNo]; !exist {
			logger.Errorw(ctx, "reconciling - OnuUniPort data not found  - terminate reconcilement",
				log.Fields{"uniNo": uniNo, "device-id": dh.DeviceID})
			dh.stopReconciling(ctx, false, cWaitReconcileFlowNoActivity)
			return
		}
		//needed to split up function due to sca complexity
		dh.updateReconcileFlowConfig(ctx, uniPort, uniData.PersFlowParams, uniVlanConfigEntries, &loWaitGroupWTO, &flowsFound)

		logger.Debugw(ctx, "reconciling - flows processed", log.Fields{
			"device-id": dh.DeviceID, "uni-id": uniData.PersUniID,
			"NumUniFlows":       dh.UniVlanConfigFsmMap[uniData.PersUniID].NumUniFlows,
			"ConfiguredUniFlow": dh.UniVlanConfigFsmMap[uniData.PersUniID].ConfiguredUniFlow})
		// this can't be used as global finished reconciling flag because
		// assumes is getting called before the state machines for the last flow is completed,
		// while this is not guaranteed.
		pDevEntry.MutexPersOnuConfig.RLock() //set protection again for loop test on SOnuPersistentData
	} // for all UNI entries from SOnuPersistentData
	pDevEntry.MutexPersOnuConfig.RUnlock()

	if !flowsFound {
		logger.Warn(ctx, "reconciling - no flows have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)
		return
	}
	logger.Debugw(ctx, "reconciling flows - waiting on ready indication of requested UNIs", log.Fields{
		"device-id": dh.DeviceID, "expiry": dh.reconcileExpiryVlanConfig})
	if executed := loWaitGroupWTO.WaitTimeout(dh.reconcileExpiryVlanConfig); executed {
		logger.Debugw(ctx, "reconciling flows for all UNI's has been finished in time",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, true, cWaitReconcileFlowAbortOnSuccess)
		if pDevEntry != nil {
			pDevEntry.SendChReconcilingFlowsFinished(ctx, true)
		}
	} else {
		logger.Errorw(ctx, "reconciling - timeout waiting for reconciling flows for all UNI's to be finished!",
			log.Fields{"device-id": dh.DeviceID})
		dh.stopReconciling(ctx, false, cWaitReconcileFlowAbortOnError)
		if pDevEntry != nil {
			pDevEntry.SendChReconcilingFlowsFinished(ctx, false)
		}
		return
	}
	_ = dh.ReasonUpdate(ctx, cmn.DrOmciFlowsPushed, dh.IsReconcilingReasonUpdate())
}

func (dh *deviceHandler) updateReconcileFlowConfig(ctx context.Context, apUniPort *cmn.OnuUniPort,
	aPersFlowParam []cmn.UniVlanFlowParams, aUniVlanConfigEntries []uint8,
	apWaitGroup *cmn.WaitGroupWithTimeOut, apFlowsFound *bool) {
	flowsProcessed := 0
	lastFlowToReconcile := false
	loUniID := apUniPort.UniID
	for _, flowData := range aPersFlowParam {
		if !(*apFlowsFound) {
			*apFlowsFound = true
			syncChannel := make(chan struct{})
			// start go routine with select() on reconciling vlan config channel before
			// starting vlan config reconciling process to prevent loss of any signal
			// this routine just collects all the received 'flow-reconciled' signals - possibly from different UNI's
			go dh.waitOnUniVlanConfigReconcilingReady(ctx, syncChannel, apWaitGroup)
			//block until the wait routine is really blocked on channel input
			//  in order to prevent to early ready signal from VlanConfig processing
			<-syncChannel
		}
		if flowsProcessed == len(aPersFlowParam)-1 {
			var uniAdded bool
			lastFlowToReconcile = true
			if aUniVlanConfigEntries, uniAdded = dh.appendIfMissing(aUniVlanConfigEntries, loUniID); uniAdded {
				apWaitGroup.Add(1) //increment the waiting group
			}
		}
		logger.Debugw(ctx, "reconciling - add flow with cookie slice", log.Fields{
			"device-id": dh.DeviceID, "uni-id": loUniID,
			"flowsProcessed": flowsProcessed, "cookies": flowData.CookieSlice})
		dh.lockVlanConfig.Lock()
		//the CookieSlice can be passed 'by value' here, - which internally passes its reference
		if _, exist := dh.UniVlanConfigFsmMap[loUniID]; exist {
			if err := dh.UniVlanConfigFsmMap[loUniID].SetUniFlowParams(ctx, flowData.VlanRuleParams.TpID,
				flowData.CookieSlice, uint16(flowData.VlanRuleParams.MatchVid), uint8(flowData.VlanRuleParams.MatchPcp), uint16(flowData.VlanRuleParams.SetVid), uint8(flowData.VlanRuleParams.SetPcp), flowData.VlanRuleParams.InnerCvlan, lastFlowToReconcile, flowData.Meter, nil); err != nil {
				logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.DeviceID})
			}
		} else {
			if err := dh.createVlanFilterFsm(ctx, apUniPort, flowData.VlanRuleParams.TpID, flowData.CookieSlice,
				uint16(flowData.VlanRuleParams.MatchVid), uint8(flowData.VlanRuleParams.MatchPcp), uint16(flowData.VlanRuleParams.SetVid),
				uint8(flowData.VlanRuleParams.SetPcp), flowData.VlanRuleParams.InnerCvlan, cmn.OmciVlanFilterAddDone, lastFlowToReconcile, flowData.Meter, nil); err != nil {
				logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.DeviceID})
			}
		}
		dh.lockVlanConfig.Unlock()
		flowsProcessed++
	} //for all flows of this UNI
}

// waitOnUniVlanConfigReconcilingReady collects all VlanConfigReady signals from VlanConfig FSM processing in reconciling
//
//	and decrements the according handler wait group waiting for these indications
func (dh *deviceHandler) waitOnUniVlanConfigReconcilingReady(ctx context.Context, aSyncChannel chan<- struct{},
	waitGroup *cmn.WaitGroupWithTimeOut) {
	var reconciledUniVlanConfigEntries []uint8
	var appended bool
	expiry := dh.GetReconcileExpiryVlanConfigAbort()
	logger.Debugw(ctx, "start waiting on reconcile vlanConfig ready indications", log.Fields{
		"device-id": dh.DeviceID, "expiry": expiry})
	// indicate blocking on channel now to the caller
	aSyncChannel <- struct{}{}
	for {
		select {
		case uniIndication := <-dh.chUniVlanConfigReconcilingDone:
			switch uniIndication {
			// no activity requested (should normally not be received) - just continue waiting
			case cWaitReconcileFlowNoActivity:
			// waiting on channel inputs from VlanConfig for all UNI's to be aborted on error condition
			case cWaitReconcileFlowAbortOnError:
				logger.Warn(ctx, "waitReconcileFlow aborted on error",
					log.Fields{"device-id": dh.DeviceID, "rxEntries": reconciledUniVlanConfigEntries})
				return
			// waiting on channel inputs from VlanConfig for all UNI's to be aborted on success condition
			case cWaitReconcileFlowAbortOnSuccess:
				logger.Warn(ctx, "waitReconcileFlow aborted on success",
					log.Fields{"device-id": dh.DeviceID, "rxEntries": reconciledUniVlanConfigEntries})
				return
			// this should be a valid UNI vlan config done indication
			default:
				if uniIndication < platform.MaxUnisPerOnu {
					logger.Info(ctx, "reconciling flows has been finished in time for this UNI",
						log.Fields{"device-id": dh.DeviceID, "uni-id": uniIndication})
					if reconciledUniVlanConfigEntries, appended =
						dh.appendIfMissing(reconciledUniVlanConfigEntries, uint8(uniIndication)); appended {
						waitGroup.Done()
					}
				} else {
					logger.Errorw(ctx, "received unexpected UNI flowConfig done indication - is ignored", log.Fields{"device-id": dh.DeviceID, "uni-id": uniIndication})
				}
			} //switch uniIndication

		case <-time.After(expiry): //a bit longer than reconcileExpiryVlanConfig
			logger.Errorw(ctx, "timeout waiting for reconciling all UNI flows to be finished!",
				log.Fields{"device-id": dh.DeviceID})
			return
		}
	}
}

func (dh *deviceHandler) GetReconcileExpiryVlanConfigAbort() time.Duration {
	return dh.reconcileExpiryVlanConfig + (500 * time.Millisecond)
}

func (dh *deviceHandler) appendIfMissing(slice []uint8, val uint8) ([]uint8, bool) {
	for _, ele := range slice {
		if ele == val {
			return slice, false
		}
	}
	return append(slice, val), true
}

// sendChReconcileFinished - sends true or false on reconcileFinish channel
func (dh *deviceHandler) sendChReconcileFinished(success bool) {
	if dh != nil { //if the object still exists (might have been already deleted in background)
		//use asynchronous channel sending to avoid stucking on non-waiting receiver
		select {
		case dh.chReconcilingFinished <- success:
		default:
		}
	}
}

// SendChUniVlanConfigFinished - sends the Uni number on channel if the flow reconcilement for this UNI is finished
func (dh *deviceHandler) SendChUniVlanConfigFinished(value uint16) {
	if dh != nil { //if the object still exists (might have been already deleted in background)
		//use asynchronous channel sending to avoid stucking on non-waiting receiver
		select {
		case dh.chUniVlanConfigReconcilingDone <- value:
		default:
		}
	}
}

func (dh *deviceHandler) deleteDevicePersistencyData(ctx context.Context) error {
	logger.Debugw(ctx, "delete device persistency data", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		//IfDevEntry does not exist here, no problem - no persistent data should have been stored
		logger.Debugw(ctx, "OnuDevice does not exist - nothing to delete", log.Fields{"device-id": dh.DeviceID})
		return nil
	}

	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := pDevEntry.DeleteDataFromOnuKvStore(log.WithSpanFromContext(dctx, ctx))
	if err != nil {
		logger.Errorw(ctx, "delete data from onu kv store failed", log.Fields{"device-id": dh.DeviceID, "err": err})
		return err
	}
	return nil
}

// func (dh *deviceHandler) rebootDevice(ctx context.Context, device *voltha.Device) error {
// before this change here return like this was used:
//
//	return fmt.Errorf("device-unreachable: %s, %s", dh.DeviceID, device.SerialNumber)
//
// was and is called in background - error return does not make sense
func (dh *deviceHandler) rebootDevice(ctx context.Context, aCheckDeviceState bool, device *voltha.Device) {
	logger.Infow(ctx, "reboot-device", log.Fields{"device-id": dh.DeviceID, "SerialNumber": dh.device.SerialNumber})
	if aCheckDeviceState && device.ConnectStatus != voltha.ConnectStatus_REACHABLE {
		logger.Errorw(ctx, "device-unreachable", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
		return
	}
	if err := dh.pOnuOmciDevice.Reboot(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-rebooting-device", log.Fields{"device-id": dh.DeviceID, "error": err})
		return
	}

	//transfer the possibly modified logical uni port state
	dh.DisableUniPortStateUpdate(ctx)

	logger.Debugw(ctx, "call DeviceStateUpdate upon reboot", log.Fields{
		"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.DeviceID})
	// do not set the ConnStatus here as it may conflict with the parallel setting from ONU down indication (updateInterface())
	if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
		DeviceId:   dh.DeviceID,
		ConnStatus: connectStatusINVALID, //use some dummy value to prevent modification of the ConnStatus
		OperStatus: voltha.OperStatus_DISCOVERED,
	}); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
		return
	}
	if err := dh.ReasonUpdate(ctx, cmn.DrRebooting, true); err != nil {
		return
	}
	dh.SetReadyForOmciConfig(false)
	//no specific activity to synchronize any internal FSM to the 'rebooted' state is explicitly done here
	//  the expectation ids for a real device, that it will be synced with the expected following 'down' indication
	//  as BBSIM does not support this testing requires explicite disable/enable device calls in which sequence also
	//  all other FSM's should be synchronized again
}

// doOnuSwUpgrade initiates the SW download transfer to the ONU and on success activates the (inactive) image
//
//	used only for old - R2.7 style - upgrade API
func (dh *deviceHandler) doOnuSwUpgrade(ctx context.Context, apImageDsc *voltha.ImageDownload,
	apDownloadManager *swupg.AdapterDownloadManager) error {
	logger.Debugw(ctx, "onuSwUpgrade requested", log.Fields{
		"device-id": dh.DeviceID, "image-name": (*apImageDsc).Name})

	var err error
	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "start Onu SW upgrade rejected: no valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("start Onu SW upgrade rejected: no valid OnuDevice for device-id: %s", dh.DeviceID)
	}

	if dh.IsReadyForOmciConfig() {
		var inactiveImageID uint16
		if inactiveImageID, err = pDevEntry.GetInactiveImageMeID(ctx); err == nil {
			dh.lockUpgradeFsm.Lock()
			//lockUpgradeFsm must be release before cancellation as this may implicitly request RemoveOnuUpgradeFsm()
			//  but must be still locked at calling createOnuUpgradeFsm
			if dh.pOnuUpradeFsm == nil {
				err = dh.createOnuUpgradeFsm(ctx, pDevEntry, cmn.OmciOnuSwUpgradeDone)
				dh.lockUpgradeFsm.Unlock()
				if err == nil {
					if err = dh.pOnuUpradeFsm.SetDownloadParams(ctx, inactiveImageID, apImageDsc, apDownloadManager); err != nil {
						logger.Errorw(ctx, "onu upgrade fsm could not set parameters", log.Fields{
							"device-id": dh.DeviceID, "error": err})
					}
				} else {
					logger.Errorw(ctx, "onu upgrade fsm could not be created", log.Fields{
						"device-id": dh.DeviceID, "error": err})
				}
			} else { //OnuSw upgrade already running - restart (with possible abort of running)
				dh.lockUpgradeFsm.Unlock()
				logger.Debugw(ctx, "Onu SW upgrade already running - abort", log.Fields{"device-id": dh.DeviceID})
				if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
					dh.upgradeCanceled = true
					dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_REQUEST) //complete abort
				}
				//no effort spent anymore for the old API to automatically cancel and restart the download
				//  like done for the new API
			}
		} else {
			logger.Errorw(ctx, "start Onu SW upgrade rejected: no inactive image", log.Fields{
				"device-id": dh.DeviceID, "error": err})
		}
	} else {
		logger.Errorw(ctx, "start Onu SW upgrade rejected: no active OMCI connection", log.Fields{"device-id": dh.DeviceID})
		err = fmt.Errorf("start Onu SW upgrade rejected: no active OMCI connection for device-id: %s", dh.DeviceID)
	}
	return err
}

// onuSwUpgradeAfterDownload initiates the SW download transfer to the ONU with activate and commit options
// after the OnuImage has been downloaded to the adapter, called in background
func (dh *deviceHandler) onuSwUpgradeAfterDownload(ctx context.Context, apImageRequest *voltha.DeviceImageDownloadRequest,
	apDownloadManager *swupg.FileDownloadManager, aImageIdentifier string) {

	var err error
	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "start Onu SW upgrade rejected: no valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
		return
	}

	var inactiveImageID uint16
	if inactiveImageID, err = pDevEntry.GetInactiveImageMeID(ctx); err == nil {
		logger.Debugw(ctx, "onuSwUpgrade requested", log.Fields{
			"device-id": dh.DeviceID, "image-version": apImageRequest.Image.Version, "to onu-image": inactiveImageID})

		dh.lockUpgradeFsm.Lock()
		//lockUpgradeFsm must be release before cancellation as this may implicitly request RemoveOnuUpgradeFsm()
		//  but must be still locked at calling createOnuUpgradeFsm
		//  (and working with a local pointer copy does not work here if asynchronous request are done to fast
		//	[e.g.leaving the local pointer on nil even though a creation is already on the way])
		if dh.pOnuUpradeFsm != nil {
			//OnuSw upgrade already running on this device (e.g. with activate/commit not yet set)
			// abort the current processing, running upgrades are always aborted by newer request
			logger.Debugw(ctx, "Onu SW upgrade already running - abort previous activity", log.Fields{"device-id": dh.DeviceID})
			//flush the remove upgradeFsmChan channel
			select {
			case <-dh.upgradeFsmChan:
				logger.Debugw(ctx, "flushed-upgrade-fsm-channel", log.Fields{"device-id": dh.DeviceID})
			default:
			}
			dh.lockUpgradeFsm.Unlock()
			if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
				dh.upgradeCanceled = true
				dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_REQUEST) //complete abort
			}
			select {
			case <-time.After(cTimeOutRemoveUpgrade * time.Second):
				logger.Errorw(ctx, "could not remove Upgrade FSM in time, aborting", log.Fields{"device-id": dh.DeviceID})
				//should not appear, can't proceed with new upgrade, perhaps operator can retry manually later
				return
			case <-dh.upgradeFsmChan:
				logger.Debugw(ctx, "recent Upgrade FSM removed, proceed with new request", log.Fields{"device-id": dh.DeviceID})
			}
			dh.lockUpgradeFsm.Lock() //lock again for following creation
		}

		//here it can be assumed that no running upgrade processing exists (anymore)
		//OmciOnuSwUpgradeDone could be used to create some event notification with information on upgrade completion,
		//  but none yet defined
		err = dh.createOnuUpgradeFsm(ctx, pDevEntry, cmn.OmciOnuSwUpgradeDone)
		dh.lockUpgradeFsm.Unlock()
		if err == nil {
			if err = dh.pOnuUpradeFsm.SetDownloadParamsAfterDownload(ctx, inactiveImageID,
				apImageRequest, apDownloadManager, aImageIdentifier); err != nil {
				logger.Errorw(ctx, "onu upgrade fsm could not set parameters", log.Fields{
					"device-id": dh.DeviceID, "error": err})
				return
			}
		} else {
			logger.Errorw(ctx, "onu upgrade fsm could not be created", log.Fields{
				"device-id": dh.DeviceID, "error": err})
		}
		return
	}
	logger.Errorw(ctx, "start Onu SW upgrade rejected: no inactive image", log.Fields{
		"device-id": dh.DeviceID, "error": err})
}

// onuSwActivateRequest ensures activation of the requested image with commit options
func (dh *deviceHandler) onuSwActivateRequest(ctx context.Context,
	aVersion string, aCommitRequest bool) (*voltha.ImageState, error) {
	var err error
	//SW activation for the ONU image may have two use cases, one of them is selected here according to following prioritization:
	//  1.) activation of the image for a started upgrade process (in case the running upgrade runs on the requested image)
	//  2.) activation of the inactive image

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "Onu image activation rejected: no valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
		return nil, fmt.Errorf("no valid OnuDevice for device-id: %s", dh.DeviceID)
	}
	dh.lockUpgradeFsm.RLock()
	if dh.pOnuUpradeFsm != nil {
		dh.lockUpgradeFsm.RUnlock()
		onuVolthaDevice, getErr := dh.getDeviceFromCore(ctx, dh.DeviceID)
		if getErr != nil || onuVolthaDevice == nil {
			logger.Errorw(ctx, "Failed to fetch Onu device for image activation", log.Fields{"device-id": dh.DeviceID, "err": getErr})
			return nil, fmt.Errorf("could not fetch device for device-id: %s", dh.DeviceID)
		}
		if dh.upgradeCanceled { //avoid starting some new action in case it is already doing the cancelation
			logger.Errorw(ctx, "Some upgrade procedure still runs cancelation - abort", log.Fields{"device-id": dh.DeviceID})
			return nil, fmt.Errorf("request collides with some ongoing cancelation for device-id: %s", dh.DeviceID)
		}
		//  use the OnuVendor identification from this device for the internal unique name
		imageIdentifier := onuVolthaDevice.VendorId + aVersion //head on vendor ID of the ONU
		// 1.) check a started upgrade process and relay the activation request to it
		if err = dh.pOnuUpradeFsm.SetActivationParamsRunning(ctx, imageIdentifier, aCommitRequest); err != nil {
			//if some ONU upgrade is ongoing we do not accept some explicit ONU image-version related activation
			logger.Errorw(ctx, "onu upgrade fsm did not accept activation while running", log.Fields{
				"device-id": dh.DeviceID, "error": err})
			return nil, fmt.Errorf("activation not accepted for this version for device-id: %s", dh.DeviceID)
		}
		logger.Debugw(ctx, "image activation acknowledged by onu upgrade processing", log.Fields{
			"device-id": dh.DeviceID, "image-id": imageIdentifier})
		pImageStates := dh.pOnuUpradeFsm.GetImageStates(ctx, "", aVersion)
		return pImageStates, nil
	} //else
	dh.lockUpgradeFsm.RUnlock()

	// 2.) check if requested image-version equals the inactive one and start its activation
	//   (image version is not [yet] checked - would be possible, but with increased effort ...)
	var inactiveImageID uint16
	if inactiveImageID, err = pDevEntry.GetInactiveImageMeID(ctx); err != nil || inactiveImageID > 1 {
		logger.Errorw(ctx, "get inactive image failed", log.Fields{
			"device-id": dh.DeviceID, "err": err, "image-id": inactiveImageID})
		return nil, fmt.Errorf("no valid inactive image found for device-id: %s", dh.DeviceID)
	}
	dh.lockUpgradeFsm.Lock() //lock again for following creation
	err = dh.createOnuUpgradeFsm(ctx, pDevEntry, cmn.OmciOnuSwUpgradeDone)
	dh.lockUpgradeFsm.Unlock()
	if err == nil {
		if err = dh.pOnuUpradeFsm.SetActivationParamsStart(ctx, aVersion,
			inactiveImageID, aCommitRequest); err != nil {
			logger.Errorw(ctx, "onu upgrade fsm did not accept activation to start", log.Fields{
				"device-id": dh.DeviceID, "error": err})
			return nil, fmt.Errorf("activation to start from scratch not accepted for device-id: %s", dh.DeviceID)
		}
		logger.Debugw(ctx, "inactive image activation acknowledged by onu upgrade", log.Fields{
			"device-id": dh.DeviceID, "image-version": aVersion})
		pImageStates := dh.pOnuUpradeFsm.GetImageStates(ctx, "", aVersion)
		return pImageStates, nil
	} //else
	logger.Errorw(ctx, "onu upgrade fsm could not be created", log.Fields{
		"device-id": dh.DeviceID, "error": err})
	return nil, fmt.Errorf("could not start upgradeFsm for device-id: %s", dh.DeviceID)
}

// onuSwCommitRequest ensures commitment of the requested image
func (dh *deviceHandler) onuSwCommitRequest(ctx context.Context,
	aVersion string) (*voltha.ImageState, error) {
	var err error
	//SW commitment for the ONU image may have two use cases, one of them is selected here according to following prioritization:
	//  1.) commitment of the image for a started upgrade process (in case the running upgrade runs on the requested image)
	//  2.) commitment of the active image

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "Onu image commitment rejected: no valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
		return nil, fmt.Errorf("no valid OnuDevice for device-id: %s", dh.DeviceID)
	}
	dh.lockUpgradeFsm.RLock()
	if dh.pOnuUpradeFsm != nil {
		dh.lockUpgradeFsm.RUnlock()
		onuVolthaDevice, getErr := dh.getDeviceFromCore(ctx, dh.DeviceID)
		if getErr != nil || onuVolthaDevice == nil {
			logger.Errorw(ctx, "Failed to fetch Onu device for image commitment", log.Fields{"device-id": dh.DeviceID, "err": getErr})
			return nil, fmt.Errorf("could not fetch device for device-id: %s", dh.DeviceID)
		}
		if dh.upgradeCanceled { //avoid starting some new action in case it is already doing the cancelation
			logger.Errorw(ctx, "Some upgrade procedure still runs cancelation - abort", log.Fields{"device-id": dh.DeviceID})
			return nil, fmt.Errorf("request collides with some ongoing cancelation for device-id: %s", dh.DeviceID)
		}
		//  use the OnuVendor identification from this device for the internal unique name
		imageIdentifier := onuVolthaDevice.VendorId + aVersion //head on vendor ID of the ONU
		// 1.) check a started upgrade process and relay the commitment request to it
		// the running upgrade may be based either on the imageIdentifier (started from download)
		//   or on the imageVersion (started from pure activation)
		if err = dh.pOnuUpradeFsm.SetCommitmentParamsRunning(ctx, imageIdentifier, aVersion); err != nil {
			//if some ONU upgrade is ongoing we do not accept some explicit different ONU image-version related commitment
			logger.Errorw(ctx, "onu upgrade fsm did not accept commitment while running", log.Fields{
				"device-id": dh.DeviceID, "error": err})
			return nil, fmt.Errorf("commitment not accepted for this version for device-id: %s", dh.DeviceID)
		}
		logger.Debugw(ctx, "image commitment acknowledged by onu upgrade processing", log.Fields{
			"device-id": dh.DeviceID, "image-id": imageIdentifier})
		pImageStates := dh.pOnuUpradeFsm.GetImageStates(ctx, "", aVersion)
		return pImageStates, nil
	} //else
	dh.lockUpgradeFsm.RUnlock()

	// 2.) use the active image to directly commit
	var activeImageID uint16
	if activeImageID, err = pDevEntry.GetActiveImageMeID(ctx); err != nil || activeImageID > 1 {
		logger.Errorw(ctx, "get active image failed", log.Fields{
			"device-id": dh.DeviceID, "err": err, "image-id": activeImageID})
		return nil, fmt.Errorf("no valid active image found for device-id: %s", dh.DeviceID)
	}
	dh.lockUpgradeFsm.Lock() //lock again for following creation
	err = dh.createOnuUpgradeFsm(ctx, pDevEntry, cmn.OmciOnuSwUpgradeDone)
	dh.lockUpgradeFsm.Unlock()
	if err == nil {
		if err = dh.pOnuUpradeFsm.SetCommitmentParamsStart(ctx, aVersion, activeImageID); err != nil {
			logger.Errorw(ctx, "onu upgrade fsm did not accept commitment to start", log.Fields{
				"device-id": dh.DeviceID, "error": err})
			return nil, fmt.Errorf("commitment to start from scratch not accepted for device-id: %s", dh.DeviceID)
		}
		logger.Debugw(ctx, "active image commitment acknowledged by onu upgrade", log.Fields{
			"device-id": dh.DeviceID, "image-version": aVersion})
		pImageStates := dh.pOnuUpradeFsm.GetImageStates(ctx, "", aVersion)
		return pImageStates, nil
	} //else
	logger.Errorw(ctx, "onu upgrade fsm could not be created", log.Fields{
		"device-id": dh.DeviceID, "error": err})
	return nil, fmt.Errorf("could not start upgradeFsm for device-id: %s", dh.DeviceID)
}

func (dh *deviceHandler) requestOnuSwUpgradeState(ctx context.Context, aImageIdentifier string,
	aVersion string) *voltha.ImageState {
	var pImageState *voltha.ImageState
	dh.lockUpgradeFsm.RLock()
	defer dh.lockUpgradeFsm.RUnlock()
	if dh.pOnuUpradeFsm != nil {
		pImageState = dh.pOnuUpradeFsm.GetImageStates(ctx, aImageIdentifier, aVersion)
	} else { //use the last stored ImageState (if the requested Imageversion coincides)
		if aVersion == dh.pLastUpgradeImageState.Version {
			pImageState = dh.pLastUpgradeImageState
		} else { //state request for an image version different from last processed image version
			pImageState = &voltha.ImageState{
				Version: aVersion,
				//we cannot state something concerning this version
				DownloadState: voltha.ImageState_DOWNLOAD_UNKNOWN,
				Reason:        voltha.ImageState_NO_ERROR,
				ImageState:    voltha.ImageState_IMAGE_UNKNOWN,
			}
		}
	}
	return pImageState
}

func (dh *deviceHandler) cancelOnuSwUpgrade(ctx context.Context, aImageIdentifier string,
	aVersion string, pDeviceImageState *voltha.DeviceImageState) {
	pDeviceImageState.DeviceId = dh.DeviceID
	pDeviceImageState.ImageState.Version = aVersion
	dh.lockUpgradeFsm.RLock()
	if dh.pOnuUpradeFsm != nil {
		dh.lockUpgradeFsm.RUnlock()
		// so then we cancel the upgrade operation
		// but before we still request the actual upgrade states for the direct response
		pImageState := dh.pOnuUpradeFsm.GetImageStates(ctx, aImageIdentifier, aVersion)
		pDeviceImageState.ImageState.DownloadState = pImageState.DownloadState
		pDeviceImageState.ImageState.Reason = voltha.ImageState_CANCELLED_ON_REQUEST
		pDeviceImageState.ImageState.ImageState = pImageState.ImageState
		if pImageState.DownloadState != voltha.ImageState_DOWNLOAD_UNKNOWN {
			//so here the imageIdentifier or version equals to what is used in the upgrade FSM
			if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
				dh.upgradeCanceled = true
				dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_REQUEST) //complete abort
			}
		} //nothing to cancel (upgrade FSM for different image stays alive)
	} else {
		dh.lockUpgradeFsm.RUnlock()
		// if no upgrade is ongoing, nothing is canceled and accordingly the states of the requested image are unknown
		// reset also the dh handler LastUpgradeImageState (not relevant anymore/cleared)
		(*dh.pLastUpgradeImageState).DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
		(*dh.pLastUpgradeImageState).Reason = voltha.ImageState_NO_ERROR
		(*dh.pLastUpgradeImageState).ImageState = voltha.ImageState_IMAGE_UNKNOWN
		(*dh.pLastUpgradeImageState).Version = "" //reset to 'no (relevant) upgrade done' (like initial state)
		pDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
		pDeviceImageState.ImageState.Reason = voltha.ImageState_NO_ERROR
		pDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
		//an abort request to a not active upgrade processing can be used to reset the device upgrade states completely
	}
}

func (dh *deviceHandler) getOnuImages(ctx context.Context) (*voltha.OnuImages, error) {

	var onuImageStatus *swupg.OnuImageStatus

	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry != nil {
		onuImageStatus = swupg.NewOnuImageStatus(dh, pDevEntry)
		pDevEntry.MutexOnuImageStatus.Lock()
		pDevEntry.POnuImageStatus = onuImageStatus
		pDevEntry.MutexOnuImageStatus.Unlock()

	} else {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return nil, fmt.Errorf("no-valid-OnuDevice-aborting")
	}
	images, err := onuImageStatus.GetOnuImageStatus(ctx)
	pDevEntry.MutexOnuImageStatus.Lock()
	pDevEntry.POnuImageStatus = nil
	pDevEntry.MutexOnuImageStatus.Unlock()
	return images, err
}

//  deviceHandler methods that implement the adapters interface requests## end #########
// #####################################################################################

// ################  to be updated acc. needs of ONU Device ########################
// deviceHandler StateMachine related state transition methods ##### begin #########

func (dh *deviceHandler) logStateChange(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "Device FSM: ", log.Fields{"event name": string(e.Event),
		"src state": string(e.Src), "dst state": string(e.Dst), "device-id": dh.DeviceID})
}

// doStateInit provides the device update to the core
func (dh *deviceHandler) doStateInit(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "doStateInit-started", log.Fields{"device-id": dh.DeviceID})
	var err error

	// populate what we know.  rest comes later after mib sync
	dh.device.Root = false
	dh.device.Vendor = "OpenONU"
	dh.device.Model = "go"
	dh.device.Reason = cmn.DeviceReasonMap[cmn.DrActivatingOnu]
	_ = dh.ReasonUpdate(ctx, cmn.DrActivatingOnu, false)

	dh.logicalDeviceID = dh.DeviceID // really needed - what for ??? //TODO!!!

	if !dh.IsReconciling() {
		logger.Infow(ctx, "DeviceUpdate", log.Fields{"deviceReason": dh.device.Reason, "device-id": dh.DeviceID})
		if err = dh.updateDeviceInCore(ctx, dh.device); err != nil {
			logger.Errorw(ctx, "device-update-failed", log.Fields{"device-id": dh.device.Id, "error": err})
		}
		//TODO Need to Update Device Reason To CORE as part of device update userstory
	} else {
		logger.Infow(ctx, "reconciling - don't notify core about DeviceUpdate",
			log.Fields{"device-id": dh.DeviceID})
	}

	dh.parentID = dh.device.ParentId
	dh.ponPortNumber = dh.device.ParentPortNo

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	logger.Debugw(ctx, "device-updated", log.Fields{"device-id": dh.DeviceID, "proxyAddressID": dh.ProxyAddressID,
		"proxyAddressType": dh.ProxyAddressType, "SNR": dh.device.SerialNumber,
		"ParentId": dh.parentID, "ParentPortNo": dh.ponPortNumber})

	/*
		self._pon = PonPort.create(self, self._pon_port_number)
		self._pon.add_peer(self.parent_id, self._pon_port_number)
		self.logger.debug('adding-pon-port-to-agent',
				   type=self._pon.get_port().type,
				   admin_state=self._pon.get_port().admin_state,
				   oper_status=self._pon.get_port().oper_status,
				   )
	*/
	if !dh.IsReconciling() {
		logger.Infow(ctx, "adding-pon-port", log.Fields{"device-id": dh.DeviceID, "ponPortNo": dh.ponPortNumber})
		var ponPortNo uint32 = 1
		if dh.ponPortNumber != 0 {
			ponPortNo = dh.ponPortNumber
		}

		pPonPort := &voltha.Port{
			DeviceId:   dh.DeviceID,
			PortNo:     ponPortNo,
			Label:      fmt.Sprintf("pon-%d", ponPortNo),
			Type:       voltha.Port_PON_ONU,
			OperStatus: voltha.OperStatus_ACTIVE,
			Peers: []*voltha.Port_PeerPort{{DeviceId: dh.parentID, // Peer device  is OLT
				PortNo: ponPortNo}}, // Peer port is parent's port number
		}
		if err = dh.CreatePortInCore(ctx, pPonPort); err != nil {
			logger.Fatalf(ctx, "Device FSM: PortCreated-failed-%s:%s", err, dh.DeviceID)
			e.Cancel(err)
			return
		}
	} else {
		logger.Infow(ctx, "reconciling - pon-port already added", log.Fields{"device-id": dh.DeviceID})
	}
	logger.Debugw(ctx, "doStateInit-done", log.Fields{"device-id": dh.DeviceID})
}

// postInit setups the DeviceEntry for the conerned device
func (dh *deviceHandler) postInit(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "postInit-started", log.Fields{"device-id": dh.DeviceID})
	var err error
	/*
		dh.Client = oop.NewOpenoltClient(dh.clientCon)
		dh.pTransitionMap.Handle(ctx, GrpcConnected)
		return nil
	*/
	if err = dh.addOnuDeviceEntry(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		logger.Fatalf(ctx, "Device FSM: addOnuDeviceEntry-failed-%s:%s", err, dh.DeviceID)
		e.Cancel(err)
		return
	}

	if dh.IsReconciling() {
		go dh.reconcileDeviceOnuInd(ctx)
		// reconcilement will be continued after mib download is done
	}

	/*
			############################################################################
			# Setup Alarm handler
			self.events = AdapterEvents(self.core_proxy, device.id, self.logical_device_id,
										device.serial_number)
			############################################################################
			# Setup PM configuration for this device
			# Pass in ONU specific options
			kwargs = {
				OnuPmMetrics.DEFAULT_FREQUENCY_KEY: OnuPmMetrics.DEFAULT_ONU_COLLECTION_FREQUENCY,
				'heartbeat': self.heartbeat,
				OnuOmciPmMetrics.OMCI_DEV_KEY: self._onu_omci_device
			}
			self.logger.debug('create-pm-metrics', device_id=device.id, serial_number=device.serial_number)
			self._pm_metrics = OnuPmMetrics(self.events, self.core_proxy, self.device_id,
										   self.logical_device_id, device.serial_number,
										   grouped=True, freq_override=False, **kwargs)
			pm_config = self._pm_metrics.make_proto()
			self._onu_omci_device.set_pm_config(self._pm_metrics.omci_pm.openomci_interval_pm)
			self.logger.info("initial-pm-config", device_id=device.id, serial_number=device.serial_number)
			yield self.core_proxy.device_pm_config_update(pm_config, init=True)

			# Note, ONU ID and UNI intf set in add_uni_port method
			self._onu_omci_device.alarm_synchronizer.set_alarm_params(mgr=self.events,
																	  ani_ports=[self._pon])

			# Code to Run OMCI Test Action
			kwargs_omci_test_action = {
				OmciTestRequest.DEFAULT_FREQUENCY_KEY:
					OmciTestRequest.DEFAULT_COLLECTION_FREQUENCY
			}
			serial_number = device.serial_number
			self._test_request = OmciTestRequest(self.core_proxy,
										   self.omci_agent, self.device_id,
										   AniG, serial_number,
										   self.logical_device_id,
										   exclusive=False,
										   **kwargs_omci_test_action)

			self.Enabled = True
		else:
			self.logger.info('onu-already-activated')
	*/

	logger.Debugw(ctx, "postInit-done", log.Fields{"device-id": dh.DeviceID})
}

// doStateConnected get the device info and update to voltha core
// for comparison of the original method (not that easy to uncomment): compare here:
//
//	voltha-openolt-adapter/adaptercore/device_handler.go
//	-> this one obviously initiates all communication interfaces of the device ...?
func (dh *deviceHandler) doStateConnected(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "doStateConnected-started", log.Fields{"device-id": dh.DeviceID})
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debugw(ctx, "doStateConnected-done", log.Fields{"device-id": dh.DeviceID})
}

// doStateUp handle the onu up indication and update to voltha core
func (dh *deviceHandler) doStateUp(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "doStateUp-started", log.Fields{"device-id": dh.DeviceID})
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debugw(ctx, "doStateUp-done", log.Fields{"device-id": dh.DeviceID})

	/*
		// Synchronous call to update device state - this method is run in its own go routine
		if err := dh.coreProxy.DeviceStateUpdate(ctx, dh.device.Id, voltha.ConnectStatus_REACHABLE,
			voltha.OperStatus_ACTIVE); err != nil {
			logger.Errorw("Failed to update device with OLT UP indication", log.Fields{"device-id": dh.device.Id, "error": err})
			return err
		}
		return nil
	*/
}

// doStateDown handle the onu down indication
func (dh *deviceHandler) doStateDown(ctx context.Context, e *fsm.Event) {

	logger.Debugw(ctx, "doStateDown-started", log.Fields{"device-id": dh.DeviceID})
	var err error

	device := dh.device
	if device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Errorw(ctx, "Failed to fetch handler device", log.Fields{"device-id": dh.DeviceID})
		e.Cancel(err)
		return
	}

	cloned := proto.Clone(device).(*voltha.Device)
	logger.Debugw(ctx, "do-state-down", log.Fields{"ClonedDeviceID": cloned.Id})
	/*
		// Update the all ports state on that device to disable
		if er := dh.coreProxy.PortsStateUpdate(ctx, cloned.Id, voltha.OperStatus_UNKNOWN); er != nil {
			logger.Errorw("updating-ports-failed", log.Fields{"device-id": device.Id, "error": er})
			return er
		}

		//Update the device oper state and connection status
		cloned.OperStatus = voltha.OperStatus_UNKNOWN
		cloned.ConnectStatus = common.ConnectStatus_UNREACHABLE
		dh.device = cloned

		if er := dh.coreProxy.DeviceStateUpdate(ctx, cloned.Id, cloned.ConnectStatus, cloned.OperStatus); er != nil {
			logger.Errorw("error-updating-device-state", log.Fields{"device-id": device.Id, "error": er})
			return er
		}

		//get the child device for the parent device
		onuDevices, err := dh.coreProxy.GetChildDevices(ctx, dh.device.Id)
		if err != nil {
			logger.Errorw("failed to get child devices information", log.Fields{"device-id": dh.device.Id, "error": err})
			return err
		}
		for _, onuDevice := range onuDevices.Items {

			// Update onu state as down in onu adapter
			onuInd := oop.OnuIndication{}
			onuInd.OperState = "down"
			er := dh.adapterProxy.SendInterAdapterMessage(ctx, &onuInd, ca.InterAdapterMessageType_ONU_IND_REQUEST,
				"openolt", onuDevice.Type, onuDevice.Id, onuDevice.ProxyAddress.DeviceId, "")
			if er != nil {
				logger.Errorw("Failed to send inter-adapter-message", log.Fields{"OnuInd": onuInd,
					"From Adapter": "openolt", "DevieType": onuDevice.Type, "device-id": onuDevice.Id})
				//Do not return here and continue to process other ONUs
			}
		}
		// * Discovered ONUs entries need to be cleared , since after OLT
		//   is up, it starts sending discovery indications again* /
		dh.discOnus = sync.Map{}
		logger.Debugw("do-state-down-end", log.Fields{"device-id": device.Id})
		return nil
	*/
	err = errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debugw(ctx, "doStateDown-done", log.Fields{"device-id": dh.DeviceID})
}

// deviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// deviceHandler utility methods ##### begin #########

// GetOnuDeviceEntry gets the ONU device entry and may wait until its value is defined
func (dh *deviceHandler) GetOnuDeviceEntry(ctx context.Context, aWait bool) *mib.OnuDeviceEntry {
	dh.lockDevice.RLock()
	pOnuDeviceEntry := dh.pOnuOmciDevice
	if aWait && pOnuDeviceEntry == nil {
		//keep the read sema short to allow for subsequent write
		dh.lockDevice.RUnlock()
		logger.Debugw(ctx, "Waiting for DeviceEntry to be set ...", log.Fields{"device-id": dh.DeviceID})
		// based on concurrent processing the deviceEntry setup may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(60 * time.Second): //timer may be discussed ...
			logger.Errorw(ctx, "No valid DeviceEntry set after maxTime", log.Fields{"device-id": dh.DeviceID})
			return nil
		case <-dh.deviceEntrySet:
			logger.Debugw(ctx, "devicEntry ready now - continue", log.Fields{"device-id": dh.DeviceID})
			// if written now, we can return the written value without sema
			return dh.pOnuOmciDevice
		}
	}
	dh.lockDevice.RUnlock()
	return pOnuDeviceEntry
}

// setDeviceHandlerEntries sets the ONU device entry within the handler
func (dh *deviceHandler) setDeviceHandlerEntries(apDeviceEntry *mib.OnuDeviceEntry, apOnuTp *avcfg.OnuUniTechProf,
	apOnuMetricsMgr *pmmgr.OnuMetricsManager, apOnuAlarmMgr *almgr.OnuAlarmManager, apSelfTestHdlr *otst.SelfTestControlBlock) {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	dh.pOnuOmciDevice = apDeviceEntry
	dh.pOnuTP = apOnuTp
	dh.pOnuMetricsMgr = apOnuMetricsMgr
	dh.pAlarmMgr = apOnuAlarmMgr
	dh.pSelfTestHdlr = apSelfTestHdlr
}

// addOnuDeviceEntry creates a new ONU device or returns the existing
//
//nolint:unparam
func (dh *deviceHandler) addOnuDeviceEntry(ctx context.Context) error {
	logger.Debugw(ctx, "adding-deviceEntry", log.Fields{"device-id": dh.DeviceID})

	deviceEntry := dh.GetOnuDeviceEntry(ctx, false)
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = mib.NewOnuDeviceEntry(ctx, dh.coreClient, dh, dh.pOpenOnuAc)
		onuTechProfProc := avcfg.NewOnuUniTechProf(ctx, dh, deviceEntry)
		onuMetricsMgr := pmmgr.NewOnuMetricsManager(ctx, dh, deviceEntry)
		onuAlarmManager := almgr.NewAlarmManager(ctx, dh, deviceEntry)
		selfTestHdlr := otst.NewSelfTestMsgHandlerCb(ctx, dh, deviceEntry)
		//error treatment possible //TODO!!!
		dh.setDeviceHandlerEntries(deviceEntry, onuTechProfProc, onuMetricsMgr, onuAlarmManager, selfTestHdlr)
		// fire deviceEntry ready event to spread to possibly waiting processing
		dh.deviceEntrySet <- true
		logger.Debugw(ctx, "onuDeviceEntry-added", log.Fields{"device-id": dh.DeviceID})
	} else {
		logger.Debugw(ctx, "onuDeviceEntry-add: Device already exists", log.Fields{"device-id": dh.DeviceID})
	}
	// might be updated with some error handling !!!
	return nil
}

func (dh *deviceHandler) createInterface(ctx context.Context, onuind *oop.OnuIndication) error {
	logger.Debugw(ctx, "create_interface-started", log.Fields{"device-id": dh.DeviceID, "OnuId": onuind.GetOnuId(),
		"OnuIntfId": onuind.GetIntfId(), "OnuSerialNumber": onuind.GetSerialNumber()})

	dh.pOnuIndication = onuind // let's revise if storing the pointer is sufficient...

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}

	if !dh.IsReconciling() {
		if err := dh.StorePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue as there will be additional write attempts",
				log.Fields{"device-id": dh.DeviceID, "err": err})
		}
		logger.Debugw(ctx, "call DeviceStateUpdate upon create interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVATING, "device-id": dh.DeviceID})

		if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
			DeviceId:   dh.DeviceID,
			OperStatus: voltha.OperStatus_ACTIVATING,
			ConnStatus: voltha.ConnectStatus_REACHABLE,
		}); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
		}
	} else {
		logger.Info(ctx, "reconciling - don't notify core about DeviceStateUpdate to ACTIVATING",
			log.Fields{"device-id": dh.DeviceID})

		pDevEntry.MutexPersOnuConfig.RLock()
		if !pDevEntry.SOnuPersistentData.PersUniUnlockDone {
			pDevEntry.MutexPersOnuConfig.RUnlock()
			logger.Debugw(ctx, "reconciling - uni-ports were not unlocked before adapter restart - resume with a normal start-up",
				log.Fields{"device-id": dh.DeviceID})
			dh.stopReconciling(ctx, true, cWaitReconcileFlowNoActivity)

			//VOL-4965: Recover previously Activating ONU during reconciliation.
			if dh.device.OperStatus == common.OperStatus_ACTIVATING {
				logger.Debugw(ctx, "Reconciling an ONU in previously activating state, perform MIB reset and resume normal start up",
					log.Fields{"device-id": dh.DeviceID})
				pDevEntry.MutexPersOnuConfig.Lock()
				pDevEntry.SOnuPersistentData.PersMibLastDbSync = 0
				pDevEntry.MutexPersOnuConfig.Unlock()
			}
		} else {
			pDevEntry.MutexPersOnuConfig.RUnlock()
		}
	}
	// It does not look to me as if makes sense to work with the real core device here, (not the stored clone)?
	// in this code the GetDevice would just make a check if the DeviceID's Device still exists in core
	// in python code it looks as the started onu_omci_device might have been updated with some new instance state of the core device
	// but I would not know why, and the go code anyway does not work with the device directly anymore in the mib.OnuDeviceEntry
	// so let's just try to keep it simple ...
	/*
			device, err := dh.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx), dh.device.Id, dh.device.Id)
		    if err != nil || device == nil {
					//TODO: needs to handle error scenarios
				logger.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
				return errors.New("Voltha Device not found")
			}
	*/

	if err := pDevEntry.Start(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		return err
	}
	_ = dh.ReasonUpdate(ctx, cmn.DrStartingOpenomci, !dh.IsReconciling() || dh.IsReconcilingReasonUpdate())
	if !dh.IsReconciling() && !dh.GetSkipOnuConfigEnabled() {
		/* this might be a good time for Omci Verify message?  */
		verifyExec := make(chan bool)
		omciVerify := otst.NewOmciTestRequest(log.WithSpanFromContext(context.TODO(), ctx),
			dh.device.Id, pDevEntry.PDevOmciCC, false,
			true, true) //exclusive and allowFailure (anyway not yet checked)
		omciVerify.PerformOmciTest(log.WithSpanFromContext(context.TODO(), ctx), verifyExec)

		/* 	give the handler some time here to wait for the OMCi verification result
		after Timeout start and try MibUpload FSM anyway
		(to prevent stopping on just not supported OMCI verification from ONU) */
		select {
		case <-time.After(((cmn.CDefaultRetries+1)*otst.CTestRequestOmciTimeout + 1) * time.Second):
			logger.Warnw(ctx, "omci start-verification timed out (continue normal)", log.Fields{"device-id": dh.DeviceID})
		case testresult := <-verifyExec:
			logger.Infow(ctx, "Omci start verification done", log.Fields{"device-id": dh.DeviceID, "result": testresult})
		case <-dh.deviceDeleteCommChan:
			logger.Warnw(ctx, "Deleting device, stopping the omci test activity", log.Fields{"device-id": dh.DeviceID})
			return nil
		}
	}

	/* In py code it looks earlier (on activate ..)
			# Code to Run OMCI Test Action
			kwargs_omci_test_action = {
				OmciTestRequest.DEFAULT_FREQUENCY_KEY:
					OmciTestRequest.DEFAULT_COLLECTION_FREQUENCY
			}
			serial_number = device.serial_number
			self._test_request = OmciTestRequest(self.core_proxy,
											self.omci_agent, self.device_id,
											AniG, serial_number,
											self.logical_device_id,
											exclusive=False,
											**kwargs_omci_test_action)
	...
	                    # Start test requests after a brief pause
	                    if not self._test_request_started:
	                        self._test_request_started = True
	                        tststart = _STARTUP_RETRY_WAIT * (random.randint(1, 5))
	                        reactor.callLater(tststart, self._test_request.start_collector)

	*/
	/* which is then: in omci_test_request.py : */
	/*
	   def start_collector(self, callback=None):
	       """
	               Start the collection loop for an adapter if the frequency > 0

	               :param callback: (callable) Function to call to collect PM data
	       """
	       self.logger.info("starting-pm-collection", device_name=self.name, default_freq=self.default_freq)
	       if callback is None:
	           callback = self.perform_test_omci

	       if self.lc is None:
	           self.lc = LoopingCall(callback)

	       if self.default_freq > 0:
	           self.lc.start(interval=self.default_freq / 10)

	   def perform_test_omci(self):
	       """
	       Perform the initial test request
	       """
	       ani_g_entities = self._device.configuration.ani_g_entities
	       ani_g_entities_ids = list(ani_g_entities.keys()) if ani_g_entities \
	                                                     is not None else None
	       self._entity_id = ani_g_entities_ids[0]
	       self.logger.info('perform-test', entity_class=self._entity_class,
	                     entity_id=self._entity_id)
	       try:
	           frame = MEFrame(self._entity_class, self._entity_id, []).test()
	           result = yield self._device.omci_cc.send(frame)
	           if not result.fields['omci_message'].fields['success_code']:
	               self.logger.info('Self-Test Submitted Successfully',
	                             code=result.fields[
	                                 'omci_message'].fields['success_code'])
	           else:
	               raise TestFailure('Test Failure: {}'.format(
	                   result.fields['omci_message'].fields['success_code']))
	       except TimeoutError as e:
	           self.deferred.errback(failure.Failure(e))

	       except Exception as e:
	           self.logger.exception('perform-test-Error', e=e,
	                              class_id=self._entity_class,
	                              entity_id=self._entity_id)
	           self.deferred.errback(failure.Failure(e))

	*/

	// PM related heartbeat??? !!!TODO....
	//self._heartbeat.Enabled = True

	/* Note: Even though FSM calls look 'synchronous' here, FSM is running in background with the effect that possible errors
	 * 	 within the MibUpload are not notified in the OnuIndication response, this might be acceptable here,
	 *   as further OltAdapter processing may rely on the deviceReason event 'MibUploadDone' as a result of the FSM processing
	 *   otherwise some processing synchronization would be required - cmp. e.g TechProfile processing
	 */
	//call MibUploadFSM - transition up to state UlStInSync
	pMibUlFsm := pDevEntry.PMibUploadFsm.PFsm
	if pMibUlFsm != nil {
		if pMibUlFsm.Is(mib.UlStDisabled) {
			if err := pMibUlFsm.Event(mib.UlEvStart); err != nil {
				logger.Errorw(ctx, "MibSyncFsm: Can't go to state starting", log.Fields{"device-id": dh.DeviceID, "err": err})
				return fmt.Errorf("can't go to state starting: %s", dh.DeviceID)
			}
			logger.Debugw(ctx, "MibSyncFsm", log.Fields{"device-id": dh.DeviceID, "state": string(pMibUlFsm.Current())})
			//Determine ONU status and start/re-start MIB Synchronization tasks
			//Determine if this ONU has ever synchronized
			if pDevEntry.IsNewOnu() {
				if err := pMibUlFsm.Event(mib.UlEvResetMib); err != nil {
					logger.Errorw(ctx, "MibSyncFsm: Can't go to state resetting_mib", log.Fields{"device-id": dh.DeviceID, "err": err})
					return fmt.Errorf("can't go to state resetting_mib: %s", dh.DeviceID)
				}
			} else {
				if err := pMibUlFsm.Event(mib.UlEvVerifyAndStoreTPs); err != nil {
					logger.Errorw(ctx, "MibSyncFsm: Can't go to state verify and store TPs", log.Fields{"device-id": dh.DeviceID, "err": err})
					return fmt.Errorf("can't go to state verify and store TPs: %s", dh.DeviceID)
				}
				logger.Debugw(ctx, "state of MibSyncFsm", log.Fields{"device-id": dh.DeviceID, "state": string(pMibUlFsm.Current())})
			}
		} else {
			logger.Errorw(ctx, "wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(pMibUlFsm.Current()),
				"device-id": dh.DeviceID})
			return fmt.Errorf("wrong state of MibSyncFsm: %s", dh.DeviceID)
		}
	} else {
		logger.Errorw(ctx, "MibSyncFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("can't execute MibSync: %s", dh.DeviceID)
	}
	return nil
}

func (dh *deviceHandler) UpdateInterface(ctx context.Context) error {
	//state checking to prevent unneeded processing (eg. on ONU 'unreachable' and 'down')
	// (but note that the deviceReason may also have changed to e.g. TechProf*Delete_Success in between)
	if dh.getDeviceReason() != cmn.DrStoppingOpenomci {
		logger.Info(ctx, "updateInterface-started - stopping-device", log.Fields{"device-id": dh.DeviceID})

		//stop all running FSM processing - make use of the DH-state as mirrored in the deviceReason
		//here no conflict with aborted FSM's should arise as a complete OMCI initialization is assumed on ONU-Up
		//but that might change with some simple MDS check on ONU-Up treatment -> attention!!!
		if err := dh.resetFsms(ctx, true); err != nil {
			logger.Errorw(ctx, "error-updateInterface at FSM stop",
				log.Fields{"device-id": dh.DeviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
		//all stored persistent data are not valid anymore (loosing knowledge about the connected ONU)
		_ = dh.deleteDevicePersistencyData(ctx) //ignore possible errors here and continue, hope is that data is synchronized with new ONU-Up

		//deviceEntry stop without omciCC reset here, regarding the OMCI_CC still valid for this ONU
		//stop the device entry to allow for all system event transfers again
		pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.DeviceID})
			return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
		}
		_ = pDevEntry.Stop(log.WithSpanFromContext(context.TODO(), ctx), false)

		//TODO!!! remove existing traffic profiles
		/* from py code, if TP's exist, remove them - not yet implemented
		self._tp = dict()
		# Let TP download happen again
		for uni_id in self._tp_service_specific_task:
			self._tp_service_specific_task[uni_id].clear()
		for uni_id in self._tech_profile_download_done:
			self._tech_profile_download_done[uni_id].clear()
		*/

		dh.DisableUniPortStateUpdate(ctx)

		dh.SetReadyForOmciConfig(false)

		if err := dh.ReasonUpdate(ctx, cmn.DrStoppingOpenomci, true); err != nil {
			// abort: system behavior is just unstable ...
			return err
		}
		logger.Debugw(ctx, "call DeviceStateUpdate upon update interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_UNREACHABLE,
			"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.DeviceID})
		if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
			DeviceId:   dh.DeviceID,
			ConnStatus: voltha.ConnectStatus_UNREACHABLE,
			OperStatus: voltha.OperStatus_DISCOVERED,
		}); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state unreachable-discovered",
				log.Fields{"device-id": dh.DeviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
	} else {
		logger.Debugw(ctx, "updateInterface - device already stopped", log.Fields{"device-id": dh.DeviceID})
	}
	return nil
}

func (dh *deviceHandler) resetFsms(ctx context.Context, includingMibSyncFsm bool) error {
	//all possible FSM's are stopped or reset here to ensure their transition to 'disabled'
	//it is not sufficient to stop/reset the latest running FSM as done in previous versions
	//  as after down/up procedures all FSM's might be active/ongoing (in theory)
	//  and using the stop/reset event should never harm
	logger.Debugw(ctx, "resetFsms entered", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	//VOL-5260: During race conditions when adoptDevice has not yet completed
	// and deleteDevice is issued , returning error will further prevent clean up
	// at rwcore . Returning success for clean up to happen and discovery to happen again.
	if pDevEntry == nil {
		errMsg := fmt.Sprintf("Device entry is not found %s", dh.DeviceID)
		logger.Error(ctx, errMsg)
		return status.Error(codes.NotFound, errMsg)
	}
	if pDevEntry.PDevOmciCC != nil {
		pDevEntry.PDevOmciCC.CancelRequestMonitoring(ctx)
	}
	pDevEntry.MutexOnuImageStatus.RLock()
	if pDevEntry.POnuImageStatus != nil {
		pDevEntry.POnuImageStatus.CancelProcessing(ctx)
	}
	pDevEntry.MutexOnuImageStatus.RUnlock()

	if includingMibSyncFsm {
		pDevEntry.CancelProcessing(ctx)
	}
	//MibDownload may run
	pMibDlFsm := pDevEntry.PMibDownloadFsm.PFsm
	if pMibDlFsm != nil {
		_ = pMibDlFsm.Event(mib.DlEvReset)
	}
	//stop any deviceHandler reconcile processing (if running)
	dh.stopReconciling(ctx, false, cWaitReconcileFlowAbortOnError)
	//port lock/unlock FSM's may be active
	if dh.pUnlockStateFsm != nil {
		_ = dh.pUnlockStateFsm.PAdaptFsm.PFsm.Event(uniprt.UniEvReset)
	}
	if dh.pLockStateFsm != nil {
		_ = dh.pLockStateFsm.PAdaptFsm.PFsm.Event(uniprt.UniEvReset)
	}
	//techProfile related PonAniConfigFsm FSM may be active
	if dh.pOnuTP != nil {
		// should always be the case here
		// FSM  stop maybe encapsulated as OnuTP method - perhaps later in context of module splitting
		if dh.pOnuTP.PAniConfigFsm != nil {
			for uniTP := range dh.pOnuTP.PAniConfigFsm {
				dh.pOnuTP.PAniConfigFsm[uniTP].CancelProcessing(ctx)
			}
		}
		for _, uniPort := range dh.uniEntityMap {
			// reset the possibly existing VlanConfigFsm
			dh.lockVlanConfig.RLock()
			if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[uniPort.UniID]; exist {
				//VlanFilterFsm exists and was already started
				dh.lockVlanConfig.RUnlock()
				//ensure the FSM processing is stopped in case waiting for some response
				pVlanFilterFsm.CancelProcessing(ctx)
			} else {
				dh.lockVlanConfig.RUnlock()
			}
		}
	}

	dh.mutexCollectorFlag.Lock()
	logger.Debugw(ctx, "check-collector-is-running", log.Fields{"device-id": dh.device.Id, "flag": dh.collectorIsRunning})
	if dh.collectorIsRunning {
		// Stop collector routine
		dh.stopCollector <- true
		dh.collectorIsRunning = false
	}
	dh.mutexCollectorFlag.Unlock()

	dh.mutextAlarmManagerFlag.Lock()
	logger.Debugw(ctx, "check-alarm-manager-is-running", log.Fields{"device-id": dh.device.Id, "flag": dh.alarmManagerIsRunning})
	if dh.alarmManagerIsRunning {
		dh.stopAlarmManager <- true
		dh.alarmManagerIsRunning = false
	}
	dh.mutextAlarmManagerFlag.Unlock()

	dh.pSelfTestHdlr.SelfTestHandlerLock.Lock()
	logger.Debugw(ctx, "check-self-test-control-block-is-running", log.Fields{"device-id": dh.device.Id, "flag": dh.pSelfTestHdlr.SelfTestHandlerActive})
	if dh.pSelfTestHdlr.SelfTestHandlerActive {
		dh.pSelfTestHdlr.StopSelfTestModule <- true
		dh.pSelfTestHdlr.SelfTestHandlerActive = false
	}
	dh.pSelfTestHdlr.SelfTestHandlerLock.Unlock()

	// Note: We want flow deletes to be processed on onu down, so do not stop flow monitoring routines

	//reset a possibly running upgrade FSM
	//  (note the Upgrade FSM may stay alive e.g. in state UpgradeStWaitForCommit to endure the ONU reboot)
	dh.lockUpgradeFsm.RLock()
	lopOnuUpradeFsm := dh.pOnuUpradeFsm
	//lockUpgradeFsm must be release before cancellation as this may implicitly request RemoveOnuUpgradeFsm()
	dh.lockUpgradeFsm.RUnlock()
	if lopOnuUpradeFsm != nil {
		if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
			//here we do not expect intermediate cancelation, we still allow for other commands on this FSM
			//  (even though it may also run into direct cancellation, a bit hard to verify here)
			//  so don't set 'dh.upgradeCanceled = true' here!
			lopOnuUpradeFsm.CancelProcessing(ctx, false, voltha.ImageState_CANCELLED_ON_ONU_STATE) //conditional cancel
		}
	}

	logger.Infow(ctx, "resetFsms done", log.Fields{"device-id": dh.DeviceID})
	return nil
}

//nolint:unparam
func (dh *deviceHandler) processMibDatabaseSyncEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "MibInSync event received, adding uni ports and locking the ONU interfaces", log.Fields{"device-id": dh.DeviceID})

	// store persistent data collected during MIB upload processing
	if err := dh.StorePersistentData(ctx); err != nil {
		logger.Warnw(ctx, "store persistent data error - continue as there will be additional write attempts",
			log.Fields{"device-id": dh.DeviceID, "err": err})
	}
	_ = dh.ReasonUpdate(ctx, cmn.DrDiscoveryMibsyncComplete, !dh.IsReconciling() || dh.IsReconcilingReasonUpdate())
	dh.AddAllUniPorts(ctx)

	/* 200605: lock processing after initial MIBUpload removed now as the ONU should be in the lock state per default here */
	/* 201117: build_dt-berlin-pod-openonugo_1T8GEM_voltha_DT_openonugo_master_test runs into error TC
	 *    'Test Disable ONUs and OLT Then Delete ONUs and OLT for DT' with Sercom ONU, which obviously needs
	 *    disable/enable toggling here to allow traffic
	 *    but moreover it might be useful for tracking the interface operState changes if this will be implemented,
	 *    like the py comment says:
	 *      # start by locking all the unis till mib sync and initial mib is downloaded
	 *      # this way we can capture the port down/up events when we are ready
	 */

	// Init Uni Ports to Admin locked state
	// *** should generate UniLockStateDone event *****
	if dh.pLockStateFsm == nil {
		dh.createUniLockFsm(ctx, true, cmn.UniLockStateDone)
	} else { //LockStateFSM already init
		dh.pLockStateFsm.SetSuccessEvent(cmn.UniLockStateDone)
		dh.runUniLockFsm(ctx, true)
	}
}

//nolint:unparam
func (dh *deviceHandler) processUniLockStateDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Infow(ctx, "UniLockStateDone event: Starting MIB download", log.Fields{"device-id": dh.DeviceID})
	/*  Mib download procedure -
	***** should run over 'downloaded' state and generate MibDownloadDone event *****
	 */
	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	pMibDlFsm := pDevEntry.PMibDownloadFsm.PFsm
	if pMibDlFsm != nil {
		if pMibDlFsm.Is(mib.DlStDisabled) {
			if err := pMibDlFsm.Event(mib.DlEvStart); err != nil {
				logger.Errorw(ctx, "MibDownloadFsm: Can't go to state starting", log.Fields{"device-id": dh.DeviceID, "err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				logger.Debugw(ctx, "MibDownloadFsm", log.Fields{"device-id": dh.DeviceID, "state": string(pMibDlFsm.Current())})
				// maybe use more specific states here for the specific download steps ...
				if err := pMibDlFsm.Event(mib.DlEvCreateGal); err != nil {
					logger.Errorw(ctx, "MibDownloadFsm: Can't start CreateGal", log.Fields{"device-id": dh.DeviceID, "err": err})
				} else {
					logger.Debugw(ctx, "state of MibDownloadFsm", log.Fields{"device-id": dh.DeviceID, "state": string(pMibDlFsm.Current())})
					//Begin MIB data download (running autonomously)
				}
			}
		} else {
			logger.Errorw(ctx, "wrong state of MibDownloadFsm - want: disabled", log.Fields{"have": string(pMibDlFsm.Current()),
				"device-id": dh.DeviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
		/***** Mib download started */
	} else {
		logger.Errorw(ctx, "MibDownloadFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.DeviceID})
	}
}

//nolint:unparam
func (dh *deviceHandler) processMibDownloadDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Info(ctx, "MibDownloadDone event received, unlocking the ONU interfaces", log.Fields{"device-id": dh.DeviceID})
	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	if !dh.IsReconciling() {
		logger.Debugw(ctx, "call DeviceUpdate and DeviceStateUpdate upon mib-download done", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.DeviceID})
		// update device info in core
		pDevEntry.MutexPersOnuConfig.RLock()
		dh.device.Vendor = pDevEntry.SOnuPersistentData.PersVendorID
		dh.device.VendorId = pDevEntry.SOnuPersistentData.PersVendorID
		dh.device.Model = pDevEntry.SOnuPersistentData.PersVersion
		pDevEntry.MutexPersOnuConfig.RUnlock()
		dh.logicalDeviceID = dh.DeviceID
		if err := dh.updateDeviceInCore(ctx, dh.device); err != nil {
			logger.Errorw(ctx, "device-update-failed", log.Fields{"device-id": dh.device.Id, "error": err})
		}
		// update device state in core
		//we allow a possible OnuSw image commit only in the normal startup, not at reconciling
		// in case of adapter restart connected to an ONU upgrade I would not rely on the image quality
		// maybe some 'forced' commitment can be done in this situation from system management (or upgrade restarted)
		dh.checkOnOnuImageCommit(ctx)
		if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
			DeviceId:   dh.DeviceID,
			ConnStatus: voltha.ConnectStatus_REACHABLE,
			OperStatus: voltha.OperStatus_ACTIVE,
		}); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
		} else {
			logger.Debugw(ctx, "dev state updated to 'Oper.Active'", log.Fields{"device-id": dh.DeviceID})
		}
	} else {
		logger.Info(ctx, "reconciling - don't notify core about updated device info and DeviceStateUpdate to ACTIVE",
			log.Fields{"device-id": dh.DeviceID})
	}
	_ = dh.ReasonUpdate(ctx, cmn.DrInitialMibDownloaded, !dh.IsReconciling() || dh.IsReconcilingReasonUpdate())

	if !dh.GetCollectorIsRunning() {
		var waitForOmciProcessor sync.WaitGroup
		waitForOmciProcessor.Add(1)
		// Start PM collector routine
		go dh.StartCollector(ctx, &waitForOmciProcessor)
		waitForOmciProcessor.Wait()
	}
	if !dh.GetAlarmManagerIsRunning(ctx) {
		go dh.StartAlarmManager(ctx)
	}

	// Start flow handler routines per UNI
	for _, uniPort := range dh.uniEntityMap {
		// only if this port was enabled for use by the operator at startup
		if (1<<uniPort.UniID)&dh.pOpenOnuAc.config.UniPortMask == (1 << uniPort.UniID) {
			if !dh.GetFlowMonitoringIsRunning(uniPort.UniID) {
				go dh.PerOnuFlowHandlerRoutine(uniPort.UniID)
			}
		}
	}

	// Initialize classical L2 PM Interval Counters
	if err := dh.pOnuMetricsMgr.PAdaptFsm.PFsm.Event(pmmgr.L2PmEventInit); err != nil {
		// There is no way we should be landing here, but if we do then
		// there is nothing much we can do about this other than log error
		logger.Errorw(ctx, "error starting l2 pm fsm", log.Fields{"device-id": dh.device.Id, "err": err})
	}

	dh.SetReadyForOmciConfig(true)

	pDevEntry.MutexPersOnuConfig.RLock()
	if dh.IsReconciling() && pDevEntry.SOnuPersistentData.PersUniDisableDone {
		pDevEntry.MutexPersOnuConfig.RUnlock()
		logger.Warn(ctx, "reconciling - uni-ports were disabled by admin before adapter restart - keep the ports locked",
			log.Fields{"device-id": dh.DeviceID})
		dh.mutexForDisableDeviceRequested.Lock()
		dh.disableDeviceRequested = true
		dh.mutexForDisableDeviceRequested.Unlock()
		dh.ReconcileDeviceTechProf(ctx)
		// reconcilement will be continued after ani config is done
	} else {
		pDevEntry.MutexPersOnuConfig.RUnlock()
		// *** should generate UniUnlockStateDone event *****
		dh.mutexForDisableDeviceRequested.RLock()
		if !dh.disableDeviceRequested {
			if dh.pUnlockStateFsm == nil {
				dh.createUniLockFsm(ctx, false, cmn.UniUnlockStateDone)
			} else { //UnlockStateFSM already init
				dh.pUnlockStateFsm.SetSuccessEvent(cmn.UniUnlockStateDone)
				dh.runUniLockFsm(ctx, false)
			}
			dh.mutexForDisableDeviceRequested.RUnlock()
		} else {
			dh.mutexForDisableDeviceRequested.RUnlock()
			logger.Debugw(ctx, "Uni already lock", log.Fields{"device-id": dh.DeviceID})
		}
	}
}

//nolint:unparam
func (dh *deviceHandler) processUniUnlockStateDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	dh.EnableUniPortStateUpdate(ctx) //cmp python yield self.enable_ports()

	if !dh.IsReconciling() {
		logger.Infow(ctx, "UniUnlockStateDone event: Sending OnuUp event", log.Fields{"device-id": dh.DeviceID})
		raisedTs := time.Now().Unix()
		go dh.sendOnuOperStateEvent(ctx, voltha.OperStatus_ACTIVE, dh.DeviceID, raisedTs) //cmp python onu_active_event
		pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
			return
		}
		pDevEntry.MutexPersOnuConfig.Lock()
		pDevEntry.SOnuPersistentData.PersUniUnlockDone = true
		pDevEntry.MutexPersOnuConfig.Unlock()
		if err := dh.StorePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
				log.Fields{"device-id": dh.DeviceID, "err": err})
		}
	} else {
		logger.Info(ctx, "reconciling - don't notify core that onu went to active but trigger tech profile config",
			log.Fields{"device-id": dh.DeviceID})
		dh.ReconcileDeviceTechProf(ctx)

		// reconcilement will be continued after ani config is done

	}
}

//nolint:unparam
func (dh *deviceHandler) processUniDisableStateDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "DeviceStateUpdate upon disable", log.Fields{
		"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.DeviceID})

	// disable device should have no impact on ConnStatus
	if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
		DeviceId:   dh.DeviceID,
		ConnStatus: connectStatusINVALID, //use some dummy value to prevent modification of the ConnStatus
		OperStatus: voltha.OperStatus_UNKNOWN,
	}); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
	}

	logger.Debugw(ctx, "DeviceReasonUpdate upon disable", log.Fields{"reason": cmn.DeviceReasonMap[cmn.DrOmciAdminLock], "device-id": dh.DeviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	_ = dh.ReasonUpdate(ctx, cmn.DrOmciAdminLock, true)

	//transfer the modified logical uni port state
	dh.DisableUniPortStateUpdate(ctx)

	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	pDevEntry.MutexPersOnuConfig.Lock()
	pDevEntry.SOnuPersistentData.PersUniDisableDone = true
	pDevEntry.MutexPersOnuConfig.Unlock()
	if err := dh.StorePersistentData(ctx); err != nil {
		logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
			log.Fields{"device-id": dh.DeviceID, "err": err})
	}
}

//nolint:unparam
func (dh *deviceHandler) processUniEnableStateDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "DeviceStateUpdate upon re-enable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.DeviceID})
	if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
		DeviceId:   dh.DeviceID,
		ConnStatus: voltha.ConnectStatus_REACHABLE,
		OperStatus: voltha.OperStatus_ACTIVE,
	}); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
	}

	logger.Debugw(ctx, "DeviceReasonUpdate upon re-enable", log.Fields{
		"reason": cmn.DeviceReasonMap[cmn.DrOnuReenabled], "device-id": dh.DeviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	_ = dh.ReasonUpdate(ctx, cmn.DrOnuReenabled, true)

	//transfer the modified logical uni port state
	dh.EnableUniPortStateUpdate(ctx)

	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	pDevEntry.MutexPersOnuConfig.Lock()
	pDevEntry.SOnuPersistentData.PersUniDisableDone = false
	pDevEntry.MutexPersOnuConfig.Unlock()
	if err := dh.StorePersistentData(ctx); err != nil {
		logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
			log.Fields{"device-id": dh.DeviceID, "err": err})
	}
}

//nolint:unparam
func (dh *deviceHandler) processUniEnableStateFailedEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "DeviceStateUpdate upon re-enable failure. ", log.Fields{
		"OperStatus": voltha.OperStatus_FAILED, "device-id": dh.DeviceID})
	if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
		DeviceId:   dh.DeviceID,
		ConnStatus: connectStatusINVALID, //use some dummy value to prevent modification of the ConnStatus
		OperStatus: voltha.OperStatus_FAILED,
	}); err != nil {
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.DeviceID, "error": err})
	}
}

func (dh *deviceHandler) processOmciAniConfigDoneEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	if devEvent == cmn.OmciAniConfigDone {
		logger.Debugw(ctx, "OmciAniConfigDone event received", log.Fields{"device-id": dh.DeviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.getDeviceReason() != cmn.DrTechProfileConfigDownloadSuccess {
			// which may be the case from some previous activity even on this UNI Port (but also other UNI ports)
			_ = dh.ReasonUpdate(ctx, cmn.DrTechProfileConfigDownloadSuccess, !dh.IsReconciling() || dh.IsReconcilingReasonUpdate())
		}
		if dh.IsReconciling() {
			// during reconciling with OMCI configuration in TT multi-UNI scenario, OmciAniConfigDone is reached several times
			// therefore it must be ensured that reconciling of flow config is only started on the first pass of this code position
			dh.mutexReconcilingFirstPassFlag.Lock()
			if dh.reconcilingFirstPass {
				logger.Info(ctx, "reconciling - OmciAniConfigDone first pass, start flow processing", log.Fields{"device-id": dh.DeviceID})
				dh.reconcilingFirstPass = false
				go dh.ReconcileDeviceFlowConfig(ctx)
			}
			dh.mutexReconcilingFirstPassFlag.Unlock()
		}
	} else { // should be the OmciAniResourceRemoved block
		logger.Debugw(ctx, "OmciAniResourceRemoved event received", log.Fields{"device-id": dh.DeviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.getDeviceReason() != cmn.DrTechProfileConfigDeleteSuccess {
			// which may be the case from some previous activity even on this ONU port (but also other UNI ports)
			_ = dh.ReasonUpdate(ctx, cmn.DrTechProfileConfigDeleteSuccess, true)
		}
	}
}

func (dh *deviceHandler) processOmciVlanFilterDoneEvent(ctx context.Context, aDevEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "OmciVlanFilterDone event received",
		log.Fields{"device-id": dh.DeviceID, "event": aDevEvent})
	// attention: the device reason update is done based on ONU-UNI-Port related activity
	//  - which may cause some inconsistency

	if aDevEvent == cmn.OmciVlanFilterAddDone || aDevEvent == cmn.OmciVlanFilterAddDoneNoKvStore {
		if dh.getDeviceReason() != cmn.DrOmciFlowsPushed {
			// which may be the case from some previous activity on another UNI Port of the ONU
			// or even some previous flow add activity on the same port
			_ = dh.ReasonUpdate(ctx, cmn.DrOmciFlowsPushed, !dh.IsReconciling() || dh.IsReconcilingReasonUpdate())
		}
	} else {
		if dh.getDeviceReason() != cmn.DrOmciFlowsDeleted {
			//not relevant for reconcile
			_ = dh.ReasonUpdate(ctx, cmn.DrOmciFlowsDeleted, true)
		}
	}

	if aDevEvent == cmn.OmciVlanFilterAddDone || aDevEvent == cmn.OmciVlanFilterRemDone {
		//events that request KvStore write
		if err := dh.StorePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
				log.Fields{"device-id": dh.DeviceID, "err": err})
		}
	} else {
		logger.Debugw(ctx, "OmciVlanFilter*Done* - write to KvStore not requested",
			log.Fields{"device-id": dh.DeviceID})
	}
}

// DeviceProcStatusUpdate evaluates possible processing events and initiates according next activities
func (dh *deviceHandler) DeviceProcStatusUpdate(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	switch devEvent {
	case cmn.MibDatabaseSync:
		{
			dh.processMibDatabaseSyncEvent(ctx, devEvent)
		}
	case cmn.UniLockStateDone:
		{
			dh.processUniLockStateDoneEvent(ctx, devEvent)
		}
	case cmn.MibDownloadDone:
		{
			dh.processMibDownloadDoneEvent(ctx, devEvent)
		}
	case cmn.UniUnlockStateDone:
		{
			dh.processUniUnlockStateDoneEvent(ctx, devEvent)
		}
	case cmn.UniEnableStateDone:
		{
			dh.processUniEnableStateDoneEvent(ctx, devEvent)
		}
	case cmn.UniEnableStateFailed:
		{
			dh.processUniEnableStateFailedEvent(ctx, devEvent)
		}
	case cmn.UniDisableStateDone:
		{
			dh.processUniDisableStateDoneEvent(ctx, devEvent)
		}
	case cmn.OmciAniConfigDone, cmn.OmciAniResourceRemoved:
		{
			dh.processOmciAniConfigDoneEvent(ctx, devEvent)
		}
	case cmn.OmciVlanFilterAddDone, cmn.OmciVlanFilterAddDoneNoKvStore, cmn.OmciVlanFilterRemDone, cmn.OmciVlanFilterRemDoneNoKvStore:
		{
			dh.processOmciVlanFilterDoneEvent(ctx, devEvent)
		}
	default:
		{
			logger.Debugw(ctx, "unhandled-device-event", log.Fields{"device-id": dh.DeviceID, "event": devEvent})
		}
	} //switch
}

func (dh *deviceHandler) addUniPort(ctx context.Context, aUniInstNo uint16, aUniID uint8, aPortType cmn.UniPortType) {
	// parameters are IntfId, OnuId, uniId
	uniNo := platform.MkUniPortNum(ctx, dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(),
		uint32(aUniID))
	if _, present := dh.uniEntityMap[uniNo]; present {
		logger.Warnw(ctx, "OnuUniPort-add: Port already exists", log.Fields{"device-id": dh.DeviceID, "for InstanceId": aUniInstNo})
	} else {
		//with arguments aUniID, a_portNo, aPortType
		pUniPort := cmn.NewOnuUniPort(ctx, aUniID, uniNo, aUniInstNo, aPortType)
		if pUniPort == nil {
			logger.Warnw(ctx, "OnuUniPort-add: Could not create Port", log.Fields{"device-id": dh.DeviceID, "for InstanceId": aUniInstNo})
		} else {
			//store UniPort with the System-PortNumber key
			dh.uniEntityMap[uniNo] = pUniPort
			if !dh.IsReconciling() {
				// create announce the UniPort to the core as VOLTHA Port object
				if err := pUniPort.CreateVolthaPort(ctx, dh); err == nil {
					logger.Infow(ctx, "OnuUniPort-added", log.Fields{"device-id": dh.DeviceID, "for PortNo": uniNo})
				} //error logging already within UniPort method
			} else {
				logger.Warn(ctx, "reconciling - OnuUniPort already added", log.Fields{"for PortNo": uniNo, "device-id": dh.DeviceID})
			}
		}
	}
}

func (dh *deviceHandler) AddAllUniPorts(ctx context.Context) {
	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	uniCnt := uint8(0) //UNI Port limit: see MaxUnisPerOnu (by now 16) (OMCI supports max 255 p.b.)
	if pptpInstKeys := pDevEntry.GetOnuDB().GetSortedInstKeys(
		ctx, me.PhysicalPathTerminationPointEthernetUniClassID); len(pptpInstKeys) > 0 {
		for _, mgmtEntityID := range pptpInstKeys {
			logger.Debugw(ctx, "Add PPTPEthUni port for MIB-stored instance:", log.Fields{
				"device-id": dh.DeviceID, "PPTPEthUni EntityID": mgmtEntityID})
			dh.addUniPort(ctx, mgmtEntityID, uniCnt, cmn.UniPPTP)
			uniCnt++
		}
	} else {
		logger.Debugw(ctx, "No PPTP instances found", log.Fields{"device-id": dh.DeviceID})
	}
	if veipInstKeys := pDevEntry.GetOnuDB().GetSortedInstKeys(
		ctx, me.VirtualEthernetInterfacePointClassID); len(veipInstKeys) > 0 {
		for _, mgmtEntityID := range veipInstKeys {
			logger.Debugw(ctx, "Add VEIP for MIB-stored instance:", log.Fields{
				"device-id": dh.DeviceID, "VEIP EntityID": mgmtEntityID})
			dh.addUniPort(ctx, mgmtEntityID, uniCnt, cmn.UniVEIP)
			uniCnt++
		}
	} else {
		logger.Debugw(ctx, "No VEIP instances found", log.Fields{"device-id": dh.DeviceID})
	}
	if potsInstKeys := pDevEntry.GetOnuDB().GetSortedInstKeys(
		ctx, me.PhysicalPathTerminationPointPotsUniClassID); len(potsInstKeys) > 0 {
		for _, mgmtEntityID := range potsInstKeys {
			logger.Debugw(ctx, "Add PPTP Pots UNI for MIB-stored instance:", log.Fields{
				"device-id": dh.DeviceID, "PPTP Pots UNI EntityID": mgmtEntityID})
			dh.addUniPort(ctx, mgmtEntityID, uniCnt, cmn.UniPPTPPots)
			uniCnt++
		}
	} else {
		logger.Debugw(ctx, "No PPTP Pots UNI instances found", log.Fields{"device-id": dh.DeviceID})
	}
	if uniCnt == 0 {
		logger.Warnw(ctx, "No UniG instances found", log.Fields{"device-id": dh.DeviceID})
		return
	}

	//Note: For the moment is is not required to include the (newly added) POTS ports into the range
	//  of flowCall or reconcile channels. But some sort of flow and reconcile processing might get necessary
	//  also for the POTS ports, so we include them already for future usage - should anyway do no great harm
	dh.flowCbChan = make([]chan FlowCb, uniCnt)
	dh.stopFlowMonitoringRoutine = make([]chan bool, uniCnt)
	dh.isFlowMonitoringRoutineActive = make([]bool, uniCnt)
	//chUniVlanConfigReconcilingDone needs to have the capacity of all UniPorts as flow reconcile may run parallel for all of them
	dh.chUniVlanConfigReconcilingDone = make(chan uint16, uniCnt)
	for i := 0; i < int(uniCnt); i++ {
		dh.flowCbChan[i] = make(chan FlowCb, dh.pOpenOnuAc.config.MaxConcurrentFlowsPerUni)
		dh.stopFlowMonitoringRoutine[i] = make(chan bool, 1)
	}
}

// EnableUniPortStateUpdate enables UniPortState and update core port state accordingly
func (dh *deviceHandler) EnableUniPortStateUpdate(ctx context.Context) {
	//  py code was updated 2003xx to activate the real ONU UNI ports per OMCI (VEIP or PPTP)
	//    but towards core only the first port active state is signaled
	//    with following remark:
	//       # TODO: for now only support the first UNI given no requirement for multiple uni yet. Also needed to reduce flow
	//       #  load on the core

	// lock_ports(false) as done in py code here is shifted to separate call from device event processing

	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.UniID)&dh.pOpenOnuAc.config.UniPortMask == (1 << uniPort.UniID) {
			logger.Infow(ctx, "OnuUniPort-forced-OperState-ACTIVE", log.Fields{"for PortNo": uniNo, "device-id": dh.DeviceID})
			uniPort.SetOperState(vc.OperStatus_ACTIVE)
			if !dh.IsReconciling() {
				//maybe also use getter functions on uniPort - perhaps later ...
				go func(port *cmn.OnuUniPort) {
					if err := dh.updatePortStateInCore(ctx, &ca.PortState{
						DeviceId:   dh.DeviceID,
						PortType:   voltha.Port_ETHERNET_UNI,
						PortNo:     port.PortNo,
						OperStatus: port.OperState,
					}); err != nil {
						logger.Errorw(ctx, "port-state-update-failed", log.Fields{"error": err, "port-no": uniPort.PortNo, "device-id": dh.DeviceID})
					}
				}(uniPort)
			} else {
				logger.Debug(ctx, "reconciling - don't notify core about PortStateUpdate", log.Fields{"device-id": dh.DeviceID})
			}
		}
	}
}

// Disable UniPortState and update core port state accordingly
func (dh *deviceHandler) DisableUniPortStateUpdate(ctx context.Context) {
	// compare EnableUniPortStateUpdate() above
	//   -> use current restriction to operate only on first UNI port as inherited from actual Py code
	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer

		if (1<<uniPort.UniID)&dh.pOpenOnuAc.config.UniPortMask == (1 << uniPort.UniID) {
			logger.Infow(ctx, "OnuUniPort-forced-OperState-UNKNOWN", log.Fields{"for PortNo": uniNo, "device-id": dh.DeviceID})
			uniPort.SetOperState(vc.OperStatus_UNKNOWN)
			if !dh.IsReconciling() {
				//maybe also use getter functions on uniPort - perhaps later ...
				go func(port *cmn.OnuUniPort) {
					if err := dh.updatePortStateInCore(ctx, &ca.PortState{
						DeviceId:   dh.DeviceID,
						PortType:   voltha.Port_ETHERNET_UNI,
						PortNo:     port.PortNo,
						OperStatus: port.OperState,
					}); err != nil {
						logger.Errorw(ctx, "port-state-update-failed", log.Fields{"error": err, "port-no": uniPort.PortNo, "device-id": dh.DeviceID})
					}
				}(uniPort)
			} else {
				logger.Debug(ctx, "reconciling - don't notify core about PortStateUpdate", log.Fields{"device-id": dh.DeviceID})
			}

		}
	}
}

// ONU_Active/Inactive announcement on system KAFKA bus
// tried to re-use procedure of oltUpDownIndication from openolt_eventmgr.go with used values from Py code
func (dh *deviceHandler) sendOnuOperStateEvent(ctx context.Context, aOperState vc.OperStatus_Types, aDeviceID string, raisedTs int64) {
	var de voltha.DeviceEvent
	eventContext := make(map[string]string)
	//Populating event context
	//  assume giving ParentId in GetDevice twice really gives the ParentDevice (there is no GetParentDevice()...)
	parentDevice, err := dh.getDeviceFromCore(ctx, dh.parentID)
	if err != nil || parentDevice == nil {
		logger.Errorw(ctx, "Failed to fetch parent device for OnuEvent",
			log.Fields{"device-id": dh.DeviceID, "parentID": dh.parentID, "err": err})
		return //TODO with VOL-3045: rw-core is unresponsive: report error and/or perform self-initiated onu-reset?
	}
	oltSerialNumber := parentDevice.SerialNumber

	eventContext["pon-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.IntfId), 10)
	eventContext["onu-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.OnuId), 10)
	eventContext["serial-number"] = dh.device.SerialNumber
	eventContext["olt-serial-number"] = oltSerialNumber
	eventContext["device-id"] = aDeviceID
	eventContext["registration-id"] = aDeviceID //py: string(device_id)??
	eventContext["num-of-unis"] = strconv.Itoa(len(dh.uniEntityMap))
	if deviceEntry := dh.GetOnuDeviceEntry(ctx, false); deviceEntry != nil {
		deviceEntry.MutexPersOnuConfig.RLock()
		eventContext["vendor-id"] = deviceEntry.SOnuPersistentData.PersVendorID
		eventContext["model"] = deviceEntry.SOnuPersistentData.PersVersion
		eventContext["equipment-id"] = deviceEntry.SOnuPersistentData.PersEquipmentID
		deviceEntry.MutexPersOnuConfig.RUnlock()
		eventContext["software-version"] = deviceEntry.GetActiveImageVersion(ctx)
		eventContext["inactive-software-version"] = deviceEntry.GetInactiveImageVersion(ctx)
		logger.Debugw(ctx, "prepare ONU_ACTIVATED event",
			log.Fields{"device-id": aDeviceID, "EventContext": eventContext})
	} else {
		logger.Errorw(ctx, "Failed to fetch device-entry. ONU_ACTIVATED event is not sent",
			log.Fields{"device-id": aDeviceID})
		return
	}

	/* Populating device event body */
	de.Context = eventContext
	de.ResourceId = aDeviceID
	if aOperState == voltha.OperStatus_ACTIVE {
		de.DeviceEventName = fmt.Sprintf("%s_%s", cOnuActivatedEvent, "RAISE_EVENT")
		de.Description = fmt.Sprintf("%s Event - %s - %s",
			cEventObjectType, cOnuActivatedEvent, "Raised")
	} else {
		de.DeviceEventName = fmt.Sprintf("%s_%s", cOnuActivatedEvent, "CLEAR_EVENT")
		de.Description = fmt.Sprintf("%s Event - %s - %s",
			cEventObjectType, cOnuActivatedEvent, "Cleared")
	}
	/* Send event to KAFKA */
	if err := dh.EventProxy.SendDeviceEventWithKey(ctx, &de, equipment, pon, raisedTs, aDeviceID); err != nil {
		logger.Warnw(ctx, "could not send ONU_ACTIVATED event",
			log.Fields{"device-id": aDeviceID, "error": err})
	}
	logger.Infow(ctx, "ctx, ONU_ACTIVATED event sent to KAFKA",
		log.Fields{"device-id": aDeviceID, "with-EventName": de.DeviceEventName})
}

// createUniLockFsm initializes and runs the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) createUniLockFsm(ctx context.Context, aAdminState bool, devEvent cmn.OnuDeviceEvent) {
	chLSFsm := make(chan cmn.Message, 2)
	var sFsmName string
	if aAdminState {
		logger.Debugw(ctx, "createLockStateFSM", log.Fields{"device-id": dh.DeviceID})
		sFsmName = "LockStateFSM"
	} else {
		logger.Debugw(ctx, "createUnlockStateFSM", log.Fields{"device-id": dh.DeviceID})
		sFsmName = "UnLockStateFSM"
	}

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.DeviceID})
		return
	}
	pLSFsm := uniprt.NewLockStateFsm(ctx, aAdminState, devEvent, sFsmName, dh, pDevEntry, chLSFsm)
	if pLSFsm != nil {
		if aAdminState {
			dh.pLockStateFsm = pLSFsm
		} else {
			dh.pUnlockStateFsm = pLSFsm
		}
		dh.runUniLockFsm(ctx, aAdminState)
	} else {
		logger.Errorw(ctx, "LockStateFSM could not be created - abort!!", log.Fields{"device-id": dh.DeviceID})
	}
}

// runUniLockFsm starts the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) runUniLockFsm(ctx context.Context, aAdminState bool) {
	/*  Uni Port lock/unlock procedure -
	 ***** should run via 'adminDone' state and generate the argument requested event *****
	 */
	var pLSStatemachine *fsm.FSM
	if aAdminState {
		pLSStatemachine = dh.pLockStateFsm.PAdaptFsm.PFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pUnlockStateFsm != nil) &&
			(dh.pUnlockStateFsm.PAdaptFsm.PFsm.Current() != uniprt.UniStDisabled) {
			_ = dh.pUnlockStateFsm.PAdaptFsm.PFsm.Event(uniprt.UniEvReset)
		}
	} else {
		pLSStatemachine = dh.pUnlockStateFsm.PAdaptFsm.PFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pLockStateFsm != nil) &&
			(dh.pLockStateFsm.PAdaptFsm.PFsm.Current() != uniprt.UniStDisabled) {
			_ = dh.pLockStateFsm.PAdaptFsm.PFsm.Event(uniprt.UniEvReset)
		}
	}
	if pLSStatemachine != nil {
		if pLSStatemachine.Is(uniprt.UniStDisabled) {
			if err := pLSStatemachine.Event(uniprt.UniEvStart); err != nil {
				logger.Warnw(ctx, "LockStateFSM: can't start", log.Fields{"device-id": dh.DeviceID, "err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				/***** LockStateFSM started */
				logger.Debugw(ctx, "LockStateFSM started", log.Fields{
					"state": pLSStatemachine.Current(), "device-id": dh.DeviceID})
			}
		} else {
			logger.Warnw(ctx, "wrong state of LockStateFSM - want: disabled", log.Fields{
				"have": pLSStatemachine.Current(), "device-id": dh.DeviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
	} else {
		logger.Errorw(ctx, "LockStateFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": dh.DeviceID})
		// maybe try a FSM reset and then again ... - TODO!!!
	}
}

// createOnuUpgradeFsm initializes and runs the Onu Software upgrade FSM
// precondition: lockUpgradeFsm is already locked from caller of this function
//
//nolint:unparam
func (dh *deviceHandler) createOnuUpgradeFsm(ctx context.Context, apDevEntry *mib.OnuDeviceEntry, aDevEvent cmn.OnuDeviceEvent) error {
	chUpgradeFsm := make(chan cmn.Message, 2)
	var sFsmName = "OnuSwUpgradeFSM"
	logger.Debugw(ctx, "create OnuSwUpgradeFSM", log.Fields{"device-id": dh.DeviceID})
	if apDevEntry.PDevOmciCC == nil {
		logger.Errorw(ctx, "no valid OnuDevice or omciCC - abort", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid omciCC - abort for device-id: %s", dh.device.Id)
	}
	dh.pOnuUpradeFsm = swupg.NewOnuUpgradeFsm(ctx, dh, apDevEntry, apDevEntry.GetOnuDB(), aDevEvent,
		sFsmName, chUpgradeFsm)
	if dh.pOnuUpradeFsm != nil {
		pUpgradeStatemachine := dh.pOnuUpradeFsm.PAdaptFsm.PFsm
		if pUpgradeStatemachine != nil {
			if pUpgradeStatemachine.Is(swupg.UpgradeStDisabled) {
				if err := pUpgradeStatemachine.Event(swupg.UpgradeEvStart); err != nil {
					logger.Errorw(ctx, "OnuSwUpgradeFSM: can't start", log.Fields{"device-id": dh.DeviceID, "err": err})
					// maybe try a FSM reset and then again ... - TODO!!!
					return fmt.Errorf("OnuSwUpgradeFSM could not be started for device-id: %s", dh.device.Id)
				}
				/***** Upgrade FSM started */
				//reset the last stored upgrade states (which anyway should be don't care as long as the newly created FSM exists)
				(*dh.pLastUpgradeImageState).DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
				(*dh.pLastUpgradeImageState).Reason = voltha.ImageState_NO_ERROR
				(*dh.pLastUpgradeImageState).ImageState = voltha.ImageState_IMAGE_UNKNOWN
				logger.Debugw(ctx, "OnuSwUpgradeFSM started", log.Fields{
					"state": pUpgradeStatemachine.Current(), "device-id": dh.DeviceID})
			} else {
				logger.Errorw(ctx, "wrong state of OnuSwUpgradeFSM to start - want: disabled", log.Fields{
					"have": pUpgradeStatemachine.Current(), "device-id": dh.DeviceID})
				// maybe try a FSM reset and then again ... - TODO!!!
				return fmt.Errorf("OnuSwUpgradeFSM could not be started for device-id: %s, wrong internal state", dh.device.Id)
			}
		} else {
			logger.Errorw(ctx, "OnuSwUpgradeFSM internal FSM invalid - cannot be executed!!", log.Fields{"device-id": dh.DeviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
			return fmt.Errorf("OnuSwUpgradeFSM internal FSM could not be created for device-id: %s", dh.device.Id)
		}
	} else {
		logger.Errorw(ctx, "OnuSwUpgradeFSM could not be created  - abort", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("OnuSwUpgradeFSM could not be created - abort for device-id: %s", dh.device.Id)
	}
	return nil
}

// RemoveOnuUpgradeFsm clears the Onu Software upgrade FSM
func (dh *deviceHandler) RemoveOnuUpgradeFsm(ctx context.Context, apImageState *voltha.ImageState) {
	logger.Debugw(ctx, "remove OnuSwUpgradeFSM StateMachine", log.Fields{
		"device-id": dh.DeviceID})
	dh.lockUpgradeFsm.Lock()
	dh.pOnuUpradeFsm = nil     //resource clearing is left to garbage collector
	dh.upgradeCanceled = false //cancelation done
	dh.pLastUpgradeImageState = apImageState
	dh.lockUpgradeFsm.Unlock()
	//signal upgradeFsm removed using non-blocking channel send
	select {
	case dh.upgradeFsmChan <- struct{}{}:
	default:
		logger.Debugw(ctx, "removed-UpgradeFsm signal not send on upgradeFsmChan (no receiver)", log.Fields{
			"device-id": dh.DeviceID})
	}
}

// checkOnOnuImageCommit verifies if the ONU is in some upgrade state that allows for image commit and if tries to commit
func (dh *deviceHandler) checkOnOnuImageCommit(ctx context.Context) {
	pDevEntry := dh.GetOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting checkOnOnuImageCommit", log.Fields{"device-id": dh.DeviceID})
		return
	}

	dh.lockUpgradeFsm.RLock()
	//lockUpgradeFsm must be release before cancellation as this may implicitly request RemoveOnuUpgradeFsm()
	if dh.pOnuUpradeFsm != nil {
		if dh.upgradeCanceled { //avoid starting some new action in case it is already doing the cancelation
			dh.lockUpgradeFsm.RUnlock()
			logger.Errorw(ctx, "Some upgrade procedure still runs cancelation - abort", log.Fields{"device-id": dh.DeviceID})
			return
		}
		pUpgradeStatemachine := dh.pOnuUpradeFsm.PAdaptFsm.PFsm
		if pUpgradeStatemachine != nil {
			// commit is only processed in case out upgrade FSM indicates the according state (for automatic commit)
			//  (some manual forced commit could do without)
			UpgradeState := pUpgradeStatemachine.Current()
			if (UpgradeState == swupg.UpgradeStWaitForCommit) ||
				(UpgradeState == swupg.UpgradeStRequestingActivate) {
				// also include UpgradeStRequestingActivate as it may be left in case the ActivateResponse just got lost
				// here no need to update the upgrade image state to activated as the state will be immediately be set to committing
				if pDevEntry.IsImageToBeCommitted(ctx, dh.pOnuUpradeFsm.InactiveImageMeID) {
					activeImageID, errImg := pDevEntry.GetActiveImageMeID(ctx)
					if errImg != nil {
						dh.lockUpgradeFsm.RUnlock()
						logger.Errorw(ctx, "OnuSwUpgradeFSM abort - could not get active image after reboot",
							log.Fields{"device-id": dh.DeviceID})
						if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
							dh.upgradeCanceled = true
							dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_ONU_STATE) //complete abort
						}
						return
					}
					dh.lockUpgradeFsm.RUnlock()
					if activeImageID == dh.pOnuUpradeFsm.InactiveImageMeID {
						if (UpgradeState == swupg.UpgradeStRequestingActivate) && !dh.pOnuUpradeFsm.GetCommitFlag(ctx) {
							// if FSM was waiting on activateResponse, new image is active, but FSM shall not commit, then:
							if err := pUpgradeStatemachine.Event(swupg.UpgradeEvActivationDone); err != nil {
								logger.Errorw(ctx, "OnuSwUpgradeFSM: can't call activate-done event",
									log.Fields{"device-id": dh.DeviceID, "err": err})
								return
							}
							logger.Debugw(ctx, "OnuSwUpgradeFSM activate-done after reboot", log.Fields{
								"state": UpgradeState, "device-id": dh.DeviceID})
						} else {
							//FSM in waitForCommit or (UpgradeStRequestingActivate [lost ActivateResp] and commit allowed)
							if err := pUpgradeStatemachine.Event(swupg.UpgradeEvCommitSw); err != nil {
								logger.Errorw(ctx, "OnuSwUpgradeFSM: can't call commit event",
									log.Fields{"device-id": dh.DeviceID, "err": err})
								return
							}
							logger.Debugw(ctx, "OnuSwUpgradeFSM commit image requested", log.Fields{
								"state": UpgradeState, "device-id": dh.DeviceID})
						}
					} else {
						logger.Errorw(ctx, "OnuSwUpgradeFSM waiting to commit/on ActivateResponse, but load did not start with expected image Id",
							log.Fields{"device-id": dh.DeviceID})
						if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
							dh.upgradeCanceled = true
							dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_ONU_STATE) //complete abort
						}
					}
					return
				}
				dh.lockUpgradeFsm.RUnlock()
				logger.Errorw(ctx, "OnuSwUpgradeFSM waiting to commit, but nothing to commit on ONU - abort upgrade",
					log.Fields{"device-id": dh.DeviceID})
				if !dh.upgradeCanceled { //avoid double cancelation in case it is already doing the cancelation
					dh.upgradeCanceled = true
					dh.pOnuUpradeFsm.CancelProcessing(ctx, true, voltha.ImageState_CANCELLED_ON_ONU_STATE) //complete abort
				}
				return
			}
			//upgrade FSM is active but not waiting for commit: maybe because commit flag is not set
			// upgrade FSM is to be informed if the current active image is the one that was used in upgrade for the download
			if activeImageID, err := pDevEntry.GetActiveImageMeID(ctx); err == nil {
				if dh.pOnuUpradeFsm.InactiveImageMeID == activeImageID {
					logger.Debugw(ctx, "OnuSwUpgradeFSM image state set to activated", log.Fields{
						"state": pUpgradeStatemachine.Current(), "device-id": dh.DeviceID})
					dh.pOnuUpradeFsm.SetImageStateActive(ctx)
				}
			}
		}
	} else {
		logger.Debugw(ctx, "no ONU image to be committed", log.Fields{"device-id": dh.DeviceID})
	}
	dh.lockUpgradeFsm.RUnlock()
}

// SetBackend provides a DB backend for the specified path on the existing KV client
func (dh *deviceHandler) SetBackend(ctx context.Context, aBasePathKvStore string) *db.Backend {

	logger.Debugw(ctx, "SetKVStoreBackend", log.Fields{"IpTarget": dh.pOpenOnuAc.KVStoreAddress,
		"BasePathKvStore": aBasePathKvStore, "device-id": dh.DeviceID})
	// kvbackend := db.NewBackend(ctx, dh.pOpenOnuAc.KVStoreType, dh.pOpenOnuAc.KVStoreAddress, dh.pOpenOnuAc.KVStoreTimeout, aBasePathKvStore)
	kvbackend := &db.Backend{
		Client:    dh.pOpenOnuAc.kvClient,
		StoreType: dh.pOpenOnuAc.KVStoreType,
		/* address config update acc. to [VOL-2736] */
		Address:    dh.pOpenOnuAc.KVStoreAddress,
		Timeout:    dh.pOpenOnuAc.KVStoreTimeout,
		PathPrefix: aBasePathKvStore}

	return kvbackend
}

//nolint:unparam
func (dh *deviceHandler) getFlowOfbFields(ctx context.Context, apFlowItem *of.OfpFlowStats, loMatchVlan *uint16,
	loMatchPcp *uint8, loIPProto *uint32) {

	for _, field := range flow.GetOfbFields(apFlowItem) {
		switch field.Type {
		case of.OxmOfbFieldTypes_OFPXMT_OFB_ETH_TYPE:
			{
				logger.Debugw(ctx, "flow type EthType", log.Fields{"device-id": dh.DeviceID,
					"EthType": strconv.FormatInt(int64(field.GetEthType()), 16)})
			}
		/* TT related temporary workaround - should not be needed anymore
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IP_PROTO:
			{
				*loIPProto = field.GetIpProto()
				logger.Debugw("flow type IpProto", log.Fields{"device-id": dh.DeviceID,
					"IpProto": strconv.FormatInt(int64(*loIPProto), 16)})
				if *loIPProto == 2 {
					// some workaround for TT workflow at proto == 2 (IGMP trap) -> ignore the flow
					// avoids installing invalid EVTOCD rule
					logger.Debugw("flow type IpProto 2: TT workaround: ignore flow",
						log.Fields{"device-id": dh.DeviceID})
					return
				}
			}
		*/
		case of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_VID:
			{
				*loMatchVlan = uint16(field.GetVlanVid())
				loMatchVlanMask := uint16(field.GetVlanVidMask())
				if !(*loMatchVlan == uint16(of.OfpVlanId_OFPVID_PRESENT) &&
					loMatchVlanMask == uint16(of.OfpVlanId_OFPVID_PRESENT)) {
					*loMatchVlan = *loMatchVlan & 0xFFF // not transparent: copy only ID bits
				}
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"VID": strconv.FormatInt(int64(*loMatchVlan), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP:
			{
				*loMatchPcp = uint8(field.GetVlanPcp())
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"PCP": loMatchPcp})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_DST:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"UDP-DST": strconv.FormatInt(int64(field.GetUdpDst()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_SRC:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"UDP-SRC": strconv.FormatInt(int64(field.GetUdpSrc()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_DST:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"IPv4-DST": field.GetIpv4Dst()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_SRC:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"IPv4-SRC": field.GetIpv4Src()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_METADATA:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.DeviceID,
					"Metadata": field.GetTableMetadata()})
			}
			/*
				default:
					{
						//all other entires ignored
					}
			*/
		}
	} //for all OfbFields
}

func (dh *deviceHandler) getFlowActions(ctx context.Context, apFlowItem *of.OfpFlowStats, loSetPcp *uint8, loSetVlan *uint16) {
	for _, action := range flow.GetActions(apFlowItem) {
		switch action.Type {
		/* not used:
		case of.OfpActionType_OFPAT_OUTPUT:
			{
				logger.Debugw("flow action type", log.Fields{"device-id": dh.DeviceID,
					"Output": action.GetOutput()})
			}
		*/
		case of.OfpActionType_OFPAT_PUSH_VLAN:
			{
				logger.Debugw(ctx, "flow action type", log.Fields{"device-id": dh.DeviceID,
					"PushEthType": strconv.FormatInt(int64(action.GetPush().Ethertype), 16)})
			}
		case of.OfpActionType_OFPAT_SET_FIELD:
			{
				pActionSetField := action.GetSetField()
				if pActionSetField.Field.OxmClass != of.OfpOxmClass_OFPXMC_OPENFLOW_BASIC {
					logger.Warnw(ctx, "flow action SetField invalid OxmClass (ignored)", log.Fields{"device-id": dh.DeviceID,
						"OxcmClass": pActionSetField.Field.OxmClass})
				}
				if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_VID {
					*loSetVlan = uint16(pActionSetField.Field.GetOfbField().GetVlanVid())
					logger.Debugw(ctx, "flow Set VLAN from SetField action", log.Fields{"device-id": dh.DeviceID,
						"SetVlan": strconv.FormatInt(int64(*loSetVlan), 16)})
				} else if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP {
					*loSetPcp = uint8(pActionSetField.Field.GetOfbField().GetVlanPcp())
					logger.Debugw(ctx, "flow Set PCP from SetField action", log.Fields{"device-id": dh.DeviceID,
						"SetPcp": *loSetPcp})
				} else {
					logger.Warnw(ctx, "flow action SetField invalid FieldType", log.Fields{"device-id": dh.DeviceID,
						"Type": pActionSetField.Field.GetOfbField().Type})
				}
			}
			/*
				default:
					{
						//all other entires ignored
					}
			*/
		}
	} //for all Actions
}

// addFlowItemToUniPort parses the actual flow item to add it to the UniPort
func (dh *deviceHandler) addFlowItemToUniPort(ctx context.Context, apFlowItem *of.OfpFlowStats, apUniPort *cmn.OnuUniPort,
	apFlowMetaData *of.FlowMetadata, respChan *chan error) {
	var loSetVlan uint16 = uint16(of.OfpVlanId_OFPVID_NONE)      //noValidEntry
	var loMatchVlan uint16 = uint16(of.OfpVlanId_OFPVID_PRESENT) //reserved VLANID entry
	var loSetPcp uint8
	var loMatchPcp uint8 = 8 // could the const 'cPrioDoNotFilter' be used from omci_vlan_config.go ?
	var loIPProto uint32
	/* the TechProfileId is part of the flow Metadata - compare also comment within
	 * OLT-Adapter:openolt_flowmgr.go
	 *     Metadata 8 bytes:
	 *	   Most Significant 2 Bytes = Inner VLAN
	 *	   Next 2 Bytes = Tech Profile ID(TPID)
	 *	   Least Significant 4 Bytes = Port ID
	 *     Flow Metadata carries Tech-Profile (TP) ID and is mandatory in all
	 *     subscriber related flows.
	 */

	metadata := flow.GetMetadataFromWriteMetadataAction(ctx, apFlowItem)
	if metadata == 0 {
		logger.Debugw(ctx, "flow-add invalid metadata - abort",
			log.Fields{"device-id": dh.DeviceID})
		*respChan <- fmt.Errorf("flow-add invalid metadata: %s", dh.DeviceID)
	}
	loTpID := uint8(flow.GetTechProfileIDFromWriteMetaData(ctx, metadata))
	loCookie := apFlowItem.GetCookie()
	loCookieSlice := []uint64{loCookie}
	loInnerCvlan := flow.GetInnerTagFromWriteMetaData(ctx, metadata)
	logger.Debugw(ctx, "flow-add base indications", log.Fields{"device-id": dh.DeviceID,
		"TechProf-Id": loTpID, "cookie": loCookie, "innerCvlan": loInnerCvlan})

	dh.getFlowOfbFields(ctx, apFlowItem, &loMatchVlan, &loMatchPcp, &loIPProto)
	/* TT related temporary workaround - should not be needed anymore
	if loIPProto == 2 {
		// some workaround for TT workflow at proto == 2 (IGMP trap) -> ignore the flow
		// avoids installing invalid EVTOCD rule
		logger.Debugw("flow-add type IpProto 2: TT workaround: ignore flow",
			log.Fields{"device-id": dh.DeviceID})
		return nil
	}
	*/
	dh.getFlowActions(ctx, apFlowItem, &loSetPcp, &loSetVlan)

	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) && (loMatchPcp == 8) &&
		loInnerCvlan == uint16(of.OfpVlanId_OFPVID_NONE) {
		logger.Errorw(ctx, "flow-add aborted - SetVlanId undefined, but MatchVid set", log.Fields{
			"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo,
			"set_vid":   strconv.FormatInt(int64(loSetVlan), 16),
			"match_vid": strconv.FormatInt(int64(loMatchVlan), 16)})
		//TODO!!: Use DeviceId within the error response to rwCore
		//  likewise also in other error response cases to calling components as requested in [VOL-3458]
		*respChan <- fmt.Errorf("flow-add Set/Match VlanId inconsistent: %s", dh.DeviceID)
	}
	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan == uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Debugw(ctx, "flow-add vlan-any/copy", log.Fields{"device-id": dh.DeviceID})
		loSetVlan = loMatchVlan //both 'transparent' (copy any)
	} else if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) &&
		loInnerCvlan != uint16(of.OfpVlanId_OFPVID_NONE) {
		loSetVlan = loMatchVlan
		logger.Debugw(ctx, "flow-add, double tagged case, set setvlan to matchvlan ", log.Fields{"device-id": dh.DeviceID, "loSetVlan": loSetVlan, "loMatchVlan": loMatchVlan})
	} else {
		//looks like OMCI value 4097 (copyFromOuter - for Uni double tagged) is not supported here
		if loSetVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) {
			// not set to transparent
			loSetVlan &= 0x0FFF //mask VID bits as prerequisite for vlanConfigFsm
		}
		logger.Debugw(ctx, "flow-add vlan-set", log.Fields{"device-id": dh.DeviceID})
	}

	var meter *of.OfpMeterConfig
	if apFlowMetaData != nil {
		meter = apFlowMetaData.Meters[0]
	}
	//mutex protection as the update_flow rpc maybe running concurrently for different flows, perhaps also activities
	//  must be set including the execution of createVlanFilterFsm() to avoid unintended creation of FSM's
	//  when different rules are requested concurrently for the same uni
	//  (also vlan persistency data does not support multiple FSM's on the same UNI correctly!)
	dh.lockVlanAdd.Lock()     //prevent multiple add activities to start in parallel
	dh.lockVlanConfig.RLock() //read protection on UniVlanConfigFsmMap (removeFlowItemFromUniPort)
	logger.Debugw(ctx, "flow-add got lock", log.Fields{"device-id": dh.DeviceID, "tpID": loTpID, "uniID": apUniPort.UniID})
	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.UniID]; exist {
		//SetUniFlowParams() may block on some rule that is suspended-to-add
		//  in order to allow for according flow removal lockVlanConfig may only be used with RLock here
		// Also the error is returned to caller via response channel
		_ = dh.UniVlanConfigFsmMap[apUniPort.UniID].SetUniFlowParams(ctx, loTpID, loCookieSlice,
			loMatchVlan, loMatchPcp, loSetVlan, loSetPcp, loInnerCvlan, false, meter, respChan)
		dh.lockVlanConfig.RUnlock()
		dh.lockVlanAdd.Unlock() //re-admit new Add-flow-processing
		return
	}
	dh.lockVlanConfig.RUnlock()
	dh.lockVlanConfig.Lock() //createVlanFilterFsm should always be a non-blocking operation and requires r+w lock
	err := dh.createVlanFilterFsm(ctx, apUniPort, loTpID, loCookieSlice,
		loMatchVlan, loMatchPcp, loSetVlan, loSetPcp, loInnerCvlan, cmn.OmciVlanFilterAddDone, false, meter, respChan)
	dh.lockVlanConfig.Unlock()
	dh.lockVlanAdd.Unlock() //re-admit new Add-flow-processing
	if err != nil {
		*respChan <- err
	}
}

// removeFlowItemFromUniPort parses the actual flow item to remove it from the UniPort
func (dh *deviceHandler) removeFlowItemFromUniPort(ctx context.Context, apFlowItem *of.OfpFlowStats, apUniPort *cmn.OnuUniPort, respChan *chan error) {
	//optimization and assumption: the flow cookie uniquely identifies the flow and with that the internal rule
	//hence only the cookie is used here to find the relevant flow and possibly remove the rule
	//no extra check is done on the rule parameters
	//accordingly the removal is done only once - for the first found flow with that cookie, even though
	// at flow creation is not assured, that the same cookie is not configured for different flows - just assumed
	//additionally it is assumed here, that removal can only be done for one cookie per flow in a sequence (different
	// from addFlow - where at reconcilement multiple cookies per flow ) can be configured in one sequence)
	// - some possible 'delete-all' sequence would have to be implemented separately (where the cookies are don't care anyway)
	loCookie := apFlowItem.GetCookie()
	logger.Debugw(ctx, "flow-remove base indications", log.Fields{"device-id": dh.DeviceID, "cookie": loCookie})

	/* TT related temporary workaround - should not be needed anymore
	for _, field := range flow.GetOfbFields(apFlowItem) {
		if field.Type == of.OxmOfbFieldTypes_OFPXMT_OFB_IP_PROTO {
			loIPProto := field.GetIpProto()
			logger.Debugw(ctx, "flow type IpProto", log.Fields{"device-id": dh.DeviceID,
				"IpProto": strconv.FormatInt(int64(loIPProto), 16)})
			if loIPProto == 2 {
				// some workaround for TT workflow on proto == 2 (IGMP trap) -> the flow was not added, no need to remove
				logger.Debugw(ctx, "flow-remove type IpProto 2: TT workaround: ignore flow",
					log.Fields{"device-id": dh.DeviceID})
				return nil
			}
		}
	} //for all OfbFields
	*/

	//mutex protection as the update_flow rpc maybe running concurrently for different flows, perhaps also activities
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	logger.Debugw(ctx, "flow-remove got RLock", log.Fields{"device-id": dh.DeviceID, "uniID": apUniPort.UniID})
	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.UniID]; exist {
		_ = dh.UniVlanConfigFsmMap[apUniPort.UniID].RemoveUniFlowParams(ctx, loCookie, respChan)
		return
	}
	logger.Debugw(ctx, "flow-remove called, but no flow is configured (no VlanConfigFsm, flow already removed) ",
		log.Fields{"device-id": dh.DeviceID})
	//but as we regard the flow as not existing = removed we respond just ok
	// and treat the reason accordingly (which in the normal removal procedure is initiated by the FSM)
	// Push response on the response channel
	if respChan != nil {
		// Do it in a non blocking fashion, so that in case the flow handler routine has shutdown for any reason, we do not block here
		select {
		case *respChan <- nil:
			logger.Debugw(ctx, "submitted-response-for-flow", log.Fields{"device-id": dh.DeviceID, "err": nil})
		default:
		}
	}
	go dh.DeviceProcStatusUpdate(ctx, cmn.OmciVlanFilterRemDone)
}

// createVlanFilterFsm initializes and runs the VlanFilter FSM to transfer OMCI related VLAN config
// if this function is called from possibly concurrent processes it must be mutex-protected from the caller!
// precondition: dh.lockVlanConfig is locked by the caller!
func (dh *deviceHandler) createVlanFilterFsm(ctx context.Context, apUniPort *cmn.OnuUniPort, aTpID uint8, aCookieSlice []uint64,
	aMatchVlan uint16, aMatchPcp uint8, aSetVlan uint16, aSetPcp uint8, innerCvlan uint16, aDevEvent cmn.OnuDeviceEvent, lastFlowToReconcile bool, aMeter *of.OfpMeterConfig, respChan *chan error) error {
	chVlanFilterFsm := make(chan cmn.Message, 2)

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice for device-id %x - aborting", dh.DeviceID)
	}

	if dh.pDeviceStateFsm.Current() == devStDown {
		logger.Warnw(ctx, "UniVlanConfigFsm : aborting, device state down", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("device state down for device-id %x - aborting", dh.DeviceID)
	}

	pVlanFilterFsm := avcfg.NewUniVlanConfigFsm(ctx, dh, pDevEntry, pDevEntry.PDevOmciCC, apUniPort, dh.pOnuTP,
		pDevEntry.GetOnuDB(), aTpID, aDevEvent, "UniVlanConfigFsm", chVlanFilterFsm,
		dh.pOpenOnuAc.AcceptIncrementalEvto, aCookieSlice, aMatchVlan, aMatchPcp, aSetVlan, aSetPcp, innerCvlan, lastFlowToReconcile, aMeter, respChan)
	if pVlanFilterFsm != nil {
		//dh.lockVlanConfig is locked (by caller) throughout the state transition to 'starting'
		// to prevent unintended (ignored) events to be sent there (from parallel processing)
		dh.UniVlanConfigFsmMap[apUniPort.UniID] = pVlanFilterFsm
		pVlanFilterStatemachine := pVlanFilterFsm.PAdaptFsm.PFsm
		if pVlanFilterStatemachine != nil {
			if pVlanFilterStatemachine.Is(avcfg.VlanStDisabled) {
				if err := pVlanFilterStatemachine.Event(avcfg.VlanEvStart); err != nil {
					logger.Warnw(ctx, "UniVlanConfigFsm: can't start",
						log.Fields{"device-id": dh.DeviceID, "err": err})
					return fmt.Errorf("can't start UniVlanConfigFsm for device-id %x", dh.DeviceID)
				}
				/***** UniVlanConfigFsm started */
				logger.Debugw(ctx, "UniVlanConfigFsm started", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID,
					"UniPort": apUniPort.PortNo})
			} else {
				logger.Warnw(ctx, "wrong state of UniVlanConfigFsm - want: disabled", log.Fields{
					"have": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID})
				return fmt.Errorf("uniVlanConfigFsm not in expected disabled state for device-id %x", dh.DeviceID)
			}
		} else {
			logger.Errorw(ctx, "UniVlanConfigFsm StateMachine invalid - cannot be executed!!", log.Fields{
				"device-id": dh.DeviceID})
			return fmt.Errorf("uniVlanConfigFsm invalid for device-id %x", dh.DeviceID)
		}
	} else {
		logger.Errorw(ctx, "UniVlanConfigFsm could not be created - abort!!", log.Fields{
			"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo})
		return fmt.Errorf("uniVlanConfigFsm could not be created for device-id %x", dh.DeviceID)
	}
	return nil
}

// VerifyVlanConfigRequest checks on existence of a given uniPort
// and starts verification of flow config based on that
func (dh *deviceHandler) VerifyVlanConfigRequest(ctx context.Context, aUniID uint8, aTpID uint8) {
	//ensure that the given uniID is available (configured) in the UniPort class (used for OMCI entities)
	var pCurrentUniPort *cmn.OnuUniPort
	for _, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if uniPort.UniID == uint8(aUniID) {
			pCurrentUniPort = uniPort
			break //found - end search loop
		}
	}
	if pCurrentUniPort == nil {
		logger.Debugw(ctx, "VerifyVlanConfig aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": dh.DeviceID, "uni-id": aUniID})
		return
	}
	dh.VerifyUniVlanConfigRequest(ctx, pCurrentUniPort, aTpID)
}

// VerifyUniVlanConfigRequest checks on existence of flow configuration and starts it accordingly
func (dh *deviceHandler) VerifyUniVlanConfigRequest(ctx context.Context, apUniPort *cmn.OnuUniPort, aTpID uint8) {
	//TODO!! verify and start pending flow configuration
	//some pending config request my exist in case the UniVlanConfig FSM was already started - with internal data -
	//but execution was set to 'on hold' as first the TechProfile config had to be applied
	logger.Info(ctx, "Verifying UniVlanConfig Request", log.Fields{"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo, "techprofile-id": aTpID})
	dh.lockVlanConfig.RLock()
	if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[apUniPort.UniID]; exist {
		dh.lockVlanConfig.RUnlock()
		//VlanFilterFsm exists and was already started (assumed to wait for TechProfile execution here)
		pVlanFilterStatemachine := pVlanFilterFsm.PAdaptFsm.PFsm
		if pVlanFilterStatemachine != nil {
			//if this was an event of the TP processing that was waited for in the VlanFilterFsm
			if pVlanFilterFsm.GetWaitingTpID(ctx) == aTpID {
				if pVlanFilterStatemachine.Is(avcfg.VlanStWaitingTechProf) {
					if err := pVlanFilterStatemachine.Event(avcfg.VlanEvContinueConfig); err != nil {
						logger.Warnw(ctx, "UniVlanConfigFsm: can't continue processing", log.Fields{"err": err,
							"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo})
					} else {
						/***** UniVlanConfigFsm continued */
						logger.Debugw(ctx, "UniVlanConfigFsm continued", log.Fields{
							"state": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID,
							"UniPort": apUniPort.PortNo})
					}
				} else if pVlanFilterStatemachine.Is(avcfg.VlanStIncrFlowWaitTP) {
					if err := pVlanFilterStatemachine.Event(avcfg.VlanEvIncrFlowConfig); err != nil {
						logger.Warnw(ctx, "UniVlanConfigFsm: can't continue processing", log.Fields{"err": err,
							"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo})
					} else {
						/***** UniVlanConfigFsm continued */
						logger.Debugw(ctx, "UniVlanConfigFsm continued with incremental flow", log.Fields{
							"state": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID,
							"UniPort": apUniPort.PortNo})
					}
				} else {
					logger.Debugw(ctx, "no state of UniVlanConfigFsm to be continued", log.Fields{
						"have": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID,
						"UniPort": apUniPort.PortNo})
				}
			} else {
				logger.Debugw(ctx, "TechProfile Ready event for TpId that was not waited for in the VlanConfigFsm - continue waiting", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID,
					"UniPort": apUniPort.PortNo, "techprofile-id (done)": aTpID})
			}
		} else {
			logger.Debugw(ctx, "UniVlanConfigFsm StateMachine does not exist, no flow processing", log.Fields{
				"device-id": dh.DeviceID, "UniPort": apUniPort.PortNo})
		}
	} else {
		dh.lockVlanConfig.RUnlock()
	}
}

// handleAniConfigFSMFailure handles the failure of the ANI config FSM by resetting the VLAN filter FSM
func (dh *deviceHandler) HandleAniConfigFSMFailure(ctx context.Context, uniID uint8) {
	dh.lockVlanConfig.Lock()
	defer dh.lockVlanConfig.Unlock()

	if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[uniID]; exist {
		pVlanFilterStatemachine := pVlanFilterFsm.PAdaptFsm.PFsm
		if pVlanFilterStatemachine != nil {
			if err := pVlanFilterStatemachine.Event(avcfg.VlanEvReset); err != nil {
				logger.Warnw(ctx, "Failed to reset UniVlanConfigFsm", log.Fields{
					"err": err, "device-id": dh.DeviceID, "UniPort": uniID, "FsmState": pVlanFilterStatemachine.Current(),
				})
			} else {
				logger.Infow(ctx, "Successfully reset UniVlanConfigFsm", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.DeviceID, "UniPort": uniID,
				})
			}
		} else {
			logger.Debugw(ctx, "UniVlanConfigFsm StateMachine does not exist, no reset performed", log.Fields{
				"device-id": dh.DeviceID, "UniPort": uniID,
			})
		}
	} else {
		logger.Debugw(ctx, "No UniVlanConfigFsm found for the UNI ID", log.Fields{
			"device-id": dh.DeviceID, "UniPort": uniID,
		})
	}
}

// RemoveVlanFilterFsm deletes the stored pointer to the VlanConfigFsm
// intention is to provide this method to be called from VlanConfigFsm itself, when resources (and methods!) are cleaned up
func (dh *deviceHandler) RemoveVlanFilterFsm(ctx context.Context, apUniPort *cmn.OnuUniPort) {
	logger.Debugw(ctx, "remove UniVlanConfigFsm StateMachine", log.Fields{
		"device-id": dh.DeviceID, "uniPort": apUniPort.PortNo})
	//save to do, even if entry dows not exist
	dh.lockVlanConfig.Lock()
	delete(dh.UniVlanConfigFsmMap, apUniPort.UniID)
	dh.lockVlanConfig.Unlock()
}

// startWritingOnuDataToKvStore initiates the KVStore write of ONU persistent data
func (dh *deviceHandler) startWritingOnuDataToKvStore(ctx context.Context, aPDevEntry *mib.OnuDeviceEntry) error {
	dh.mutexKvStoreContext.Lock()         //this write routine may (could) be called with the same context,
	defer dh.mutexKvStoreContext.Unlock() //this write routine may (could) be called with the same context,
	// obviously then parallel processing on the cancel must be avoided
	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel() // Ensure cancel is called to release resources

	err := aPDevEntry.UpdateOnuKvStore(log.WithSpanFromContext(dctx, ctx))
	if err != nil {
		logger.Errorw(ctx, "UpdateOnuKvStore-failed", log.Fields{"device-id": dh.DeviceID})
		return err
	}
	return nil
}

// StorePersUniFlowConfig updates local storage of OnuUniFlowConfig and writes it into kv-store afterwards to have it
// available for potential reconcilement
func (dh *deviceHandler) StorePersUniFlowConfig(ctx context.Context, aUniID uint8,
	aUniVlanFlowParams *[]cmn.UniVlanFlowParams, aWriteToKvStore bool) error {

	if dh.IsReconciling() {
		logger.Info(ctx, "reconciling - don't store persistent UniFlowConfig", log.Fields{"device-id": dh.DeviceID})
		return nil
	}
	logger.Debugw(ctx, "Store or clear persistent UniFlowConfig", log.Fields{"device-id": dh.DeviceID})

	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}
	pDevEntry.UpdateOnuUniFlowConfig(aUniID, aUniVlanFlowParams)

	if aWriteToKvStore {
		return dh.startWritingOnuDataToKvStore(ctx, pDevEntry)
	}
	return nil
}

func (dh *deviceHandler) waitForCompletion(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, aCallerIdent string) {
	defer cancel() //ensure termination of context (may be pro forma)
	wg.Wait()
	logger.Debugw(ctx, "WaitGroup processing completed", log.Fields{
		"device-id": dh.DeviceID, "called from": aCallerIdent})
}

// ReasonUpdate set the internally store device reason and if requested in notifyCore updates this state in the core
//
//	(renamed from previous deviceReasonUpdate to avoid confusing with the core function DeviceReasonUpdate)
func (dh *deviceHandler) ReasonUpdate(ctx context.Context, deviceReason uint8, notifyCore bool) error {
	// acquire the deviceReason semaphore throughout this function including the possible update processing in core
	// in order to avoid reversion of the state sequence within core in case of quasi-parallel calls (eg. in multi UNI processing)
	dh.mutexDeviceReason.Lock()
	defer dh.mutexDeviceReason.Unlock()
	if notifyCore {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing at calling position
		if err := dh.updateDeviceReasonInCore(ctx, &ca.DeviceReason{
			DeviceId: dh.DeviceID,
			Reason:   cmn.DeviceReasonMap[deviceReason],
		}); err != nil {
			logger.Errorf(ctx, "updating reason in core failed for: %s",
				log.Fields{"device-id": dh.DeviceID, "error": err}, cmn.DeviceReasonMap[deviceReason])
			return err
		}
	} else {
		logger.Debugf(ctx, "update reason in core not requested: %s - device-id: %s", cmn.DeviceReasonMap[deviceReason], dh.DeviceID)
	}
	dh.deviceReason = deviceReason
	logger.Infof(ctx, "reason update done for: %s - device-id: %s - with core update: %v",
		cmn.DeviceReasonMap[deviceReason], dh.DeviceID, notifyCore)
	return nil
}

func (dh *deviceHandler) StorePersistentData(ctx context.Context) error {
	pDevEntry := dh.GetOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Warnw(ctx, "No valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.DeviceID)
	}
	return dh.startWritingOnuDataToKvStore(ctx, pDevEntry)
}

// getUniPortMEEntityID takes uniPortNo as the input and returns the Entity ID corresponding to this UNI-G ME Instance
// nolint: unused
func (dh *deviceHandler) getUniPortMEEntityID(uniPortNo uint32) (uint16, error) {
	dh.lockDevice.RLock()
	defer dh.lockDevice.RUnlock()
	if uniPort, ok := dh.uniEntityMap[uniPortNo]; ok {
		return uniPort.EntityID, nil
	}
	return 0, errors.New("error-fetching-uni-port")
}

// updatePmConfig updates the pm metrics config.
func (dh *deviceHandler) updatePmConfig(ctx context.Context, pmConfigs *voltha.PmConfigs) error {
	var errorsList []error
	logger.Infow(ctx, "update-pm-config", log.Fields{"device-id": dh.device.Id, "new-pm-configs": pmConfigs, "old-pm-config": dh.pmConfigs})

	errorsList = append(dh.handleGlobalPmConfigUpdates(ctx, pmConfigs), errorsList...)
	errorsList = append(dh.handleGroupPmConfigUpdates(ctx, pmConfigs), errorsList...)
	errorsList = append(dh.handleStandalonePmConfigUpdates(ctx, pmConfigs), errorsList...)

	// Note that if more than one pm config field is updated in a given call, it is possible that partial pm config is handled
	// successfully.
	// TODO: Although it is possible to revert to old config in case of partial failure, the code becomes quite complex. Needs more investigation
	// Is it possible the rw-core reverts to old config on partial failure but adapter retains a partial new config?
	if len(errorsList) > 0 {
		logger.Errorw(ctx, "one-or-more-pm-config-failed", log.Fields{"device-id": dh.DeviceID, "pmConfig": dh.pmConfigs})
		return fmt.Errorf("errors-handling-one-or-more-pm-config, errors:%v", errorsList)
	}
	logger.Infow(ctx, "pm-config-updated", log.Fields{"device-id": dh.DeviceID, "pmConfig": dh.pmConfigs})
	return nil
}

func (dh *deviceHandler) handleGlobalPmConfigUpdates(ctx context.Context, pmConfigs *voltha.PmConfigs) []error {
	var err error
	var errorsList []error
	logger.Infow(ctx, "handling-global-pm-config-params - start", log.Fields{"device-id": dh.device.Id})

	if pmConfigs.DefaultFreq != dh.pmConfigs.DefaultFreq {
		if err = dh.pOnuMetricsMgr.UpdateDefaultFrequency(ctx, pmConfigs); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	logger.Infow(ctx, "handling-global-pm-config-params - done", log.Fields{"device-id": dh.device.Id})

	return errorsList
}

func (dh *deviceHandler) handleGroupPmConfigUpdates(ctx context.Context, pmConfigs *voltha.PmConfigs) []error {
	var err error
	var errorsList []error
	logger.Debugw(ctx, "handling-group-pm-config-params - start", log.Fields{"device-id": dh.device.Id})
	// Check if group metric related config is updated
	for _, v := range pmConfigs.Groups {
		dh.pOnuMetricsMgr.OnuMetricsManagerLock.RLock()
		m, ok := dh.pOnuMetricsMgr.GroupMetricMap[v.GroupName]
		dh.pOnuMetricsMgr.OnuMetricsManagerLock.RUnlock()

		if ok && m.Frequency != v.GroupFreq {
			if err = dh.pOnuMetricsMgr.UpdateGroupFreq(ctx, v.GroupName, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		if ok && m.Enabled != v.Enabled {
			if err = dh.pOnuMetricsMgr.UpdateGroupSupport(ctx, v.GroupName, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
	}
	logger.Debugw(ctx, "handling-group-pm-config-params - done", log.Fields{"device-id": dh.device.Id})
	return errorsList
}

func (dh *deviceHandler) handleStandalonePmConfigUpdates(ctx context.Context, pmConfigs *voltha.PmConfigs) []error {
	var err error
	var errorsList []error
	logger.Debugw(ctx, "handling-individual-pm-config-params - start", log.Fields{"device-id": dh.device.Id})
	// Check if standalone metric related config is updated
	for _, v := range pmConfigs.Metrics {
		dh.pOnuMetricsMgr.OnuMetricsManagerLock.RLock()
		m, ok := dh.pOnuMetricsMgr.StandaloneMetricMap[v.Name]
		dh.pOnuMetricsMgr.OnuMetricsManagerLock.RUnlock()

		if ok && m.Frequency != v.SampleFreq {
			if err = dh.pOnuMetricsMgr.UpdateMetricFreq(ctx, v.Name, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		if ok && m.Enabled != v.Enabled {
			if err = dh.pOnuMetricsMgr.UpdateMetricSupport(ctx, v.Name, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
	}
	logger.Debugw(ctx, "handling-individual-pm-config-params - done", log.Fields{"device-id": dh.device.Id})
	return errorsList
}

// nolint: gocyclo
func (dh *deviceHandler) StartCollector(ctx context.Context, waitForOmciProcessor *sync.WaitGroup) {
	logger.Debugw(ctx, "startingCollector", log.Fields{"device-id": dh.device.Id})

	// Start routine to process OMCI GET Responses
	go dh.pOnuMetricsMgr.ProcessOmciMessages(ctx, waitForOmciProcessor)
	// Create Extended Frame PM ME
	go dh.pOnuMetricsMgr.CreateEthernetFrameExtendedPMME(ctx)
	// Initialize the next metric collection time.
	// Normally done when the onu_metrics_manager is initialized the first time, but needed again later when ONU is
	// reset like onu rebooted.
	dh.pOnuMetricsMgr.InitializeMetricCollectionTime(ctx)
	dh.setCollectorIsRunning(true)
	statsCollectionticker := time.NewTicker((pmmgr.FrequencyGranularity) * time.Second)
	defer statsCollectionticker.Stop()
	for {

		select {
		case <-dh.stopCollector:
			logger.Debugw(ctx, "stopping-collector-for-onu", log.Fields{"device-id": dh.device.Id})
			// Stop the L2 PM FSM
			go func() {
				if dh.pOnuMetricsMgr.PAdaptFsm != nil && dh.pOnuMetricsMgr.PAdaptFsm.PFsm != nil {
					if err := dh.pOnuMetricsMgr.PAdaptFsm.PFsm.Event(pmmgr.L2PmEventStop); err != nil {
						logger.Errorw(ctx, "error calling event", log.Fields{"device-id": dh.DeviceID, "err": err})
					}
				} else {
					logger.Errorw(ctx, "metrics manager fsm not initialized", log.Fields{"device-id": dh.DeviceID})
				}
			}()
			if dh.pOnuMetricsMgr.GetOmciProcessingStatus() {
				dh.pOnuMetricsMgr.StopProcessingOmciResponses <- true // Stop the OMCI GET response processing routine
			}
			if dh.pOnuMetricsMgr.GetTickGenerationStatus() {
				dh.pOnuMetricsMgr.StopTicks <- true
			}

			return
		case <-statsCollectionticker.C: // Check every FrequencyGranularity to see if it is time for collecting metrics
			if !dh.pmConfigs.FreqOverride { // If FreqOverride is false, then NextGlobalMetricCollectionTime applies
				// If the current time is eqaul to or greater than the NextGlobalMetricCollectionTime, collect the group and standalone metrics
				if time.Now().Equal(dh.pOnuMetricsMgr.NextGlobalMetricCollectionTime) || time.Now().After(dh.pOnuMetricsMgr.NextGlobalMetricCollectionTime) {
					go dh.pOnuMetricsMgr.CollectAllGroupAndStandaloneMetrics(ctx)
					// Update the next metric collection time.
					prevInternal := dh.pOnuMetricsMgr.NextGlobalMetricCollectionTime
					dh.pOnuMetricsMgr.NextGlobalMetricCollectionTime = prevInternal.Add(time.Duration(dh.pmConfigs.DefaultFreq) * time.Second)
				}
			} else {
				if dh.pmConfigs.Grouped { // metrics are managed as a group
					// parse through the group and standalone metrics to see it is time to collect their metrics
					dh.pOnuMetricsMgr.OnuMetricsManagerLock.RLock() // Rlock as we are reading GroupMetricMap and StandaloneMetricMap

					for n, g := range dh.pOnuMetricsMgr.GroupMetricMap {
						// If the group is enabled AND (current time is equal to OR after NextCollectionInterval, collect the group metric)
						// Since the L2 PM counters are collected in a separate FSM, we should avoid those counters in the check.
						if g.Enabled && !g.IsL2PMCounter && (time.Now().Equal(g.NextCollectionInterval) || time.Now().After(g.NextCollectionInterval)) {
							go dh.pOnuMetricsMgr.CollectGroupMetric(ctx, n)
						}
					}
					for n, m := range dh.pOnuMetricsMgr.StandaloneMetricMap {
						// If the standalone is enabled AND (current time is equal to OR after NextCollectionInterval, collect the metric)
						if m.Enabled && (time.Now().Equal(m.NextCollectionInterval) || time.Now().After(m.NextCollectionInterval)) {
							go dh.pOnuMetricsMgr.CollectStandaloneMetric(ctx, n)
						}
					}
					dh.pOnuMetricsMgr.OnuMetricsManagerLock.RUnlock()

					// parse through the group and update the next metric collection time
					dh.pOnuMetricsMgr.OnuMetricsManagerLock.Lock() // Lock as we are writing the next metric collection time
					for _, g := range dh.pOnuMetricsMgr.GroupMetricMap {
						// If group enabled, and the NextCollectionInterval is old (before or equal to current time), update the next collection time stamp
						// Since the L2 PM counters are collected and managed in a separate FSM, we should avoid those counters in the check.
						if g.Enabled && !g.IsL2PMCounter && (g.NextCollectionInterval.Before(time.Now()) || g.NextCollectionInterval.Equal(time.Now())) {
							prevInternal := g.NextCollectionInterval
							g.NextCollectionInterval = prevInternal.Add(time.Duration(g.Frequency) * time.Second)
						}
					}
					// parse through the standalone metrics and update the next metric collection time
					for _, m := range dh.pOnuMetricsMgr.StandaloneMetricMap {
						// If standalone metrics enabled, and the NextCollectionInterval is old (before or equal to current time), update the next collection time stamp
						if m.Enabled && (m.NextCollectionInterval.Before(time.Now()) || m.NextCollectionInterval.Equal(time.Now())) {
							prevInternal := m.NextCollectionInterval
							m.NextCollectionInterval = prevInternal.Add(time.Duration(m.Frequency) * time.Second)
						}
					}
					dh.pOnuMetricsMgr.OnuMetricsManagerLock.Unlock()
				} /* else { // metrics are not managed as a group
					// TODO: We currently do not have standalone metrics. When available, add code here to fetch the metrca.
				} */
			}
		}
	}
}

//nolint:unparam
func (dh *deviceHandler) setOnuOffloadStats(ctx context.Context, config *extension.AppOffloadOnuConfig) *extension.SingleSetValueResponse {

	singleValResp := extension.SingleSetValueResponse{
		Response: &extension.SetValueResponse{
			Status: extension.SetValueResponse_OK,
		},
	}

	return &singleValResp
}

func (dh *deviceHandler) GetUniPortStatus(ctx context.Context, uniInfo *extension.GetOnuUniInfoRequest) *extension.SingleGetValueResponse {

	portStatus := uniprt.NewUniPortStatus(dh, dh.pOnuOmciDevice.PDevOmciCC)
	return portStatus.GetUniPortStatus(ctx, uniInfo.UniIndex)
}

func (dh *deviceHandler) getOnuOMCICounters(ctx context.Context, onuInfo *extension.GetOmciEthernetFrameExtendedPmRequest) *extension.SingleGetValueResponse {
	if dh.pOnuMetricsMgr == nil {
		return &extension.SingleGetValueResponse{
			Response: &extension.GetValueResponse{
				Status:    extension.GetValueResponse_ERROR,
				ErrReason: extension.GetValueResponse_INTERNAL_ERROR,
			},
		}
	}
	resp := dh.pOnuMetricsMgr.CollectEthernetFrameExtendedPMCounters(ctx, onuInfo)
	return resp
}

func (dh *deviceHandler) getOnuOMCIStats(ctx context.Context) (*extension.SingleGetValueResponse, error) {

	var err error
	var pDevOmciCC *cmn.OmciCC
	if dh.pOnuOmciDevice == nil {
		logger.Errorw(ctx, "No valid DeviceEntry", log.Fields{"device-id": dh.DeviceID})
		err = fmt.Errorf("no-valid-DeviceEntry-%s", dh.DeviceID)
	} else {
		pDevOmciCC = dh.pOnuOmciDevice.GetDevOmciCC()
		if pDevOmciCC == nil {
			logger.Errorw(ctx, "No valid DeviceOmciCCEntry", log.Fields{"device-id": dh.DeviceID})
			err = fmt.Errorf("no-valid-DeviceOmciCCEntry-%s", dh.DeviceID)
		}
	}
	if err != nil {
		return &extension.SingleGetValueResponse{
				Response: &extension.GetValueResponse{
					Status:    extension.GetValueResponse_ERROR,
					ErrReason: extension.GetValueResponse_INTERNAL_ERROR,
				},
			},
			err
	}
	return pDevOmciCC.GetOmciCounters(), nil
}

//nolint:unparam
func (dh *deviceHandler) isFsmInOmciIdleState(ctx context.Context, PFsm *fsm.FSM, wantedState string) bool {
	if PFsm == nil {
		return true //FSM not active - so there is no activity on omci
	}
	return PFsm.Current() == wantedState
}

func (dh *deviceHandler) isFsmInOmciIdleStateDefault(ctx context.Context, omciFsm cmn.UsedOmciConfigFsms, wantedState string) bool {
	var pAdapterFsm *cmn.AdapterFsm
	//note/TODO!!: might be that access to all these specific FSM pointers need a semaphore protection as well, cmp lockUpgradeFsm
	switch omciFsm {
	case cmn.CUploadFsm:
		{
			if dh.pOnuOmciDevice != nil {
				pAdapterFsm = dh.pOnuOmciDevice.PMibUploadFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cmn.CDownloadFsm:
		{
			if dh.pOnuOmciDevice != nil {
				pAdapterFsm = dh.pOnuOmciDevice.PMibDownloadFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cmn.CUniLockFsm:
		{
			if dh.pLockStateFsm != nil {
				pAdapterFsm = dh.pLockStateFsm.PAdaptFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cmn.CUniUnLockFsm:
		{
			if dh.pUnlockStateFsm != nil {
				pAdapterFsm = dh.pUnlockStateFsm.PAdaptFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cmn.CL2PmFsm:
		{
			if dh.pOnuMetricsMgr != nil {
				pAdapterFsm = dh.pOnuMetricsMgr.PAdaptFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cmn.COnuUpgradeFsm:
		{
			dh.lockUpgradeFsm.RLock()
			defer dh.lockUpgradeFsm.RUnlock()
			if dh.pOnuUpradeFsm != nil {
				pAdapterFsm = dh.pOnuUpradeFsm.PAdaptFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	default:
		{
			logger.Errorw(ctx, "invalid stateMachine selected for idle check", log.Fields{
				"device-id": dh.DeviceID, "selectedFsm number": omciFsm})
			return false //logical error in FSM check, do not not indicate 'idle' - we can't be sure
		}
	}
	if pAdapterFsm != nil && pAdapterFsm.PFsm != nil {
		return dh.isFsmInOmciIdleState(ctx, pAdapterFsm.PFsm, wantedState)
	}
	return true //FSM not active - so there is no activity on omci
}

func (dh *deviceHandler) isAniConfigFsmInOmciIdleState(ctx context.Context, omciFsm cmn.UsedOmciConfigFsms, idleState string) bool {
	for _, v := range dh.pOnuTP.PAniConfigFsm {
		if !dh.isFsmInOmciIdleState(ctx, v.PAdaptFsm.PFsm, idleState) {
			return false
		}
	}
	return true
}

//nolint:unparam
func (dh *deviceHandler) isUniVlanConfigFsmInOmciIdleState(ctx context.Context, omciFsm cmn.UsedOmciConfigFsms, idleState string) bool {
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	for _, v := range dh.UniVlanConfigFsmMap {
		if !dh.isFsmInOmciIdleState(ctx, v.PAdaptFsm.PFsm, idleState) {
			return false
		}
	}
	return true //FSM not active - so there is no activity on omci
}

//nolint:unparam
func (dh *deviceHandler) checkUserServiceExists(ctx context.Context) bool {
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	for _, v := range dh.UniVlanConfigFsmMap {
		if v.PAdaptFsm.PFsm != nil {
			if v.PAdaptFsm.PFsm.Is(avcfg.CVlanFsmConfiguredState) {
				return true //there is at least one VLAN FSM with some active configuration
			}
		}
	}
	return false //there is no VLAN FSM with some active configuration
}

func (dh *deviceHandler) CheckAuditStartCondition(ctx context.Context, callingFsm cmn.UsedOmciConfigFsms) bool {
	for fsmName, fsmStruct := range fsmOmciIdleStateFuncMap {
		if fsmName != callingFsm && !fsmStruct.omciIdleCheckFunc(dh, ctx, fsmName, fsmStruct.omciIdleState) {
			return false
		}
	}
	// a further check is done to identify, if at least some data traffic related configuration exists
	// so that a user of this ONU could be 'online' (otherwise it makes no sense to check the MDS [with the intention to keep the user service up])
	return dh.checkUserServiceExists(ctx)
}

func (dh *deviceHandler) PrepareReconcilingWithActiveAdapter(ctx context.Context) {
	logger.Info(ctx, "prepare to reconcile the ONU with adapter using persistency data", log.Fields{"device-id": dh.device.Id})
	if err := dh.resetFsms(ctx, false); err != nil {
		logger.Errorw(ctx, "reset of FSMs failed!", log.Fields{"device-id": dh.DeviceID, "error": err})
		// TODO: fatal error reset ONU, delete deviceHandler!
		return
	}
	dh.uniEntityMap = make(map[uint32]*cmn.OnuUniPort)
	dh.StartReconciling(ctx, false)
}

func (dh *deviceHandler) setCollectorIsRunning(flagValue bool) {
	dh.mutexCollectorFlag.Lock()
	dh.collectorIsRunning = flagValue
	dh.mutexCollectorFlag.Unlock()
}

func (dh *deviceHandler) GetCollectorIsRunning() bool {
	dh.mutexCollectorFlag.RLock()
	flagValue := dh.collectorIsRunning
	dh.mutexCollectorFlag.RUnlock()
	return flagValue
}

func (dh *deviceHandler) setAlarmManagerIsRunning(flagValue bool) {
	dh.mutextAlarmManagerFlag.Lock()
	dh.alarmManagerIsRunning = flagValue
	dh.mutextAlarmManagerFlag.Unlock()
}

func (dh *deviceHandler) GetAlarmManagerIsRunning(ctx context.Context) bool {
	dh.mutextAlarmManagerFlag.RLock()
	flagValue := dh.alarmManagerIsRunning
	logger.Debugw(ctx, "alarm-manager-is-running", log.Fields{"device-id": dh.device.Id, "flag": dh.alarmManagerIsRunning})
	dh.mutextAlarmManagerFlag.RUnlock()
	return flagValue
}

func (dh *deviceHandler) StartAlarmManager(ctx context.Context) {
	logger.Debugw(ctx, "startingAlarmManager", log.Fields{"device-id": dh.device.Id})

	// Start routine to process OMCI GET Responses
	go dh.pAlarmMgr.StartOMCIAlarmMessageProcessing(ctx)
	dh.setAlarmManagerIsRunning(true)
	if stop := <-dh.stopAlarmManager; stop {
		logger.Debugw(ctx, "stopping-alarm-manager-for-onu", log.Fields{"device-id": dh.device.Id})
		go func() {
			if dh.pAlarmMgr.AlarmSyncFsm != nil && dh.pAlarmMgr.AlarmSyncFsm.PFsm != nil {
				_ = dh.pAlarmMgr.AlarmSyncFsm.PFsm.Event(almgr.AsEvStop)
			}
		}()
		dh.pAlarmMgr.StopProcessingOmciMessages <- true // Stop the OMCI routines if any(This will stop the fsms also)
		dh.pAlarmMgr.StopAlarmAuditTimer <- struct{}{}
		logger.Debugw(ctx, "sent-all-stop-signals-to-alarm-manager", log.Fields{"device-id": dh.device.Id})
	}
}

func (dh *deviceHandler) setFlowMonitoringIsRunning(uniID uint8, flag bool) {
	dh.mutexFlowMonitoringRoutineFlag.Lock()
	defer dh.mutexFlowMonitoringRoutineFlag.Unlock()
	logger.Debugw(context.Background(), "set-flow-monitoring-routine", log.Fields{"device-id": dh.device.Id, "flag": flag})
	dh.isFlowMonitoringRoutineActive[uniID] = flag
}

func (dh *deviceHandler) GetFlowMonitoringIsRunning(uniID uint8) bool {
	dh.mutexFlowMonitoringRoutineFlag.RLock()
	defer dh.mutexFlowMonitoringRoutineFlag.RUnlock()
	logger.Debugw(context.Background(), "get-flow-monitoring-routine",
		log.Fields{"device-id": dh.device.Id, "isFlowMonitoringRoutineActive": dh.isFlowMonitoringRoutineActive})
	if len(dh.isFlowMonitoringRoutineActive) != 0 {
		return dh.isFlowMonitoringRoutineActive[uniID]
	}
	return false
}

func (dh *deviceHandler) StartReconciling(ctx context.Context, skipOnuConfig bool) {
	logger.Info(ctx, "start reconciling", log.Fields{"skipOnuConfig": skipOnuConfig, "device-id": dh.DeviceID})

	connectStatus := voltha.ConnectStatus_UNREACHABLE
	operState := voltha.OperStatus_UNKNOWN

	if !dh.IsReconciling() {
		go func() {
			logger.Debugw(ctx, "wait for channel signal or timeout",
				log.Fields{"timeout": dh.reconcileExpiryComplete, "device-id": dh.DeviceID})
			select {
			case success := <-dh.chReconcilingFinished:
				logger.Info(ctx, "reconciling finished signal received",
					log.Fields{"device-id": dh.DeviceID, "dh.chReconcilingFinished": dh.chReconcilingFinished})
				// To guarantee that the case-branch below is completely processed before reconciling processing is continued,
				// dh.mutexReconcilingFlag is locked already here. Thereby it is ensured, that further reconciling processing is stopped
				// at next call of dh.IsReconciling() until dh.reconciling is set after informing core about finished reconciling below.
				// This change addresses a problem described in VOL-4533 where the flag dh.reconciling not yet reset causes the uni ports
				// not to be created in ONOS in function dh.addUniPort(), when reconciling was started in reason "starting-openomci".
				// TODO: Keeping the mutex beyond an RPC towards core seems justifiable, as the effects here are easily overseeable.
				// However, a later refactoring of the functionality remains unaffected.
				dh.mutexReconcilingFlag.Lock()
				if success {
					if onuDevEntry := dh.GetOnuDeviceEntry(ctx, true); onuDevEntry == nil {
						logger.Errorw(ctx, "No valid OnuDevice - aborting Core DeviceStateUpdate",
							log.Fields{"device-id": dh.DeviceID})
					} else {
						onuDevEntry.MutexPersOnuConfig.RLock()
						if onuDevEntry.SOnuPersistentData.PersOperState == "up" {
							connectStatus = voltha.ConnectStatus_REACHABLE
							if !onuDevEntry.SOnuPersistentData.PersUniDisableDone {
								if onuDevEntry.SOnuPersistentData.PersUniUnlockDone {
									operState = voltha.OperStatus_ACTIVE
								} else {
									operState = voltha.OperStatus_ACTIVATING
								}
							}
						} else if onuDevEntry.SOnuPersistentData.PersOperState == "down" ||
							onuDevEntry.SOnuPersistentData.PersOperState == "unknown" ||
							onuDevEntry.SOnuPersistentData.PersOperState == "" {
							operState = voltha.OperStatus_DISCOVERED
						}
						onuDevEntry.MutexPersOnuConfig.RUnlock()
						logger.Debugw(ctx, "Core DeviceStateUpdate",
							log.Fields{"device-id": dh.device.Id, "connectStatus": connectStatus, "operState": operState})
					}
					logger.Info(ctx, "reconciling has been finished in time",
						log.Fields{"device-id": dh.DeviceID})
					if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
						DeviceId:   dh.DeviceID,
						ConnStatus: connectStatus,
						OperStatus: operState,
					}); err != nil {
						logger.Errorw(ctx, "unable to update device state to core",
							log.Fields{"device-id": dh.DeviceID, "Err": err})
					}
				} else {
					logger.Errorw(ctx, "wait for reconciling aborted",
						log.Fields{"device-id": dh.DeviceID})

					if onuDevEntry := dh.GetOnuDeviceEntry(ctx, true); onuDevEntry == nil {
						logger.Errorw(ctx, "No valid OnuDevice",
							log.Fields{"device-id": dh.DeviceID})
					} else {
						onuDevEntry.MutexPersOnuConfig.RLock()
						if onuDevEntry.SOnuPersistentData.PersOperState == "up" {
							connectStatus = voltha.ConnectStatus_REACHABLE
						}
						onuDevEntry.MutexPersOnuConfig.RUnlock()
					}
					dh.deviceReconcileFailedUpdate(ctx, cmn.DrReconcileCanceled, connectStatus)
				}
			case <-time.After(dh.reconcileExpiryComplete):
				logger.Errorw(ctx, "timeout waiting for reconciling to be finished!",
					log.Fields{"device-id": dh.DeviceID})
				dh.mutexReconcilingFlag.Lock()

				if onuDevEntry := dh.GetOnuDeviceEntry(ctx, true); onuDevEntry == nil {
					logger.Errorw(ctx, "No valid OnuDevice",
						log.Fields{"device-id": dh.DeviceID})
				} else {
					onuDevEntry.MutexPersOnuConfig.RLock()
					if onuDevEntry.SOnuPersistentData.PersOperState == "up" {
						connectStatus = voltha.ConnectStatus_REACHABLE
					}
					onuDevEntry.MutexPersOnuConfig.RUnlock()
				}

				dh.deviceReconcileFailedUpdate(ctx, cmn.DrReconcileMaxTimeout, connectStatus)

			}
			dh.reconciling = cNoReconciling
			dh.mutexReconcilingFlag.Unlock()
			dh.SetReconcilingReasonUpdate(false)
			dh.SetReconcilingFirstPass(true)

			if onuDevEntry := dh.GetOnuDeviceEntry(ctx, true); onuDevEntry == nil {
				logger.Errorw(ctx, "No valid OnuDevice", log.Fields{"device-id": dh.DeviceID})
			} else {
				onuDevEntry.MutexReconciledTpInstances.Lock()
				onuDevEntry.ReconciledTpInstances = make(map[uint8]map[uint8]inter_adapter.TechProfileDownloadMessage)
				onuDevEntry.MutexReconciledTpInstances.Unlock()
			}
		}()
	}
	dh.mutexReconcilingFlag.Lock()
	if skipOnuConfig || dh.GetSkipOnuConfigEnabled() {
		dh.reconciling = cSkipOnuConfigReconciling
	} else {
		dh.reconciling = cOnuConfigReconciling
	}
	dh.mutexReconcilingFlag.Unlock()
}

func (dh *deviceHandler) stopReconciling(ctx context.Context, success bool, reconcileFlowResult uint16) {
	logger.Warn(ctx, "stop reconciling", log.Fields{"device-id": dh.DeviceID, "success": success})
	if dh.IsReconciling() {
		dh.sendChReconcileFinished(success)
		if reconcileFlowResult != cWaitReconcileFlowNoActivity {
			dh.SendChUniVlanConfigFinished(reconcileFlowResult)
		}
	} else {
		logger.Debugw(ctx, "nothing to stop - reconciling is not running", log.Fields{"device-id": dh.DeviceID})
	}
}

func (dh *deviceHandler) IsReconciling() bool {
	dh.mutexReconcilingFlag.RLock()
	defer dh.mutexReconcilingFlag.RUnlock()
	return dh.reconciling != cNoReconciling
}

func (dh *deviceHandler) IsSkipOnuConfigReconciling() bool {
	dh.mutexReconcilingFlag.RLock()
	defer dh.mutexReconcilingFlag.RUnlock()
	return dh.reconciling == cSkipOnuConfigReconciling
}

func (dh *deviceHandler) SetReconcilingFirstPass(value bool) {
	dh.mutexReconcilingFirstPassFlag.Lock()
	dh.reconcilingFirstPass = value
	dh.mutexReconcilingFirstPassFlag.Unlock()
}

func (dh *deviceHandler) SetReconcilingReasonUpdate(value bool) {
	dh.mutexReconcilingReasonUpdate.Lock()
	dh.reconcilingReasonUpdate = value
	dh.mutexReconcilingReasonUpdate.Unlock()
}

func (dh *deviceHandler) IsReconcilingReasonUpdate() bool {
	dh.mutexReconcilingReasonUpdate.RLock()
	defer dh.mutexReconcilingReasonUpdate.RUnlock()
	return dh.reconcilingReasonUpdate
}

func (dh *deviceHandler) getDeviceReason() uint8 {
	dh.mutexDeviceReason.RLock()
	value := dh.deviceReason
	dh.mutexDeviceReason.RUnlock()
	return value
}

func (dh *deviceHandler) GetDeviceReasonString() string {
	return cmn.DeviceReasonMap[dh.getDeviceReason()]
}

func (dh *deviceHandler) SetReadyForOmciConfig(flagValue bool) {
	dh.mutexReadyForOmciConfig.Lock()
	dh.readyForOmciConfig = flagValue
	dh.mutexReadyForOmciConfig.Unlock()
}
func (dh *deviceHandler) IsReadyForOmciConfig() bool {
	dh.mutexReadyForOmciConfig.RLock()
	flagValue := dh.readyForOmciConfig
	dh.mutexReadyForOmciConfig.RUnlock()
	return flagValue
}

func (dh *deviceHandler) deviceReconcileFailedUpdate(ctx context.Context, deviceReason uint8, connectStatus voltha.ConnectStatus_Types) {
	if err := dh.ReasonUpdate(ctx, deviceReason, true); err != nil {
		logger.Errorw(ctx, "unable to update device reason to core",
			log.Fields{"device-id": dh.DeviceID, "Err": err})
	}

	logger.Debugw(ctx, "Core DeviceStateUpdate",
		log.Fields{"device-id": dh.device.Id, "connectStatus": connectStatus, "operState": voltha.OperStatus_RECONCILING_FAILED})
	if err := dh.updateDeviceStateInCore(ctx, &ca.DeviceStateFilter{
		DeviceId:   dh.DeviceID,
		ConnStatus: connectStatus,
		OperStatus: voltha.OperStatus_RECONCILING_FAILED,
	}); err != nil {
		logger.Errorw(ctx, "unable to update device state to core",
			log.Fields{"device-id": dh.DeviceID, "Err": err})
	}
}

/*
Helper functions to communicate with Core
*/

func (dh *deviceHandler) getDeviceFromCore(ctx context.Context, deviceID string) (*voltha.Device, error) {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return nil, err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	logger.Debugw(subCtx, "get-device-from-core", log.Fields{"device-id": deviceID})
	return cClient.GetDevice(subCtx, &vc.ID{Id: deviceID})
}

func (dh *deviceHandler) updateDeviceStateInCore(ctx context.Context, deviceStateFilter *ca.DeviceStateFilter) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.DeviceStateUpdate(subCtx, deviceStateFilter)
	logger.Debugw(subCtx, "device-updated-in-core",
		log.Fields{"device-id": dh.device.Id, "device-state": deviceStateFilter, "error": err})
	return err
}

func (dh *deviceHandler) updatePMConfigInCore(ctx context.Context, pmConfigs *voltha.PmConfigs) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.DevicePMConfigUpdate(subCtx, pmConfigs)
	logger.Debugw(subCtx, "pmconfig-updated-in-core",
		log.Fields{"device-id": dh.device.Id, "pm-configs": pmConfigs, "error": err})
	return err
}

func (dh *deviceHandler) updateDeviceInCore(ctx context.Context, device *voltha.Device) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.DeviceUpdate(subCtx, device)
	logger.Debugw(subCtx, "device-updated-in-core", log.Fields{"device-id": device.Id, "error": err})
	return err
}

func (dh *deviceHandler) CreatePortInCore(ctx context.Context, port *voltha.Port) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.PortCreated(subCtx, port)
	logger.Debugw(subCtx, "port-created-in-core", log.Fields{"port": port, "error": err})
	return err
}

func (dh *deviceHandler) updatePortStateInCore(ctx context.Context, portState *ca.PortState) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.PortStateUpdate(subCtx, portState)
	logger.Debugw(subCtx, "port-state-updated-in-core", log.Fields{"device-id": dh.device.Id, "port-state": portState, "error": err})
	return err
}

func (dh *deviceHandler) updateDeviceReasonInCore(ctx context.Context, reason *ca.DeviceReason) error {
	cClient, err := dh.coreClient.GetCoreServiceClient()
	if err != nil || cClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.RPCTimeout)
	defer cancel()
	_, err = cClient.DeviceReasonUpdate(subCtx, reason)
	logger.Debugw(subCtx, "device-reason-updated-in-core", log.Fields{"device-id": dh.device.Id, "reason": reason, "error": err})
	return err
}

/*
Helper functions to communicate with parent adapter
*/

func (dh *deviceHandler) GetTechProfileInstanceFromParentAdapter(ctx context.Context, aUniID uint8,
	aTpPath string) (*ia.TechProfileDownloadMessage, error) {

	var request = ia.TechProfileInstanceRequestMessage{
		DeviceId:       dh.DeviceID,
		TpInstancePath: aTpPath,
		ParentDeviceId: dh.parentID,
		ParentPonPort:  dh.device.ParentPortNo,
		OnuId:          dh.device.ProxyAddress.OnuId,
		UniId:          uint32(aUniID),
	}

	pgClient, err := dh.pOpenOnuAc.getParentAdapterServiceClient(dh.device.ProxyAddress.AdapterEndpoint)
	if err != nil || pgClient == nil {
		return nil, err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.MaxTimeoutInterAdapterComm)
	defer cancel()
	logger.Debugw(subCtx, "get-tech-profile-instance",
		log.Fields{"device-id": dh.device.Id, "request": request, "parent-endpoint": dh.device.ProxyAddress.AdapterEndpoint})
	return pgClient.GetTechProfileInstance(subCtx, &request)
}

// This routine is unique per ONU ID and blocks on flowControlBlock channel for incoming flows
// Each incoming flow is processed in a synchronous manner, i.e., the flow is processed to completion before picking another
func (dh *deviceHandler) PerOnuFlowHandlerRoutine(uniID uint8) {
	logger.Infow(context.Background(), "starting-flow-handler-routine", log.Fields{"device-id": dh.DeviceID})
	dh.setFlowMonitoringIsRunning(uniID, true)
	for {
		select {
		// block on the channel to receive an incoming flow
		// process the flow completely before proceeding to handle the next flow
		case flowCb := <-dh.flowCbChan[uniID]:
			startTime := time.Now()
			logger.Info(flowCb.ctx, "serial-flow-processor--start", log.Fields{"device-id": dh.DeviceID})
			respChan := make(chan error)
			if flowCb.addFlow {
				go dh.addFlowItemToUniPort(flowCb.ctx, flowCb.flowItem, flowCb.uniPort, flowCb.flowMetaData, &respChan)
			} else {
				go dh.removeFlowItemFromUniPort(flowCb.ctx, flowCb.flowItem, flowCb.uniPort, &respChan)
			}
			// Block on response and tunnel it back to the caller
			*flowCb.respChan <- <-respChan
			logger.Info(flowCb.ctx, "serial-flow-processor--end",
				log.Fields{"device-id": dh.DeviceID, "absoluteTimeForFlowProcessingInSecs": time.Since(startTime).Seconds()})
		case <-dh.stopFlowMonitoringRoutine[uniID]:
			logger.Infow(context.Background(), "stopping-flow-handler-routine", log.Fields{"device-id": dh.DeviceID})
			dh.setFlowMonitoringIsRunning(uniID, false)
			return
		}
	}
}

func (dh *deviceHandler) SendOnuSwSectionsOfWindow(ctx context.Context, parentEndpoint string, request *ia.OmciMessages) error {
	request.ParentDeviceId = dh.GetProxyAddressID()
	request.ChildDeviceId = dh.DeviceID
	request.ProxyAddress = dh.GetProxyAddress()
	request.ConnectStatus = common.ConnectStatus_REACHABLE

	pgClient, err := dh.pOpenOnuAc.getParentAdapterServiceClient(parentEndpoint)
	if err != nil || pgClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.MaxTimeoutInterAdapterComm)
	defer cancel()
	logger.Debugw(subCtx, "send-omci-request", log.Fields{"request": request, "parent-endpoint": parentEndpoint})
	_, err = pgClient.ProxyOmciRequests(subCtx, request)
	if err != nil {
		logger.Errorw(ctx, "omci-failure", log.Fields{"device-id": dh.device.Id, "request": request, "error": err,
			"request-parent": request.ParentDeviceId, "request-child": request.ChildDeviceId, "request-proxy": request.ProxyAddress})
	}
	return err
}

func (dh *deviceHandler) SendOMCIRequest(ctx context.Context, parentEndpoint string, request *ia.OmciMessage) error {
	pgClient, err := dh.pOpenOnuAc.getParentAdapterServiceClient(parentEndpoint)
	if err != nil || pgClient == nil {
		return err
	}
	subCtx, cancel := context.WithTimeout(log.WithSpanFromContext(context.Background(), ctx), dh.config.MaxTimeoutInterAdapterComm)
	defer cancel()
	dh.setOltAvailable(true)
	logger.Debugw(subCtx, "send-omci-request", log.Fields{"device-id": dh.device.Id, "request": request, "parent-endpoint": parentEndpoint})
	_, err = pgClient.ProxyOmciRequest(subCtx, request)
	if err != nil {
		if status.Code(err) == codes.Unavailable {
			dh.setOltAvailable(false)
		}
		logger.Errorw(ctx, "omci-failure",
			log.Fields{"device-id": dh.device.Id, "request": request, "error": err, "request-parent": request.ParentDeviceId,
				"request-child": request.ChildDeviceId, "request-proxy": request.ProxyAddress, "oltAvailable": dh.IsOltAvailable})
	}
	return err
}

func (dh *deviceHandler) CheckAvailableOnuCapabilities(ctx context.Context, pDevEntry *mib.OnuDeviceEntry, tpInst tech_profile.TechProfileInstance) error {
	// Check if there are additional TCONT instances necessary/available
	pDevEntry.MutexPersOnuConfig.Lock()
	if _, ok := pDevEntry.SOnuPersistentData.PersTcontMap[uint16(tpInst.UsScheduler.AllocId)]; !ok {
		numberOfTcontMapEntries := len(pDevEntry.SOnuPersistentData.PersTcontMap)
		pDevEntry.MutexPersOnuConfig.Unlock()
		numberOfTcontDbInsts := pDevEntry.GetOnuDB().GetNumberOfInst(me.TContClassID)
		logger.Debugw(ctx, "checking available TCONT instances",
			log.Fields{"device-id": dh.DeviceID, "numberOfTcontMapEntries": numberOfTcontMapEntries, "numberOfTcontDbInsts": numberOfTcontDbInsts})
		if numberOfTcontMapEntries >= numberOfTcontDbInsts {
			logger.Errorw(ctx, "configuration exceeds ONU capabilities - running out of TCONT instances: send ONU device event!",
				log.Fields{"device-id": dh.device.Id})
			pDevEntry.SendOnuDeviceEvent(ctx, cmn.OnuConfigFailureMissingTcont, cmn.OnuConfigFailureMissingTcontDesc)
			return fmt.Errorf("configuration exceeds ONU capabilities - running out of TCONT instances: device-id: %s", dh.DeviceID)
		}
	} else {
		pDevEntry.MutexPersOnuConfig.Unlock()
	}
	// Check if there are enough PrioQueue instances available
	if dh.pOnuTP != nil {
		var numberOfUsPrioQueueDbInsts int

		queueInstKeys := pDevEntry.GetOnuDB().GetSortedInstKeys(ctx, me.PriorityQueueClassID)
		for _, mgmtEntityID := range queueInstKeys {
			if mgmtEntityID >= 0x8000 {
				numberOfUsPrioQueueDbInsts++
			}
		}
		// Check if there is an upstream PriorityQueue instance available for each Gem port
		numberOfConfiguredGemPorts := dh.pOnuTP.GetNumberOfConfiguredUsGemPorts(ctx)
		logger.Debugw(ctx, "checking available upstream PriorityQueue instances",
			log.Fields{"device-id": dh.DeviceID,
				"numberOfConfiguredGemPorts": numberOfConfiguredGemPorts,
				"tpInst.NumGemPorts":         tpInst.NumGemPorts,
				"numberOfUsPrioQueueDbInsts": numberOfUsPrioQueueDbInsts})

		if numberOfConfiguredGemPorts+int(tpInst.NumGemPorts) > numberOfUsPrioQueueDbInsts {
			logger.Errorw(ctx, "configuration exceeds ONU capabilities - running out of upstream PrioQueue instances: send ONU device event!",
				log.Fields{"device-id": dh.device.Id})
			pDevEntry.SendOnuDeviceEvent(ctx, cmn.OnuConfigFailureMissingUsPriorityQueue, cmn.OnuConfigFailureMissingUsPriorityQueueDesc)
			return fmt.Errorf("configuration exceeds ONU capabilities - running out of upstream PrioQueue instances: device-id: %s", dh.DeviceID)
		}
		// Downstream PrioQueue instances are evaluated in accordance with ONU MIB upload data in function UniPonAniConfigFsm::prepareAndEnterConfigState().
		// In case of missing downstream PrioQueues the attribute "Priority queue pointer for downstream" of ME "GEM port network CTP" will be set to "0",
		// which then alternatively activates the queuing mechanisms of the ONU (refer to Rec. ITU-T G.988 chapter 9.2.3).
	} else {
		logger.Warnw(ctx, "onuTechProf instance not set up - check for PriorityQueue instances skipped!",
			log.Fields{"device-id": dh.DeviceID})
	}
	return nil
}

// GetDeviceID - TODO: add comment
func (dh *deviceHandler) GetDeviceID() string {
	return dh.DeviceID
}

// GetProxyAddressID - TODO: add comment
func (dh *deviceHandler) GetProxyAddressID() string {
	return dh.device.ProxyAddress.GetDeviceId()
}

// GetProxyAddressType - TODO: add comment
func (dh *deviceHandler) GetProxyAddressType() string {
	return dh.device.ProxyAddress.GetDeviceType()
}

// GetProxyAddress - TODO: add comment
func (dh *deviceHandler) GetProxyAddress() *voltha.Device_ProxyAddress {
	return dh.device.ProxyAddress
}

// GetEventProxy - TODO: add comment
func (dh *deviceHandler) GetEventProxy() eventif.EventProxy {
	return dh.EventProxy
}

// GetOmciTimeout - TODO: add comment
func (dh *deviceHandler) GetOmciTimeout() int {
	return dh.pOpenOnuAc.omciTimeout
}

// GetAlarmAuditInterval - TODO: add comment
func (dh *deviceHandler) GetAlarmAuditInterval() time.Duration {
	return dh.pOpenOnuAc.alarmAuditInterval
}

// GetDlToOnuTimeout4M - TODO: add comment
func (dh *deviceHandler) GetDlToOnuTimeout4M() time.Duration {
	return dh.pOpenOnuAc.dlToOnuTimeout4M
}

// GetUniEntityMap - TODO: add comment
func (dh *deviceHandler) GetUniEntityMap() *cmn.OnuUniPortMap {
	return &dh.uniEntityMap
}

// GetPonPortNumber - TODO: add comment
func (dh *deviceHandler) GetPonPortNumber() *uint32 {
	return &dh.ponPortNumber
}

// GetUniVlanConfigFsm - TODO: add comment
func (dh *deviceHandler) GetUniVlanConfigFsm(uniID uint8) cmn.IuniVlanConfigFsm {
	dh.lockVlanConfig.RLock()
	value := dh.UniVlanConfigFsmMap[uniID]
	dh.lockVlanConfig.RUnlock()
	return value
}

// GetOnuAlarmManager - TODO: add comment
func (dh *deviceHandler) GetOnuAlarmManager() cmn.IonuAlarmManager {
	return dh.pAlarmMgr
}

// GetOnuMetricsManager - TODO: add comment
func (dh *deviceHandler) GetOnuMetricsManager() cmn.IonuMetricsManager {
	return dh.pOnuMetricsMgr
}

// GetOnuTP - TODO: add comment
func (dh *deviceHandler) GetOnuTP() cmn.IonuUniTechProf {
	return dh.pOnuTP
}

// GetBackendPathPrefix - TODO: add comment
func (dh *deviceHandler) GetBackendPathPrefix() string {
	return dh.pOpenOnuAc.cm.Backend.PathPrefix
}

// GetOnuIndication - TODO: add comment
func (dh *deviceHandler) GetOnuIndication() *openolt.OnuIndication {
	return dh.pOnuIndication
}

// RLockMutexDeletionInProgressFlag - TODO: add comment
func (dh *deviceHandler) RLockMutexDeletionInProgressFlag() {
	dh.mutexDeletionInProgressFlag.RLock()
}

// RUnlockMutexDeletionInProgressFlag - TODO: add comment
func (dh *deviceHandler) RUnlockMutexDeletionInProgressFlag() {
	dh.mutexDeletionInProgressFlag.RUnlock()
}

// GetDeletionInProgress - TODO: add comment
func (dh *deviceHandler) GetDeletionInProgress() bool {
	return dh.deletionInProgress
}

// GetPmConfigs - TODO: add comment
func (dh *deviceHandler) GetPmConfigs() *voltha.PmConfigs {
	return dh.pmConfigs
}

// GetDeviceType - TODO: add comment
func (dh *deviceHandler) GetDeviceType() string {
	return dh.DeviceType
}

// GetLogicalDeviceID - TODO: add comment
func (dh *deviceHandler) GetLogicalDeviceID() string {
	return dh.logicalDeviceID
}

// GetDevice - TODO: add comment
func (dh *deviceHandler) GetDevice() *voltha.Device {
	return dh.device
}

func (dh *deviceHandler) setOltAvailable(value bool) {
	dh.mutexOltAvailable.Lock()
	dh.oltAvailable = value
	dh.mutexOltAvailable.Unlock()
}

// IsOltAvailable - TODO: add comment
func (dh *deviceHandler) IsOltAvailable() bool {
	dh.mutexOltAvailable.RLock()
	defer dh.mutexOltAvailable.RUnlock()
	return dh.oltAvailable
}

// GetMetricsEnabled - TODO: add comment
func (dh *deviceHandler) GetMetricsEnabled() bool {
	return dh.pOpenOnuAc.MetricsEnabled
}

// GetExtendedOmciSupportEnabled - TODO: add comment
func (dh *deviceHandler) GetExtendedOmciSupportEnabled() bool {
	return dh.pOpenOnuAc.ExtendedOmciSupportEnabled
}

// GetExtendedOmciSupportEnabled - TODO: add comment
func (dh *deviceHandler) GetSkipOnuConfigEnabled() bool {
	return dh.pOpenOnuAc.skipOnuConfig
}

// InitPmConfigs - TODO: add comment
func (dh *deviceHandler) InitPmConfigs() {
	dh.pmConfigs = &voltha.PmConfigs{}
}

// GetUniPortMask - TODO: add comment
func (dh *deviceHandler) GetUniPortMask() int {
	return dh.pOpenOnuAc.config.UniPortMask
}

func (dh *deviceHandler) anyTpPathExists(aTpPathMap map[uint8]string) bool {
	tpPathFound := false
	for _, tpPath := range aTpPathMap {
		if tpPath != "" {
			tpPathFound = true
		}
	}
	return tpPathFound
}

func (dh *deviceHandler) getOnuActiveAlarms(ctx context.Context) *extension.SingleGetValueResponse {
	resp := dh.GetOnuAlarmManager().GetOnuActiveAlarms(ctx)
	logger.Debugw(ctx, "Received response from AlarmManager for Active Alarms for DeviceEntry", log.Fields{"device-id": dh.DeviceID})
	return resp
}

// getONUGEMStatsInfo - Get the GEM PM history data of the request ONT device
func (dh *deviceHandler) getONUGEMStatsInfo(ctx context.Context) *extension.SingleGetValueResponse {
	resp := dh.pOnuMetricsMgr.GetONUGEMCounters(ctx)
	logger.Debugw(ctx, "Received response from AlarmManager for Active Alarms for DeviceEntry", log.Fields{"device-id": dh.DeviceID})
	return resp
}

func (dh *deviceHandler) GetDeviceDeleteCommChan(ctx context.Context) chan bool {
	return dh.deviceDeleteCommChan
}

// PrepareForGarbageCollection - remove references to prepare for garbage collection
func (dh *deviceHandler) PrepareForGarbageCollection(ctx context.Context, aDeviceID string) {
	logger.Debugw(ctx, "prepare for garbage collection", log.Fields{"device-id": aDeviceID})

	// Note: This function must be called as a goroutine to prevent blocking of further processing!
	// first let the objects rest for some time to give all asynchronously started
	// cleanup routines a chance to come to an end
	time.Sleep(2 * time.Second)

	if dh.pOnuOmciDevice != nil {
		if dh.pOnuOmciDevice.PDevOmciCC != nil {
			// Since we cannot rule out that one of the handlers had initiated any OMCI configurations during its
			// reset handling (even in future coding), request monitoring is canceled here one last time to
			// be sure that all corresponding go routines are terminated
			dh.pOnuOmciDevice.PDevOmciCC.CancelRequestMonitoring(ctx)
		}
	}
	time.Sleep(3 * time.Second)

	if dh.pOnuTP != nil {
		dh.pOnuTP.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pOnuMetricsMgr != nil {
		logger.Debugw(ctx, "preparation of garbage collection is done under control of pm fsm - wait for completion",
			log.Fields{"device-id": aDeviceID})
		select {
		case <-dh.pOnuMetricsMgr.GarbageCollectionComplete:
			logger.Debugw(ctx, "pm fsm shut down and garbage collection complete", log.Fields{"deviceID": aDeviceID})
		case <-time.After(pmmgr.MaxTimeForPmFsmShutDown * time.Second):
			logger.Errorw(ctx, "fsm did not shut down in time", log.Fields{"deviceID": aDeviceID})
		default:
		}
	}
	if dh.pAlarmMgr != nil {
		dh.pAlarmMgr.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pSelfTestHdlr != nil {
		dh.pSelfTestHdlr.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pLockStateFsm != nil {
		dh.pLockStateFsm.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pUnlockStateFsm != nil {
		dh.pUnlockStateFsm.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pOnuUpradeFsm != nil {
		dh.pOnuUpradeFsm.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	if dh.pOnuOmciDevice != nil {
		dh.pOnuOmciDevice.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	for k, v := range dh.UniVlanConfigFsmMap {
		v.PrepareForGarbageCollection(ctx, aDeviceID)
		delete(dh.UniVlanConfigFsmMap, k)
	}
	dh.pOnuIndication = nil
	dh.pOnuOmciDevice = nil
	dh.pOnuTP = nil
	dh.pOnuMetricsMgr = nil
	dh.pAlarmMgr = nil
	dh.pSelfTestHdlr = nil
	dh.pLockStateFsm = nil
	dh.pUnlockStateFsm = nil
	dh.pOnuUpradeFsm = nil
}
