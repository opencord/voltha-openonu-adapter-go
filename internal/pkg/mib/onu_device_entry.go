/*
 * Copyright 2020-2024 Open Networking Foundation (ONF) and the ONF Contributors
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

// Package mib provides the utilities for managing the onu mib
package mib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/db"
	"github.com/opencord/voltha-lib-go/v7/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v7/pkg/events/eventif"
	vgrpc "github.com/opencord/voltha-lib-go/v7/pkg/grpc"
	"github.com/opencord/voltha-protos/v5/go/inter_adapter"
	"github.com/opencord/voltha-protos/v5/go/voltha"

	"github.com/opencord/voltha-lib-go/v7/pkg/log"

	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	devdb "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/swupg"
)

// events of MibUpload FSM
const (
	UlEvStart              = "UlEvStart"
	UlEvResetMib           = "UlEvResetMib"
	UlEvGetVendorAndSerial = "UlEvGetVendorAndSerial"
	UlEvGetVersion         = "UlEvGetVersion"
	UlEvGetEquipIDAndOmcc  = "UlEvGetEquipIDAndOmcc"
	UlEvTestExtOmciSupport = "UlEvTestExtOmciSupport"
	UlEvGetFirstSwVersion  = "UlEvGetFirstSwVersion"
	UlEvGetSecondSwVersion = "UlEvGetSecondSwVersion"
	UlEvGetMacAddress      = "UlEvGetMacAddress"
	UlEvGetMibTemplate     = "UlEvGetMibTemplate"
	UlEvUploadMib          = "UlEvUploadMib"
	UlEvVerifyAndStoreTPs  = "UlEvVerifyAndStoreTPs"
	UlEvExamineMds         = "UlEvExamineMds"
	UlEvSuccess            = "UlEvSuccess"
	UlEvMismatch           = "UlEvMismatch"
	UlEvAuditMib           = "UlEvAuditMib"
	UlEvForceResync        = "UlEvForceResync"
	UlEvDiffsFound         = "UlEvDiffsFound"
	UlEvTimeout            = "UlEvTimeout"
	UlEvStop               = "UlEvStop"
)

// states of MibUpload FSM
const (
	UlStDisabled               = "UlStDisabled"
	UlStStarting               = "UlStStarting"
	UlStResettingMib           = "UlStResettingMib"
	UlStGettingVendorAndSerial = "UlStGettingVendorAndSerial"
	UlStGettingVersion         = "UlStGettingVersion"
	UlStGettingEquipIDAndOmcc  = "UlStGettingEquipIDAndOmcc"
	UlStTestingExtOmciSupport  = "UlStTestingExtOmciSupport"
	UlStGettingFirstSwVersion  = "UlStGettingFirstSwVersion"
	UlStGettingSecondSwVersion = "UlStGettingSecondSwVersion"
	UlStGettingMacAddress      = "UlStGettingMacAddress"
	UlStGettingMibTemplate     = "UlStGettingMibTemplate"
	UlStUploading              = "UlStUploading"
	UlStUploadDone             = "UlStUploadDone"
	UlStInSync                 = "UlStInSync"
	UlStVerifyingAndStoringTPs = "UlStVerifyingAndStoringTPs"
	UlStExaminingMds           = "UlStExaminingMds"
	UlStResynchronizing        = "UlStResynchronizing"
	UlStExaminingMdsSuccess    = "UlStExaminingMdsSuccess"
	UlStAuditing               = "UlStAuditing"
	UlStReAuditing             = "UlStReAuditing"
	UlStOutOfSync              = "UlStOutOfSync"
)

// CMibUlFsmIdleState - TODO: add comment
const CMibUlFsmIdleState = UlStInSync

// events of MibDownload FSM
const (
	DlEvStart         = "DlEvStart"
	DlEvCreateGal     = "DlEvCreateGal"
	DlEvRxGalResp     = "DlEvRxGalResp"
	DlEvRxOnu2gResp   = "DlEvRxOnu2gResp"
	DlEvRxBridgeResp  = "DlEvRxBridgeResp"
	DlEvTimeoutSimple = "DlEvTimeoutSimple"
	DlEvTimeoutBridge = "DlEvTimeoutBridge"
	DlEvReset         = "DlEvReset"
	DlEvRestart       = "DlEvRestart"
)

// states of MibDownload FSM
const (
	DlStDisabled     = "DlStDisabled"
	DlStStarting     = "DlStStarting"
	DlStCreatingGal  = "DlStCreatingGal"
	DlStSettingOnu2g = "DlStSettingOnu2g"
	DlStBridgeInit   = "DlStBridgeInit"
	DlStDownloaded   = "DlStDownloaded"
	DlStResetting    = "DlStResetting"
)

// CMibDlFsmIdleState - TODO: add comment
const CMibDlFsmIdleState = DlStDisabled

const (
	// NOTE that this hardcoded to service/voltha as the MIB template is shared across stacks
	cBasePathMibTemplateKvStore = "service/voltha/omci_mibs/go_templates"
	cSuffixMibTemplateKvStore   = "%s/%s/%s"
)

const cEmptyVendorIDString = "____"
const cEmptyVersionString = "______________"
const cEmptyMacAddrString = "000000000000"
const cEmptySerialNumberString = "0000000000000000"
const cEmptyEquipIDString = "EMPTY_EQUIP_ID"
const cNotPresentEquipIDString = "NOT_PRESENT_EQUIP_ID"

const vlanConfigSendChanExpiry = 5

type uniPersConfig struct {
	PersTpPathMap  map[uint8]string        `json:"PersTpPathMap"` // tp-id to tp-path map
	PersFlowParams []cmn.UniVlanFlowParams `json:"flow_params"`   //as defined in omci_ani_config.go
	PersUniID      uint8                   `json:"uni_id"`
}

type onuPersistentData struct {
	PersTcontMap           map[uint16]uint16 `json:"tcont_map"` //alloc-id to me-instance-id map
	PersSerialNumber       string            `json:"serial_number"`
	PersMacAddress         string            `json:"mac_address"`
	PersVendorID           string            `json:"vendor_id"`
	PersVersion            string            `json:"version"`
	PersEquipmentID        string            `json:"equipment_id"`
	PersActiveSwVersion    string            `json:"active_sw_version"`
	PersAdminState         string            `json:"admin_state"`
	PersOperState          string            `json:"oper_state"`
	PersUniConfig          []uniPersConfig   `json:"uni_config"`
	PersMibAuditInterval   time.Duration     `json:"mib_audit_interval"`
	PersAlarmAuditInterval time.Duration     `json:"alarm_audit_interval"`
	PersOnuID              uint32            `json:"onu_id"`
	PersIntfID             uint32            `json:"intf_id"`
	PersMibLastDbSync      uint32            `json:"mib_last_db_sync"`
	PersIsExtOmciSupported bool              `json:"is_ext_omci_supported"`
	PersUniUnlockDone      bool              `json:"uni_unlock_done"`
	PersUniDisableDone     bool              `json:"uni_disable_done"`
	PersMibDataSyncAdpt    uint8             `json:"mib_data_sync_adpt"`
}

//type UniTpidInstances map[uint8]map[uint8]inter_adapter.TechProfileDownloadMessage

// OnuDeviceEntry - ONU device info and FSM events.
//
//nolint:govet
type OnuDeviceEntry struct {
	SOnuPersistentData         onuPersistentData
	baseDeviceHandler          cmn.IdeviceHandler
	eventProxy                 eventif.EventProxy
	pOpenOnuAc                 cmn.IopenONUAC
	pOnuTP                     cmn.IonuUniTechProf
	onuKVStoreProcResult       error //error indication of processing
	coreClient                 *vgrpc.Client
	PDevOmciCC                 *cmn.OmciCC
	pOnuDB                     *devdb.OnuDeviceDB
	mibTemplateKVStore         *db.Backend
	ReconciledTpInstances      map[uint8]map[uint8]inter_adapter.TechProfileDownloadMessage
	chReconcilingFlowsFinished chan bool //channel to indicate that reconciling of flows has been finished
	onuKVStore                 *db.Backend
	POnuImageStatus            *swupg.OnuImageStatus
	//lockDeviceEntries           sync.RWMutex
	mibDbClass    func(context.Context) error
	supportedFsms cmn.OmciDeviceFsms

	// for mibUpload
	PMibUploadFsm *cmn.AdapterFsm //could be handled dynamically and more general as pcmn.AdapterFsm - perhaps later
	// for mibDownload
	PMibDownloadFsm                  *cmn.AdapterFsm //could be handled dynamically and more general as pcmn.AdapterFsm - perhaps later
	pLastTxMeInstance                *me.ManagedEntity
	omciMessageReceived              chan bool        //seperate channel needed by DownloadFsm
	omciRebootMessageReceivedChannel chan cmn.Message // channel needed by reboot request
	lastTxParamStruct                sLastTxMeParameter
	deviceID                         string
	mibTemplatePath                  string
	onuKVStorePath                   string
	onuSwImageIndications            cmn.SswImageIndications
	devState                         cmn.OnuDeviceEvent
	// Audit and MDS
	mibAuditInterval   time.Duration
	alarmAuditInterval time.Duration
	// TODO: periodical mib resync will be implemented with story VOL-3792
	//mibNextDbResync uint32

	MutexPersOnuConfig              sync.RWMutex
	MutexReconciledTpInstances      sync.RWMutex
	mutexReconcilingFlowsFlag       sync.RWMutex
	mutexOnuKVStore                 sync.RWMutex
	mutexOnuKVStoreProcResult       sync.RWMutex
	mutexOnuSwImageIndications      sync.RWMutex
	MutexOnuImageStatus             sync.RWMutex
	mutexLastTxParamStruct          sync.RWMutex
	mutexMibSyncMsgProcessorRunning sync.RWMutex
	//remark: general usage of pAdapterFsm would require generalization of CommChan  usage and internal event setting
	//  within the FSM event procedures
	mutexPLastTxMeInstance     sync.RWMutex
	reconcilingFlows           bool
	mibSyncMsgProcessorRunning bool
}

// NewOnuDeviceEntry returns a new instance of a OnuDeviceEntry
// mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOnuDeviceEntry(ctx context.Context, cc *vgrpc.Client, dh cmn.IdeviceHandler,
	openonu cmn.IopenONUAC) *OnuDeviceEntry {
	var onuDeviceEntry OnuDeviceEntry
	onuDeviceEntry.deviceID = dh.GetDeviceID()
	logger.Debugw(ctx, "init-onuDeviceEntry", log.Fields{"device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.baseDeviceHandler = dh
	onuDeviceEntry.eventProxy = dh.GetEventProxy()
	onuDeviceEntry.pOpenOnuAc = openonu
	onuDeviceEntry.coreClient = cc
	onuDeviceEntry.devState = cmn.DeviceStatusInit
	onuDeviceEntry.SOnuPersistentData.PersUniConfig = make([]uniPersConfig, 0)
	onuDeviceEntry.SOnuPersistentData.PersTcontMap = make(map[uint16]uint16)
	onuDeviceEntry.ReconciledTpInstances = make(map[uint8]map[uint8]inter_adapter.TechProfileDownloadMessage)
	onuDeviceEntry.chReconcilingFlowsFinished = make(chan bool)
	onuDeviceEntry.reconcilingFlows = false
	onuDeviceEntry.omciRebootMessageReceivedChannel = make(chan cmn.Message, 2)
	//openomciagent.lockDeviceHandlersMap = sync.RWMutex{}
	//OMCI related databases are on a per-agent basis. State machines and tasks
	//are per ONU Vendor
	//
	// MIB Synchronization Database - possible overloading from arguments
	supportedFsms := onuDeviceEntry.pOpenOnuAc.GetSupportedFsms()
	if supportedFsms != nil {
		onuDeviceEntry.supportedFsms = *supportedFsms
	} else {
		// This branch is currently not used and is for potential future usage of alternative MIB Sync FSMs only!
		//var mibSyncFsm = NewMibSynchronizer()
		// use some internal defaults, if not defined from outside
		onuDeviceEntry.supportedFsms = cmn.OmciDeviceFsms{
			"mib-synchronizer": cmn.ActivityDescr{
				//mibSyncFsm,        // Implements the MIB synchronization state machine
				DatabaseClass: onuDeviceEntry.mibDbVolatileDict, // Implements volatile ME MIB database
				//true,                             // Advertise events on OpenOMCI event bus
				AuditInterval: dh.GetAlarmAuditInterval(), // Time to wait between MIB audits.  0 to disable audits.
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
	onuDeviceEntry.mibDbClass = onuDeviceEntry.supportedFsms["mib-synchronizer"].DatabaseClass
	logger.Debug(ctx, "access2mibDbClass")
	go func() {
		_ = onuDeviceEntry.mibDbClass(ctx)
	}()
	if !dh.IsReconciling() {
		onuDeviceEntry.mibAuditInterval = onuDeviceEntry.supportedFsms["mib-synchronizer"].AuditInterval
		onuDeviceEntry.SOnuPersistentData.PersMibAuditInterval = onuDeviceEntry.mibAuditInterval
		onuDeviceEntry.alarmAuditInterval = dh.GetAlarmAuditInterval()
		onuDeviceEntry.SOnuPersistentData.PersAlarmAuditInterval = onuDeviceEntry.alarmAuditInterval
	} else {
		logger.Debugw(ctx, "reconciling - take audit interval from persistent data", log.Fields{"device-id": onuDeviceEntry.deviceID})
		// TODO: This is a preparation for VOL-VOL-3811 to preserve config history in case of
		// vendor- or deviceID-specific configurations via voltctl-commands
		onuDeviceEntry.mibAuditInterval = onuDeviceEntry.SOnuPersistentData.PersMibAuditInterval
		onuDeviceEntry.alarmAuditInterval = onuDeviceEntry.SOnuPersistentData.PersAlarmAuditInterval
	}
	logger.Debugw(ctx, "MibAuditInterval and AlarmAuditInterval is set to", log.Fields{"mib-audit-interval": onuDeviceEntry.mibAuditInterval,
		"alarm-audit-interval": onuDeviceEntry.alarmAuditInterval})
	// TODO: periodical mib resync will be implemented with story VOL-3792
	//onuDeviceEntry.mibNextDbResync = 0

	// Omci related Mib upload sync state machine
	mibUploadChan := make(chan cmn.Message, 2)
	onuDeviceEntry.PMibUploadFsm = cmn.NewAdapterFsm("MibUpload", onuDeviceEntry.deviceID, mibUploadChan)
	onuDeviceEntry.PMibUploadFsm.PFsm = fsm.NewFSM(
		UlStDisabled,
		fsm.Events{

			{Name: UlEvStart, Src: []string{UlStDisabled}, Dst: UlStStarting},

			{Name: UlEvResetMib, Src: []string{UlStStarting}, Dst: UlStResettingMib},
			{Name: UlEvGetVendorAndSerial, Src: []string{UlStResettingMib}, Dst: UlStGettingVendorAndSerial},
			{Name: UlEvGetVersion, Src: []string{UlStGettingVendorAndSerial}, Dst: UlStGettingVersion},
			{Name: UlEvGetEquipIDAndOmcc, Src: []string{UlStGettingVersion}, Dst: UlStGettingEquipIDAndOmcc},
			{Name: UlEvTestExtOmciSupport, Src: []string{UlStGettingEquipIDAndOmcc}, Dst: UlStTestingExtOmciSupport},
			{Name: UlEvGetFirstSwVersion, Src: []string{UlStGettingEquipIDAndOmcc, UlStTestingExtOmciSupport}, Dst: UlStGettingFirstSwVersion},
			{Name: UlEvGetSecondSwVersion, Src: []string{UlStGettingFirstSwVersion}, Dst: UlStGettingSecondSwVersion},
			{Name: UlEvGetMacAddress, Src: []string{UlStGettingSecondSwVersion}, Dst: UlStGettingMacAddress},
			{Name: UlEvGetMibTemplate, Src: []string{UlStGettingMacAddress}, Dst: UlStGettingMibTemplate},

			{Name: UlEvUploadMib, Src: []string{UlStGettingMibTemplate}, Dst: UlStUploading},

			{Name: UlEvVerifyAndStoreTPs, Src: []string{UlStStarting}, Dst: UlStVerifyingAndStoringTPs},
			{Name: UlEvSuccess, Src: []string{UlStVerifyingAndStoringTPs}, Dst: UlStExaminingMds},
			{Name: UlEvMismatch, Src: []string{UlStVerifyingAndStoringTPs}, Dst: UlStResettingMib},

			{Name: UlEvSuccess, Src: []string{UlStGettingMibTemplate}, Dst: UlStUploadDone},
			{Name: UlEvSuccess, Src: []string{UlStUploading}, Dst: UlStUploadDone},

			{Name: UlEvSuccess, Src: []string{UlStUploadDone}, Dst: UlStInSync},
			//{Name: UlEvSuccess, Src: []string{UlStExaminingMds}, Dst: UlStInSync},
			{Name: UlEvSuccess, Src: []string{UlStExaminingMds}, Dst: UlStExaminingMdsSuccess},
			// TODO: As long as mib-resynchronizing is not implemented, failed MDS-examination triggers
			// mib-reset and new provisioning at this point
			//{Name: UlEvMismatch, Src: []string{UlStExaminingMds}, Dst: UlStResynchronizing},
			{Name: UlEvMismatch, Src: []string{UlStExaminingMds}, Dst: UlStResettingMib},

			{Name: UlEvSuccess, Src: []string{UlStExaminingMdsSuccess}, Dst: UlStInSync},
			{Name: UlEvMismatch, Src: []string{UlStExaminingMdsSuccess}, Dst: UlStResettingMib},

			{Name: UlEvAuditMib, Src: []string{UlStInSync}, Dst: UlStAuditing},

			{Name: UlEvSuccess, Src: []string{UlStOutOfSync}, Dst: UlStInSync},
			{Name: UlEvAuditMib, Src: []string{UlStOutOfSync}, Dst: UlStAuditing},

			{Name: UlEvSuccess, Src: []string{UlStAuditing}, Dst: UlStInSync},
			{Name: UlEvMismatch, Src: []string{UlStAuditing}, Dst: UlStReAuditing},
			{Name: UlEvForceResync, Src: []string{UlStAuditing}, Dst: UlStResynchronizing},

			{Name: UlEvSuccess, Src: []string{UlStReAuditing}, Dst: UlStInSync},
			{Name: UlEvMismatch, Src: []string{UlStReAuditing}, Dst: UlStResettingMib},

			{Name: UlEvSuccess, Src: []string{UlStResynchronizing}, Dst: UlStInSync},
			{Name: UlEvDiffsFound, Src: []string{UlStResynchronizing}, Dst: UlStOutOfSync},

			{Name: UlEvTimeout, Src: []string{UlStResettingMib, UlStGettingVendorAndSerial, UlStGettingVersion, UlStGettingEquipIDAndOmcc, UlStTestingExtOmciSupport,
				UlStGettingFirstSwVersion, UlStGettingSecondSwVersion, UlStGettingMacAddress, UlStGettingMibTemplate, UlStUploading, UlStResynchronizing,
				UlStVerifyingAndStoringTPs, UlStExaminingMds, UlStUploadDone, UlStInSync, UlStOutOfSync, UlStAuditing, UlStReAuditing}, Dst: UlStStarting},

			{Name: UlEvStop, Src: []string{UlStStarting, UlStResettingMib, UlStGettingVendorAndSerial, UlStGettingVersion, UlStGettingEquipIDAndOmcc, UlStTestingExtOmciSupport,
				UlStGettingFirstSwVersion, UlStGettingSecondSwVersion, UlStGettingMacAddress, UlStGettingMibTemplate, UlStUploading, UlStResynchronizing,
				UlStVerifyingAndStoringTPs, UlStExaminingMds, UlStUploadDone, UlStInSync, UlStOutOfSync, UlStAuditing, UlStReAuditing}, Dst: UlStDisabled},
		},

		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { onuDeviceEntry.PMibUploadFsm.LogFsmStateChange(ctx, e) },
			"enter_" + UlStStarting:               func(e *fsm.Event) { onuDeviceEntry.enterStartingState(ctx, e) },
			"enter_" + UlStResettingMib:           func(e *fsm.Event) { onuDeviceEntry.enterResettingMibState(ctx, e) },
			"enter_" + UlStGettingVendorAndSerial: func(e *fsm.Event) { onuDeviceEntry.enterGettingVendorAndSerialState(ctx, e) },
			"enter_" + UlStGettingVersion:         func(e *fsm.Event) { onuDeviceEntry.enterGettingVersionState(ctx, e) },
			"enter_" + UlStGettingEquipIDAndOmcc:  func(e *fsm.Event) { onuDeviceEntry.enterGettingEquipIDAndOmccVersState(ctx, e) },
			"enter_" + UlStTestingExtOmciSupport:  func(e *fsm.Event) { onuDeviceEntry.enterTestingExtOmciSupportState(ctx, e) },
			"enter_" + UlStGettingFirstSwVersion:  func(e *fsm.Event) { onuDeviceEntry.enterGettingFirstSwVersionState(ctx, e) },
			"enter_" + UlStGettingSecondSwVersion: func(e *fsm.Event) { onuDeviceEntry.enterGettingSecondSwVersionState(ctx, e) },
			"enter_" + UlStGettingMacAddress:      func(e *fsm.Event) { onuDeviceEntry.enterGettingMacAddressState(ctx, e) },
			"enter_" + UlStGettingMibTemplate:     func(e *fsm.Event) { onuDeviceEntry.enterGettingMibTemplateState(ctx, e) },
			"enter_" + UlStUploading:              func(e *fsm.Event) { onuDeviceEntry.enterUploadingState(ctx, e) },
			"enter_" + UlStUploadDone:             func(e *fsm.Event) { onuDeviceEntry.enterUploadDoneState(ctx, e) },
			"enter_" + UlStExaminingMds:           func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsState(ctx, e) },
			"enter_" + UlStVerifyingAndStoringTPs: func(e *fsm.Event) { onuDeviceEntry.enterVerifyingAndStoringTPsState(ctx, e) },
			"enter_" + UlStResynchronizing:        func(e *fsm.Event) { onuDeviceEntry.enterResynchronizingState(ctx, e) },
			"enter_" + UlStExaminingMdsSuccess:    func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsSuccessState(ctx, e) },
			"enter_" + UlStAuditing:               func(e *fsm.Event) { onuDeviceEntry.enterAuditingState(ctx, e) },
			"enter_" + UlStReAuditing:             func(e *fsm.Event) { onuDeviceEntry.enterReAuditingState(ctx, e) },
			"enter_" + UlStOutOfSync:              func(e *fsm.Event) { onuDeviceEntry.enterOutOfSyncState(ctx, e) },
			"enter_" + UlStInSync:                 func(e *fsm.Event) { onuDeviceEntry.enterInSyncState(ctx, e) },
		},
	)
	// Omci related Mib download state machine
	mibDownloadChan := make(chan cmn.Message, 2)
	onuDeviceEntry.PMibDownloadFsm = cmn.NewAdapterFsm("MibDownload", onuDeviceEntry.deviceID, mibDownloadChan)
	onuDeviceEntry.PMibDownloadFsm.PFsm = fsm.NewFSM(
		DlStDisabled,
		fsm.Events{

			{Name: DlEvStart, Src: []string{DlStDisabled}, Dst: DlStStarting},

			{Name: DlEvCreateGal, Src: []string{DlStStarting}, Dst: DlStCreatingGal},
			{Name: DlEvRxGalResp, Src: []string{DlStCreatingGal}, Dst: DlStSettingOnu2g},
			{Name: DlEvRxOnu2gResp, Src: []string{DlStSettingOnu2g}, Dst: DlStBridgeInit},
			// the bridge state is used for multi ME config for alle UNI related ports
			// maybe such could be reflected in the state machine as well (port number parametrized)
			// but that looks not straightforward here - so we keep it simple here for the beginning(?)
			{Name: DlEvRxBridgeResp, Src: []string{DlStBridgeInit}, Dst: DlStDownloaded},

			{Name: DlEvTimeoutSimple, Src: []string{DlStCreatingGal, DlStSettingOnu2g}, Dst: DlStStarting},
			{Name: DlEvTimeoutBridge, Src: []string{DlStBridgeInit}, Dst: DlStStarting},

			{Name: DlEvReset, Src: []string{DlStStarting, DlStCreatingGal, DlStSettingOnu2g,
				DlStBridgeInit, DlStDownloaded}, Dst: DlStResetting},
			// exceptional treatment for all states except DlStResetting
			{Name: DlEvRestart, Src: []string{DlStStarting, DlStCreatingGal, DlStSettingOnu2g,
				DlStBridgeInit, DlStDownloaded, DlStResetting}, Dst: DlStDisabled},
		},

		fsm.Callbacks{
			"enter_state":               func(e *fsm.Event) { onuDeviceEntry.PMibDownloadFsm.LogFsmStateChange(ctx, e) },
			"enter_" + DlStStarting:     func(e *fsm.Event) { onuDeviceEntry.enterDLStartingState(ctx, e) },
			"enter_" + DlStCreatingGal:  func(e *fsm.Event) { onuDeviceEntry.enterCreatingGalState(ctx, e) },
			"enter_" + DlStSettingOnu2g: func(e *fsm.Event) { onuDeviceEntry.enterSettingOnu2gState(ctx, e) },
			"enter_" + DlStBridgeInit:   func(e *fsm.Event) { onuDeviceEntry.enterBridgeInitState(ctx, e) },
			"enter_" + DlStDownloaded:   func(e *fsm.Event) { onuDeviceEntry.enterDownloadedState(ctx, e) },
			"enter_" + DlStResetting:    func(e *fsm.Event) { onuDeviceEntry.enterResettingState(ctx, e) },
		},
	)
	if onuDeviceEntry.PMibDownloadFsm == nil || onuDeviceEntry.PMibDownloadFsm.PFsm == nil {
		logger.Errorw(ctx, "MibDownloadFsm could not be instantiated", log.Fields{"device-id": onuDeviceEntry.deviceID})
		// TODO some specific error treatment - or waiting for crash ?
	}

	onuDeviceEntry.mibTemplateKVStore = onuDeviceEntry.baseDeviceHandler.SetBackend(ctx, cBasePathMibTemplateKvStore)
	if onuDeviceEntry.mibTemplateKVStore == nil {
		logger.Errorw(ctx, "Can't access mibTemplateKVStore - no backend connection to service",
			log.Fields{"device-id": onuDeviceEntry.deviceID, "service": cBasePathMibTemplateKvStore})
	}

	onuDeviceEntry.onuKVStorePath = onuDeviceEntry.deviceID
	baseKvStorePath := fmt.Sprintf(cmn.CBasePathOnuKVStore, dh.GetBackendPathPrefix())
	onuDeviceEntry.onuKVStore = onuDeviceEntry.baseDeviceHandler.SetBackend(ctx, baseKvStorePath)
	if onuDeviceEntry.onuKVStore == nil {
		logger.Errorw(ctx, "Can't access onuKVStore - no backend connection to service",
			log.Fields{"device-id": onuDeviceEntry.deviceID, "service": baseKvStorePath})
	}

	// Alarm Synchronization Database

	//self._alarm_db = None
	//self._alarm_database_cls = support_classes['alarm-synchronizer']['database']
	return &onuDeviceEntry
}

// Start starts (logs) the omci agent
func (oo *OnuDeviceEntry) Start(ctx context.Context) error {
	logger.Debugw(ctx, "OnuDeviceEntry-starting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC == nil {
		oo.PDevOmciCC = cmn.NewOmciCC(ctx, oo.deviceID, oo.baseDeviceHandler, oo, oo.baseDeviceHandler.GetOnuAlarmManager(), oo.coreClient)
		if oo.PDevOmciCC == nil {
			logger.Errorw(ctx, "Could not create devOmciCc - abort", log.Fields{"for device-id": oo.deviceID})
			return fmt.Errorf("could not create devOmciCc %s", oo.deviceID)
		}
	}
	return nil
}

// Stop stops/resets the omciCC
func (oo *OnuDeviceEntry) Stop(ctx context.Context, abResetOmciCC bool) error {
	logger.Debugw(ctx, "OnuDeviceEntry-stopping", log.Fields{"for device-id": oo.deviceID})
	if abResetOmciCC && (oo.PDevOmciCC != nil) {
		_ = oo.PDevOmciCC.Stop(ctx)
	}
	//to allow for all event notifications again when re-using the device and omciCC
	oo.devState = cmn.DeviceStatusInit
	return nil
}

// Reboot - TODO: add comment
func (oo *OnuDeviceEntry) Reboot(ctx context.Context) error {
	logger.Debugw(ctx, "OnuDeviceEntry-rebooting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC != nil {
		if err := oo.PDevOmciCC.SendReboot(ctx, oo.baseDeviceHandler.GetOmciTimeout(), true, oo.omciRebootMessageReceivedChannel); err != nil {
			logger.Errorw(ctx, "onu didn't reboot", log.Fields{"for device-id": oo.deviceID})
			return err
		}
	}
	return nil
}

// WaitForRebootResponse - TODO: add comment
func (oo *OnuDeviceEntry) WaitForRebootResponse(ctx context.Context, responseChannel chan cmn.Message) error {
	select {
	case <-time.After(oo.PDevOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw(ctx, "reboot timeout", log.Fields{"for device-id": oo.deviceID})
		return fmt.Errorf("rebootTimeout")
	case data := <-responseChannel:
		switch data.Data.(cmn.OmciMessage).OmciMsg.MessageType {
		case omci.RebootResponseType:
			{
				msgLayer := (*data.Data.(cmn.OmciMessage).OmciPacket).Layer(omci.LayerTypeRebootResponse)
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

// Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(ctx context.Context, devEvent cmn.OnuDeviceEvent) {
	logger.Debugw(ctx, "relaying system-event", log.Fields{"device-id": oo.deviceID, "Event": devEvent})
	// decouple the handler transfer from further processing here
	// TODO!!! check if really no synch is required within the system e.g. to ensure following steps ..
	if devEvent == cmn.MibDatabaseSync {
		if oo.devState < cmn.MibDatabaseSync { //devState has not been synced yet
			oo.devState = cmn.MibDatabaseSync
			go oo.baseDeviceHandler.DeviceProcStatusUpdate(ctx, devEvent)
			//TODO!!! device control: next step: start MIB capability verification from here ?!!!
		} else {
			logger.Debugw(ctx, "mibinsync-event in some already synced state - ignored",
				log.Fields{"device-id": oo.deviceID, "state": oo.devState})
		}
	} else if devEvent == cmn.MibDownloadDone {
		if oo.devState < cmn.MibDownloadDone { //devState has not been synced yet
			oo.devState = cmn.MibDownloadDone
			go oo.baseDeviceHandler.DeviceProcStatusUpdate(ctx, devEvent)
		} else {
			logger.Debugw(ctx, "mibdownloaddone-event was already seen - ignored",
				log.Fields{"device-id": oo.deviceID, "state": oo.devState})
		}
	} else {
		logger.Warnw(ctx, "device-event not yet handled",
			log.Fields{"device-id": oo.deviceID, "state": devEvent})
	}
}

// RestoreDataFromOnuKvStore - TODO: add comment
func (oo *OnuDeviceEntry) RestoreDataFromOnuKvStore(ctx context.Context) error {
	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("onuKVStore-not-set-abort-%s", oo.deviceID)
	}
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()
	oo.SOnuPersistentData =
		onuPersistentData{
			PersTcontMap:           make(map[uint16]uint16),
			PersSerialNumber:       "",
			PersMacAddress:         "",
			PersVendorID:           "",
			PersVersion:            "",
			PersEquipmentID:        "",
			PersActiveSwVersion:    "",
			PersAdminState:         "",
			PersOperState:          "",
			PersUniConfig:          make([]uniPersConfig, 0),
			PersMibAuditInterval:   oo.mibAuditInterval,
			PersAlarmAuditInterval: oo.alarmAuditInterval,
			PersOnuID:              0,
			PersIntfID:             0,
			PersMibLastDbSync:      0,
			PersIsExtOmciSupported: false,
			PersUniUnlockDone:      false,
			PersUniDisableDone:     false,
			PersMibDataSyncAdpt:    0,
		}
	oo.mutexOnuKVStore.RLock()
	Value, err := oo.onuKVStore.Get(ctx, oo.onuKVStorePath)
	oo.mutexOnuKVStore.RUnlock()
	if err == nil {
		if Value != nil {
			logger.Debugw(ctx, "ONU-data read",
				log.Fields{"Key": Value.Key, "device-id": oo.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, &oo.SOnuPersistentData); err != nil {
				logger.Errorw(ctx, "unable to unmarshal ONU-data", log.Fields{"error": err, "device-id": oo.deviceID})
				return fmt.Errorf("unable-to-unmarshal-ONU-data-%s", oo.deviceID)
			}
			logger.Debugw(ctx, "ONU-data", log.Fields{"SOnuPersistentData": oo.SOnuPersistentData,
				"device-id": oo.deviceID})
		} else {
			logger.Debugw(ctx, "no ONU-data found", log.Fields{"path": oo.onuKVStorePath, "device-id": oo.deviceID})
			return fmt.Errorf("no-ONU-data-found")
		}
	} else {
		logger.Errorw(ctx, "unable to read from KVstore", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("unable-to-read-from-KVstore-%s", oo.deviceID)
	}
	return nil
}

// DeleteDataFromOnuKvStore - TODO: add comment
func (oo *OnuDeviceEntry) DeleteDataFromOnuKvStore(ctx context.Context) error {

	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		return errors.New("onu-data delete aborted: onuKVStore not set")
	}
	err := oo.deletePersistentData(ctx)
	if err != nil {
		logger.Errorf(ctx, "onu-data delete aborted: during kv-access", log.Fields{"device-id": oo.deviceID, "err": err})
		return err
	}
	return nil
}

func (oo *OnuDeviceEntry) deletePersistentData(ctx context.Context) error {

	logger.Debugw(ctx, "delete and clear internal persistency data", log.Fields{"device-id": oo.deviceID})
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()

	oo.SOnuPersistentData.PersUniConfig = nil //releasing all UniConfig entries to garbage collector default entry
	oo.SOnuPersistentData =
		onuPersistentData{
			PersTcontMap:           make(map[uint16]uint16),
			PersSerialNumber:       "",
			PersMacAddress:         "",
			PersVendorID:           "",
			PersVersion:            "",
			PersEquipmentID:        "",
			PersActiveSwVersion:    "",
			PersAdminState:         "",
			PersOperState:          "",
			PersUniConfig:          make([]uniPersConfig, 0),
			PersMibAuditInterval:   oo.mibAuditInterval,
			PersAlarmAuditInterval: oo.alarmAuditInterval,
			PersOnuID:              0,
			PersIntfID:             0,
			PersMibLastDbSync:      0,
			PersIsExtOmciSupported: false,
			PersUniUnlockDone:      false,
			PersUniDisableDone:     false,
			PersMibDataSyncAdpt:    0,
		}
	logger.Debugw(ctx, "delete ONU-data from KVStore", log.Fields{"device-id": oo.deviceID})
	oo.mutexOnuKVStore.Lock()
	err := oo.onuKVStore.Delete(ctx, oo.onuKVStorePath)
	oo.mutexOnuKVStore.Unlock()
	if err != nil {
		logger.Errorw(ctx, "unable to delete in KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		return err
	}
	return nil
}

// UpdateOnuKvStore - TODO: add comment
func (oo *OnuDeviceEntry) UpdateOnuKvStore(ctx context.Context) error {

	if oo.onuKVStore == nil {
		logger.Debugw(ctx, "onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		return errors.New("onu-data update aborted: onuKVStore not set")
	}
	err := oo.storeDataInOnuKvStore(ctx)
	if err != nil {
		logger.Errorf(ctx, "onu-data update aborted: during writing process", log.Fields{"device-id": oo.deviceID, "err": err})
		return err
	}
	return nil
}

func (oo *OnuDeviceEntry) storeDataInOnuKvStore(ctx context.Context) error {

	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()

	oo.pOpenOnuAc.RLockMutexDeviceHandlersMap()
	if _, exist := oo.pOpenOnuAc.GetDeviceHandler(oo.deviceID); !exist {
		logger.Debugw(ctx, "delete_device in progress - skip write request", log.Fields{"device-id": oo.deviceID})
		oo.pOpenOnuAc.RUnlockMutexDeviceHandlersMap()
		return nil
	}
	oo.baseDeviceHandler.RLockMutexDeletionInProgressFlag()
	if oo.baseDeviceHandler.GetDeletionInProgress() {
		logger.Debugw(ctx, "delete_device in progress - skip write request", log.Fields{"device-id": oo.deviceID})
		oo.pOpenOnuAc.RUnlockMutexDeviceHandlersMap()
		oo.baseDeviceHandler.RUnlockMutexDeletionInProgressFlag()
		return nil
	}
	oo.pOpenOnuAc.RUnlockMutexDeviceHandlersMap()
	oo.baseDeviceHandler.RUnlockMutexDeletionInProgressFlag()

	//assign values which are not already present when NewOnuDeviceEntry() is called
	onuIndication := oo.baseDeviceHandler.GetOnuIndication()
	if onuIndication != nil {
		oo.SOnuPersistentData.PersOnuID = onuIndication.OnuId
		oo.SOnuPersistentData.PersIntfID = onuIndication.IntfId
		//TODO: verify usage of these values during restart UC
		oo.SOnuPersistentData.PersAdminState = onuIndication.AdminState
		oo.SOnuPersistentData.PersOperState = onuIndication.OperState
	} else {
		logger.Errorw(ctx, "onuIndication not set, unable to load ONU-data", log.Fields{"device-id": oo.deviceID})
		return errors.New("onuIndication not set, unable to load ONU-data")
	}

	logger.Debugw(ctx, "Update ONU-data in KVStore", log.Fields{"device-id": oo.deviceID, "SOnuPersistentData": oo.SOnuPersistentData})

	Value, err := json.Marshal(oo.SOnuPersistentData)
	if err != nil {
		logger.Errorw(ctx, "unable to marshal ONU-data", log.Fields{"SOnuPersistentData": oo.SOnuPersistentData,
			"device-id": oo.deviceID, "err": err})
		return err
	}

	oo.mutexOnuKVStore.Lock()
	err = oo.onuKVStore.Put(ctx, oo.onuKVStorePath, Value)
	oo.mutexOnuKVStore.Unlock()
	if err != nil {
		logger.Errorf(ctx, "unable to write ONU-data into KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		return err
	}
	return nil
}

// UpdateOnuUniTpPath - TODO: add comment
func (oo *OnuDeviceEntry) UpdateOnuUniTpPath(ctx context.Context, aUniID uint8, aTpID uint8, aPathString string) bool {
	/* within some specific InterAdapter processing request write/read access to data is ensured to be sequentially,
	   as also the complete sequence is ensured to 'run to completion' before some new request is accepted
	   no specific concurrency protection to SOnuPersistentData is required here
	*/
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()

	for k, v := range oo.SOnuPersistentData.PersUniConfig {
		if v.PersUniID == aUniID {
			existingPath, ok := oo.SOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID]
			logger.Debugw(ctx, "PersUniConfig-entry exists", log.Fields{"device-id": oo.deviceID, "uniID": aUniID,
				"tpID": aTpID, "path": aPathString, "existingPath": existingPath, "ok": ok})
			if !ok {
				logger.Debugw(ctx, "tp-does-not-exist", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "tpID": aTpID, "path": aPathString})
			}
			if existingPath != aPathString {
				if aPathString == "" {
					//existing entry to be deleted
					logger.Debugw(ctx, "UniTp delete path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.SOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID] = ""
				} else {
					//existing entry to be modified
					logger.Debugw(ctx, "UniTp modify path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.SOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpID] = aPathString
				}
				return true
			}
			//entry already exists
			if aPathString == "" {
				//no active TechProfile
				logger.Debugw(ctx, "UniTp path has already been removed - no AniSide config to be removed", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID})
			} else {
				//the given TechProfile already exists and is assumed to be active - update devReason as if the config has been done here
				//was needed e.g. in voltha POD Tests:Validate authentication on a disabled ONU
				//  (as here the TechProfile has not been removed with the disable-device before the new enable-device)
				logger.Debugw(ctx, "UniTp path already exists - TechProfile supposed to be active", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
				//no deviceReason update (DeviceProcStatusUpdate) here to ensure 'omci_flows_pushed' state within disable/enable procedure of ATT scenario
				//  (during which the flows are removed/re-assigned but the techProf is left active)
				//and as the TechProfile is regarded as active we have to verify, if some flow configuration still waits on it
				//  (should not be the case, but should not harm or be more robust ...)
				// and to be sure, that for some reason the corresponding TpDelete was lost somewhere in history
				//  we also reset a possibly outstanding delete request - repeated TpConfig is regarded as valid for waiting flow config
				if oo.pOnuTP != nil {
					oo.pOnuTP.SetProfileToDelete(aUniID, aTpID, false)
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
	oo.SOnuPersistentData.PersUniConfig =
		append(oo.SOnuPersistentData.PersUniConfig, uniPersConfig{PersUniID: aUniID, PersTpPathMap: perSubTpPathMap, PersFlowParams: make([]cmn.UniVlanFlowParams, 0)})
	return true
}

// UpdateOnuUniFlowConfig - TODO: add comment
func (oo *OnuDeviceEntry) UpdateOnuUniFlowConfig(aUniID uint8, aUniVlanFlowParams *[]cmn.UniVlanFlowParams) {

	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()

	for k, v := range oo.SOnuPersistentData.PersUniConfig {
		if v.PersUniID == aUniID {
			oo.SOnuPersistentData.PersUniConfig[k].PersFlowParams = make([]cmn.UniVlanFlowParams, len(*aUniVlanFlowParams))
			copy(oo.SOnuPersistentData.PersUniConfig[k].PersFlowParams, *aUniVlanFlowParams)
			return
		}
	}
	//flow update was faster than tp-config - create PersUniConfig-entry
	//TODO!!: following activity to 'add' some new uni entry might not be quite correct if this function is called to clear the data
	//  (e.g after flow removal from RemoveUniFlowParams()).
	//  This has the effect of misleading indication that there is still some active UNI entry, even though there might be only some nil flow entry
	//  The effect of this flaw is that at TechProfile removal there is an additional attempt to remove the entry even though no techProfile exists anymore
	//  The code is not changed here because of the current release lane, changes might have unexpected secondary effects, perhaps later with more elaborate tests
	tmpConfig := uniPersConfig{PersUniID: aUniID, PersTpPathMap: make(map[uint8]string), PersFlowParams: make([]cmn.UniVlanFlowParams, len(*aUniVlanFlowParams))}
	copy(tmpConfig.PersFlowParams, *aUniVlanFlowParams)
	oo.SOnuPersistentData.PersUniConfig = append(oo.SOnuPersistentData.PersUniConfig, tmpConfig)
}

// ResetKvProcessingErrorIndication - TODO: add comment
func (oo *OnuDeviceEntry) ResetKvProcessingErrorIndication() {
	oo.mutexOnuKVStoreProcResult.Lock()
	oo.onuKVStoreProcResult = nil
	oo.mutexOnuKVStoreProcResult.Unlock()
}

// GetKvProcessingErrorIndication - TODO: add comment
func (oo *OnuDeviceEntry) GetKvProcessingErrorIndication() error {
	oo.mutexOnuKVStoreProcResult.RLock()
	value := oo.onuKVStoreProcResult
	oo.mutexOnuKVStoreProcResult.RUnlock()
	return value
}

// func (oo *OnuDeviceEntry) setKvProcessingErrorIndication(value error) {
// 	oo.mutexOnuKVStoreProcResult.Lock()
// 	oo.onuKVStoreProcResult = value
// 	oo.mutexOnuKVStoreProcResult.Unlock()
// }

// IncrementMibDataSync - TODO: add comment
func (oo *OnuDeviceEntry) IncrementMibDataSync(ctx context.Context) {
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()
	if oo.SOnuPersistentData.PersMibDataSyncAdpt < 255 {
		oo.SOnuPersistentData.PersMibDataSyncAdpt++
	} else {
		// per G.984 and G.988 overflow starts over at 1 given 0 is reserved for reset
		oo.SOnuPersistentData.PersMibDataSyncAdpt = 1
	}
	logger.Debugf(ctx, "mibDataSync updated - mds: %d - device-id: %s", oo.SOnuPersistentData.PersMibDataSyncAdpt, oo.deviceID)
}

// ModifySwImageInactiveVersion - updates the inactive SW image version stored
func (oo *OnuDeviceEntry) ModifySwImageInactiveVersion(ctx context.Context, aImageVersion string) {
	oo.mutexOnuSwImageIndications.Lock()
	defer oo.mutexOnuSwImageIndications.Unlock()
	logger.Debugw(ctx, "software-image set inactive version", log.Fields{
		"device-id": oo.deviceID, "version": aImageVersion})
	oo.onuSwImageIndications.InActiveEntityEntry.Version = aImageVersion
	//inactive SW version is not part of persistency data (yet) - no need to update that
}

// ModifySwImageActiveCommit - updates the active SW commit flag stored
func (oo *OnuDeviceEntry) ModifySwImageActiveCommit(ctx context.Context, aCommitted uint8) {
	oo.mutexOnuSwImageIndications.Lock()
	defer oo.mutexOnuSwImageIndications.Unlock()
	logger.Debugw(ctx, "software-image set active entity commit flag", log.Fields{
		"device-id": oo.deviceID, "committed": aCommitted})
	oo.onuSwImageIndications.ActiveEntityEntry.IsCommitted = aCommitted
	//commit flag is not part of persistency data (yet) - no need to update that
}

// GetActiveImageVersion - returns the active SW image version stored
func (oo *OnuDeviceEntry) GetActiveImageVersion(ctx context.Context) string {
	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.ActiveEntityEntry.Valid {
		value := oo.onuSwImageIndications.ActiveEntityEntry.Version
		oo.mutexOnuSwImageIndications.RUnlock()
		return value
	}
	oo.mutexOnuSwImageIndications.RUnlock()
	logger.Debugw(ctx, "Active Image is not valid", log.Fields{"device-id": oo.deviceID})
	return ""
}

// GetInactiveImageVersion - TODO: add comment
func (oo *OnuDeviceEntry) GetInactiveImageVersion(ctx context.Context) string {
	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.InActiveEntityEntry.Valid {
		value := oo.onuSwImageIndications.InActiveEntityEntry.Version
		oo.mutexOnuSwImageIndications.RUnlock()
		return value
	}
	oo.mutexOnuSwImageIndications.RUnlock()
	logger.Debugw(ctx, "Inactive Image is not valid", log.Fields{"device-id": oo.deviceID})
	return ""
}

func (oo *OnuDeviceEntry) buildMibTemplatePath() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	return fmt.Sprintf(cSuffixMibTemplateKvStore, oo.SOnuPersistentData.PersVendorID, oo.SOnuPersistentData.PersVersion,
		oo.SOnuPersistentData.PersActiveSwVersion)
}

// AllocateFreeTcont - TODO: add comment
func (oo *OnuDeviceEntry) AllocateFreeTcont(ctx context.Context, allocID uint16) (uint16, bool, error) {
	logger.Debugw(ctx, "allocate-free-tcont", log.Fields{"device-id": oo.deviceID, "allocID": allocID,
		"allocated-instances": oo.SOnuPersistentData.PersTcontMap})

	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()
	if entityID, ok := oo.SOnuPersistentData.PersTcontMap[allocID]; ok {
		//tcont already allocated before, return the used instance-id
		return entityID, true, nil
	}
	//First allocation of tcont. Find a free instance
	if tcontInstKeys := oo.pOnuDB.GetSortedInstKeys(ctx, me.TContClassID); len(tcontInstKeys) > 0 {
		logger.Debugw(ctx, "allocate-free-tcont-db-keys", log.Fields{"device-id": oo.deviceID, "keys": tcontInstKeys})
		for _, instID := range tcontInstKeys {
			instExist := false
			//If this instance exist in map, it means it is not  empty. It is allocated before
			for _, v := range oo.SOnuPersistentData.PersTcontMap {
				if v == instID {
					instExist = true
					break
				}
			}
			if !instExist {
				oo.SOnuPersistentData.PersTcontMap[allocID] = instID
				return instID, false, nil
			}
		}
	}
	return 0, false, fmt.Errorf("no-free-tcont-left-for-device-%s", oo.deviceID)
}

// FreeTcont - TODO: add comment
func (oo *OnuDeviceEntry) FreeTcont(ctx context.Context, allocID uint16) {
	logger.Debugw(ctx, "free-tcont", log.Fields{"device-id": oo.deviceID, "alloc": allocID})
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()
	delete(oo.SOnuPersistentData.PersTcontMap, allocID)
}

// GetDevOmciCC - TODO: add comment
func (oo *OnuDeviceEntry) GetDevOmciCC() *cmn.OmciCC {
	return oo.PDevOmciCC
}

// GetOnuDB - TODO: add comment
func (oo *OnuDeviceEntry) GetOnuDB() *devdb.OnuDeviceDB {
	return oo.pOnuDB
}

// GetPersSerialNumber - TODO: add comment
func (oo *OnuDeviceEntry) GetPersSerialNumber() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	value := oo.SOnuPersistentData.PersSerialNumber
	return value
}

// GetPersVendorID - TODO: add comment
func (oo *OnuDeviceEntry) GetPersVendorID() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	value := oo.SOnuPersistentData.PersVendorID
	return value
}

