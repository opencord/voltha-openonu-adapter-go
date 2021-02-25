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
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/looplab/fsm"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v4/pkg/db"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	flow "github.com/opencord/voltha-lib-go/v4/pkg/flows"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	vc "github.com/opencord/voltha-protos/v4/go/common"
	"github.com/opencord/voltha-protos/v4/go/extension"
	ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	"github.com/opencord/voltha-protos/v4/go/openflow_13"
	of "github.com/opencord/voltha-protos/v4/go/openflow_13"
	ofp "github.com/opencord/voltha-protos/v4/go/openflow_13"
	oop "github.com/opencord/voltha-protos/v4/go/openolt"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

/*
// Constants for number of retries and for timeout
const (
	MaxRetry       = 10
	MaxTimeOutInMs = 500
)
*/

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

//Event category and subcategory definitions - same as defiend for OLT in eventmgr.go  - should be done more centrally
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

type usedOmciConfigFsms int

const (
	cUploadFsm usedOmciConfigFsms = iota
	cDownloadFsm
	cUniLockFsm
	cUniUnLockFsm
	cAniConfigFsm
	cUniVlanConfigFsm
	cL2PmFsm
	cOnuUpgradeFsm
)

type omciIdleCheckStruct struct {
	omciIdleCheckFunc func(*deviceHandler, context.Context, usedOmciConfigFsms, string) bool
	omciIdleState     string
}

var fsmOmciIdleStateFuncMap = map[usedOmciConfigFsms]omciIdleCheckStruct{
	cUploadFsm:        {(*deviceHandler).isFsmInOmciIdleStateDefault, cMibUlFsmIdleState},
	cDownloadFsm:      {(*deviceHandler).isFsmInOmciIdleStateDefault, cMibDlFsmIdleState},
	cUniLockFsm:       {(*deviceHandler).isFsmInOmciIdleStateDefault, cUniFsmIdleState},
	cUniUnLockFsm:     {(*deviceHandler).isFsmInOmciIdleStateDefault, cUniFsmIdleState},
	cAniConfigFsm:     {(*deviceHandler).isAniConfigFsmInOmciIdleState, cAniFsmIdleState},
	cUniVlanConfigFsm: {(*deviceHandler).isUniVlanConfigFsmInOmciIdleState, cVlanFsmIdleState},
	cL2PmFsm:          {(*deviceHandler).isFsmInOmciIdleStateDefault, cL2PmFsmIdleState},
	cOnuUpgradeFsm:    {(*deviceHandler).isFsmInOmciIdleStateDefault, cOnuUpgradeFsmIdleState},
}

const (
	// device reasons
	drUnset                            = 0
	drActivatingOnu                    = 1
	drStartingOpenomci                 = 2
	drDiscoveryMibsyncComplete         = 3
	drInitialMibDownloaded             = 4
	drTechProfileConfigDownloadSuccess = 5
	drOmciFlowsPushed                  = 6
	drOmciAdminLock                    = 7
	drOnuReenabled                     = 8
	drStoppingOpenomci                 = 9
	drRebooting                        = 10
	drOmciFlowsDeleted                 = 11
	drTechProfileConfigDeleteSuccess   = 12
)

var deviceReasonMap = map[uint8]string{
	drUnset:                            "unset",
	drActivatingOnu:                    "activating-onu",
	drStartingOpenomci:                 "starting-openomci",
	drDiscoveryMibsyncComplete:         "discovery-mibsync-complete",
	drInitialMibDownloaded:             "initial-mib-downloaded",
	drTechProfileConfigDownloadSuccess: "tech-profile-config-download-success",
	drOmciFlowsPushed:                  "omci-flows-pushed",
	drOmciAdminLock:                    "omci-admin-lock",
	drOnuReenabled:                     "onu-reenabled",
	drStoppingOpenomci:                 "stopping-openomci",
	drRebooting:                        "rebooting",
	drOmciFlowsDeleted:                 "omci-flows-deleted",
	drTechProfileConfigDeleteSuccess:   "tech-profile-config-delete-success",
}

//deviceHandler will interact with the ONU ? device.
type deviceHandler struct {
	deviceID         string
	DeviceType       string
	adminState       string
	device           *voltha.Device
	logicalDeviceID  string
	ProxyAddressID   string
	ProxyAddressType string
	parentID         string
	ponPortNumber    uint32

	coreProxy    adapterif.CoreProxy
	AdapterProxy adapterif.AdapterProxy
	EventProxy   eventif.EventProxy

	pmConfigs *voltha.PmConfigs

	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	//pPonPort        *voltha.Port
	deviceEntrySet  chan bool //channel for DeviceEntry set event
	pOnuOmciDevice  *OnuDeviceEntry
	pOnuTP          *onuUniTechProf
	pOnuMetricsMgr  *onuMetricsManager
	pAlarmMgr       *onuAlarmManager
	exitChannel     chan int
	lockDevice      sync.RWMutex
	pOnuIndication  *oop.OnuIndication
	deviceReason    uint8
	pLockStateFsm   *lockStateFsm
	pUnlockStateFsm *lockStateFsm

	//flowMgr       *OpenOltFlowMgr
	//eventMgr      *OpenOltEventMgr
	//resourceMgr   *rsrcMgr.OpenOltResourceMgr

	//discOnus sync.Map
	//onus     sync.Map
	//portStats          *OpenOltStatisticsMgr
	collectorIsRunning         bool
	mutexCollectorFlag         sync.RWMutex
	stopCollector              chan bool
	alarmManagerIsRunning      bool
	mutextAlarmManagerFlag     sync.RWMutex
	stopAlarmManager           chan bool
	stopHeartbeatCheck         chan bool
	uniEntityMap               map[uint32]*onuUniPort
	mutexKvStoreContext        sync.Mutex
	lockVlanConfig             sync.RWMutex
	UniVlanConfigFsmMap        map[uint8]*UniVlanConfigFsm
	lockUpgradeFsm             sync.RWMutex
	pOnuUpradeFsm              *OnuUpgradeFsm
	reconciling                bool
	mutexReconcilingFlag       sync.RWMutex
	chReconcilingFinished      chan bool //channel to indicate that reconciling has been finished
	ReadyForSpecificOmciConfig bool
}

//newDeviceHandler creates a new device handler
func newDeviceHandler(ctx context.Context, cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep eventif.EventProxy, device *voltha.Device, adapter *OpenONUAC) *deviceHandler {
	var dh deviceHandler
	dh.coreProxy = cp
	dh.AdapterProxy = ap
	dh.EventProxy = ep
	cloned := (proto.Clone(device)).(*voltha.Device)
	dh.deviceID = cloned.Id
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
	dh.uniEntityMap = make(map[uint32]*onuUniPort)
	dh.lockVlanConfig = sync.RWMutex{}
	dh.lockUpgradeFsm = sync.RWMutex{}
	dh.UniVlanConfigFsmMap = make(map[uint8]*UniVlanConfigFsm)
	dh.reconciling = false
	dh.chReconcilingFinished = make(chan bool)
	dh.ReadyForSpecificOmciConfig = false

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
	logger.Debugw(ctx, "starting-device-handler", log.Fields{"device": dh.device, "device-id": dh.deviceID})
	// Add the initial device to the local model
	logger.Debug(ctx, "device-handler-started")
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

//adoptOrReconcileDevice adopts the ONU device
func (dh *deviceHandler) adoptOrReconcileDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "Adopt_or_reconcile_device", log.Fields{"device-id": device.Id, "Address": device.GetHostAndPort()})

	logger.Debugw(ctx, "Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
	if dh.pDeviceStateFsm.Is(devStNull) {
		if err := dh.pDeviceStateFsm.Event(devEvDeviceInit); err != nil {
			logger.Errorw(ctx, "Device FSM: Can't go to state DeviceInit", log.Fields{"err": err})
		}
		logger.Debugw(ctx, "Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
		// device.PmConfigs is not nil in cases when adapter restarts. We should not re-set the core again.
		if device.PmConfigs == nil {
			// Now, set the initial PM configuration for that device
			if err := dh.coreProxy.DevicePMConfigUpdate(ctx, dh.pmConfigs); err != nil {
				logger.Errorw(ctx, "error updating pm config to core", log.Fields{"device-id": dh.deviceID, "err": err})
			}
		}
	} else {
		logger.Debugw(ctx, "AdoptOrReconcileDevice: Agent/device init already done", log.Fields{"device-id": device.Id})
	}

}

func (dh *deviceHandler) processInterAdapterOMCIReceiveMessage(ctx context.Context, msg *ic.InterAdapterMessage) error {
	msgBody := msg.GetBody()
	omciMsg := &ic.InterAdapterOmciMessage{}
	if err := ptypes.UnmarshalAny(msgBody, omciMsg); err != nil {
		logger.Warnw(ctx, "cannot-unmarshal-omci-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	/* msg print moved symmetrically to omci_cc, if wanted here as additional debug, than perhaps only based on additional debug setting!
	//assuming omci message content is hex coded!
	// with restricted output of 16(?) bytes would be ...omciMsg.Message[:16]
	logger.Debugw(ctx, "inter-adapter-recv-omci", log.Fields{
		"device-id": dh.deviceID, "RxOmciMessage": hex.EncodeToString(omciMsg.Message)})
	*/
	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry != nil {
		if pDevEntry.PDevOmciCC != nil {
			return pDevEntry.PDevOmciCC.receiveMessage(log.WithSpanFromContext(context.TODO(), ctx), omciMsg.Message)
		}
		logger.Debugw(ctx, "omciCC not ready to receive omci messages - incoming omci message ignored", log.Fields{"rxMsg": omciMsg.Message})
	}
	logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
	return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
}

func (dh *deviceHandler) processInterAdapterTechProfileDownloadReqMessage(
	ctx context.Context,
	msg *ic.InterAdapterMessage) error {

	logger.Infow(ctx, "tech-profile-download-request", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Errorw(ctx, "onuTechProf instance not set up for DLMsg request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DLMsg request while onuTechProf instance not setup: %s", dh.deviceID)
	}
	if !dh.ReadyForSpecificOmciConfig {
		logger.Errorw(ctx, "TechProf-set rejected: improper device state", log.Fields{"device-id": dh.deviceID,
			"device-state": deviceReasonMap[dh.deviceReason]})
		return fmt.Errorf("improper device state %s on device %s", deviceReasonMap[dh.deviceReason], dh.deviceID)
	}
	//previous state test here was just this one, now extended for more states to reject the SetRequest:
	// at least 'mib-downloaded' should be reached for processing of this specific ONU configuration
	//  if (dh.deviceReason == "stopping-openomci") || (dh.deviceReason == "omci-admin-lock")

	msgBody := msg.GetBody()
	techProfMsg := &ic.InterAdapterTechProfileDownloadMessage{}
	if err := ptypes.UnmarshalAny(msgBody, techProfMsg); err != nil {
		logger.Warnw(ctx, "cannot-unmarshal-techprof-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	// we have to lock access to TechProfile processing based on different messageType calls or
	// even to fast subsequent calls of the same messageType as well as OnuKVStore processing due
	// to possible concurrent access by flow processing
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()

	if techProfMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			techProfMsg.UniId, dh.deviceID))
	}
	uniID := uint8(techProfMsg.UniId)
	tpID, err := GetTpIDFromTpPath(techProfMsg.Path)
	if err != nil {
		logger.Errorw(ctx, "error-parsing-tpid-from-tppath", log.Fields{"err": err, "tp-path": techProfMsg.Path})
		return err
	}

	if bTpModify := pDevEntry.updateOnuUniTpPath(ctx, uniID, uint8(tpID), techProfMsg.Path); bTpModify {
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

		dh.pOnuTP.resetTpProcessingErrorIndication(uniID, tpID)
		pDevEntry.resetKvProcessingErrorIndication()

		var wg sync.WaitGroup
		wg.Add(2) // for the 2 go routines to finish
		// attention: deadline completion check and wg.Done is to be done in both routines
		go dh.pOnuTP.configureUniTp(log.WithSpanFromContext(dctx, ctx), uniID, techProfMsg.Path, &wg)
		go pDevEntry.updateOnuKvStore(log.WithSpanFromContext(dctx, ctx), &wg)
		dh.waitForCompletion(ctx, cancel, &wg, "TechProfDwld") //wait for background process to finish

		return dh.combineErrorStrings(dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID), pDevEntry.getKvProcessingErrorIndication())
	}
	// no change, nothing really to do - return success
	return nil
}

func (dh *deviceHandler) processInterAdapterDeleteGemPortReqMessage(
	ctx context.Context,
	msg *ic.InterAdapterMessage) error {

	logger.Infow(ctx, "delete-gem-port-request", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw(ctx, "onuTechProf instance not set up for DelGem request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DelGem request while onuTechProf instance not setup: %s", dh.deviceID)
	}

	msgBody := msg.GetBody()
	delGemPortMsg := &ic.InterAdapterDeleteGemPortMessage{}
	if err := ptypes.UnmarshalAny(msgBody, delGemPortMsg); err != nil {
		logger.Warnw(ctx, "cannot-unmarshal-delete-gem-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()

	if delGemPortMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			delGemPortMsg.UniId, dh.deviceID))
	}
	uniID := uint8(delGemPortMsg.UniId)
	tpID, err := GetTpIDFromTpPath(delGemPortMsg.TpPath)
	if err != nil {
		logger.Errorw(ctx, "error-extracting-tp-id-from-tp-path", log.Fields{"err": err, "tp-path": delGemPortMsg.TpPath})
		return err
	}

	//a removal of some GemPort would never remove the complete TechProfile entry (done on T-Cont)

	// deadline context to ensure completion of background routines waited for
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	dh.pOnuTP.resetTpProcessingErrorIndication(uniID, tpID)

	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish
	go dh.pOnuTP.deleteTpResource(log.WithSpanFromContext(dctx, ctx), uniID, tpID, delGemPortMsg.TpPath,
		cResourceGemPort, delGemPortMsg.GemPortId, &wg)
	dh.waitForCompletion(ctx, cancel, &wg, "GemDelete") //wait for background process to finish

	return dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID)
}

