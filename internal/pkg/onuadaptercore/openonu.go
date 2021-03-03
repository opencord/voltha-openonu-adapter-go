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

	conf "github.com/opencord/voltha-lib-go/v4/pkg/config"

	"github.com/golang/protobuf/ptypes"
	"github.com/opencord/voltha-lib-go/v4/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/extension"
	ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	"github.com/opencord/voltha-protos/v4/go/openflow_13"
	oop "github.com/opencord/voltha-protos/v4/go/openolt"
	"github.com/opencord/voltha-protos/v4/go/voltha"

	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/config"
)

//OpenONUAC structure holds the ONU core information
type OpenONUAC struct {
	deviceHandlers              map[string]*deviceHandler
	deviceHandlersCreateChan    map[string]chan bool //channels for deviceHandler create events
	lockDeviceHandlersMap       sync.RWMutex
	coreProxy                   adapterif.CoreProxy
	adapterProxy                adapterif.AdapterProxy
	eventProxy                  eventif.EventProxy
	kafkaICProxy                kafka.InterContainerProxy
	kvClient                    kvstore.Client
	cm                          *conf.ConfigManager
	config                      *config.AdapterFlags
	numOnus                     int
	KVStoreAddress              string
	KVStoreType                 string
	KVStoreTimeout              time.Duration
	mibTemplatesGenerated       map[string]bool
	lockMibTemplateGenerated    sync.RWMutex
	exitChannel                 chan int
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	AcceptIncrementalEvto       bool
	//GrpcTimeoutInterval         time.Duration
	pSupportedFsms             *OmciDeviceFsms
	maxTimeoutInterAdapterComm time.Duration
	maxTimeoutReconciling      time.Duration
	pDownloadManager           *adapterDownloadManager
	metricsEnabled             bool
	mibAuditInterval           time.Duration
}