// GetPersIsExtOmciSupported - TODO: add comment
func (oo *OnuDeviceEntry) GetPersIsExtOmciSupported() bool {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	value := oo.SOnuPersistentData.PersIsExtOmciSupported
	return value
}

// GetPersVersion - TODO: add comment
func (oo *OnuDeviceEntry) GetPersVersion() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	value := oo.SOnuPersistentData.PersVersion
	return value
}

// GetPersEquipmentID - TODO: add comment
func (oo *OnuDeviceEntry) GetPersEquipmentID() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	value := oo.SOnuPersistentData.PersEquipmentID
	return value
}

// GetMibUploadFsmCommChan - TODO: add comment
func (oo *OnuDeviceEntry) GetMibUploadFsmCommChan() chan cmn.Message {
	return oo.PMibUploadFsm.CommChan
}

// GetMibDownloadFsmCommChan - TODO: add comment
func (oo *OnuDeviceEntry) GetMibDownloadFsmCommChan() chan cmn.Message {
	return oo.PMibDownloadFsm.CommChan
}

// GetOmciRebootMsgRevChan - TODO: add comment
func (oo *OnuDeviceEntry) GetOmciRebootMsgRevChan() chan cmn.Message {
	return oo.omciRebootMessageReceivedChannel
}

// GetPersActiveSwVersion - TODO: add comment
func (oo *OnuDeviceEntry) GetPersActiveSwVersion() string {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	return oo.SOnuPersistentData.PersActiveSwVersion
}

