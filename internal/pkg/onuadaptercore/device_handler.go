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
	flow "github.com/opencord/voltha-lib-go/v3/pkg/flows"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	vc "github.com/opencord/voltha-protos/v3/go/common"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	"github.com/opencord/voltha-protos/v3/go/openflow_13"
	of "github.com/opencord/voltha-protos/v3/go/openflow_13"
	ofp "github.com/opencord/voltha-protos/v3/go/openflow_13"
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
	EventProxy   adapterif.EventProxy

	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	//pPonPort        *voltha.Port
	deviceEntrySet  chan bool //channel for DeviceEntry set event
	pOnuOmciDevice  *OnuDeviceEntry
	pOnuTP          *onuUniTechProf
	exitChannel     chan int
	lockDevice      sync.RWMutex
	pOnuIndication  *oop.OnuIndication
	deviceReason    string
	pLockStateFsm   *lockStateFsm
	pUnlockStateFsm *lockStateFsm

	//flowMgr       *OpenOltFlowMgr
	//eventMgr      *OpenOltEventMgr
	//resourceMgr   *rsrcMgr.OpenOltResourceMgr

	//discOnus sync.Map
	//onus     sync.Map
	//portStats          *OpenOltStatisticsMgr
	//metrics            *pmmetrics.PmMetrics
	stopCollector              chan bool
	stopHeartbeatCheck         chan bool
	activePorts                sync.Map
	uniEntityMap               map[uint32]*onuUniPort
	UniVlanConfigFsmMap        map[uint8]*UniVlanConfigFsm
	reconciling                bool
	ReadyForSpecificOmciConfig bool
}

//newDeviceHandler creates a new device handler
func newDeviceHandler(cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep adapterif.EventProxy, device *voltha.Device, adapter *OpenONUAC) *deviceHandler {
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
	dh.stopCollector = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	dh.activePorts = sync.Map{}
	//TODO initialize the support classes.
	dh.uniEntityMap = make(map[uint32]*onuUniPort)
	dh.UniVlanConfigFsmMap = make(map[uint8]*UniVlanConfigFsm)
	dh.reconciling = false
	dh.ReadyForSpecificOmciConfig = false

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
func (dh *deviceHandler) start(ctx context.Context) {
	logger.Debugw("starting-device-handler", log.Fields{"device": dh.device, "device-id": dh.deviceID})
	// Add the initial device to the local model
	logger.Debug("device-handler-started")
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

//adoptOrReconcileDevice adopts the OLT device
func (dh *deviceHandler) adoptOrReconcileDevice(ctx context.Context, device *voltha.Device) {
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

}

func (dh *deviceHandler) processInterAdapterOMCIReqMessage(msg *ic.InterAdapterMessage) error {
	msgBody := msg.GetBody()
	omciMsg := &ic.InterAdapterOmciMessage{}
	if err := ptypes.UnmarshalAny(msgBody, omciMsg); err != nil {
		logger.Warnw("cannot-unmarshal-omci-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	//assuming omci message content is hex coded!
	// with restricted output of 16(?) bytes would be ...omciMsg.Message[:16]
	logger.Debugw("inter-adapter-recv-omci", log.Fields{
		"device-id": dh.deviceID, "RxOmciMessage": hex.EncodeToString(omciMsg.Message)})
	//receive_message(omci_msg.message)
	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry != nil {
		if pDevEntry.PDevOmciCC != nil {
			return pDevEntry.PDevOmciCC.receiveMessage(context.TODO(), omciMsg.Message)
		}
		logger.Debugw("omciCC not ready to receive omci messages - incoming omci message ignored", log.Fields{"rxMsg": omciMsg.Message})
	}
	logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
	return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
}

func (dh *deviceHandler) processInterAdapterONUIndReqMessage(msg *ic.InterAdapterMessage) error {
	msgBody := msg.GetBody()
	onuIndication := &oop.OnuIndication{}
	if err := ptypes.UnmarshalAny(msgBody, onuIndication); err != nil {
		logger.Warnw("cannot-unmarshal-onu-indication-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	onuOperstate := onuIndication.GetOperState()
	logger.Debugw("inter-adapter-recv-onu-ind", log.Fields{"device-id": dh.deviceID,
		"OnuId":      onuIndication.GetOnuId(),
		"AdminState": onuIndication.GetAdminState(), "OperState": onuOperstate,
		"SNR": onuIndication.GetSerialNumber()})

	//interface related functions might be error checked ....
	if onuOperstate == "up" {
		_ = dh.createInterface(onuIndication)
	} else if (onuOperstate == "down") || (onuOperstate == "unreachable") {
		_ = dh.updateInterface(onuIndication)
	} else {
		logger.Errorw("unknown-onu-indication operState", log.Fields{"OnuId": onuIndication.GetOnuId()})
		return fmt.Errorf("invalidOperState: %s, %s", onuOperstate, dh.deviceID)
	}
	return nil
}

func (dh *deviceHandler) processInterAdapterTechProfileDownloadReqMessage(
	msg *ic.InterAdapterMessage) error {

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw("onuTechProf instance not set up for DLMsg request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DLMsg request while onuTechProf instance not setup: %s", dh.deviceID)
	}
	if !dh.ReadyForSpecificOmciConfig {
		logger.Warnw("TechProf-set rejected: improper device state", log.Fields{"device-id": dh.deviceID,
			"device-state": dh.deviceReason})
		return fmt.Errorf("improper device state %s on device %s", dh.deviceReason, dh.deviceID)
	}
	//previous state test here was just this one, now extended for more states to reject the SetRequest:
	// at least 'mib-downloaded' should be reached for processing of this specific ONU configuration
	//  if (dh.deviceReason == "stopping-openomci") || (dh.deviceReason == "omci-admin-lock")

	msgBody := msg.GetBody()
	techProfMsg := &ic.InterAdapterTechProfileDownloadMessage{}
	if err := ptypes.UnmarshalAny(msgBody, techProfMsg); err != nil {
		logger.Warnw("cannot-unmarshal-techprof-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	// we have to lock access to TechProfile processing based on different messageType calls or
	// even to fast subsequent calls of the same messageType as well as OnuKVStore processing due
	// to possible concurrent access by flow processing
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()
	pDevEntry.lockOnuKVStoreMutex()
	defer pDevEntry.unlockOnuKVStoreMutex()

	if techProfMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			techProfMsg.UniId, dh.deviceID))
	}
	uniID := uint8(techProfMsg.UniId)
	tpID, err := GetTpIDFromTpPath(techProfMsg.Path)
	if err != nil {
		logger.Errorw("error-parsing-tpid-from-tppath", log.Fields{"err": err, "tp-path": techProfMsg.Path})
		return err
	}

	if bTpModify := pDevEntry.updateOnuUniTpPath(uniID, uint8(tpID), techProfMsg.Path); bTpModify {
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
		go dh.pOnuTP.configureUniTp(dctx, uniID, techProfMsg.Path, &wg)
		go pDevEntry.updateOnuKvStore(dctx, &wg)
		dh.waitForCompletion(cancel, &wg, "TechProfDwld") //wait for background process to finish

		return dh.combineErrorStrings(dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID), pDevEntry.getKvProcessingErrorIndication())
	}
	// no change, nothing really to do - return success
	return nil
}

func (dh *deviceHandler) processInterAdapterDeleteGemPortReqMessage(
	msg *ic.InterAdapterMessage) error {

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw("onuTechProf instance not set up for DelGem request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DelGem request while onuTechProf instance not setup: %s", dh.deviceID)
	}

	msgBody := msg.GetBody()
	delGemPortMsg := &ic.InterAdapterDeleteGemPortMessage{}
	if err := ptypes.UnmarshalAny(msgBody, delGemPortMsg); err != nil {
		logger.Warnw("cannot-unmarshal-delete-gem-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()
	pDevEntry.lockOnuKVStoreMutex()
	defer pDevEntry.unlockOnuKVStoreMutex()

	if delGemPortMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			delGemPortMsg.UniId, dh.deviceID))
	}
	uniID := uint8(delGemPortMsg.UniId)
	tpID, err := GetTpIDFromTpPath(delGemPortMsg.TpPath)
	if err != nil {
		logger.Errorw("error-extracting-tp-id-from-tp-path", log.Fields{"err": err, "tp-path": delGemPortMsg.TpPath})
		return err
	}

	//a removal of some GemPort would never remove the complete TechProfile entry (done on T-Cont)

	// deadline context to ensure completion of background routines waited for
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	dh.pOnuTP.resetTpProcessingErrorIndication(uniID, tpID)

	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish
	go dh.pOnuTP.deleteTpResource(dctx, uniID, delGemPortMsg.TpPath,
		cResourceGemPort, delGemPortMsg.GemPortId, &wg)
	dh.waitForCompletion(cancel, &wg, "GemDelete") //wait for background process to finish

	return dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID)
}

