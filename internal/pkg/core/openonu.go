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

//Package core provides the utility for onu devices, flows and statistics
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	conf "github.com/opencord/voltha-lib-go/v5/pkg/config"

	"github.com/golang/protobuf/ptypes"
	"github.com/opencord/voltha-lib-go/v5/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v5/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v5/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v5/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v5/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/extension"
	ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	"github.com/opencord/voltha-protos/v4/go/openflow_13"
	oop "github.com/opencord/voltha-protos/v4/go/openolt"
	"github.com/opencord/voltha-protos/v4/go/voltha"

	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/config"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/swupg"
	uniprt "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/uniprt"
)

//OpenONUAC structure holds the ONU core information
type OpenONUAC struct {
	deviceHandlers              map[string]*deviceHandler
	deviceHandlersCreateChan    map[string]chan bool //channels for deviceHandler create events
	mutexDeviceHandlersMap      sync.RWMutex
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
	mutexMibTemplateGenerated   sync.RWMutex
	exitChannel                 chan int
	HeartbeatCheckInterval      time.Duration
	HeartbeatFailReportInterval time.Duration
	AcceptIncrementalEvto       bool
	//GrpcTimeoutInterval         time.Duration
	pSupportedFsms             *cmn.OmciDeviceFsms
	maxTimeoutInterAdapterComm time.Duration
	maxTimeoutReconciling      time.Duration
	pDownloadManager           *swupg.AdapterDownloadManager
	pFileManager               *swupg.FileDownloadManager //let coexist 'old and new' DownloadManager as long as 'old' does not get obsolete
	MetricsEnabled             bool
	mibAuditInterval           time.Duration
	omciTimeout                int // in seconds
	alarmAuditInterval         time.Duration
	dlToOnuTimeout4M           time.Duration
}

