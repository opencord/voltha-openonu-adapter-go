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
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/looplab/fsm"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	vc "github.com/opencord/voltha-protos/v3/go/common"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	oop "github.com/opencord/voltha-protos/v3/go/openolt"
	"github.com/opencord/voltha-protos/v3/go/voltha"
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
	pon           = voltha.EventSubCategory_PON
	olt           = voltha.EventSubCategory_OLT
	ont           = voltha.EventSubCategory_ONT
	onu           = voltha.EventSubCategory_ONU
	nni           = voltha.EventSubCategory_NNI
	service       = voltha.EventCategory_SERVICE
	security      = voltha.EventCategory_SECURITY
	equipment     = voltha.EventCategory_EQUIPMENT
	processing    = voltha.EventCategory_PROCESSING
	environment   = voltha.EventCategory_ENVIRONMENT
	communication = voltha.EventCategory_COMMUNICATION
)

const (
	cEventObjectType = "ONU"
)
const (
	cOnuActivatedEvent = "ONU_ACTIVATED"
)

//DeviceHandler will interact with the ONU ? device.
type DeviceHandler struct {
	deviceID         string
	DeviceType       string
	adminState       string
	device           *voltha.Device
	logicalDeviceID  string
	ProxyAddressID   string
	ProxyAddressType string
	parentId         string
	ponPortNumber    uint32

	coreProxy    adapterif.CoreProxy
	AdapterProxy adapterif.AdapterProxy
	EventProxy   adapterif.EventProxy

	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	pPonPort        *voltha.Port
	deviceEntrySet  chan bool //channel for DeviceEntry set event
	pOnuOmciDevice  *OnuDeviceEntry
	pOnuTP          *OnuUniTechProf
	exitChannel     chan int
	lockDevice      sync.RWMutex
	pOnuIndication  *oop.OnuIndication
	deviceReason    string
	pLockStateFsm   *LockStateFsm
	pUnlockStateFsm *LockStateFsm

	//flowMgr       *OpenOltFlowMgr
	//eventMgr      *OpenOltEventMgr
	//resourceMgr   *rsrcMgr.OpenOltResourceMgr

	//discOnus sync.Map
	//onus     sync.Map
	//portStats          *OpenOltStatisticsMgr
	//metrics            *pmmetrics.PmMetrics
	stopCollector      chan bool
	stopHeartbeatCheck chan bool
	activePorts        sync.Map
	uniEntityMap       map[uint32]*OnuUniPort
	reconciling        bool
}

//NewDeviceHandler creates a new device handler
func NewDeviceHandler(cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep adapterif.EventProxy, device *voltha.Device, adapter *OpenONUAC) *DeviceHandler {
	var dh DeviceHandler
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
	dh.stopCollector = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	dh.activePorts = sync.Map{}
	//TODO initialize the support classes.
	dh.uniEntityMap = make(map[uint32]*OnuUniPort)
	dh.reconciling = false

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
			"before_event":                      func(e *fsm.Event) { dh.logStateChange(e) },
			("before_" + devEvDeviceInit):       func(e *fsm.Event) { dh.doStateInit(e) },
			("after_" + devEvDeviceInit):        func(e *fsm.Event) { dh.postInit(e) },
			("before_" + devEvGrpcConnected):    func(e *fsm.Event) { dh.doStateConnected(e) },
			("before_" + devEvGrpcDisconnected): func(e *fsm.Event) { dh.doStateInit(e) },
			("after_" + devEvGrpcDisconnected):  func(e *fsm.Event) { dh.postInit(e) },
			("before_" + devEvDeviceUpInd):      func(e *fsm.Event) { dh.doStateUp(e) },
			("before_" + devEvDeviceDownInd):    func(e *fsm.Event) { dh.doStateDown(e) },
		},
	)

	return &dh
}

// start save the device to the data model
func (dh *DeviceHandler) Start(ctx context.Context) {
	logger.Debugw("starting-device-handler", log.Fields{"device": dh.device, "deviceId": dh.deviceID})
	// Add the initial device to the local model
	logger.Debug("device-handler-started")
}

// stop stops the device dh.  Not much to do for now
func (dh *DeviceHandler) stop(ctx context.Context) {
	logger.Debug("stopping-device-handler")
	dh.exitChannel <- 1
}

// ##########################################################################################
// DeviceHandler methods that implement the adapters interface requests ##### begin #########