func (dh *deviceHandler) processInterAdapterDeleteTcontReqMessage(
	msg *ic.InterAdapterMessage) error {

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if dh.pOnuTP == nil {
		//should normally not happen ...
		logger.Warnw("onuTechProf instance not set up for DelTcont request - ignoring request",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("techProfile DelTcont request while onuTechProf instance not setup: %s", dh.deviceID)
	}

	msgBody := msg.GetBody()
	delTcontMsg := &ic.InterAdapterDeleteTcontMessage{}
	if err := ptypes.UnmarshalAny(msgBody, delTcontMsg); err != nil {
		logger.Warnw("cannot-unmarshal-delete-tcont-msg-body", log.Fields{
			"device-id": dh.deviceID, "error": err})
		return err
	}

	//compare TECH_PROFILE_DOWNLOAD_REQUEST
	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()
	pDevEntry.lockOnuKVStoreMutex()
	defer pDevEntry.unlockOnuKVStoreMutex()

	if delTcontMsg.UniId > 255 {
		return fmt.Errorf(fmt.Sprintf("received UniId value exceeds range: %d, device-id: %s",
			delTcontMsg.UniId, dh.deviceID))
	}
	uniID := uint8(delTcontMsg.UniId)
	tpPath := delTcontMsg.TpPath
	tpID, err := GetTpIDFromTpPath(tpPath)
	if err != nil {
		logger.Errorw("error-extracting-tp-id-from-tp-path", log.Fields{"err": err, "tp-path": tpPath})
		return err
	}

	if bTpModify := pDevEntry.updateOnuUniTpPath(uniID, tpID, ""); bTpModify {
		// deadline context to ensure completion of background routines waited for
		deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
		dctx, cancel := context.WithDeadline(context.Background(), deadline)

		dh.pOnuTP.resetTpProcessingErrorIndication(uniID, tpID)
		pDevEntry.resetKvProcessingErrorIndication()

		var wg sync.WaitGroup
		wg.Add(2) // for the 2 go routines to finish
		go dh.pOnuTP.deleteTpResource(dctx, uniID, delTcontMsg.TpPath,
			cResourceTcont, delTcontMsg.AllocId, &wg)
		// Removal of the tcont/alloc id mapping represents the removal of the tech profile
		go pDevEntry.updateOnuKvStore(dctx, &wg)
		dh.waitForCompletion(cancel, &wg, "TContDelete") //wait for background process to finish

		return dh.combineErrorStrings(dh.pOnuTP.getTpProcessingErrorIndication(uniID, tpID), pDevEntry.getKvProcessingErrorIndication())
	}
	return nil
}

//processInterAdapterMessage sends the proxied messages to the target device
// If the proxy address is not found in the unmarshalled message, it first fetches the onu device for which the message
// is meant, and then send the unmarshalled omci message to this onu
func (dh *deviceHandler) processInterAdapterMessage(msg *ic.InterAdapterMessage) error {
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
			return dh.processInterAdapterOMCIReqMessage(msg)
		}
	case ic.InterAdapterMessageType_ONU_IND_REQUEST:
		{
			return dh.processInterAdapterONUIndReqMessage(msg)
		}
	case ic.InterAdapterMessageType_TECH_PROFILE_DOWNLOAD_REQUEST:
		{
			return dh.processInterAdapterTechProfileDownloadReqMessage(msg)
		}
	case ic.InterAdapterMessageType_DELETE_GEM_PORT_REQUEST:
		{
			return dh.processInterAdapterDeleteGemPortReqMessage(msg)

		}
	case ic.InterAdapterMessageType_DELETE_TCONT_REQUEST:
		{
			return dh.processInterAdapterDeleteTcontReqMessage(msg)
		}
	default:
		{
			logger.Errorw("inter-adapter-unhandled-type", log.Fields{
				"msgType": msg.Header.Type, "device-id": dh.deviceID})
			return fmt.Errorf("inter-adapter-unhandled-type: %d, %s", msg.Header.Type, dh.deviceID)
		}
	}
}

