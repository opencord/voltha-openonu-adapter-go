//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
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

//DeviceHandler will interact with the ONT ? device.
type DeviceHandler struct {
	deviceID         string
	DeviceType       string
	adminState       string
	device           *voltha.Device
	logicalDeviceID  string
	ProxyAddressID   string
	ProxyAddressType string

	coreProxy     adapterif.CoreProxy
	AdapterProxy  adapterif.AdapterProxy
	EventProxy    adapterif.EventProxy
	openOnuAc     *OpenONUAC
	transitionMap *TransitionMap
	omciAgent     *OpenOMCIAgent
	ponPort       *voltha.Port
	onuOmciDevice *OnuDeviceEntry
	exitChannel   chan int
	lockDevice    sync.RWMutex
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
	dh.openOnuAc = adapter
	dh.transitionMap = nil
	dh.exitChannel = make(chan int, 1)
	dh.lockDevice = sync.RWMutex{}
	dh.stopCollector = make(chan bool, 2)
	dh.stopHeartbeatCheck = make(chan bool, 2)
	//dh.metrics = pmmetrics.NewPmMetrics(cloned.Id, pmmetrics.Frequency(150), pmmetrics.FrequencyOverride(false), pmmetrics.Grouped(false), pmmetrics.Metrics(pmNames))
	dh.activePorts = sync.Map{}
	//TODO initialize the support classes.
	return &dh
}

// start save the device to the data model
func (dh *DeviceHandler) Start(ctx context.Context) {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	log.Debugw("starting-device-handler", log.Fields{"device": dh.device, "deviceId": dh.deviceID})
	// Add the initial device to the local model
	log.Debug("device-handler-started")
}

// stop stops the device dh.  Not much to do for now
func (dh *DeviceHandler) stop(ctx context.Context) {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	log.Debug("stopping-device-agent")
	dh.exitChannel <- 1
	log.Debug("device-agent-stopped")
}

// ##########################################################################################
// DeviceHandler methods that implement the adapters interface requests ##### begin #########

