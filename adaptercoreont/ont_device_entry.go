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

//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"context"
	//"errors"
	//"sync"
	//"time"

	"github.com/looplab/fsm"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

type OnuDeviceEvent int

const (
	// Events of interest to Device Adapters and OpenOMCI State Machines
	DeviceStatusInit     OnuDeviceEvent = 0 // OnuDeviceEntry default start state
	MibDatabaseSync      OnuDeviceEvent = 1 // MIB database sync (upload done)
	OmciCapabilitiesDone OnuDeviceEvent = 2 // OMCI ME and message type capabilities known
	PortLinkUp           OnuDeviceEvent = 3 // Port link state change
	PortLinkDw           OnuDeviceEvent = 4 // Port link state change
	// Add other events here as needed (alarms separate???)
)

type activityDescr struct {
	databaseClass   func() error
	advertiseEvents bool
	auditDelay      int
	//tasks           map[string]func() error
}
type OmciDeviceFsms map[string]activityDescr

//OntDeviceEntry structure holds information about the attached FSM'as and their communication
type OnuDeviceEntry struct {
	deviceID          string
	baseDeviceHandler *DeviceHandler
	coreProxy         adapterif.CoreProxy
	adapterProxy      adapterif.AdapterProxy
	started           bool
	PDevOmciCC        *OmciCC
	//lockDeviceEntries           sync.RWMutex
	mibDbClass    func() error
	supportedFsms OmciDeviceFsms
	MibSyncFsm    *fsm.FSM
	MibSyncChan   chan Message
	devState      OnuDeviceEvent
}

//OnuDeviceEntry returns a new instance of a OnuDeviceEntry
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOnuDeviceEntry(ctx context.Context,
	device_id string, device_Handler *DeviceHandler,
	core_proxy adapterif.CoreProxy, adapter_proxy adapterif.AdapterProxy,
	mib_db func() error, supported_Fsms_Ptr *OmciDeviceFsms) *OnuDeviceEntry {
	log.Infow("init-onuDeviceEntry", log.Fields{"deviceId": device_id})
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
	log.Debug("access2mibDbClass")
	go onuDeviceEntry.mibDbClass()

	// Omci related Mib sync state machine
	onuDeviceEntry.MibSyncFsm = fsm.NewFSM(
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
			"enter_state":                func(e *fsm.Event) { onuDeviceEntry.logStateChange(e) },
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

	// Alarm Synchronization Database
	//self._alarm_db = None
	//self._alarm_database_cls = support_classes['alarm-synchronizer']['database']
	return &onuDeviceEntry
}

//Start starts (logs) the omci agent
func (oo *OnuDeviceEntry) Start(ctx context.Context) error {
	log.Info("starting-OnuDeviceEntry")

	oo.PDevOmciCC = NewOmciCC(ctx, oo, oo.deviceID, oo.baseDeviceHandler,
		oo.coreProxy, oo.adapterProxy)

	//TODO .....
	//mib_db.start()
	oo.started = true
	log.Info("OnuDeviceEntry-started, but not yet mib_db!!!")
	return nil
}

//Stop terminates the session
func (oo *OnuDeviceEntry) Stop(ctx context.Context) error {
	log.Info("stopping-OnuDeviceEntry")
	oo.started = false
	//oo.exitChannel <- 1
	log.Info("OnuDeviceEntry-stopped")
	return nil
}

//Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(dev_Event OnuDeviceEvent) error {
	log.Debugw("relaying system-event", log.Fields{"Event": dev_Event})
	// decouple the handler transfer from further processing here
	// TODO!!! check if really no synch is required within the system e.g. to ensure following steps ..
	if dev_Event == MibDatabaseSync {
		if oo.devState < MibDatabaseSync { //devState has not been synced yet
			oo.devState = MibDatabaseSync
			go oo.baseDeviceHandler.DeviceStateUpdate(dev_Event)
			//TODO!!! device control: next step: start MIB capability verification from here ?!!!
		} else {
			log.Debugw("mib-in-sync-event in some already synced state - ignored", log.Fields{"state": oo.devState})
		}
	} else {
		log.Warnw("device-event not yet handled", log.Fields{"state": dev_Event})
	}
	return nil
}