//FlowUpdateIncremental removes and/or adds the flow changes on a given device
func (dh *deviceHandler) FlowUpdateIncremental(apOfFlowChanges *openflow_13.FlowChanges,
	apOfGroupChanges *openflow_13.FlowGroupChanges, apFlowMetaData *voltha.FlowMetadata) error {
	logger.Debugw("FlowUpdateIncremental started", log.Fields{"device-id": dh.deviceID})

	var retError error = nil
	//Remove flows (always remove flows first - remove old and add new with same cookie may be part of the same request)
	if apOfFlowChanges.ToRemove != nil {
		for _, flowItem := range apOfFlowChanges.ToRemove.Items {
			if flowItem.GetCookie() == 0 {
				logger.Warnw("flow-remove no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-remove no cookie, device-id %s", dh.deviceID)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw("flow-remove inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-remove inPort invalid, device-id %s", dh.deviceID)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.deviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow, not regarded as error, just ignored
				logger.Debugw("flow-remove for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *onuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw("flow-remove inPort not found in UniPorts: ignore and continuing on checking further flows",
						log.Fields{"device-id": dh.deviceID, "inPort": flowInPort})
					retError = fmt.Errorf("flow-remove inPort not found in UniPorts, inPort %d, device-id %s",
						flowInPort, dh.deviceID)
					continue
				}
				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw("flow-remove port indications", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.name})
				err := dh.removeFlowItemFromUniPort(flowItem, loUniPort)
				//try next flow after processing error
				if err != nil {
					logger.Warnw("flow-remove processing error: continuing on checking further flows",
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
				logger.Debugw("incremental flow-add no cookie: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-add no cookie, device-id %s", dh.deviceID)
				continue
			}
			flowInPort := flow.GetInPort(flowItem)
			if flowInPort == uint32(of.OfpPortNo_OFPP_INVALID) {
				logger.Warnw("flow-add inPort invalid: ignore and continuing on checking further flows", log.Fields{"device-id": dh.deviceID})
				retError = fmt.Errorf("flow-add inPort invalid, device-id %s", dh.deviceID)
				continue
				//return fmt.Errorf("flow inPort invalid: %s", dh.deviceID)
			} else if flowInPort == dh.ponPortNumber {
				//this is some downstream flow
				logger.Debugw("flow-add for downstream: ignore and continuing on checking further flows", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort})
				continue
			} else {
				// this is the relevant upstream flow
				var loUniPort *onuUniPort
				if uniPort, exist := dh.uniEntityMap[flowInPort]; exist {
					loUniPort = uniPort
				} else {
					logger.Warnw("flow-add inPort not found in UniPorts: ignore and continuing on checking further flows",
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
					logger.Warnw("flow-add rejected: improper device state", log.Fields{"device-id": dh.deviceID,
						"last device-reason": dh.deviceReason})
					return fmt.Errorf("improper device state on device %s", dh.deviceID)
				}

				flowOutPort := flow.GetOutPort(flowItem)
				logger.Debugw("flow-add port indications", log.Fields{
					"device-id": dh.deviceID, "inPort": flowInPort, "outPort": flowOutPort,
					"uniPortName": loUniPort.name})
				err := dh.addFlowItemToUniPort(flowItem, loUniPort)
				//try next flow after processing error
				if err != nil {
					logger.Warnw("flow-add processing error: continuing on checking further flows",
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
func (dh *deviceHandler) disableDevice(device *voltha.Device) {
	logger.Debugw("disable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})

	//admin-lock reason can also be used uniquely for setting the DeviceState accordingly
	//note that disableDevice sequences in some 'ONU active' state may yield also
	// "tech...delete-success" or "omci-flow-deleted" according to further received requests in the end
	// - inblock state checking to prevent possibly unneeded processing (on command repitition)
	if dh.deviceReason != "omci-admin-lock" {
		//disable-device shall be just a UNi/ONU-G related admin state setting
		//all other configurations/FSM's shall not be impacted and shall execute as required by the system

		if dh.ReadyForSpecificOmciConfig {
			// disable UNI ports/ONU
			// *** should generate UniDisableStateDone event - used to disable the port(s) on success
			if dh.pLockStateFsm == nil {
				dh.createUniLockFsm(true, UniDisableStateDone)
			} else { //LockStateFSM already init
				dh.pLockStateFsm.setSuccessEvent(UniDisableStateDone)
				dh.runUniLockFsm(true)
			}
		} else {
			logger.Debugw("DeviceStateUpdate upon disable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
				"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.deviceID})
			if err := dh.coreProxy.DeviceStateUpdate(context.TODO(),
				dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_UNKNOWN); err != nil {
				//TODO with VOL-3045/VOL-3046: return the error and stop further processing
				logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
			}

			logger.Debugw("DeviceReasonUpdate upon disable", log.Fields{
				"reason": "omci-admin-lock", "device-id": dh.deviceID})
			// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
			if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "omci-admin-lock"); err != nil {
				//TODO with VOL-3045/VOL-3046: return the error and stop further processing
				logger.Errorw("error-updating-reason-state", log.Fields{"device-id": dh.deviceID, "error": err})
			}
			dh.deviceReason = "omci-admin-lock"
		}
	}
}

//reEnableDevice unlocks the ONU and its UNI/VEIP ports (admin unlock via OMCI)
func (dh *deviceHandler) reEnableDevice(device *voltha.Device) {
	logger.Debugw("reenable-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})

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
		dh.createUniLockFsm(false, UniEnableStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.setSuccessEvent(UniEnableStateDone)
		dh.runUniLockFsm(false)
	}
}

func (dh *deviceHandler) reconcileDeviceOnuInd() {
	logger.Debugw("reconciling - simulate onu indication", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	if err := pDevEntry.restoreDataFromOnuKvStore(context.TODO()); err != nil {
		if err == fmt.Errorf("no-ONU-data-found") {
			logger.Debugw("no persistent data found - abort reconciling", log.Fields{"device-id": dh.deviceID})
		} else {
			logger.Errorw("reconciling - restoring OnuTp-data failed - abort", log.Fields{"err": err, "device-id": dh.deviceID})
		}
		dh.reconciling = false
		return
	}
	var onuIndication oop.OnuIndication
	onuIndication.IntfId = pDevEntry.sOnuPersistentData.PersIntfID
	onuIndication.OnuId = pDevEntry.sOnuPersistentData.PersOnuID
	onuIndication.OperState = pDevEntry.sOnuPersistentData.PersOperState
	onuIndication.AdminState = pDevEntry.sOnuPersistentData.PersAdminState
	_ = dh.createInterface(&onuIndication)
}

func (dh *deviceHandler) reconcileDeviceTechProf() {
	logger.Debugw("reconciling - trigger tech profile config", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}

	dh.pOnuTP.lockTpProcMutex()
	defer dh.pOnuTP.unlockTpProcMutex()

	for _, uniData := range pDevEntry.sOnuPersistentData.PersUniConfig {
		// deadline context to ensure completion of background routines waited for
		//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
		deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
		dctx, cancel := context.WithDeadline(context.Background(), deadline)

		for tpID := range uniData.PersTpPathMap {
			dh.pOnuTP.resetTpProcessingErrorIndication(uniData.PersUniID, tpID)
			var wg sync.WaitGroup
			wg.Add(1) // for the 1 go routine to finish
			go dh.pOnuTP.configureUniTp(dctx, uniData.PersUniID, uniData.PersTpPathMap[tpID], &wg)
			dh.waitForCompletion(cancel, &wg, "TechProfReconcile") //wait for background process to finish
			if err := dh.pOnuTP.getTpProcessingErrorIndication(uniData.PersUniID, tpID); err != nil {
				logger.Errorw(err.Error(), log.Fields{"device-id": dh.deviceID})
			}
		}
	}
}

func (dh *deviceHandler) reconcileDeviceFlowConfig() {
	logger.Debugw("reconciling - trigger flow config", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	for _, uniData := range pDevEntry.sOnuPersistentData.PersUniConfig {
		var uniPort *onuUniPort
		var exist bool
		uniNo := mkUniPortNum(dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(), uint32(uniData.PersUniID))
		if uniPort, exist = dh.uniEntityMap[uniNo]; !exist {
			logger.Errorw("onuUniPort data not found!", log.Fields{"uniNo": uniNo, "device-id": dh.deviceID})
			return
		}
		for _, flowData := range uniData.PersFlowParams {
			logger.Debugw("add flow with cookie slice", log.Fields{"device-id": dh.deviceID, "cookies": flowData.CookieSlice})
			//the slice can be passed 'by value' here, - which internally passes its reference copy
			if _, exist = dh.UniVlanConfigFsmMap[uniData.PersUniID]; exist {
				if err := dh.UniVlanConfigFsmMap[uniData.PersUniID].SetUniFlowParams(flowData.VlanRuleParams.TpID,
					flowData.CookieSlice, uint16(flowData.VlanRuleParams.MatchVid), uint16(flowData.VlanRuleParams.SetVid),
					uint8(flowData.VlanRuleParams.SetPcp)); err != nil {
					logger.Errorw(err.Error(), log.Fields{"device-id": dh.deviceID})
				}
			} else {
				if err := dh.createVlanFilterFsm(uniPort, flowData.VlanRuleParams.TpID, flowData.CookieSlice,
					uint16(flowData.VlanRuleParams.MatchVid), uint16(flowData.VlanRuleParams.SetVid),
					uint8(flowData.VlanRuleParams.SetPcp), OmciVlanFilterAddDone); err != nil {
					logger.Errorw(err.Error(), log.Fields{"device-id": dh.deviceID})
				}
			}
		}
	}
}

func (dh *deviceHandler) reconcileMetrics() {
	logger.Debugw("reconciling - trigger metrics - to be implemented in scope of VOL-3324!", log.Fields{"device-id": dh.deviceID})

	//TODO: reset of reconciling-flag has always to be done in the last reconcile*() function
	dh.reconciling = false
}

func (dh *deviceHandler) deleteDevicePersistencyData() error {
	logger.Debugw("delete device persistency data", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(false)
	if pDevEntry == nil {
		//IfDevEntry does not exist here, no problem - no persistent data should have been stored
		logger.Debugw("OnuDevice does not exist - nothing to delete", log.Fields{"device-id": dh.deviceID})
		return nil
	}
	pDevEntry.lockOnuKVStoreMutex()
	defer pDevEntry.unlockOnuKVStoreMutex()

	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	pDevEntry.resetKvProcessingErrorIndication()

	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish
	go pDevEntry.deleteDataFromOnuKvStore(dctx, &wg)
	dh.waitForCompletion(cancel, &wg, "DeleteDevice") //wait for background process to finish

	// TODO: further actions - stop metrics and FSMs, remove device ...
	return pDevEntry.getKvProcessingErrorIndication()
}

func (dh *deviceHandler) rebootDevice(device *voltha.Device) error {
	logger.Debugw("reboot-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
	if device.ConnectStatus != voltha.ConnectStatus_REACHABLE {
		logger.Errorw("device-unreachable", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
		return fmt.Errorf("device-unreachable: %s, %s", dh.deviceID, device.SerialNumber)
	}
	if err := dh.pOnuOmciDevice.reboot(context.TODO()); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-rebooting-device", log.Fields{"device-id": dh.deviceID, "error": err})
		return err
	}

	//transfer the possibly modified logical uni port state
	dh.disableUniPortStateUpdate()

	logger.Debugw("call DeviceStateUpdate upon reboot", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE,
		voltha.OperStatus_DISCOVERED); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		return err
	}
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "rebooting"); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-reason-state", log.Fields{"device-id": dh.deviceID, "error": err})
		return err
	}
	dh.deviceReason = "rebooting"
	dh.ReadyForSpecificOmciConfig = false
	return nil
}

//  deviceHandler methods that implement the adapters interface requests## end #########
// #####################################################################################

// ################  to be updated acc. needs of ONU Device ########################
// deviceHandler StateMachine related state transition methods ##### begin #########

func (dh *deviceHandler) logStateChange(e *fsm.Event) {
	logger.Debugw("Device FSM: ", log.Fields{"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst), "device-id": dh.deviceID})
}

// doStateInit provides the device update to the core
func (dh *deviceHandler) doStateInit(e *fsm.Event) {

	logger.Debug("doStateInit-started")
	var err error

	// populate what we know.  rest comes later after mib sync
	dh.device.Root = false
	dh.device.Vendor = "OpenONU"
	dh.device.Model = "go"
	dh.device.Reason = "activating-onu"
	dh.deviceReason = "activating-onu"

	dh.logicalDeviceID = dh.deviceID // really needed - what for ??? //TODO!!!

	if !dh.reconciling {
		_ = dh.coreProxy.DeviceUpdate(context.TODO(), dh.device)
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceUpdate",
			log.Fields{"device-id": dh.deviceID})
	}

	dh.parentID = dh.device.ParentId
	dh.ponPortNumber = dh.device.ParentPortNo

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	logger.Debugw("device-updated", log.Fields{"device-id": dh.deviceID, "proxyAddressID": dh.ProxyAddressID,
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
	if !dh.reconciling {
		logger.Debugw("adding-pon-port", log.Fields{"device-id": dh.deviceID, "ponPortNo": dh.ponPortNumber})
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
func (dh *deviceHandler) postInit(e *fsm.Event) {

	logger.Debug("postInit-started")
	var err error
	/*
		dh.Client = oop.NewOpenoltClient(dh.clientCon)
		dh.pTransitionMap.Handle(ctx, GrpcConnected)
		return nil
	*/
	if err = dh.addOnuDeviceEntry(context.TODO()); err != nil {
		logger.Fatalf("Device FSM: addOnuDeviceEntry-failed-%s", err)
		e.Cancel(err)
		return
	}

	if dh.reconciling {
		go dh.reconcileDeviceOnuInd()
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
func (dh *deviceHandler) doStateConnected(e *fsm.Event) {

	logger.Debug("doStateConnected-started")
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debug("doStateConnected-done")
}

// doStateUp handle the onu up indication and update to voltha core
func (dh *deviceHandler) doStateUp(e *fsm.Event) {

	logger.Debug("doStateUp-started")
	err := errors.New("device FSM: function not implemented yet")
	e.Cancel(err)
	logger.Debug("doStateUp-done")

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
func (dh *deviceHandler) doStateDown(e *fsm.Event) {

	logger.Debug("doStateDown-started")
	var err error

	device := dh.device
	if device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Errorw("Failed to fetch handler device", log.Fields{"device-id": dh.deviceID})
		e.Cancel(err)
		return
	}

	cloned := proto.Clone(device).(*voltha.Device)
	logger.Debugw("do-state-down", log.Fields{"ClonedDeviceID": cloned.Id})
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
	logger.Debug("doStateDown-done")
}

// deviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// deviceHandler utility methods ##### begin #########

//getOnuDeviceEntry gets the ONU device entry and may wait until its value is defined
func (dh *deviceHandler) getOnuDeviceEntry(aWait bool) *OnuDeviceEntry {
	dh.lockDevice.RLock()
	pOnuDeviceEntry := dh.pOnuOmciDevice
	if aWait && pOnuDeviceEntry == nil {
		//keep the read sema short to allow for subsequent write
		dh.lockDevice.RUnlock()
		logger.Debugw("Waiting for DeviceEntry to be set ...", log.Fields{"device-id": dh.deviceID})
		// based on concurrent processing the deviceEntry setup may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(60 * time.Second): //timer may be discussed ...
			logger.Errorw("No valid DeviceEntry set after maxTime", log.Fields{"device-id": dh.deviceID})
			return nil
		case <-dh.deviceEntrySet:
			logger.Debugw("devicEntry ready now - continue", log.Fields{"device-id": dh.deviceID})
			// if written now, we can return the written value without sema
			return dh.pOnuOmciDevice
		}
	}
	dh.lockDevice.RUnlock()
	return pOnuDeviceEntry
}

//setOnuDeviceEntry sets the ONU device entry within the handler
func (dh *deviceHandler) setOnuDeviceEntry(
	apDeviceEntry *OnuDeviceEntry, apOnuTp *onuUniTechProf) {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	dh.pOnuOmciDevice = apDeviceEntry
	dh.pOnuTP = apOnuTp
}

//addOnuDeviceEntry creates a new ONU device or returns the existing
func (dh *deviceHandler) addOnuDeviceEntry(ctx context.Context) error {
	logger.Debugw("adding-deviceEntry", log.Fields{"device-id": dh.deviceID})

	deviceEntry := dh.getOnuDeviceEntry(false)
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = newOnuDeviceEntry(ctx, dh.deviceID, dh.pOpenOnuAc.KVStoreHost,
			dh.pOpenOnuAc.KVStorePort, dh.pOpenOnuAc.KVStoreType,
			dh, dh.coreProxy, dh.AdapterProxy,
			dh.pOpenOnuAc.pSupportedFsms) //nil as FSM pointer would yield deviceEntry internal defaults ...
		onuTechProfProc := newOnuUniTechProf(ctx, dh)
		//error treatment possible //TODO!!!
		dh.setOnuDeviceEntry(deviceEntry, onuTechProfProc)
		// fire deviceEntry ready event to spread to possibly waiting processing
		dh.deviceEntrySet <- true
		logger.Infow("onuDeviceEntry-added", log.Fields{"device-id": dh.deviceID})
	} else {
		logger.Infow("onuDeviceEntry-add: Device already exists", log.Fields{"device-id": dh.deviceID})
	}
	// might be updated with some error handling !!!
	return nil
}

// doStateInit provides the device update to the core
func (dh *deviceHandler) createInterface(onuind *oop.OnuIndication) error {
	logger.Debugw("create_interface-started", log.Fields{"OnuId": onuind.GetOnuId(),
		"OnuIntfId": onuind.GetIntfId(), "OnuSerialNumber": onuind.GetSerialNumber()})

	dh.pOnuIndication = onuind // let's revise if storing the pointer is sufficient...

	if !dh.reconciling {
		logger.Debugw("call DeviceStateUpdate upon create interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVATING, "device-id": dh.deviceID})
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		}
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceStateUpdate to ACTIVATING",
			log.Fields{"device-id": dh.deviceID})
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

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry != nil {
		if err := pDevEntry.start(context.TODO()); err != nil {
			return err
		}
	} else {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	if !dh.reconciling {
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "starting-openomci"); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-DeviceReasonUpdate to starting-openomci", log.Fields{"device-id": dh.deviceID, "error": err})
		}
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceReasonUpdate to starting-openomci",
			log.Fields{"device-id": dh.deviceID})
	}
	dh.deviceReason = "starting-openomci"

	/* this might be a good time for Omci Verify message?  */
	verifyExec := make(chan bool)
	omciVerify := newOmciTestRequest(context.TODO(),
		dh.device.Id, pDevEntry.PDevOmciCC,
		true, true) //exclusive and allowFailure (anyway not yet checked)
	omciVerify.performOmciTest(context.TODO(), verifyExec)

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
	 *   otherwise some processing synchronization would be required - cmp. e.g TechProfile processing
	 */
	//call MibUploadFSM - transition up to state ulStInSync
	pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		if pMibUlFsm.Is(ulStDisabled) {
			if err := pMibUlFsm.Event(ulEvStart); err != nil {
				logger.Errorw("MibSyncFsm: Can't go to state starting", log.Fields{"device-id": dh.deviceID, "err": err})
				return fmt.Errorf("can't go to state starting: %s", dh.deviceID)
			}
			logger.Debugw("MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
			//Determine ONU status and start/re-start MIB Synchronization tasks
			//Determine if this ONU has ever synchronized
			if true { //TODO: insert valid check
				if err := pMibUlFsm.Event(ulEvResetMib); err != nil {
					logger.Errorw("MibSyncFsm: Can't go to state resetting_mib", log.Fields{"device-id": dh.deviceID, "err": err})
					return fmt.Errorf("can't go to state resetting_mib: %s", dh.deviceID)
				}
			} else {
				if err := pMibUlFsm.Event(ulEvExamineMds); err != nil {
					logger.Errorw("MibSyncFsm: Can't go to state examine_mds", log.Fields{"device-id": dh.deviceID, "err": err})
					return fmt.Errorf("can't go to examine_mds: %s", dh.deviceID)
				}
				logger.Debugw("state of MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
				//Examine the MIB Data Sync
				// callbacks to be handled:
				// Event(ulEvSuccess)
				// Event(ulEvTimeout)
				// Event(ulEvMismatch)
			}
		} else {
			logger.Errorw("wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(pMibUlFsm.Current()),
				"device-id": dh.deviceID})
			return fmt.Errorf("wrong state of MibSyncFsm: %s", dh.deviceID)
		}
	} else {
		logger.Errorw("MibSyncFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("can't execute MibSync: %s", dh.deviceID)
	}
	return nil
}

func (dh *deviceHandler) updateInterface(onuind *oop.OnuIndication) error {
	//state checking to prevent unneeded processing (eg. on ONU 'unreachable' and 'down')
	// (but note that the deviceReason may also have changed to e.g. TechProf*Delete_Success in between)
	if dh.deviceReason != "stopping-openomci" {
		logger.Debugw("updateInterface-started - stopping-device", log.Fields{"device-id": dh.deviceID})

		//stop all running FSM processing - make use of the DH-state as mirrored in the deviceReason
		//here no conflict with aborted FSM's should arise as a complete OMCI initialization is assumed on ONU-Up
		//but that might change with some simple MDS check on ONU-Up treatment -> attention!!!
		if err := dh.resetFsms(); err != nil {
			logger.Errorw("error-updateInterface at FSM stop",
				log.Fields{"device-id": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
		//all stored persitent data are not valid anymore (loosing knowledge abouit the connected ONU)
		_ = dh.deleteDevicePersistencyData() //ignore possible errors here and continue, hope is that data is synchronized with new ONU-Up

		//deviceEntry stop without omciCC reset here, regarding the OMCI_CC still valid for this ONU
		// - in contrary to disableDevice - compare with processUniDisableStateDoneEvent
		//stop the device entry which resets the attached omciCC
		pDevEntry := dh.getOnuDeviceEntry(false)
		if pDevEntry == nil {
			logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
			return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
		}
		_ = pDevEntry.stop(context.TODO(), false)

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

		dh.deviceReason = "stopping-openomci"
		dh.ReadyForSpecificOmciConfig = false

		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "stopping-openomci"); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-DeviceReasonUpdate to stopping-openomci",
				log.Fields{"device-id": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}

		logger.Debugw("call DeviceStateUpdate upon update interface", log.Fields{"ConnectStatus": voltha.ConnectStatus_UNREACHABLE,
			"OperStatus": voltha.OperStatus_DISCOVERED, "device-id": dh.deviceID})
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_UNREACHABLE, voltha.OperStatus_DISCOVERED); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-updating-device-state unreachable-discovered",
				log.Fields{"device-id": dh.deviceID, "error": err})
			// abort: system behavior is just unstable ...
			return err
		}
	} else {
		logger.Debugw("updateInterface - device already stopped", log.Fields{"device-id": dh.deviceID})
	}
	return nil
}

func (dh *deviceHandler) resetFsms() error {
	//all possible FSM's are stopped or reset here to ensure their transition to 'disabled'
	//it is not sufficient to stop/reset the latest running FSM as done in previous versions
	//  as after down/up procedures all FSM's might be active/ongoing (in theory)
	//  and using the stop/reset event should never harm

	pDevEntry := dh.getOnuDeviceEntry(false)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}

	//the MibSync FSM might be active all the ONU-active time,
	// hence it must be stopped unconditionally
	pMibUlFsm := pDevEntry.pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		_ = pMibUlFsm.Event(ulEvStop) //TODO!! verify if MibSyncFsm stop-processing is sufficient (to allow it again afterwards)
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
			if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[uniPort.uniID]; exist {
				//VlanFilterFsm exists and was already started
				pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
				if pVlanFilterStatemachine != nil {
					//reset of all Fsm is always accompagnied by global persistency data removal
					//  no need to remove specific data
					pVlanFilterFsm.RequestClearPersistency(false)
					//and reset the UniVlanConfig FSM
					_ = pVlanFilterStatemachine.Event(vlanEvReset)
				}
			}
		}
	}
	//TODO!!! care about PM/Alarm processing once started
	return nil
}

func (dh *deviceHandler) processMibDatabaseSyncEvent(devEvent OnuDeviceEvent) {
	logger.Debugw("MibInSync event received", log.Fields{"device-id": dh.deviceID})
	if !dh.reconciling {
		//initiate DevStateUpdate
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "discovery-mibsync-complete"); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-DeviceReasonUpdate to mibsync-complete", log.Fields{
				"device-id": dh.deviceID, "error": err})
		} else {
			logger.Infow("dev reason updated to MibSync complete", log.Fields{"device-id": dh.deviceID})
		}
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceReasonUpdate to mibsync-complete",
			log.Fields{"device-id": dh.deviceID})
	}
	//set internal state anyway - as it was done
	dh.deviceReason = "discovery-mibsync-complete"

	i := uint8(0) //UNI Port limit: see MaxUnisPerOnu (by now 16) (OMCI supports max 255 p.b.)
	pDevEntry := dh.getOnuDeviceEntry(false)
	if unigInstKeys := pDevEntry.pOnuDB.getSortedInstKeys(me.UniGClassID); len(unigInstKeys) > 0 {
		for _, mgmtEntityID := range unigInstKeys {
			logger.Debugw("Add UNI port for stored UniG instance:", log.Fields{
				"device-id": dh.deviceID, "UnigMe EntityID": mgmtEntityID})
			dh.addUniPort(mgmtEntityID, i, uniPPTP)
			i++
		}
	} else {
		logger.Debugw("No UniG instances found", log.Fields{"device-id": dh.deviceID})
	}
	if veipInstKeys := pDevEntry.pOnuDB.getSortedInstKeys(me.VirtualEthernetInterfacePointClassID); len(veipInstKeys) > 0 {
		for _, mgmtEntityID := range veipInstKeys {
			logger.Debugw("Add VEIP acc. to stored VEIP instance:", log.Fields{
				"device-id": dh.deviceID, "VEIP EntityID": mgmtEntityID})
			dh.addUniPort(mgmtEntityID, i, uniVEIP)
			i++
		}
	} else {
		logger.Debugw("No VEIP instances found", log.Fields{"device-id": dh.deviceID})
	}
	if i == 0 {
		logger.Warnw("No PPTP instances found", log.Fields{"device-id": dh.deviceID})
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
			logger.Infow("UniLockStateDone event: Starting MIB download", log.Fields{"device-id": dh.deviceID})
	* lockState processing commented out
	*/
	/*  Mib download procedure -
	***** should run over 'downloaded' state and generate MibDownloadDone event *****
	 */
	pMibDlFsm := pDevEntry.pMibDownloadFsm.pFsm
	if pMibDlFsm != nil {
		if pMibDlFsm.Is(dlStDisabled) {
			if err := pMibDlFsm.Event(dlEvStart); err != nil {
				logger.Errorw("MibDownloadFsm: Can't go to state starting", log.Fields{"device-id": dh.deviceID, "err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				logger.Debugw("MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
				// maybe use more specific states here for the specific download steps ...
				if err := pMibDlFsm.Event(dlEvCreateGal); err != nil {
					logger.Errorw("MibDownloadFsm: Can't start CreateGal", log.Fields{"device-id": dh.deviceID, "err": err})
				} else {
					logger.Debugw("state of MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
					//Begin MIB data download (running autonomously)
				}
			}
		} else {
			logger.Errorw("wrong state of MibDownloadFsm - want: disabled", log.Fields{"have": string(pMibDlFsm.Current()),
				"device-id": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
		/***** Mib download started */
	} else {
		logger.Errorw("MibDownloadFsm invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
	}
}

func (dh *deviceHandler) processMibDownloadDoneEvent(devEvent OnuDeviceEvent) {
	logger.Debugw("MibDownloadDone event received", log.Fields{"device-id": dh.deviceID})
	//initiate DevStateUpdate
	if !dh.reconciling {
		logger.Debugw("call DeviceStateUpdate upon mib-download done", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
			"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.deviceID})
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVE); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
		} else {
			logger.Debugw("dev state updated to 'Oper.Active'", log.Fields{"device-id": dh.deviceID})
		}
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceStateUpdate to ACTIVE",
			log.Fields{"device-id": dh.deviceID})
	}
	if !dh.reconciling {
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "initial-mib-downloaded"); err != nil {
			//TODO with VOL-3045/VOL-3046: return the error and stop further processing
			logger.Errorw("error-DeviceReasonUpdate to initial-mib-downloaded",
				log.Fields{"device-id": dh.deviceID, "error": err})
		} else {
			logger.Infow("dev reason updated to initial-mib-downloaded", log.Fields{"device-id": dh.deviceID})
		}
	} else {
		logger.Debugw("reconciling - don't notify core about DeviceReasonUpdate to initial-mib-downloaded",
			log.Fields{"device-id": dh.deviceID})
	}
	//set internal state anyway - as it was done
	dh.deviceReason = "initial-mib-downloaded"
	dh.ReadyForSpecificOmciConfig = true
	// *** should generate UniUnlockStateDone event *****
	if dh.pUnlockStateFsm == nil {
		dh.createUniLockFsm(false, UniUnlockStateDone)
	} else { //UnlockStateFSM already init
		dh.pUnlockStateFsm.setSuccessEvent(UniUnlockStateDone)
		dh.runUniLockFsm(false)
	}
}

func (dh *deviceHandler) processUniUnlockStateDoneEvent(devEvent OnuDeviceEvent) {
	dh.enableUniPortStateUpdate() //cmp python yield self.enable_ports()

	if !dh.reconciling {
		logger.Infow("UniUnlockStateDone event: Sending OnuUp event", log.Fields{"device-id": dh.deviceID})
		raisedTs := time.Now().UnixNano()
		go dh.sendOnuOperStateEvent(voltha.OperStatus_ACTIVE, dh.deviceID, raisedTs) //cmp python onu_active_event
	} else {
		logger.Debugw("reconciling - don't notify core that onu went to active but trigger tech profile config",
			log.Fields{"device-id": dh.deviceID})
		go dh.reconcileDeviceTechProf()
		// reconcilement will be continued after ani config is done
	}
}

func (dh *deviceHandler) processUniDisableStateDoneEvent(devEvent OnuDeviceEvent) {
	logger.Debugw("DeviceStateUpdate upon disable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_UNKNOWN, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(),
		dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_UNKNOWN); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}

	logger.Debugw("DeviceReasonUpdate upon disable", log.Fields{
		"reason": "omci-admin-lock", "device-id": dh.deviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "omci-admin-lock"); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-reason-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}
	dh.deviceReason = "omci-admin-lock"

	//transfer the modified logical uni port state
	dh.disableUniPortStateUpdate()
}

func (dh *deviceHandler) processUniEnableStateDoneEvent(devEvent OnuDeviceEvent) {
	logger.Debugw("DeviceStateUpdate upon re-enable", log.Fields{"ConnectStatus": voltha.ConnectStatus_REACHABLE,
		"OperStatus": voltha.OperStatus_ACTIVE, "device-id": dh.deviceID})
	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE,
		voltha.OperStatus_ACTIVE); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-device-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}

	logger.Debugw("DeviceReasonUpdate upon re-enable", log.Fields{
		"reason": "onu-reenabled", "device-id": dh.deviceID})
	// DeviceReason to update acc.to modified py code as per beginning of Sept 2020
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "onu-reenabled"); err != nil {
		//TODO with VOL-3045/VOL-3046: return the error and stop further processing
		logger.Errorw("error-updating-reason-state", log.Fields{"device-id": dh.deviceID, "error": err})
	}
	dh.deviceReason = "onu-reenabled"

	//transfer the modified logical uni port state
	dh.enableUniPortStateUpdate()
}

func (dh *deviceHandler) processOmciAniConfigDoneEvent(devEvent OnuDeviceEvent) {
	if devEvent == OmciAniConfigDone {
		logger.Debugw("OmciAniConfigDone event received", log.Fields{"device-id": dh.deviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.deviceReason != "tech-profile-config-download-success" {
			// which may be the case from some previous actvity even on this UNI Port (but also other UNI ports)
			if !dh.reconciling {
				if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "tech-profile-config-download-success"); err != nil {
					//TODO with VOL-3045/VOL-3046: return the error and stop further processing
					logger.Errorw("error-DeviceReasonUpdate to tech-profile-config-download-success",
						log.Fields{"device-id": dh.deviceID, "error": err})
				} else {
					logger.Infow("update dev reason to tech-profile-config-download-success",
						log.Fields{"device-id": dh.deviceID})
				}
			} else {
				logger.Debugw("reconciling - don't notify core about DeviceReasonUpdate to tech-profile-config-download-success",
					log.Fields{"device-id": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "tech-profile-config-download-success"
		}
		if dh.reconciling {
			go dh.reconcileDeviceFlowConfig()
		}
	} else { // should be the OmciAniResourceRemoved block
		logger.Debugw("OmciAniResourceRemoved event received", log.Fields{"device-id": dh.deviceID})
		// attention: the device reason update is done based on ONU-UNI-Port related activity
		//  - which may cause some inconsistency
		if dh.deviceReason != "tech-profile-config-delete-success" {
			// which may be the case from some previous actvity even on this ONU port (but also other UNI ports)
			if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "tech-profile-config-delete-success"); err != nil {
				//TODO with VOL-3045/VOL-3046: return the error and stop further processing
				logger.Errorw("error-DeviceReasonUpdate to tech-profile-config-delete-success",
					log.Fields{"device-id": dh.deviceID, "error": err})
			} else {
				logger.Infow("update dev reason to tech-profile-config-delete-success",
					log.Fields{"device-id": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "tech-profile-config-delete-success"
		}
	}
}

func (dh *deviceHandler) processOmciVlanFilterDoneEvent(aDevEvent OnuDeviceEvent) {
	logger.Debugw("OmciVlanFilterDone event received",
		log.Fields{"device-id": dh.deviceID, "event": aDevEvent})
	// attention: the device reason update is done based on ONU-UNI-Port related activity
	//  - which may cause some inconsistency

	if aDevEvent == OmciVlanFilterAddDone {
		if dh.deviceReason != "omci-flows-pushed" {
			// which may be the case from some previous actvity on another UNI Port of the ONU
			// or even some previous flow add activity on the same port
			if !dh.reconciling {
				if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "omci-flows-pushed"); err != nil {
					logger.Errorw("error-DeviceReasonUpdate to omci-flows-pushed",
						log.Fields{"device-id": dh.deviceID, "error": err})
				} else {
					logger.Infow("updated dev reason to omci-flows-pushed",
						log.Fields{"device-id": dh.deviceID})
				}
			} else {
				logger.Debugw("reconciling - don't notify core about DeviceReasonUpdate to omci-flows-pushed",
					log.Fields{"device-id": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "omci-flows-pushed"

			if dh.reconciling {
				go dh.reconcileMetrics()
			}
		}
	} else {
		if dh.deviceReason != "omci-flows-deleted" {
			//not relevant for reconcile
			if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "omci-flows-deleted"); err != nil {
				logger.Errorw("error-DeviceReasonUpdate to omci-flows-deleted",
					log.Fields{"device-id": dh.deviceID, "error": err})
			} else {
				logger.Infow("updated dev reason to omci-flows-deleted",
					log.Fields{"device-id": dh.deviceID})
			}
			//set internal state anyway - as it was done
			dh.deviceReason = "omci-flows-deleted"
		}
	}
}

//deviceProcStatusUpdate evaluates possible processing events and initiates according next activities
func (dh *deviceHandler) deviceProcStatusUpdate(devEvent OnuDeviceEvent) {
	switch devEvent {
	case MibDatabaseSync:
		{
			dh.processMibDatabaseSyncEvent(devEvent)
		}
	case MibDownloadDone:
		{
			dh.processMibDownloadDoneEvent(devEvent)
		}
	case UniUnlockStateDone:
		{
			dh.processUniUnlockStateDoneEvent(devEvent)
		}
	case UniEnableStateDone:
		{
			dh.processUniEnableStateDoneEvent(devEvent)
		}
	case UniDisableStateDone:
		{
			dh.processUniDisableStateDoneEvent(devEvent)
		}
	case OmciAniConfigDone, OmciAniResourceRemoved:
		{
			dh.processOmciAniConfigDoneEvent(devEvent)
		}
	case OmciVlanFilterAddDone, OmciVlanFilterRemDone:
		{
			dh.processOmciVlanFilterDoneEvent(devEvent)
		}
	default:
		{
			logger.Debugw("unhandled-device-event", log.Fields{"device-id": dh.deviceID, "event": devEvent})
		}
	} //switch
}

func (dh *deviceHandler) addUniPort(aUniInstNo uint16, aUniID uint8, aPortType uniPortType) {
	// parameters are IntfId, OnuId, uniId
	uniNo := mkUniPortNum(dh.pOnuIndication.GetIntfId(), dh.pOnuIndication.GetOnuId(),
		uint32(aUniID))
	if _, present := dh.uniEntityMap[uniNo]; present {
		logger.Warnw("onuUniPort-add: Port already exists", log.Fields{"for InstanceId": aUniInstNo})
	} else {
		//with arguments aUniID, a_portNo, aPortType
		pUniPort := newOnuUniPort(aUniID, uniNo, aUniInstNo, aPortType)
		if pUniPort == nil {
			logger.Warnw("onuUniPort-add: Could not create Port", log.Fields{"for InstanceId": aUniInstNo})
		} else {
			//store UniPort with the System-PortNumber key
			dh.uniEntityMap[uniNo] = pUniPort
			if !dh.reconciling {
				// create announce the UniPort to the core as VOLTHA Port object
				if err := pUniPort.createVolthaPort(dh); err == nil {
					logger.Infow("onuUniPort-added", log.Fields{"for PortNo": uniNo})
				} //error logging already within UniPort method
			} else {
				logger.Debugw("reconciling - onuUniPort already added", log.Fields{"for PortNo": uniNo, "device-id": dh.deviceID})
			}
		}
	}
}

// enableUniPortStateUpdate enables UniPortState and update core port state accordingly
func (dh *deviceHandler) enableUniPortStateUpdate() {
	//  py code was updated 2003xx to activate the real ONU UNI ports per OMCI (VEIP or PPTP)
	//    but towards core only the first port active state is signaled
	//    with following remark:
	//       # TODO: for now only support the first UNI given no requirement for multiple uni yet. Also needed to reduce flow
	//       #  load on the core

	// lock_ports(false) as done in py code here is shifted to separate call from devicevent processing

	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniID)&activeUniPortStateUpdateMask == (1 << uniPort.uniID) {
			logger.Infow("onuUniPort-forced-OperState-ACTIVE", log.Fields{"for PortNo": uniNo})
			uniPort.setOperState(vc.OperStatus_ACTIVE)
			if !dh.reconciling {
				//maybe also use getter functions on uniPort - perhaps later ...
				go dh.coreProxy.PortStateUpdate(context.TODO(), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
			} else {
				//TODO there is no retry mechanism, return error
				logger.Debugw("reconciling - don't notify core about PortStateUpdate", log.Fields{"device-id": dh.deviceID})
			}
		}
	}
}

// Disable UniPortState and update core port state accordingly
func (dh *deviceHandler) disableUniPortStateUpdate() {
	// compare enableUniPortStateUpdate() above
	//   -> use current restriction to operate only on first UNI port as inherited from actual Py code
	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer
		if (1<<uniPort.uniID)&activeUniPortStateUpdateMask == (1 << uniPort.uniID) {
			logger.Infow("onuUniPort-forced-OperState-UNKNOWN", log.Fields{"for PortNo": uniNo})
			uniPort.setOperState(vc.OperStatus_UNKNOWN)
			//maybe also use getter functions on uniPort - perhaps later ...
			go dh.coreProxy.PortStateUpdate(context.TODO(), dh.deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
		}
	}
}

// ONU_Active/Inactive announcement on system KAFKA bus
// tried to re-use procedure of oltUpDownIndication from openolt_eventmgr.go with used values from Py code
func (dh *deviceHandler) sendOnuOperStateEvent(aOperState vc.OperStatus_Types, aDeviceID string, raisedTs int64) {
	var de voltha.DeviceEvent
	eventContext := make(map[string]string)
	//Populating event context
	//  assume giving ParentId in GetDevice twice really gives the ParentDevice (there is no GetParentDevice()...)
	parentDevice, err := dh.coreProxy.GetDevice(context.TODO(), dh.parentID, dh.parentID)
	if err != nil || parentDevice == nil {
		logger.Errorw("Failed to fetch parent device for OnuEvent",
			log.Fields{"parentID": dh.parentID, "err": err})
	}
	oltSerialNumber := parentDevice.SerialNumber

	eventContext["pon-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.IntfId), 10)
	eventContext["onu-id"] = strconv.FormatUint(uint64(dh.pOnuIndication.OnuId), 10)
	eventContext["serial-number"] = dh.device.SerialNumber
	eventContext["olt_serial_number"] = oltSerialNumber
	eventContext["device_id"] = aDeviceID
	eventContext["registration_id"] = aDeviceID //py: string(device_id)??
	logger.Debugw("prepare ONU_ACTIVATED event",
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
	if err := dh.EventProxy.SendDeviceEvent(&de, equipment, pon, raisedTs); err != nil {
		logger.Warnw("could not send ONU_ACTIVATED event",
			log.Fields{"device-id": aDeviceID, "error": err})
	}
	logger.Debugw("ONU_ACTIVATED event sent to KAFKA",
		log.Fields{"device-id": aDeviceID, "with-EventName": de.DeviceEventName})
}

// createUniLockFsm initializes and runs the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) createUniLockFsm(aAdminState bool, devEvent OnuDeviceEvent) {
	chLSFsm := make(chan Message, 2048)
	var sFsmName string
	if aAdminState {
		logger.Infow("createLockStateFSM", log.Fields{"device-id": dh.deviceID})
		sFsmName = "LockStateFSM"
	} else {
		logger.Infow("createUnlockStateFSM", log.Fields{"device-id": dh.deviceID})
		sFsmName = "UnLockStateFSM"
	}

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return
	}
	pLSFsm := newLockStateFsm(pDevEntry.PDevOmciCC, aAdminState, devEvent,
		sFsmName, dh, chLSFsm)
	if pLSFsm != nil {
		if aAdminState {
			dh.pLockStateFsm = pLSFsm
		} else {
			dh.pUnlockStateFsm = pLSFsm
		}
		dh.runUniLockFsm(aAdminState)
	} else {
		logger.Errorw("LockStateFSM could not be created - abort!!", log.Fields{"device-id": dh.deviceID})
	}
}

// runUniLockFsm starts the UniLock FSM to transfer the OMCI related commands for port lock/unlock
func (dh *deviceHandler) runUniLockFsm(aAdminState bool) {
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
				logger.Warnw("LockStateFSM: can't start", log.Fields{"err": err})
				// maybe try a FSM reset and then again ... - TODO!!!
			} else {
				/***** LockStateFSM started */
				logger.Debugw("LockStateFSM started", log.Fields{
					"state": pLSStatemachine.Current(), "device-id": dh.deviceID})
			}
		} else {
			logger.Warnw("wrong state of LockStateFSM - want: disabled", log.Fields{
				"have": pLSStatemachine.Current(), "device-id": dh.deviceID})
			// maybe try a FSM reset and then again ... - TODO!!!
		}
	} else {
		logger.Errorw("LockStateFSM StateMachine invalid - cannot be executed!!", log.Fields{"device-id": dh.deviceID})
		// maybe try a FSM reset and then again ... - TODO!!!
	}
}

//setBackend provides a DB backend for the specified path on the existing KV client
func (dh *deviceHandler) setBackend(aBasePathKvStore string) *db.Backend {
	addr := dh.pOpenOnuAc.KVStoreHost + ":" + strconv.Itoa(dh.pOpenOnuAc.KVStorePort)
	logger.Debugw("SetKVStoreBackend", log.Fields{"IpTarget": addr,
		"BasePathKvStore": aBasePathKvStore, "device-id": dh.deviceID})
	kvbackend := &db.Backend{
		Client:    dh.pOpenOnuAc.kvClient,
		StoreType: dh.pOpenOnuAc.KVStoreType,
		/* address config update acc. to [VOL-2736] */
		Address:    addr,
		Timeout:    dh.pOpenOnuAc.KVStoreTimeout,
		PathPrefix: aBasePathKvStore}

	return kvbackend
}
func (dh *deviceHandler) getFlowOfbFields(apFlowItem *ofp.OfpFlowStats, loMatchVlan *uint16,
	loAddPcp *uint8, loIPProto *uint32) {

	for _, field := range flow.GetOfbFields(apFlowItem) {
		switch field.Type {
		case of.OxmOfbFieldTypes_OFPXMT_OFB_ETH_TYPE:
			{
				logger.Debugw("flow type EthType", log.Fields{"device-id": dh.deviceID,
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
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"VID": strconv.FormatInt(int64(*loMatchVlan), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP:
			{
				*loAddPcp = uint8(field.GetVlanPcp())
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"PCP": loAddPcp})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_DST:
			{
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"UDP-DST": strconv.FormatInt(int64(field.GetUdpDst()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_UDP_SRC:
			{
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"UDP-SRC": strconv.FormatInt(int64(field.GetUdpSrc()), 16)})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_DST:
			{
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"IPv4-DST": field.GetIpv4Dst()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_IPV4_SRC:
			{
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
					"IPv4-SRC": field.GetIpv4Src()})
			}
		case of.OxmOfbFieldTypes_OFPXMT_OFB_METADATA:
			{
				logger.Debugw("flow field type", log.Fields{"device-id": dh.deviceID,
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

func (dh *deviceHandler) getFlowActions(apFlowItem *ofp.OfpFlowStats, loSetPcp *uint8, loSetVlan *uint16) {
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
				logger.Debugw("flow action type", log.Fields{"device-id": dh.deviceID,
					"PushEthType": strconv.FormatInt(int64(action.GetPush().Ethertype), 16)})
			}
		case of.OfpActionType_OFPAT_SET_FIELD:
			{
				pActionSetField := action.GetSetField()
				if pActionSetField.Field.OxmClass != of.OfpOxmClass_OFPXMC_OPENFLOW_BASIC {
					logger.Warnw("flow action SetField invalid OxmClass (ignored)", log.Fields{"device-id": dh.deviceID,
						"OxcmClass": pActionSetField.Field.OxmClass})
				}
				if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_VID {
					*loSetVlan = uint16(pActionSetField.Field.GetOfbField().GetVlanVid())
					logger.Debugw("flow Set VLAN from SetField action", log.Fields{"device-id": dh.deviceID,
						"SetVlan": strconv.FormatInt(int64(*loSetVlan), 16)})
				} else if pActionSetField.Field.GetOfbField().Type == of.OxmOfbFieldTypes_OFPXMT_OFB_VLAN_PCP {
					*loSetPcp = uint8(pActionSetField.Field.GetOfbField().GetVlanPcp())
					logger.Debugw("flow Set PCP from SetField action", log.Fields{"device-id": dh.deviceID,
						"SetPcp": *loSetPcp})
				} else {
					logger.Warnw("flow action SetField invalid FieldType", log.Fields{"device-id": dh.deviceID,
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
func (dh *deviceHandler) addFlowItemToUniPort(apFlowItem *ofp.OfpFlowStats, apUniPort *onuUniPort) error {
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

	metadata := flow.GetMetadataFromWriteMetadataAction(apFlowItem)
	if metadata == 0 {
		logger.Debugw("flow-add invalid metadata - abort",
			log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("flow-add invalid metadata: %s", dh.deviceID)
	}
	loTpID := flow.GetTechProfileIDFromWriteMetaData(metadata)
	loCookie := apFlowItem.GetCookie()
	loCookieSlice := []uint64{loCookie}
	logger.Debugw("flow-add base indications", log.Fields{"device-id": dh.deviceID,
		"TechProf-Id": loTpID, "cookie": loCookie})

	dh.getFlowOfbFields(apFlowItem, &loMatchVlan, &loAddPcp, &loIPProto)
	/* TT related temporary workaround - should not be needed anymore
	if loIPProto == 2 {
		// some workaround for TT workflow at proto == 2 (IGMP trap) -> ignore the flow
		// avoids installing invalid EVTOCD rule
		logger.Debugw("flow-add type IpProto 2: TT workaround: ignore flow",
			log.Fields{"device-id": dh.deviceID})
		return nil
	}
	*/
	dh.getFlowActions(apFlowItem, &loSetPcp, &loSetVlan)

	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Errorw("flow-add aborted - SetVlanId undefined, but MatchVid set", log.Fields{
			"device-id": dh.deviceID, "UniPort": apUniPort.portNo,
			"set_vid":   strconv.FormatInt(int64(loSetVlan), 16),
			"match_vid": strconv.FormatInt(int64(loMatchVlan), 16)})
		//TODO!!: Use DeviceId within the error response to rwCore
		//  likewise also in other error response cases to calling components as requested in [VOL-3458]
		return fmt.Errorf("flow-add Set/Match VlanId inconsistent: %s", dh.deviceID)
	}
	if loSetVlan == uint16(of.OfpVlanId_OFPVID_NONE) && loMatchVlan == uint16(of.OfpVlanId_OFPVID_PRESENT) {
		logger.Debugw("flow-add vlan-any/copy", log.Fields{"device-id": dh.deviceID})
		loSetVlan = loMatchVlan //both 'transparent' (copy any)
	} else {
		//looks like OMCI value 4097 (copyFromOuter - for Uni double tagged) is not supported here
		if loSetVlan != uint16(of.OfpVlanId_OFPVID_PRESENT) {
			// not set to transparent
			loSetVlan &= 0x0FFF //mask VID bits as prerequisite for vlanConfigFsm
		}
		logger.Debugw("flow-add vlan-set", log.Fields{"device-id": dh.deviceID})
	}
	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		return dh.UniVlanConfigFsmMap[apUniPort.uniID].SetUniFlowParams(loTpID, loCookieSlice,
			loMatchVlan, loSetVlan, loSetPcp)
	}
	return dh.createVlanFilterFsm(apUniPort, loTpID, loCookieSlice,
		loMatchVlan, loSetVlan, loSetPcp, OmciVlanFilterAddDone)
}

//removeFlowItemFromUniPort parses the actual flow item to remove it from the UniPort
func (dh *deviceHandler) removeFlowItemFromUniPort(apFlowItem *ofp.OfpFlowStats, apUniPort *onuUniPort) error {
	//optimization and assumption: the flow cookie uniquely identifies the flow and with that the internal rule
	//hence only the cookie is used here to find the relevant flow and possibly remove the rule
	//no extra check is done on the rule parameters
	//accordingly the removal is done only once - for the first found flow with that cookie, even though
	// at flow creation is not assured, that the same cookie is not configured for different flows - just assumed
	//additionally it is assumed here, that removal can only be done for one cookie per flow in a sequence (different
	// from addFlow - where at reconcilement multiple cookies per flow ) can be configured in one sequence)
	// - some possible 'delete-all' sequence would have to be implemented separately (where the cookies are don't care anyway)
	loCookie := apFlowItem.GetCookie()
	logger.Debugw("flow-remove base indications", log.Fields{"device-id": dh.deviceID, "cookie": loCookie})

	/* TT related temporary workaround - should not be needed anymore
	for _, field := range flow.GetOfbFields(apFlowItem) {
		if field.Type == of.OxmOfbFieldTypes_OFPXMT_OFB_IP_PROTO {
			loIPProto := field.GetIpProto()
			logger.Debugw("flow type IpProto", log.Fields{"device-id": dh.deviceID,
				"IpProto": strconv.FormatInt(int64(loIPProto), 16)})
			if loIPProto == 2 {
				// some workaround for TT workflow on proto == 2 (IGMP trap) -> the flow was not added, no need to remove
				logger.Debugw("flow-remove type IpProto 2: TT workaround: ignore flow",
					log.Fields{"device-id": dh.deviceID})
				return nil
			}
		}
	} //for all OfbFields
	*/

	if _, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		return dh.UniVlanConfigFsmMap[apUniPort.uniID].RemoveUniFlowParams(loCookie)
	}
	logger.Debugw("flow-remove called, but no flow is configured (no VlanConfigFsm, flow already removed) ",
		log.Fields{"device-id": dh.deviceID})
	//but as we regard the flow as not existing = removed we respond just ok
	// and treat the reason accordingly (which in the normal removal procedure is initiated by the FSM)
	go dh.deviceProcStatusUpdate(OmciVlanFilterRemDone)

	return nil
}

// createVlanFilterFsm initializes and runs the VlanFilter FSM to transfer OMCI related VLAN config
func (dh *deviceHandler) createVlanFilterFsm(apUniPort *onuUniPort, aTpID uint16, aCookieSlice []uint64,
	aMatchVlan uint16, aSetVlan uint16, aSetPcp uint8, aDevEvent OnuDeviceEvent) error {
	chVlanFilterFsm := make(chan Message, 2048)

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice -aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice for device-id %x - aborting", dh.deviceID)
	}

	pVlanFilterFsm := NewUniVlanConfigFsm(dh, pDevEntry.PDevOmciCC, apUniPort, dh.pOnuTP,
		pDevEntry.pOnuDB, aTpID, aDevEvent, "UniVlanConfigFsm", chVlanFilterFsm,
		dh.pOpenOnuAc.AcceptIncrementalEvto, aCookieSlice, aMatchVlan, aSetVlan, aSetPcp)
	if pVlanFilterFsm != nil {
		dh.UniVlanConfigFsmMap[apUniPort.uniID] = pVlanFilterFsm
		pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
		if pVlanFilterStatemachine != nil {
			if pVlanFilterStatemachine.Is(vlanStDisabled) {
				if err := pVlanFilterStatemachine.Event(vlanEvStart); err != nil {
					logger.Warnw("UniVlanConfigFsm: can't start", log.Fields{"err": err})
					return fmt.Errorf("can't start UniVlanConfigFsm for device-id %x", dh.deviceID)
				}
				/***** UniVlanConfigFsm started */
				logger.Debugw("UniVlanConfigFsm started", log.Fields{
					"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
					"UniPort": apUniPort.portNo})
			} else {
				logger.Warnw("wrong state of UniVlanConfigFsm - want: disabled", log.Fields{
					"have": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID})
				return fmt.Errorf("uniVlanConfigFsm not in expected disabled state for device-id %x", dh.deviceID)
			}
		} else {
			logger.Errorw("UniVlanConfigFsm StateMachine invalid - cannot be executed!!", log.Fields{
				"device-id": dh.deviceID})
			return fmt.Errorf("uniVlanConfigFsm invalid for device-id %x", dh.deviceID)
		}
	} else {
		logger.Errorw("UniVlanConfigFsm could not be created - abort!!", log.Fields{
			"device-id": dh.deviceID, "UniPort": apUniPort.portNo})
		return fmt.Errorf("uniVlanConfigFsm could not be created for device-id %x", dh.deviceID)
	}
	return nil
}

//VerifyVlanConfigRequest checks on existence of a given uniPort
// and starts verification of flow config based on that
func (dh *deviceHandler) VerifyVlanConfigRequest(aUniID uint8) {
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
		logger.Debugw("VerifyVlanConfig aborted: requested uniID not found in PortDB",
			log.Fields{"device-id": dh.deviceID, "uni-id": aUniID})
		return
	}
	dh.verifyUniVlanConfigRequest(pCurrentUniPort)
}

//verifyUniVlanConfigRequest checks on existence of flow configuration and starts it accordingly
func (dh *deviceHandler) verifyUniVlanConfigRequest(apUniPort *onuUniPort) {
	//TODO!! verify and start pending flow configuration
	//some pending config request my exist in case the UniVlanConfig FSM was already started - with internal data -
	//but execution was set to 'on hold' as first the TechProfile config had to be applied
	if pVlanFilterFsm, exist := dh.UniVlanConfigFsmMap[apUniPort.uniID]; exist {
		//VlanFilterFsm exists and was already started (assumed to wait for TechProfile execution here)
		pVlanFilterStatemachine := pVlanFilterFsm.pAdaptFsm.pFsm
		if pVlanFilterStatemachine != nil {
			if pVlanFilterStatemachine.Is(vlanStWaitingTechProf) {
				if err := pVlanFilterStatemachine.Event(vlanEvContinueConfig); err != nil {
					logger.Warnw("UniVlanConfigFsm: can't continue processing", log.Fields{"err": err})
				} else {
					/***** UniVlanConfigFsm continued */
					logger.Debugw("UniVlanConfigFsm continued", log.Fields{
						"state": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID,
						"UniPort": apUniPort.portNo})
				}
			} else {
				logger.Debugw("no state of UniVlanConfigFsm to be continued", log.Fields{
					"have": pVlanFilterStatemachine.Current(), "device-id": dh.deviceID})
			}
		} else {
			logger.Debugw("UniVlanConfigFsm StateMachine does not exist, no flow processing", log.Fields{
				"device-id": dh.deviceID})
		}

	} // else: nothing to do
}

//RemoveVlanFilterFsm deletes the stored pointer to the VlanConfigFsm
// intention is to provide this method to be called from VlanConfigFsm itself, when resources (and methods!) are cleaned up
func (dh *deviceHandler) RemoveVlanFilterFsm(apUniPort *onuUniPort) {
	logger.Debugw("remove UniVlanConfigFsm StateMachine", log.Fields{
		"device-id": dh.deviceID, "uniPort": apUniPort.portNo})
	//save to do, even if entry dows not exist
	delete(dh.UniVlanConfigFsmMap, apUniPort.uniID)
}

//storePersUniFlowConfig updates local storage of OnuUniFlowConfig and writes it into kv-store afterwards to have it
//available for potential reconcilement

func (dh *deviceHandler) storePersUniFlowConfig(aUniID uint8, aUniVlanFlowParams *[]uniVlanFlowParams) error {

	if dh.reconciling {
		logger.Debugw("reconciling - don't store persistent UniFlowConfig", log.Fields{"device-id": dh.deviceID})
		return nil
	}
	logger.Debugw("Store or clear persistent UniFlowConfig", log.Fields{"device-id": dh.deviceID})

	pDevEntry := dh.getOnuDeviceEntry(true)
	if pDevEntry == nil {
		logger.Errorw("No valid OnuDevice - aborting", log.Fields{"device-id": dh.deviceID})
		return fmt.Errorf("no valid OnuDevice: %s", dh.deviceID)
	}
	pDevEntry.updateOnuUniFlowConfig(aUniID, aUniVlanFlowParams)

	pDevEntry.lockOnuKVStoreMutex()
	defer pDevEntry.unlockOnuKVStoreMutex()

	// deadline context to ensure completion of background routines waited for
	//20200721: 10s proved to be less in 8*8 ONU test on local vbox machine with debug, might be further adapted
	deadline := time.Now().Add(dh.pOpenOnuAc.maxTimeoutInterAdapterComm) //allowed run time to finish before execution
	dctx, cancel := context.WithDeadline(context.Background(), deadline)

	pDevEntry.resetKvProcessingErrorIndication()
	var wg sync.WaitGroup
	wg.Add(1) // for the 1 go routine to finish

	go pDevEntry.updateOnuKvStore(dctx, &wg)
	dh.waitForCompletion(cancel, &wg, "UpdateKvStore") //wait for background process to finish

	return pDevEntry.getKvProcessingErrorIndication()
}

func (dh *deviceHandler) waitForCompletion(cancel context.CancelFunc, wg *sync.WaitGroup, aCallerIdent string) {
	defer cancel() //ensure termination of context (may be pro forma)
	wg.Wait()
	logger.Debugw("WaitGroup processing completed", log.Fields{
		"device-id": dh.deviceID, "called from": aCallerIdent})
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