//AdoptDevice adopts the OLT device
func (dh *DeviceHandler) AdoptDevice(ctx context.Context, device *voltha.Device) {
	log.Infow("Adopt_device", log.Fields{"deviceID": device.Id, "Address": device.GetHostAndPort()})

	if dh.transitionMap == nil {
		dh.transitionMap = NewTransitionMap(dh)
		dh.transitionMap.Handle(ctx, DeviceInit)
	} else {
		log.Debug("AdoptDevice: Agent/device init already done")
	}

	/*
		// Now, set the initial PM configuration for that device
		if err := dh.coreProxy.DevicePMConfigUpdate(nil, dh.metrics.ToPmConfigs()); err != nil {
			log.Errorw("error-updating-PMs", log.Fields{"deviceId": device.Id, "error": err})
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
	log.Debugw("InterAdapter message header", log.Fields{"msgID": msgID, "msgType": msgType,
		"fromTopic": fromTopic, "toTopic": toTopic, "toDeviceID": toDeviceID, "proxyDeviceID": proxyDeviceID})

	switch msgType {
	case ic.InterAdapterMessageType_OMCI_REQUEST:
		{
			/* TOBECHECKED: I assume, ONT Adapter receives the message hier already 'unmarshalled'? else: (howTo?)*/
			msgBody := msg.GetBody()

			omciMsg := &ic.InterAdapterOmciMessage{}
			if err := ptypes.UnmarshalAny(msgBody, omciMsg); err != nil {
				log.Warnw("cannot-unmarshal-omci-msg-body", log.Fields{"error": err})
				return err
			}

			//assuming omci message content is already hex coded! with restricted output of 16 bytes here
			log.Debugw("inter-adapter-recv-omci", log.Fields{"OmciMessage": omciMsg.Message[:16]})
			//receive_message(omci_msg.message)
			return dh.onuOmciDevice.DevOmciCC.ReceiveMessage(context.TODO(), omciMsg.Message)
		}
	case ic.InterAdapterMessageType_ONU_IND_REQUEST:
		{
			/* TOBECHECKED: I assume, ONT Adapter receives the message hier already 'unmarshalled'? else: see above omci block */
			msgBody := msg.GetBody()

			onu_indication := &oop.OnuIndication{}
			if err := ptypes.UnmarshalAny(msgBody, onu_indication); err != nil {
				log.Warnw("cannot-unmarshal-onu-indication-msg-body", log.Fields{"error": err})
				return err
			}

			onu_operstate := onu_indication.GetOperState()
			log.Debugw("inter-adapter-recv-onu-ind", log.Fields{"OnuId": onu_indication.GetOnuId(),
				"AdminState": onu_indication.GetAdminState(), "OperState": onu_operstate,
				"SNR": onu_indication.GetSerialNumber()})

			//interface related functioons might be error checked ....
			if onu_operstate == "up" {
				dh.create_interface(onu_indication)
			} else if (onu_operstate == "down") || (onu_operstate == "unreachable") {
				dh.update_interface(onu_indication)
			} else {
				log.Errorw("unknown-onu-indication operState", log.Fields{"OnuId": onu_indication.GetOnuId()})
				return errors.New("InvalidOperState")
			}
		}
	default:
		{
			log.Errorw("inter-adapter-unhandled-type", log.Fields{"msgType": msg.Header.Type})
			return errors.New("unimplemented")
		}
	}

	/* form py code:
	   elif request.header.type == InterAdapterMessageType.TECH_PROFILE_DOWNLOAD_REQUEST:
	       tech_msg = InterAdapterTechProfileDownloadMessage()
	       request.body.Unpack(tech_msg)
	       self.log.debug('inter-adapter-recv-tech-profile', tech_msg=tech_msg)

	       self.load_and_configure_tech_profile(tech_msg.uni_id, tech_msg.path)

	   elif request.header.type == InterAdapterMessageType.DELETE_GEM_PORT_REQUEST:
	       del_gem_msg = InterAdapterDeleteGemPortMessage()
	       request.body.Unpack(del_gem_msg)
	       self.log.debug('inter-adapter-recv-del-gem', gem_del_msg=del_gem_msg)

	       self.delete_tech_profile(uni_id=del_gem_msg.uni_id,
	                                gem_port_id=del_gem_msg.gem_port_id,
	                                tp_path=del_gem_msg.tp_path)

	   elif request.header.type == InterAdapterMessageType.DELETE_TCONT_REQUEST:
	       del_tcont_msg = InterAdapterDeleteTcontMessage()
	       request.body.Unpack(del_tcont_msg)
	       self.log.debug('inter-adapter-recv-del-tcont', del_tcont_msg=del_tcont_msg)

	       self.delete_tech_profile(uni_id=del_tcont_msg.uni_id,
	                                alloc_id=del_tcont_msg.alloc_id,
	                                tp_path=del_tcont_msg.tp_path)
	   else:
	       self.log.error("inter-adapter-unhandled-type", request=request)
	*/
	return nil
}

//  DeviceHandler methods that implement the adapters interface requests## end #########
// #####################################################################################

// ################  to be updated acc. needs of ONT Device ########################
// DeviceHandler StateMachine related state transition methods ##### begin #########