func (dh *deviceHandler) processInterAdapterDeleteTcontReqMessage(
	ctx context.Context,
	msg *ic.InterAdapterMessage) error {

	logger.Infow(ctx, "delete-tcont-request", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw(ctx, "onuTechProf instance not set up for DelTcont request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DelTcont request while onuTechProf instance not setup: %s", dh.deviceID)
	}

	msgBody := msg.GetBody()
	delTcontMsg := &ic.InterAdapterDeleteTcontMessage{}
	if err := ptypes.UnmarshalAny(msgBody, delTcontMsg); err != nil {
		logger.Warnw(ctx, "cannot-unmarshal-delete-tcont-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()

	if delTcontMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			delTcontMsg.UniId, dh.deviceID))
	}
	uniID := uint8(delTcontMsg.UniId)
	tpPath := delTcontMsg.TpPath
	tpID, err := GetTpIDFromTpPath(tpPath)
	if err != nil {
		logger.Errorw(ctx, "error-extracting-tp-id-from-tp-path", log.Fields{"err": err, "tp-path": tpPath})
		return err
	}

	if bTpModify := pDevEntry.updateOnuUniTpPath(ctx, uniID, tpID, ""); bTpModify {
		// deadline context to ensure completion of background routines waited for
		deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
		dctx, cancel := context.WithDeadline(context.Background(), deadline)

		dh.pOnuTP.resetTpProcessingErrorIndication(uniID, tpID)
		pDevEntry.resetKvProcessingErrorIndication()

		var wg sync.WaitGroup
		wg.Add(2) // for the 2 go routines to finish
		go dh.pOnuTP.deleteTpResource(log.WithSpanFromContext(dctx, ctx), uniID, tpID, delTcontMsg.TpPath,
			cResourceTcont, delTcontMsg.AllocId, &wg)
		// Removal of the tcont/alloc id mapping represents the removal of the tech profile
		go pDevEntry.updateOnuKvStore(log.WithSpanFromContext(dctx, ctx), &wg)
		dh.waitForCompletion(ctx, cancel, &wg, "TContDelete") //wait for background process to finish

		return dh.combineErrorStrings(dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID), pDevEntry.getKvProcessingErrorIndication())
	}
	return nil
}

//processInterAdapterMessage sends the proxied messages to the target device
// If the proxy address is not found in the unmarshalled message, it first fetches the onu device for which the message
// is meant, and then send the unmarshalled omci message to this onu
func (dh *deviceHandler) processInterAdapterMessage(ctx context.Context, msg *ic.InterAdapterMessage) error {
	msgID := msg.Header.Id
	msgType := msg.Header.Type
	fromTopic := msg.Header.FromTopic
	toTopic := msg.Header.ToTopic
	toDeviceID := msg.Header.ToDeviceId
	proxyDeviceID := msg.Header.ProxyDeviceId
	logger.Debugw(ctx, "InterAdapter message header", log.Fields{"msgID": msgID, "msgType": msgType,
		"fromTopic": fromTopic, "toTopic": toTopic, "toDeviceID": toDeviceID, "proxyDeviceID": proxyDeviceID})

	switch msgType {
	// case ic.InterAdapterMessageType_ONU_IND_REQUEST: was handled by OpenONUAC already - see comments there
	//OMCI_RESPONSE also accepted acc. to VOL-3756 (OMCI_REQUEST request was legacy code)
	case ic.InterAdapterMessageType_OMCI_RESPONSE, ic.InterAdapterMessageType_OMCI_REQUEST:
		{
			return dh.processInterAdapterOMCIReceiveMessage(ctx, msg)
		}
	case ic.InterAdapterMessageType_TECH_PROFILE_DOWNLOAD_REQUEST:
		{
			return dh.processInterAdapterTechProfileDownloadReqMessage(ctx, msg)
		}
	case ic.InterAdapterMessageType_DELETE_GEM_PORT_REQUEST:
		{
			return dh.processInterAdapterDeleteGemPortReqMessage(ctx, msg)

		}
	case ic.InterAdapterMessageType_DELETE_TCONT_REQUEST:
		{
			return dh.processInterAdapterDeleteTcontReqMessage(ctx, msg)
		}
	default:
		{
			logger.Errorw(ctx, "inter-adapter-unhandled-type", log.Fields{
				"msgType": msg.Header.Type, "device-id": dh.deviceID})
			return fmt.Errorf("inter-adapter-unhandled-type: %d, %s", msg.Header.Type, dh.deviceID)
		}
	}
}

//FlowUpdateIncremental removes and/or adds the flow changes on a given device
func (dh *deviceHandler) FlowUpdateIncremental(ctx context.Context,
	apOfFlowChanges *openflow_13.FlowChanges,
	apOfGroupChanges *openflow_13.FlowGroupChanges, apFlowMetaData *voltha.FlowMetadata) error {
	logger.Debugw(ctx, "FlowUpdateIncremental started", log.Fields{"device-id": dh.deviceID})

	var retError error = nil
	//Remove flows (always remove flows first - remove old and add new with same cookie may be part of the same request)
	if apOfFlowChanges.ToRemove != nil {
		for _, flowItem := range apOfFlowChanges.ToRemove.Items {
			if flowItem.GetCookie() == 0 {
				logger.Warnw(ctx, "flow-remove no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-remove no cookie, device-id %s", dh.deviceID)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw(ctx, "flow-remove inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-remove inPort invalid, device-id %s", dh.deviceID)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.deviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow, not regarded as error, just ignored
				logger.Debugw(ctx, "flow-remove for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *onuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw(ctx, "flow-remove inPort not found in UniPorts: ignore and continuing on checking further flows",
						log.Fields{"device-id": dh.deviceID, "inPort": flowInPort})
					retError = fmt.Errorf("flow-remove inPort not found in UniPorts, inPort %d, device-id %s",
						flowInPort, dh.deviceID)
					continue
				}
				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw(ctx, "flow-remove port indications", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.name})
				err := dh.removeFlowItemFromUniPort(ctx, flowItem, loUniPort)
				//try next flow after processing error
				if err != nil {
					logger.Warnw(ctx, "flow-remove processing error: continuing on checking further flows",
						log.Fields{"device-id": dh.deviceID, "error": err})
					retError = err
					continue
					//return err
				} else { // if last setting succeeds, overwrite possibly previously set error
					retError = nil
				}
			}
		}
	}
	if apOfFlowChanges.ToAdd != nil {
		for _, flowItem := range apOfFlowChanges.ToAdd.Items {
			if flowItem.GetCookie() == 0 {
				logger.Debugw(ctx, "incremental flow-add no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-add no cookie, device-id %s", dh.deviceID)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw(ctx, "flow-add inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-add inPort invalid, device-id %s", dh.deviceID)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.deviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow
				logger.Debugw(ctx, "flow-add for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *onuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw(ctx, "flow-add inPort not found in UniPorts: ignore and continuing on checking further flows",
						log.Fields{"device-id": dh.deviceID, "inPort": flowInPort})
					retError = fmt.Errorf("flow-add inPort not found in UniPorts, inPort %d, device-id %s",
						flowInPort, dh.deviceID)
					continue
					//return fmt.Errorf("flow-parameter inPort %d not found in internal UniPorts", flowInPort)
				}
				// let's still assume that we receive the flow-add only in some 'active' device state (as so far observed)
				// if not, we just throw some error here to have an indication about that, if we really need to support that
				//   then we would need to create some means to activate the internal stored flows
				//   after the device gets active automatically (and still with its dependency to the TechProfile)
				// for state checking compare also code here: processInterAdapterTechProfileDownloadReqMessage
				// also abort for the other still possible flows here
				if !dh.ReadyForSpecificOmciConfig {
					logger.Errorw(ctx, "flow-add rejected: improper device state", log.Fields{"device-id": dh.deviceID,
						"last device-reason": deviceReasonMap[dh.deviceReason]})
					return fmt.Errorf("improper device state on device %s", dh.deviceID)
				}

				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw(ctx, "flow-add port indications", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.name})
				err := dh.addFlowItemToUniPort(ctx, flowItem, loUniPort)
				//try next flow after processing error
				if err != nil {
					logger.Warnw(ctx, "flow-add processing error: continuing on checking further flows",
						log.Fields{"device-id": dh.deviceID, "error": err})
					retError = err
					continue
					//return err
				} else { // if last setting succeeds, overwrite possibly previously set error
					retError = nil
				}
			}
		}
	}
	return retError
}

//disableDevice locks the ONU and its UNI/VEIP ports (admin lock via OMCI)
//following are the expected device states after this activity:
//Device Admin-State : down (on rwCore), Port-State: UNKNOWN, Conn-State: REACHABLE, Reason: omci-admin-lock
// (Conn-State: REACHABLE might conflict with some previous ONU Down indication - maybe to be resolved later)
func (dh *deviceHandler) disableDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "disable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})

	//admin-lock reason can also be used uniquely for setting the DeviceState accordingly
	//note that disableDevice sequences in some 'ONU active' state may yield also
	// "tech...delete-success" or "omci-flow-deleted" according to further received requests in the end
	// - inblock state checking to prevent possibly unneeded processing (on command repitition)
	if dh.deviceReason != drOmciAdminLock {
		//disable-device shall be just a UNi/ONU-G related admin state setting
		//all other configurations/FSM's shall not be impacted and shall execute as required by the system

		if dh.ReadyForSpecificOmciConfig {
			// disable UNI ports/ONU
			// *** should generate UniDisableStateDone event - used to disable the port(s) on success
			if dh.pLockStateFsm == nil {
				dh.createUniLockFsm(ctx, true, UniDisableStateDone)
			} else { //LockStateFSM already init
				dh.pLockStateFsm.setSuccessEvent(UniDisableStateDone)
				dh.runUniLockFsm(ctx, true)
			}
		} else {
			logger.Debugw(ctx, "DeviceStateUpdate upon disable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
				"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.deviceID})
			if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx),
				dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_UNKNOWN); err != nil {
				//TODO with VOL-3045/VOL-3046: return the error and stop further processing
				logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
			}
			// DeviceReason to update acc.to modified py code as per beginning of Sept 2020

			//TODO with VOL-3045/VOL-3046: catch and return error, valid for all occurrences in the codebase
			_ = dh.deviceReasonUpdate(ctx, drOmciAdminLock, true)
		}
	}
}

//reEnableDevice unlocks the ONU and its UNI/VEIP ports (admin unlock via OMCI)
func (dh *deviceHandler) reEnableDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw(ctx, "reenable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})

	//setting ReadyForSpecificOmciConfig here is just a workaround for BBSIM testing in the sequence
	//  OnuSoftReboot-disable-enable, because BBSIM does not generate a new OnuIndication-Up event after SoftReboot
	//  which is the assumption for real ONU's, where the ready-state is then set according to the following MibUpload/Download
	//  for real ONU's that should have nearly no influence
	//  Note that for real ONU's there is anyway a problematic situation with following sequence:
	//		OnuIndication-Dw (or not active at all) (- disable) - enable: here already the LockFsm may run into timeout (no OmciResponse)
	//      but that anyway is hopefully resolved by some OnuIndication-Up event (maybe to be tested)
	//      one could also argue, that a device-enable should also enable attempts for specific omci configuration
	dh.ReadyForSpecificOmciConfig = true //needed to allow subsequent flow/techProf config (on BBSIM)

	// enable ONU/UNI ports
	// *** should generate UniEnableStateDone event - used to disable the port(s) on success
	if dh.pUnlockStateFsm == nil {
		dh.createUniLockFsm(ctx, false, UniEnableStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.setSuccessEvent(UniEnableStateDone)
		dh.runUniLockFsm(ctx, false)
	}
}

func (dh *deviceHandler) reconcileDeviceOnuInd(ctx context.Context) {
	logger.Debugw(ctx, "reconciling - simulate onu indication", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	if err := pDevEntry.restoreDataFromOnuKvStore(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		if err == fmt.Errorf("no-ONU-data-found") {
			logger.Debugw(ctx, "no persistent data found - abort reconciling", log.Fields{"device-id": dh.deviceID})
		} else {
			logger.Errorw(ctx, "reconciling - restoring OnuTp-data failed - abort", log.Fields{"err": err, "device-id": dh.deviceID})
		}
		dh.stopReconciling(ctx)
		return
	}
	var onuIndication oop.OnuIndication
	onuIndication.IntfId = pDevEntry.sOnuPersistentData.PersIntfID
	onuIndication.OnuId = pDevEntry.sOnuPersistentData.PersOnuID
	onuIndication.OperState = pDevEntry.sOnuPersistentData.PersOperState
	onuIndication.AdminState = pDevEntry.sOnuPersistentData.PersAdminState
	_ = dh.createInterface(ctx, &onuIndication)
}

func (dh *deviceHandler) reconcileDeviceTechProf(ctx context.Context) {
	logger.Debugw(ctx, "reconciling - trigger tech profile config", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()
	pDevEntry.persUniConfigMutex.RLock()
	defer pDevEntry.persUniConfigMutex.RUnlock()

	if len(pDevEntry.sOnuPersistentData.PersUniConfig) == 0 {
		logger.Debugw(ctx, "reconciling - no uni-configs have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.deviceID})
		dh.stopReconciling(ctx)
		return
	}
	for _, uniData := range pDevEntry.sOnuPersistentData.PersUniConfig {
		//TODO: check for uni-port specific reconcilement in case of multi-uni-port-per-onu-support
		if len(uniData.PersTpPathMap) == 0 {
			logger.Debugw(ctx, "reconciling - no TPs have been stored before adapter restart - terminate reconcilement",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.deviceID})
			dh.stopReconciling(ctx)
			return
		}
		for tpID := range uniData.PersTpPathMap {
			// deadline context to ensure completion of background routines waited for
			//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
			deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
			dctx, cancel := context.WithDeadline(ctx, deadline)

			dh.pOnuTP.resetTpProcessingErrorIndication(uniData.PersUniID, tpID)
			var wg sync.WaitGroup
			wg.Add(1) // for the 1 go routine to finish
			go dh.pOnuTP.configureUniTp(log.WithSpanFromContext(dctx, ctx), uniData.PersUniID, uniData.PersTpPathMap[tpID], &wg)
			dh.waitForCompletion(ctx, cancel, &wg, "TechProfReconcile") //wait for background process to finish
			if err := dh.pOnuTP.getTpProcessingErrorIndication(uniData.PersUniID, tpID); err != nil {
				logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.deviceID})
			}
		}
		if len(uniData.PersFlowParams) == 0 {
			logger.Debugw(ctx, "reconciling - no flows have been stored before adapter restart - terminate reconcilement",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.deviceID})
			dh.stopReconciling(ctx)
		}
	}
}

