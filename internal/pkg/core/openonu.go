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
	"hash/fnv"
	"sync"
	"time"

	"github.com/opencord/voltha-lib-go/v7/pkg/db"
	vgrpc "github.com/opencord/voltha-lib-go/v7/pkg/grpc"

	conf "github.com/opencord/voltha-lib-go/v7/pkg/config"
	"github.com/opencord/voltha-protos/v5/go/adapter_service"
	"github.com/opencord/voltha-protos/v5/go/common"
	"github.com/opencord/voltha-protos/v5/go/health"
	"github.com/opencord/voltha-protos/v5/go/olt_inter_adapter_service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/opencord/voltha-lib-go/v7/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v7/pkg/events/eventif"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	ca "github.com/opencord/voltha-protos/v5/go/core_adapter"
	"github.com/opencord/voltha-protos/v5/go/extension"
	ia "github.com/opencord/voltha-protos/v5/go/inter_adapter"
	"github.com/opencord/voltha-protos/v5/go/omci"
	"github.com/opencord/voltha-protos/v5/go/voltha"

	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/config"
	pmmgr "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/pmmgr"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/swupg"
	uniprt "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/uniprt"
)

var onuKvStorePathPrefixes = []string{cmn.CBasePathOnuKVStore, pmmgr.CPmKvStorePrefixBase}

type reachabilityFromRemote struct {
	lastKeepAlive     time.Time
	keepAliveInterval int64
}

//OpenONUAC structure holds the ONU core information
type OpenONUAC struct {
	deviceHandlers              map[string]*deviceHandler
	deviceHandlersCreateChan    map[string]chan bool //channels for deviceHandler create events
	mutexDeviceHandlersMap      sync.RWMutex
	coreClient                  *vgrpc.Client
	parentAdapterClients        map[string]*vgrpc.Client
	lockParentAdapterClients    sync.RWMutex
	reachableFromRemote         map[string]*reachabilityFromRemote
	lockReachableFromRemote     sync.RWMutex
	eventProxy                  eventif.EventProxy
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
	pSupportedFsms              *cmn.OmciDeviceFsms
	maxTimeoutInterAdapterComm  time.Duration
	maxTimeoutReconciling       time.Duration
	pDownloadManager            *swupg.AdapterDownloadManager
	pFileManager                *swupg.FileDownloadManager //let coexist 'old and new' DownloadManager as long as 'old' does not get obsolete
	MetricsEnabled              bool
	mibAuditInterval            time.Duration
	omciTimeout                 int // in seconds
	alarmAuditInterval          time.Duration
	dlToOnuTimeout4M            time.Duration
	rpcTimeout                  time.Duration
	maxConcurrentFlowsPerUni    int
}

//NewOpenONUAC returns a new instance of OpenONU_AC
func NewOpenONUAC(ctx context.Context, coreClient *vgrpc.Client, eventProxy eventif.EventProxy,
	kvClient kvstore.Client, cfg *config.AdapterFlags, cm *conf.ConfigManager) *OpenONUAC {
	var openOnuAc OpenONUAC
	openOnuAc.exitChannel = make(chan int, 1)
	openOnuAc.deviceHandlers = make(map[string]*deviceHandler)
	openOnuAc.deviceHandlersCreateChan = make(map[string]chan bool)
	openOnuAc.parentAdapterClients = make(map[string]*vgrpc.Client)
	openOnuAc.reachableFromRemote = make(map[string]*reachabilityFromRemote)
	openOnuAc.mutexDeviceHandlersMap = sync.RWMutex{}
	openOnuAc.config = cfg
	openOnuAc.cm = cm
	openOnuAc.coreClient = coreClient
	openOnuAc.numOnus = cfg.OnuNumber
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
	openOnuAc.rpcTimeout = cfg.RPCTimeout
	openOnuAc.maxConcurrentFlowsPerUni = cfg.MaxConcurrentFlowsPerUni

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

//stop terminates the session
func (oo *OpenONUAC) Stop(ctx context.Context) error {
	logger.Info(ctx, "stopping-device-manager")
	close(oo.exitChannel)
	oo.StopAllGrpcClients(ctx)
	logger.Info(ctx, "device-manager-stopped")
	return nil
}

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

// AdoptDevice creates a new device handler if not present already and then adopts the device
func (oo *OpenONUAC) AdoptDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	if device == nil {
		logger.Warn(ctx, "voltha-device-is-nil")
		return nil, errors.New("nil-device")
	}
	logger.Infow(ctx, "adopt-device", log.Fields{"device-id": device.Id})
	var handler *deviceHandler
	if handler = oo.getDeviceHandler(ctx, device.Id, false); handler == nil {
		handler := newDeviceHandler(ctx, oo.coreClient, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)

		// Setup the grpc communication with the parent adapter
		if err := oo.setupParentInterAdapterClient(ctx, device.ProxyAddress.AdapterEndpoint); err != nil {
			// TODO: Cleanup on failure needed
			return nil, err
		}

		go handler.adoptOrReconcileDevice(log.WithSpanFromContext(context.Background(), ctx), device)
	}
	return &empty.Empty{}, nil
}