//NewOpenONUAC returns a new instance of OpenONU_AC
func NewOpenONUAC(ctx context.Context, kafkaICProxy kafka.InterContainerProxy,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy,
	eventProxy eventif.EventProxy, kvClient kvstore.Client, cfg *config.AdapterFlags, cm *conf.ConfigManager) *OpenONUAC {
	var openOnuAc OpenONUAC
	openOnuAc.exitChannel = make(chan int, 1)
	openOnuAc.deviceHandlers = make(map[string]*deviceHandler)
	openOnuAc.deviceHandlersCreateChan = make(map[string]chan bool)
	openOnuAc.mutexDeviceHandlersMap = sync.RWMutex{}
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
	openOnuAc.mutexMibTemplateGenerated = sync.RWMutex{}
	openOnuAc.HeartbeatCheckInterval = cfg.HeartbeatCheckInterval
	openOnuAc.HeartbeatFailReportInterval = cfg.HeartbeatFailReportInterval
	openOnuAc.AcceptIncrementalEvto = cfg.AccIncrEvto
	openOnuAc.maxTimeoutInterAdapterComm = cfg.MaxTimeoutInterAdapterComm
	openOnuAc.maxTimeoutReconciling = cfg.MaxTimeoutReconciling
	//openOnuAc.GrpcTimeoutInterval = cfg.GrpcTimeoutInterval
	openOnuAc.MetricsEnabled = cfg.MetricsEnabled
	openOnuAc.mibAuditInterval = cfg.MibAuditInterval
	// since consumers of OMCI timeout value everywhere in code is in "int seconds", do this useful conversion
	openOnuAc.omciTimeout = int(cfg.OmciTimeout.Seconds())
	openOnuAc.alarmAuditInterval = cfg.AlarmAuditInterval
	openOnuAc.dlToOnuTimeout4M = cfg.DownloadToOnuTimeout4MB

	openOnuAc.pSupportedFsms = &cmn.OmciDeviceFsms{
		"mib-synchronizer": {
			//mibSyncFsm,        // Implements the MIB synchronization state machine
			DatabaseClass: mibDbVolatileDictImpl, // Implements volatile ME MIB database
			//true,                  // Advertise events on OpenOMCI event bus
			AuditInterval: openOnuAc.mibAuditInterval, // Time to wait between MIB audits.  0 to disable audits.
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

	openOnuAc.pDownloadManager = swupg.NewAdapterDownloadManager(ctx)
	openOnuAc.pFileManager = swupg.NewFileDownloadManager(ctx)
	openOnuAc.pFileManager.SetDownloadTimeout(ctx, cfg.DownloadToAdapterTimeout)

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
	oo.mutexDeviceHandlersMap.Lock()
	defer oo.mutexDeviceHandlersMap.Unlock()
	if _, exist := oo.deviceHandlers[agent.DeviceID]; !exist {
		oo.deviceHandlers[agent.DeviceID] = agent
		oo.deviceHandlers[agent.DeviceID].start(ctx)
		if _, exist := oo.deviceHandlersCreateChan[agent.DeviceID]; exist {
			logger.Debugw(ctx, "deviceHandler created - trigger processing of pending ONU_IND_REQUEST", log.Fields{"device-id": agent.DeviceID})
			oo.deviceHandlersCreateChan[agent.DeviceID] <- true
		}
	}
}

func (oo *OpenONUAC) deleteDeviceHandlerToMap(agent *deviceHandler) {
	oo.mutexDeviceHandlersMap.Lock()
	defer oo.mutexDeviceHandlersMap.Unlock()
	delete(oo.deviceHandlers, agent.DeviceID)
	delete(oo.deviceHandlersCreateChan, agent.DeviceID)
}

//getDeviceHandler gets the ONU deviceHandler and may wait until it is created
func (oo *OpenONUAC) getDeviceHandler(ctx context.Context, deviceID string, aWait bool) *deviceHandler {
	oo.mutexDeviceHandlersMap.Lock()
	agent, ok := oo.deviceHandlers[deviceID]
	if aWait && !ok {
		logger.Infow(ctx, "Race condition: deviceHandler not present - wait for creation or timeout",
			log.Fields{"device-id": deviceID})
		if _, exist := oo.deviceHandlersCreateChan[deviceID]; !exist {
			oo.deviceHandlersCreateChan[deviceID] = make(chan bool, 1)
		}
		deviceCreateChan := oo.deviceHandlersCreateChan[deviceID]
		//keep the read sema short to allow for subsequent write
		oo.mutexDeviceHandlersMap.Unlock()
		// based on concurrent processing the deviceHandler creation may not yet be finished at his point
		// so it might be needed to wait here for that event with some timeout
		select {
		case <-time.After(1 * time.Second): //timer may be discussed ...
			logger.Warnw(ctx, "No valid deviceHandler created after max WaitTime", log.Fields{"device-id": deviceID})
			return nil
		case <-deviceCreateChan:
			logger.Debugw(ctx, "deviceHandler is ready now - continue", log.Fields{"device-id": deviceID})
			oo.mutexDeviceHandlersMap.RLock()
			defer oo.mutexDeviceHandlersMap.RUnlock()
			return oo.deviceHandlers[deviceID]
		}
	}
	oo.mutexDeviceHandlersMap.Unlock()
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

//Process_tech_profile_instance_request not implemented
func (oo *OpenONUAC) Process_tech_profile_instance_request(ctx context.Context, msg *ic.InterAdapterTechProfileInstanceRequestMessage) *ic.InterAdapterTechProfileDownloadMessage {
	logger.Error(ctx, "unImplemented")
	return nil
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
		if err := handler.coreProxy.DeviceStateUpdate(ctx, device.Id, device.ConnectStatus, voltha.OperStatus_RECONCILING); err != nil {
			return fmt.Errorf("not able to update device state to reconciling. Err : %s", err.Error())
		}
		handler.StartReconciling(ctx, false)
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
	return fmt.Errorf("handler-not-found-for-device: %s", device.Id)
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

		handler.mutexDeletionInProgressFlag.Lock()
		handler.deletionInProgress = true
		handler.mutexDeletionInProgressFlag.Unlock()

		if err := handler.deleteDevicePersistencyData(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
		select {
		case handler.stopCollector <- true: // stop the metric collector routine
			logger.Debugw(ctx, "sent stop signal to metric collector routine", log.Fields{"device-id": device.Id})
		default:
			logger.Warnw(ctx, "metric collector routine not waiting on stop signal", log.Fields{"device-id": device.Id})
		}
		select {
		case handler.stopAlarmManager <- true: //stop the alarm manager.
			logger.Debugw(ctx, "sent stop signal to alarm manager", log.Fields{"device-id": device.Id})
		default:
			logger.Warnw(ctx, "alarm manager not waiting on stop signal", log.Fields{"device-id": device.Id})
		}
		if handler.pOnuMetricsMgr != nil {
			if err := handler.pOnuMetricsMgr.ClearAllPmData(ctx); err != nil {
				errorsList = append(errorsList, err)
			}
		}
		select {
		case handler.pSelfTestHdlr.StopSelfTestModule <- true:
			logger.Debugw(ctx, "sent stop signal to self test handler module", log.Fields{"device-id": device.Id})
		default:
			logger.Warnw(ctx, "self test handler module not waiting on stop signal", log.Fields{"device-id": device.Id})
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
		if !oo.pDownloadManager.ImageExists(ctx, request) {
			logger.Debugw(ctx, "start image download", log.Fields{"image-description": request})
			// Download_image is not supposed to be blocking, anyway let's call the DownloadManager still synchronously to detect 'fast' problems
			// the download itself is later done in background
			err := oo.pDownloadManager.StartDownload(ctx, request)
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

//Activate_image_update requests downloading some Onu Software image to the ONU via OMCI
//  according to indications as given in request and on success activate the image on the ONU
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) Activate_image_update(ctx context.Context, device *voltha.Device, request *voltha.ImageDownload) (*voltha.ImageDownload, error) {
	if request != nil && (*request).Name != "" {
		if oo.pDownloadManager.ImageLocallyDownloaded(ctx, request) {
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
func (oo *OpenONUAC) Child_device_lost(ctx context.Context, device *voltha.Device) error {
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
			return handler.GetUniPortStatus(ctx, reqType.UniInfo), nil
		case *extension.GetValueRequest_OnuOpticalInfo:
			CommChan := make(chan cmn.Message)
			respChan := make(chan extension.SingleGetValueResponse)
			// Initiate the self test request
			if err := handler.pSelfTestHdlr.SelfTestRequestStart(ctx, request, CommChan, respChan); err != nil {
				return &extension.SingleGetValueResponse{
					Response: &extension.GetValueResponse{
						Status:    extension.GetValueResponse_ERROR,
						ErrReason: extension.GetValueResponse_INTERNAL_ERROR,
					},
				}, err
			}
			// The timeout handling is already implemented in omci_self_test_handler module
			resp := <-respChan
			return &resp, nil
		case *extension.GetValueRequest_OnuInfo:
			return handler.getOnuOMCICounters(ctx, reqType.OnuInfo), nil
		default:
			return uniprt.PostUniStatusErrResponse(extension.GetValueResponse_UNSUPPORTED), nil

		}
	}
	logger.Errorw(ctx, "Single_get_value_request failed ", log.Fields{"request": request})
	return uniprt.PostUniStatusErrResponse(extension.GetValueResponse_INVALID_DEVICE_ID), nil
}

//if update >= 4.3.0
// Note: already with the implementation of the 'old' download interface problems were detected when the argument name used here is not the same
//   as defined in the adapter interface file. That sounds strange and the effects were strange as well.
//   The reason for that was never finally investigated.
//   To be on the safe side argument names are left here always as defined in iAdapter.go .

// Download_onu_image downloads (and optionally activates and commits) the indicated ONU image to the requested ONU(s)
//   if the image is not yet present on the adapter it has to be automatically downloaded
func (oo *OpenONUAC) Download_onu_image(ctx context.Context, request *voltha.DeviceImageDownloadRequest) (*voltha.DeviceImageResponse, error) {
	if request != nil && len((*request).DeviceId) > 0 && (*request).Image.Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*request).Image.Version
		downloadedToAdapter := false
		firstDevice := true
		var vendorID string
		for _, pCommonID := range (*request).DeviceId {
			vendorIDMatch := true
			loDeviceID := (*pCommonID).Id
			loDeviceImageState := voltha.DeviceImageState{}
			loDeviceImageState.DeviceId = loDeviceID
			loImageState := voltha.ImageState{}
			loDeviceImageState.ImageState = &loImageState
			loDeviceImageState.ImageState.Version = (*request).Image.Version

			onuVolthaDevice, err := oo.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx),
				loDeviceID, loDeviceID)
			if err != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch Onu device for image download",
					log.Fields{"device-id": loDeviceID, "err": err})
				loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
				loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR //proto restriction, better option: 'INVALID_DEVICE'
				loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
			} else {
				if firstDevice {
					//start/verify download of the image to the adapter based on first found device only
					//  use the OnuVendor identification from first given device
					firstDevice = false
					vendorID = onuVolthaDevice.VendorId
					imageIdentifier = vendorID + imageIdentifier //head on vendor ID of the ONU
					logger.Debugw(ctx, "download request for file", log.Fields{"image-id": imageIdentifier})

					if !oo.pFileManager.ImageExists(ctx, imageIdentifier) {
						logger.Debugw(ctx, "start image download", log.Fields{"image-description": request})
						// Download_image is not supposed to be blocking, anyway let's call the DownloadManager still synchronously to detect 'fast' problems
						// the download itself is later done in background
						if err := oo.pFileManager.StartDownload(ctx, imageIdentifier, (*request).Image.Url); err == nil {
							downloadedToAdapter = true
						}
						//else: treat any error here as 'INVALID_URL' (even though it might as well be some issue on local FS, eg. 'INSUFFICIENT_SPACE')
						// otherwise a more sophisticated error evaluation is needed
					} else {
						// image already exists
						downloadedToAdapter = true
						logger.Debugw(ctx, "image already downloaded", log.Fields{"image-description": imageIdentifier})
						// note: If the image (with vendorId+name) has already been downloaded before from some other
						//   valid URL, the current URL is just ignored. If the operators want to ensure that the new URL
						//   is really used, then they first have to use the 'abort' API to remove the existing image!
						//   (abort API can be used also after some successful download to just remove the image from adapter)
					}
				} else {
					//for all following devices verify the matching vendorID
					if onuVolthaDevice.VendorId != vendorID {
						logger.Warnw(ctx, "onu vendor id does not match image vendor id, device ignored",
							log.Fields{"onu-vendor-id": onuVolthaDevice.VendorId, "image-vendor-id": vendorID})
						vendorIDMatch = false
					}
				}
				if downloadedToAdapter && vendorIDMatch {
					// start the ONU download activity for each possible device
					// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
					if handler := oo.getDeviceHandler(ctx, loDeviceID, false); handler != nil {
						logger.Debugw(ctx, "image download on omci requested", log.Fields{
							"image-id": imageIdentifier, "device-id": loDeviceID})
						//onu upgrade handling called in background without immediate error evaluation here
						//  as the processing can be done for multiple ONU's and an error on one ONU should not stop processing for others
						//  state/progress/success of the request has to be verified using the Get_onu_image_status() API
						go handler.onuSwUpgradeAfterDownload(ctx, request, oo.pFileManager, imageIdentifier)
						loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_STARTED
						loDeviceImageState.ImageState.Reason = voltha.ImageState_NO_ERROR
						loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
					} else {
						//cannot start ONU download for requested device
						logger.Warnw(ctx, "no handler found for image activation", log.Fields{"device-id": loDeviceID})
						loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
						loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR //proto restriction, better option: 'INVALID_DEVICE'
						loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
					}
				} else {
					loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
					if !downloadedToAdapter {
						loDeviceImageState.ImageState.Reason = voltha.ImageState_INVALID_URL
					} else { //only logical option is !vendorIDMatch
						loDeviceImageState.ImageState.Reason = voltha.ImageState_VENDOR_DEVICE_MISMATCH
					}
					loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
				}
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, &loDeviceImageState)
		}
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image download parameters")
}

// Get_onu_image_status delivers the adapter-related information about the download/activation/commitment
//   status for the requested image
func (oo *OpenONUAC) Get_onu_image_status(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		var vendorIDSet bool
		firstDevice := true
		var vendorID string
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			pDeviceImageState := &voltha.DeviceImageState{
				DeviceId: loDeviceID,
			}
			vendorIDSet = false
			onuVolthaDevice, err := oo.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx),
				loDeviceID, loDeviceID)
			if err != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch Onu device to get image status",
					log.Fields{"device-id": loDeviceID, "err": err})
				pImageState := &voltha.ImageState{
					Version:       (*in).Version,
					DownloadState: voltha.ImageState_DOWNLOAD_UNKNOWN, //no statement about last activity possible
					Reason:        voltha.ImageState_UNKNOWN_ERROR,    //something like "DEVICE_NOT_EXISTS" would be better (proto def)
					ImageState:    voltha.ImageState_IMAGE_UNKNOWN,
				}
				pDeviceImageState.ImageState = pImageState
			} else {
				if firstDevice {
					//start/verify download of the image to the adapter based on first found device only
					//  use the OnuVendor identification from first given device
					firstDevice = false
					vendorID = onuVolthaDevice.VendorId
					imageIdentifier = vendorID + imageIdentifier //head on vendor ID of the ONU
					vendorIDSet = true
					logger.Debugw(ctx, "status request for image", log.Fields{"image-id": imageIdentifier})
				} else {
					//for all following devices verify the matching vendorID
					if onuVolthaDevice.VendorId != vendorID {
						logger.Warnw(ctx, "onu vendor id does not match image vendor id, device ignored",
							log.Fields{"onu-vendor-id": onuVolthaDevice.VendorId, "image-vendor-id": vendorID})
					} else {
						vendorIDSet = true
					}
				}
				if !vendorIDSet {
					pImageState := &voltha.ImageState{
						Version:       (*in).Version,
						DownloadState: voltha.ImageState_DOWNLOAD_UNKNOWN, //can't be sure that download for this device was really tried
						Reason:        voltha.ImageState_VENDOR_DEVICE_MISMATCH,
						ImageState:    voltha.ImageState_IMAGE_UNKNOWN,
					}
					pDeviceImageState.ImageState = pImageState
				} else {
					// get the handler for the device
					if handler := oo.getDeviceHandler(ctx, loDeviceID, false); handler != nil {
						logger.Debugw(ctx, "image status request for", log.Fields{
							"image-id": imageIdentifier, "device-id": loDeviceID})
						//status request is called synchronously to collect the indications for all concerned devices
						pDeviceImageState.ImageState = handler.requestOnuSwUpgradeState(ctx, imageIdentifier, (*in).Version)
					} else {
						//cannot get the handler
						logger.Warnw(ctx, "no handler found for image status request ", log.Fields{"device-id": loDeviceID})
						pImageState := &voltha.ImageState{
							Version:       (*in).Version,
							DownloadState: voltha.ImageState_DOWNLOAD_UNKNOWN, //no statement about last activity possible
							Reason:        voltha.ImageState_UNKNOWN_ERROR,    //something like "DEVICE_NOT_EXISTS" would be better (proto def)
							ImageState:    voltha.ImageState_IMAGE_UNKNOWN,
						}
						pDeviceImageState.ImageState = pImageState
					}
				}
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, pDeviceImageState)
		}
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image status request parameters")
}

