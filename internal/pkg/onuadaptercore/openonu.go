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
	"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	"github.com/opencord/voltha-protos/v3/go/openflow_13"
	"github.com/opencord/voltha-protos/v3/go/voltha"

	"test.internal/openadapter/internal/pkg/config"
)

//OpenONUAC structure holds the ONU core information
type OpenONUAC struct {
	deviceHandlers              map[string]*DeviceHandler
	coreProxy                   adapterif.CoreProxy
	adapterProxy                adapterif.AdapterProxy
	eventProxy                  adapterif.EventProxy
	kafkaICProxy                kafka.InterContainerProxy
	config                      *config.AdapterFlags
	numOnus                     int
	KVStoreHost                 string
	KVStorePort                 int
	KVStoreType                 string
	exitChannel                 chan int
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	//GrpcTimeoutInterval         time.Duration
	lockDeviceHandlersMap sync.RWMutex
	pSupportedFsms        *OmciDeviceFsms
}

//NewOpenONUAC returns a new instance of OpenONU_AC
func NewOpenONUAC(ctx context.Context, kafkaICProxy kafka.InterContainerProxy,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy,
	eventProxy adapterif.EventProxy, cfg *config.AdapterFlags) *OpenONUAC {
	var openOnuAc OpenONUAC
	openOnuAc.exitChannel = make(chan int, 1)
	openOnuAc.deviceHandlers = make(map[string]*DeviceHandler)
	openOnuAc.kafkaICProxy = kafkaICProxy
	openOnuAc.config = cfg
	openOnuAc.numOnus = cfg.OnuNumber
	openOnuAc.coreProxy = coreProxy
	openOnuAc.adapterProxy = adapterProxy
	openOnuAc.eventProxy = eventProxy
	openOnuAc.KVStoreHost = cfg.KVStoreHost
	openOnuAc.KVStorePort = cfg.KVStorePort
	openOnuAc.KVStoreType = cfg.KVStoreType
	openOnuAc.HeartbeatCheckInterval = cfg.HeartbeatCheckInterval
	openOnuAc.HeartbeatFailReportInterval = cfg.HeartbeatFailReportInterval
	//openOnuAc.GrpcTimeoutInterval = cfg.GrpcTimeoutInterval
	openOnuAc.lockDeviceHandlersMap = sync.RWMutex{}

	openOnuAc.pSupportedFsms = &OmciDeviceFsms{
		"mib-synchronizer": {
			//mibSyncFsm,        // Implements the MIB synchronization state machine
			MibDbVolatileDictImpl, // Implements volatile ME MIB database
			true,                  // Advertise events on OpenOMCI event bus
			cMibAuditDelayImpl,    // Time to wait between MIB audits.  0 to disable audits.
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

//Stop terminates the session
func (oo *OpenONUAC) Stop(ctx context.Context) error {
	logger.Info("stopping-device-manager")
	oo.exitChannel <- 1
	logger.Info("device-manager-stopped")
	return nil
}

func (oo *OpenONUAC) addDeviceHandlerToMap(ctx context.Context, agent *DeviceHandler) {
	oo.lockDeviceHandlersMap.Lock()
	defer oo.lockDeviceHandlersMap.Unlock()
	if _, exist := oo.deviceHandlers[agent.deviceID]; !exist {
		oo.deviceHandlers[agent.deviceID] = agent
		oo.deviceHandlers[agent.deviceID].Start(ctx)
	}
}

func (oo *OpenONUAC) deleteDeviceHandlerToMap(agent *DeviceHandler) {
	oo.lockDeviceHandlersMap.Lock()
	defer oo.lockDeviceHandlersMap.Unlock()
	delete(oo.deviceHandlers, agent.deviceID)
}

func (oo *OpenONUAC) getDeviceHandler(deviceID string) *DeviceHandler {
	oo.lockDeviceHandlersMap.Lock()
	defer oo.lockDeviceHandlersMap.Unlock()
	if agent, ok := oo.deviceHandlers[deviceID]; ok {
		return agent
	}
	return nil
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
	logger.Infow("adopt-device", log.Fields{"deviceId": device.Id})
	var handler *DeviceHandler
	if handler = oo.getDeviceHandler(device.Id); handler == nil {
		handler := NewDeviceHandler(oo.coreProxy, oo.adapterProxy, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		go handler.AdoptDevice(ctx, device)
		// Launch the creation of the device topic
		// go oo.createDeviceTopic(device)
	}
	return nil
}

//Get_ofp_device_info returns OFP information for the given device
func (oo *OpenONUAC) Get_ofp_device_info(device *voltha.Device) (*ic.SwitchCapability, error) {
	logger.Errorw("device-handler-not-set", log.Fields{"deviceId": device.Id})
	return nil, errors.New("device-handler-not-set")
}

//Get_ofp_port_info returns OFP port information for the given device
func (oo *OpenONUAC) Get_ofp_port_info(device *voltha.Device, portNo int64) (*ic.PortCapability, error) {
	//this method expects a return value to be sent to the core
	// and internal processing should not take that long
	// so it makes no sense to try to work asynchronously here
	logger.Infow("get-ofp-port-info started", log.Fields{"deviceId": device.Id, "portNo": portNo})
	// basically the same code as in openOlt.go - unify???
	if handler := oo.getDeviceHandler(device.Id); handler != nil {
		return handler.GetOfpPortInfo(device, portNo)
		// error treatment might be more sophisticated, but indeed it would be logged within handler
	}
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found for deviceId %s", device.Id))
	//return nil, olterrors.NewErrNotFound("device-handler", log.Fields{"device-id": device.Id}, nil)
}

//Process_inter_adapter_message sends messages to a target device (between adapters)
func (oo *OpenONUAC) Process_inter_adapter_message(msg *ic.InterAdapterMessage) error {
	logger.Debugw("Process_inter_adapter_message", log.Fields{"msgId": msg.Header.Id,
		"msgProxyDeviceId": msg.Header.ProxyDeviceId, "msgToDeviceId": msg.Header.ToDeviceId})

	targetDevice := msg.Header.ToDeviceId
	//ToDeviceId should address an DeviceHandler instance
	if handler := oo.getDeviceHandler(targetDevice); handler != nil {
		go handler.ProcessInterAdapterMessage(msg)
		// error treatment might be more sophisticated
		// by now let's just accept the message on 'communication layer'
		// message content problems have to be evaluated then in the handler
		//   and are by now not reported to the calling party (to force what reaction there?)
		return nil
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

//Reconcile_device unimplemented
func (oo *OpenONUAC) Reconcile_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Abandon_device unimplemented
func (oo *OpenONUAC) Abandon_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Disable_device disables the given device
func (oo *OpenONUAC) Disable_device(device *voltha.Device) error {
	logger.Debugw("Disable_device", log.Fields{"deviceId": device.Id})
	if handler := oo.getDeviceHandler(device.Id); handler != nil {
		go handler.DisableDevice(device)
		return nil
	}
	logger.Warnw("no handler found for device-disable", log.Fields{"deviceId": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reenable_device enables the onu device after disable
func (oo *OpenONUAC) Reenable_device(device *voltha.Device) error {
	logger.Debugw("Reenable_device", log.Fields{"deviceId": device.Id})
	if handler := oo.getDeviceHandler(device.Id); handler != nil {
		go handler.ReenableDevice(device)
		return nil
	}
	logger.Warnw("no handler found for device-reenable", log.Fields{"deviceId": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reboot_device reboots the given device
func (oo *OpenONUAC) Reboot_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Self_test_device unimplemented
func (oo *OpenONUAC) Self_test_device(device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Delete_device unimplemented
func (oo *OpenONUAC) Delete_device(device *voltha.Device) error {
	return errors.New("unImplemented")
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
func (oo *OpenONUAC) Update_flows_incrementally(device *voltha.Device, flows *openflow_13.FlowChanges, groups *openflow_13.FlowGroupChanges, flowMetadata *voltha.FlowMetadata) error {
	return errors.New("unImplemented")
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

//needed for if update >= 3.1.x
func (oo *OpenONUAC) Child_device_lost(deviceID string, pPortNo uint32, onuID uint32) error {
	return errors.New("unImplemented")
}

func (oo *OpenONUAC) Start_omci_test(device *voltha.Device, request *voltha.OmciTestRequest) (*voltha.TestResponse, error) {
	return nil, errors.New("unImplemented")
}

func (oo *OpenONUAC) Get_ext_value(deviceID string, device *voltha.Device, valueparam voltha.ValueType_Type) (*voltha.ReturnValues, error) {
	return nil, errors.New("unImplemented")
}

// Adapter interface required methods ################ end #########
// #################################################################