//ReconcileDevice is called once when the adapter needs to re-create device - usually on core restart
func (oo *OpenONUAC) ReconcileDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	if device == nil {
		logger.Warn(ctx, "reconcile-device-voltha-device-is-nil")
		return nil, errors.New("nil-device")
	}
	logger.Infow(ctx, "reconcile-device", log.Fields{"device-id": device.Id, "parent-id": device.ParentId})

	// Check whether the grpc client in the adapter of the parent device can reach us yet
	if !oo.isReachableFromRemote(ctx, device.ProxyAddress.AdapterEndpoint, device.ProxyAddress.DeviceId) {
		return nil, status.Errorf(codes.Unavailable, "adapter-not-reachable-from-parent-%s", device.ProxyAddress.AdapterEndpoint)
	}

	var handler *deviceHandler
	if handler = oo.getDeviceHandler(ctx, device.Id, false); handler == nil {
		handler := newDeviceHandler(ctx, oo.coreClient, oo.eventProxy, device, oo)
		oo.addDeviceHandlerToMap(ctx, handler)
		handler.device = device
		if err := handler.updateDeviceStateInCore(log.WithSpanFromContext(context.Background(), ctx), &ca.DeviceStateFilter{
			DeviceId:   device.Id,
			OperStatus: voltha.OperStatus_RECONCILING,
			ConnStatus: device.ConnectStatus,
		}); err != nil {
			return nil, fmt.Errorf("not able to update device state to reconciling. Err : %s", err.Error())
		}
		// Setup the grpc communication with the parent adapter
		if err := oo.setupParentInterAdapterClient(ctx, device.ProxyAddress.AdapterEndpoint); err != nil {
			// TODO: Cleanup on failure needed
			return nil, err
		}

		handler.StartReconciling(log.WithSpanFromContext(context.Background(), ctx), false)
		go handler.adoptOrReconcileDevice(log.WithSpanFromContext(context.Background(), ctx), handler.device)
		// reconcilement will be continued after onu-device entry is added
	} else {
		return nil, fmt.Errorf(fmt.Sprintf("device-already-reconciled-or-active-%s", device.Id))
	}
	return &empty.Empty{}, nil
}

//DisableDevice disables the given device
func (oo *OpenONUAC) DisableDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	logger.Infow(ctx, "disable-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.disableDevice(log.WithSpanFromContext(context.Background(), ctx), device)
		return &empty.Empty{}, nil
	}
	logger.Warnw(ctx, "no handler found for device-disable", log.Fields{"device-id": device.Id})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//ReEnableDevice enables the onu device after disable
func (oo *OpenONUAC) ReEnableDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	logger.Infow(ctx, "reenable-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.reEnableDevice(log.WithSpanFromContext(context.Background(), ctx), device)
		return &empty.Empty{}, nil
	}
	logger.Warnw(ctx, "no handler found for device-reenable", log.Fields{"device-id": device.Id})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", device.Id))
}

//RebootDevice reboots the given device
func (oo *OpenONUAC) RebootDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	logger.Infow(ctx, "reboot-device", log.Fields{"device-id": device.Id})
	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		go handler.rebootDevice(log.WithSpanFromContext(context.Background(), ctx), true, device) //reboot request with device checking
		return &empty.Empty{}, nil
	}
	logger.Warnw(ctx, "no handler found for device-reboot", log.Fields{"device-id": device.Id})
	return nil, fmt.Errorf("handler-not-found-for-device: %s", device.Id)
}

// DeleteDevice deletes the given device
func (oo *OpenONUAC) DeleteDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	nctx := log.WithSpanFromContext(context.Background(), ctx)
	logger.Infow(ctx, "delete-device", log.Fields{"device-id": device.Id, "SerialNumber": device.SerialNumber, "ctx": ctx, "nctx": nctx})

	if handler := oo.getDeviceHandler(ctx, device.Id, false); handler != nil {
		var errorsList []error

		handler.mutexDeletionInProgressFlag.Lock()
		handler.deletionInProgress = true
		handler.mutexDeletionInProgressFlag.Unlock()

		if err := handler.resetFsms(ctx, true); err != nil {
			errorsList = append(errorsList, err)
		}
		for _, uni := range handler.uniEntityMap {
			if handler.GetFlowMonitoringIsRunning(uni.UniID) {
				handler.stopFlowMonitoringRoutine[uni.UniID] <- true
				logger.Debugw(ctx, "sent stop signal to self flow monitoring routine", log.Fields{"device-id": device.Id})
			}
		}
		//don't leave any garbage in kv-store
		if err := oo.forceDeleteDeviceKvData(ctx, device.Id); err != nil {
			errorsList = append(errorsList, err)
		}
		oo.deleteDeviceHandlerToMap(handler)
		go handler.PrepareForGarbageCollection(ctx, handler.DeviceID)

		if len(errorsList) > 0 {
			logger.Errorw(ctx, "one-or-more-error-during-device-delete", log.Fields{"device-id": device.Id})
			return nil, fmt.Errorf("one-or-more-error-during-device-delete, errors:%v", errorsList)
		}
		return &empty.Empty{}, nil
	}
	logger.Infow(ctx, "no handler found for device-deletion - trying to delete remaining data in the kv-store ", log.Fields{"device-id": device.Id})

	if err := oo.forceDeleteDeviceKvData(ctx, device.Id); err != nil {
		return nil, err
	}
	return &empty.Empty{}, nil
}

//UpdateFlowsIncrementally updates (add/remove) the flows on a given device
func (oo *OpenONUAC) UpdateFlowsIncrementally(ctx context.Context, incrFlows *ca.IncrementalFlows) (*empty.Empty, error) {
	logger.Infow(ctx, "update-flows-incrementally", log.Fields{"device-id": incrFlows.Device.Id})

	//flow config is relayed to handler even if the device might be in some 'inactive' state
	// let the handler or related FSM's decide, what to do with the modified flow state info
	// at least the flow-remove must be done in respect to internal data, while OMCI activity might not be needed here

	// For now, there is no support for group changes (as in the actual Py-adapter code)
	//   but processing is continued for flowUpdate possibly also set in the request
	if incrFlows.Groups.ToAdd != nil && incrFlows.Groups.ToAdd.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group add not supported (ignored)", log.Fields{"device-id": incrFlows.Device.Id})
	}
	if incrFlows.Groups.ToRemove != nil && incrFlows.Groups.ToRemove.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group remove not supported (ignored)", log.Fields{"device-id": incrFlows.Device.Id})
	}
	if incrFlows.Groups.ToUpdate != nil && incrFlows.Groups.ToUpdate.Items != nil {
		logger.Warnw(ctx, "Update-flow-incr: group update not supported (ignored)", log.Fields{"device-id": incrFlows.Device.Id})
	}

	if handler := oo.getDeviceHandler(ctx, incrFlows.Device.Id, false); handler != nil {
		if err := handler.FlowUpdateIncremental(log.WithSpanFromContext(context.Background(), ctx), incrFlows.Flows, incrFlows.Groups, incrFlows.FlowMetadata); err != nil {
			return nil, err
		}
		return &empty.Empty{}, nil
	}
	logger.Warnw(ctx, "no handler found for incremental flow update", log.Fields{"device-id": incrFlows.Device.Id})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", incrFlows.Device.Id))
}

