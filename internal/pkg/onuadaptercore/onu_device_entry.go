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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"

	//"sync"
	//"time"

	"github.com/looplab/fsm"
	"github.com/opencord/voltha-lib-go/v4/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v4/pkg/db"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"

	//"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

const (
	// events of MibUpload FSM
	ulEvStart              = "ulEvStart"
	ulEvResetMib           = "ulEvResetMib"
	ulEvGetVendorAndSerial = "ulEvGetVendorAndSerial"
	ulEvGetEquipmentID     = "ulEvGetEquipmentId"
	ulEvGetFirstSwVersion  = "ulEvGetFirstSwVersion"
	ulEvGetSecondSwVersion = "ulEvGetSecondSwVersion"
	ulEvGetMacAddress      = "ulEvGetMacAddress"
	ulEvGetMibTemplate     = "ulEvGetMibTemplate"
	ulEvUploadMib          = "ulEvUploadMib"
	ulEvExamineMds         = "ulEvExamineMds"
	ulEvSuccess            = "ulEvSuccess"
	ulEvMismatch           = "ulEvMismatch"
	ulEvAuditMib           = "ulEvAuditMib"
	ulEvForceResync        = "ulEvForceResync"
	ulEvDiffsFound         = "ulEvDiffsFound"
	ulEvTimeout            = "ulEvTimeout"
	ulEvStop               = "ulEvStop"
)
const (
	// states of MibUpload FSM
	ulStDisabled               = "ulStDisabled"
	ulStStarting               = "ulStStarting"
	ulStResettingMib           = "ulStResettingMib"
	ulStGettingVendorAndSerial = "ulStGettingVendorAndSerial"
	ulStGettingEquipmentID     = "ulStGettingEquipmentID"
	ulStGettingFirstSwVersion  = "ulStGettingFirstSwVersion"
	ulStGettingSecondSwVersion = "ulStGettingSecondSwVersion"
	ulStGettingMacAddress      = "ulStGettingMacAddress"
	ulStGettingMibTemplate     = "ulStGettingMibTemplate"
	ulStUploading              = "ulStUploading"
	ulStUploadDone             = "ulStUploadDone"
	ulStInSync                 = "ulStInSync"
	ulStExaminingMds           = "ulStExaminingMds"
	ulStResynchronizing        = "ulStResynchronizing"
	ulStExaminingMdsSuccess    = "ulStExaminingMdsSuccess"
	ulStAuditing               = "ulStAuditing"
	ulStReAuditing             = "ulStReAuditing"
	ulStOutOfSync              = "ulStOutOfSync"
)
const cMibUlFsmIdleState = ulStInSync

const (
	// events of MibDownload FSM
	dlEvStart         = "dlEvStart"
	dlEvCreateGal     = "dlEvCreateGal"
	dlEvRxGalResp     = "dlEvRxGalResp"
	dlEvRxOnu2gResp   = "dlEvRxOnu2gResp"
	dlEvRxBridgeResp  = "dlEvRxBridgeResp"
	dlEvTimeoutSimple = "dlEvTimeoutSimple"
	dlEvTimeoutBridge = "dlEvTimeoutBridge"
	dlEvReset         = "dlEvReset"
	dlEvRestart       = "dlEvRestart"
)
const (
	// states of MibDownload FSM
	dlStDisabled     = "dlStDisabled"
	dlStStarting     = "dlStStarting"
	dlStCreatingGal  = "dlStCreatingGal"
	dlStSettingOnu2g = "dlStSettingOnu2g"
	dlStBridgeInit   = "dlStBridgeInit"
	dlStDownloaded   = "dlStDownloaded"
	dlStResetting    = "dlStResetting"
)
const cMibDlFsmIdleState = dlStDisabled

const (
	// NOTE that this hardcoded to service/voltha as the MIB template is shared across stacks
	cBasePathMibTemplateKvStore = "service/voltha/omci_mibs/go_templates"
	cSuffixMibTemplateKvStore   = "%s/%s/%s"
	cBasePathOnuKVStore         = "%s/openonu"
)

// OnuDeviceEvent - event of interest to Device Adapters and OpenOMCI State Machines
type OnuDeviceEvent int

const (
	// Events of interest to Device Adapters and OpenOMCI State Machines

	// DeviceStatusInit - default start state
	DeviceStatusInit OnuDeviceEvent = iota
	// MibDatabaseSync - MIB database sync (upload done)
	MibDatabaseSync
	// OmciCapabilitiesDone - OMCI ME and message type capabilities known
	OmciCapabilitiesDone
	// MibDownloadDone - // MIB download done
	MibDownloadDone
	// UniLockStateDone - Uni ports admin set to lock
	UniLockStateDone
	// UniUnlockStateDone - Uni ports admin set to unlock
	UniUnlockStateDone
	// UniDisableStateDone - Uni ports admin set to lock based on device disable
	UniDisableStateDone
	// UniEnableStateDone - Uni ports admin set to unlock based on device re-enable
	UniEnableStateDone
	// PortLinkUp - Port link state change
	PortLinkUp
	// PortLinkDw - Port link state change
	PortLinkDw
	// OmciAniConfigDone -  AniSide config according to TechProfile done
	OmciAniConfigDone
	// OmciAniResourceRemoved - AniSide TechProfile related resource (Gem/TCont) removed
	OmciAniResourceRemoved // needs to be the successor of OmciAniConfigDone!
	// OmciVlanFilterAddDone - Omci Vlan config done according to flow-add with request to write kvStore
	OmciVlanFilterAddDone
	// OmciVlanFilterAddDoneNoKvStore - Omci Vlan config done according to flow-add without writing kvStore
	OmciVlanFilterAddDoneNoKvStore // needs to be the successor of OmciVlanFilterAddDone!
	// OmciVlanFilterRemDone - Omci Vlan config done according to flow-remove with request to write kvStore
	OmciVlanFilterRemDone // needs to be the successor of OmciVlanFilterAddDoneNoKvStore!
	// OmciVlanFilterRemDoneNoKvStore - Omci Vlan config done according to flow-remove without writing kvStore
	OmciVlanFilterRemDoneNoKvStore // needs to be the successor of OmciVlanFilterRemDone!
	// OmciOnuSwUpgradeDone - SoftwareUpgrade to ONU finished
	OmciOnuSwUpgradeDone
	// Add other events here as needed (alarms separate???)
)