// Abort_onu_image_upgrade stops the actual download/activation/commitment process (on next possibly step)
func (oo *OpenONUAC) Abort_onu_image_upgrade(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		firstDevice := true
		var vendorID string
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			onuVolthaDevice, err := oo.coreProxy.GetDevice(log.WithSpanFromContext(context.TODO(), ctx),
				loDeviceID, loDeviceID)
			if err != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch Onu device to abort its download",
					log.Fields{"device-id": loDeviceID, "err": err})
				continue //try the work with next deviceId
			}
			pDeviceImageState := &voltha.DeviceImageState{}
			loImageState := voltha.ImageState{}
			pDeviceImageState.ImageState = &loImageState

			if firstDevice {
				//start/verify download of the image to the adapter based on first found device only
				//  use the OnuVendor identification from first given device
				firstDevice = false
				vendorID = onuVolthaDevice.VendorId
				imageIdentifier = vendorID + imageIdentifier //head on vendor ID of the ONU
				logger.Debugw(ctx, "abort request for file", log.Fields{"image-id": imageIdentifier})
			} else {
				//for all following devices verify the matching vendorID
				if onuVolthaDevice.VendorId != vendorID {
					logger.Warnw(ctx, "onu vendor id does not match image vendor id, device ignored",
						log.Fields{"onu-vendor-id": onuVolthaDevice.VendorId, "image-vendor-id": vendorID})
					continue //try the work with next deviceId
				}
			}

			// cancel the ONU upgrade activity for each possible device
			if handler := oo.getDeviceHandler(ctx, loDeviceID, false); handler != nil {
				logger.Debugw(ctx, "image upgrade abort requested", log.Fields{
					"image-id": imageIdentifier, "device-id": loDeviceID})
				//upgrade cancel is called synchronously to collect the imageResponse indications for all concerned devices
				handler.cancelOnuSwUpgrade(ctx, imageIdentifier, (*in).Version, pDeviceImageState)
			} else {
				//cannot start aborting ONU download for requested device
				logger.Warnw(ctx, "no handler found for aborting upgrade ", log.Fields{"device-id": loDeviceID})
				pDeviceImageState.DeviceId = loDeviceID
				pDeviceImageState.ImageState.Version = (*in).Version
				pDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_CANCELLED
				pDeviceImageState.ImageState.Reason = voltha.ImageState_CANCELLED_ON_REQUEST //something better would be possible with protos modification
				pDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, pDeviceImageState)
		}
		if !firstDevice {
			//if at least one valid device was found cancel also a possibly running download to adapter and remove the image
			//  this is to be done after the upgradeOnu cancel activities in order to not subduct the file for still running processes
			oo.pFileManager.CancelDownload(ctx, imageIdentifier)
		}
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image upgrade abort parameters")
}