func (dh *deviceHandler) reconcileDeviceFlowConfig(ctx context.Context) {
	logger.Debugw(ctx, "reconciling - trigger flow config", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pDevEntry.persUniConfigMutex.RLock()
	defer pDevEntry.persUniConfigMutex.RUnlock()
	if len(pDevEntry.sOnuPersistentData.PersUniConfig) == 0 {
		logger.Debugw(ctx, "reconciling - no uni-configs have been stored before adapter restart - terminate reconcilement",
			log.Fields{"device-id": dh.deviceID})
		dh.stopReconciling(ctx)
		return
	}
	for _, uniData := range pDevEntry.sOnuPersistentData.PersUniConfig {
		//TODO: check for uni-port specific reconcilement in case of multi-uni-port-per-onu-support
		if len(uniData.PersFlowParams) == 0 {
			logger.Debugw(ctx, "reconciling - no flows have been stored before adapter restart - terminate reconcilement",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.deviceID})
			dh.stopReconciling(ctx)
			return
		}
		var uniPort *onuUniPort
		var exist bool
		uniNo := mkUniPortNum(ctx, dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(), uint32(uniData.PersUniID))
		if uniPort, exist = dh.uniEntityMap[uniNo]; !exist {
			logger.Errorw(ctx, "onuUniPort data not found!", log.Fields{"uniNo": uniNo, "device-id": dh.deviceID})
			return
		}
		for _, flowData := range uniData.PersFlowParams {
			logger.Debugw(ctx, "add flow with cookie slice", log.Fields{"device-id": dh.deviceID, "cookies": flowData.CookieSlice})
			//the slice can be passed 'by value' here, - which internally passes its reference copy
			dh.lockVlanConfig.RLock()
			if _, exist = dh.UniVlanConfigFsmMap[uniData.PersUniID]; exist {
				if err := dh.UniVlanConfigFsmMap[uniData.PersUniID].SetUniFlowParams(ctx, flowData.VlanRuleParams.TpID,
					flowData.CookieSlice, uint16(flowData.VlanRuleParams.MatchVid), uint16(flowData.VlanRuleParams.SetVid),
					uint8(flowData.VlanRuleParams.SetPcp)); err != nil {
					logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.deviceID})
				}
				dh.lockVlanConfig.RUnlock()
			} else {
				dh.lockVlanConfig.RUnlock()
				if err := dh.createVlanFilterFsm(ctx, uniPort, flowData.VlanRuleParams.TpID, flowData.CookieSlice,
					uint16(flowData.VlanRuleParams.MatchVid), uint16(flowData.VlanRuleParams.SetVid),
					uint8(flowData.VlanRuleParams.SetPcp), OmciVlanFilterAddDone); err != nil {
					logger.Errorw(ctx, err.Error(), log.Fields{"device-id": dh.deviceID})
				}
			}
		}
		if len(uniData.PersTpPathMap) == 0 {
			logger.Debugw(ctx, "reconciling - no TPs have been stored before adapter restart - terminate reconcilement",
				log.Fields{"uni-id": uniData.PersUniID, "device-id": dh.deviceID})
			dh.stopReconciling(ctx)
		}
	}
}

func (dh *deviceHandler) reconcileMetrics(ctx context.Context) {
	logger.Debugw(ctx, "reconciling - trigger metrics - to be implemented in scope of VOL-3324!", log.Fields{"device-id": dh.deviceID})

	//TODO: reset of reconciling-flag has always to be done in the last reconcile*() function
	dh.stopReconciling(ctx)
}

func (dh *deviceHandler) deleteDevicePersistencyData(ctx context.Context) error {
	logger.Debugw(ctx, "delete device persistency data", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		//IfDevEntry does not exist here, no problem - no persistent data should have been stored
		logger.Debugw(ctx, "OnuDevice does not exist - nothing to delete", log.Fields{"device-id": dh.deviceID})
		return nil
	}

	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(ctx, deadline)

	pDevEntry.resetKvProcessingErrorIndication()

	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish
	go pDevEntry.deleteDataFromOnuKvStore(log.WithSpanFromContext(dctx, ctx), &wg)
	dh.waitForCompletion(ctx, cancel, &wg, "DeleteDevice") //wait for background process to finish

	// TODO: further actions - stop metrics and FSMs, remove device ...
	return pDevEntry.getKvProcessingErrorIndication()
}

//func (dh *deviceHandler) rebootDevice(ctx context.Context, device *voltha.Device) error {
// before this change here return like this was used:
// 		return fmt.Errorf("device-unreachable: %s, %s", dh.deviceID, device.SerialNumber)
//was and is called in background - error return does not make sense
func (dh *deviceHandler) rebootDevice(ctx context.Context, aCheckDeviceState bool, device *voltha.Device) {
	logger.Infow(ctx, "reboot-device", log.Fields{"device-id": dh.deviceID, "SerialNumber": dh.device.SerialNumber})
	if aCheckDeviceState && device.ConnectStatus != voltha.ConnectStatus_REACHABLE {
		logger.Errorw(ctx, "device-unreachable", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
		return
	}
	if err := dh.pOnuOmciDevice.reboot(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-rebooting-device", log.Fields{"device-id": dh.deviceID, "error": err})
		return
	}

	//transfer the possibly modified logical uni port state
	dh.disableUniPortStateUpdate(ctx)

	logger.Debugw(ctx, "call DeviceStateUpdate upon reboot", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, voltha.ConnectStatus_REACHABLE,
		voltha.OperStatus_DISCOVERED); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		return
	}
	if err := dh.deviceReasonUpdate(ctx, drRebooting, true); err != nil {
		return
	}
	dh.ReadyForSpecificOmciConfig = false
	//no specific activity to synchronize any internal FSM to the 'rebooted' state is explicitly done here
	//  the expectation ids for a real device, that it will be synced with the expected following 'down' indication
	//  as BBSIM does not support this testing requires explicite disable/enable device calls in which sequence also
	//  all other FSM's should be synchronized again
}

//doOnuSwUpgrade initiates the SW download transfer to the ONU and on success activates the (inactive) image
func (dh *deviceHandler) doOnuSwUpgrade(ctx context.Context, apImageDsc *voltha.ImageDownload,
	apDownloadManager *adapterDownloadManager) error {
	logger.Debugw(ctx, "onuSwUpgrade requested", log.Fields{
		"device-id": dh.deviceID, "image-name": (*apImageDsc).Name})

	var err error
	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "start Onu SW upgrade rejected: no valid OnuDevice", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("start Onu SW upgrade rejected: no valid OnuDevice for device-id: %s", dh.deviceID)
	}

	if dh.ReadyForSpecificOmciConfig {
		var inactiveImageID uint16
		if inactiveImageID, err = pDevEntry.GetInactiveImageMeID(ctx); err == nil {
			dh.lockUpgradeFsm.Lock()
			defer dh.lockUpgradeFsm.Unlock()
			if dh.pOnuUpradeFsm == nil {
				err = dh.createOnuUpgradeFsm(ctx, pDevEntry, OmciOnuSwUpgradeDone)
				if err == nil {
					if err = dh.pOnuUpradeFsm.SetDownloadParams(ctx, inactiveImageID, apImageDsc, apDownloadManager); err != nil {
						logger.Errorw(ctx, "onu upgrade fsm could not set parameters", log.Fields{
							"device-id": dh.deviceID, "error": err})
					}
				} else {
					logger.Errorw(ctx, "onu upgrade fsm could not be created", log.Fields{
						"device-id": dh.deviceID, "error": err})
				}
			} else { //OnuSw upgrade already running - restart (with possible abort of running)
				logger.Debugw(ctx, "Onu SW upgrade already running - abort", log.Fields{"device-id": dh.deviceID})
				pUpgradeStatemachine := dh.pOnuUpradeFsm.pAdaptFsm.pFsm
				if pUpgradeStatemachine != nil {
					if err = pUpgradeStatemachine.Event(upgradeEvAbort); err != nil {
						logger.Errorw(ctx, "onu upgrade fsm could not abort a running processing", log.Fields{
							"device-id": dh.deviceID, "error": err})
					}
					err = fmt.Errorf("aborted Onu SW upgrade but not automatically started, try again, device-id: %s", dh.deviceID)
					//TODO!!!: wait for 'ready' to start and configure - see above SetDownloadParams()
					// for now a second start of download should work again
				} else { //should never occur
					logger.Errorw(ctx, "onu upgrade fsm inconsistent setup", log.Fields{
						"device-id": dh.deviceID})
					err = fmt.Errorf("onu upgrade fsm inconsistent setup, baseFsm invalid for device-id: %s", dh.deviceID)
				}
			}
		} else {
			logger.Errorw(ctx, "start Onu SW upgrade rejected: no inactive image", log.Fields{
				"device-id": dh.deviceID, "error": err})
		}
	} else {
		logger.Errorw(ctx, "start Onu SW upgrade rejected: no active OMCI connection", log.Fields{"device-id": dh.deviceID})
		err = fmt.Errorf("start Onu SW upgrade rejected: no active OMCI connection for device-id: %s", dh.deviceID)
	}
	return err
}

//  deviceHandler methods that implement the adapters interface requests## end #########
// #####################################################################################

// ################  to be updated acc. needs of ONU Device ########################
// deviceHandler StateMachine related state transition methods ##### begin #########