// SetPersActiveSwVersion - TODO: add comment
func (oo *OnuDeviceEntry) SetPersActiveSwVersion(value string) {
	oo.MutexPersOnuConfig.Lock()
	defer oo.MutexPersOnuConfig.Unlock()
	oo.SOnuPersistentData.PersActiveSwVersion = value
}

// setReconcilingFlows - TODO: add comment
func (oo *OnuDeviceEntry) setReconcilingFlows(value bool) {
	oo.mutexReconcilingFlowsFlag.Lock()
	oo.reconcilingFlows = value
	oo.mutexReconcilingFlowsFlag.Unlock()
}

// SendChReconcilingFlowsFinished - TODO: add comment
func (oo *OnuDeviceEntry) SendChReconcilingFlowsFinished(ctx context.Context, value bool) {
	if oo != nil { //if the object still exists (might have been already deleted in background)
		// wait for some time before exiting, as the receiver might not have started and will lead to Mib reset if this signal is missed
		expiry := vlanConfigSendChanExpiry * time.Second
		select {
		case oo.chReconcilingFlowsFinished <- value:
			logger.Info(ctx, "reconciling - flows finished sent", log.Fields{"device-id": oo.deviceID})
		case <-time.After(expiry):
			logger.Infow(ctx, "reconciling - timer expired, flows finished not sent!", log.Fields{"device-id": oo.deviceID})
		}
	}
}