//NewOpenONUAC returns a new instance of OpenONU_AC
func NewOpenONUAC(ctx context.Context, kafkaICProxy kafka.InterContainerProxy,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy,
	eventProxy eventif.EventProxy, kvClient kvstore.Client, cfg *config.AdapterFlags, cm *conf.ConfigManager) *OpenONUAC {
	var openOnuAc OpenONUAC
	openOnuAc.exitChannel = make(chan int, 1)
	openOnuAc.deviceHandlers = make(map[string]*deviceHandler)
	openOnuAc.deviceHandlersCreateChan = make(map[string]chan bool)
	openOnuAc.lockDeviceHandlersMap = sync.RWMutex{}
	openOnuAc.kafkaICProxy = kafkaICProxy
	openOnuAc.config = cfg
	openOnuAc.cm = cm
	openOnuAc.numOnus = cfg.OnuNumber
	openOnuAc.coreProxy = coreProxy
	openOnuAc.adapterProxy = adapterProxy
	openOnuAc.eventProxy = eventProxy
	openOnuAc.kvClient = kvClient
	openOnuAc.KVStoreAddress = cfg.KVStoreAddress
	openOnuAc.KVStoreType = cfg.KVStoreType
	openOnuAc.KVStoreTimeout = cfg.KVStoreTimeout
	openOnuAc.mibTemplatesGenerated = make(map[string]bool)
	openOnuAc.lockMibTemplateGenerated = sync.RWMutex{}
	openOnuAc.HeartbeatCheckInterval = cfg.HeartbeatCheckInterval
	openOnuAc.HeartbeatFailReportInterval = cfg.HeartbeatFailReportInterval
	openOnuAc.AcceptIncrementalEvto = cfg.AccIncrEvto
	openOnuAc.maxTimeoutInterAdapterComm = cfg.MaxTimeoutInterAdapterComm
	openOnuAc.maxTimeoutReconciling = cfg.MaxTimeoutReconciling
	//openOnuAc.GrpcTimeoutInterval = cfg.GrpcTimeoutInterval
	openOnuAc.metricsEnabled = cfg.MetricsEnabled
	openOnuAc.mibAuditInterval = cfg.MibAuditInterval

	openOnuAc.pSupportedFsms = &OmciDeviceFsms{
		"mib-synchronizer": {
			//mibSyncFsm,        // Implements the MIB synchronization state machine
			mibDbVolatileDictImpl, // Implements volatile ME MIB database
			//true,                  // Advertise events on OpenOMCI event bus
			openOnuAc.mibAuditInterval, // Time to wait between MIB audits.  0 to disable audits.
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

	openOnuAc.pDownloadManager = newAdapterDownloadManager(ctx)

	return &openOnuAc
}

//Start starts (logs) the adapter
func (oo *OpenONUAC) Start(ctx context.Context) error {
	logger.Info(ctx, "starting-openonu-adapter")

	return nil
}

/*
//stop terminates the session
func (oo *OpenONUAC) stop(ctx context.Context) error {
	logger.Info(ctx,"stopping-device-manager")
	oo.exitChannel <- 1
	logger.Info(ctx,"device-manager-stopped")
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
			logger.Debugw(ctx, "deviceHandler created - trigger processing of pending ONU_IND_REQUEST", log.Fields{"device-id": agent.deviceID})
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
func (oo *OpenONUAC) getDeviceHandler(ctx context.Context, deviceID string, aWait bool) *deviceHandler {
	oo.lockDeviceHandlersMap.Lock()
	agent, ok := oo.deviceHandlers[deviceID]
	if aWait && !ok {
		logger.Infow(ctx, "Race condition: deviceHandler not present - wait for creation or timeout",
			log.Fields{"device-id": deviceID})
		if _, exist := oo.deviceHandlersCreateChan[deviceID]; !exist {
			oo.deviceHandlersCreateChan[deviceID] = make(chan bool, 1)
		}
		deviceCreateChan := oo.deviceHandlersCreateChan[deviceID]
		//keep the read sema short to allow for subsequent write
		oo.lockDeviceHandlersMap.Unlock()
		// based on concurrent processing the deviceHandler creation may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(1 * time.Second): //timer may be discussed ...
			logger.Warnw(ctx, "No valid deviceHandler created after max WaitTime", log.Fields{"device-id": deviceID})
			return nil
		case <-deviceCreateChan:
			logger.Debugw(ctx, "deviceHandler is ready now - continue", log.Fields{"device-id": deviceID})
			oo.lockDeviceHandlersMap.RLock()
			defer oo.lockDeviceHandlersMap.RUnlock()
			return oo.deviceHandlers[deviceID]
		}
	}
	oo.lockDeviceHandlersMap.Unlock()
	return agent
}

func (oo *OpenONUAC) processInterAdapterONUIndReqMessage(ctx context.Context, msg *ic.InterAdapterMessage) error {
	msgBody := msg.GetBody()
	onuIndication := &oop.OnuIndication{}
	if err := ptypes.UnmarshalAny(msgBody, onuIndication); err != nil {
		logger.Warnw(ctx, "onu-ind-request-cannot-unmarshal-msg-body", log.Fields{"error": err})
		return err
	}
	//ToDeviceId should address a DeviceHandler instance
	targetDevice := msg.Header.ToDeviceId

	onuOperstate := onuIndication.GetOperState()
	waitForDhInstPresent := false
	if onuOperstate == "up" {
		//Race condition (relevant in BBSIM-environment only): Due to unsynchronized processing of olt-adapter and rw_core,
		//ONU_IND_REQUEST msg by olt-adapter could arrive a little bit earlier than rw_core was able to announce the corresponding
		//ONU by RPC of Adopt_device(). Therefore it could be necessary to wait with processing of ONU_IND_REQUEST until call of
		//Adopt_device() arrived and DeviceHandler instance was created
		waitForDhInstPresent = true
	}
	if handler := oo.getDeviceHandler(ctx, targetDevice, waitForDhInstPresent); handler != nil {
		logger.Infow(ctx, "onu-ind-request", log.Fields{"device-id": targetDevice,
			"OnuId":      onuIndication.GetOnuId(),
			"AdminState": onuIndication.GetAdminState(), "OperState": onuOperstate,
			"SNR": onuIndication.GetSerialNumber()})

		if onuOperstate == "up" {
			return handler.createInterface(ctx, onuIndication)
		} else if (onuOperstate == "down") || (onuOperstate == "unreachable") {
			return handler.updateInterface(ctx, onuIndication)
		} else {
			logger.Errorw(ctx, "unknown-onu-ind-request operState", log.Fields{"OnuId": onuIndication.GetOnuId()})
			return fmt.Errorf("invalidOperState: %s, %s", onuOperstate, targetDevice)
		}
	}
	logger.Warnw(ctx, "no handler found for received onu-ind-request", log.Fields{
		"msgToDeviceId": targetDevice})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", targetDevice))
}

// Adapter interface required methods ############## begin #########
// #################################################################

// for original content compare: (needs according deviceHandler methods)
// /voltha-openolt-adapter/adaptercore/openolt.go

// Adopt_device creates a new device handler if not present already and then adopts the device
func (oo *OpenONUAC) Adopt_device(ctx context.Context, device *voltha.Device) error {
	if device == nil {
		logger.Warn(ctx, "voltha-device-is-nil")
		return errors.New("nil-device")
	}
	logger.Infow(ctx, "adopt-device", log.Fields{"device-id": device.Id})
	var handler *deviceHandler
	if handler = oo.getDeviceHandler(ctx, device.Id, false); handler == nil {
		handler := newDeviceHandler(ctx, oo.coreProxy, oo.adapterProxy, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		go handler.adoptOrReconcileDevice(ctx, device)
		// Launch the creation of the device topic
		// go oo.createDeviceTopic(device)
	}
	return nil
}

//Get_ofp_device_info returns OFP information for the given device
func (oo *OpenONUAC) Get_ofp_device_info(ctx context.Context, device *voltha.Device) (*ic.SwitchCapability, error) {
	logger.Errorw(ctx, "device-handler-not-set", log.Fields{"device-id": device.Id})
	return nil, fmt.Errorf("device-handler-not-set %s", device.Id)
}

//Get_ofp_port_info returns OFP port information for the given device
//200630: method removed as per [VOL-3202]: OF port info is now to be delivered within UniPort create
// cmp changes in onu_uni_port.go::CreateVolthaPort()

//Process_inter_adapter_message sends messages to a target device (between adapters)
func (oo *OpenONUAC) Process_inter_adapter_message(ctx context.Context, msg *ic.InterAdapterMessage) error {
	logger.Debugw(ctx, "Process_inter_adapter_message", log.Fields{"msgId": msg.Header.Id,
		"msgProxyDeviceId": msg.Header.ProxyDeviceId, "msgToDeviceId": msg.Header.ToDeviceId, "Type": msg.Header.Type})

	if msg.Header.Type == ic.InterAdapterMessageType_ONU_IND_REQUEST {
		// we have to handle ONU_IND_REQUEST already here - see comments in processInterAdapterONUIndReqMessage()
		return oo.processInterAdapterONUIndReqMessage(ctx, msg)
	}
	//ToDeviceId should address a DeviceHandler instance
	targetDevice := msg.Header.ToDeviceId
	if handler := oo.getDeviceHandler(ctx, targetDevice, false); handler != nil {
		/* 200724: modification towards synchronous implementation - possible errors within processing shall be
		 * 	 in the accordingly delayed response, some timing effect might result in Techprofile processing for multiple UNI's
		 */
		return handler.processInterAdapterMessage(ctx, msg)
		/* so far the processing has been in background with according commented error treatment restrictions:
		go handler.ProcessInterAdapterMessage(msg)
		// error treatment might be more sophisticated
		// by now let's just accept the message on 'communication layer'
		// message content problems have to be evaluated then in the handler
		//   and are by now not reported to the calling party (to force what reaction there?)
		return nil
		*/
	}
	logger.Warnw(ctx, "no handler found for received Inter-Proxy-message", log.Fields{
		"msgToDeviceId": targetDevice})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", targetDevice))
}