// doStateInit provides the device update to the core
func (dh *DeviceHandler) doStateInit(ctx context.Context) error {
	log.Debug("doStateInit-started")
	/*
		var err error
		dh.clientCon, err = grpc.Dial(dh.device.GetHostAndPort(), grpc.WithInsecure(), grpc.WithBlock())
		if err != nil {
			log.Errorw("Failed to dial device", log.Fields{"DeviceId": dh.deviceID, "HostAndPort": dh.device.GetHostAndPort(), "err": err})
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

	dh.coreProxy.DeviceUpdate(ctx, dh.device)

	// store proxy parameters for later communication - assumption: invariant, else they have to be requested dynamically!!
	dh.ProxyAddressID = dh.device.ProxyAddress.GetDeviceId()
	dh.ProxyAddressType = dh.device.ProxyAddress.GetDeviceType()
	log.Debugw("device-updated", log.Fields{"deviceID": dh.deviceID, "proxyAddressID": dh.ProxyAddressID,
		"proxyAddressType": dh.ProxyAddressType, "SNR": dh.device.SerialNumber,
		"ParentId": dh.device.ParentId, "ParentPortNo": dh.device.ParentPortNo})

	/*
		self._pon = PonPort.create(self, self._pon_port_number)
		self._pon.add_peer(self.parent_id, self._pon_port_number)
		self.log.debug('adding-pon-port-to-agent',
				   type=self._pon.get_port().type,
				   admin_state=self._pon.get_port().admin_state,
				   oper_status=self._pon.get_port().oper_status,
				   )
	*/
	log.Debug("adding-pon-port")
	ponPortNo := uint32(1)
	if dh.device.ParentPortNo != 0 {
		ponPortNo = dh.device.ParentPortNo
	}

	ponPort := &voltha.Port{
		PortNo:     ponPortNo,
		Label:      fmt.Sprintf("pon-%d", ponPortNo),
		Type:       voltha.Port_PON_ONU,
		OperStatus: voltha.OperStatus_ACTIVE,
		Peers: []*voltha.Port_PeerPort{{DeviceId: dh.device.ParentId, // Peer device  is OLT
			PortNo: dh.device.ParentPortNo}}, // Peer port is parent's port number
	}
	var err error
	if err = dh.coreProxy.PortCreated(context.TODO(), dh.deviceID, ponPort); err != nil {
		log.Fatalf("PortCreated-failed-%s", err)
	}

	log.Debug("doStateInit-done")
	return nil
}

// postInit setups the DeviceEntry for the conerned device
func (dh *DeviceHandler) postInit(ctx context.Context) error {
	/*
		dh.Client = oop.NewOpenoltClient(dh.clientCon)
		dh.transitionMap.Handle(ctx, GrpcConnected)
		return nil
	*/
	//start the Agent object with no specific FSM setting
	dh.omciAgent = NewOpenOMCIAgent(ctx, dh.coreProxy, dh.AdapterProxy)
	dh.omciAgent.Start(ctx)
	// might be updated with some error handling !!!
	dh.onuOmciDevice, _ = dh.omciAgent.Add_device(ctx, dh.deviceID, dh)
	//dh.transitionMap.Handle(ctx, GrpcConnected)

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
			self.log.debug('create-pm-metrics', device_id=device.id, serial_number=device.serial_number)
			self._pm_metrics = OnuPmMetrics(self.events, self.core_proxy, self.device_id,
										   self.logical_device_id, device.serial_number,
										   grouped=True, freq_override=False, **kwargs)
			pm_config = self._pm_metrics.make_proto()
			self._onu_omci_device.set_pm_config(self._pm_metrics.omci_pm.openomci_interval_pm)
			self.log.info("initial-pm-config", device_id=device.id, serial_number=device.serial_number)
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
			self.log.info('onu-already-activated')
	*/

	return nil
}

// doStateUp handle the ont up indication and update to voltha core
func (dh *DeviceHandler) doStateUp(ctx context.Context) error {
	/*
		// Synchronous call to update device state - this method is run in its own go routine
		if err := dh.coreProxy.DeviceStateUpdate(ctx, dh.device.Id, voltha.ConnectStatus_REACHABLE,
			voltha.OperStatus_ACTIVE); err != nil {
			log.Errorw("Failed to update device with OLT UP indication", log.Fields{"deviceID": dh.device.Id, "error": err})
			return err
		}
		return nil
	*/
	return errors.New("unimplemented")
}