func (dh *deviceHandler) logStateChange(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "Device FSM: ", log.Fields{"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst), "device-id": dh.deviceID})
}

// doStateInit provides the device update to the core
func (dh *deviceHandler) doStateInit(ctx context.Context, e *fsm.Event) {

	logger.Debug(ctx, "doStateInit-started")
	var err error

	// populate what we know.  rest comes later after mib sync
	dh.device.Root = false
	dh.device.Vendor = "OpenONU"
	dh.device.Model = "go"
	dh.device.Reason = deviceReasonMap[drActivatingOnu]
	dh.deviceReason = drActivatingOnu

	dh.logicalDeviceID = dh.deviceID // really needed - what for ??? //TODO!!!

	if !dh.isReconciling() {
		logger.Infow(ctx, "DeviceUpdate", log.Fields{"deviceReason": dh.device.Reason, "device-id": dh.deviceID})
		_ = dh.coreProxy.DeviceUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.device)
		//TODO Need to Update Device Reason To CORE as part of device update userstory
	} else {
		logger.Debugw(ctx, "reconciling - don't notify core about DeviceUpdate",
			log.Fields{"device-id": dh.deviceID})
	}

	dh.parentID = dh.device.ParentId
	dh.ponPortNumber = dh.device.ParentPortNo

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	logger.Debugw(ctx, "device-updated", log.Fields{"device-id": dh.deviceID, "proxyAddressID": dh.ProxyAddressID,
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
	if !dh.isReconciling() {
		logger.Debugw(ctx, "adding-pon-port", log.Fields{"device-id": dh.deviceID, "ponPortNo": dh.ponPortNumber})
		var ponPortNo uint32 = 1
		if dh.ponPortNumber != 0 {
			ponPortNo = dh.ponPortNumber
		}

		pPonPort := &voltha.Port{
			PortNo:     ponPortNo,
			Label:      fmt.Sprintf("pon-%d", ponPortNo),
			Type:       voltha.Port_PON_ONU,
			OperStatus: voltha.OperStatus_ACTIVE,
			Peers: []*voltha.Port_PeerPort{{DeviceId: dh.parentID, // Peer device  is OLT
				PortNo: ponPortNo}}, // Peer port is parent's port number
		}
		if err = dh.coreProxy.PortCreated(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, pPonPort); err != nil {
			logger.Fatalf(ctx, "Device FSM: PortCreated-failed-%s", err)
			e.Cancel(err)
			return
		}
	} else {
		logger.Debugw(ctx, "reconciling - pon-port already added", log.Fields{"device-id": dh.deviceID})
	}
	logger.Debug(ctx, "doStateInit-done")
}

// postInit setups the DeviceEntry for the conerned device
func (dh *deviceHandler) postInit(ctx context.Context, e *fsm.Event) {

	logger.Debug(ctx, "postInit-started")
	var err error
	/*
		dh.Client = oop.NewOpenoltClient(dh.clientCon)
		dh.pTransitionMap.Handle(ctx, GrpcConnected)
		return nil
	*/
	if err = dh.addOnuDeviceEntry(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		logger.Fatalf(ctx, "Device FSM: addOnuDeviceEntry-failed-%s", err)
		e.Cancel(err)
		return
	}

	if dh.isReconciling() {
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

			self.enabled = True
		else:
			self.logger.info('onu-already-activated')
	*/

	logger.Debug(ctx, "postInit-done")
}

// doStateConnected get the device info and update to voltha core
// for comparison of the original method (not that easy to uncomment): compare here:
//  voltha-openolt-adapter/adaptercore/device_handler.go
//  -> this one obviously initiates all communication interfaces of the device ...?
func (dh *deviceHandler) doStateConnected(ctx context.Context, e *fsm.Event) {

	logger.Debug(ctx, "doStateConnected-started")
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debug(ctx, "doStateConnected-done")
}

// doStateUp handle the onu up indication and update to voltha core
func (dh *deviceHandler) doStateUp(ctx context.Context, e *fsm.Event) {

	logger.Debug(ctx, "doStateUp-started")
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debug(ctx, "doStateUp-done")

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

	logger.Debug(ctx, "doStateDown-started")
	var err error

	device := dh.device
	if device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Errorw(ctx, "Failed to fetch handler device", log.Fields{"device-id": dh.deviceID})
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
			er := dh.AdapterProxy.SendInterAdapterMessage(ctx, &onuInd, ic.InterAdapterMessageType_ONU_IND_REQUEST,
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
	logger.Debug(ctx, "doStateDown-done")
}

// deviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// deviceHandler utility methods ##### begin #########

//getOnuDeviceEntry gets the ONU device entry and may wait until its value is defined
func (dh *deviceHandler) getOnuDeviceEntry(ctx context.Context, aWait bool) *OnuDeviceEntry {
	dh.lockDevice.RLock()
	pOnuDeviceEntry := dh.pOnuOmciDevice
	if aWait && pOnuDeviceEntry == nil {
		//keep the read sema short to allow for subsequent write
		dh.lockDevice.RUnlock()
		logger.Debugw(ctx, "Waiting for DeviceEntry to be set ...", log.Fields{"device-id": dh.deviceID})
		// based on concurrent processing the deviceEntry setup may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(60 * time.Second): //timer may be discussed ...
			logger.Errorw(ctx, "No valid DeviceEntry set after maxTime", log.Fields{"device-id": dh.deviceID})
			return nil
		case <-dh.deviceEntrySet:
			logger.Debugw(ctx, "devicEntry ready now - continue", log.Fields{"device-id": dh.deviceID})
			// if written now, we can return the written value without sema
			return dh.pOnuOmciDevice
		}
	}
	dh.lockDevice.RUnlock()
	return pOnuDeviceEntry
}

//setOnuDeviceEntry sets the ONU device entry within the handler
func (dh *deviceHandler) setOnuDeviceEntry(
	apDeviceEntry *OnuDeviceEntry, apOnuTp *onuUniTechProf, apOnuMetricsMgr *onuMetricsManager, apOnuAlarmMgr *onuAlarmManager) {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	dh.pOnuOmciDevice = apDeviceEntry
	dh.pOnuTP = apOnuTp
	dh.pOnuMetricsMgr = apOnuMetricsMgr
	dh.pAlarmMgr = apOnuAlarmMgr
}

//addOnuDeviceEntry creates a new ONU device or returns the existing
func (dh *deviceHandler) addOnuDeviceEntry(ctx context.Context) error {
	logger.Debugw(ctx, "adding-deviceEntry", log.Fields{"device-id": dh.deviceID})

	deviceEntry := dh.getOnuDeviceEntry(ctx, false)
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = newOnuDeviceEntry(ctx, dh)
		onuTechProfProc := newOnuUniTechProf(ctx, dh)
		onuMetricsMgr := newonuMetricsManager(ctx, dh)
		onuAlarmManager := newAlarmManager(ctx, dh)
		//error treatment possible //TODO!!!
		dh.setOnuDeviceEntry(deviceEntry, onuTechProfProc, onuMetricsMgr, onuAlarmManager)
		// fire deviceEntry ready event to spread to possibly waiting processing
		dh.deviceEntrySet <- true
		logger.Debugw(ctx, "onuDeviceEntry-added", log.Fields{"device-id": dh.deviceID})
	} else {
		logger.Debugw(ctx, "onuDeviceEntry-add: Device already exists", log.Fields{"device-id": dh.deviceID})
	}
	// might be updated with some error handling !!!
	return nil
}

func (dh *deviceHandler) createInterface(ctx context.Context, onuind *oop.OnuIndication) error {
	logger.Debugw(ctx, "create_interface-started", log.Fields{"OnuId": onuind.GetOnuId(),
		"OnuIntfId": onuind.GetIntfId(), "OnuSerialNumber": onuind.GetSerialNumber()})

	dh.pOnuIndication = onuind // let's revise if storing the pointer is sufficient...

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if !dh.isReconciling() {
		if err := dh.storePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue as there will be additional write attempts",
				log.Fields{"device-id": dh.deviceID, "err": err})
		}
		logger.Debugw(ctx, "call DeviceStateUpdate upon create interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVATING, "device-id": dh.deviceID})
		if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		}
	} else {
		logger.Debugw(ctx, "reconciling - don't notify core about DeviceStateUpdate to ACTIVATING",
			log.Fields{"device-id": dh.deviceID})

		if !pDevEntry.sOnuPersistentData.PersUniUnlockDone {
			logger.Debugw(ctx, "reconciling - uni-ports were not unlocked before adapter restart - resume with a normal start-up",
				log.Fields{"device-id": dh.deviceID})
			dh.stopReconciling(ctx)
		}
	}
	// It does not look to me as if makes sense to work with the real core device here, (not the stored clone)?
	// in this code the GetDevice would just make a check if the DeviceID's Device still exists in core
	// in python code it looks as the started onu_omci_device might have been updated with some new instance state of the core device
	// but I would not know why, and the go code anyway does not work with the device directly anymore in the OnuDeviceEntry
	// so let's just try to keep it simple ...
	/*
			device, err := dh.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx), dh.device.Id, dh.device.Id)
		    if err != nil || device == nil {
					//TODO: needs to handle error scenarios
				logger.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
				return errors.New("Voltha Device not found")
			}
	*/

	if err := pDevEntry.start(log.WithSpanFromContext(context.TODO(), ctx)); err != nil {
		return err
	}

	_ = dh.deviceReasonUpdate(ctx, drStartingOpenomci, !dh.isReconciling())

	/* this might be a good time for Omci Verify message?  */
	verifyExec := make(chan bool)
	omciVerify := newOmciTestRequest(log.WithSpanFromContext(context.TODO(), ctx),
		dh.device.Id, pDevEntry.PDevOmciCC,
		true, true) //exclusive and allowFailure (anyway not yet checked)
	omciVerify.performOmciTest(log.WithSpanFromContext(context.TODO(), ctx), verifyExec)

	/* 	give the handler some time here to wait for the OMCi verification result
	after Timeout start and try MibUpload FSM anyway
	(to prevent stopping on just not supported OMCI verification from ONU) */
	select {
	case <-time.After(2 * time.Second):
		logger.Warn(ctx, "omci start-verification timed out (continue normal)")
	case testresult := <-verifyExec:
		logger.Infow(ctx, "Omci start verification done", log.Fields{"result": testresult})
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
	//self._heartbeat.enabled = True

	/* Note: Even though FSM calls look 'synchronous' here, FSM is running in background with the effect that possible errors
	 * 	 within the MibUpload are not notified in the OnuIndication response, this might be acceptable here,
	 *   as further OltAdapter processing may rely on the deviceReason event 'MibUploadDone' as a result of the FSM processing
	 *   otherwise some processing synchronization would be required - cmp. e.g TechProfile processing
	 */
	//call MibUploadFSM - transition up to state ulStInSync
	pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		if pMibUlFsm.Is(ulStDisabled) {
			if err := pMibUlFsm.Event(ulEvStart); err != nil {
				logger.Errorw(ctx, "MibSyncFsm: Can't go to state starting", log.Fields{"device-id": dh.deviceID, "err": err})
				return fmt.Errorf("can't go to state starting: %s", dh.deviceID)
			}
			logger.Debugw(ctx, "MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
			//Determine ONU status and start/re-start MIB Synchronization tasks
			//Determine if this ONU has ever synchronized
			if pDevEntry.isNewOnu() {
				if err := pMibUlFsm.Event(ulEvResetMib); err != nil {
					logger.Errorw(ctx, "MibSyncFsm: Can't go to state resetting_mib", log.Fields{"device-id": dh.deviceID, "err": err})
					return fmt.Errorf("can't go to state resetting_mib: %s", dh.deviceID)
				}
			} else {
				if err := pMibUlFsm.Event(ulEvExamineMds); err != nil {
					logger.Errorw(ctx, "MibSyncFsm: Can't go to state examine_mds", log.Fields{"device-id": dh.deviceID, "err": err})
					return fmt.Errorf("can't go to examine_mds: %s", dh.deviceID)
				}
				logger.Debugw(ctx, "state of MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
			}
		} else {
			logger.Errorw(ctx, "wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(pMibUlFsm.Current()),
				"device-id": dh.deviceID})
			return fmt.Errorf("wrong state of MibSyncFsm: %s", dh.deviceID)
		}
	} else {
		logger.Errorw(ctx, "MibSyncFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("can't execute MibSync: %s", dh.deviceID)
	}

	if !dh.getCollectorIsRunning() {
		// Start PM collector routine
		go dh.startCollector(ctx)
	}
	if !dh.getAlarmManagerIsRunning() {
		go dh.startAlarmManager(ctx)
	}

	return nil
}

func (dh *deviceHandler) updateInterface(ctx context.Context, onuind *oop.OnuIndication) error {
	//state checking to prevent unneeded processing (eg. on ONU 'unreachable' and 'down')
	// (but note that the deviceReason may also have changed to e.g. TechProf*Delete_Success in between)
	if dh.deviceReason != drStoppingOpenomci {
		logger.Debugw(ctx, "updateInterface-started - stopping-device", log.Fields{"device-id": dh.deviceID})

		//stop all running FSM processing - make use of the DH-state as mirrored in the deviceReason
		//here no conflict with aborted FSM's should arise as a complete OMCI initialization is assumed on ONU-Up
		//but that might change with some simple MDS check on ONU-Up treatment -> attention!!!
		if err := dh.resetFsms(ctx, true); err != nil {
			logger.Errorw(ctx, "error-updateInterface at FSM stop",
				log.Fields{"device-id": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
		//all stored persistent data are not valid anymore (loosing knowledge about the connected ONU)
		_ = dh.deleteDevicePersistencyData(ctx) //ignore possible errors here and continue, hope is that data is synchronized with new ONU-Up

		//deviceEntry stop without omciCC reset here, regarding the OMCI_CC still valid for this ONU
		// - in contrary to disableDevice - compare with processUniDisableStateDoneEvent
		//stop the device entry which resets the attached omciCC
		pDevEntry := dh.getOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
			return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
		}
		_ = pDevEntry.stop(log.WithSpanFromContext(context.TODO(), ctx), false)

		//TODO!!! remove existing traffic profiles
		/* from py code, if TP's exist, remove them - not yet implemented
		self._tp = dict()
		# Let TP download happen again
		for uni_id in self._tp_service_specific_task:
			self._tp_service_specific_task[uni_id].clear()
		for uni_id in self._tech_profile_download_done:
			self._tech_profile_download_done[uni_id].clear()
		*/

		dh.disableUniPortStateUpdate(ctx)

		dh.ReadyForSpecificOmciConfig = false

		if err := dh.deviceReasonUpdate(ctx, drStoppingOpenomci, true); err != nil {
			// abort: system behavior is just unstable ...
			return err
		}
		logger.Debugw(ctx, "call DeviceStateUpdate upon update interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_UNREACHABLE,
			"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.deviceID})
		if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID,
			voltha.ConnectStatus_UNREACHABLE, voltha.OperStatus_DISCOVERED); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state unreachable-discovered",
				log.Fields{"device-id": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
	} else {
		logger.Debugw(ctx, "updateInterface - device already stopped", log.Fields{"device-id": dh.deviceID})
	}
	return nil
}

func (dh *deviceHandler) resetFsms(ctx context.Context, includingMibSyncFsm bool) error {
	//all possible FSM's are stopped or reset here to ensure their transition to 'disabled'
	//it is not sufficient to stop/reset the latest running FSM as done in previous versions
	//  as after down/up procedures all FSM's might be active/ongoing (in theory)
	//  and using the stop/reset event should never harm

	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if includingMibSyncFsm {
		//the MibSync FSM might be active all the ONU-active time,
		// hence it must be stopped unconditionally
		pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
		if pMibUlFsm != nil {
			_ = pMibUlFsm.Event(ulEvStop) //TODO!! verify if MibSyncFsm stop-processing is sufficient (to allow it again afterwards)
		}
	}
	//MibDownload may run
	pMibDlFsm := pDevEntry.pMibDownloadFsm.pFsm
	if pMibDlFsm != nil {
		_ = pMibDlFsm.Event(dlEvReset)
	}
	//port lock/unlock FSM's may be active
	if dh.pUnlockStateFsm != nil {
		_ = dh.pUnlockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
	}
	if dh.pLockStateFsm != nil {
		_ = dh.pLockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
	}
	//techProfile related PonAniConfigFsm FSM may be active
	if dh.pOnuTP != nil {
		// should always be the case here
		// FSM  stop maybe encapsulated as OnuTP method - perhaps later in context of module splitting
		if dh.pOnuTP.pAniConfigFsm != nil {
			for uniTP := range dh.pOnuTP.pAniConfigFsm {
				_ = dh.pOnuTP.pAniConfigFsm[uniTP].pAdaptFsm.pFsm.Event(aniEvReset)
			}
		}
		for _, uniPort := range dh.uniEntityMap {
			// reset the possibly existing VlanConfigFsm
			dh.lockVlanConfig.RLock()
			if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[uniPort.uniID]; exist {
				dh.lockVlanConfig.RUnlock()
				//VlanFilterFsm exists and was already started
				pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
				if pVlanFilterStatemachine != nil {
					//reset of all Fsm is always accompanied by global persistency data removal
					//  no need to remove specific data
					pVlanFilterFsm.RequestClearPersistency(false)
					//and reset the UniVlanConfig FSM
					_ = pVlanFilterStatemachine.Event(vlanEvReset)
				}
			} else {
				dh.lockVlanConfig.RUnlock()
			}
		}
	}
	if dh.getCollectorIsRunning() {
		// Stop collector routine
		dh.stopCollector <- true
	}
	if dh.getAlarmManagerIsRunning() {
		dh.stopAlarmManager <- true
	}

	//reset a possibly running upgrade FSM
	// specific here: If the FSM is in upgradeStWaitForCommit, it is left there for possibly later commit
	// this possibly also refers later to (not yet existing) upgradeStWaitForActivate (with ctl API changes)
	dh.lockUpgradeFsm.RLock()
	if dh.pOnuUpradeFsm != nil {
		_ = dh.pOnuUpradeFsm.pAdaptFsm.pFsm.Event(upgradeEvReset)
	}
	dh.lockUpgradeFsm.RUnlock()

	return nil
}

func (dh *deviceHandler) processMibDatabaseSyncEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "MibInSync event received, adding uni ports and locking the ONU interfaces", log.Fields{"device-id": dh.deviceID})

	_ = dh.deviceReasonUpdate(ctx, drDiscoveryMibsyncComplete, !dh.isReconciling())
	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	i := uint8(0) //UNI Port limit: see MaxUnisPerOnu (by now 16) (OMCI supports max 255 p.b.)
	if pptpInstKeys := pDevEntry.pOnuDB.getSortedInstKeys(
		ctx, me.PhysicalPathTerminationPointEthernetUniClassID); len(pptpInstKeys) > 0 {
		for _, mgmtEntityID := range pptpInstKeys {
			logger.Debugw(ctx, "Add PPTPEthUni port for MIB-stored instance:", log.Fields{
				"device-id": dh.deviceID, "PPTPEthUni EntityID": mgmtEntityID})
			dh.addUniPort(ctx, mgmtEntityID, i, uniPPTP)
			i++
		}
	} else {
		logger.Debugw(ctx, "No UniG instances found", log.Fields{"device-id": dh.deviceID})
	}
	if veipInstKeys := pDevEntry.pOnuDB.getSortedInstKeys(
		ctx, me.VirtualEthernetInterfacePointClassID); len(veipInstKeys) > 0 {
		for _, mgmtEntityID := range veipInstKeys {
			logger.Debugw(ctx, "Add VEIP for MIB-stored instance:", log.Fields{
				"device-id": dh.deviceID, "VEIP EntityID": mgmtEntityID})
			dh.addUniPort(ctx, mgmtEntityID, i, uniVEIP)
			i++
		}
	} else {
		logger.Debugw(ctx, "No VEIP instances found", log.Fields{"device-id": dh.deviceID})
	}
	if i == 0 {
		logger.Warnw(ctx, "No PPTP instances found", log.Fields{"device-id": dh.deviceID})
	}
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
		dh.createUniLockFsm(ctx, true, UniLockStateDone)
	} else { //LockStateFSM already init
		dh.pLockStateFsm.setSuccessEvent(UniLockStateDone)
		dh.runUniLockFsm(ctx, true)
	}
}

func (dh *deviceHandler) processUniLockStateDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Infow(ctx, "UniLockStateDone event: Starting MIB download", log.Fields{"device-id": dh.deviceID})
	/*  Mib download procedure -
	***** should run over 'downloaded' state and generate MibDownloadDone event *****
	 */
	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pMibDlFsm := pDevEntry.pMibDownloadFsm.pFsm
	if pMibDlFsm != nil {
		if pMibDlFsm.Is(dlStDisabled) {
			if err := pMibDlFsm.Event(dlEvStart); err != nil {
				logger.Errorw(ctx, "MibDownloadFsm: Can't go to state starting", log.Fields{"device-id": dh.deviceID, "err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				logger.Debugw(ctx, "MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
				// maybe use more specific states here for the specific download steps ...
				if err := pMibDlFsm.Event(dlEvCreateGal); err != nil {
					logger.Errorw(ctx, "MibDownloadFsm: Can't start CreateGal", log.Fields{"device-id": dh.deviceID, "err": err})
				} else {
					logger.Debugw(ctx, "state of MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
					//Begin MIB data download (running autonomously)
				}
			}
		} else {
			logger.Errorw(ctx, "wrong state of MibDownloadFsm - want: disabled", log.Fields{"have": string(pMibDlFsm.Current()),
				"device-id": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
		/***** Mib download started */
	} else {
		logger.Errorw(ctx, "MibDownloadFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
	}
}

func (dh *deviceHandler) processMibDownloadDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "MibDownloadDone event received, unlocking the ONU interfaces", log.Fields{"device-id": dh.deviceID})
	//initiate DevStateUpdate
	if !dh.isReconciling() {
		logger.Debugw(ctx, "call DeviceStateUpdate upon mib-download done", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.deviceID})
		//we allow a possible OnuSw image commit only in the normal startup, not at reconciling
		// in case of adapter restart connected to an ONU upgrade I would not rely on the image quality
		// maybe some 'forced' commitment can be done in this situation from system management (or upgrade restarted)
		dh.checkOnOnuImageCommit(ctx)
		if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVE); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		} else {
			logger.Debugw(ctx, "dev state updated to 'Oper.Active'", log.Fields{"device-id": dh.deviceID})
		}
	} else {
		pDevEntry := dh.getOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
			return
		}
		if pDevEntry.sOnuPersistentData.PersUniDisableDone {
			logger.Debugw(ctx, "reconciling - uni-ports were disabled by admin before adapter restart - keep the ports locked and wait for re-enabling",
				log.Fields{"device-id": dh.deviceID})
			dh.stopReconciling(ctx)
			return
		}
		logger.Debugw(ctx, "reconciling - don't notify core about DeviceStateUpdate to ACTIVE",
			log.Fields{"device-id": dh.deviceID})
	}
	_ = dh.deviceReasonUpdate(ctx, drInitialMibDownloaded, !dh.isReconciling())

	// Initialize classical L2 PM Interval Counters
	if err := dh.pOnuMetricsMgr.pAdaptFsm.pFsm.Event(l2PmEventInit); err != nil {
		// There is no way we should be landing here, but if we do then
		// there is nothing much we can do about this other than log error
		logger.Errorw(ctx, "error starting l2 pm fsm", log.Fields{"device-id": dh.device.Id, "err": err})
	}

	dh.ReadyForSpecificOmciConfig = true
	// *** should generate UniUnlockStateDone event *****
	if dh.pUnlockStateFsm == nil {
		dh.createUniLockFsm(ctx, false, UniUnlockStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.setSuccessEvent(UniUnlockStateDone)
		dh.runUniLockFsm(ctx, false)
	}
}

func (dh *deviceHandler) processUniUnlockStateDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	dh.enableUniPortStateUpdate(ctx) //cmp python yield self.enable_ports()

	if !dh.isReconciling() {
		logger.Infow(ctx, "UniUnlockStateDone event: Sending OnuUp event", log.Fields{"device-id": dh.deviceID})
		raisedTs := time.Now().UnixNano()
		go dh.sendOnuOperStateEvent(ctx, voltha.OperStatus_ACTIVE, dh.deviceID, raisedTs) //cmp python onu_active_event
		pDevEntry := dh.getOnuDeviceEntry(ctx, false)
		if pDevEntry == nil {
			logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
			return
		}
		pDevEntry.sOnuPersistentData.PersUniUnlockDone = true
		if err := dh.storePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
				log.Fields{"device-id": dh.deviceID, "err": err})
		}
	} else {
		logger.Debugw(ctx, "reconciling - don't notify core that onu went to active but trigger tech profile config",
			log.Fields{"device-id": dh.deviceID})
		go dh.reconcileDeviceTechProf(ctx)
		// reconcilement will be continued after ani config is done
	}
}