// Get_onu_images retrieves the ONU SW image status information via OMCI
func (oo *OpenONUAC) Get_onu_images(ctx context.Context, deviceID string) (*voltha.OnuImages, error) {
	logger.Infow(ctx, "Get_onu_images", log.Fields{"device-id": deviceID})
	if handler := oo.getDeviceHandler(ctx, deviceID, false); handler != nil {
		images, err := handler.getOnuImages(ctx)
		if err == nil {
			return images, nil
		}
		return nil, fmt.Errorf(fmt.Sprintf("%s-%s", err, deviceID))
	}
	logger.Warnw(ctx, "no handler found for Get_onu_images", log.Fields{"device-id": deviceID})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", deviceID))
}

// Activate_onu_image initiates the activation of the image for the requested ONU(s)
//  precondition: image downloaded and not yet activated or image refers to current inactive image
func (oo *OpenONUAC) Activate_onu_image(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		//let the deviceHandler find the adequate way of requesting the image activation
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			loDeviceImageState := voltha.DeviceImageState{}
			loDeviceImageState.DeviceId = loDeviceID
			loImageState := voltha.ImageState{}
			loDeviceImageState.ImageState = &loImageState
			loDeviceImageState.ImageState.Version = imageIdentifier
			//compared to download procedure the vendorID (from device) is secondary here
			//   and only needed in case the upgrade process is based on some ongoing download process (and can be retrieved in deviceHandler if needed)
			// start image activation activity for each possible device
			// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
			if handler := oo.getDeviceHandler(ctx, loDeviceID, false); handler != nil {
				logger.Debugw(ctx, "onu image activation requested", log.Fields{
					"image-id": imageIdentifier, "device-id": loDeviceID})
				//onu activation handling called in background without immediate error evaluation here
				//  as the processing can be done for multiple ONU's and an error on one ONU should not stop processing for others
				//  state/progress/success of the request has to be verified using the Get_onu_image_status() API
				if pImageStates, err := handler.onuSwActivateRequest(ctx, imageIdentifier, (*in).CommitOnSuccess); err != nil {
					loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
					loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR
					loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_ACTIVATION_ABORTED
				} else {
					loDeviceImageState.ImageState.DownloadState = pImageStates.DownloadState
					loDeviceImageState.ImageState.Reason = pImageStates.Reason
					loDeviceImageState.ImageState.ImageState = pImageStates.ImageState
				}
			} else {
				//cannot start SW activation for requested device
				logger.Warnw(ctx, "no handler found for image activation", log.Fields{"device-id": loDeviceID})
				loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
				loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR
				loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_ACTIVATION_ABORTED
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, &loDeviceImageState)
		}
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image activation parameters")
}