//AdoptOrReconcileDevice adopts the OLT device
func (dh *DeviceHandler) AdoptOrReconcileDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw("Adopt_or_reconcile_device", log.Fields{"device-id": device.Id, "Address": device.GetHostAndPort()})

	logger.Debugw("Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
	if dh.pDeviceStateFsm.Is(devStNull) {
		if err := dh.pDeviceStateFsm.Event(devEvDeviceInit); err != nil {
			logger.Errorw("Device FSM: Can't go to state DeviceInit", log.Fields{"err": err})
		}
		logger.Debugw("Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
	} else {
		logger.Debugw("AdoptOrReconcileDevice: Agent/device init already done", log.Fields{"device-id": device.Id})
	}

	/*
		// Now, set the initial PM configuration for that device
		if err := dh.coreProxy.DevicePMConfigUpdate(nil, dh.metrics.ToPmConfigs()); err != nil {
			logger.Errorw("error-updating-PMs", log.Fields{"deviceId": device.Id, "error": err})
		}

		go startCollector(dh)
		go startHeartbeatCheck(dh)
	*/
}

//ProcessInterAdapterMessage sends the proxied messages to the target device
// If the proxy address is not found in the unmarshalled message, it first fetches the onu device for which the message
// is meant, and then send the unmarshalled omci message to this onu
func (dh *DeviceHandler) ProcessInterAdapterMessage(msg *ic.InterAdapterMessage) error {
	msgID := msg.Header.Id
	msgType := msg.Header.Type
	fromTopic := msg.Header.FromTopic
	toTopic := msg.Header.ToTopic
	toDeviceID := msg.Header.ToDeviceId
	proxyDeviceID := msg.Header.ProxyDeviceId
	logger.Debugw("InterAdapter message header", log.Fields{"msgID": msgID, "msgType": msgType,
		"fromTopic": fromTopic, "toTopic": toTopic, "toDeviceID": toDeviceID, "proxyDeviceID": proxyDeviceID})

	switch msgType {
	case ic.InterAdapterMessageType_OMCI_REQUEST:
		{
			msgBody := msg.GetBody()
			omciMsg := &ic.InterAdapterOmciMessage{}
			if err := ptypes.UnmarshalAny(msgBody, omciMsg); err != nil {
				logger.Warnw("cannot-unmarshal-omci-msg-body", log.Fields{
					"deviceID": dh.deviceID, "error": err})
				return err
			}

			//assuming omci message content is hex coded!
			// with restricted output of 16(?) bytes would be ...omciMsg.Message[:16]
			logger.Debugw("inter-adapter-recv-omci", log.Fields{
				"deviceID": dh.deviceID, "RxOmciMessage": hex.EncodeToString(omciMsg.Message)})
			//receive_message(omci_msg.message)
			pDevEntry := dh.GetOnuDeviceEntry(true)
			if pDevEntry != nil {
				return pDevEntry.PDevOmciCC.ReceiveMessage(context.TODO(), omciMsg.Message)
			} else {
				logger.Errorw("No valid OnuDevice -aborting", log.Fields{"deviceID": dh.deviceID})
				return errors.New("No valid OnuDevice")
			}
		}
	case ic.InterAdapterMessageType_ONU_IND_REQUEST:
		{
			msgBody := msg.GetBody()
			onu_indication := &oop.OnuIndication{}
			if err := ptypes.UnmarshalAny(msgBody, onu_indication); err != nil {
				logger.Warnw("cannot-unmarshal-onu-indication-msg-body", log.Fields{
					"deviceID": dh.deviceID, "error": err})
				return err
			}

			onu_operstate := onu_indication.GetOperState()
			logger.Debugw("inter-adapter-recv-onu-ind", log.Fields{"OnuId": onu_indication.GetOnuId(),
				"AdminState": onu_indication.GetAdminState(), "OperState": onu_operstate,
				"SNR": onu_indication.GetSerialNumber()})

			//interface related functions might be error checked ....
			if onu_operstate == "up" {
				dh.create_interface(onu_indication)
			} else if (onu_operstate == "down") || (onu_operstate == "unreachable") {
				dh.updateInterface(onu_indication)
			} else {
				logger.Errorw("unknown-onu-indication operState", log.Fields{"OnuId": onu_indication.GetOnuId()})
				return errors.New("InvalidOperState")
			}
		}
	case ic.InterAdapterMessageType_TECH_PROFILE_DOWNLOAD_REQUEST:
		{
			if dh.pOnuTP == nil {
				//should normally not happen ...
				logger.Warnw("onuTechProf instance not set up for DLMsg request - ignoring request",
					log.Fields{"deviceID": dh.deviceID})
				return errors.New("TechProfile DLMsg request while onuTechProf instance not setup")
			}
			if (dh.deviceReason == "stopping-openomci") || (dh.deviceReason == "omci-admin-lock") {
				// I've seen cases for this request, where the device was already stopped
				logger.Warnw("TechProf stopped: device-unreachable", log.Fields{"deviceId": dh.deviceID})
				return errors.New("device-unreachable")
			}

			msgBody := msg.GetBody()
			techProfMsg := &ic.InterAdapterTechProfileDownloadMessage{}
			if err := ptypes.UnmarshalAny(msgBody, techProfMsg); err != nil {
				logger.Warnw("cannot-unmarshal-techprof-msg-body", log.Fields{
					"deviceID": dh.deviceID, "error": err})
				return err
			}

			// we have to lock access to TechProfile processing based on different messageType calls or
			// even to fast subsequent calls of the same messageType
			dh.pOnuTP.lockTpProcMutex()
			// lock hangs as long as below decoupled or other related TechProfile processing is active
			if bTpModify := dh.pOnuTP.updateOnuUniTpPath(techProfMsg.UniId, techProfMsg.Path); bTpModify == true {
				//	if there has been some change for some uni TechProfilePath
				//in order to allow concurrent calls to other dh instances we do not wait for execution here
				//but doing so we can not indicate problems to the caller (who does what with that then?)
				//by now we just assume straightforward successful execution
				//TODO!!! Generally: In this scheme it would be good to have some means to indicate
				//  possible problems to the caller later autonomously

				// deadline context to ensure completion of background routines waited for
				//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
				deadline := time.Now().Add(30 * time.Second) //allowed run time to finish before execution
				dctx, cancel := context.WithDeadline(context.Background(), deadline)

				dh.pOnuTP.resetProcessingErrorIndication()
				var wg sync.WaitGroup
				wg.Add(2) // for the 2 go routines to finish
				// attention: deadline completion check and wg.Done is to be done in both routines
				go dh.pOnuTP.configureUniTp(dctx, techProfMsg.UniId, techProfMsg.Path, &wg)
				go dh.pOnuTP.updateOnuTpPathKvStore(dctx, &wg)
				//the wait.. function is responsible for tpProcMutex.Unlock()
				err := dh.pOnuTP.waitForTpCompletion(cancel, &wg) //wait for background process to finish and collect their result
				return err
			}
			// no change, nothing really to do
			dh.pOnuTP.unlockTpProcMutex()
			//return success
			return nil
		}
	case ic.InterAdapterMessageType_DELETE_GEM_PORT_REQUEST:
		{
			if dh.pOnuTP == nil {
				//should normally not happen ...
				logger.Warnw("onuTechProf instance not set up for DelGem request - ignoring request",
					log.Fields{"deviceID": dh.deviceID})
				return errors.New("TechProfile DelGem request while onuTechProf instance not setup")
			}

			msgBody := msg.GetBody()
			delGemPortMsg := &ic.InterAdapterDeleteGemPortMessage{}
			if err := ptypes.UnmarshalAny(msgBody, delGemPortMsg); err != nil {
				logger.Warnw("cannot-unmarshal-delete-gem-msg-body", log.Fields{
					"deviceID": dh.deviceID, "error": err})
				return err
			}

			//compare TECH_PROFILE_DOWNLOAD_REQUEST
			dh.pOnuTP.lockTpProcMutex()

			// deadline context to ensure completion of background routines waited for
			deadline := time.Now().Add(10 * time.Second) //allowed run time to finish before execution
			dctx, cancel := context.WithDeadline(context.Background(), deadline)

			dh.pOnuTP.resetProcessingErrorIndication()
			var wg sync.WaitGroup
			wg.Add(1) // for the 1 go routine to finish
			go dh.pOnuTP.deleteTpResource(dctx, delGemPortMsg.UniId, delGemPortMsg.TpPath,
				cResourceGemPort, delGemPortMsg.GemPortId, &wg)
			//the wait.. function is responsible for tpProcMutex.Unlock()
			err := dh.pOnuTP.waitForTpCompletion(cancel, &wg) //let that also run off-line to let the IA messaging return!
			return err
		}
	case ic.InterAdapterMessageType_DELETE_TCONT_REQUEST:
		{
			if dh.pOnuTP == nil {
				//should normally not happen ...
				logger.Warnw("onuTechProf instance not set up for DelTcont request - ignoring request",
					log.Fields{"deviceID": dh.deviceID})
				return errors.New("TechProfile DelTcont request while onuTechProf instance not setup")
			}

			msgBody := msg.GetBody()
			delTcontMsg := &ic.InterAdapterDeleteTcontMessage{}
			if err := ptypes.UnmarshalAny(msgBody, delTcontMsg); err != nil {
				logger.Warnw("cannot-unmarshal-delete-tcont-msg-body", log.Fields{
					"deviceID": dh.deviceID, "error": err})
				return err
			}

			//compare TECH_PROFILE_DOWNLOAD_REQUEST
			dh.pOnuTP.lockTpProcMutex()
			if bTpModify := dh.pOnuTP.updateOnuUniTpPath(delTcontMsg.UniId, ""); bTpModify == true {
				// deadline context to ensure completion of background routines waited for
				deadline := time.Now().Add(10 * time.Second) //allowed run time to finish before execution
				dctx, cancel := context.WithDeadline(context.Background(), deadline)

				dh.pOnuTP.resetProcessingErrorIndication()
				var wg sync.WaitGroup
				wg.Add(2) // for the 2 go routines to finish
				go dh.pOnuTP.deleteTpResource(dctx, delTcontMsg.UniId, delTcontMsg.TpPath,
					cResourceTcont, delTcontMsg.AllocId, &wg)
				// Removal of the tcont/alloc id mapping represents the removal of the tech profile
				go dh.pOnuTP.updateOnuTpPathKvStore(dctx, &wg)
				//the wait.. function is responsible for tpProcMutex.Unlock()
				err := dh.pOnuTP.waitForTpCompletion(cancel, &wg) //let that also run off-line to let the IA messaging return!
				return err
			}
			dh.pOnuTP.unlockTpProcMutex()
			//return success
			return nil
		}
	default:
		{
			logger.Errorw("inter-adapter-unhandled-type", log.Fields{
				"deviceID": dh.deviceID, "msgType": msg.Header.Type})
			return errors.New("unimplemented")
		}
	}
	return nil
}

//DisableDevice locks the ONU and its UNI/VEIP ports (admin lock via OMCI)
// TODO!!! Clarify usage of this method, it is for sure not used within ONOS (OLT) device disable
//         maybe it is obsolete by now
func (dh *DeviceHandler) DisableDevice(device *voltha.Device) {
	logger.Debugw("disable-device", log.Fields{"DeviceId": device.Id, "SerialNumber": device.SerialNumber})

	//admin-lock reason can also be used uniquely for setting the DeviceState accordingly - inblock
	//state checking to prevent unneeded processing (eg. on ONU 'unreachable' and 'down')
	if dh.deviceReason != "omci-admin-lock" {
		// disable UNI ports/ONU
		// *** should generate UniAdminStateDone event - unrelated to DeviceProcStatusUpdate!!
		//     here the result of the processing is not checked (trusted in background) *****
		if dh.pLockStateFsm == nil {
			dh.createUniLockFsm(true, UniAdminStateDone)
		} else { //LockStateFSM already init
			dh.pLockStateFsm.SetSuccessEvent(UniAdminStateDone)
			dh.runUniLockFsm(true)
		}

		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "omci-admin-lock"); err != nil {
			logger.Errorw("error-updating-reason-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		}
		dh.deviceReason = "omci-admin-lock"
		//200604: ConnState improved to 'unreachable' (was not set in python-code), OperState 'unknown' seems to be best choice
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_UNREACHABLE,
			voltha.OperStatus_UNKNOWN); err != nil {
			logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		}
	}
}

//ReenableDevice unlocks the ONU and its UNI/VEIP ports (admin unlock via OMCI)
// TODO!!! Clarify usage of this method, compare above DisableDevice, usage may clarify resulting states
//         maybe it is obsolete by now
func (dh *DeviceHandler) ReenableDevice(device *voltha.Device) {
	logger.Debugw("reenable-device", log.Fields{"DeviceId": device.Id, "SerialNumber": device.SerialNumber})

	// TODO!!! ConnectStatus and OperStatus to be set here could be more accurate, for now just ...(like python code)
	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE,
		voltha.OperStatus_ACTIVE); err != nil {
		logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	// TODO!!! DeviceReason to be set here could be more accurate, for now just ...(like python code)
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "initial-mib-downloaded"); err != nil {
		logger.Errorw("error-updating-reason-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}
	dh.deviceReason = "initial-mib-downloaded"

	// enable ONU/UNI ports
	// *** should generate UniAdminStateDone event - unrelated to DeviceProcStatusUpdate!!
	//     here the result of the processing is not checked (trusted in background) *****
	if dh.pUnlockStateFsm == nil {
		dh.createUniLockFsm(false, UniAdminStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.SetSuccessEvent(UniAdminStateDone)
		dh.runUniLockFsm(false)
	}
}

func (dh *DeviceHandler) ReconcileDeviceOnuInd() {
	logger.Debugw("Reconciling onu indication", log.Fields{"device-id": dh.deviceID})

	if err := dh.pOnuTP.restoreFromOnuTpPathKvStore(context.TODO()); err == nil {
		var onu_indication oop.OnuIndication

		onu_indication.IntfId = dh.pOnuTP.sOnuPersistentData.PersIntfID
		onu_indication.OnuId = dh.pOnuTP.sOnuPersistentData.PersOnuID
		onu_indication.OperState = dh.pOnuTP.sOnuPersistentData.PersOperState
		onu_indication.AdminState = dh.pOnuTP.sOnuPersistentData.PersAdminState
		dh.create_interface(&onu_indication)
	} else {
		logger.Errorw("Restoring OnuTp-data failed  - abort reconcilement", log.Fields{"err": err, "device-id": dh.deviceID})
		dh.reconciling = false
		return
	}
}

func (dh *DeviceHandler) ReconcileDeviceTechProf() {
	logger.Debugw("start restoring tech profiles", log.Fields{"device-id": dh.deviceID})

	dh.pOnuTP.lockTpProcMutex()
	// lock hangs as long as below decoupled or other related TechProfile processing is active
	for _, uniData := range dh.pOnuTP.sOnuPersistentData.PersUniTpPath {
		//In order to allow concurrent calls to other dh instances we do not wait for execution here
		//but doing so we can not indicate problems to the caller (who does what with that then?)
		//by now we just assume straightforward successful execution
		//TODO!!! Generally: In this scheme it would be good to have some means to indicate
		//  possible problems to the caller later autonomously

		// deadline context to ensure completion of background routines waited for
		//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
		deadline := time.Now().Add(30 * time.Second) //allowed run time to finish before execution
		dctx, cancel := context.WithDeadline(context.Background(), deadline)

		dh.pOnuTP.resetProcessingErrorIndication()
		var wg sync.WaitGroup
		wg.Add(1) // for the 1 go routines to finish
		// attention: deadline completion check and wg.Done is to be done in both routines
		go dh.pOnuTP.configureUniTp(dctx, uniData.PersUniId, uniData.PersTpPath, &wg)
		//the wait.. function is responsible for tpProcMutex.Unlock()
		dh.pOnuTP.waitForTpCompletion(cancel, &wg) //wait for background process to finish and collect their result
		return
	}
	dh.pOnuTP.unlockTpProcMutex()
	//TODO: reset of reconciling-flag has always to be done in the last ReconcileDevice*() function
	dh.reconciling = false
}

func (dh *DeviceHandler) DeleteDevice(device *voltha.Device) error {
	logger.Debugw("delete-device", log.Fields{"DeviceId": device.Id, "SerialNumber": device.SerialNumber})
	if err := dh.pOnuTP.deleteOnuTpPathKvStore(context.TODO()); err != nil {
		return err
	}
	// TODO: further actions - stop metrics and FSMs, remove device ...
	return nil
}

func (dh *DeviceHandler) RebootDevice(device *voltha.Device) error {
	logger.Debugw("reboot-device", log.Fields{"DeviceId": device.Id, "SerialNumber": device.SerialNumber})
	if device.ConnectStatus != voltha.ConnectStatus_REACHABLE {
		logger.Errorw("device-unreachable", log.Fields{"DeviceId": device.Id, "SerialNumber": device.SerialNumber})
		return errors.New("device-unreachable")
	}
	dh.pOnuOmciDevice.Reboot(context.TODO())
	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_UNREACHABLE,
		voltha.OperStatus_DISCOVERED); err != nil {
		logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		return err
	}
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "rebooting-onu"); err != nil {
		logger.Errorw("error-updating-reason-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		return err
	}
	dh.deviceReason = "rebooting-onu"
	return nil
}

//  DeviceHandler methods that implement the adapters interface requests## end #########
// #####################################################################################

// ################  to be updated acc. needs of ONU Device ########################
// DeviceHandler StateMachine related state transition methods ##### begin #########

func (dh *DeviceHandler) logStateChange(e *fsm.Event) {
	logger.Debugw("Device FSM: ", log.Fields{"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst), "device-id": dh.deviceID})
}

// doStateInit provides the device update to the core
func (dh *DeviceHandler) doStateInit(e *fsm.Event) {

	logger.Debug("doStateInit-started")
	var err error

	// populate what we know.  rest comes later after mib sync
	dh.device.Root = false
	dh.device.Vendor = "OpenONU"
	dh.device.Model = "go"
	dh.device.Reason = "activating-onu"
	dh.deviceReason = "activating-onu"

	dh.logicalDeviceID = dh.deviceID // really needed - what for ??? //TODO!!!
	dh.coreProxy.DeviceUpdate(context.TODO(), dh.device)

	dh.parentId = dh.device.ParentId
	dh.ponPortNumber = dh.device.ParentPortNo

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	logger.Debugw("device-updated", log.Fields{"deviceID": dh.deviceID, "proxyAddressID": dh.ProxyAddressID,
		"proxyAddressType": dh.ProxyAddressType, "SNR": dh.device.SerialNumber,
		"ParentId": dh.parentId, "ParentPortNo": dh.ponPortNumber})

	/*
		self._pon = PonPort.create(self, self._pon_port_number)
		self._pon.add_peer(self.parent_id, self._pon_port_number)
		self.logger.debug('adding-pon-port-to-agent',
				   type=self._pon.get_port().type,
				   admin_state=self._pon.get_port().admin_state,
				   oper_status=self._pon.get_port().oper_status,
				   )
	*/
	if !dh.reconciling {
		logger.Debug("adding-pon-port")
		var ponPortNo uint32 = 1
		if dh.ponPortNumber != 0 {
			ponPortNo = dh.ponPortNumber
		}

		pPonPort := &voltha.Port{
			PortNo:     ponPortNo,
			Label:      fmt.Sprintf("pon-%d", ponPortNo),
			Type:       voltha.Port_PON_ONU,
			OperStatus: voltha.OperStatus_ACTIVE,
			Peers: []*voltha.Port_PeerPort{{DeviceId: dh.parentId, // Peer device  is OLT
				PortNo: ponPortNo}}, // Peer port is parent's port number
		}
		if err = dh.coreProxy.PortCreated(context.TODO(), dh.deviceID, pPonPort); err != nil {
			logger.Fatalf("Device FSM: PortCreated-failed-%s", err)
			e.Cancel(err)
			return
		}
	} else {
		logger.Debugw("reconciling - pon-port already added", log.Fields{"device-id": dh.deviceID})
	}
	logger.Debug("doStateInit-done")
}

// postInit setups the DeviceEntry for the conerned device
func (dh *DeviceHandler) postInit(e *fsm.Event) {

	logger.Debug("postInit-started")
	var err error
	/*
		dh.Client = oop.NewOpenoltClient(dh.clientCon)
		dh.pTransitionMap.Handle(ctx, GrpcConnected)
		return nil
	*/
	if err = dh.AddOnuDeviceEntry(context.TODO()); err != nil {
		logger.Fatalf("Device FSM: AddOnuDeviceEntry-failed-%s", err)
		e.Cancel(err)
		return
	}

	if dh.reconciling {
		logger.Debugw("Reconciling - onu indication", log.Fields{"device-id": dh.deviceID})
		go dh.ReconcileDeviceOnuInd()
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
	logger.Debug("postInit-done")
}

// doStateConnected get the device info and update to voltha core
// for comparison of the original method (not that easy to uncomment): compare here:
//  voltha-openolt-adapter/adaptercore/device_handler.go
//  -> this one obviously initiates all communication interfaces of the device ...?
func (dh *DeviceHandler) doStateConnected(e *fsm.Event) {

	logger.Debug("doStateConnected-started")
	var err error
	err = errors.New("Device FSM: function not implemented yet!")
	e.Cancel(err)
	logger.Debug("doStateConnected-done")
	return
}

// doStateUp handle the onu up indication and update to voltha core
func (dh *DeviceHandler) doStateUp(e *fsm.Event) {

	logger.Debug("doStateUp-started")
	var err error
	err = errors.New("Device FSM: function not implemented yet!")
	e.Cancel(err)
	logger.Debug("doStateUp-done")
	return

	/*
		// Synchronous call to update device state - this method is run in its own go routine
		if err := dh.coreProxy.DeviceStateUpdate(ctx, dh.device.Id, voltha.ConnectStatus_REACHABLE,
			voltha.OperStatus_ACTIVE); err != nil {
			logger.Errorw("Failed to update device with OLT UP indication", log.Fields{"deviceID": dh.device.Id, "error": err})
			return err
		}
		return nil
	*/
}

// doStateDown handle the onu down indication
func (dh *DeviceHandler) doStateDown(e *fsm.Event) {

	logger.Debug("doStateDown-started")
	var err error

	device := dh.device
	if device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Error("Failed to fetch handler device")
		e.Cancel(err)
		return
	}

	cloned := proto.Clone(device).(*voltha.Device)
	logger.Debugw("do-state-down", log.Fields{"ClonedDeviceID": cloned.Id})
	/*
		// Update the all ports state on that device to disable
		if er := dh.coreProxy.PortsStateUpdate(ctx, cloned.Id, voltha.OperStatus_UNKNOWN); er != nil {
			logger.Errorw("updating-ports-failed", log.Fields{"deviceID": device.Id, "error": er})
			return er
		}

		//Update the device oper state and connection status
		cloned.OperStatus = voltha.OperStatus_UNKNOWN
		cloned.ConnectStatus = common.ConnectStatus_UNREACHABLE
		dh.device = cloned

		if er := dh.coreProxy.DeviceStateUpdate(ctx, cloned.Id, cloned.ConnectStatus, cloned.OperStatus); er != nil {
			logger.Errorw("error-updating-device-state", log.Fields{"deviceID": device.Id, "error": er})
			return er
		}

		//get the child device for the parent device
		onuDevices, err := dh.coreProxy.GetChildDevices(ctx, dh.device.Id)
		if err != nil {
			logger.Errorw("failed to get child devices information", log.Fields{"deviceID": dh.device.Id, "error": err})
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
					"From Adapter": "openolt", "DevieType": onuDevice.Type, "DeviceID": onuDevice.Id})
				//Do not return here and continue to process other ONUs
			}
		}
		// * Discovered ONUs entries need to be cleared , since after OLT
		//   is up, it starts sending discovery indications again* /
		dh.discOnus = sync.Map{}
		logger.Debugw("do-state-down-end", log.Fields{"deviceID": device.Id})
		return nil
	*/
	err = errors.New("Device FSM: function not implemented yet!")
	e.Cancel(err)
	logger.Debug("doStateDown-done")
	return
}

// DeviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// DeviceHandler utility methods ##### begin #########

//GetOnuDeviceEntry getsthe  ONU device entry and may wait until its value is defined
func (dh *DeviceHandler) GetOnuDeviceEntry(aWait bool) *OnuDeviceEntry {
	dh.lockDevice.RLock()
	pOnuDeviceEntry := dh.pOnuOmciDevice
	if aWait && pOnuDeviceEntry == nil {
		//keep the read sema short to allow for subsequent write
		dh.lockDevice.RUnlock()
		logger.Debugw("Waiting for DeviceEntry to be set ...", log.Fields{"deviceID": dh.deviceID})
		// based on concurrent processing the deviceEntry setup may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(60 * time.Second): //timer may be discussed ...
			logger.Errorw("No valid DeviceEntry set after maxTime", log.Fields{"deviceID": dh.deviceID})
			return nil
		case <-dh.deviceEntrySet:
			logger.Debugw("devicEntry ready now - continue", log.Fields{"deviceID": dh.deviceID})
			// if written now, we can return the written value without sema
			return dh.pOnuOmciDevice
		}
	}
	dh.lockDevice.RUnlock()
	return pOnuDeviceEntry
}

//SetOnuDeviceEntry sets the ONU device entry within the handler
func (dh *DeviceHandler) SetOnuDeviceEntry(
	apDeviceEntry *OnuDeviceEntry, apOnuTp *OnuUniTechProf) error {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	dh.pOnuOmciDevice = apDeviceEntry
	dh.pOnuTP = apOnuTp
	return nil
}

//AddOnuDeviceEntry creates a new ONU device or returns the existing
func (dh *DeviceHandler) AddOnuDeviceEntry(ctx context.Context) error {
	logger.Debugw("adding-deviceEntry", log.Fields{"for deviceId": dh.deviceID})

	deviceEntry := dh.GetOnuDeviceEntry(false)
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = NewOnuDeviceEntry(ctx, dh.deviceID, dh.pOpenOnuAc.KVStoreHost,
			dh.pOpenOnuAc.KVStorePort, dh.pOpenOnuAc.KVStoreType,
			dh, dh.coreProxy, dh.AdapterProxy,
			dh.pOpenOnuAc.pSupportedFsms) //nil as FSM pointer would yield deviceEntry internal defaults ...
		onuTechProfProc := NewOnuUniTechProf(ctx, dh.deviceID, dh)
		//error treatment possible //TODO!!!
		dh.SetOnuDeviceEntry(deviceEntry, onuTechProfProc)
		// fire deviceEntry ready event to spread to possibly waiting processing
		dh.deviceEntrySet <- true
		logger.Infow("onuDeviceEntry-added", log.Fields{"for deviceId": dh.deviceID})
	} else {
		logger.Infow("onuDeviceEntry-add: Device already exists", log.Fields{"for deviceId": dh.deviceID})
	}
	// might be updated with some error handling !!!
	return nil
}

// doStateInit provides the device update to the core
func (dh *DeviceHandler) create_interface(onuind *oop.OnuIndication) error {
	logger.Debugw("create_interface-started", log.Fields{"OnuId": onuind.GetOnuId(),
		"OnuIntfId": onuind.GetIntfId(), "OnuSerialNumber": onuind.GetSerialNumber()})

	dh.pOnuIndication = onuind // let's revise if storing the pointer is sufficient...

	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
		voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
		logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}
	// It does not look to me as if makes sense to work with the real core device here, (not the stored clone)?
	// in this code the GetDevice would just make a check if the DeviceID's Device still exists in core
	// in python code it looks as the started onu_omci_device might have been updated with some new instance state of the core device
	// but I would not know why, and the go code anyway does not work with the device directly anymore in the OnuDeviceEntry
	// so let's just try to keep it simple ...
	/*
			device, err := dh.coreProxy.GetDevice(context.TODO(), dh.device.Id, dh.device.Id)
		    if err != nil || device == nil {
					//TODO: needs to handle error scenarios
				logger.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
				return errors.New("Voltha Device not found")
			}
	*/

	pDevEntry := dh.GetOnuDeviceEntry(true)
	if pDevEntry != nil {
		pDevEntry.Start(context.TODO())
	} else {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"deviceID": dh.deviceID})
		return errors.New("No valid OnuDevice")
	}
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "starting-openomci"); err != nil {
		logger.Errorw("error-DeviceReasonUpdate to starting-openomci", log.Fields{"deviceID": dh.deviceID, "error": err})
	}
	dh.deviceReason = "starting-openomci"

	/* this might be a good time for Omci Verify message?  */
	verifyExec := make(chan bool)
	omci_verify := NewOmciTestRequest(context.TODO(),
		dh.device.Id, pDevEntry.PDevOmciCC,
		true, true) //eclusive and allowFailure (anyway not yet checked)
	omci_verify.PerformOmciTest(context.TODO(), verifyExec)

	/* 	give the handler some time here to wait for the OMCi verification result
	after Timeout start and try MibUpload FSM anyway
	(to prevent stopping on just not supported OMCI verification from ONU) */
	select {
	case <-time.After(2 * time.Second):
		logger.Warn("omci start-verification timed out (continue normal)")
	case testresult := <-verifyExec:
		logger.Infow("Omci start verification done", log.Fields{"result": testresult})
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
	 *   otherwise some processing synchronisation would be required - cmp. e.g TechProfile processing
	 */
	//call MibUploadFSM - transition up to state ulStInSync
	pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		if pMibUlFsm.Is(ulStDisabled) {
			if err := pMibUlFsm.Event(ulEvStart); err != nil {
				logger.Errorw("MibSyncFsm: Can't go to state starting", log.Fields{"err": err})
				return errors.New("Can't go to state starting")
			} else {
				logger.Debugw("MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
				//Determine ONU status and start/re-start MIB Synchronization tasks
				//Determine if this ONU has ever synchronized
				if true { //TODO: insert valid check
					if err := pMibUlFsm.Event(ulEvResetMib); err != nil {
						logger.Errorw("MibSyncFsm: Can't go to state resetting_mib", log.Fields{"err": err})
						return errors.New("Can't go to state resetting_mib")
					}
				} else {
					pMibUlFsm.Event(ulEvExamineMds)
					logger.Debugw("state of MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
					//Examine the MIB Data Sync
					// callbacks to be handled:
					// Event(ulEvSuccess)
					// Event(ulEvTimeout)
					// Event(ulEvMismatch)
				}
			}
		} else {
			logger.Errorw("wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(pMibUlFsm.Current())})
			return errors.New("wrong state of MibSyncFsm")
		}
	} else {
		logger.Errorw("MibSyncFsm invalid - cannot be executed!!", log.Fields{"deviceID": dh.deviceID})
		return errors.New("cannot execut MibSync")
	}
	return nil
}

func (dh *DeviceHandler) updateInterface(onuind *oop.OnuIndication) error {
	//state checking to prevent unneeded processing (eg. on ONU 'unreachable' and 'down')
	if dh.deviceReason != "stopping-openomci" {
		logger.Debugw("updateInterface-started - stopping-device", log.Fields{"deviceID": dh.deviceID})
		//stop all running SM processing - make use of the DH-state as mirrored in the deviceReason
		pDevEntry := dh.GetOnuDeviceEntry(false)
		if pDevEntry == nil {
			logger.Errorw("No valid OnuDevice -aborting", log.Fields{"deviceID": dh.deviceID})
			return errors.New("No valid OnuDevice")
		}

		switch dh.deviceReason {
		case "starting-openomci":
			{ //MIBSync FSM may run
				pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
				if pMibUlFsm != nil {
					pMibUlFsm.Event(ulEvStop) //TODO!! verify if MibSyncFsm stop-processing is sufficient (to allow it again afterwards)
				}
			}
		case "discovery-mibsync-complete":
			{ //MibDownload may run
				pMibDlFsm := pDevEntry.pMibDownloadFsm.pFsm
				if pMibDlFsm != nil {
					pMibDlFsm.Event(dlEvReset)
				}
			}
		default:
			{
				//port lock/unlock FSM's may be active
				if dh.pUnlockStateFsm != nil {
					dh.pUnlockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
				}
				if dh.pLockStateFsm != nil {
					dh.pLockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
				}
				//techProfile related PonAniConfigFsm FSM may be active
				// maybe encapsulated as OnuTP method - perhaps later in context of module splitting
				if dh.pOnuTP.pAniConfigFsm != nil {
					dh.pOnuTP.pAniConfigFsm.pAdaptFsm.pFsm.Event(aniEvReset)
				}
			}
			//TODO!!! care about PM/Alarm processing once started
		}
		//TODO: from here the deviceHandler FSM itself may be stuck in some of the initial states
		//  (mainly the still seperate 'Event states')
		//  so it is questionable, how this is resolved after some possible re-enable
		//  assumption there is obviously, that the system may continue with some 'after "mib-download-done" state'

		//stop/remove(?) the device entry
		pDevEntry.Stop(context.TODO()) //maybe some more sophisticated context treatment should be used here?

		//TODO!!! remove existing traffic profiles
		/* from py code, if TP's exist, remove them - not yet implemented
		self._tp = dict()
		# Let TP download happen again
		for uni_id in self._tp_service_specific_task:
			self._tp_service_specific_task[uni_id].clear()
		for uni_id in self._tech_profile_download_done:
			self._tech_profile_download_done[uni_id].clear()
		*/

		dh.disableUniPortStateUpdate()

		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "stopping-openomci"); err != nil {
			logger.Errorw("error-DeviceReasonUpdate to 'stopping-openomci'",
				log.Fields{"deviceID": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
		dh.deviceReason = "stopping-openomci"

		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_UNREACHABLE, voltha.OperStatus_DISCOVERED); err != nil {
			logger.Errorw("error-updating-device-state unreachable-discovered",
				log.Fields{"deviceID": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
	} else {
		logger.Debugw("updateInterface - device already stopped", log.Fields{"deviceID": dh.deviceID})
	}
	return nil
}

//DeviceProcStatusUpdate evaluates possible processing events and initiates according next activities
func (dh *DeviceHandler) DeviceProcStatusUpdate(dev_Event OnuDeviceEvent) {
	switch dev_Event {
	case MibDatabaseSync:
		{
			logger.Debugw("MibInSync event received", log.Fields{"deviceID": dh.deviceID})
			//initiate DevStateUpdate
			if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "discovery-mibsync-complete"); err != nil {
				logger.Errorw("error-DeviceReasonUpdate to 'mibsync-complete'", log.Fields{
					"deviceID": dh.deviceID, "error": err})
			} else {
				logger.Infow("dev reason updated to 'MibSync complete'", log.Fields{"deviceID": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "discovery-mibsync-complete"

			i := uint8(0) //UNI Port limit: see MaxUnisPerOnu (by now 16) (OMCI supports max 255 p.b.)
			pDevEntry := dh.GetOnuDeviceEntry(false)
			if unigInstKeys := pDevEntry.pOnuDB.GetSortedInstKeys(me.UniGClassID); len(unigInstKeys) > 0 {
				for _, mgmtEntityId := range unigInstKeys {
					logger.Debugw("Add UNI port for stored UniG instance:", log.Fields{
						"deviceId": dh.deviceID, "UnigMe EntityID": mgmtEntityId})
					dh.addUniPort(mgmtEntityId, i, UniPPTP)
					i++
				}
			} else {
				logger.Debugw("No UniG instances found", log.Fields{"deviceId": dh.deviceID})
			}
			if veipInstKeys := pDevEntry.pOnuDB.GetSortedInstKeys(me.VirtualEthernetInterfacePointClassID); len(veipInstKeys) > 0 {
				for _, mgmtEntityId := range veipInstKeys {
					logger.Debugw("Add VEIP acc. to stored VEIP instance:", log.Fields{
						"deviceId": dh.deviceID, "VEIP EntityID": mgmtEntityId})
					dh.addUniPort(mgmtEntityId, i, UniVEIP)
					i++
				}
			} else {
				logger.Debugw("No VEIP instances found", log.Fields{"deviceId": dh.deviceID})
			}
			if i == 0 {
				logger.Warnw("No PPTP instances found", log.Fields{"deviceId": dh.deviceID})
			}

			/* 200605: lock processing after initial MIBUpload removed now as the ONU should be in the lock state per default here
			 *  left the code here as comment in case such processing should prove needed unexpectedly
					// Init Uni Ports to Admin locked state
					// maybe not really needed here as UNI ports should be locked by default, but still left as available in python code
					// *** should generate UniLockStateDone event *****
					if dh.pLockStateFsm == nil {
						dh.createUniLockFsm(true, UniLockStateDone)
					} else { //LockStateFSM already init
						dh.pLockStateFsm.SetSuccessEvent(UniLockStateDone)
						dh.runUniLockFsm(true)
					}
				}
			case UniLockStateDone:
				{
					logger.Infow("UniLockStateDone event: Starting MIB download", log.Fields{"deviceID": dh.deviceID})
			* lockState processing commented out
			*/
			/*  Mib download procedure -
			***** should run over 'downloaded' state and generate MibDownloadDone event *****
			 */
			pMibDlFsm := pDevEntry.pMibDownloadFsm.pFsm
			if pMibDlFsm != nil {
				if pMibDlFsm.Is(dlStDisabled) {
					if err := pMibDlFsm.Event(dlEvStart); err != nil {
						logger.Errorw("MibDownloadFsm: Can't go to state starting", log.Fields{"err": err})
						// maybe try a FSM reset and then again ... - TODO!!!
					} else {
						logger.Debugw("MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
						// maybe use more specific states here for the specific download steps ...
						if err := pMibDlFsm.Event(dlEvCreateGal); err != nil {
							logger.Errorw("MibDownloadFsm: Can't start CreateGal", log.Fields{"err": err})
						} else {
							logger.Debugw("state of MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
							//Begin MIB data download (running autonomously)
						}
					}
				} else {
					logger.Errorw("wrong state of MibDownloadFsm - want: disabled", log.Fields{"have": string(pMibDlFsm.Current())})
					// maybe try a FSM reset and then again ... - TODO!!!
				}
				/***** Mib download started */
			} else {
				logger.Errorw("MibDownloadFsm invalid - cannot be executed!!", log.Fields{"deviceID": dh.deviceID})
			}
		}
	case MibDownloadDone:
		{
			logger.Debugw("MibDownloadDone event received", log.Fields{"deviceID": dh.deviceID})
			//initiate DevStateUpdate
			if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
				voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVE); err != nil {
				logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
			} else {
				logger.Debugw("dev state updated to 'Oper.Active'", log.Fields{"deviceID": dh.deviceID})
			}

			if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "initial-mib-downloaded"); err != nil {
				logger.Errorw("error-DeviceReasonUpdate to 'initial-mib-downloaded'",
					log.Fields{"deviceID": dh.deviceID, "error": err})
			} else {
				logger.Infow("dev reason updated to 'initial-mib-downloaded'", log.Fields{"deviceID": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "initial-mib-downloaded"
			// *** should generate UniUnlockStateDone event *****
			if dh.pUnlockStateFsm == nil {
				dh.createUniLockFsm(false, UniUnlockStateDone)
			} else { //UnlockStateFSM already init
				dh.pUnlockStateFsm.SetSuccessEvent(UniUnlockStateDone)
				dh.runUniLockFsm(false)
			}
			if dh.reconciling {
				logger.Debugw("Reconciling - restore tech profile", log.Fields{"device-id": dh.device})
				go dh.ReconcileDeviceTechProf()
				//TODO: further actions e.g. restore flows, metrics, ...
			}
		}
	case UniUnlockStateDone:
		{
			go dh.enableUniPortStateUpdate() //cmp python yield self.enable_ports()

			logger.Infow("UniUnlockStateDone event: Sending OnuUp event", log.Fields{"deviceID": dh.deviceID})
			raisedTs := time.Now().UnixNano()
			go dh.sendOnuOperStateEvent(voltha.OperStatus_ACTIVE, dh.deviceID, raisedTs) //cmp python onu_active_event
		}
	case OmciAniConfigDone:
		{
			logger.Debugw("OmciAniConfigDone event received", log.Fields{"deviceID": dh.deviceID})
			//TODO!: it might be needed to check some 'cached' pending flow configuration (vlan setting)
			//  - to consider with outstanding flow implementation
			// attention: the device reason update is done based on ONU-UNI-Port related activity
			//  - which may cause some inconsistency
			if dh.deviceReason != "tech-profile-config-download-success" {
				// which may be the case from some previous actvity on another UNI Port of the ONU
				if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "tech-profile-config-download-success"); err != nil {
					logger.Errorw("error-DeviceReasonUpdate to 'tech-profile-config-download-success'",
						log.Fields{"deviceID": dh.deviceID, "error": err})
				} else {
					logger.Infow("update dev reason to 'tech-profile-config-download-success'",
						log.Fields{"deviceID": dh.deviceID})
				}
				//set internal state anyway - as it was done
				dh.deviceReason = "tech-profile-config-download-success"
			}
		}
	default:
		{
			logger.Warnw("unhandled-device-event", log.Fields{"deviceID": dh.deviceID, "event": dev_Event})
		}
	} //switch
}

func (dh *DeviceHandler) addUniPort(a_uniInstNo uint16, a_uniId uint8, a_portType UniPortType) {
	// parameters are IntfId, OnuId, uniId
	uniNo := MkUniPortNum(dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(),
		uint32(a_uniId))
	if _, present := dh.uniEntityMap[uniNo]; present {
		logger.Warnw("onuUniPort-add: Port already exists", log.Fields{"for InstanceId": a_uniInstNo})
	} else {
		//with arguments a_uniId, a_portNo, a_portType
		pUniPort := NewOnuUniPort(a_uniId, uniNo, a_uniInstNo, a_portType)
		if pUniPort == nil {
			logger.Warnw("onuUniPort-add: Could not create Port", log.Fields{"for InstanceId": a_uniInstNo})
		} else {
			//store UniPort with the System-PortNumber key
			dh.uniEntityMap[uniNo] = pUniPort
			if !dh.reconciling {
				// create announce the UniPort to the core as VOLTHA Port object
				if err := pUniPort.CreateVolthaPort(dh); err == nil {
					logger.Infow("onuUniPort-added", log.Fields{"for PortNo": uniNo})
				} //error logging already within UniPort method
			} else {
				logger.Debugw("reconciling - onuUniPort already added", log.Fields{"for PortNo": uniNo, "device-id": dh.deviceID})
			}
		}
	}
}

// enableUniPortStateUpdate enables UniPortState and update core port state accordingly
func (dh *DeviceHandler) enableUniPortStateUpdate() {
	//  py code was updated 2003xx to activate the real ONU UNI ports per OMCI (VEIP or PPTP)
	//    but towards core only the first port active state is signalled
	//    with following remark:
	//       # TODO: for now only support the first UNI given no requirement for multiple uni yet. Also needed to reduce flow
	//       #  load on the core

	// lock_ports(false) as done in py code here is shifted to separate call from devicevent processing

	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniId)&ActiveUniPortStateUpdateMask == (1 << uniPort.uniId) {
			logger.Infow("onuUniPort-forced-OperState-ACTIVE", log.Fields{"for PortNo": uniNo})
			uniPort.SetOperState(vc.OperStatus_ACTIVE)
			//maybe also use getter functions on uniPort - perhaps later ...
			go dh.coreProxy.PortStateUpdate(context.TODO(), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
		}
	}
}

// Disable UniPortState and update core port state accordingly
func (dh *DeviceHandler) disableUniPortStateUpdate() {
	// compare enableUniPortStateUpdate() above
	//   -> use current restriction to operate only on first UNI port as inherited from actual Py code
	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniId)&ActiveUniPortStateUpdateMask == (1 << uniPort.uniId) {
			logger.Infow("onuUniPort-forced-OperState-UNKNOWN", log.Fields{"for PortNo": uniNo})
			uniPort.SetOperState(vc.OperStatus_UNKNOWN)
			//maybe also use getter functions on uniPort - perhaps later ...
			go dh.coreProxy.PortStateUpdate(context.TODO(), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
		}
	}
}

// ONU_Active/Inactive announcement on system KAFKA bus
// tried to re-use procedure of oltUpDownIndication from openolt_eventmgr.go with used values from Py code
func (dh *DeviceHandler) sendOnuOperStateEvent(a_OperState vc.OperStatus_Types, a_deviceID string, raisedTs int64) {
	var de voltha.DeviceEvent
	eventContext := make(map[string]string)
	//Populating event context
	//  assume giving ParentId in GetDevice twice really gives the ParentDevice (there is no GetParentDevice()...)
	parentDevice, err := dh.coreProxy.GetDevice(context.TODO(), dh.parentId, dh.parentId)
	if err != nil || parentDevice == nil {
		logger.Errorw("Failed to fetch parent device for OnuEvent",
			log.Fields{"parentId": dh.parentId, "err": err})
	}
	oltSerialNumber := parentDevice.SerialNumber

	eventContext["pon-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.IntfId), 10)
	eventContext["onu-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.OnuId), 10)
	eventContext["serial-number"] = dh.device.SerialNumber
	eventContext["olt_serial_number"] = oltSerialNumber
	eventContext["device_id"] = a_deviceID
	eventContext["registration_id"] = a_deviceID //py: string(device_id)??
	logger.Debugw("prepare ONU_ACTIVATED event",
		log.Fields{"DeviceId": a_deviceID, "EventContext": eventContext})

	/* Populating device event body */
	de.Context = eventContext
	de.ResourceId = a_deviceID
	if a_OperState == voltha.OperStatus_ACTIVE {
		de.DeviceEventName = fmt.Sprintf("%s_%s", cOnuActivatedEvent, "RAISE_EVENT")
		de.Description = fmt.Sprintf("%s Event - %s - %s",
			cEventObjectType, cOnuActivatedEvent, "Raised")
	} else {
		de.DeviceEventName = fmt.Sprintf("%s_%s", cOnuActivatedEvent, "CLEAR_EVENT")
		de.Description = fmt.Sprintf("%s Event - %s - %s",
			cEventObjectType, cOnuActivatedEvent, "Cleared")
	}
	/* Send event to KAFKA */
	if err := dh.EventProxy.SendDeviceEvent(&de, equipment, pon, raisedTs); err != nil {
		logger.Warnw("could not send ONU_ACTIVATED event",
			log.Fields{"DeviceId": a_deviceID, "error": err})
	}
	logger.Debugw("ONU_ACTIVATED event sent to KAFKA",
		log.Fields{"DeviceId": a_deviceID, "with-EventName": de.DeviceEventName})
}

// createUniLockFsm initialises and runs the UniLock FSM to transfer teh OMCi related commands for port lock/unlock
func (dh *DeviceHandler) createUniLockFsm(aAdminState bool, devEvent OnuDeviceEvent) {
	chLSFsm := make(chan Message, 2048)
	var sFsmName string
	if aAdminState == true {
		logger.Infow("createLockStateFSM", log.Fields{"deviceID": dh.deviceID})
		sFsmName = "LockStateFSM"
	} else {
		logger.Infow("createUnlockStateFSM", log.Fields{"deviceID": dh.deviceID})
		sFsmName = "UnLockStateFSM"
	}

	pDevEntry := dh.GetOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"deviceID": dh.deviceID})
		return
	}
	pLSFsm := NewLockStateFsm(pDevEntry.PDevOmciCC, aAdminState, devEvent,
		sFsmName, dh.deviceID, chLSFsm)
	if pLSFsm != nil {
		if aAdminState == true {
			dh.pLockStateFsm = pLSFsm
		} else {
			dh.pUnlockStateFsm = pLSFsm
		}
		dh.runUniLockFsm(aAdminState)
	} else {
		logger.Errorw("LockStateFSM could not be created - abort!!", log.Fields{"deviceID": dh.deviceID})
	}
}

// runUniLockFsm starts the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *DeviceHandler) runUniLockFsm(aAdminState bool) {
	/*  Uni Port lock/unlock procedure -
	 ***** should run via 'adminDone' state and generate the argument requested event *****
	 */
	var pLSStatemachine *fsm.FSM
	if aAdminState == true {
		pLSStatemachine = dh.pLockStateFsm.pAdaptFsm.pFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pUnlockStateFsm != nil) &&
			(dh.pUnlockStateFsm.pAdaptFsm.pFsm.Current() != uniStDisabled) {
			dh.pUnlockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
		}
	} else {
		pLSStatemachine = dh.pUnlockStateFsm.pAdaptFsm.pFsm
		//make sure the opposite FSM is not running and if so, terminate it as not relevant anymore
		if (dh.pLockStateFsm != nil) &&
			(dh.pLockStateFsm.pAdaptFsm.pFsm.Current() != uniStDisabled) {
			dh.pLockStateFsm.pAdaptFsm.pFsm.Event(uniEvReset)
		}
	}
	if pLSStatemachine != nil {
		if pLSStatemachine.Is(uniStDisabled) {
			if err := pLSStatemachine.Event(uniEvStart); err != nil {
				logger.Warnw("LockStateFSM: can't start", log.Fields{"err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				/***** LockStateFSM started */
				logger.Debugw("LockStateFSM started", log.Fields{
					"state": pLSStatemachine.Current(), "deviceID": dh.deviceID})
			}
		} else {
			logger.Warnw("wrong state of LockStateFSM - want: disabled", log.Fields{
				"have": pLSStatemachine.Current(), "deviceID": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
	} else {
		logger.Errorw("LockStateFSM StateMachine invalid - cannot be executed!!", log.Fields{"deviceID": dh.deviceID})
		// maybe try a FSM reset and then again ... - TODO!!!
	}
}

//SetBackend provides a DB backend for the specified path on the existing KV client
func (dh *DeviceHandler) SetBackend(aBasePathKvStore string) *db.Backend {
	addr := dh.pOpenOnuAc.KVStoreHost + ":" + strconv.Itoa(dh.pOpenOnuAc.KVStorePort)
	logger.Debugw("SetKVStoreBackend", log.Fields{"IpTarget": addr,
		"BasePathKvStore": aBasePathKvStore, "deviceId": dh.deviceID})
	kvbackend := &db.Backend{
		Client:    dh.pOpenOnuAc.kvClient,
		StoreType: dh.pOpenOnuAc.KVStoreType,
		/* address config update acc. to [VOL-2736] */
		Address:    addr,
		Timeout:    dh.pOpenOnuAc.KVStoreTimeout,
		PathPrefix: aBasePathKvStore}

	return kvbackend
}
