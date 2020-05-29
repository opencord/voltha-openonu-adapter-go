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

	//"sync"
	//"time"

	"github.com/looplab/fsm"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

const (
	KvstoreTimeout             = 5 //in seconds
	BasePathMibTemplateKvStore = "service/voltha/omci_mibs/templates"
	SuffixMibTemplateKvStore   = "%s/%s/%s"
)

type OnuDeviceEvent int

const (
	// Events of interest to Device Adapters and OpenOMCI State Machines
	DeviceStatusInit     OnuDeviceEvent = 0 // OnuDeviceEntry default start state
	MibDatabaseSync      OnuDeviceEvent = 1 // MIB database sync (upload done)
	OmciCapabilitiesDone OnuDeviceEvent = 2 // OMCI ME and message type capabilities known
	MibDownloadDone      OnuDeviceEvent = 3 // MIB database sync (upload done)
	UniLockStateDone     OnuDeviceEvent = 4 // Uni ports admin set to lock
	UniUnlockStateDone   OnuDeviceEvent = 5 // Uni ports admin set to unlock
	UniAdminStateDone    OnuDeviceEvent = 6 // Uni ports admin set done - general
	PortLinkUp           OnuDeviceEvent = 7 // Port link state change
	PortLinkDw           OnuDeviceEvent = 8 // Port link state change
	// Add other events here as needed (alarms separate???)
)

type activityDescr struct {
	databaseClass   func() error
	advertiseEvents bool
	auditDelay      uint16
	//tasks           map[string]func() error
}
type OmciDeviceFsms map[string]activityDescr

type AdapterFsm struct {
	fsmName  string
	deviceID string
	commChan chan Message
	pFsm     *fsm.FSM
}

func NewAdapterFsm(a_name string, a_deviceID string, a_commChannel chan Message) *AdapterFsm {
	aFsm := &AdapterFsm{
		fsmName:  a_name,
		deviceID: a_deviceID,
		commChan: a_commChannel,
	}
	return aFsm
}

