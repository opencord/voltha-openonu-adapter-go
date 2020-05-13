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
	"strings"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/looplab/fsm"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	vc "github.com/opencord/voltha-protos/v3/go/common"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	of "github.com/opencord/voltha-protos/v3/go/openflow_13"
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

	coreProxy       adapterif.CoreProxy
	AdapterProxy    adapterif.AdapterProxy
	EventProxy      adapterif.EventProxy
	pOpenOnuAc      *OpenONUAC
	pDeviceStateFsm *fsm.FSM
	pPonPort        *voltha.Port
	pOnuOmciDevice  *OnuDeviceEntry
	exitChannel     chan int
	lockDevice      sync.RWMutex
	pOnuIndication  *oop.OnuIndication

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
	uniEntityMap       map[uint32]*OnuUniPort
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
	dh.stopCollector = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	dh.activePorts = sync.Map{}
	//TODO initialize the support classes.
	dh.uniEntityMap = make(map[uint32]*OnuUniPort)

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

func (dh *DeviceHandler) GetOfpPortInfo(device *voltha.Device,
	portNo int64) (*ic.PortCapability, error) {
	logger.Debugw("GetOfpPortInfo start", log.Fields{"deviceID": device.Id, "portNo": portNo})

	//function body as per OLTAdapter handler code
	// adapted with values from py dapter code
	if pUniPort, exist := dh.uniEntityMap[uint32(portNo)]; exist {
		var macOctets [6]uint8
		macOctets[5] = 0x08
		macOctets[4] = uint8(dh.ponPortNumber >> 8)
		macOctets[3] = uint8(dh.ponPortNumber)
		macOctets[2] = uint8(portNo >> 16)
		macOctets[1] = uint8(portNo >> 8)
		macOctets[0] = uint8(portNo)
		hwAddr := genMacFromOctets(macOctets)
		capacity := uint32(of.OfpPortFeatures_OFPPF_1GB_FD | of.OfpPortFeatures_OFPPF_FIBER)
		name := device.SerialNumber + "-" + strconv.FormatUint(uint64(pUniPort.macBpNo), 10)
		ofUniPortState := of.OfpPortState_OFPPS_LINK_DOWN
		if pUniPort.operState == vc.OperStatus_ACTIVE {
			ofUniPortState = of.OfpPortState_OFPPS_LIVE
		}
		logger.Debugw("setting LogicalPort", log.Fields{"with-name": name,
			"withUniPort": pUniPort.name, "withMacBase": hwAddr, "OperState": ofUniPortState})

		return &ic.PortCapability{
			Port: &voltha.LogicalPort{
				OfpPort: &of.OfpPort{
					Name: name,
					//HwAddr:     macAddressToUint32Array(dh.device.MacAddress),
					HwAddr:     macAddressToUint32Array(hwAddr),
					Config:     0,
					State:      uint32(ofUniPortState),
					Curr:       capacity,
					Advertised: capacity,
					Peer:       capacity,
					CurrSpeed:  uint32(of.OfpPortFeatures_OFPPF_1GB_FD),
					MaxSpeed:   uint32(of.OfpPortFeatures_OFPPF_1GB_FD),
				},
				DeviceId:     device.Id,
				DevicePortNo: uint32(portNo),
			},
		}, nil
	}
	logger.Warnw("No UniPort found - abort", log.Fields{"for PortNo": uint32(portNo)})
	return nil, errors.New("UniPort not found")
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
	logger.Debugw("create_interface-started", log.Fields{"OnuId": onuind.GetOnuId(),
		"OnuIntfId": onuind.GetIntfId(), "OnuSerialNumber": onuind.GetSerialNumber()})

	dh.pOnuIndication = onuind // let's revise if storing the pointer is sufficient...

	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
		logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	// It does not look to me as if makes sense to work with the real core device here, (not the stored clone)?
	// in this code the GetDevice would just make a check if the DeviceID's Device still exists in core
	// in python code it looks as the started onu_omci_device might have been updated with some new instance state of the core device
	// but I would not know why, and the go code anyway dows not work with the device directly anymore in the OnuDeviceEntry
	// so let's just try to keep it simple ...
	/*
			device, err := dh.coreProxy.GetDevice(context.TODO(), dh.device.Id, dh.device.Id)
		    if err != nil || device == nil {
					//TODO: needs to handle error scenarios
				logger.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
				return errors.New("Voltha Device not found")
			}
	*/

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

	//call MibUploadFSM - transition up to state "in_sync"
	pMibUlFsm := dh.GetOnuDeviceEntry().pMibUploadFsm.pFsm
	if pMibUlFsm != nil {
		if pMibUlFsm.Is("disabled") {
			if err := pMibUlFsm.Event("start"); err != nil {
				logger.Errorw("MibSyncFsm: Can't go to state starting", log.Fields{"err": err})
				return errors.New("Can't go to state starting")
			} else {
				logger.Debug("MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
				//Determine ONU status and start/re-start MIB Synchronization tasks
				//Determine if this ONU has ever synchronized
				if true { //TODO: insert valid check
					if err := pMibUlFsm.Event("load_mib_template"); err != nil {
						logger.Errorw("MibSyncFsm: Can't go to state loading_mib_template", log.Fields{"err": err})
						return errors.New("Can't go to state loading_mib_template")
					} else {
						logger.Debug("MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
						//Find and load a mib template. If not found proceed with mib_upload
						// callbacks to be handled:
						// Event("success")
						// Event("timeout")
						//no mib template found
						if true { //TODO: insert valid check
							if err := pMibUlFsm.Event("upload_mib"); err != nil {
								logger.Errorw("MibSyncFsm: Can't go to state uploading", log.Fields{"err": err})
								return errors.New("Can't go to state uploading")
							} else {
								logger.Debug("state of MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
								//Begin full MIB data upload, starting with a MIB RESET
								// callbacks to be handled:
								// success: e.Event("success")
								// failure: e.Event("timeout")
							}
						}
					}
				} else {
					pMibUlFsm.Event("examine_mds")
					logger.Debug("state of MibSyncFsm", log.Fields{"state": string(pMibUlFsm.Current())})
					//Examine the MIB Data Sync
					// callbacks to be handled:
					// Event("success")
					// Event("timeout")
					// Event("mismatch")
				}
			}
		} else {
			logger.Errorw("wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(pMibUlFsm.Current())})
			return errors.New("wrong state of MibSyncFsm")
		}
	} else {
		logger.Errorw("MibSyncFsm invalid - cannot be executed!!", log.Fields{"deviceID": dh.deviceID})
	}
	return nil
}

func (dh *DeviceHandler) update_interface(onuind *oop.OnuIndication) error {
	logger.Debug("update_interface-started - not yet implemented")
	return nil
}

func (dh *DeviceHandler) DeviceStateUpdate(dev_Event OnuDeviceEvent) {
	if dev_Event == MibDatabaseSync {
		logger.Debugw("MibInSync event: update dev state to 'MibSync complete'", log.Fields{"deviceID": dh.deviceID})
		//initiate DevStateUpdate
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "discovery-mibsync-complete"); err != nil {
			logger.Errorw("error-DeviceReasonUpdate to 'mibsync-complete'", log.Fields{"deviceID": dh.deviceID, "error": err})
		}

		unigMap, ok := dh.GetOnuDeviceEntry().pOnuDB.meDb[me.UniGClassID]
		unigInstKeys := dh.GetOnuDeviceEntry().pOnuDB.GetSortedInstKeys(unigMap)
		if ok {
			i := uint16(0)
			for _, mgmtEntityId := range unigInstKeys {
				logger.Debugw("Add UNI port for stored UniG instance:", log.Fields{"deviceId": dh.GetOnuDeviceEntry().deviceID, "UnigMe EntityID": mgmtEntityId})
				dh.addUniPort(mgmtEntityId, i, UniPPTP)
				i++
			}
		} else {
			logger.Warnw("No UniG instances found!", log.Fields{"deviceId": dh.GetOnuDeviceEntry().deviceID})
		}

		/*  real Mib download procedure could look somthing like this:
		 ***** but for the moment the FSM is still limited (sending no OMCI)  *****
		 ***** thus never reaches 'downloaded' state                          *****
		 */
		pMibDlFsm := dh.GetOnuDeviceEntry().pMibDownloadFsm.pFsm
		if pMibDlFsm != nil {
			if pMibDlFsm.Is("disabled") {
				if err := pMibDlFsm.Event("start"); err != nil {
					logger.Errorw("MibDownloadFsm: Can't go to state starting", log.Fields{"err": err})
					// maybe try a FSM restart and then again ... - TODO!!!
				} else {
					logger.Debug("MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
					// maybe use more specific states here for the specific download steps ...
					if err := pMibDlFsm.Event("download_mib"); err != nil {
						logger.Errorw("MibDownloadFsm: Can't go to state downloading", log.Fields{"err": err})
					} else {
						logger.Debug("state of MibDownloadFsm", log.Fields{"state": string(pMibDlFsm.Current())})
						//Begin MIB data download
					}
				}
			} else {
				logger.Errorw("wrong state of MibDownloadFsm - want: disabled", log.Fields{"have": string(pMibDlFsm.Current())})
			}
			/***** Mib download started */
		} else {
			logger.Errorw("MibDownloadFsm invalid - cannot be executed!!", log.Fields{"deviceID": dh.deviceID})
		}

		//shortcut code to fake download-done!!!:  TODO!!! to be removed with complete DL FSM
		go dh.GetOnuDeviceEntry().transferSystemEvent(MibDownloadDone)

	} else if dev_Event == MibDownloadDone {
		logger.Debugw("MibDownloadDone event: update dev state to 'Oper.Active'", log.Fields{"deviceID": dh.deviceID})
		//initiate DevStateUpdate
		if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID,
			voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVE); err != nil {
			logger.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
		}
		logger.Debug("MibDownloadDone Event: update dev reason to 'initial-mib-downloaded'")
		if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "initial-mib-downloaded"); err != nil {
			logger.Errorw("error-DeviceReasonUpdate to 'initial-mib-downloaded'",
				log.Fields{"deviceID": dh.deviceID, "error": err})
		}

		go dh.enableUniPortStateUpdate(dh.deviceID) //cmp python yield self.enable_ports()

		raisedTs := time.Now().UnixNano()
		go dh.sendOnuOperStateEvent(voltha.OperStatus_ACTIVE, dh.deviceID, raisedTs) //cmp python onu_active_event
	} else {
		logger.Warnw("unhandled-device-event", log.Fields{"deviceID": dh.deviceID, "event": dev_Event})
	}
}

func (dh *DeviceHandler) addUniPort(a_uniInstNo uint16, a_uniId uint16, a_portType UniPortType) {
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
			// create announce the UniPort to the core as VOLTHA Port object
			if err := pUniPort.CreateVolthaPort(dh); err == nil {
				logger.Infow("onuUniPort-added", log.Fields{"for PortNo": uniNo})
			} //error logging already within UniPort method
		}
	}
}

