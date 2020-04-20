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
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/looplab/fsm"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
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

//DeviceHandler will interact with the ONU ? device.
type DeviceHandler struct {
	deviceID         string
	DeviceType       string
	adminState       string
	device           *voltha.Device
	logicalDeviceID  string
	ProxyAddressID   string
	ProxyAddressType string

	coreProxy       adapterif.CoreProxy
	AdapterProxy    adapterif.AdapterProxy
	EventProxy      adapterif.EventProxy
	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	pPonPort        *voltha.Port
	pOnuOmciDevice  *OnuDeviceEntry
	exitChannel     chan int
	lockDevice      sync.RWMutex

	//Client        oop.OpenoltClient
	//clientCon     *grpc.ClientConn
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
	uniEntityMap       map[uint16]*OnuUniPort
}

/*
//OnuDevice represents ONU related info
type OnuDevice struct {
	deviceID      string
	deviceType    string
	serialNumber  string
	onuID         uint32
	intfID        uint32
	proxyDeviceID string
	uniPorts      map[uint32]struct{}
}

//NewOnuDevice creates a new Onu Device
func NewOnuDevice(devID, deviceTp, serialNum string, onuID, intfID uint32, proxyDevID string) *OnuDevice {
	var device OnuDevice
	device.deviceID = devID
	device.deviceType = deviceTp
	device.serialNumber = serialNum
	device.onuID = onuID
	device.intfID = intfID
	device.proxyDeviceID = proxyDevID
	device.uniPorts = make(map[uint32]struct{})
	return &device
}
*/

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
	dh.stopCollector = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	dh.activePorts = sync.Map{}
	//TODO initialize the support classes.
	dh.uniEntityMap = make(map[uint16]*OnuUniPort)

	// Device related state machine
	dh.pDeviceStateFsm = fsm.NewFSM(
		"null",
		fsm.Events{
			{Name: "DeviceInit", Src: []string{"null", "down"}, Dst: "init"},
			{Name: "GrpcConnected", Src: []string{"init"}, Dst: "connected"},
			{Name: "GrpcDisconnected", Src: []string{"connected", "down"}, Dst: "init"},
			{Name: "DeviceUpInd", Src: []string{"connected", "down"}, Dst: "up"},
			{Name: "DeviceDownInd", Src: []string{"up"}, Dst: "down"},
		},
		fsm.Callbacks{
			"before_event":            func(e *fsm.Event) { dh.logStateChange(e) },
			"before_DeviceInit":       func(e *fsm.Event) { dh.doStateInit(e) },
			"after_DeviceInit":        func(e *fsm.Event) { dh.postInit(e) },
			"before_GrpcConnected":    func(e *fsm.Event) { dh.doStateConnected(e) },
			"before_GrpcDisconnected": func(e *fsm.Event) { dh.doStateInit(e) },
			"after_GrpcDisconnected":  func(e *fsm.Event) { dh.postInit(e) },
			"before_DeviceUpInd":      func(e *fsm.Event) { dh.doStateUp(e) },
			"before_DeviceDownInd":    func(e *fsm.Event) { dh.doStateDown(e) },
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

//AdoptDevice adopts the OLT device
func (dh *DeviceHandler) AdoptDevice(ctx context.Context, device *voltha.Device) {
	logger.Debugw("Adopt_device", log.Fields{"deviceID": device.Id, "Address": device.GetHostAndPort()})

	logger.Debug("Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
	if dh.pDeviceStateFsm.Is("null") {
		if err := dh.pDeviceStateFsm.Event("DeviceInit"); err != nil {
			logger.Errorw("Device FSM: Can't go to state DeviceInit", log.Fields{"err": err})
		}
		logger.Debug("Device FSM: ", log.Fields{"state": string(dh.pDeviceStateFsm.Current())})
	} else {
		logger.Debug("AdoptDevice: Agent/device init already done")
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
			/* TOBECHECKED: I assume, ONU Adapter receives the message hier already 'unmarshalled'? else: (howTo?)*/
			msgBody := msg.GetBody()

			omciMsg := &ic.InterAdapterOmciMessage{}
			if err := ptypes.UnmarshalAny(msgBody, omciMsg); err != nil {
				logger.Warnw("cannot-unmarshal-omci-msg-body", log.Fields{"error": err})
				return err
			}

			//assuming omci message content is hex coded!
			// with restricted output of 16(?) bytes would be ...omciMsg.Message[:16]
			logger.Debugw("inter-adapter-recv-omci",
				log.Fields{"RxOmciMessage": hex.EncodeToString(omciMsg.Message)})
			//receive_message(omci_msg.message)
			return dh.GetOnuDeviceEntry().PDevOmciCC.ReceiveMessage(context.TODO(), omciMsg.Message)
		}
	case ic.InterAdapterMessageType_ONU_IND_REQUEST:
		{
			/* TOBECHECKED: I assume, ONU Adapter receives the message hier already 'unmarshalled'? else: see above omci block */
			msgBody := msg.GetBody()

			onu_indication := &oop.OnuIndication{}
			if err := ptypes.UnmarshalAny(msgBody, onu_indication); err != nil {
				logger.Warnw("cannot-unmarshal-onu-indication-msg-body", log.Fields{"error": err})
				return err
			}

			onu_operstate := onu_indication.GetOperState()
			logger.Debugw("inter-adapter-recv-onu-ind", log.Fields{"OnuId": onu_indication.GetOnuId(),
				"AdminState": onu_indication.GetAdminState(), "OperState": onu_operstate,
				"SNR": onu_indication.GetSerialNumber()})

			//interface related functioons might be error checked ....
			if onu_operstate == "up" {
				dh.create_interface(onu_indication)
			} else if (onu_operstate == "down") || (onu_operstate == "unreachable") {
				dh.update_interface(onu_indication)
			} else {
				logger.Errorw("unknown-onu-indication operState", log.Fields{"OnuId": onu_indication.GetOnuId()})
				return errors.New("InvalidOperState")
			}
		}
	default:
		{
			logger.Errorw("inter-adapter-unhandled-type", log.Fields{"msgType": msg.Header.Type})
			return errors.New("unimplemented")
		}
	}

	/* form py code:
	   elif request.header.type == InterAdapterMessageType.TECH_PROFILE_DOWNLOAD_REQUEST:
	       tech_msg = InterAdapterTechProfileDownloadMessage()
	       request.body.Unpack(tech_msg)
	       self.logger.debug('inter-adapter-recv-tech-profile', tech_msg=tech_msg)

	       self.load_and_configure_tech_profile(tech_msg.uni_id, tech_msg.path)

	   elif request.header.type == InterAdapterMessageType.DELETE_GEM_PORT_REQUEST:
	       del_gem_msg = InterAdapterDeleteGemPortMessage()
	       request.body.Unpack(del_gem_msg)
	       self.logger.debug('inter-adapter-recv-del-gem', gem_del_msg=del_gem_msg)

	       self.delete_tech_profile(uni_id=del_gem_msg.uni_id,
	                                gem_port_id=del_gem_msg.gem_port_id,
	                                tp_path=del_gem_msg.tp_path)

	   elif request.header.type == InterAdapterMessageType.DELETE_TCONT_REQUEST:
	       del_tcont_msg = InterAdapterDeleteTcontMessage()
	       request.body.Unpack(del_tcont_msg)
	       self.logger.debug('inter-adapter-recv-del-tcont', del_tcont_msg=del_tcont_msg)

	       self.delete_tech_profile(uni_id=del_tcont_msg.uni_id,
	                                alloc_id=del_tcont_msg.alloc_id,
	                                tp_path=del_tcont_msg.tp_path)
	   else:
	       self.logger.error("inter-adapter-unhandled-type", request=request)
	*/
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

	/*
		var err error
		dh.clientCon, err = grpc.Dial(dh.device.GetHostAndPort(), grpc.WithInsecure(), grpc.WithBlock())
		if err != nil {
			logger.Errorw("Failed to dial device", log.Fields{"DeviceId": dh.deviceID, "HostAndPort": dh.device.GetHostAndPort(), "err": err})
			return err
		}
		return nil
	*/

	// populate what we know.  rest comes later after mib sync
	dh.device.Root = false
	dh.device.Vendor = "OpenONU"
	dh.device.Model = "go"
	dh.device.Reason = "activating-onu"
	dh.logicalDeviceID = dh.deviceID

	dh.coreProxy.DeviceUpdate(context.TODO(), dh.device)

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	logger.Debugw("device-updated", log.Fields{"deviceID": dh.deviceID, "proxyAddressID": dh.ProxyAddressID,
		"proxyAddressType": dh.ProxyAddressType, "SNR": dh.device.SerialNumber,
		"ParentId": dh.device.ParentId, "ParentPortNo": dh.device.ParentPortNo})

	/*
		self._pon = PonPort.create(self, self._pon_port_number)
		self._pon.add_peer(self.parent_id, self._pon_port_number)
		self.logger.debug('adding-pon-port-to-agent',
				   type=self._pon.get_port().type,
				   admin_state=self._pon.get_port().admin_state,
				   oper_status=self._pon.get_port().oper_status,
				   )
	*/
	logger.Debug("adding-pon-port")
	pPonPortNo := uint32(1)
	if dh.device.ParentPortNo != 0 {
		pPonPortNo = dh.device.ParentPortNo
	}

	pPonPort := &voltha.Port{
		PortNo:     pPonPortNo,
		Label:      fmt.Sprintf("pon-%d", pPonPortNo),
		Type:       voltha.Port_PON_ONU,
		OperStatus: voltha.OperStatus_ACTIVE,
		Peers: []*voltha.Port_PeerPort{{DeviceId: dh.device.ParentId, // Peer device  is OLT
			PortNo: dh.device.ParentPortNo}}, // Peer port is parent's port number
	}
	if err = dh.coreProxy.PortCreated(context.TODO(), dh.deviceID, pPonPort); err != nil {
		logger.Fatalf("Device FSM: PortCreated-failed-%s", err)
		e.Cancel(err)
		return
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
	if err = dh.Add_OnuDeviceEntry(context.TODO()); err != nil {
		logger.Fatalf("Device FSM: Add_OnuDeviceEntry-failed-%s", err)
		e.Cancel(err)
		return
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
	return
	logger.Debug("doStateConnected-done")
}

// doStateUp handle the onu up indication and update to voltha core
func (dh *DeviceHandler) doStateUp(e *fsm.Event) {

	logger.Debug("doStateUp-started")
	var err error
	err = errors.New("Device FSM: function not implemented yet!")
	e.Cancel(err)
	return
	logger.Debug("doStateUp-done")

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

	device, err := dh.coreProxy.GetDevice(context.TODO(), dh.device.Id, dh.device.Id)
	if err != nil || device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Errorw("Failed to fetch device device", log.Fields{"err": err})
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
	return
	logger.Debug("doStateDown-done")
}

// DeviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// DeviceHandler utility methods ##### begin #########

// Get ONU device entry for this deviceId specific handler
func (dh *DeviceHandler) GetOnuDeviceEntry() *OnuDeviceEntry {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	if dh.pOnuOmciDevice != nil {
		logger.Debugw("GetOnuDeviceEntry params:",
			log.Fields{"onu_device_entry": dh.pOnuOmciDevice, "device_id": dh.pOnuOmciDevice.deviceID,
				"device_handler": dh.pOnuOmciDevice.baseDeviceHandler, "core_proxy": dh.pOnuOmciDevice.coreProxy, "adapter_proxy": dh.pOnuOmciDevice.adapterProxy})
	} else {
		logger.Error("GetOnuDeviceEntry returns nil")
	}
	return dh.pOnuOmciDevice
}

// Set ONU device entry
func (dh *DeviceHandler) SetOnuDeviceEntry(pDeviceEntry *OnuDeviceEntry) error {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	dh.pOnuOmciDevice = pDeviceEntry
	return nil
}

//creates a new ONU device or returns the existing
func (dh *DeviceHandler) Add_OnuDeviceEntry(ctx context.Context) error {
	logger.Debugw("adding-deviceEntry", log.Fields{"for deviceId": dh.deviceID})

	deviceEntry := dh.GetOnuDeviceEntry()
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = NewOnuDeviceEntry(ctx, dh.deviceID, dh, dh.coreProxy, dh.AdapterProxy,
			dh.pOpenOnuAc.pSupportedFsms) //nil as FSM pointer would yield deviceEntry internal defaults ...
		//error treatment possible //TODO!!!
		dh.SetOnuDeviceEntry(deviceEntry)
		logger.Infow("onuDeviceEntry-added", log.Fields{"for deviceId": dh.deviceID})
	} else {
		logger.Infow("onuDeviceEntry-add: Device already exists", log.Fields{"for deviceId": dh.deviceID})
	}
	// might be updated with some error handling !!!
	return nil
}

// doStateInit provides the device update to the core
func (dh *DeviceHandler) create_interface(onuind *oop.OnuIndication) error {
	logger.Debug("create_interface-started - not yet fully implemented (only device state update)")

	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
		logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	device, err := dh.coreProxy.GetDevice(context.TODO(), dh.device.Id, dh.device.Id)
	if err != nil || device == nil {
		/*TODO: needs to handle error scenarios */
		logger.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
		return errors.New("Voltha Device not found")
	}

	dh.GetOnuDeviceEntry().Start(context.TODO())
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "starting-openomci"); err != nil {
		logger.Errorw("error-DeviceReasonUpdate to starting-openomci", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	/* this might be a good time for Omci Verify message?  */
	verifyExec := make(chan bool)
	omci_verify := NewOmciTestRequest(context.TODO(),
		dh.device.Id, dh.GetOnuDeviceEntry().PDevOmciCC,
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

	//example how to call FSM - transition up to state "uploading"
	if dh.GetOnuDeviceEntry().MibSyncFsm.Is("disabled") {

		if err := dh.GetOnuDeviceEntry().MibSyncFsm.Event("start"); err != nil {
			logger.Errorw("MibSyncFsm: Can't go to state starting", log.Fields{"err": err})
			return errors.New("Can't go to state starting")
		} else {
			logger.Debug("MibSyncFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibSyncFsm.Current())})
			//Determine ONU status and start/re-start MIB Synchronization tasks
			//Determine if this ONU has ever synchronized
			if true { //TODO: insert valid check
				if err := dh.GetOnuDeviceEntry().MibSyncFsm.Event("load_mib_template"); err != nil {
					logger.Errorw("MibSyncFsm: Can't go to state loading_mib_template", log.Fields{"err": err})
					return errors.New("Can't go to state loading_mib_template")
				} else {
					logger.Debug("MibSyncFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibSyncFsm.Current())})
					//Find and load a mib template. If not found proceed with mib_upload
					// callbacks to be handled:
					// Event("success")
					// Event("timeout")
					//no mib template found
					if true { //TODO: insert valid check
						if err := dh.GetOnuDeviceEntry().MibSyncFsm.Event("upload_mib"); err != nil {
							logger.Errorw("MibSyncFsm: Can't go to state uploading", log.Fields{"err": err})
							return errors.New("Can't go to state uploading")
						} else {
							logger.Debug("state of MibSyncFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibSyncFsm.Current())})
							//Begin full MIB data upload, starting with a MIB RESET
							// callbacks to be handled:
							// success: e.Event("success")
							// failure: e.Event("timeout")
						}
					}
				}
			} else {
				dh.GetOnuDeviceEntry().MibSyncFsm.Event("examine_mds")
				logger.Debug("state of MibSyncFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibSyncFsm.Current())})
				//Examine the MIB Data Sync
				// callbacks to be handled:
				// Event("success")
				// Event("timeout")
				// Event("mismatch")
			}
		}
	} else {
		logger.Errorw("wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(dh.GetOnuDeviceEntry().MibSyncFsm.Current())})
		return errors.New("wrong state of MibSyncFsm")
	}
	return nil
}

func (dh *DeviceHandler) update_interface(onuind *oop.OnuIndication) error {
	logger.Debug("update_interface-started - not yet implemented")
	return nil
}

func (dh *DeviceHandler) DeviceStateUpdate(dev_Event OnuDeviceEvent) {
	if dev_Event == MibDatabaseSync {
		logger.Debug("MibInSync event: update dev state to 'MibSync complete'")
		//initiate DevStateUpdate
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "discovery-mibsync-complete"); err != nil {
			logger.Errorw("error-DeviceReasonUpdate to 'mibsync-complete'", log.Fields{"deviceID": dh.deviceID, "error": err})
		}

		// fixed assumption about PPTP/UNI-G ONU-config
		// to be replaced by DB parsing of MibUpload data TODO!!!
		// parameters are: InstanceNo, running UniNo, type
		dh.addUniPort(257, 0, UniPPTP)
		dh.addUniPort(258, 1, UniPPTP)
		dh.addUniPort(259, 2, UniPPTP)
		dh.addUniPort(260, 3, UniPPTP)

		// start the MibDownload (assumed here to be done via some FSM again - open //TODO!!!)
		/* the mib-download code may look something like that:
		if err := dh.GetOnuDeviceEntry().MibDownloadFsm.Event("start"); err != nil {
			logger.Errorw("MibDownloadFsm: Can't go to state starting", log.Fields{"err": err})
			return errors.New("Can't go to state starting")
		} else {
			logger.Debug("MibDownloadFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibDownloadFsm.Current())})
			//Determine ONU status and start/re-start MIB MibDownloadFsm
			//Determine if this ONU has ever synchronized
			if true { //TODO: insert valid check
				if err := dh.GetOnuDeviceEntry().MibSyncFsm.Event("download_mib"); err != nil {
					logger.Errorw("MibDownloadFsm: Can't go to state 'download_mib'", log.Fields{"err": err})
					return errors.New("Can't go to state 'download_mib'")
				} else {
					//some further processing ???
					logger.Debug("state of MibDownloadFsm", log.Fields{"state": string(dh.GetOnuDeviceEntry().MibDownloadFsm.Current())})
					//some further processing ???
				}
			}
		}
		but by now we shortcut the download here and immediately fake the ONU-active state to get the state indication on ONUS!!!:
		*/
		//shortcut code to fake download-done!!!:
		go dh.GetOnuDeviceEntry().transferSystemEvent(MibDownloadDone)
	} else if dev_Event == MibDownloadDone {
		logger.Debug("MibDownloadDone event: update dev state to 'Oper.Active'")
		//initiate DevStateUpdate
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVE); err != nil {
			logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		}
		logger.Debug("MibDownloadDone Event: update dev reasone to 'initial-mib-downloaded'")
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "initial-mib-downloaded"); err != nil {
			logger.Errorw("error-DeviceReasonUpdate to 'initial-mib-downloaded'",
				log.Fields{"deviceID": dh.deviceID, "error": err})
		}

		//TODO !!! following activities according to python code:
		/*
			yield self.enable_ports()
			self._mib_download_task = None
			yield self.onu_active_event()  -> with 'OnuActiveEvent' !!! might be this is required for ONOS visibility??
		*/
	} else {
		logger.Warnw("unhandled-device-event", log.Fields{"event": dev_Event})
	}
}

func (dh *DeviceHandler) addUniPort(a_uniInstNo uint16, a_uniId uint16, a_portType UniPortType) {
	if _, present := dh.uniEntityMap[a_uniInstNo]; present {
		logger.Warnw("onuUniPort-add: Port already exists", log.Fields{"for InstanceId": a_uniInstNo})
	} else {
		//TODO: need to find the ONU intfId and OnuId, using hard coded values for single ONU test!!!
		// parameters are IntfId, OnuId, uniId
		uni_no := MkUniPortNum(0, 1, uint32(a_uniId))
		//with arguments a_uniId, a_portNo, a_portType
		pUniPort := NewOnuUniPort(a_uniId, uni_no, a_uniInstNo, a_portType)
		if pUniPort == nil {
			logger.Warnw("onuUniPort-add: Could not create Port", log.Fields{"for InstanceId": a_uniInstNo})
		} else {
			dh.uniEntityMap[a_uniInstNo] = pUniPort
			// create announce the UniPort to the core as VOLTHA Port object
			if err := pUniPort.CreateVolthaPort(dh); err != nil {
				logger.Infow("onuUniPort-added", log.Fields{"for InstanceId": a_uniInstNo})
			} //error looging already within UniPort method
		}
	}
}
