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
	"sync"
	"time"

	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	"github.com/opencord/voltha-protos/v3/go/openflow_13"
	"github.com/opencord/voltha-protos/v3/go/voltha"

	"test.internal/openadapter/internal/pkg/config"
)

//OpenONUAC structure holds the ONU core information
type OpenONUAC struct {
	deviceHandlers              map[string]*deviceHandler
	deviceHandlersCreateChan    map[string]chan bool //channels for deviceHandler create events
	coreProxy                   adapterif.CoreProxy
	adapterProxy                adapterif.AdapterProxy
	eventProxy                  adapterif.EventProxy
	kafkaICProxy                kafka.InterContainerProxy
	kvClient                    kvstore.Client
	config                      *config.AdapterFlags
	numOnus                     int
	KVStoreHost                 string
	KVStorePort                 int
	KVStoreType                 string
	KVStoreTimeout              time.Duration
	exitChannel                 chan int
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	AcceptIncrementalEvto       bool
	//GrpcTimeoutInterval         time.Duration
	lockDeviceHandlersMap      sync.RWMutex
	pSupportedFsms             *OmciDeviceFsms
	maxTimeoutInterAdapterComm time.Duration
}

//NewOpenONUAC returns a new instance of OpenONU_AC
func NewOpenONUAC(ctx context.Context, kafkaICProxy kafka.InterContainerProxy,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy,
	eventProxy adapterif.EventProxy, kvClient kvstore.Client, cfg *config.AdapterFlags) *OpenONUAC {
	var openOnuAc OpenONUAC
	openOnuAc.exitChannel = make(chan int, 1)
	openOnuAc.deviceHandlers = make(map[string]*deviceHandler)
	openOnuAc.deviceHandlersCreateChan = make(map[string]chan bool)
	openOnuAc.kafkaICProxy = kafkaICProxy
	openOnuAc.config = cfg
	openOnuAc.numOnus = cfg.OnuNumber
	openOnuAc.coreProxy = coreProxy
	openOnuAc.adapterProxy = adapterProxy
	openOnuAc.eventProxy = eventProxy
	openOnuAc.kvClient = kvClient
	openOnuAc.KVStoreHost = cfg.KVStoreHost
	openOnuAc.KVStorePort = cfg.KVStorePort
	openOnuAc.KVStoreType = cfg.KVStoreType
	openOnuAc.KVStoreTimeout = cfg.KVStoreTimeout
	openOnuAc.HeartbeatCheckInterval = cfg.HeartbeatCheckInterval
	openOnuAc.HeartbeatFailReportInterval = cfg.HeartbeatFailReportInterval
	openOnuAc.AcceptIncrementalEvto = cfg.AccIncrEvto
	openOnuAc.maxTimeoutInterAdapterComm = cfg.MaxTimeoutInterAdapterComm
	//openOnuAc.GrpcTimeoutInterval = cfg.GrpcTimeoutInterval
	openOnuAc.lockDeviceHandlersMap = sync.RWMutex{}

	openOnuAc.pSupportedFsms = &OmciDeviceFsms{
		"mib-synchronizer": {
			//mibSyncFsm,        // Implements the MIB synchronization state machine
			mibDbVolatileDictImpl, // Implements volatile ME MIB database
			//true,                  // Advertise events on OpenOMCI event bus
			cMibAuditDelayImpl, // Time to wait between MIB audits.  0 to disable audits.
			// map[string]func() error{
			// 	"mib-upload":    onuDeviceEntry.MibUploadTask,
			// 	"mib-template":  onuDeviceEntry.MibTemplateTask,
			// 	"get-mds":       onuDeviceEntry.GetMdsTask,
			// 	"mib-audit":     onuDeviceEntry.GetMdsTask,
			// 	"mib-resync":    onuDeviceEntry.MibResyncTask,
			// 	"mib-reconcile": onuDeviceEntry.MibReconcileTask,
			// },
		},
	}

	return &openOnuAc
}

//Start starts (logs) the adapter
func (oo *OpenONUAC) Start(ctx context.Context) error {
	logger.Info("starting-openonu-adapter")
	logger.Info("openonu-adapter-started")
	return nil
}

/*
//stop terminates the session
func (oo *OpenONUAC) stop(ctx context.Context) error {
	logger.Info("stopping-device-manager")
	oo.exitChannel <- 1
	logger.Info("device-manager-stopped")
	return nil
}
*/