// Commit_onu_image enforces the commitment of the image for the requested ONU(s)
//  precondition: image activated and not yet committed
func (oo *OpenONUAC) Commit_onu_image(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		//let the deviceHandler find the adequate way of requesting the image activation
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			loDeviceImageState := voltha.DeviceImageState{}
			loDeviceImageState.DeviceId = loDeviceID
			loImageState := voltha.ImageState{}
			loDeviceImageState.ImageState = &loImageState
			loDeviceImageState.ImageState.Version = imageIdentifier
			//compared to download procedure the vendorID (from device) is secondary here
			//   and only needed in case the upgrade process is based on some ongoing download process (and can be retrieved in deviceHandler if needed)
			// start image activation activity for each possible device
			// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
			if handler := oo.getDeviceHandler(ctx, loDeviceID, false); handler != nil {
				logger.Debugw(ctx, "onu image commitment requested", log.Fields{
					"image-id": imageIdentifier, "device-id": loDeviceID})
				//onu commitment handling called in background without immediate error evaluation here
				//  as the processing can be done for multiple ONU's and an error on one ONU should not stop processing for others
				//  state/progress/success of the request has to be verified using the Get_onu_image_status() API
				if pImageStates, err := handler.onuSwCommitRequest(ctx, imageIdentifier); err != nil {
					loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
					loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR //can be multiple reasons here
					loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_COMMIT_ABORTED
				} else {
					loDeviceImageState.ImageState.DownloadState = pImageStates.DownloadState
					loDeviceImageState.ImageState.Reason = pImageStates.Reason
					loDeviceImageState.ImageState.ImageState = pImageStates.ImageState
				}
			} else {
				//cannot start SW commitment for requested device
				logger.Warnw(ctx, "no handler found for image commitment", log.Fields{"device-id": loDeviceID})
				loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
				loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR
				loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_COMMIT_ABORTED
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, &loDeviceImageState)
		}
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image commitment parameters")
}