//AdapterFsm related error string
//error string could be checked on waitforOmciResponse() e.g. to avoid misleading error log
// but not used that way so far (permit error log even for wanted cancellation)
const cErrWaitAborted = "waitResponse aborted"

type activityDescr struct {
	databaseClass func(context.Context) error
	//advertiseEvents bool
	auditInterval time.Duration
	//tasks           map[string]func() error
}

// OmciDeviceFsms - FSM event mapping to database class and time to wait between audits
type OmciDeviceFsms map[string]activityDescr

// AdapterFsm - Adapter FSM details including channel, event and  device
type AdapterFsm struct {
	fsmName  string
	deviceID string
	commChan chan Message
	pFsm     *fsm.FSM
}

//NewAdapterFsm - FSM details including event, device and channel.
func NewAdapterFsm(aName string, aDeviceID string, aCommChannel chan Message) *AdapterFsm {
	aFsm := &AdapterFsm{
		fsmName:  aName,
		deviceID: aDeviceID,
		commChan: aCommChannel,
	}
	return aFsm
}

//Start starts (logs) the omci agent
func (oo *AdapterFsm) logFsmStateChange(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "FSM state change", log.Fields{"device-id": oo.deviceID, "FSM name": oo.fsmName,
		"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst)})
}

//OntDeviceEntry structure holds information about the attached FSM'as and their communication

const (
	firstSwImageMeID  = 0
	secondSwImageMeID = 1
)
const ( //definitions as per G.988 softwareImage::IsCommitted
	swIsUncommitted = 0
	swIsCommitted   = 1
)
const ( //definitions as per G.988 softwareImage::IsActive
	//swIsInactive = 0  not yet used
	swIsActive = 1
)
const onuDataMeID = 0
const onugMeID = 0
const onu2gMeID = 0
const ipHostConfigDataMeID = 1
const onugSerialNumberLen = 8
const omciMacAddressLen = 6

const cEmptyMacAddrString = "000000000000"
const cEmptySerialNumberString = "0000000000000000"

type sEntrySwImageIndication struct {
	valid       bool
	entityID    uint16
	version     string
	isCommitted uint8
}
type sSwImageIndications struct {
	activeEntityEntry   sEntrySwImageIndication
	inactiveEntityEntry sEntrySwImageIndication
}

type uniPersConfig struct {
	PersUniID      uint8               `json:"uni_id"`
	PersTpPathMap  map[uint8]string    `json:"PersTpPathMap"` // tp-id to tp-path map
	PersFlowParams []uniVlanFlowParams `json:"flow_params"`   //as defined in omci_ani_config.go
}

type onuPersistentData struct {
	PersOnuID              uint32          `json:"onu_id"`
	PersIntfID             uint32          `json:"intf_id"`
	PersSerialNumber       string          `json:"serial_number"`
	PersMacAddress         string          `json:"mac_address"`
	PersVendorID           string          `json:"vendor_id"`
	PersEquipmentID        string          `json:"equipment_id"`
	PersActiveSwVersion    string          `json:"active_sw_version"`
	PersAdminState         string          `json:"admin_state"`
	PersOperState          string          `json:"oper_state"`
	PersUniUnlockDone      bool            `json:"uni_unlock_done"`
	PersUniDisableDone     bool            `json:"uni_disable_done"`
	PersMibAuditInterval   time.Duration   `json:"mib_audit_interval"`
	PersMibLastDbSync      uint32          `json:"mib_last_db_sync"`
	PersMibDataSyncAdpt    uint8           `json:"mib_data_sync_adpt"`
	PersUniConfig          []uniPersConfig `json:"uni_config"`
	PersAlarmAuditInterval time.Duration   `json:"alarm_audit_interval"`
}

// OnuDeviceEntry - ONU device info and FSM events.
type OnuDeviceEntry struct {
	deviceID              string
	baseDeviceHandler     *deviceHandler
	pOpenOnuAc            *OpenONUAC
	coreProxy             adapterif.CoreProxy
	adapterProxy          adapterif.AdapterProxy
	PDevOmciCC            *omciCC
	pOnuDB                *onuDeviceDB
	mibTemplateKVStore    *db.Backend
	persUniConfigMutex    sync.RWMutex
	sOnuPersistentData    onuPersistentData
	mibTemplatePath       string
	onuKVStoreMutex       sync.RWMutex
	onuKVStore            *db.Backend
	onuKVStorePath        string
	onuKVStoreprocResult  error //error indication of processing
	chOnuKvProcessingStep chan uint8
	onuSwImageIndications sSwImageIndications
	//lockDeviceEntries           sync.RWMutex
	mibDbClass    func(context.Context) error
	supportedFsms OmciDeviceFsms
	devState      OnuDeviceEvent
	// Audit and MDS
	mibAuditInterval   time.Duration
	alarmAuditInterval time.Duration
	// TODO: periodical mib resync will be implemented with story VOL-3792
	//mibNextDbResync uint32

	// for mibUpload
	pMibUploadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	// for mibDownload
	pMibDownloadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	//remark: general usage of pAdapterFsm would require generalization of commChan  usage and internal event setting
	//  within the FSM event procedures
	omciMessageReceived              chan bool    //seperate channel needed by DownloadFsm
	omciRebootMessageReceivedChannel chan Message // channel needed by Reboot request
}

//newOnuDeviceEntry returns a new instance of a OnuDeviceEntry
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newOnuDeviceEntry(ctx context.Context, dh *deviceHandler) *OnuDeviceEntry {
	logger.Debugw(ctx, "init-onuDeviceEntry", log.Fields{"device-id": dh.deviceID})
	var onuDeviceEntry OnuDeviceEntry
	onuDeviceEntry.deviceID = dh.deviceID
	onuDeviceEntry.baseDeviceHandler = dh
	onuDeviceEntry.pOpenOnuAc = dh.pOpenOnuAc
	onuDeviceEntry.coreProxy = dh.coreProxy
	onuDeviceEntry.adapterProxy = dh.AdapterProxy
	onuDeviceEntry.devState = DeviceStatusInit
	onuDeviceEntry.sOnuPersistentData.PersUniConfig = make([]uniPersConfig, 0)
	onuDeviceEntry.chOnuKvProcessingStep = make(chan uint8)
	onuDeviceEntry.omciRebootMessageReceivedChannel = make(chan Message, 2048)
	//openomciagent.lockDeviceHandlersMap = sync.RWMutex{}
	//OMCI related databases are on a per-agent basis. State machines and tasks
	//are per ONU Vendor
	//
	// MIB Synchronization Database - possible overloading from arguments
	if dh.pOpenOnuAc.pSupportedFsms != nil {
		onuDeviceEntry.supportedFsms = *dh.pOpenOnuAc.pSupportedFsms
	} else {
		// This branch is currently not used and is for potential future usage of alternative MIB Sync FSMs only!
		//var mibSyncFsm = NewMibSynchronizer()
		// use some internal defaults, if not defined from outside
		onuDeviceEntry.supportedFsms = OmciDeviceFsms{
			"mib-synchronizer": {
				//mibSyncFsm,        // Implements the MIB synchronization state machine
				onuDeviceEntry.mibDbVolatileDict, // Implements volatile ME MIB database
				//true,                             // Advertise events on OpenOMCI event bus
				dh.pOpenOnuAc.mibAuditInterval, // Time to wait between MIB audits.  0 to disable audits.
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
	logger.Debug(ctx, "access2mibDbClass")
	go onuDeviceEntry.mibDbClass(ctx)
	if !dh.isReconciling() {
		onuDeviceEntry.mibAuditInterval = onuDeviceEntry.supportedFsms["mib-synchronizer"].auditInterval
		onuDeviceEntry.sOnuPersistentData.PersMibAuditInterval = onuDeviceEntry.mibAuditInterval
		onuDeviceEntry.alarmAuditInterval = dh.pOpenOnuAc.alarmAuditInterval
		onuDeviceEntry.sOnuPersistentData.PersAlarmAuditInterval = onuDeviceEntry.alarmAuditInterval
	} else {
		logger.Debugw(ctx, "reconciling - take audit interval from persistent data", log.Fields{"device-id": dh.deviceID})
		// TODO: This is a preparation for VOL-VOL-3811 to preserve config history in case of
		// vendor- or deviceID-specific configurations via voltctl-commands
		onuDeviceEntry.mibAuditInterval = onuDeviceEntry.sOnuPersistentData.PersMibAuditInterval
		onuDeviceEntry.alarmAuditInterval = onuDeviceEntry.sOnuPersistentData.PersAlarmAuditInterval
	}
	logger.Debugw(ctx, "MibAuditInterval and AlarmAuditInterval is set to", log.Fields{"mib-audit-interval": onuDeviceEntry.mibAuditInterval,
		"alarm-audit-interval": onuDeviceEntry.alarmAuditInterval})
	// TODO: periodical mib resync will be implemented with story VOL-3792
	//onuDeviceEntry.mibNextDbResync = 0

	// Omci related Mib upload sync state machine
	mibUploadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibUploadFsm = NewAdapterFsm("MibUpload", dh.deviceID, mibUploadChan)
	onuDeviceEntry.pMibUploadFsm.pFsm = fsm.NewFSM(
		ulStDisabled,
		fsm.Events{

			{Name: ulEvStart, Src: []string{ulStDisabled}, Dst: ulStStarting},

			{Name: ulEvResetMib, Src: []string{ulStStarting}, Dst: ulStResettingMib},
			{Name: ulEvGetVendorAndSerial, Src: []string{ulStResettingMib}, Dst: ulStGettingVendorAndSerial},
			{Name: ulEvGetEquipmentID, Src: []string{ulStGettingVendorAndSerial}, Dst: ulStGettingEquipmentID},
			{Name: ulEvGetFirstSwVersion, Src: []string{ulStGettingEquipmentID}, Dst: ulStGettingFirstSwVersion},
			{Name: ulEvGetSecondSwVersion, Src: []string{ulStGettingFirstSwVersion}, Dst: ulStGettingSecondSwVersion},
			{Name: ulEvGetMacAddress, Src: []string{ulStGettingSecondSwVersion}, Dst: ulStGettingMacAddress},
			{Name: ulEvGetMibTemplate, Src: []string{ulStGettingMacAddress}, Dst: ulStGettingMibTemplate},

			{Name: ulEvUploadMib, Src: []string{ulStGettingMibTemplate}, Dst: ulStUploading},
			{Name: ulEvExamineMds, Src: []string{ulStStarting}, Dst: ulStExaminingMds},

			{Name: ulEvSuccess, Src: []string{ulStGettingMibTemplate}, Dst: ulStUploadDone},
			{Name: ulEvSuccess, Src: []string{ulStUploading}, Dst: ulStUploadDone},

			{Name: ulEvSuccess, Src: []string{ulStUploadDone}, Dst: ulStInSync},
			//{Name: ulEvSuccess, Src: []string{ulStExaminingMds}, Dst: ulStInSync},
			{Name: ulEvSuccess, Src: []string{ulStExaminingMds}, Dst: ulStExaminingMdsSuccess},
			// TODO: As long as mib-resynchronizing is not implemented, failed MDS-examination triggers
			// mib-reset and new provisioning at this point
			//{Name: ulEvMismatch, Src: []string{ulStExaminingMds}, Dst: ulStResynchronizing},
			{Name: ulEvMismatch, Src: []string{ulStExaminingMds}, Dst: ulStResettingMib},

			{Name: ulEvSuccess, Src: []string{ulStExaminingMdsSuccess}, Dst: ulStInSync},
			{Name: ulEvMismatch, Src: []string{ulStExaminingMdsSuccess}, Dst: ulStResettingMib},

			{Name: ulEvAuditMib, Src: []string{ulStInSync}, Dst: ulStAuditing},

			{Name: ulEvSuccess, Src: []string{ulStOutOfSync}, Dst: ulStInSync},
			{Name: ulEvAuditMib, Src: []string{ulStOutOfSync}, Dst: ulStAuditing},

			{Name: ulEvSuccess, Src: []string{ulStAuditing}, Dst: ulStInSync},
			{Name: ulEvMismatch, Src: []string{ulStAuditing}, Dst: ulStReAuditing},
			{Name: ulEvForceResync, Src: []string{ulStAuditing}, Dst: ulStResynchronizing},

			{Name: ulEvSuccess, Src: []string{ulStReAuditing}, Dst: ulStInSync},
			{Name: ulEvMismatch, Src: []string{ulStReAuditing}, Dst: ulStResettingMib},

			{Name: ulEvSuccess, Src: []string{ulStResynchronizing}, Dst: ulStInSync},
			{Name: ulEvDiffsFound, Src: []string{ulStResynchronizing}, Dst: ulStOutOfSync},

			{Name: ulEvTimeout, Src: []string{ulStResettingMib, ulStGettingVendorAndSerial, ulStGettingEquipmentID, ulStGettingFirstSwVersion,
				ulStGettingSecondSwVersion, ulStGettingMacAddress, ulStGettingMibTemplate, ulStUploading, ulStResynchronizing, ulStExaminingMds,
				ulStUploadDone, ulStInSync, ulStOutOfSync, ulStAuditing, ulStReAuditing}, Dst: ulStStarting},

			{Name: ulEvStop, Src: []string{ulStStarting, ulStResettingMib, ulStGettingVendorAndSerial, ulStGettingEquipmentID, ulStGettingFirstSwVersion,
				ulStGettingSecondSwVersion, ulStGettingMacAddress, ulStGettingMibTemplate, ulStUploading, ulStResynchronizing, ulStExaminingMds,
				ulStUploadDone, ulStInSync, ulStOutOfSync, ulStAuditing, ulStReAuditing}, Dst: ulStDisabled},
		},

		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { onuDeviceEntry.pMibUploadFsm.logFsmStateChange(ctx, e) },
			"enter_" + ulStStarting:               func(e *fsm.Event) { onuDeviceEntry.enterStartingState(ctx, e) },
			"enter_" + ulStResettingMib:           func(e *fsm.Event) { onuDeviceEntry.enterResettingMibState(ctx, e) },
			"enter_" + ulStGettingVendorAndSerial: func(e *fsm.Event) { onuDeviceEntry.enterGettingVendorAndSerialState(ctx, e) },
			"enter_" + ulStGettingEquipmentID:     func(e *fsm.Event) { onuDeviceEntry.enterGettingEquipmentIDState(ctx, e) },
			"enter_" + ulStGettingFirstSwVersion:  func(e *fsm.Event) { onuDeviceEntry.enterGettingFirstSwVersionState(ctx, e) },
			"enter_" + ulStGettingSecondSwVersion: func(e *fsm.Event) { onuDeviceEntry.enterGettingSecondSwVersionState(ctx, e) },
			"enter_" + ulStGettingMacAddress:      func(e *fsm.Event) { onuDeviceEntry.enterGettingMacAddressState(ctx, e) },
			"enter_" + ulStGettingMibTemplate:     func(e *fsm.Event) { onuDeviceEntry.enterGettingMibTemplateState(ctx, e) },
			"enter_" + ulStUploading:              func(e *fsm.Event) { onuDeviceEntry.enterUploadingState(ctx, e) },
			"enter_" + ulStUploadDone:             func(e *fsm.Event) { onuDeviceEntry.enterUploadDoneState(ctx, e) },
			"enter_" + ulStExaminingMds:           func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsState(ctx, e) },
			"enter_" + ulStResynchronizing:        func(e *fsm.Event) { onuDeviceEntry.enterResynchronizingState(ctx, e) },
			"enter_" + ulStExaminingMdsSuccess:    func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsSuccessState(ctx, e) },
			"enter_" + ulStAuditing:               func(e *fsm.Event) { onuDeviceEntry.enterAuditingState(ctx, e) },
			"enter_" + ulStReAuditing:             func(e *fsm.Event) { onuDeviceEntry.enterReAuditingState(ctx, e) },
			"enter_" + ulStOutOfSync:              func(e *fsm.Event) { onuDeviceEntry.enterOutOfSyncState(ctx, e) },
			"enter_" + ulStInSync:                 func(e *fsm.Event) { onuDeviceEntry.enterInSyncState(ctx, e) },
		},
	)
	// Omci related Mib download state machine
	mibDownloadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibDownloadFsm = NewAdapterFsm("MibDownload", dh.deviceID, mibDownloadChan)
	onuDeviceEntry.pMibDownloadFsm.pFsm = fsm.NewFSM(
		dlStDisabled,
		fsm.Events{

			{Name: dlEvStart, Src: []string{dlStDisabled}, Dst: dlStStarting},

			{Name: dlEvCreateGal, Src: []string{dlStStarting}, Dst: dlStCreatingGal},
			{Name: dlEvRxGalResp, Src: []string{dlStCreatingGal}, Dst: dlStSettingOnu2g},
			{Name: dlEvRxOnu2gResp, Src: []string{dlStSettingOnu2g}, Dst: dlStBridgeInit},
			// the bridge state is used for multi ME config for alle UNI related ports
			// maybe such could be reflected in the state machine as well (port number parametrized)
			// but that looks not straightforward here - so we keep it simple here for the beginning(?)
			{Name: dlEvRxBridgeResp, Src: []string{dlStBridgeInit}, Dst: dlStDownloaded},

			{Name: dlEvTimeoutSimple, Src: []string{dlStCreatingGal, dlStSettingOnu2g}, Dst: dlStStarting},
			{Name: dlEvTimeoutBridge, Src: []string{dlStBridgeInit}, Dst: dlStStarting},

			{Name: dlEvReset, Src: []string{dlStStarting, dlStCreatingGal, dlStSettingOnu2g,
				dlStBridgeInit, dlStDownloaded}, Dst: dlStResetting},
			// exceptional treatment for all states except dlStResetting
			{Name: dlEvRestart, Src: []string{dlStStarting, dlStCreatingGal, dlStSettingOnu2g,
				dlStBridgeInit, dlStDownloaded, dlStResetting}, Dst: dlStDisabled},
		},

		fsm.Callbacks{
			"enter_state":               func(e *fsm.Event) { onuDeviceEntry.pMibDownloadFsm.logFsmStateChange(ctx, e) },
			"enter_" + dlStStarting:     func(e *fsm.Event) { onuDeviceEntry.enterDLStartingState(ctx, e) },
			"enter_" + dlStCreatingGal:  func(e *fsm.Event) { onuDeviceEntry.enterCreatingGalState(ctx, e) },
			"enter_" + dlStSettingOnu2g: func(e *fsm.Event) { onuDeviceEntry.enterSettingOnu2gState(ctx, e) },
			"enter_" + dlStBridgeInit:   func(e *fsm.Event) { onuDeviceEntry.enterBridgeInitState(ctx, e) },
			"enter_" + dlStDownloaded:   func(e *fsm.Event) { onuDeviceEntry.enterDownloadedState(ctx, e) },
			"enter_" + dlStResetting:    func(e *fsm.Event) { onuDeviceEntry.enterResettingState(ctx, e) },
		},
	)
	if onuDeviceEntry.pMibDownloadFsm == nil || onuDeviceEntry.pMibDownloadFsm.pFsm == nil {
		logger.Errorw(ctx, "MibDownloadFsm could not be instantiated", log.Fields{"device-id": dh.deviceID})
		// TODO some specific error treatment - or waiting for crash ?
	}

	onuDeviceEntry.mibTemplateKVStore = onuDeviceEntry.baseDeviceHandler.setBackend(ctx, cBasePathMibTemplateKvStore)
	if onuDeviceEntry.mibTemplateKVStore == nil {
		logger.Errorw(ctx, "Can't access mibTemplateKVStore - no backend connection to service",
			log.Fields{"device-id": dh.deviceID, "service": cBasePathMibTemplateKvStore})
	}

	onuDeviceEntry.onuKVStorePath = onuDeviceEntry.deviceID
	baseKvStorePath := fmt.Sprintf(cBasePathOnuKVStore, dh.pOpenOnuAc.cm.Backend.PathPrefix)
	onuDeviceEntry.onuKVStore = onuDeviceEntry.baseDeviceHandler.setBackend(ctx, baseKvStorePath)
	if onuDeviceEntry.onuKVStore == nil {
		logger.Errorw(ctx, "Can't access onuKVStore - no backend connection to service",
			log.Fields{"device-id": dh.deviceID, "service": baseKvStorePath})
	}

	// Alarm Synchronization Database

	//self._alarm_db = None
	//self._alarm_database_cls = support_classes['alarm-synchronizer']['database']
	return &onuDeviceEntry
}

//start starts (logs) the omci agent
func (oo *OnuDeviceEntry) start(ctx context.Context) error {
	logger.Debugw(ctx, "OnuDeviceEntry-starting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC == nil {
		oo.PDevOmciCC = newOmciCC(ctx, oo, oo.deviceID, oo.baseDeviceHandler,
			oo.coreProxy, oo.adapterProxy)
		if oo.PDevOmciCC == nil {
			logger.Errorw(ctx, "Could not create devOmciCc - abort", log.Fields{"for device-id": oo.deviceID})
			return fmt.Errorf("could not create devOmciCc %s", oo.deviceID)
		}
	}
	return nil
}

//stop stops/resets the omciCC
func (oo *OnuDeviceEntry) stop(ctx context.Context, abResetOmciCC bool) error {
	logger.Debugw(ctx, "OnuDeviceEntry-stopping", log.Fields{"for device-id": oo.deviceID})
	if abResetOmciCC && (oo.PDevOmciCC != nil) {
		_ = oo.PDevOmciCC.stop(ctx)
	}
	//to allow for all event notifications again when re-using the device and omciCC
	oo.devState = DeviceStatusInit
	return nil
}

func (oo *OnuDeviceEntry) reboot(ctx context.Context) error {
	logger.Debugw(ctx, "OnuDeviceEntry-rebooting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC != nil {
		if err := oo.PDevOmciCC.sendReboot(ctx, oo.pOpenOnuAc.omciTimeout, true, oo.omciRebootMessageReceivedChannel); err != nil {
			logger.Errorw(ctx, "onu didn't reboot", log.Fields{"for device-id": oo.deviceID})
			return err
		}
	}
	return nil
}

func (oo *OnuDeviceEntry) waitForRebootResponse(ctx context.Context, responseChannel chan Message) error {
	select {
	case <-time.After(3 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "Reboot timeout", log.Fields{"for device-id": oo.deviceID})
		return fmt.Errorf("rebootTimeout")
	case data := <-responseChannel:
		switch data.Data.(OmciMessage).OmciMsg.MessageType {
		case omci.RebootResponseType:
			{
				msgLayer := (*data.Data.(OmciMessage).OmciPacket).Layer(omci.LayerTypeRebootResponse)
				if msgLayer == nil {
					return fmt.Errorf("omci Msg layer could not be detected for RebootResponseType")
				}
				msgObj, msgOk := msgLayer.(*omci.RebootResponse)
				if !msgOk {
					return fmt.Errorf("omci Msg layer could not be assigned for RebootResponseType %s", oo.deviceID)
				}
				logger.Debugw(ctx, "RebootResponse data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				if msgObj.Result != me.Success {
					logger.Errorw(ctx, "Omci RebootResponse result error", log.Fields{"device-id": oo.deviceID, "Error": msgObj.Result})
					// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
					return fmt.Errorf("omci RebootResponse result error indication %s for device %s",
						msgObj.Result, oo.deviceID)
				}
				return nil
			}
		}
		logger.Warnw(ctx, "Reboot response message type error", log.Fields{"for device-id": oo.deviceID})
		return fmt.Errorf("unexpected OmciResponse type received %s", oo.deviceID)
	}
}

//Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(ctx context.Context, devEvent OnuDeviceEvent) {
	logger.Debugw(ctx, "relaying system-event", log.Fields{"Event": devEvent})
	// decouple the handler transfer from further processing here
	// TODO!!! check if really no synch is required within the system e.g. to ensure following steps ..
	if devEvent == MibDatabaseSync {
		if oo.devState < MibDatabaseSync { //devState has not been synced yet
			oo.devState = MibDatabaseSync
			go oo.baseDeviceHandler.deviceProcStatusUpdate(ctx, devEvent)
			//TODO!!! device control: next step: start MIB capability verification from here ?!!!
		} else {
			logger.Debugw(ctx, "mibinsync-event in some already synced state - ignored", log.Fields{"state": oo.devState})
		}
	} else if devEvent == MibDownloadDone {
		if oo.devState < MibDownloadDone { //devState has not been synced yet
			oo.devState = MibDownloadDone
			go oo.baseDeviceHandler.deviceProcStatusUpdate(ctx, devEvent)
		} else {
			logger.Debugw(ctx, "mibdownloaddone-event was already seen - ignored", log.Fields{"state": oo.devState})
		}
	} else {
		logger.Warnw(ctx, "device-event not yet handled", log.Fields{"state": devEvent})
	}
}