//Start starts (logs) the omci agent
func (oo *AdapterFsm) logFsmStateChange(e *fsm.Event) {
	logger.Debugw("FSM state change", log.Fields{"device-id": oo.deviceID, "FSM name": oo.fsmName,
		"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst)})
}

//OntDeviceEntry structure holds information about the attached FSM'as and their communication
type OnuDeviceEntry struct {
	deviceID           string
	baseDeviceHandler  *DeviceHandler
	coreProxy          adapterif.CoreProxy
	adapterProxy       adapterif.AdapterProxy
	started            bool
	PDevOmciCC         *OmciCC
	pOnuDB             *OnuDeviceDB
	mibTemplateKVStore *db.Backend
	vendorID           string
	serialNumber       string
	equipmentID        string
	activeSwVersion    string
	macAddress         string
	//lockDeviceEntries           sync.RWMutex
	mibDbClass    func() error
	supportedFsms OmciDeviceFsms
	devState      OnuDeviceEvent
	// for mibUpload
	mibAuditDelay uint16

	// for mibUpload
	pMibUploadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	// for mibDownload
	pMibDownloadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	//remark: general usage of pAdapterFsm would require generalization of commChan  usage and internal event setting
	//  within the FSM event procedures
	omciMessageReceived chan bool //seperate channel needed by DownloadFsm
}

//OnuDeviceEntry returns a new instance of a OnuDeviceEntry
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOnuDeviceEntry(ctx context.Context, device_id string, kVStoreHost string, kVStorePort int, kvStoreType string, device_Handler *DeviceHandler,
	core_proxy adapterif.CoreProxy, adapter_proxy adapterif.AdapterProxy,
	supported_Fsms_Ptr *OmciDeviceFsms) *OnuDeviceEntry {
	logger.Infow("init-onuDeviceEntry", log.Fields{"deviceId": device_id})
	var onuDeviceEntry OnuDeviceEntry
	onuDeviceEntry.started = false
	onuDeviceEntry.deviceID = device_id
	onuDeviceEntry.baseDeviceHandler = device_Handler
	onuDeviceEntry.coreProxy = core_proxy
	onuDeviceEntry.adapterProxy = adapter_proxy
	onuDeviceEntry.devState = DeviceStatusInit
	//openomciagent.lockDeviceHandlersMap = sync.RWMutex{}
	//OMCI related databases are on a per-agent basis. State machines and tasks
	//are per ONU Vendor
	//
	// MIB Synchronization Database - possible overloading from arguments
	if supported_Fsms_Ptr != nil {
		onuDeviceEntry.supportedFsms = *supported_Fsms_Ptr
	} else {
		//var mibSyncFsm = NewMibSynchronizer()
		// use some internaö defaults, if not defined from outside
		onuDeviceEntry.supportedFsms = OmciDeviceFsms{
			"mib-synchronizer": {
				//mibSyncFsm,        // Implements the MIB synchronization state machine
				onuDeviceEntry.MibDbVolatileDict, // Implements volatile ME MIB database
				true,                             // Advertise events on OpenOMCI event bus
				60,                               // Time to wait between MIB audits.  0 to disable audits.
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
	}
	onuDeviceEntry.mibDbClass = onuDeviceEntry.supportedFsms["mib-synchronizer"].databaseClass
	logger.Debug("access2mibDbClass")
	go onuDeviceEntry.mibDbClass()
	onuDeviceEntry.mibAuditDelay = onuDeviceEntry.supportedFsms["mib-synchronizer"].auditDelay
	logger.Debugw("MibAudit is set to", log.Fields{"Delay": onuDeviceEntry.mibAuditDelay})

	// Omci related Mib upload sync state machine
	mibUploadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibUploadFsm = NewAdapterFsm("MibUpload", device_id, mibUploadChan)
	onuDeviceEntry.pMibUploadFsm.pFsm = fsm.NewFSM(
		"disabled",
		fsm.Events{

			{Name: "start", Src: []string{"disabled"}, Dst: "starting"},

			{Name: "load_mib_template", Src: []string{"starting"}, Dst: "loading_mib_template"},
			{Name: "upload_mib", Src: []string{"loading_mib_template"}, Dst: "uploading"},
			{Name: "examine_mds", Src: []string{"starting"}, Dst: "examining_mds"},

			{Name: "success", Src: []string{"loading_mib_template"}, Dst: "in_sync"},
			{Name: "success", Src: []string{"uploading"}, Dst: "in_sync"},

			{Name: "success", Src: []string{"examining_mds"}, Dst: "in_sync"},
			{Name: "mismatch", Src: []string{"examining_mds"}, Dst: "resynchronizing"},

			{Name: "audit_mib", Src: []string{"in_sync"}, Dst: "auditing"},

			{Name: "success", Src: []string{"out_of_sync"}, Dst: "in_sync"},
			{Name: "audit_mib", Src: []string{"out_of_sync"}, Dst: "auditing"},

			{Name: "success", Src: []string{"auditing"}, Dst: "in_sync"},
			{Name: "mismatch", Src: []string{"auditing"}, Dst: "resynchronizing"},
			{Name: "force_resync", Src: []string{"auditing"}, Dst: "resynchronizing"},

			{Name: "success", Src: []string{"resynchronizing"}, Dst: "in_sync"},
			{Name: "diffs_found", Src: []string{"resynchronizing"}, Dst: "out_of_sync"},

			{Name: "timeout", Src: []string{"loading_mib_template", "uploading", "resynchronizing", "examining_mds", "in_sync", "out_of_sync", "auditing"}, Dst: "starting"},

			{Name: "stop", Src: []string{"starting", "loading_mib_template", "uploading", "resynchronizing", "examining_mds", "in_sync", "out_of_sync", "auditing"}, Dst: "disabled"},
		},

		fsm.Callbacks{
			"enter_state":                func(e *fsm.Event) { onuDeviceEntry.pMibUploadFsm.logFsmStateChange(e) },
			"enter_starting":             func(e *fsm.Event) { onuDeviceEntry.enterStartingState(e) },
			"enter_loading_mib_template": func(e *fsm.Event) { onuDeviceEntry.enterLoadingMibTemplateState(e) },
			"enter_uploading":            func(e *fsm.Event) { onuDeviceEntry.enterUploadingState(e) },
			"enter_examining_mds":        func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsState(e) },
			"enter_resynchronizing":      func(e *fsm.Event) { onuDeviceEntry.enterResynchronizingState(e) },
			"enter_auditing":             func(e *fsm.Event) { onuDeviceEntry.enterAuditingState(e) },
			"enter_out_of_sync":          func(e *fsm.Event) { onuDeviceEntry.enterOutOfSyncState(e) },
			"enter_in_sync":              func(e *fsm.Event) { onuDeviceEntry.enterInSyncState(e) },
		},
	)
	// Omci related Mib download state machine
	mibDownloadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibDownloadFsm = NewAdapterFsm("MibDownload", device_id, mibDownloadChan)
	onuDeviceEntry.pMibDownloadFsm.pFsm = fsm.NewFSM(
		"disabled",
		fsm.Events{

			{Name: "start", Src: []string{"disabled"}, Dst: "starting"},

			{Name: "create_gal", Src: []string{"starting"}, Dst: "creatingGal"},
			{Name: "rx_gal_resp", Src: []string{"creatingGal"}, Dst: "settingOnu2g"},
			{Name: "rx_onu2g_resp", Src: []string{"settingOnu2g"}, Dst: "bridgeInit"},
			// the bridge state is used for multi ME config for alle UNI related ports
			// maybe such could be reflected in the state machine as well (port number parametrized)
			// but that looks not straightforward here - so we keep it simple here for the beginning(?)
			{Name: "rx_bridge_resp", Src: []string{"bridgeInit"}, Dst: "downloaded"},

			{Name: "timeout_simple", Src: []string{"creatingGal", "settingOnu2g"}, Dst: "starting"},
			{Name: "timeout_bridge", Src: []string{"bridgeInit"}, Dst: "starting"},

			{Name: "reset", Src: []string{"starting", "creatingGal", "settingOnu2g",
				"bridgeInit", "downloaded"}, Dst: "resetting"},
			// exceptional treatment for all states except "resetting"
			{Name: "restart", Src: []string{"starting", "creatingGal", "settingOnu2g",
				"bridgeInit", "downloaded", "resetting"}, Dst: "disabled"},
		},

		fsm.Callbacks{
			"enter_state":        func(e *fsm.Event) { onuDeviceEntry.pMibDownloadFsm.logFsmStateChange(e) },
			"enter_starting":     func(e *fsm.Event) { onuDeviceEntry.enterDLStartingState(e) },
			"enter_creatingGal":  func(e *fsm.Event) { onuDeviceEntry.enterCreatingGalState(e) },
			"enter_settingOnu2g": func(e *fsm.Event) { onuDeviceEntry.enterSettingOnu2gState(e) },
			"enter_bridgeInit":   func(e *fsm.Event) { onuDeviceEntry.enterBridgeInitState(e) },
			"enter_downloaded":   func(e *fsm.Event) { onuDeviceEntry.enterDownloadedState(e) },
			"enter_resetting":    func(e *fsm.Event) { onuDeviceEntry.enterResettingState(e) },
		},
	)
	if onuDeviceEntry.pMibDownloadFsm == nil || onuDeviceEntry.pMibDownloadFsm.pFsm == nil {
		logger.Error("MibDownloadFsm could not be instantiated!!")
		// some specifc error treatment - or waiting for crash ???
	}

	onuDeviceEntry.mibTemplateKVStore = onuDeviceEntry.SetKVClient(kvStoreType, kVStoreHost, kVStorePort, BasePathMibTemplateKvStore)
	if onuDeviceEntry.mibTemplateKVStore == nil {
		logger.Error("Failed to setup mibTemplateKVStore")
	}

	// Alarm Synchronization Database
	//self._alarm_db = None
	//self._alarm_database_cls = support_classes['alarm-synchronizer']['database']
	return &onuDeviceEntry
}

//Start starts (logs) the omci agent
func (oo *OnuDeviceEntry) Start(ctx context.Context) error {
	logger.Info("starting-OnuDeviceEntry")

	oo.PDevOmciCC = NewOmciCC(ctx, oo, oo.deviceID, oo.baseDeviceHandler,
		oo.coreProxy, oo.adapterProxy)
	if oo.PDevOmciCC == nil {
		logger.Errorw("Could not create devOmciCc - abort", log.Fields{"for device": oo.deviceID})
		return errors.New("Could not create devOmciCc")
	}

	oo.started = true
	logger.Info("OnuDeviceEntry-started")
	return nil
}

//Stop terminates the session
func (oo *OnuDeviceEntry) Stop(ctx context.Context) error {
	logger.Info("stopping-OnuDeviceEntry")
	oo.started = false
	//oo.exitChannel <- 1
	logger.Info("OnuDeviceEntry-stopped")
	return nil
}

//Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(dev_Event OnuDeviceEvent) error {
	logger.Debugw("relaying system-event", log.Fields{"Event": dev_Event})
	// decouple the handler transfer from further processing here
	// TODO!!! check if really no synch is required within the system e.g. to ensure following steps ..
	if dev_Event == MibDatabaseSync {
		if oo.devState < MibDatabaseSync { //devState has not been synced yet
			oo.devState = MibDatabaseSync
			go oo.baseDeviceHandler.DeviceProcStatusUpdate(dev_Event)
			//TODO!!! device control: next step: start MIB capability verification from here ?!!!
		} else {
			logger.Debugw("mibinsync-event in some already synced state - ignored", log.Fields{"state": oo.devState})
		}
	} else if dev_Event == MibDownloadDone {
		if oo.devState < MibDownloadDone { //devState has not been synced yet
			oo.devState = MibDownloadDone
			go oo.baseDeviceHandler.DeviceProcStatusUpdate(dev_Event)
		} else {
			logger.Debugw("mibdownloaddone-event was already seen - ignored", log.Fields{"state": oo.devState})
		}
	} else {
		logger.Warnw("device-event not yet handled", log.Fields{"state": dev_Event})
	}
	return nil
}