//UpdatePmConfig returns PmConfigs nil or error
func (oo *OpenONUAC) UpdatePmConfig(ctx context.Context, configs *ca.PmConfigsInfo) (*empty.Empty, error) {
	logger.Infow(ctx, "update-pm-config", log.Fields{"device-id": configs.DeviceId})
	if handler := oo.getDeviceHandler(ctx, configs.DeviceId, false); handler != nil {
		if err := handler.updatePmConfig(log.WithSpanFromContext(context.Background(), ctx), configs.PmConfigs); err != nil {
			return nil, err
		}
		return &empty.Empty{}, nil
	}
	logger.Warnw(ctx, "no handler found for update-pm-config", log.Fields{"device-id": configs.DeviceId})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", configs.DeviceId))
}

//DownloadImage requests downloading some image according to indications as given in request
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) DownloadImage(ctx context.Context, imageInfo *ca.ImageDownloadMessage) (*voltha.ImageDownload, error) {
	ctx = log.WithSpanFromContext(context.Background(), ctx)
	if imageInfo != nil && imageInfo.Image != nil && imageInfo.Image.Name != "" {
		if !oo.pDownloadManager.ImageExists(ctx, imageInfo.Image) {
			logger.Debugw(ctx, "start image download", log.Fields{"image-description": imageInfo.Image})
			// Download_image is not supposed to be blocking, anyway let's call the DownloadManager still synchronously to detect 'fast' problems
			// the download itself is later done in background
			if err := oo.pDownloadManager.StartDownload(ctx, imageInfo.Image); err != nil {
				return nil, err
			}
			return imageInfo.Image, nil
		}
		// image already exists
		logger.Debugw(ctx, "image already downloaded", log.Fields{"image-description": imageInfo.Image})
		return imageInfo.Image, nil
	}

	return nil, errors.New("invalid image definition")
}

//ActivateImageUpdate requests downloading some Onu Software image to the ONU via OMCI
//  according to indications as given in request and on success activate the image on the ONU
//The ImageDownload needs to be called `request`due to library reflection requirements
func (oo *OpenONUAC) ActivateImageUpdate(ctx context.Context, imageInfo *ca.ImageDownloadMessage) (*voltha.ImageDownload, error) {
	if imageInfo != nil && imageInfo.Image != nil && imageInfo.Image.Name != "" {
		if oo.pDownloadManager.ImageLocallyDownloaded(ctx, imageInfo.Image) {
			if handler := oo.getDeviceHandler(ctx, imageInfo.Device.Id, false); handler != nil {
				logger.Debugw(ctx, "image download on omci requested", log.Fields{
					"image-description": imageInfo.Image, "device-id": imageInfo.Device.Id})
				if err := handler.doOnuSwUpgrade(ctx, imageInfo.Image, oo.pDownloadManager); err != nil {
					return nil, err
				}
				return imageInfo.Image, nil
			}
			logger.Warnw(ctx, "no handler found for image activation", log.Fields{"device-id": imageInfo.Device.Id})
			return nil, fmt.Errorf(fmt.Sprintf("handler-not-found - device-id: %s", imageInfo.Device.Id))
		}
		logger.Debugw(ctx, "image not yet downloaded on activate request", log.Fields{"image-description": imageInfo.Image})
		return nil, fmt.Errorf(fmt.Sprintf("image-not-yet-downloaded - device-id: %s", imageInfo.Device.Id))
	}
	return nil, errors.New("invalid image definition")
}