func (oo *OnuDeviceEntry) restoreDataFromOnuKvStore(ctx context.Context) error {
	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf(fmt.Sprintf("onuKVStore-not-set-abort-%s", oo.deviceID))
	}
	oo.persUniConfigMutex.Lock()
	defer oo.persUniConfigMutex.Unlock()
	oo.sOnuPersistentData =
		onuPersistentData{0, 0, "", "", "", "", "", "", "", false, false, oo.mibAuditInterval, 0, 0, make([]uniPersConfig, 0), oo.alarmAuditInterval}
	oo.onuKVStoreMutex.RLock()
	Value, err := oo.onuKVStore.Get(ctx, oo.onuKVStorePath)
	oo.onuKVStoreMutex.RUnlock()
	if err == nil {
		if Value != nil {
			logger.Debugw(ctx, "ONU-data read",
				log.Fields{"Key": Value.Key, "device-id": oo.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, &oo.sOnuPersistentData); err != nil {
				logger.Errorw(ctx, "unable to unmarshal ONU-data", log.Fields{"error": err, "device-id": oo.deviceID})
				return fmt.Errorf(fmt.Sprintf("unable-to-unmarshal-ONU-data-%s", oo.deviceID))
			}
			logger.Debugw(ctx, "ONU-data", log.Fields{"sOnuPersistentData": oo.sOnuPersistentData,
				"device-id": oo.deviceID})
		} else {
			logger.Debugw(ctx, "no ONU-data found", log.Fields{"path": oo.onuKVStorePath, "device-id": oo.deviceID})
			return fmt.Errorf("no-ONU-data-found")
		}
	} else {
		logger.Errorw(ctx, "unable to read from KVstore", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf(fmt.Sprintf("unable-to-read-from-KVstore-%s", oo.deviceID))
	}
	return nil
}

func (oo *OnuDeviceEntry) deleteDataFromOnuKvStore(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data delete aborted: onuKVStore not set")
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chOnuKvProcessingStep
	go oo.deletePersistentData(ctx, processingStep)
	if !oo.waitForTimeoutOrCompletion(ctx, oo.chOnuKvProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw(ctx, "ONU-data not deleted - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data delete aborted: during kv-access")
		return
	}
}

func (oo *OnuDeviceEntry) deletePersistentData(ctx context.Context, aProcessingStep uint8) {

	logger.Debugw(ctx, "delete and clear internal persistency data", log.Fields{"device-id": oo.deviceID})

	oo.persUniConfigMutex.Lock()
	defer oo.persUniConfigMutex.Unlock()

	oo.sOnuPersistentData.PersUniConfig = nil //releasing all UniConfig entries to garbage collector default entry
	oo.sOnuPersistentData =
		onuPersistentData{0, 0, "", "", "", "", "", "", "", false, false, oo.mibAuditInterval, 0, 0, make([]uniPersConfig, 0), oo.alarmAuditInterval}
	logger.Debugw(ctx, "delete ONU-data from KVStore", log.Fields{"device-id": oo.deviceID})
	oo.onuKVStoreMutex.Lock()
	err := oo.onuKVStore.Delete(ctx, oo.onuKVStorePath)
	oo.onuKVStoreMutex.Unlock()
	if err != nil {
		logger.Errorw(ctx, "unable to delete in KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	oo.chOnuKvProcessingStep <- aProcessingStep //done
}

func (oo *OnuDeviceEntry) updateOnuKvStore(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data update aborted: onuKVStore not set")
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chOnuKvProcessingStep
	go oo.storeDataInOnuKvStore(ctx, processingStep)
	if !oo.waitForTimeoutOrCompletion(ctx, oo.chOnuKvProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw(ctx, "ONU-data not written - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data update aborted: during writing process")
		return
	}
}

func (oo *OnuDeviceEntry) storeDataInOnuKvStore(ctx context.Context, aProcessingStep uint8) {

	//assign values which are not already present when newOnuDeviceEntry() is called
	oo.sOnuPersistentData.PersOnuID = oo.baseDeviceHandler.pOnuIndication.OnuId
	oo.sOnuPersistentData.PersIntfID = oo.baseDeviceHandler.pOnuIndication.IntfId
	//TODO: verify usage of these values during restart UC
	oo.sOnuPersistentData.PersAdminState = oo.baseDeviceHandler.pOnuIndication.AdminState
	oo.sOnuPersistentData.PersOperState = oo.baseDeviceHandler.pOnuIndication.OperState

	oo.persUniConfigMutex.RLock()
	defer oo.persUniConfigMutex.RUnlock()
	logger.Debugw(ctx, "Update ONU-data in KVStore", log.Fields{"device-id": oo.deviceID, "sOnuPersistentData": oo.sOnuPersistentData})

	Value, err := json.Marshal(oo.sOnuPersistentData)
	if err != nil {
		logger.Errorw(ctx, "unable to marshal ONU-data", log.Fields{"sOnuPersistentData": oo.sOnuPersistentData,
			"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	oo.onuKVStoreMutex.Lock()
	err = oo.onuKVStore.Put(ctx, oo.onuKVStorePath, Value)
	oo.onuKVStoreMutex.Unlock()
	if err != nil {
		logger.Errorw(ctx, "unable to write ONU-data into KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	oo.chOnuKvProcessingStep <- aProcessingStep //done
}

func (oo *OnuDeviceEntry) updateOnuUniTpPath(ctx context.Context, aUniID uint8, aTpID uint8, aPathString string) bool {
	/* within some specific InterAdapter processing request write/read access to data is ensured to be sequentially,
	   as also the complete sequence is ensured to 'run to completion' before some new request is accepted
	   no specific concurrency protection to sOnuPersistentData is required here
	*/
	oo.persUniConfigMutex.Lock()
	defer oo.persUniConfigMutex.Unlock()

	for k, v := range oo.sOnuPersistentData.PersUniConfig {
		if v.PersUniID == aUniID {
			logger.Debugw(ctx, "PersUniConfig-entry already exists", log.Fields{"device-id": oo.deviceID, "uniID": aUniID})
			existingPath, ok := oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID]
			if !ok {
				logger.Debugw(ctx, "tp-does-not-exist--to-be-created-afresh", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "tpID": aTpID, "path": aPathString})
			}
			if existingPath != aPathString {
				if aPathString == "" {
					//existing entry to be deleted
					logger.Debugw(ctx, "UniTp delete path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID] = ""
				} else {
					//existing entry to be modified
					logger.Debugw(ctx, "UniTp modify path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID] = aPathString
				}
				return true
			}
			//entry already exists
			if aPathString == "" {
				//no active TechProfile
				logger.Debugw(ctx, "UniTp path has already been removed - no AniSide config to be removed", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID})
				// attention 201105: this block is at the moment entered for each of subsequent GemPortDeletes and TContDelete
				//   as the path is already cleared with the first GemPort - this will probably change with the upcoming real
				//   TechProfile removal (still TODO), but anyway the reasonUpdate initiated here should not harm overall behavior
				go oo.baseDeviceHandler.deviceProcStatusUpdate(ctx, OmciAniResourceRemoved)
				// no flow config pending on 'remove' so far
			} else {
				//the given TechProfile already exists and is assumed to be active - update devReason as if the config has been done here
				//was needed e.g. in voltha POD Tests:Validate authentication on a disabled ONU
				//  (as here the TechProfile has not been removed with the disable-device before the new enable-device)
				logger.Debugw(ctx, "UniTp path already exists - TechProfile supposed to be active", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
				//no deviceReason update (deviceProcStatusUpdate) here to ensure 'omci_flows_pushed' state within disable/enable procedure of ATT scenario
				//  (during which the flows are removed/re-assigned but the techProf is left active)
				//and as the TechProfile is regarded as active we have to verify, if some flow configuration still waits on it
				//  (should not be the case, but should not harm or be more robust ...)
				// and to be sure, that for some reason the corresponding TpDelete was lost somewhere in history
				//  we also reset a possibly outstanding delete request - repeated TpConfig is regarded as valid for waiting flow config
				if oo.baseDeviceHandler.pOnuTP != nil {
					oo.baseDeviceHandler.pOnuTP.setProfileToDelete(aUniID, aTpID, false)
				}
				go oo.baseDeviceHandler.VerifyVlanConfigRequest(ctx, aUniID, aTpID)
			}
			return false //indicate 'no change' - nothing more to do, TechProf inter-adapter message is return with success anyway here
		}
	}
	//no entry exists for uniId

	if aPathString == "" {
		//delete request in non-existing state , accept as no change
		logger.Debugw(ctx, "UniTp path already removed", log.Fields{"device-id": oo.deviceID, "uniID": aUniID})
		return false
	}
	//new entry to be created
	logger.Debugw(ctx, "New UniTp path set", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
	perSubTpPathMap := make(map[uint8]string)
	perSubTpPathMap[aTpID] = aPathString
	oo.sOnuPersistentData.PersUniConfig =
		append(oo.sOnuPersistentData.PersUniConfig, uniPersConfig{PersUniID: aUniID, PersTpPathMap: perSubTpPathMap, PersFlowParams: make([]uniVlanFlowParams, 0)})
	return true
}

func (oo *OnuDeviceEntry) updateOnuUniFlowConfig(aUniID uint8, aUniVlanFlowParams *[]uniVlanFlowParams) {

	oo.persUniConfigMutex.Lock()
	defer oo.persUniConfigMutex.Unlock()

	for k, v := range oo.sOnuPersistentData.PersUniConfig {
		if v.PersUniID == aUniID {
			oo.sOnuPersistentData.PersUniConfig[k].PersFlowParams = make([]uniVlanFlowParams, len(*aUniVlanFlowParams))
			copy(oo.sOnuPersistentData.PersUniConfig[k].PersFlowParams, *aUniVlanFlowParams)
			return
		}
	}
	//flow update was faster than tp-config - create PersUniConfig-entry
	tmpConfig := uniPersConfig{PersUniID: aUniID, PersTpPathMap: make(map[uint8]string), PersFlowParams: make([]uniVlanFlowParams, len(*aUniVlanFlowParams))}
	copy(tmpConfig.PersFlowParams, *aUniVlanFlowParams)
	oo.sOnuPersistentData.PersUniConfig = append(oo.sOnuPersistentData.PersUniConfig, tmpConfig)
}

func (oo *OnuDeviceEntry) waitForTimeoutOrCompletion(
	ctx context.Context, aChOnuProcessingStep <-chan uint8, aProcessingStep uint8) bool {
	select {
	case <-ctx.Done():
		logger.Warnw(ctx, "processing not completed in-time!",
			log.Fields{"device-id": oo.deviceID, "error": ctx.Err()})
		return false
	case rxStep := <-aChOnuProcessingStep:
		if rxStep == aProcessingStep {
			return true
		}
		//all other values are not accepted - including 0 for error indication
		logger.Warnw(ctx, "Invalid processing step received: abort!",
			log.Fields{"device-id": oo.deviceID,
				"wantedStep": aProcessingStep, "haveStep": rxStep})
		return false
	}
}

func (oo *OnuDeviceEntry) resetKvProcessingErrorIndication() {
	oo.onuKVStoreprocResult = nil
}
func (oo *OnuDeviceEntry) getKvProcessingErrorIndication() error {
	return oo.onuKVStoreprocResult
}
func (oo *OnuDeviceEntry) incrementMibDataSync(ctx context.Context) {
	if oo.sOnuPersistentData.PersMibDataSyncAdpt < 255 {
		oo.sOnuPersistentData.PersMibDataSyncAdpt++
	} else {
		// per G.984 and G.988 overflow starts over at 1 given 0 is reserved for reset
		oo.sOnuPersistentData.PersMibDataSyncAdpt = 1
	}
	logger.Debugf(ctx, "mibDataSync updated - mds: %d - device-id: %s", oo.sOnuPersistentData.PersMibDataSyncAdpt, oo.deviceID)
}

func (oo *OnuDeviceEntry) buildMibTemplatePath() string {
	return fmt.Sprintf(cSuffixMibTemplateKvStore, oo.sOnuPersistentData.PersVendorID, oo.sOnuPersistentData.PersEquipmentID, oo.sOnuPersistentData.PersActiveSwVersion)
}