// Enable listen on UniPortState changes and update core port state accordingly
func (dh *DeviceHandler) enableUniPortStateUpdate(a_deviceID string) {
	//  py code was updated 2003xx to activate the real ONU UNI ports per OMCI (VEIP or PPTP)
	//    but towards core only the first port active state is signalled
	//    with following remark:
	//       # TODO: for now only support the first UNI given no requirement for multiple uni yet. Also needed to reduce flow
	//       #  load on the core

	// dh.lock_ports(false) ONU port activation via OMCI //TODO!!! not yet supported

	for uniNo, uniPort := range dh.uniEntityMap {
		// only if this port is validated for operState transfer}
		if (1<<uniPort.uniId)&ActiveUniPortStateUpdateMask == (1 << uniPort.uniId) {
			logger.Infow("onuUniPort-forced-OperState-ACTIVE", log.Fields{"for PortNo": uniNo})
			uniPort.SetOperState(vc.OperStatus_ACTIVE)
			//maybe also use getter functions on uniPort - perhaps later ...
			go dh.coreProxy.PortStateUpdate(context.TODO(), a_deviceID, voltha.Port_ETHERNET_UNI, uniPort.portNo, uniPort.operState)
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

/* *********************************************************** */

func genMacFromOctets(a_octets [6]uint8) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		a_octets[5], a_octets[4], a_octets[3],
		a_octets[2], a_octets[1], a_octets[0])
}

//copied from OLT Adapter: unify centrally ?
func macAddressToUint32Array(mac string) []uint32 {
	slist := strings.Split(mac, ":")
	result := make([]uint32, len(slist))
	var err error
	var tmp int64
	for index, val := range slist {
		if tmp, err = strconv.ParseInt(val, 16, 32); err != nil {
			return []uint32{1, 2, 3, 4, 5, 6}
		}
		result[index] = uint32(tmp)
	}
	return result
}