//Adapter_descriptor not implemented
func (oo *OpenONUAC) Adapter_descriptor(ctx context.Context) error {
	return errors.New("unImplemented")
}

//Device_types unimplemented
func (oo *OpenONUAC) Device_types(ctx context.Context) (*voltha.DeviceTypes, error) {
	return nil, errors.New("unImplemented")
}

//Health  returns unimplemented
func (oo *OpenONUAC) Health(ctx context.Context) (*voltha.HealthStatus, error) {
	return nil, errors.New("unImplemented")
}

//Reconcile_device is called once when the adapter needs to re-create device - usually on core restart
func (oo *OpenONUAC) Reconcile_device(ctx context.Context, device *voltha.Device) error {
	if device == nil {
		logger.Warn(ctx, "reconcile-device-voltha-device-is-nil")
		return errors.New("nil-device")
	}
	logger.Infow(ctx, "reconcile-device", log.Fields{"device-id": device.Id})
	var handler *deviceHandler
	if handler = oo.getDeviceHandler(ctx, device.Id, false); handler == nil {
		handler := newDeviceHandler(ctx, oo.coreProxy, oo.adapterProxy, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		handler.device = device
		handler.startReconciling(ctx)
		go handler.adoptOrReconcileDevice(ctx, handler.device)
		// reconcilement will be continued after onu-device entry is added
	} else {
		return fmt.Errorf(fmt.Sprintf("device-already-reconciled-or-active-%s", device.Id))
	}
	return nil
}

//Abandon_device unimplemented
func (oo *OpenONUAC) Abandon_device(ctx context.Context, device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Disable_device disables the given device
func (oo *OpenONUAC) Disable_device(ctx context.Context, device *voltha.Device) error {
	logger.Infow(ctx, "disable-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.disableDevice(ctx, device)
		return nil
	}
	logger.Warnw(ctx, "no handler found for device-disable", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reenable_device enables the onu device after disable
func (oo *OpenONUAC) Reenable_device(ctx context.Context, device *voltha.Device) error {
	logger.Infow(ctx, "reenable-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.reEnableDevice(ctx, device)
		return nil
	}
	logger.Warnw(ctx, "no handler found for device-reenable", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Reboot_device reboots the given device
func (oo *OpenONUAC) Reboot_device(ctx context.Context, device *voltha.Device) error {
	logger.Infow(ctx, "reboot-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.rebootDevice(ctx, true, device) //reboot request with device checking
		return nil
	}
	logger.Warnw(ctx, "no handler found for device-reboot", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-#{device.Id}"))
}

//Self_test_device unimplemented
func (oo *OpenONUAC) Self_test_device(ctx context.Context, device *voltha.Device) error {
	return errors.New("unImplemented")
}

// Delete_device deletes the given device
func (oo *OpenONUAC) Delete_device(ctx context.Context, device *voltha.Device) error {
	logger.Infow(ctx, "delete-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		var errorsList []error
		if err := handler.deleteDevicePersistencyData(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
		handler.stopCollector <- true // stop the metric collector routine
		if handler.pOnuMetricsMgr != nil {
			if err := handler.pOnuMetricsMgr.clearAllPmData(ctx); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		//don't leave any garbage - even in error case
		oo.deleteDeviceHandlerToMap(handler)
		if len(errorsList) > 0 {
			logger.Errorw(ctx, "one-or-more-error-during-device-delete", log.Fields{"device-id": device.Id})
			return fmt.Errorf("one-or-more-error-during-device-delete, errors:%v", errorsList)
		}
		return nil
	}
	logger.Warnw(ctx, "no handler found for device-deletion", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Get_device_details unimplemented
func (oo *OpenONUAC) Get_device_details(ctx context.Context, device *voltha.Device) error {
	return errors.New("unImplemented")
}

//Update_flows_bulk returns
func (oo *OpenONUAC) Update_flows_bulk(ctx context.Context, device *voltha.Device, flows *voltha.Flows, groups *voltha.FlowGroups, flowMetadata *voltha.FlowMetadata) error {
	return errors.New("unImplemented")
}

//Update_flows_incrementally updates (add/remove) the flows on a given device
func (oo *OpenONUAC) Update_flows_incrementally(ctx context.Context, device *voltha.Device,
	flows *openflow_13.FlowChanges, groups *openflow_13.FlowGroupChanges, flowMetadata *voltha.FlowMetadata) error {

	logger.Infow(ctx, "update-flows-incrementally", log.Fields{"device-id": device.Id})
	//flow config is relayed to handler even if the device might be in some 'inactive' state
	// let the handler or related FSM's decide, what to do with the modified flow state info
	// at least the flow-remove must be done in respect to internal data, while OMCI activity might not be needed here

	// For now, there is no support for group changes (as in the actual Py-adapter code)
	//   but processing is continued for flowUpdate possibly also set in the request
	if groups.ToAdd != nil && groups.ToAdd.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group add not supported (ignored)", log.Fields{"device-id": device.Id})
	}
	if groups.ToRemove != nil && groups.ToRemove.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group remove not supported (ignored)", log.Fields{"device-id": device.Id})
	}
	if groups.ToUpdate != nil && groups.ToUpdate.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group update not supported (ignored)", log.Fields{"device-id": device.Id})
	}

	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		err := handler.FlowUpdateIncremental(ctx, flows, groups, flowMetadata)
		return err
	}
	logger.Warnw(ctx, "no handler found for incremental flow update", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Update_pm_config returns PmConfigs nil or error
func (oo *OpenONUAC) Update_pm_config(ctx context.Context, device *voltha.Device, pmConfigs *voltha.PmConfigs) error {
	logger.Infow(ctx, "update-pm-config", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		return handler.updatePmConfig(ctx, pmConfigs)
	}
	logger.Warnw(ctx, "no handler found for update-pm-config", log.Fields{"device-id": device.Id})
	return fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//Receive_packet_out sends packet out to the device
func (oo *OpenONUAC) Receive_packet_out(ctx context.Context, deviceID string, egressPortNo int, packet *openflow_13.OfpPacketOut) error {
	return errors.New("unImplemented")
}

//Suppress_event unimplemented
func (oo *OpenONUAC) Suppress_event(ctx context.Context, filter *voltha.EventFilter) error {
	return errors.New("unImplemented")
}

//Unsuppress_event  unimplemented
func (oo *OpenONUAC) Unsuppress_event(ctx context.Context, filter *voltha.EventFilter) error {
	return errors.New("unImplemented")
}

//Download_image requests downloading some image according to indications as given in request
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) Download_image(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	if request != nil && (*request).Name != "" {
		if !oo.pDownloadManager.imageExists(ctx, request) {
			logger.Debugw(ctx, "start image download", log.Fields{"image-description": request})
			// Download_image is not supposed to be blocking, anyway let's call the DownloadManager still synchronously to detect 'fast' problems
			// the download itself is later done in background
			err := oo.pDownloadManager.startDownload(ctx, request)
			return request, err
		}
		// image already exists
		logger.Debugw(ctx, "image already downloaded", log.Fields{"image-description": request})
		return request, nil
	}
	return request, errors.New("invalid image definition")
}

//Get_image_download_status unimplemented
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) Get_image_download_status(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Cancel_image_download unimplemented
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) Cancel_image_download(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//Activate_image_update requests downloading some Onu Software image to the INU via OMCI
//  according to indications as given in request and on success activate the image on the ONU
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) Activate_image_update(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	if request != nil && (*request).Name != "" {
		if oo.pDownloadManager.imageLocallyDownloaded(ctx, request) {
			if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
				logger.Debugw(ctx, "image download on omci requested", log.Fields{
					"image-description": request, "device-id": device.Id})
				err := handler.doOnuSwUpgrade(ctx, request, oo.pDownloadManager)
				return request, err
			}
			logger.Warnw(ctx, "no handler found for image activation", log.Fields{"device-id": device.Id})
			return request, fmt.Errorf(fmt.Sprintf("handler-not-found - device-id: %s", device.Id))
		}
		logger.Debugw(ctx, "image not yet downloaded on activate request", log.Fields{"image-description": request})
		return request, fmt.Errorf(fmt.Sprintf("image-not-yet-downloaded - device-id: %s", device.Id))
	}
	return request, errors.New("invalid image definition")
}

//Revert_image_update unimplemented
func (oo *OpenONUAC) Revert_image_update(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

// Enable_port to Enable PON/NNI interface - seems not to be used/required according to python code
func (oo *OpenONUAC) Enable_port(ctx context.Context, deviceID string, port *voltha.Port) error {
	return errors.New("unImplemented")
}

// Disable_port to Disable pon/nni interface  - seems not to be used/required according to python code
func (oo *OpenONUAC) Disable_port(ctx context.Context, deviceID string, port *voltha.Port) error {
	return errors.New("unImplemented")
}

//Child_device_lost - unimplemented
//needed for if update >= 3.1.x
func (oo *OpenONUAC) Child_device_lost(ctx context.Context, deviceID string, pPortNo uint32, onuID uint32) error {
	return errors.New("unImplemented")
}

// Start_omci_test unimplemented
func (oo *OpenONUAC) Start_omci_test(ctx context.Context, device *voltha.Device, request *voltha.OmciTestRequest) (*voltha.TestResponse, error) {
	return nil, errors.New("unImplemented")
}

// Get_ext_value - unimplemented
func (oo *OpenONUAC) Get_ext_value(ctx context.Context, deviceID string, device *voltha.Device, valueparam voltha.ValueType_Type) (*voltha.ReturnValues, error) {
	return nil, errors.New("unImplemented")
}

//Single_get_value_request handles the core request to retrieve uni status
func (oo *OpenONUAC) Single_get_value_request(ctx context.Context, request extension.SingleGetValueRequest) (*extension.SingleGetValueResponse, error) {
	logger.Infow(ctx, "Single_get_value_request", log.Fields{"request": request})

	if handler := oo.getDeviceHandler(ctx, request.TargetId, false); handler != nil {
		switch reqType := request.GetRequest().GetRequest().(type) {
		case *extension.GetValueRequest_UniInfo:
			return handler.getUniPortStatus(ctx, reqType.UniInfo), nil
		default:
			return postUniStatusErrResponse(extension.GetValueResponse_UNSUPPORTED), nil

		}
	}
	logger.Errorw(ctx, "Single_get_value_request failed ", log.Fields{"request": request})
	return postUniStatusErrResponse(extension.GetValueResponse_INVALID_DEVICE_ID), nil
}

// Adapter interface required methods ################ end #########
// #################################################################