func (dh *deviceHandler) processUniDisableStateDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "DeviceStateUpdate upon disable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx),
		dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_UNKNOWN); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}

	logger.Debugw(ctx, "DeviceReasonUpdate upon disable", log.Fields{"reason": deviceReasonMap[drOmciAdminLock], "device-id": dh.deviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	_ = dh.deviceReasonUpdate(ctx, drOmciAdminLock, true)

	//transfer the modified logical uni port state
	dh.disableUniPortStateUpdate(ctx)

	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pDevEntry.sOnuPersistentData.PersUniDisableDone = true
	if err := dh.storePersistentData(ctx); err != nil {
		logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
			log.Fields{"device-id": dh.deviceID, "err": err})
	}
}

func (dh *deviceHandler) processUniEnableStateDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "DeviceStateUpdate upon re-enable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, voltha.ConnectStatus_REACHABLE,
		voltha.OperStatus_ACTIVE); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw(ctx, "error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}

	logger.Debugw(ctx, "DeviceReasonUpdate upon re-enable", log.Fields{
		"reason": deviceReasonMap[drOnuReenabled], "device-id": dh.deviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	_ = dh.deviceReasonUpdate(ctx, drOnuReenabled, true)

	//transfer the modified logical uni port state
	dh.enableUniPortStateUpdate(ctx)

	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pDevEntry.sOnuPersistentData.PersUniDisableDone = false
	if err := dh.storePersistentData(ctx); err != nil {
		logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
			log.Fields{"device-id": dh.deviceID, "err": err})
	}
}

func (dh *deviceHandler) processOmciAniConfigDoneEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	if devEvent == OmciAniConfigDone {
		logger.Debugw(ctx, "OmciAniConfigDone event received", log.Fields{"device-id": dh.deviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.deviceReason != drTechProfileConfigDownloadSuccess {
			// which may be the case from some previous actvity even on this UNI Port (but also other UNI ports)
			_ = dh.deviceReasonUpdate(ctx, drTechProfileConfigDownloadSuccess, !dh.isReconciling())
		}
		if dh.isReconciling() {
			go dh.reconcileDeviceFlowConfig(ctx)
		}
	} else { // should be the OmciAniResourceRemoved block
		logger.Debugw(ctx, "OmciAniResourceRemoved event received", log.Fields{"device-id": dh.deviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.deviceReason != drTechProfileConfigDeleteSuccess {
			// which may be the case from some previous actvity even on this ONU port (but also other UNI ports)
			_ = dh.deviceReasonUpdate(ctx, drTechProfileConfigDeleteSuccess, true)
		}
	}
}

func (dh *deviceHandler) processOmciVlanFilterDoneEvent(ctx context.Context, aDevEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "OmciVlanFilterDone event received",
		log.Fields{"device-id": dh.deviceID, "event": aDevEvent})
	// attention: the device reason update is done based on ONU-UNI-Port related activity
	//  - which may cause some inconsistency

	if aDevEvent == OmciVlanFilterAddDone || aDevEvent == OmciVlanFilterAddDoneNoKvStore {
		if dh.deviceReason != drOmciFlowsPushed {
			// which may be the case from some previous actvity on another UNI Port of the ONU
			// or even some previous flow add activity on the same port
			_ = dh.deviceReasonUpdate(ctx, drOmciFlowsPushed, !dh.isReconciling())
			if dh.isReconciling() {
				go dh.reconcileMetrics(ctx)
			}
		}
	} else {
		if dh.deviceReason != drOmciFlowsDeleted {
			//not relevant for reconcile
			_ = dh.deviceReasonUpdate(ctx, drOmciFlowsDeleted, true)
		}
	}

	if aDevEvent == OmciVlanFilterAddDone || aDevEvent == OmciVlanFilterRemDone {
		//events that request KvStore write
		if err := dh.storePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
				log.Fields{"device-id": dh.deviceID, "err": err})
		}
	} else {
		logger.Debugw(ctx, "OmciVlanFilter*Done* - write to KvStore not requested",
			log.Fields{"device-id": dh.deviceID})
	}
}

//deviceProcStatusUpdate evaluates possible processing events and initiates according next activities
func (dh *deviceHandler) deviceProcStatusUpdate(ctx context.Context, devEvent OnuDeviceEvent) {
	switch devEvent {
	case MibDatabaseSync:
		{
			dh.processMibDatabaseSyncEvent(ctx, devEvent)
		}
	case UniLockStateDone:
		{
			dh.processUniLockStateDoneEvent(ctx, devEvent)
		}
	case MibDownloadDone:
		{
			dh.processMibDownloadDoneEvent(ctx, devEvent)
		}
	case UniUnlockStateDone:
		{
			dh.processUniUnlockStateDoneEvent(ctx, devEvent)
		}
	case UniEnableStateDone:
		{
			dh.processUniEnableStateDoneEvent(ctx, devEvent)
		}
	case UniDisableStateDone:
		{
			dh.processUniDisableStateDoneEvent(ctx, devEvent)
		}
	case OmciAniConfigDone, OmciAniResourceRemoved:
		{
			dh.processOmciAniConfigDoneEvent(ctx, devEvent)
		}
	case OmciVlanFilterAddDone, OmciVlanFilterAddDoneNoKvStore, OmciVlanFilterRemDone, OmciVlanFilterRemDoneNoKvStore:
		{
			dh.processOmciVlanFilterDoneEvent(ctx, devEvent)
		}
	default:
		{
			logger.Debugw(ctx, "unhandled-device-event", log.Fields{"device-id": dh.deviceID, "event": devEvent})
		}
	} //switch
}

func (dh *deviceHandler) addUniPort(ctx context.Context, aUniInstNo uint16, aUniID uint8, aPortType uniPortType) {
	// parameters are IntfId, OnuId, uniId
	uniNo := mkUniPortNum(ctx, dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(),
		uint32(aUniID))
	if _, present := dh.uniEntityMap[uniNo]; present {
		logger.Warnw(ctx, "onuUniPort-add: Port already exists", log.Fields{"for InstanceId": aUniInstNo})
	} else {
		//with arguments aUniID, a_portNo, aPortType
		pUniPort := newOnuUniPort(ctx, aUniID, uniNo, aUniInstNo, aPortType)
		if pUniPort == nil {
			logger.Warnw(ctx, "onuUniPort-add: Could not create Port", log.Fields{"for InstanceId": aUniInstNo})
		} else {
			//store UniPort with the System-PortNumber key
			dh.uniEntityMap[uniNo] = pUniPort
			if !dh.isReconciling() {
				// create announce the UniPort to the core as VOLTHA Port object
				if err := pUniPort.createVolthaPort(ctx, dh); err == nil {
					logger.Infow(ctx, "onuUniPort-added", log.Fields{"for PortNo": uniNo})
				} //error logging already within UniPort method
			} else {
				logger.Debugw(ctx, "reconciling - onuUniPort already added", log.Fields{"for PortNo": uniNo, "device-id": dh.deviceID})
			}
		}
	}
}

// enableUniPortStateUpdate enables UniPortState and update core port state accordingly
func (dh *deviceHandler) enableUniPortStateUpdate(ctx context.Context) {
	//  py code was updated 2003xx to activate the real ONU UNI ports per OMCI (VEIP or PPTP)
	//    but towards core only the first port active state is signaled
	//    with following remark:
	//       # TODO: for now only support the first UNI given no requirement for multiple uni yet. Also needed to reduce flow
	//       #  load on the core

	// lock_ports(false) as done in py code here is shifted to separate call from devicevent processing

	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniID)&activeUniPortStateUpdateMask == (1 << uniPort.uniID) {
			logger.Infow(ctx, "onuUniPort-forced-OperState-ACTIVE", log.Fields{"for PortNo": uniNo})
			uniPort.setOperState(vc.OperStatus_ACTIVE)
			if !dh.isReconciling() {
				//maybe also use getter functions on uniPort - perhaps later ...
				go dh.coreProxy.PortStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
			} else {
				//TODO there is no retry mechanism, return error
				logger.Debugw(ctx, "reconciling - don't notify core about PortStateUpdate", log.Fields{"device-id": dh.deviceID})
			}
		}
	}
}

// Disable UniPortState and update core port state accordingly
func (dh *deviceHandler) disableUniPortStateUpdate(ctx context.Context) {
	// compare enableUniPortStateUpdate() above
	//   -> use current restriction to operate only on first UNI port as inherited from actual Py code
	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniID)&activeUniPortStateUpdateMask == (1 << uniPort.uniID) {
			logger.Infow(ctx, "onuUniPort-forced-OperState-UNKNOWN", log.Fields{"for PortNo": uniNo})
			uniPort.setOperState(vc.OperStatus_UNKNOWN)
			//maybe also use getter functions on uniPort - perhaps later ...
			go dh.coreProxy.PortStateUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
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
	parentDevice, err := dh.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx), dh.parentID, dh.parentID)
	if err != nil || parentDevice == nil {
		logger.Errorw(ctx, "Failed to fetch parent device for OnuEvent",
			log.Fields{"parentID": dh.parentID, "err": err})
	}
	oltSerialNumber := parentDevice.SerialNumber

	eventContext["pon-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.IntfId), 10)
	eventContext["onu-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.OnuId), 10)
	eventContext["serial-number"] = dh.device.SerialNumber
	eventContext["olt-serial-number"] = oltSerialNumber
	eventContext["device-id"] = aDeviceID
	eventContext["registration-id"] = aDeviceID //py: string(device_id)??
	logger.Debugw(ctx, "prepare ONU_ACTIVATED event",
		log.Fields{"device-id": aDeviceID, "EventContext": eventContext})

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
	if err := dh.EventProxy.SendDeviceEvent(ctx, &de, equipment, pon, raisedTs); err != nil {
		logger.Warnw(ctx, "could not send ONU_ACTIVATED event",
			log.Fields{"device-id": aDeviceID, "error": err})
	}
	logger.Debugw(ctx, "ctx, ONU_ACTIVATED event sent to KAFKA",
		log.Fields{"device-id": aDeviceID, "with-EventName": de.DeviceEventName})
}

// createUniLockFsm initializes and runs the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) createUniLockFsm(ctx context.Context, aAdminState bool, devEvent OnuDeviceEvent) {
	chLSFsm := make(chan Message, 2048)
	var sFsmName string
	if aAdminState {
		logger.Debugw(ctx, "createLockStateFSM", log.Fields{"device-id": dh.deviceID})
		sFsmName = "LockStateFSM"
	} else {
		logger.Debugw(ctx, "createUnlockStateFSM", log.Fields{"device-id": dh.deviceID})
		sFsmName = "UnLockStateFSM"
	}

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pLSFsm := newLockStateFsm(ctx, pDevEntry.PDevOmciCC, aAdminState, devEvent,
		sFsmName, dh, chLSFsm)
	if pLSFsm != nil {
		if aAdminState {
			dh.pLockStateFsm = pLSFsm
		} else {
			dh.pUnlockStateFsm = pLSFsm
		}
		dh.runUniLockFsm(ctx, aAdminState)
	} else {
		logger.Errorw(ctx, "LockStateFSM could not be created - abort!!", log.Fields{"device-id": dh.deviceID})
	}
}