func (oo *OpenONUAC) addDeviceHandlerToMap(ctx context.Context, agent *deviceHandler) {
	oo.lockDeviceHandlersMap.Lock()
	defer oo.lockDeviceHandlersMap.Unlock()
	if _, exist := oo.deviceHandlers[agent.deviceID]; !exist {
		oo.deviceHandlers[agent.deviceID] = agent
		oo.deviceHandlers[agent.deviceID].start(ctx)
		if _, exist := oo.deviceHandlersCreateChan[agent.deviceID]; exist {
			logger.Debugw("deviceHandler created - trigger processing of pending ONU_IND_REQUEST", log.Fields{"device-id": agent.deviceID})
			oo.deviceHandlersCreateChan[agent.deviceID] <- true
		}
	}
}

func (oo *OpenONUAC) deleteDeviceHandlerToMap(agent *deviceHandler) {
	oo.lockDeviceHandlersMap.Lock()
	defer oo.lockDeviceHandlersMap.Unlock()
	delete(oo.deviceHandlers, agent.deviceID)
	delete(oo.deviceHandlersCreateChan, agent.deviceID)
}

//getDeviceHandler gets the ONU deviceHandler and may wait until it is created
func (oo *OpenONUAC) getDeviceHandler(deviceID string, aWait bool) *deviceHandler {
	oo.lockDeviceHandlersMap.Lock()
	agent, ok := oo.deviceHandlers[deviceID]
	if aWait && !ok {
		logger.Debugw("deviceHandler not present - wait for creation or timeout", log.Fields{"device-id": deviceID})
		if _, exist := oo.deviceHandlersCreateChan[deviceID]; !exist {
			oo.deviceHandlersCreateChan[deviceID] = make(chan bool, 1)
		}
		//keep the read sema short to allow for subsequent write
		oo.lockDeviceHandlersMap.Unlock()
		// based on concurrent processing the deviceHandler creation may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(1 * time.Second): //timer may be discussed ...
			logger.Warnw("No valid deviceHandler created after max WaitTime", log.Fields{"device-id": deviceID})
			return nil
		case <-oo.deviceHandlersCreateChan[deviceID]:
			logger.Debugw("deviceHandler is ready now - continue", log.Fields{"device-id": deviceID})
			// if written now, we can return the written value without sema
			return oo.deviceHandlers[deviceID]
		}
	}
	oo.lockDeviceHandlersMap.Unlock()
	return agent
}

// Adapter interface required methods ############## begin #########
// #################################################################

// for original content compare: (needs according deviceHandler methods)
// /voltha-openolt-adapter/adaptercore/openolt.go