//GetSingleValue handles the core request to retrieve uni status
func (oo *OpenONUAC) GetSingleValue(ctx context.Context, request *extension.SingleGetValueRequest) (*extension.SingleGetValueResponse, error) {
	logger.Infow(ctx, "Single_get_value_request", log.Fields{"request": request})

	if handler := oo.getDeviceHandler(ctx, request.TargetId, false); handler != nil {
		switch reqType := request.GetRequest().GetRequest().(type) {
		case *extension.GetValueRequest_UniInfo:
			return handler.GetUniPortStatus(ctx, reqType.UniInfo), nil
		case *extension.GetValueRequest_OnuOpticalInfo:
			CommChan := make(chan cmn.Message)
			respChan := make(chan extension.SingleGetValueResponse)
			// Initiate the self test request
			if err := handler.pSelfTestHdlr.SelfTestRequestStart(ctx, *request, CommChan, respChan); err != nil {
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

// DownloadOnuImage downloads (and optionally activates and commits) the indicated ONU image to the requested ONU(s)
//   if the image is not yet present on the adapter it has to be automatically downloaded
func (oo *OpenONUAC) DownloadOnuImage(ctx context.Context, request *voltha.DeviceImageDownloadRequest) (*voltha.DeviceImageResponse, error) {
	if request != nil && len((*request).DeviceId) > 0 && (*request).Image.Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*request).Image.Version
		downloadStartDone := false
		firstDevice := true
		var vendorID string
		var onuVolthaDevice *voltha.Device
		var devErr error
		for _, pCommonID := range (*request).DeviceId {
			vendorIDMatch := true
			loDeviceID := (*pCommonID).Id
			loDeviceImageState := voltha.DeviceImageState{}
			loDeviceImageState.DeviceId = loDeviceID
			loImageState := voltha.ImageState{}
			loDeviceImageState.ImageState = &loImageState
			loDeviceImageState.ImageState.Version = (*request).Image.Version

			onuVolthaDevice = nil
			handler := oo.getDeviceHandler(ctx, loDeviceID, false)
			if handler != nil {
				onuVolthaDevice, devErr = handler.getDeviceFromCore(ctx, loDeviceID)
			} else {
				// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
				devErr = errors.New("no handler found for device-id")
			}
			if devErr != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch ONU device for image download",
					log.Fields{"device-id": loDeviceID, "err": devErr})
				loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
				loDeviceImageState.ImageState.Reason = voltha.ImageState_UNKNOWN_ERROR //proto restriction, better option: 'INVALID_DEVICE'
				loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
			} else {
				if firstDevice {
					//start/verify download of the image to the adapter based on first found device only
					//  use the OnuVendor identification from first given device

					//  note: if the request was done for a list of devices on the Voltha interface, rwCore
					//  translates that into a new rpc for each device, hence each device will be the first device in parallel requests!
					firstDevice = false
					vendorID = onuVolthaDevice.VendorId
					imageIdentifier = vendorID + imageIdentifier //head on vendor ID of the ONU
					logger.Infow(ctx, "download request for file",
						log.Fields{"device-id": loDeviceID, "image-id": imageIdentifier})

					// call the StartDownload synchronously to detect 'immediate' download problems
					// the real download itself is later done in background
					if fileState, err := oo.pFileManager.StartDownload(ctx, imageIdentifier, (*request).Image.Url); err == nil {
						// note: If the image (with vendorId+name) has already been downloaded before from some other
						//   valid URL, the current download request is not executed (current code delivers URL error).
						//   If the operators want to ensure that the new URL
						//   is really used, then they first have to use the 'abort' API to remove the existing image!
						//   (abort API can be used also after some successful download to just remove the image from adapter)
						if fileState == swupg.CFileStateDlSucceeded || fileState == swupg.CFileStateDlStarted {
							downloadStartDone = true
						} //else fileState may also indicate error situation, where the requested image is not ready to be used for other devices
					}
				} else {
					//for all following devices verify the matching vendorID
					if onuVolthaDevice.VendorId != vendorID {
						logger.Warnw(ctx, "onu vendor id does not match image vendor id, device ignored",
							log.Fields{"onu-vendor-id": onuVolthaDevice.VendorId, "image-vendor-id": vendorID})
						vendorIDMatch = false
					}
				}
				if downloadStartDone && vendorIDMatch {
					// start the ONU download activity for each possible device
					logger.Infow(ctx, "request image download to ONU on omci ", log.Fields{
						"image-id": imageIdentifier, "device-id": loDeviceID})
					//onu upgrade handling called in background without immediate error evaluation here
					//  as the processing can be done for multiple ONU's and an error on one ONU should not stop processing for others
					//  state/progress/success of the request has to be verified using the Get_onu_image_status() API
					go handler.onuSwUpgradeAfterDownload(ctx, request, oo.pFileManager, imageIdentifier)
					loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_STARTED
					loDeviceImageState.ImageState.Reason = voltha.ImageState_NO_ERROR
					loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
				} else {
					loDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_FAILED
					if !downloadStartDone {
						//based on above fileState more descriptive error codes would be possible, e.g
						//   IMAGE_EXISTS_WITH_DIFFERENT_URL - would require proto buf update
						loDeviceImageState.ImageState.Reason = voltha.ImageState_INVALID_URL
					} else { //only logical option is !vendorIDMatch
						loDeviceImageState.ImageState.Reason = voltha.ImageState_VENDOR_DEVICE_MISMATCH
					}
					loDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
				}
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, &loDeviceImageState)
		} //for all requested devices
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image download parameters")
}

// GetOnuImageStatus delivers the adapter-related information about the download/activation/commitment
//   status for the requested image
func (oo *OpenONUAC) GetOnuImageStatus(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		var vendorIDSet bool
		firstDevice := true
		var vendorID string
		var onuVolthaDevice *voltha.Device
		var devErr error
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			pDeviceImageState := &voltha.DeviceImageState{DeviceId: loDeviceID}
			vendorIDSet = false
			onuVolthaDevice = nil
			handler := oo.getDeviceHandler(ctx, loDeviceID, false)
			if handler != nil {
				onuVolthaDevice, devErr = handler.getDeviceFromCore(ctx, loDeviceID)
			} else {
				// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
				devErr = errors.New("no handler found for device-id")
			}
			if devErr != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch Onu device to get image status",
					log.Fields{"device-id": loDeviceID, "err": devErr})
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
					logger.Debugw(ctx, "image status request for", log.Fields{
						"image-id": imageIdentifier, "device-id": loDeviceID})
					//status request is called synchronously to collect the indications for all concerned devices
					pDeviceImageState.ImageState = handler.requestOnuSwUpgradeState(ctx, imageIdentifier, (*in).Version)
				}
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, pDeviceImageState)
		} //for all requested devices
		pImageResp := &loResponse
		return pImageResp, nil
	}
	return nil, errors.New("invalid image status request parameters")
}