// runUniLockFsm starts the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) runUniLockFsm(ctx context.Context, aAdminState bool) {
	/*  Uni Port lock/unlock procedure -
	 ***** should run via 'adminDone' state and generate the argument requested event *****
	 */
	var pLSStatemachine *fsm.FSM
	if aAdminState {
		pLSStatemachine = dh.pLockStateFsm.pAdaptFsm.pFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pUnlockStateFsm != nil) &&
			(dh.pUnlockStateFsm.pAdaptFsm.pFsm.Current() != uniStDisabled) {
			_ = dh.pUnlockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
		}
	} else {
		pLSStatemachine = dh.pUnlockStateFsm.pAdaptFsm.pFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pLockStateFsm != nil) &&
			(dh.pLockStateFsm.pAdaptFsm.pFsm.Current() != uniStDisabled) {
			_ = dh.pLockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
		}
	}
	if pLSStatemachine != nil {
		if pLSStatemachine.Is(uniStDisabled) {
			if err := pLSStatemachine.Event(uniEvStart); err != nil {
				logger.Warnw(ctx, "LockStateFSM: can't start", log.Fields{"err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				/***** LockStateFSM started */
				logger.Debugw(ctx, "LockStateFSM started", log.Fields{
					"state": pLSStatemachine.Current(), "device-id": dh.deviceID})
			}
		} else {
			logger.Warnw(ctx, "wrong state of LockStateFSM - want: disabled", log.Fields{
				"have": pLSStatemachine.Current(), "device-id": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
	} else {
		logger.Errorw(ctx, "LockStateFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
		// maybe try a FSM reset and then again ... - TODO!!!
	}
}

// createOnuUpgradeFsm initializes and runs the Onu Software upgrade FSM
func (dh *deviceHandler) createOnuUpgradeFsm(ctx context.Context, apDevEntry *OnuDeviceEntry, aDevEvent OnuDeviceEvent) error {
	//in here lockUpgradeFsm is already locked
	chUpgradeFsm := make(chan Message, 2048)
	var sFsmName = "OnuSwUpgradeFSM"
	logger.Debugw(ctx, "create OnuSwUpgradeFSM", log.Fields{"device-id": dh.deviceID})
	if apDevEntry.PDevOmciCC == nil {
		logger.Errorw(ctx, "no valid OnuDevice or omciCC - abort", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf(fmt.Sprintf("no valid omciCC - abort for device-id: %s", dh.device.Id))
	}
	dh.pOnuUpradeFsm = NewOnuUpgradeFsm(ctx, dh, apDevEntry, apDevEntry.pOnuDB, aDevEvent,
		sFsmName, chUpgradeFsm)
	if dh.pOnuUpradeFsm != nil {
		pUpgradeStatemachine := dh.pOnuUpradeFsm.pAdaptFsm.pFsm
		if pUpgradeStatemachine != nil {
			if pUpgradeStatemachine.Is(upgradeStDisabled) {
				if err := pUpgradeStatemachine.Event(upgradeEvStart); err != nil {
					logger.Errorw(ctx, "OnuSwUpgradeFSM: can't start", log.Fields{"err": err})
					// maybe try a FSM reset and then again ... - TODO!!!
					return fmt.Errorf(fmt.Sprintf("OnuSwUpgradeFSM could not be started for device-id: %s", dh.device.Id))
				}
				/***** LockStateFSM started */
				logger.Debugw(ctx, "OnuSwUpgradeFSM started", log.Fields{
					"state": pUpgradeStatemachine.Current(), "device-id": dh.deviceID})
			} else {
				logger.Errorw(ctx, "wrong state of OnuSwUpgradeFSM to start - want: disabled", log.Fields{
					"have": pUpgradeStatemachine.Current(), "device-id": dh.deviceID})
				// maybe try a FSM reset and then again ... - TODO!!!
				return fmt.Errorf(fmt.Sprintf("OnuSwUpgradeFSM could not be started for device-id: %s, wrong internal state", dh.device.Id))
			}
		} else {
			logger.Errorw(ctx, "OnuSwUpgradeFSM internal FSM invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
			return fmt.Errorf(fmt.Sprintf("OnuSwUpgradeFSM internal FSM could not be created for device-id: %s", dh.device.Id))
		}
	} else {
		logger.Errorw(ctx, "OnuSwUpgradeFSM could not be created  - abort", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf(fmt.Sprintf("OnuSwUpgradeFSM could not be created - abort for device-id: %s", dh.device.Id))
	}
	return nil
}

// removeOnuUpgradeFsm clears the Onu Software upgrade FSM
func (dh *deviceHandler) removeOnuUpgradeFsm(ctx context.Context) {
	logger.Debugw(ctx, "remove OnuSwUpgradeFSM StateMachine", log.Fields{
		"device-id": dh.deviceID})
	dh.lockUpgradeFsm.Lock()
	defer dh.lockUpgradeFsm.Unlock()
	dh.pOnuUpradeFsm = nil //resource clearing is left to garbage collector
}

// checkOnOnuImageCommit verifies if the ONU is in some upgrade state that allows for image commit and if tries to commit
func (dh *deviceHandler) checkOnOnuImageCommit(ctx context.Context) {
	pDevEntry := dh.getOnuDeviceEntry(ctx, false)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting checkOnOnuImageCommit", log.Fields{"device-id": dh.deviceID})
		return
	}

	dh.lockUpgradeFsm.RLock()
	defer dh.lockUpgradeFsm.RUnlock()
	if dh.pOnuUpradeFsm != nil {
		pUpgradeStatemachine := dh.pOnuUpradeFsm.pAdaptFsm.pFsm
		if pUpgradeStatemachine != nil {
			// commit is only processed in case out upgrade FSM indicates the according state (for automatic commit)
			//  (some manual forced commit could do without)
			if pUpgradeStatemachine.Is(upgradeStWaitForCommit) {
				forcedTest := true //TODO!!: needed as long as BBSIM does not fully support SW upgrade simulation
				if forcedTest || pDevEntry.IsImageToBeCommitted(ctx, dh.pOnuUpradeFsm.inactiveImageMeID) {
					if err := pUpgradeStatemachine.Event(upgradeEvCommitSw); err != nil {
						logger.Errorw(ctx, "OnuSwUpgradeFSM: can't call commit event", log.Fields{"err": err})
						return
					}
					logger.Debugw(ctx, "OnuSwUpgradeFSM commit image requested", log.Fields{
						"state": pUpgradeStatemachine.Current(), "device-id": dh.deviceID})
				} else {
					logger.Errorw(ctx, "OnuSwUpgradeFSM waiting to commit, but nothing to commit on ONU - abort upgrade",
						log.Fields{"device-id": dh.deviceID})
					_ = pUpgradeStatemachine.Event(upgradeEvAbort)
					return
				}
			}
		}
	} else {
		logger.Debugw(ctx, "no ONU image to be committed", log.Fields{"device-id": dh.deviceID})
	}
}

//setBackend provides a DB backend for the specified path on the existing KV client
func (dh *deviceHandler) setBackend(ctx context.Context, aBasePathKvStore string) *db.Backend {

	logger.Debugw(ctx, "SetKVStoreBackend", log.Fields{"IpTarget": dh.pOpenOnuAc.KVStoreAddress,
		"BasePathKvStore": aBasePathKvStore, "device-id": dh.deviceID})
	kvbackend := &db.Backend{
		Client:    dh.pOpenOnuAc.kvClient,
		StoreType: dh.pOpenOnuAc.KVStoreType,
		/* address config update acc. to [VOL-2736] */
		Address:    dh.pOpenOnuAc.KVStoreAddress,
		Timeout:    dh.pOpenOnuAc.KVStoreTimeout,
		PathPrefix: aBasePathKvStore}

	return kvbackend
}
func (dh *deviceHandler) getFlowOfbFields(ctx context.Context, apFlowItem *ofp.OfpFlowStats, loMatchVlan *uint16,
	loAddPcp *uint8, loIPProto *uint32) {

	for _, field := range flow.GetOfbFields(apFlowItem) {
		switch field.Type {
		case of.OxmOfbFieldTypes_OFPXMT_OFB_ETH_TYPE:
			{
				logger.Debugw(ctx, "flow type EthType", log.Fields{"device-id": dh.deviceID,
					"EthType": strconv.FormatInt(int64(field.GetEthType()), 16)})
			}
		/* TT related temporary workaround - should not be needed anymore
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IP_PROTO:
			{
				*loIPProto = field.GetIpProto()
				logger.Debugw("flow type IpProto", log.Fields{"device-id": dh.deviceID,
					"IpProto": strconv.FormatInt(int64(*loIPProto), 16)})
				if *loIPProto == 2 {
					// some workaround for TT workflow at proto == 2 (IGMP trap) -> ignore the flow
					// avoids installing invalid EVTOCD rule
					logger.Debugw("flow type IpProto 2: TT workaround: ignore flow",
						log.Fields{"device-id": dh.deviceID})
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
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"VID": strconv.FormatInt(int64(*loMatchVlan), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP:
			{
				*loAddPcp = uint8(field.GetVlanPcp())
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"PCP": loAddPcp})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_DST:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"UDP-DST": strconv.FormatInt(int64(field.GetUdpDst()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_SRC:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"UDP-SRC": strconv.FormatInt(int64(field.GetUdpSrc()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_DST:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"IPv4-DST": field.GetIpv4Dst()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_SRC:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
					"IPv4-SRC": field.GetIpv4Src()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_METADATA:
			{
				logger.Debugw(ctx, "flow field type", log.Fields{"device-id": dh.deviceID,
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

func (dh *deviceHandler) getFlowActions(ctx context.Context, apFlowItem *ofp.OfpFlowStats, loSetPcp *uint8, loSetVlan *uint16) {
	for _, action := range flow.GetActions(apFlowItem) {
		switch action.Type {
		/* not used:
		case of.OfpActionType_OFPAT_OUTPUT:
			{
				logger.Debugw("flow action type", log.Fields{"device-id": dh.deviceID,
					"Output": action.GetOutput()})
			}
		*/
		case of.OfpActionType_OFPAT_PUSH_VLAN:
			{
				logger.Debugw(ctx, "flow action type", log.Fields{"device-id": dh.deviceID,
					"PushEthType": strconv.FormatInt(int64(action.GetPush().Ethertype), 16)})
			}
		case of.OfpActionType_OFPAT_SET_FIELD:
			{
				pActionSetField := action.GetSetField()
				if pActionSetField.Field.OxmClass != of.OfpOxmClass_OFPXMC_OPENFLOW_BASIC {
					logger.Warnw(ctx, "flow action SetField invalid OxmClass (ignored)", log.Fields{"device-id": dh.deviceID,
						"OxcmClass": pActionSetField.Field.OxmClass})
				}
				if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_VID {
					*loSetVlan = uint16(pActionSetField.Field.GetOfbField().GetVlanVid())
					logger.Debugw(ctx, "flow Set VLAN from SetField action", log.Fields{"device-id": dh.deviceID,
						"SetVlan": strconv.FormatInt(int64(*loSetVlan), 16)})
				} else if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP {
					*loSetPcp = uint8(pActionSetField.Field.GetOfbField().GetVlanPcp())
					logger.Debugw(ctx, "flow Set PCP from SetField action", log.Fields{"device-id": dh.deviceID,
						"SetPcp": *loSetPcp})
				} else {
					logger.Warnw(ctx, "flow action SetField invalid FieldType", log.Fields{"device-id": dh.deviceID,
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

//addFlowItemToUniPort parses the actual flow item to add it to the UniPort
func (dh *deviceHandler) addFlowItemToUniPort(ctx context.Context, apFlowItem *ofp.OfpFlowStats, apUniPort *onuUniPort) error {
	var loSetVlan uint16 = uint16(of.OfpVlanId_OFPVID_NONE)      //noValidEntry
	var loMatchVlan uint16 = uint16(of.OfpVlanId_OFPVID_PRESENT) //reserved VLANID entry
	var loAddPcp, loSetPcp uint8
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
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("flow-add invalid metadata: %s", dh.deviceID)
	}
	loTpID := uint8(flow.GetTechProfileIDFromWriteMetaData(ctx, metadata))
	loCookie := apFlowItem.GetCookie()
	loCookieSlice := []uint64{loCookie}
	logger.Debugw(ctx, "flow-add base indications", log.Fields{"device-id": dh.deviceID,
		"TechProf-Id": loTpID, "cookie": loCookie})

	dh.getFlowOfbFields(ctx, apFlowItem, &loMatchVlan, &loAddPcp, &loIPProto)
	/* TT related temporary workaround - should not be needed anymore
	if loIPProto == 2 {
		// some workaround for TT workflow at proto == 2 (IGMP trap) -> ignore the flow
		// avoids installing invalid EVTOCD rule
		logger.Debugw("flow-add type IpProto 2: TT workaround: ignore flow",
			log.Fields{"device-id": dh.deviceID})
		return nil
	}
	*/
	dh.getFlowActions(ctx, apFlowItem, &loSetPcp, &loSetVlan)

	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Errorw(ctx, "flow-add aborted - SetVlanId undefined, but MatchVid set", log.Fields{
			"device-id": dh.deviceID, "UniPort": apUniPort.portNo,
			"set_vid":   strconv.FormatInt(int64(loSetVlan), 16),
			"match_vid": strconv.FormatInt(int64(loMatchVlan), 16)})
		//TODO!!: Use DeviceId within the error response to rwCore
		//  likewise also in other error response cases to calling components as requested in [VOL-3458]
		return fmt.Errorf("flow-add Set/Match VlanId inconsistent: %s", dh.deviceID)
	}
	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan == uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Debugw(ctx, "flow-add vlan-any/copy", log.Fields{"device-id": dh.deviceID})
		loSetVlan = loMatchVlan //both 'transparent' (copy any)
	} else {
		//looks like OMCI value 4097 (copyFromOuter - for Uni double tagged) is not supported here
		if loSetVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) {
			// not set to transparent
			loSetVlan &= 0x0FFF //mask VID bits as prerequisite for vlanConfigFsm
		}
		logger.Debugw(ctx, "flow-add vlan-set", log.Fields{"device-id": dh.deviceID})
	}

	//mutex protection as the update_flow rpc maybe running concurrently for different flows, perhaps also activities
	dh.lockVlanConfig.RLock()
	logger.Debugw(ctx, "flow-add got lock", log.Fields{"device-id": dh.deviceID})
	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		err := dh.UniVlanConfigFsmMap[apUniPort.uniID].SetUniFlowParams(ctx, loTpID, loCookieSlice,
			loMatchVlan, loSetVlan, loSetPcp)
		dh.lockVlanConfig.RUnlock()
		return err
	}
	dh.lockVlanConfig.RUnlock()
	return dh.createVlanFilterFsm(ctx, apUniPort, loTpID, loCookieSlice,
		loMatchVlan, loSetVlan, loSetPcp, OmciVlanFilterAddDone)
}

//removeFlowItemFromUniPort parses the actual flow item to remove it from the UniPort
func (dh *deviceHandler) removeFlowItemFromUniPort(ctx context.Context, apFlowItem *ofp.OfpFlowStats, apUniPort *onuUniPort) error {
	//optimization and assumption: the flow cookie uniquely identifies the flow and with that the internal rule
	//hence only the cookie is used here to find the relevant flow and possibly remove the rule
	//no extra check is done on the rule parameters
	//accordingly the removal is done only once - for the first found flow with that cookie, even though
	// at flow creation is not assured, that the same cookie is not configured for different flows - just assumed
	//additionally it is assumed here, that removal can only be done for one cookie per flow in a sequence (different
	// from addFlow - where at reconcilement multiple cookies per flow ) can be configured in one sequence)
	// - some possible 'delete-all' sequence would have to be implemented separately (where the cookies are don't care anyway)
	loCookie := apFlowItem.GetCookie()
	logger.Debugw(ctx, "flow-remove base indications", log.Fields{"device-id": dh.deviceID, "cookie": loCookie})

	/* TT related temporary workaround - should not be needed anymore
	for _, field := range flow.GetOfbFields(apFlowItem) {
		if field.Type == of.OxmOfbFieldTypes_OFPXMT_OFB_IP_PROTO {
			loIPProto := field.GetIpProto()
			logger.Debugw(ctx, "flow type IpProto", log.Fields{"device-id": dh.deviceID,
				"IpProto": strconv.FormatInt(int64(loIPProto), 16)})
			if loIPProto == 2 {
				// some workaround for TT workflow on proto == 2 (IGMP trap) -> the flow was not added, no need to remove
				logger.Debugw(ctx, "flow-remove type IpProto 2: TT workaround: ignore flow",
					log.Fields{"device-id": dh.deviceID})
				return nil
			}
		}
	} //for all OfbFields
	*/

	//mutex protection as the update_flow rpc maybe running concurrently for different flows, perhaps also activities
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		return dh.UniVlanConfigFsmMap[apUniPort.uniID].RemoveUniFlowParams(ctx, loCookie)
	}
	logger.Debugw(ctx, "flow-remove called, but no flow is configured (no VlanConfigFsm, flow already removed) ",
		log.Fields{"device-id": dh.deviceID})
	//but as we regard the flow as not existing = removed we respond just ok
	// and treat the reason accordingly (which in the normal removal procedure is initiated by the FSM)
	go dh.deviceProcStatusUpdate(ctx, OmciVlanFilterRemDone)

	return nil
}

// createVlanFilterFsm initializes and runs the VlanFilter FSM to transfer OMCI related VLAN config
// if this function is called from possibly concurrent processes it must be mutex-protected from the caller!
func (dh *deviceHandler) createVlanFilterFsm(ctx context.Context, apUniPort *onuUniPort, aTpID uint8, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8, aDevEvent OnuDeviceEvent) error {
	chVlanFilterFsm := make(chan Message, 2048)

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice for device-id %x - aborting", dh.deviceID)
	}

	pVlanFilterFsm := NewUniVlanConfigFsm(ctx, dh, pDevEntry.PDevOmciCC, apUniPort, dh.pOnuTP,
		pDevEntry.pOnuDB, aTpID, aDevEvent, "UniVlanConfigFsm", chVlanFilterFsm,
		dh.pOpenOnuAc.AcceptIncrementalEvto, aCookieSlice, aMatchVlan, aSetVlan, aSetPcp)
	if pVlanFilterFsm != nil {
		dh.lockVlanConfig.Lock()
		dh.UniVlanConfigFsmMap[apUniPort.uniID] = pVlanFilterFsm
		dh.lockVlanConfig.Unlock()
		pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
		if pVlanFilterStatemachine != nil {
			if pVlanFilterStatemachine.Is(vlanStDisabled) {
				if err := pVlanFilterStatemachine.Event(vlanEvStart); err != nil {
					logger.Warnw(ctx, "UniVlanConfigFsm: can't start", log.Fields{"err": err})
					return fmt.Errorf("can't start UniVlanConfigFsm for device-id %x", dh.deviceID)
				}
				/***** UniVlanConfigFsm started */
				logger.Debugw(ctx, "UniVlanConfigFsm started", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
					"UniPort": apUniPort.portNo})
			} else {
				logger.Warnw(ctx, "wrong state of UniVlanConfigFsm - want: disabled", log.Fields{
					"have": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID})
				return fmt.Errorf("uniVlanConfigFsm not in expected disabled state for device-id %x", dh.deviceID)
			}
		} else {
			logger.Errorw(ctx, "UniVlanConfigFsm StateMachine invalid - cannot be executed!!", log.Fields{
				"device-id": dh.deviceID})
			return fmt.Errorf("uniVlanConfigFsm invalid for device-id %x", dh.deviceID)
		}
	} else {
		logger.Errorw(ctx, "UniVlanConfigFsm could not be created - abort!!", log.Fields{
			"device-id": dh.deviceID, "UniPort": apUniPort.portNo})
		return fmt.Errorf("uniVlanConfigFsm could not be created for device-id %x", dh.deviceID)
	}
	return nil
}

//VerifyVlanConfigRequest checks on existence of a given uniPort
// and starts verification of flow config based on that
func (dh *deviceHandler) VerifyVlanConfigRequest(ctx context.Context, aUniID uint8, aTpID uint8) {
	//ensure that the given uniID is available (configured) in the UniPort class (used for OMCI entities)
	var pCurrentUniPort *onuUniPort
	for _, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if uniPort.uniID == uint8(aUniID) {
			pCurrentUniPort = uniPort
			break //found - end search loop
		}
	}
	if pCurrentUniPort == nil {
		logger.Debugw(ctx, "VerifyVlanConfig aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": dh.deviceID, "uni-id": aUniID})
		return
	}
	dh.verifyUniVlanConfigRequest(ctx, pCurrentUniPort, aTpID)
}

//verifyUniVlanConfigRequest checks on existence of flow configuration and starts it accordingly
func (dh *deviceHandler) verifyUniVlanConfigRequest(ctx context.Context, apUniPort *onuUniPort, aTpID uint8) {
	//TODO!! verify and start pending flow configuration
	//some pending config request my exist in case the UniVlanConfig FSM was already started - with internal data -
	//but execution was set to 'on hold' as first the TechProfile config had to be applied

	dh.lockVlanConfig.RLock()
	if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		dh.lockVlanConfig.RUnlock()
		//VlanFilterFsm exists and was already started (assumed to wait for TechProfile execution here)
		pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
		if pVlanFilterStatemachine != nil {
			//if this was an event of the TP processing that was waited for in the VlanFilterFsm
			if pVlanFilterFsm.GetWaitingTpID() == aTpID {
				if pVlanFilterStatemachine.Is(vlanStWaitingTechProf) {
					if err := pVlanFilterStatemachine.Event(vlanEvContinueConfig); err != nil {
						logger.Warnw(ctx, "UniVlanConfigFsm: can't continue processing", log.Fields{"err": err,
							"device-id": dh.deviceID, "UniPort": apUniPort.portNo})
					} else {
						/***** UniVlanConfigFsm continued */
						logger.Debugw(ctx, "UniVlanConfigFsm continued", log.Fields{
							"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
							"UniPort": apUniPort.portNo})
					}
				} else if pVlanFilterStatemachine.Is(vlanStIncrFlowWaitTP) {
					if err := pVlanFilterStatemachine.Event(vlanEvIncrFlowConfig); err != nil {
						logger.Warnw(ctx, "UniVlanConfigFsm: can't continue processing", log.Fields{"err": err,
							"device-id": dh.deviceID, "UniPort": apUniPort.portNo})
					} else {
						/***** UniVlanConfigFsm continued */
						logger.Debugw(ctx, "UniVlanConfigFsm continued with incremental flow", log.Fields{
							"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
							"UniPort": apUniPort.portNo})
					}
				} else {
					logger.Debugw(ctx, "no state of UniVlanConfigFsm to be continued", log.Fields{
						"have": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
						"UniPort": apUniPort.portNo})
				}
			} else {
				logger.Debugw(ctx, "TechProfile Ready event for TpId that was not waited for in the VlanConfigFsm - continue waiting", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
					"UniPort": apUniPort.portNo, "techprofile-id (done)": aTpID})
			}
		} else {
			logger.Debugw(ctx, "UniVlanConfigFsm StateMachine does not exist, no flow processing", log.Fields{
				"device-id": dh.deviceID, "UniPort": apUniPort.portNo})
		}
	} else {
		dh.lockVlanConfig.RUnlock()
	}
}

//RemoveVlanFilterFsm deletes the stored pointer to the VlanConfigFsm
// intention is to provide this method to be called from VlanConfigFsm itself, when resources (and methods!) are cleaned up
func (dh *deviceHandler) RemoveVlanFilterFsm(ctx context.Context, apUniPort *onuUniPort) {
	logger.Debugw(ctx, "remove UniVlanConfigFsm StateMachine", log.Fields{
		"device-id": dh.deviceID, "uniPort": apUniPort.portNo})
	//save to do, even if entry dows not exist
	dh.lockVlanConfig.Lock()
	delete(dh.UniVlanConfigFsmMap, apUniPort.uniID)
	dh.lockVlanConfig.Unlock()
}

//ProcessPendingTpDelete processes any pending TP delete (if available)
func (dh *deviceHandler) ProcessPendingTpDelete(ctx context.Context, apUniPort *onuUniPort, aTpID uint8) {
	logger.Debugw(ctx, "enter processing pending tp delete", log.Fields{"device-id": dh.deviceID, "tpID": aTpID})
	if apUniPort == nil {
		logger.Errorw(ctx, "uni port is nil", log.Fields{"device-id": dh.deviceID})
		return
	}
	k := uniTP{uniID: apUniPort.uniID, tpID: aTpID}
	if pAniConfigFsm, ok := dh.pOnuTP.pAniConfigFsm[k]; pAniConfigFsm != nil && ok {
		pAniConfigStatemachine := pAniConfigFsm.pAdaptFsm.pFsm
		if pAniConfigStatemachine != nil {
			//If the gem port delete was waiting on flow remove, indicate event that flow remove is done
			if pAniConfigStatemachine.Is(aniStWaitingFlowRem) {
				logger.Debugw(ctx, "ani fsm in aniStWaitingFlowRem state - handling aniEvFlowRemDone event",
					log.Fields{"device-id": dh.deviceID, "tpID": aTpID})
				if err := pAniConfigStatemachine.Event(aniEvFlowRemDone); err != nil {
					logger.Warnw(ctx, "AniConfigFsm: can't continue processing", log.Fields{"err": err,
						"device-id": dh.deviceID, "UniPort": apUniPort.portNo, "tpID": aTpID})
					return
				}
			} else {
				logger.Debugw(ctx, "ani fsm not in aniStWaitingFlowRem state", log.Fields{"device-id": dh.deviceID, "tpID": aTpID})
				return
			}
		}
		return
	}
}

//startWritingOnuDataToKvStore initiates the KVStore write of ONU persistent data
func (dh *deviceHandler) startWritingOnuDataToKvStore(ctx context.Context, aPDevEntry *OnuDeviceEntry) error {
	dh.mutexKvStoreContext.Lock()         //this write routine may (could) be called with the same context,
	defer dh.mutexKvStoreContext.Unlock() //this write routine may (could) be called with the same context,
	// obviously then parallel processing on the cancel must be avoided
	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	aPDevEntry.resetKvProcessingErrorIndication()
	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish

	go aPDevEntry.updateOnuKvStore(log.WithSpanFromContext(dctx, ctx), &wg)
	dh.waitForCompletion(ctx, cancel, &wg, "UpdateKvStore") //wait for background process to finish

	return aPDevEntry.getKvProcessingErrorIndication()
}

//storePersUniFlowConfig updates local storage of OnuUniFlowConfig and writes it into kv-store afterwards to have it
//available for potential reconcilement
func (dh *deviceHandler) storePersUniFlowConfig(ctx context.Context, aUniID uint8,
	aUniVlanFlowParams *[]uniVlanFlowParams, aWriteToKvStore bool) error {

	if dh.isReconciling() {
		logger.Debugw(ctx, "reconciling - don't store persistent UniFlowConfig", log.Fields{"device-id": dh.deviceID})
		return nil
	}
	logger.Debugw(ctx, "Store or clear persistent UniFlowConfig", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Errorw(ctx, "No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	pDevEntry.updateOnuUniFlowConfig(aUniID, aUniVlanFlowParams)

	if aWriteToKvStore {
		return dh.startWritingOnuDataToKvStore(ctx, pDevEntry)
	}
	return nil
}

func (dh *deviceHandler) waitForCompletion(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, aCallerIdent string) {
	defer cancel() //ensure termination of context (may be pro forma)
	wg.Wait()
	logger.Debugw(ctx, "WaitGroup processing completed", log.Fields{
		"device-id": dh.deviceID, "called from": aCallerIdent})
}

func (dh *deviceHandler) deviceReasonUpdate(ctx context.Context, deviceReason uint8, notifyCore bool) error {

	dh.deviceReason = deviceReason
	if notifyCore {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing at calling position
		if err := dh.coreProxy.DeviceReasonUpdate(log.WithSpanFromContext(context.TODO(), ctx), dh.deviceID, deviceReasonMap[deviceReason]); err != nil {
			logger.Errorf(ctx, "DeviceReasonUpdate error: %s",
				log.Fields{"device-id": dh.deviceID, "error": err}, deviceReasonMap[deviceReason])
			return err
		}
		logger.Infof(ctx, "DeviceReasonUpdate success: %s - device-id: %s", deviceReasonMap[deviceReason], dh.deviceID)
		return nil
	}
	logger.Infof(ctx, "Don't notify core about DeviceReasonUpdate: %s - device-id: %s", deviceReasonMap[deviceReason], dh.deviceID)
	return nil
}

func (dh *deviceHandler) storePersistentData(ctx context.Context) error {
	pDevEntry := dh.getOnuDeviceEntry(ctx, true)
	if pDevEntry == nil {
		logger.Warnw(ctx, "No valid OnuDevice", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	return dh.startWritingOnuDataToKvStore(ctx, pDevEntry)
}

func (dh *deviceHandler) combineErrorStrings(errS ...error) error {
	var errStr string = ""
	for _, err := range errS {
		if err != nil {
			errStr = errStr + err.Error() + " "
		}
	}
	if errStr != "" {
		return fmt.Errorf("%s: %s", errStr, dh.deviceID)
	}
	return nil
}

// getUniPortMEEntityID takes uniPortNo as the input and returns the Entity ID corresponding to this UNI-G ME Instance
func (dh *deviceHandler) getUniPortMEEntityID(uniPortNo uint32) (uint16, error) {
	dh.lockDevice.RLock()
	defer dh.lockDevice.RUnlock()
	if uniPort, ok := dh.uniEntityMap[uniPortNo]; ok {
		return uniPort.entityID, nil
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
		logger.Errorw(ctx, "one-or-more-pm-config-failed", log.Fields{"device-id": dh.deviceID, "pmConfig": dh.pmConfigs})
		return fmt.Errorf("errors-handling-one-or-more-pm-config, errors:%v", errorsList)
	}
	logger.Infow(ctx, "pm-config-updated", log.Fields{"device-id": dh.deviceID, "pmConfig": dh.pmConfigs})
	return nil
}

func (dh *deviceHandler) handleGlobalPmConfigUpdates(ctx context.Context, pmConfigs *voltha.PmConfigs) []error {
	var err error
	var errorsList []error
	logger.Infow(ctx, "handling-global-pm-config-params - start", log.Fields{"device-id": dh.device.Id})

	if pmConfigs.DefaultFreq != dh.pmConfigs.DefaultFreq {
		if err = dh.pOnuMetricsMgr.updateDefaultFrequency(ctx, pmConfigs); err != nil {
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
		dh.pOnuMetricsMgr.onuMetricsManagerLock.RLock()
		m, ok := dh.pOnuMetricsMgr.groupMetricMap[v.GroupName]
		dh.pOnuMetricsMgr.onuMetricsManagerLock.RUnlock()

		if ok && m.frequency != v.GroupFreq {
			if err = dh.pOnuMetricsMgr.updateGroupFreq(ctx, v.GroupName, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		if ok && m.enabled != v.Enabled {
			if err = dh.pOnuMetricsMgr.updateGroupSupport(ctx, v.GroupName, pmConfigs); err != nil {
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
		dh.pOnuMetricsMgr.onuMetricsManagerLock.RLock()
		m, ok := dh.pOnuMetricsMgr.standaloneMetricMap[v.Name]
		dh.pOnuMetricsMgr.onuMetricsManagerLock.RUnlock()

		if ok && m.frequency != v.SampleFreq {
			if err = dh.pOnuMetricsMgr.updateMetricFreq(ctx, v.Name, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		if ok && m.enabled != v.Enabled {
			if err = dh.pOnuMetricsMgr.updateMetricSupport(ctx, v.Name, pmConfigs); err != nil {
				errorsList = append(errorsList, err)
			}
		}
	}
	logger.Debugw(ctx, "handling-individual-pm-config-params - done", log.Fields{"device-id": dh.device.Id})
	return errorsList
}

// nolint: gocyclo
func (dh *deviceHandler) startCollector(ctx context.Context) {
	logger.Debugf(ctx, "startingCollector")

	// Start routine to process OMCI GET Responses
	go dh.pOnuMetricsMgr.processOmciMessages(ctx)
	// Initialize the next metric collection time.
	// Normally done when the onu_metrics_manager is initialized the first time, but needed again later when ONU is
	// reset like onu rebooted.
	dh.pOnuMetricsMgr.initializeMetricCollectionTime(ctx)
	dh.setCollectorIsRunning(true)
	for {
		select {
		case <-dh.stopCollector:
			dh.setCollectorIsRunning(false)
			logger.Debugw(ctx, "stopping-collector-for-onu", log.Fields{"device-id": dh.device.Id})
			// Stop the L2 PM FSM
			go func() {
				if dh.pOnuMetricsMgr.pAdaptFsm != nil && dh.pOnuMetricsMgr.pAdaptFsm.pFsm != nil {
					if err := dh.pOnuMetricsMgr.pAdaptFsm.pFsm.Event(l2PmEventStop); err != nil {
						logger.Errorw(ctx, "error calling event", log.Fields{"device-id": dh.deviceID, "err": err})
					}
				} else {
					logger.Errorw(ctx, "metrics manager fsm not initialized", log.Fields{"device-id": dh.deviceID})
				}
			}()

			dh.pOnuMetricsMgr.stopProcessingOmciResponses <- true // Stop the OMCI GET response processing routine
			dh.pOnuMetricsMgr.stopTicks <- true

			return
		case <-time.After(time.Duration(FrequencyGranularity) * time.Second): // Check every FrequencyGranularity to see if it is time for collecting metrics
			if !dh.pmConfigs.FreqOverride { // If FreqOverride is false, then nextGlobalMetricCollectionTime applies
				// If the current time is eqaul to or greater than the nextGlobalMetricCollectionTime, collect the group and standalone metrics
				if time.Now().Equal(dh.pOnuMetricsMgr.nextGlobalMetricCollectionTime) || time.Now().After(dh.pOnuMetricsMgr.nextGlobalMetricCollectionTime) {
					go dh.pOnuMetricsMgr.collectAllGroupAndStandaloneMetrics(ctx)
					// Update the next metric collection time.
					dh.pOnuMetricsMgr.nextGlobalMetricCollectionTime = time.Now().Add(time.Duration(dh.pmConfigs.DefaultFreq) * time.Second)
				}
			} else {
				if dh.pmConfigs.Grouped { // metrics are managed as a group
					// parse through the group and standalone metrics to see it is time to collect their metrics
					dh.pOnuMetricsMgr.onuMetricsManagerLock.RLock() // Rlock as we are reading groupMetricMap and standaloneMetricMap

					for n, g := range dh.pOnuMetricsMgr.groupMetricMap {
						// If the group is enabled AND (current time is equal to OR after nextCollectionInterval, collect the group metric)
						// Since the L2 PM counters are collected in a separate FSM, we should avoid those counters in the check.
						if g.enabled && !g.isL2PMCounter && (time.Now().Equal(g.nextCollectionInterval) || time.Now().After(g.nextCollectionInterval)) {
							go dh.pOnuMetricsMgr.collectGroupMetric(ctx, n)
						}
					}
					for n, m := range dh.pOnuMetricsMgr.standaloneMetricMap {
						// If the standalone is enabled AND (current time is equal to OR after nextCollectionInterval, collect the metric)
						if m.enabled && (time.Now().Equal(m.nextCollectionInterval) || time.Now().After(m.nextCollectionInterval)) {
							go dh.pOnuMetricsMgr.collectStandaloneMetric(ctx, n)
						}
					}
					dh.pOnuMetricsMgr.onuMetricsManagerLock.RUnlock()

					// parse through the group and update the next metric collection time
					dh.pOnuMetricsMgr.onuMetricsManagerLock.Lock() // Lock as we are writing the next metric collection time
					for _, g := range dh.pOnuMetricsMgr.groupMetricMap {
						// If group enabled, and the nextCollectionInterval is old (before or equal to current time), update the next collection time stamp
						// Since the L2 PM counters are collected and managed in a separate FSM, we should avoid those counters in the check.
						if g.enabled && !g.isL2PMCounter && (g.nextCollectionInterval.Before(time.Now()) || g.nextCollectionInterval.Equal(time.Now())) {
							g.nextCollectionInterval = time.Now().Add(time.Duration(g.frequency) * time.Second)
						}
					}
					// parse through the standalone metrics and update the next metric collection time
					for _, m := range dh.pOnuMetricsMgr.standaloneMetricMap {
						// If standalone metrics enabled, and the nextCollectionInterval is old (before or equal to current time), update the next collection time stamp
						if m.enabled && (m.nextCollectionInterval.Before(time.Now()) || m.nextCollectionInterval.Equal(time.Now())) {
							m.nextCollectionInterval = time.Now().Add(time.Duration(m.frequency) * time.Second)
						}
					}
					dh.pOnuMetricsMgr.onuMetricsManagerLock.Unlock()
				} /* else { // metrics are not managed as a group
					// TODO: We currently do not have standalone metrics. When available, add code here to fetch the metric.
				} */
			}
		}
	}
}

func (dh *deviceHandler) getUniPortStatus(ctx context.Context, uniInfo *extension.GetOnuUniInfoRequest) *extension.SingleGetValueResponse {

	portStatus := NewUniPortStatus(dh.pOnuOmciDevice.PDevOmciCC)
	return portStatus.getUniPortStatus(ctx, uniInfo.UniIndex)
}

func (dh *deviceHandler) isFsmInOmciIdleState(ctx context.Context, pFsm *fsm.FSM, wantedState string) bool {
	if pFsm == nil {
		return true //FSM not active - so there is no activity on omci
	}
	return pFsm.Current() == wantedState
}

func (dh *deviceHandler) isFsmInOmciIdleStateDefault(ctx context.Context, omciFsm usedOmciConfigFsms, wantedState string) bool {
	var pFsm *fsm.FSM
	//note/TODO!!: might be that access to all these specific FSM; pointers need a semaphore protection as well, cmp lockUpgradeFsm
	switch omciFsm {
	case cUploadFsm:
		{
			pFsm = dh.pOnuOmciDevice.pMibUploadFsm.pFsm
		}
	case cDownloadFsm:
		{
			pFsm = dh.pOnuOmciDevice.pMibDownloadFsm.pFsm
		}
	case cUniLockFsm:
		{
			pFsm = dh.pLockStateFsm.pAdaptFsm.pFsm
		}
	case cUniUnLockFsm:
		{
			pFsm = dh.pUnlockStateFsm.pAdaptFsm.pFsm
		}
	case cL2PmFsm:
		{
			if dh.pOnuMetricsMgr != nil && dh.pOnuMetricsMgr.pAdaptFsm != nil {
				pFsm = dh.pOnuMetricsMgr.pAdaptFsm.pFsm
			} else {
				return true //FSM not active - so there is no activity on omci
			}
		}
	case cOnuUpgradeFsm:
		{
			dh.lockUpgradeFsm.RLock()
			defer dh.lockUpgradeFsm.RUnlock()
			pFsm = dh.pOnuUpradeFsm.pAdaptFsm.pFsm
		}
	default:
		{
			logger.Errorw(ctx, "invalid stateMachine selected for idle check", log.Fields{
				"device-id": dh.deviceID, "selectedFsm number": omciFsm})
			return false //logical error in FSM check, do not not indicate 'idle' - we can't be sure
		}
	}
	return dh.isFsmInOmciIdleState(ctx, pFsm, wantedState)
}

func (dh *deviceHandler) isAniConfigFsmInOmciIdleState(ctx context.Context, omciFsm usedOmciConfigFsms, idleState string) bool {
	for _, v := range dh.pOnuTP.pAniConfigFsm {
		if !dh.isFsmInOmciIdleState(ctx, v.pAdaptFsm.pFsm, idleState) {
			return false
		}
	}
	return true
}

func (dh *deviceHandler) isUniVlanConfigFsmInOmciIdleState(ctx context.Context, omciFsm usedOmciConfigFsms, idleState string) bool {
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	for _, v := range dh.UniVlanConfigFsmMap {
		if !dh.isFsmInOmciIdleState(ctx, v.pAdaptFsm.pFsm, idleState) {
			return false
		}
	}
	return true //FSM not active - so there is no activity on omci
}

func (dh *deviceHandler) checkUserServiceExists(ctx context.Context) bool {
	dh.lockVlanConfig.RLock()
	defer dh.lockVlanConfig.RUnlock()
	for _, v := range dh.UniVlanConfigFsmMap {
		if v.pAdaptFsm.pFsm != nil {
			if v.pAdaptFsm.pFsm.Is(cVlanFsmConfiguredState) {
				return true //there is at least one VLAN FSM with some active configuration
			}
		}
	}
	return false //there is no VLAN FSM with some active configuration
}

func (dh *deviceHandler) checkAuditStartCondition(ctx context.Context, callingFsm usedOmciConfigFsms) bool {
	for fsmName, fsmStruct := range fsmOmciIdleStateFuncMap {
		if fsmName != callingFsm && !fsmStruct.omciIdleCheckFunc(dh, ctx, fsmName, fsmStruct.omciIdleState) {
			return false
		}
	}
	// a further check is done to identify, if at least some data traffic related configuration exists
	// so that a user of this ONU could be 'online' (otherwise it makes no sense to check the MDS [with the intention to keep the user service up])
	return dh.checkUserServiceExists(ctx)
}

func (dh *deviceHandler) prepareReconcilingWithActiveAdapter(ctx context.Context) {
	logger.Debugw(ctx, "prepare to reconcile the ONU with adapter using persistency data", log.Fields{"device-id": dh.device.Id})
	if err := dh.resetFsms(ctx, false); err != nil {
		logger.Errorw(ctx, "reset of FSMs failed!", log.Fields{"device-id": dh.deviceID, "error": err})
		// TODO: fatal error reset ONU, delete deviceHandler!
		return
	}
	if !dh.getCollectorIsRunning() {
		// Start PM collector routine
		go dh.startCollector(ctx)
	}
	if !dh.getAlarmManagerIsRunning() {
		go dh.startAlarmManager(ctx)
	}
	dh.uniEntityMap = make(map[uint32]*onuUniPort)
	dh.startReconciling(ctx)
}

func (dh *deviceHandler) setCollectorIsRunning(flagValue bool) {
	dh.mutexCollectorFlag.Lock()
	dh.collectorIsRunning = flagValue
	dh.mutexCollectorFlag.Unlock()
}

func (dh *deviceHandler) getCollectorIsRunning() bool {
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

func (dh *deviceHandler) getAlarmManagerIsRunning() bool {
	dh.mutextAlarmManagerFlag.RLock()
	flagValue := dh.alarmManagerIsRunning
	dh.mutextAlarmManagerFlag.RUnlock()
	return flagValue
}

func (dh *deviceHandler) startAlarmManager(ctx context.Context) {
	logger.Debugf(ctx, "startingAlarmManager")

	// Start routine to process OMCI GET Responses
	go dh.pAlarmMgr.startOMCIAlarmMessageProcessing(ctx)
	dh.setAlarmManagerIsRunning(true)
	if stop := <-dh.stopAlarmManager; stop {
		logger.Debugw(ctx, "stopping-collector-for-onu", log.Fields{"device-id": dh.device.Id})
		dh.pAlarmMgr.stopAlarmAuditTimer <- struct{}{}
		dh.pAlarmMgr.stopProcessingOmciMessages <- true // Stop the OMCI routines if any(This will stop the fsms also)
		dh.setAlarmManagerIsRunning(false)

	}
}

func (dh *deviceHandler) startReconciling(ctx context.Context) {
	logger.Debugw(ctx, "start reconciling", log.Fields{"timeout": dh.pOpenOnuAc.maxTimeoutReconciling, "device-id": dh.deviceID})
	if !dh.isReconciling() {
		go func() {
			select {
			case <-dh.chReconcilingFinished:
				logger.Debugw(ctx, "reconciling has been finished in time",
					log.Fields{"device-id": dh.deviceID})
			case <-time.After(dh.pOpenOnuAc.maxTimeoutReconciling):
				logger.Errorw(ctx, "timeout waiting for reconciling to be finished!",
					log.Fields{"device-id": dh.deviceID})
			}
			dh.mutexReconcilingFlag.Lock()
			dh.reconciling = false
			dh.mutexReconcilingFlag.Unlock()
		}()
		dh.mutexReconcilingFlag.Lock()
		dh.reconciling = true
		dh.mutexReconcilingFlag.Unlock()
	} else {
		logger.Warnw(ctx, "reconciling is already running", log.Fields{"device-id": dh.deviceID})
	}
}

func (dh *deviceHandler) stopReconciling(ctx context.Context) {
	logger.Debugw(ctx, "stop reconciling", log.Fields{"device-id": dh.deviceID})
	if dh.isReconciling() {
		dh.chReconcilingFinished <- true
	} else {
		logger.Infow(ctx, "reconciling is not running", log.Fields{"device-id": dh.deviceID})
	}
}

func (dh *deviceHandler) isReconciling() bool {
	dh.mutexReconcilingFlag.RLock()
	value := dh.reconciling
	dh.mutexReconcilingFlag.RUnlock()
	return value
}