// Adopt_device creates a new device handler if not present already and then adopts the device
func (oo *OpenONUAC) Adopt_device(device *voltha.Device) error {
	if device == nil {
		logger.Warn("voltha-device-is-nil")
		return errors.New("nil-device")
	}
	ctx := context.Background()
	logger.Infow("adopt-device", log.Fields{"device-id": device.Id})
	var handler *deviceHandler
	if handler = oo.getDeviceHandler(device.Id, false); handler == nil {
		handler := newDeviceHandler(oo.coreProxy, oo.adapterProxy, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		go handler.adoptOrReconcileDevice(ctx, device)
		// Launch the creation of the device topic
		// go oo.createDeviceTopic(device)
	}
	return nil
}

//Get_ofp_device_info returns OFP information for the given device
func (oo *OpenONUAC) Get_ofp_device_info(device *voltha.Device) (*ic.SwitchCapability, error) {
	logger.Errorw("device-handler-not-set", log.Fields{"device-id": device.Id})
	return nil, fmt.Errorf("device-handler-not-set %s", device.Id)
}

//Get_ofp_port_info returns OFP port information for the given device
//200630: method removed as per [VOL-3202]: OF port info is now to be delivered within UniPort create
// cmp changes in onu_uni_port.go::CreateVolthaPort()

//Process_inter_adapter_message sends messages to a target device (between adapters)
func (oo *OpenONUAC) Process_inter_adapter_message(msg *ic.InterAdapterMessage) error {
	logger.Debugw("Process_inter_adapter_message", log.Fields{"msgId": msg.Header.Id,
		"msgProxyDeviceId": msg.Header.ProxyDeviceId, "msgToDeviceId": msg.Header.ToDeviceId})

	var waitForDhInstPresent bool
	//ToDeviceId should address a DeviceHandler instance
	targetDevice := msg.Header.ToDeviceId
	// As a workaround this handling is only required for the OnuIndication with OperState=Up event.
	// But we live without that further check and use this processing also for OperState down/unreachable events to avoid
	// the deeper message processing at this stage. Should do no harm on the other events (except for run time)
	if msg.Header.Type != ic.InterAdapterMessageType_ONU_IND_REQUEST {
		waitForDhInstPresent = false
	} else {
		//Race condition (relevant in BBSIM-environment only): Due to unsynchronized processing of olt-adapter and rw_core,
		//ONU_IND_REQUEST msg by olt-adapter could arrive a little bit earlier than rw_core was able to announce the corresponding
		//ONU by RPC of Adopt_device()
		logger.Debugw("ONU_IND_REQUEST - potentially wait until DeviceHandler instance is created", log.Fields{"device-id": targetDevice})
		waitForDhInstPresent = true
	}
	if handler := oo.getDeviceHandler(targetDevice, waitForDhInstPresent); handler != nil {
		/* 200724: modification towards synchronous implementation - possible errors within processing shall be
		 * 	 in the accordingly delayed response, some timing effect might result in Techprofile processing for multiple UNI's
		 */
		return handler.processInterAdapterMessage(msg)
		/* so far the processing has been in background with according commented error treatment restrictions:
		go handler.ProcessInterAdapterMessage(msg)
		// error treatment might be more sophisticated
		// by now let's just accept the message on 'communication layer'
		// message content problems have to be evaluated then in the handler
		//   and are by now not reported to the calling party (to force what reaction there?)
		return nil
		*/
	}
	logger.Warnw("no handler found for received Inter-Proxy-message", log.Fields{
		"msgToDeviceId": targetDevice})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", targetDevice))
}

//Adapter_descriptor not implemented
func (oo *OpenONUAC) Adapter_descriptor() error {
	return errors.New("unImplemented")
}

//Device_types unimplemented
func (oo *OpenONUAC) Device_types() (*voltha.DeviceTypes, error) {
	return nil, errors.New("unImplemented")
}

//Health  returns unimplemented
func (oo *OpenONUAC) Health() (*voltha.HealthStatus, error) {
	return nil, errors.New("unImplemented")
}

//Reconcile_device is called once when the adapter needs to re-create device - usually on core restart
func (oo *OpenONUAC) Reconcile_device(device *voltha.Device) error {
	if device == nil {
		logger.Warn("voltha-device-is-nil")
		return errors.New("nil-device")
	}
	ctx := context.Background()
	logger.Infow("Reconcile_device", log.Fields{"device-id": device.Id})
	var handler *deviceHandler
	if handler = oo.getDeviceHandler(device.Id, false); handler == nil {
		handler := newDeviceHandler(oo.coreProxy, oo.adapterProxy, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		handler.device = device
		handler.reconciling = true
		go handler.adoptOrReconcileDevice(ctx, handler.device)
		// reconcilement will be continued after onu-device entry is added
	} else {
		return fmt.Errorf(fmt.Sprintf("device-already-reconciled-or-active-%s", device.Id))
	}
	return nil
}

//Abandon_device unimplemented
func (oo *OpenONUAC) Abandon_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Disable_device disables the given device
func (oo *OpenONUAC) Disable_device(device *voltha.Device) error {
	logger.Debugw("Disable_device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(device.Id, false); handler != nil {
		go handler.disableDevice(device)
		return nil
	}
	logger.Warnw("no handler found for device-disable", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reenable_device enables the onu device after disable
func (oo *OpenONUAC) Reenable_device(device *voltha.Device) error {
	logger.Debugw("Reenable_device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(device.Id, false); handler != nil {
		go handler.reEnableDevice(device)
		return nil
	}
	logger.Warnw("no handler found for device-reenable", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reboot_device reboots the given device
func (oo *OpenONUAC) Reboot_device(device *voltha.Device) error {
	logger.Debugw("Reboot-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(device.Id, false); handler != nil {
		go handler.rebootDevice(device)
		return nil
	}
	logger.Warnw("no handler found for device-reboot", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-#{device.Id}"))
}

//Self_test_device unimplemented
func (oo *OpenONUAC) Self_test_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

// Delete_device deletes the given device
func (oo *OpenONUAC) Delete_device(device *voltha.Device) error {
	logger.Debugw("Delete_device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(device.Id, false); handler != nil {
		err := handler.deleteDevice(device)
		//don't leave any garbage - even in error case
		oo.deleteDeviceHandlerToMap(handler)
		if err != nil {
			return err
		}
	} else {
		logger.Warnw("no handler found for device-deletion", log.Fields{"device-id": device.Id})
		return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
	}
	return nil
}

//Get_device_details unimplemented
func (oo *OpenONUAC) Get_device_details(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Update_flows_bulk returns
func (oo *OpenONUAC) Update_flows_bulk(device *voltha.Device, flows *voltha.Flows, groups *voltha.FlowGroups, flowMetadata *voltha.FlowMetadata) error {
	return errors.New("unImplemented")
}

//Update_flows_incrementally updates (add/remove) the flows on a given device
func (oo *OpenONUAC) Update_flows_incrementally(device *voltha.Device,
	flows *openflow_13.FlowChanges, groups *openflow_13.FlowGroupChanges, flowMetadata *voltha.FlowMetadata) error {

	//flow config is relayed to handler even if the device might be in some 'inactive' state
	// let the handler or related FSM's decide, what to do with the modified flow state info
	// at least the flow-remove must be done in respect to internal data, while OMCI activity might not be needed here

	// For now, there is no support for group changes (as in the actual Py-adapter code)
	//   but processing is continued for flowUpdate possibly also set in the request
	if groups.ToAdd != nil && groups.ToAdd.Items != nil {
		logger.Warnw("Update-flow-incr: group add not supported (ignored)", log.Fields{"device-id": device.Id})
	}
	if groups.ToRemove != nil && groups.ToRemove.Items != nil {
		logger.Warnw("Update-flow-incr: group remove not supported (ignored)", log.Fields{"device-id": device.Id})
	}
	if groups.ToUpdate != nil && groups.ToUpdate.Items != nil {
		logger.Warnw("Update-flow-incr: group update not supported (ignored)", log.Fields{"device-id": device.Id})
	}

	if handler := oo.getDeviceHandler(device.Id, false); handler != nil {
		err := handler.FlowUpdateIncremental(flows, groups, flowMetadata)
		return err
	}
	logger.Warnw("no handler found for incremental flow update", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Update_pm_config returns PmConfigs nil or error
func (oo *OpenONUAC) Update_pm_config(device *voltha.Device, pmConfigs *voltha.PmConfigs) error {
	return errors.New("unImplemented")
}

//Receive_packet_out sends packet out to the device
func (oo *OpenONUAC) Receive_packet_out(deviceID string, egressPortNo int, packet *openflow_13.OfpPacketOut) error {
	return errors.New("unImplemented")
}

//Suppress_event unimplemented
func (oo *OpenONUAC) Suppress_event(filter *voltha.EventFilter) error {
	return errors.New("unImplemented")
}

//Unsuppress_event  unimplemented
func (oo *OpenONUAC) Unsuppress_event(filter *voltha.EventFilter) error {
	return errors.New("unImplemented")
}

//Download_image unimplemented
func (oo *OpenONUAC) Download_image(device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Get_image_download_status unimplemented
func (oo *OpenONUAC) Get_image_download_status(device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Cancel_image_download unimplemented
func (oo *OpenONUAC) Cancel_image_download(device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Activate_image_update unimplemented
func (oo *OpenONUAC) Activate_image_update(device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Revert_image_update unimplemented
func (oo *OpenONUAC) Revert_image_update(device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

// Enable_port to Enable PON/NNI interface - seems not to be used/required according to python code
func (oo *OpenONUAC) Enable_port(deviceID string, port *voltha.Port) error {
	return errors.New("unImplemented")
}

// Disable_port to Disable pon/nni interface  - seems not to be used/required according to python code
func (oo *OpenONUAC) Disable_port(deviceID string, port *voltha.Port) error {
	return errors.New("unImplemented")
}

//Child_device_lost - unimplemented
//needed for if update >= 3.1.x
func (oo *OpenONUAC) Child_device_lost(deviceID string, pPortNo uint32, onuID uint32) error {
	return errors.New("unImplemented")
}

// Start_omci_test unimplemented
func (oo *OpenONUAC) Start_omci_test(device *voltha.Device, request *voltha.OmciTestRequest) (*voltha.TestResponse, error) {
	return nil, errors.New("unImplemented")
}

// Get_ext_value - unimplemented
func (oo *OpenONUAC) Get_ext_value(deviceID string, device *voltha.Device, valueparam voltha.ValueType_Type) (*voltha.ReturnValues, error) {
	return nil, errors.New("unImplemented")
}

// Adapter interface required methods ################ end #########
// #################################################################