// AbortOnuImageUpgrade stops the actual download/activation/commitment process (on next possibly step)
func (oo *OpenONUAC) AbortOnuImageUpgrade(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
	if in != nil && len((*in).DeviceId) > 0 && (*in).Version != "" {
		loResponse := voltha.DeviceImageResponse{}
		imageIdentifier := (*in).Version
		firstDevice := true
		var vendorID string
		var vendorIDSet bool
		var onuVolthaDevice *voltha.Device
		var devErr error
		for _, pCommonID := range (*in).DeviceId {
			loDeviceID := (*pCommonID).Id
			pDeviceImageState := &voltha.DeviceImageState{}
			loImageState := voltha.ImageState{}
			pDeviceImageState.ImageState = &loImageState
			vendorIDSet = false
			onuVolthaDevice = nil
			handler := oo.getDeviceHandler(ctx, loDeviceID, false)
			if handler != nil {
				onuVolthaDevice, devErr = handler.getDeviceFromCore(ctx, loDeviceID)
			} else {
				// assumption here is, that the concerned device was already created (automatic start after device creation not supported)
				devErr = errors.New("no handler found for device-id")
			}
			if devErr != nil || onuVolthaDevice == nil {
				logger.Warnw(ctx, "Failed to fetch Onu device to abort its download",
					log.Fields{"device-id": loDeviceID, "err": devErr})
				pDeviceImageState.DeviceId = loDeviceID
				pDeviceImageState.ImageState.Version = (*in).Version
				pDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
				pDeviceImageState.ImageState.Reason = voltha.ImageState_CANCELLED_ON_REQUEST //something better could be considered (MissingHandler) - proto
				pDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
			} else {
				if firstDevice {
					//start/verify download of the image to the adapter based on first found device only
					//  use the OnuVendor identification from first given device
					firstDevice = false
					vendorID = onuVolthaDevice.VendorId
					vendorIDSet = true
					imageIdentifier = vendorID + imageIdentifier //head on vendor ID of the ONU
					logger.Debugw(ctx, "abort request for file", log.Fields{"image-id": imageIdentifier})
				} else {
					//for all following devices verify the matching vendorID
					if onuVolthaDevice.VendorId != vendorID {
						logger.Warnw(ctx, "onu vendor id does not match image vendor id, device ignored",
							log.Fields{"onu-vendor-id": onuVolthaDevice.VendorId, "image-vendor-id": vendorID})
						pDeviceImageState.DeviceId = loDeviceID
						pDeviceImageState.ImageState.Version = (*in).Version
						pDeviceImageState.ImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
						pDeviceImageState.ImageState.Reason = voltha.ImageState_VENDOR_DEVICE_MISMATCH
						pDeviceImageState.ImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
					} else {
						vendorIDSet = true
					}
				}
				if vendorIDSet {
					// cancel the ONU upgrade activity for each possible device
					logger.Debugw(ctx, "image upgrade abort requested", log.Fields{
						"image-id": imageIdentifier, "device-id": loDeviceID})
					//upgrade cancel is called synchronously to collect the imageResponse indications for all concerned devices
					handler.cancelOnuSwUpgrade(ctx, imageIdentifier, (*in).Version, pDeviceImageState)
				}
			}
			loResponse.DeviceImageStates = append(loResponse.DeviceImageStates, pDeviceImageState)
		} //for all requested devices
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

// GetOnuImages retrieves the ONU SW image status information via OMCI
func (oo *OpenONUAC) GetOnuImages(ctx context.Context, id *common.ID) (*voltha.OnuImages, error) {
	logger.Infow(ctx, "Get_onu_images", log.Fields{"device-id": id.Id})
	if handler := oo.getDeviceHandler(ctx, id.Id, false); handler != nil {
		images, err := handler.getOnuImages(ctx)
		if err == nil {
			return images, nil
		}
		return nil, fmt.Errorf(fmt.Sprintf("%s-%s", err, id.Id))
	}
	logger.Warnw(ctx, "no handler found for Get_onu_images", log.Fields{"device-id": id.Id})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", id.Id))
}

// ActivateOnuImage initiates the activation of the image for the requested ONU(s)
//  precondition: image downloaded and not yet activated or image refers to current inactive image
func (oo *OpenONUAC) ActivateOnuImage(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
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

// CommitOnuImage enforces the commitment of the image for the requested ONU(s)
//  precondition: image activated and not yet committed
func (oo *OpenONUAC) CommitOnuImage(ctx context.Context, in *voltha.DeviceImageRequest) (*voltha.DeviceImageResponse, error) {
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

/*
 *
 * ONU inter adapter service
 *
 */

// OnuIndication is part of the ONU Inter-adapter service API.
func (oo *OpenONUAC) OnuIndication(ctx context.Context, onuInd *ia.OnuIndicationMessage) (*empty.Empty, error) {
	logger.Debugw(ctx, "onu-indication", log.Fields{"onu-indication": onuInd})

	if onuInd == nil || onuInd.OnuIndication == nil {
		return nil, fmt.Errorf("invalid-onu-indication-%v", onuInd)
	}

	onuIndication := onuInd.OnuIndication
	onuOperstate := onuIndication.GetOperState()
	waitForDhInstPresent := false
	if onuOperstate == "up" {
		//Race condition (relevant in BBSIM-environment only): Due to unsynchronized processing of olt-adapter and rw_core,
		//ONU_IND_REQUEST msg by olt-adapter could arrive a little bit earlier than rw_core was able to announce the corresponding
		//ONU by RPC of Adopt_device(). Therefore it could be necessary to wait with processing of ONU_IND_REQUEST until call of
		//Adopt_device() arrived and DeviceHandler instance was created
		waitForDhInstPresent = true
	}
	if handler := oo.getDeviceHandler(ctx, onuInd.DeviceId, waitForDhInstPresent); handler != nil {
		logger.Infow(ctx, "onu-ind-request", log.Fields{"device-id": onuInd.DeviceId,
			"OnuId":      onuIndication.GetOnuId(),
			"AdminState": onuIndication.GetAdminState(), "OperState": onuOperstate,
			"SNR": onuIndication.GetSerialNumber()})

		if onuOperstate == "up" {
			if err := handler.createInterface(ctx, onuIndication); err != nil {
				return nil, err
			}
			return &empty.Empty{}, nil
		} else if (onuOperstate == "down") || (onuOperstate == "unreachable") {
			if err := handler.updateInterface(ctx, onuIndication); err != nil {
				return nil, err
			}
			return &empty.Empty{}, nil
		} else {
			logger.Errorw(ctx, "unknown-onu-ind-request operState", log.Fields{"OnuId": onuIndication.GetOnuId()})
			return nil, fmt.Errorf("invalidOperState: %s, %s", onuOperstate, onuInd.DeviceId)
		}
	}
	logger.Warnw(ctx, "no handler found for received onu-ind-request", log.Fields{
		"msgToDeviceId": onuInd.DeviceId})
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", onuInd.DeviceId))
}

// OmciIndication is part of the ONU Inter-adapter service API.
func (oo *OpenONUAC) OmciIndication(ctx context.Context, msg *ia.OmciMessage) (*empty.Empty, error) {
	logger.Debugw(ctx, "omci-response", log.Fields{"parent-device-id": msg.ParentDeviceId, "child-device-id": msg.ChildDeviceId})

	if handler := oo.getDeviceHandler(ctx, msg.ChildDeviceId, false); handler != nil {
		if err := handler.handleOMCIIndication(log.WithSpanFromContext(context.Background(), ctx), msg); err != nil {
			return nil, err
		}
		return &empty.Empty{}, nil
	}
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", msg.ChildDeviceId))
}

// DownloadTechProfile is part of the ONU Inter-adapter service API.
func (oo *OpenONUAC) DownloadTechProfile(ctx context.Context, tProfile *ia.TechProfileDownloadMessage) (*empty.Empty, error) {
	logger.Debugw(ctx, "download-tech-profile", log.Fields{"device-id": tProfile.DeviceId, "uni-id": tProfile.UniId})

	if handler := oo.getDeviceHandler(ctx, tProfile.DeviceId, false); handler != nil {
		if err := handler.handleTechProfileDownloadRequest(log.WithSpanFromContext(context.Background(), ctx), tProfile); err != nil {
			return nil, err
		}
		return &empty.Empty{}, nil
	}
	return nil, fmt.Errorf(fmt.Sprintf("handler-not-found-%s", tProfile.DeviceId))
}

// DeleteGemPort is part of the ONU Inter-adapter service API.
func (oo *OpenONUAC) DeleteGemPort(ctx context.Context, gPort *ia.DeleteGemPortMessage) (*empty.Empty, error) {
	logger.Debugw(ctx, "delete-gem-port", log.Fields{"device-id": gPort.DeviceId, "uni-id": gPort.UniId})

	if handler := oo.getDeviceHandler(ctx, gPort.DeviceId, false); handler != nil {
		if err := handler.handleDeleteGemPortRequest(log.WithSpanFromContext(context.Background(), ctx), gPort); err != nil {
			return nil, err
		}
	} else {
		logger.Debugw(ctx, "deviceHandler not found", log.Fields{"device-id": gPort.DeviceId})
		// delete requests for objects of an already deleted ONU should be acknowledged positively - continue
	}
	return &empty.Empty{}, nil
}

// DeleteTCont is part of the ONU Inter-adapter service API.
func (oo *OpenONUAC) DeleteTCont(ctx context.Context, tConf *ia.DeleteTcontMessage) (*empty.Empty, error) {
	logger.Debugw(ctx, "delete-tcont", log.Fields{"device-id": tConf.DeviceId, "tconf": tConf})

	if handler := oo.getDeviceHandler(ctx, tConf.DeviceId, false); handler != nil {
		if err := handler.handleDeleteTcontRequest(log.WithSpanFromContext(context.Background(), ctx), tConf); err != nil {
			return nil, err
		}
	} else {
		logger.Debugw(ctx, "deviceHandler not found", log.Fields{"device-id": tConf.DeviceId})
		// delete requests for objects of an already deleted ONU should be acknowledged positively - continue
	}
	return &empty.Empty{}, nil
}

/*
 * Parent GRPC clients
 */

func getHash(endpoint, contextInfo string) string {
	strToHash := endpoint + contextInfo
	h := fnv.New128().Sum([]byte(strToHash))
	return string(h)
}

func (oo *OpenONUAC) updateReachabilityFromRemote(ctx context.Context, remote *common.Connection) {
	logger.Debugw(context.Background(), "updating-remote-connection-status", log.Fields{"remote": remote})
	oo.lockReachableFromRemote.Lock()
	defer oo.lockReachableFromRemote.Unlock()
	endpointHash := getHash(remote.Endpoint, remote.ContextInfo)
	if _, ok := oo.reachableFromRemote[endpointHash]; ok {
		oo.reachableFromRemote[endpointHash].lastKeepAlive = time.Now()
		oo.reachableFromRemote[endpointHash].keepAliveInterval = remote.KeepAliveInterval
		return
	}
	logger.Debugw(context.Background(), "initial-remote-connection", log.Fields{"remote": remote})
	oo.reachableFromRemote[endpointHash] = &reachabilityFromRemote{lastKeepAlive: time.Now(), keepAliveInterval: remote.KeepAliveInterval}
}

func (oo *OpenONUAC) isReachableFromRemote(ctx context.Context, endpoint string, contextInfo string) bool {
	logger.Debugw(ctx, "checking-remote-reachability", log.Fields{"endpoint": endpoint, "context": contextInfo})
	oo.lockReachableFromRemote.RLock()
	defer oo.lockReachableFromRemote.RUnlock()
	endpointHash := getHash(endpoint, contextInfo)
	if _, ok := oo.reachableFromRemote[endpointHash]; ok {
		logger.Debugw(ctx, "endpoint-exists", log.Fields{"last-keep-alive": time.Since(oo.reachableFromRemote[endpointHash].lastKeepAlive)})
		// Assume the connection is down if we did not receive 2 keep alives in succession
		maxKeepAliveWait := time.Duration(oo.reachableFromRemote[endpointHash].keepAliveInterval * 2)
		return time.Since(oo.reachableFromRemote[endpointHash].lastKeepAlive) <= maxKeepAliveWait
	}
	return false
}

func (oo *OpenONUAC) StopAllGrpcClients(ctx context.Context) {
	// Stop the clients that connect to the parent
	oo.lockParentAdapterClients.Lock()
	for key, _ := range oo.parentAdapterClients {
		oo.parentAdapterClients[key].Stop(ctx)
		delete(oo.parentAdapterClients, key)
	}
	oo.lockParentAdapterClients.Unlock()

	// Stop core client connection
	oo.coreClient.Stop(ctx)
}

func (oo *OpenONUAC) setupParentInterAdapterClient(ctx context.Context, endpoint string) error {
	logger.Infow(ctx, "setting-parent-adapter-connection", log.Fields{"parent-endpoint": endpoint})
	oo.lockParentAdapterClients.Lock()
	defer oo.lockParentAdapterClients.Unlock()
	if _, ok := oo.parentAdapterClients[endpoint]; ok {
		return nil
	}

	childClient, err := vgrpc.NewClient(
		oo.config.AdapterEndpoint,
		endpoint,
		"olt_inter_adapter_service.OltInterAdapterService",
		oo.oltAdapterRestarted)

	if err != nil {
		return err
	}

	oo.parentAdapterClients[endpoint] = childClient

	go oo.parentAdapterClients[endpoint].Start(log.WithSpanFromContext(context.TODO(), ctx), getOltInterAdapterServiceClientHandler)

	// Wait until we have a connection to the child adapter.
	// Unlimited retries or until context expires
	subCtx := log.WithSpanFromContext(context.TODO(), ctx)
	backoff := vgrpc.NewBackoff(oo.config.MinBackoffRetryDelay, oo.config.MaxBackoffRetryDelay, 0)
	for {
		client, err := oo.parentAdapterClients[endpoint].GetOltInterAdapterServiceClient()
		if err == nil && client != nil {
			logger.Infow(subCtx, "connected-to-parent-adapter", log.Fields{"parent-endpoint": endpoint})
			break
		}
		logger.Warnw(subCtx, "connection-to-parent-adapter-not-ready", log.Fields{"error": err, "parent-endpoint": endpoint})
		// Backoff
		if err = backoff.Backoff(subCtx); err != nil {
			logger.Errorw(subCtx, "received-error-on-backoff", log.Fields{"error": err, "parent-endpoint": endpoint})
			break
		}
	}
	return nil
}

func (oo *OpenONUAC) getParentAdapterServiceClient(endpoint string) (olt_inter_adapter_service.OltInterAdapterServiceClient, error) {
	// First check from cache
	oo.lockParentAdapterClients.RLock()
	if pgClient, ok := oo.parentAdapterClients[endpoint]; ok {
		oo.lockParentAdapterClients.RUnlock()
		return pgClient.GetOltInterAdapterServiceClient()
	}
	oo.lockParentAdapterClients.RUnlock()

	// Set the parent connection - can occur on restarts
	ctx, cancel := context.WithTimeout(context.Background(), oo.config.RPCTimeout)
	err := oo.setupParentInterAdapterClient(ctx, endpoint)
	cancel()
	if err != nil {
		return nil, err
	}

	// Get the parent client now
	oo.lockParentAdapterClients.RLock()
	defer oo.lockParentAdapterClients.RUnlock()
	if pgClient, ok := oo.parentAdapterClients[endpoint]; ok {
		return pgClient.GetOltInterAdapterServiceClient()
	}

	return nil, fmt.Errorf("no-client-for-endpoint-%s", endpoint)
}

// TODO:  Any action the adapter needs to do following an olt adapter restart?
func (oo *OpenONUAC) oltAdapterRestarted(ctx context.Context, endPoint string) error {
	logger.Errorw(ctx, "olt-adapter-restarted", log.Fields{"endpoint": endPoint})
	return nil
}

// getOltInterAdapterServiceClientHandler is used to setup the remote gRPC service
func getOltInterAdapterServiceClientHandler(ctx context.Context, conn *grpc.ClientConn) interface{} {
	if conn == nil {
		return nil
	}
	return olt_inter_adapter_service.NewOltInterAdapterServiceClient(conn)
}

func (oo *OpenONUAC) forceDeleteDeviceKvData(ctx context.Context, aDeviceID string) error {
	logger.Debugw(ctx, "force deletion of ONU device specific data in kv store", log.Fields{"device-id": aDeviceID})
	var errorsList []error
	// delete onu persitent data
	for i := range onuKvStorePathPrefixes {
		baseKvStorePath := fmt.Sprintf(onuKvStorePathPrefixes[i], oo.cm.Backend.PathPrefix)
		logger.Debugw(ctx, "SetKVStoreBackend", log.Fields{"IpTarget": oo.KVStoreAddress, "BasePathKvStore": baseKvStorePath,
			"device-id": aDeviceID})
		onuKvbackend := &db.Backend{
			Client:     oo.kvClient,
			StoreType:  oo.KVStoreType,
			Address:    oo.KVStoreAddress,
			Timeout:    oo.KVStoreTimeout,
			PathPrefix: baseKvStorePath,
		}
		err := onuKvbackend.DeleteWithPrefix(ctx, aDeviceID)
		if err != nil {
			logger.Errorw(ctx, "unable to delete in KVstore", log.Fields{"service": baseKvStorePath, "device-id": aDeviceID, "err": err})
			// continue to delete kv data, but accumulate any errors
			errorsList = append(errorsList, err)
		}
	}
	// delete pm data
	pmKvbackend := &db.Backend{
		Client:     oo.kvClient,
		StoreType:  oo.KVStoreType,
		Address:    oo.KVStoreAddress,
		Timeout:    oo.KVStoreTimeout,
		PathPrefix: fmt.Sprintf(pmmgr.CPmKvStorePrefixBase, oo.cm.Backend.PathPrefix),
	}
	err := pmKvbackend.DeleteWithPrefix(ctx, aDeviceID)
	if err != nil {
		logger.Errorw(ctx, "unable to delete PM in KVstore", log.Fields{"path": pmmgr.CPmKvStorePrefixBase + aDeviceID, "device-id": aDeviceID, "err": err})
		// accumulate any errors
		errorsList = append(errorsList, err)
	}
	if len(errorsList) > 0 {
		return fmt.Errorf("one or more error deleting kv data, error: %v", errorsList)
	}
	return nil
}

// GetHealthStatus is used by the voltha core to open a streaming connection with the onu adapter.
func (oo *OpenONUAC) GetHealthStatus(stream adapter_service.AdapterService_GetHealthStatusServer) error {
	ctx := context.Background()
	logger.Debugw(ctx, "receive-stream-connection", log.Fields{"stream": stream})

	if stream == nil {
		return fmt.Errorf("conn-is-nil %v", stream)
	}
	initialRequestTime := time.Now()
	var remoteClient *common.Connection
	var tempClient *common.Connection
	var err error
loop:
	for {
		tempClient, err = stream.Recv()
		if err != nil {
			logger.Warnw(ctx, "received-stream-error", log.Fields{"remote-client": remoteClient, "error": err})
			break loop
		}
		// Send a response back
		err = stream.Send(&health.HealthStatus{State: health.HealthStatus_HEALTHY})
		if err != nil {
			logger.Warnw(ctx, "sending-stream-error", log.Fields{"remote-client": remoteClient, "error": err})
			break loop
		}
		remoteClient = tempClient

		logger.Debugw(ctx, "received-keep-alive", log.Fields{"remote-client": remoteClient})

		select {
		case <-stream.Context().Done():
			logger.Infow(ctx, "stream-keep-alive-context-done", log.Fields{"remote-client": remoteClient, "error": stream.Context().Err()})
			break loop
		case <-oo.exitChannel:
			logger.Warnw(ctx, "received-stop", log.Fields{"remote-client": remoteClient, "initial-conn-time": initialRequestTime})
			break loop
		default:
		}
	}
	logger.Errorw(ctx, "connection-down", log.Fields{"remote-client": remoteClient, "error": err, "initial-conn-time": initialRequestTime})
	return err
}

/*
 *
 * Unimplemented APIs
 *
 */

//GetOfpDeviceInfo returns OFP information for the given device.  Method not implemented as per [VOL-3202].
// OF port info is now to be delivered within UniPort create cmp changes in onu_uni_port.go::CreateVolthaPort()
//
func (oo *OpenONUAC) GetOfpDeviceInfo(ctx context.Context, device *voltha.Device) (*ca.SwitchCapability, error) {
	return nil, errors.New("unImplemented")
}

//SimulateAlarm is unimplemented
func (oo *OpenONUAC) SimulateAlarm(context.Context, *ca.SimulateAlarmMessage) (*common.OperationResp, error) {
	return nil, errors.New("unImplemented")
}

//SetExtValue is unimplemented
func (oo *OpenONUAC) SetExtValue(context.Context, *ca.SetExtValueMessage) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

//SetSingleValue is unimplemented
func (oo *OpenONUAC) SetSingleValue(context.Context, *extension.SingleSetValueRequest) (*extension.SingleSetValueResponse, error) {
	return nil, errors.New("unImplemented")
}

//StartOmciTest not implemented
func (oo *OpenONUAC) StartOmciTest(ctx context.Context, test *ca.OMCITest) (*omci.TestResponse, error) {
	return nil, errors.New("unImplemented")
}

//SuppressEvent unimplemented
func (oo *OpenONUAC) SuppressEvent(ctx context.Context, filter *voltha.EventFilter) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

//UnSuppressEvent  unimplemented
func (oo *OpenONUAC) UnSuppressEvent(ctx context.Context, filter *voltha.EventFilter) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

//GetImageDownloadStatus is unimplemented
func (oo *OpenONUAC) GetImageDownloadStatus(ctx context.Context, imageInfo *ca.ImageDownloadMessage) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//CancelImageDownload is unimplemented
func (oo *OpenONUAC) CancelImageDownload(ctx context.Context, imageInfo *ca.ImageDownloadMessage) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

//RevertImageUpdate is unimplemented
func (oo *OpenONUAC) RevertImageUpdate(ctx context.Context, imageInfo *ca.ImageDownloadMessage) (*voltha.ImageDownload, error) {
	return nil, errors.New("unImplemented")
}

// UpdateFlowsBulk is unimplemented
func (oo *OpenONUAC) UpdateFlowsBulk(ctx context.Context, flows *ca.BulkFlows) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

//SelfTestDevice unimplented
func (oo *OpenONUAC) SelfTestDevice(ctx context.Context, device *voltha.Device) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

//SendPacketOut sends packet out to the device
func (oo *OpenONUAC) SendPacketOut(ctx context.Context, packet *ca.PacketOut) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

// EnablePort to Enable PON/NNI interface - seems not to be used/required according to python code
func (oo *OpenONUAC) EnablePort(ctx context.Context, port *voltha.Port) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

// DisablePort to Disable pon/nni interface  - seems not to be used/required according to python code
func (oo *OpenONUAC) DisablePort(ctx context.Context, port *voltha.Port) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

// GetExtValue - unimplemented
func (oo *OpenONUAC) GetExtValue(ctx context.Context, extInfo *ca.GetExtValueMessage) (*extension.ReturnValues, error) {
	return nil, errors.New("unImplemented")
}

// ChildDeviceLost - unimplemented
func (oo *OpenONUAC) ChildDeviceLost(ctx context.Context, childDevice *voltha.Device) (*empty.Empty, error) {
	return nil, errors.New("unImplemented")
}

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