// doStateDown handle the ont down indication
func (dh *DeviceHandler) doStateDown(ctx context.Context) error {
	dh.lockDevice.Lock()
	defer dh.lockDevice.Unlock()
	log.Debug("do-state-down-start")

	device, err := dh.coreProxy.GetDevice(ctx, dh.device.Id, dh.device.Id)
	if err != nil || device == nil {
		/*TODO: needs to handle error scenarios */
		log.Errorw("Failed to fetch device device", log.Fields{"err": err})
		return errors.New("failed to fetch device device")
	}

	cloned := proto.Clone(device).(*voltha.Device)
	log.Debugw("do-state-down", log.Fields{"ClonedDeviceID": cloned.Id})
	/*
		// Update the all ports state on that device to disable
		if er := dh.coreProxy.PortsStateUpdate(ctx, cloned.Id, voltha.OperStatus_UNKNOWN); er != nil {
			log.Errorw("updating-ports-failed", log.Fields{"deviceID": device.Id, "error": er})
			return er
		}

		//Update the device oper state and connection status
		cloned.OperStatus = voltha.OperStatus_UNKNOWN
		cloned.ConnectStatus = common.ConnectStatus_UNREACHABLE
		dh.device = cloned

		if er := dh.coreProxy.DeviceStateUpdate(ctx, cloned.Id, cloned.ConnectStatus, cloned.OperStatus); er != nil {
			log.Errorw("error-updating-device-state", log.Fields{"deviceID": device.Id, "error": er})
			return er
		}

		//get the child device for the parent device
		onuDevices, err := dh.coreProxy.GetChildDevices(ctx, dh.device.Id)
		if err != nil {
			log.Errorw("failed to get child devices information", log.Fields{"deviceID": dh.device.Id, "error": err})
			return err
		}
		for _, onuDevice := range onuDevices.Items {

			// Update onu state as down in onu adapter
			onuInd := oop.OnuIndication{}
			onuInd.OperState = "down"
			er := dh.AdapterProxy.SendInterAdapterMessage(ctx, &onuInd, ic.InterAdapterMessageType_ONU_IND_REQUEST,
				"openolt", onuDevice.Type, onuDevice.Id, onuDevice.ProxyAddress.DeviceId, "")
			if er != nil {
				log.Errorw("Failed to send inter-adapter-message", log.Fields{"OnuInd": onuInd,
					"From Adapter": "openolt", "DevieType": onuDevice.Type, "DeviceID": onuDevice.Id})
				//Do not return here and continue to process other ONUs
			}
		}
		// * Discovered ONUs entries need to be cleared , since after OLT
		//   is up, it starts sending discovery indications again* /
		dh.discOnus = sync.Map{}
		log.Debugw("do-state-down-end", log.Fields{"deviceID": device.Id})
		return nil
	*/
	return errors.New("unimplemented")
}

// doStateConnected get the device info and update to voltha core
// for comparison of the original method (not that easy to uncomment): compare here:
//  voltha-openolt-adapter/adaptercore/device_handler.go
//  -> this one obviously initiates all communication interfaces of the device ...?
func (dh *DeviceHandler) doStateConnected(ctx context.Context) error {
	log.Debug("OLT device has been connected")
	return errors.New("unimplemented")
}

// DeviceHandler StateMachine related state transition methods ##### end #########
// #################################################################################

// ###################################################
// DeviceHandler utility methods ##### begin #########