// Adapter interface required methods ################ end #########
// #################################################################

// GetSupportedFsms - TODO: add comment
func (oo *OpenONUAC) GetSupportedFsms() *cmn.OmciDeviceFsms {
	return oo.pSupportedFsms
}

// LockMutexMibTemplateGenerated - TODO: add comment
func (oo *OpenONUAC) LockMutexMibTemplateGenerated() {
	oo.mutexMibTemplateGenerated.Lock()
}

// UnlockMutexMibTemplateGenerated - TODO: add comment
func (oo *OpenONUAC) UnlockMutexMibTemplateGenerated() {
	oo.mutexMibTemplateGenerated.Unlock()
}

// GetMibTemplatesGenerated - TODO: add comment
func (oo *OpenONUAC) GetMibTemplatesGenerated(mibTemplatePath string) (value bool, exist bool) {
	value, exist = oo.mibTemplatesGenerated[mibTemplatePath]
	return value, exist
}

// SetMibTemplatesGenerated - TODO: add comment
func (oo *OpenONUAC) SetMibTemplatesGenerated(mibTemplatePath string, value bool) {
	oo.mibTemplatesGenerated[mibTemplatePath] = value
}

// RLockMutexDeviceHandlersMap - TODO: add comment
func (oo *OpenONUAC) RLockMutexDeviceHandlersMap() {
	oo.mutexDeviceHandlersMap.RLock()
}

// RUnlockMutexDeviceHandlersMap - TODO: add comment
func (oo *OpenONUAC) RUnlockMutexDeviceHandlersMap() {
	oo.mutexDeviceHandlersMap.RUnlock()
}

// GetDeviceHandler - TODO: add comment
func (oo *OpenONUAC) GetDeviceHandler(deviceID string) (value cmn.IdeviceHandler, exist bool) {
	value, exist = oo.deviceHandlers[deviceID]
	return value, exist
}