// isReconcilingFlows - TODO: add comment
func (oo *OnuDeviceEntry) isReconcilingFlows() bool {
	oo.mutexReconcilingFlowsFlag.RLock()
	value := oo.reconcilingFlows
	oo.mutexReconcilingFlowsFlag.RUnlock()
	return value
}

// PrepareForGarbageCollection - remove references to prepare for garbage collection
func (oo *OnuDeviceEntry) PrepareForGarbageCollection(ctx context.Context, aDeviceID string) {
	logger.Debugw(ctx, "prepare for garbage collection", log.Fields{"device-id": aDeviceID})
	oo.baseDeviceHandler = nil
	oo.pOnuTP = nil
	if oo.PDevOmciCC != nil {
		oo.PDevOmciCC.PrepareForGarbageCollection(ctx, aDeviceID)
	}
	oo.PDevOmciCC = nil
}

// SendOnuDeviceEvent sends an ONU DeviceEvent via eventProxy
func (oo *OnuDeviceEntry) SendOnuDeviceEvent(ctx context.Context, aDeviceEventName string, aDescription string) {

	oo.MutexPersOnuConfig.RLock()
	context := make(map[string]string)
	context["onu-id"] = strconv.FormatUint(uint64(oo.SOnuPersistentData.PersOnuID), 10)
	context["intf-id"] = strconv.FormatUint(uint64(oo.SOnuPersistentData.PersIntfID), 10)
	context["onu-serial-number"] = oo.SOnuPersistentData.PersSerialNumber
	oo.MutexPersOnuConfig.RUnlock()

	deviceEvent := &voltha.DeviceEvent{
		ResourceId:      oo.deviceID,
		DeviceEventName: aDeviceEventName,
		Description:     aDescription,
		Context:         context,
	}
	logger.Debugw(ctx, "send device event", log.Fields{"deviceEvent": deviceEvent, "device-id": oo.deviceID})
	_ = oo.eventProxy.SendDeviceEvent(ctx, deviceEvent, voltha.EventCategory_COMMUNICATION, voltha.EventSubCategory_ONU, time.Now().Unix())
}

// IsMIBTemplateGenerated checks if a MIB Template is already present for this type of ONT.
func (oo *OnuDeviceEntry) IsMIBTemplateGenerated(ctx context.Context) bool {

	oo.pOpenOnuAc.LockMutexMibTemplateGenerated()
	defer oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()

	if _, exist := oo.pOpenOnuAc.GetMibTemplatesGenerated(oo.mibTemplatePath); !exist {
		logger.Infow(ctx, "MIB template not Generated , further proceed to do MIB sync upload ", log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
		return false
	}
	return true
}