// doStateInit provides the device update to the core
func (dh *DeviceHandler) create_interface(onuind *oop.OnuIndication) error {
	log.Debug("create_interface-started - not yet fully implemented (only device state update)")

	if err := dh.coreProxy.DeviceStateUpdate(context.TODO(), dh.deviceID, voltha.ConnectStatus_REACHABLE, voltha.OperStatus_ACTIVATING); err != nil {
		log.Errorw("error-updating-device-state", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	device, err := dh.coreProxy.GetDevice(context.TODO(), dh.device.Id, dh.device.Id)
	if err != nil || device == nil {
		/*TODO: needs to handle error scenarios */
		log.Errorw("Failed to fetch device device at creating If", log.Fields{"err": err})
		return errors.New("Voltha Device not found")
	}

	log.Debug("starting-openomci-statemachine not yet done!!")
	//!!!TODO....
	//self._subscribe_to_events()

	go dh.onuOmciDevice.Start(context.TODO())
	if err := dh.coreProxy.DeviceReasonUpdate(context.TODO(), dh.deviceID, "starting-openomci"); err != nil {
		log.Errorw("error-DeviceReasonUpdate to starting-openomci", log.Fields{"deviceID": dh.deviceID, "error": err})
	}

	/* this might be a good time for Omci Verify message?  */
	omci_verify := NewOmciTestRequest(context.TODO(),
		dh.device.Id, dh.onuOmciDevice.omciAgent,
		true, true) //eclusive and allowFailure (anyway not yet checked)
	omci_verify.PerformOmciTest(context.TODO())

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
	       self.log.info("starting-pm-collection", device_name=self.name, default_freq=self.default_freq)
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
	       self.log.info('perform-test', entity_class=self._entity_class,
	                     entity_id=self._entity_id)
	       try:
	           frame = MEFrame(self._entity_class, self._entity_id, []).test()
	           result = yield self._device.omci_cc.send(frame)
	           if not result.fields['omci_message'].fields['success_code']:
	               self.log.info('Self-Test Submitted Successfully',
	                             code=result.fields[
	                                 'omci_message'].fields['success_code'])
	           else:
	               raise TestFailure('Test Failure: {}'.format(
	                   result.fields['omci_message'].fields['success_code']))
	       except TimeoutError as e:
	           self.deferred.errback(failure.Failure(e))

	       except Exception as e:
	           self.log.exception('perform-test-Error', e=e,
	                              class_id=self._entity_class,
	                              entity_id=self._entity_id)
	           self.deferred.errback(failure.Failure(e))

	*/

	// PM related heartbeat??? !!!TODO....
	//self._heartbeat.enabled = True

	//example how to call FSM - transition up to state "uploading"
	if dh.onuOmciDevice.MibSyncFsm.Is("disabled") {

		dh.onuOmciDevice.MibSyncFsm.Event("start")
		log.Info("state of MibSyncFsm", log.Fields{"state": string(dh.onuOmciDevice.MibSyncFsm.Current())})

		//Determine ONU status and start/re-start MIB Synchronization tasks
		//Determine if this ONU has ever synchronized
		if true { //TODO: insert valid check

			dh.onuOmciDevice.MibSyncFsm.Event("load_mib_template")
			log.Info("state of MibSyncFsm", log.Fields{"state": string(dh.onuOmciDevice.MibSyncFsm.Current())})
			//Find and load a mib template. If not found proceed with mib_upload
			// callbacks to be handled:
			// Event("success")
			// Event("timeout")
			//no mib template found
			if true { //TODO: insert valid check

				log.Debug("state of MibSyncFsm: trigger upload Mib via channel")
				mibSyncMsg := Message{
					Type: TestMsg,
					Data: TestMessage{
						TestMessageVal: AnyTriggerForMibSyncUploadMib,
					},
				}
				dh.onuOmciDevice.MibSyncChan <- mibSyncMsg

				//dh.onuOmciDevice.MibSyncFsm.Event("upload_mib")

				log.Info("state of MibSyncFsm", log.Fields{"state": string(dh.onuOmciDevice.MibSyncFsm.Current())})
				//Begin full MIB data upload, starting with a MIB RESET
				// callbacks to be handled:
				// success: e.Event("success")
				// failure: e.Event("timeout")
			}
		} else {
			dh.onuOmciDevice.MibSyncFsm.Event("examine_mds")
			log.Info("state of MibSyncFsm", log.Fields{"state": string(dh.onuOmciDevice.MibSyncFsm.Current())})
			//Examine the MIB Data Sync
			// callbacks to be handled:
			// Event("success")
			// Event("timeout")
			// Event("mismatch")
		}
	} else {
		log.Errorw("wrong state of MibSyncFsm - want: disabled", log.Fields{"have": string(dh.onuOmciDevice.MibSyncFsm.Current())})
	}

	return nil
}

func (dh *DeviceHandler) update_interface(onuind *oop.OnuIndication) error {
	log.Debug("update_interface-started - not yet implemented")
	return nil
}
