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
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
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
	ulStInSync                 = "ulStInSync"
	ulStExaminingMds           = "ulStExaminingMds"
	ulStResynchronizing        = "ulStResynchronizing"
	ulStAuditing               = "ulStAuditing"
	ulStOutOfSync              = "ulStOutOfSync"
)

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

const (
	cBasePathMibTemplateKvStore = "service/voltha/omci_mibs/go_templates"
	cSuffixMibTemplateKvStore   = "%s/%s/%s"
	cBasePathOnuKVStore         = "service/voltha/openonu"
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
	// OmciVlanFilterAddDone - Omci Vlan config done according to flow-add
	OmciVlanFilterAddDone
	// OmciVlanFilterRemDone - Omci Vlan config done according to flow-remove
	OmciVlanFilterRemDone // needs to be the successor of OmciVlanFilterAddDone!
	// Add other events here as needed (alarms separate???)
)

type activityDescr struct {
	databaseClass func() error
	//advertiseEvents bool
	auditDelay uint16
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
func (oo *AdapterFsm) logFsmStateChange(e *fsm.Event) {
	logger.Debugw("FSM state change", log.Fields{"device-id": oo.deviceID, "FSM name": oo.fsmName,
		"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst)})
}

//OntDeviceEntry structure holds information about the attached FSM'as and their communication

const (
	firstSwImageMeID  = 0
	secondSwImageMeID = 1
)
const onugMeID = 0
const onu2gMeID = 0
const ipHostConfigDataMeID = 1
const onugSerialNumberLen = 8
const omciMacAddressLen = 6

const cEmptyMacAddrString = "000000000000"
const cEmptySerialNumberString = "0000000000000000"

type swImages struct {
	version  string
	isActive uint8
}

type uniPersConfig struct {
	PersUniID      uint8               `json:"uni_id"`
	PersTpPathMap  map[uint8]string    `json:"PersTpPathMap"` // tp-id to tp-path map
	PersFlowParams []uniVlanFlowParams `json:"flow_params"`   //as defined in omci_ani_config.go
}

type onuPersistentData struct {
	PersOnuID      uint32          `json:"onu_id"`
	PersIntfID     uint32          `json:"intf_id"`
	PersSnr        string          `json:"serial_number"`
	PersAdminState string          `json:"admin_state"`
	PersOperState  string          `json:"oper_state"`
	PersUniConfig  []uniPersConfig `json:"uni_config"`
}

// OnuDeviceEntry - ONU device info and FSM events.
type OnuDeviceEntry struct {
	deviceID              string
	baseDeviceHandler     *deviceHandler
	coreProxy             adapterif.CoreProxy
	adapterProxy          adapterif.AdapterProxy
	PDevOmciCC            *omciCC
	pOnuDB                *onuDeviceDB
	mibTemplateKVStore    *db.Backend
	sOnuPersistentData    onuPersistentData
	onuKVStoreMutex       sync.RWMutex
	onuKVStore            *db.Backend
	onuKVStorePath        string
	onuKVStoreprocResult  error //error indication of processing
	chOnuKvProcessingStep chan uint8
	vendorID              string
	serialNumber          string
	equipmentID           string
	swImages              [secondSwImageMeID + 1]swImages
	activeSwVersion       string
	macAddress            string
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
	omciMessageReceived              chan bool    //seperate channel needed by DownloadFsm
	omciRebootMessageReceivedChannel chan Message // channel needed by Reboot request
}

//newOnuDeviceEntry returns a new instance of a OnuDeviceEntry
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newOnuDeviceEntry(ctx context.Context, deviceID string, kVStoreHost string, kVStorePort int, kvStoreType string, deviceHandler *deviceHandler,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy,
	supportedFsmsPtr *OmciDeviceFsms) *OnuDeviceEntry {
	logger.Infow("init-onuDeviceEntry", log.Fields{"device-id": deviceID})
	var onuDeviceEntry OnuDeviceEntry
	onuDeviceEntry.deviceID = deviceID
	onuDeviceEntry.baseDeviceHandler = deviceHandler
	onuDeviceEntry.coreProxy = coreProxy
	onuDeviceEntry.adapterProxy = adapterProxy
	onuDeviceEntry.devState = DeviceStatusInit
	onuDeviceEntry.sOnuPersistentData.PersUniConfig = make([]uniPersConfig, 0)
	onuDeviceEntry.chOnuKvProcessingStep = make(chan uint8)
	onuDeviceEntry.omciRebootMessageReceivedChannel = make(chan Message, 2048)
	//openomciagent.lockDeviceHandlersMap = sync.RWMutex{}
	//OMCI related databases are on a per-agent basis. State machines and tasks
	//are per ONU Vendor
	//
	// MIB Synchronization Database - possible overloading from arguments
	if supportedFsmsPtr != nil {
		onuDeviceEntry.supportedFsms = *supportedFsmsPtr
	} else {
		//var mibSyncFsm = NewMibSynchronizer()
		// use some internaö defaults, if not defined from outside
		onuDeviceEntry.supportedFsms = OmciDeviceFsms{
			"mib-synchronizer": {
				//mibSyncFsm,        // Implements the MIB synchronization state machine
				onuDeviceEntry.mibDbVolatileDict, // Implements volatile ME MIB database
				//true,                             // Advertise events on OpenOMCI event bus
				60, // Time to wait between MIB audits.  0 to disable audits.
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
	onuDeviceEntry.pMibUploadFsm = NewAdapterFsm("MibUpload", deviceID, mibUploadChan)
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

			{Name: ulEvSuccess, Src: []string{ulStGettingMibTemplate}, Dst: ulStInSync},
			{Name: ulEvSuccess, Src: []string{ulStUploading}, Dst: ulStInSync},

			{Name: ulEvSuccess, Src: []string{ulStExaminingMds}, Dst: ulStInSync},
			{Name: ulEvMismatch, Src: []string{ulStExaminingMds}, Dst: ulStResynchronizing},

			{Name: ulEvAuditMib, Src: []string{ulStInSync}, Dst: ulStAuditing},

			{Name: ulEvSuccess, Src: []string{ulStOutOfSync}, Dst: ulStInSync},
			{Name: ulEvAuditMib, Src: []string{ulStOutOfSync}, Dst: ulStAuditing},

			{Name: ulEvSuccess, Src: []string{ulStAuditing}, Dst: ulStInSync},
			{Name: ulEvMismatch, Src: []string{ulStAuditing}, Dst: ulStResynchronizing},
			{Name: ulEvForceResync, Src: []string{ulStAuditing}, Dst: ulStResynchronizing},

			{Name: ulEvSuccess, Src: []string{ulStResynchronizing}, Dst: ulStInSync},
			{Name: ulEvDiffsFound, Src: []string{ulStResynchronizing}, Dst: ulStOutOfSync},

			{Name: ulEvTimeout, Src: []string{ulStResettingMib, ulStGettingVendorAndSerial, ulStGettingEquipmentID, ulStGettingFirstSwVersion,
				ulStGettingSecondSwVersion, ulStGettingMacAddress, ulStGettingMibTemplate, ulStUploading, ulStResynchronizing, ulStExaminingMds,
				ulStInSync, ulStOutOfSync, ulStAuditing}, Dst: ulStStarting},

			{Name: ulEvStop, Src: []string{ulStStarting, ulStResettingMib, ulStGettingVendorAndSerial, ulStGettingEquipmentID, ulStGettingFirstSwVersion,
				ulStGettingSecondSwVersion, ulStGettingMacAddress, ulStGettingMibTemplate, ulStUploading, ulStResynchronizing, ulStExaminingMds,
				ulStInSync, ulStOutOfSync, ulStAuditing}, Dst: ulStDisabled},
		},

		fsm.Callbacks{
			"enter_state":                         func(e *fsm.Event) { onuDeviceEntry.pMibUploadFsm.logFsmStateChange(e) },
			"enter_" + ulStStarting:               func(e *fsm.Event) { onuDeviceEntry.enterStartingState(e) },
			"enter_" + ulStResettingMib:           func(e *fsm.Event) { onuDeviceEntry.enterResettingMibState(e) },
			"enter_" + ulStGettingVendorAndSerial: func(e *fsm.Event) { onuDeviceEntry.enterGettingVendorAndSerialState(e) },
			"enter_" + ulStGettingEquipmentID:     func(e *fsm.Event) { onuDeviceEntry.enterGettingEquipmentIDState(e) },
			"enter_" + ulStGettingFirstSwVersion:  func(e *fsm.Event) { onuDeviceEntry.enterGettingFirstSwVersionState(e) },
			"enter_" + ulStGettingSecondSwVersion: func(e *fsm.Event) { onuDeviceEntry.enterGettingSecondSwVersionState(e) },
			"enter_" + ulStGettingMacAddress:      func(e *fsm.Event) { onuDeviceEntry.enterGettingMacAddressState(e) },
			"enter_" + ulStGettingMibTemplate:     func(e *fsm.Event) { onuDeviceEntry.enterGettingMibTemplate(e) },
			"enter_" + ulStUploading:              func(e *fsm.Event) { onuDeviceEntry.enterUploadingState(e) },
			"enter_" + ulStExaminingMds:           func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsState(e) },
			"enter_" + ulStResynchronizing:        func(e *fsm.Event) { onuDeviceEntry.enterResynchronizingState(e) },
			"enter_" + ulStAuditing:               func(e *fsm.Event) { onuDeviceEntry.enterAuditingState(e) },
			"enter_" + ulStOutOfSync:              func(e *fsm.Event) { onuDeviceEntry.enterOutOfSyncState(e) },
			"enter_" + ulStInSync:                 func(e *fsm.Event) { onuDeviceEntry.enterInSyncState(e) },
		},
	)
	// Omci related Mib download state machine
	mibDownloadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibDownloadFsm = NewAdapterFsm("MibDownload", deviceID, mibDownloadChan)
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
			"enter_state":               func(e *fsm.Event) { onuDeviceEntry.pMibDownloadFsm.logFsmStateChange(e) },
			"enter_" + dlStStarting:     func(e *fsm.Event) { onuDeviceEntry.enterDLStartingState(e) },
			"enter_" + dlStCreatingGal:  func(e *fsm.Event) { onuDeviceEntry.enterCreatingGalState(e) },
			"enter_" + dlStSettingOnu2g: func(e *fsm.Event) { onuDeviceEntry.enterSettingOnu2gState(e) },
			"enter_" + dlStBridgeInit:   func(e *fsm.Event) { onuDeviceEntry.enterBridgeInitState(e) },
			"enter_" + dlStDownloaded:   func(e *fsm.Event) { onuDeviceEntry.enterDownloadedState(e) },
			"enter_" + dlStResetting:    func(e *fsm.Event) { onuDeviceEntry.enterResettingState(e) },
		},
	)
	if onuDeviceEntry.pMibDownloadFsm == nil || onuDeviceEntry.pMibDownloadFsm.pFsm == nil {
		logger.Errorw("MibDownloadFsm could not be instantiated", log.Fields{"device-id": deviceID})
		// TODO some specifc error treatment - or waiting for crash ?
	}

	onuDeviceEntry.mibTemplateKVStore = onuDeviceEntry.baseDeviceHandler.setBackend(cBasePathMibTemplateKvStore)
	if onuDeviceEntry.mibTemplateKVStore == nil {
		logger.Errorw("Can't access mibTemplateKVStore - no backend connection to service",
			log.Fields{"device-id": deviceID, "service": cBasePathMibTemplateKvStore})
	}

	onuDeviceEntry.onuKVStorePath = onuDeviceEntry.deviceID
	onuDeviceEntry.onuKVStore = onuDeviceEntry.baseDeviceHandler.setBackend(cBasePathOnuKVStore)
	if onuDeviceEntry.onuKVStore == nil {
		logger.Errorw("Can't access onuKVStore - no backend connection to service",
			log.Fields{"device-id": deviceID, "service": cBasePathOnuKVStore})
	}

	// Alarm Synchronization Database
	//self._alarm_db = None
	//self._alarm_database_cls = support_classes['alarm-synchronizer']['database']
	return &onuDeviceEntry
}

//start starts (logs) the omci agent
func (oo *OnuDeviceEntry) start(ctx context.Context) error {
	logger.Infow("OnuDeviceEntry-starting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC == nil {
		oo.PDevOmciCC = newOmciCC(ctx, oo, oo.deviceID, oo.baseDeviceHandler,
			oo.coreProxy, oo.adapterProxy)
		if oo.PDevOmciCC == nil {
			logger.Errorw("Could not create devOmciCc - abort", log.Fields{"for device-id": oo.deviceID})
			return fmt.Errorf("could not create devOmciCc %s", oo.deviceID)
		}
	}
	return nil
}

//stop stops/resets the omciCC
func (oo *OnuDeviceEntry) stop(ctx context.Context, abResetOmciCC bool) error {
	logger.Debugw("OnuDeviceEntry-stopping", log.Fields{"for device-id": oo.deviceID})
	if abResetOmciCC && (oo.PDevOmciCC != nil) {
		_ = oo.PDevOmciCC.stop(ctx)
	}
	//to allow for all event notifications again when re-using the device and omciCC
	oo.devState = DeviceStatusInit
	return nil
}

func (oo *OnuDeviceEntry) reboot(ctx context.Context) error {
	logger.Debugw("OnuDeviceEntry-rebooting", log.Fields{"for device-id": oo.deviceID})
	if oo.PDevOmciCC != nil {
		if err := oo.PDevOmciCC.sendReboot(ctx, ConstDefaultOmciTimeout, true, oo.omciRebootMessageReceivedChannel); err != nil {
			logger.Errorw("onu didn't reboot", log.Fields{"for device-id": oo.deviceID})
			return err
		}
	}
	return nil
}

func (oo *OnuDeviceEntry) waitForRebootResponse(responseChannel chan Message) error {
	select {
	case <-time.After(3 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw("Reboot timeout", log.Fields{"for device-id": oo.deviceID})
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
				logger.Debugw("RebootResponse data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				if msgObj.Result != me.Success {
					logger.Errorw("Omci RebootResponse result error", log.Fields{"device-id": oo.deviceID, "Error": msgObj.Result})
					// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
					return fmt.Errorf("omci RebootResponse result error indication %s for device %s",
						msgObj.Result, oo.deviceID)
				}
				return nil
			}
		}
		logger.Warnw("Reboot response message type error", log.Fields{"for device-id": oo.deviceID})
		return fmt.Errorf("unexpected OmciResponse type received %s", oo.deviceID)
	}
}

//Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(devEvent OnuDeviceEvent) {
	logger.Debugw("relaying system-event", log.Fields{"Event": devEvent})
	// decouple the handler transfer from further processing here
	// TODO!!! check if really no synch is required within the system e.g. to ensure following steps ..
	if devEvent == MibDatabaseSync {
		if oo.devState < MibDatabaseSync { //devState has not been synced yet
			oo.devState = MibDatabaseSync
			go oo.baseDeviceHandler.deviceProcStatusUpdate(devEvent)
			//TODO!!! device control: next step: start MIB capability verification from here ?!!!
		} else {
			logger.Debugw("mibinsync-event in some already synced state - ignored", log.Fields{"state": oo.devState})
		}
	} else if devEvent == MibDownloadDone {
		if oo.devState < MibDownloadDone { //devState has not been synced yet
			oo.devState = MibDownloadDone
			go oo.baseDeviceHandler.deviceProcStatusUpdate(devEvent)
		} else {
			logger.Debugw("mibdownloaddone-event was already seen - ignored", log.Fields{"state": oo.devState})
		}
	} else {
		logger.Warnw("device-event not yet handled", log.Fields{"state": devEvent})
	}
}

func (oo *OnuDeviceEntry) restoreDataFromOnuKvStore(ctx context.Context) error {
	if oo.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf(fmt.Sprintf("onuKVStore-not-set-abort-%s", oo.deviceID))
	}
	oo.sOnuPersistentData = onuPersistentData{0, 0, "", "", "", make([]uniPersConfig, 0)}
	Value, err := oo.onuKVStore.Get(ctx, oo.onuKVStorePath)
	if err == nil {
		if Value != nil {
			logger.Debugw("ONU-data read",
				log.Fields{"Key": Value.Key, "device-id": oo.deviceID})
			tmpBytes, _ := kvstore.ToByte(Value.Value)

			if err = json.Unmarshal(tmpBytes, &oo.sOnuPersistentData); err != nil {
				logger.Errorw("unable to unmarshal ONU-data", log.Fields{"error": err, "device-id": oo.deviceID})
				return fmt.Errorf(fmt.Sprintf("unable-to-unmarshal-ONU-data-%s", oo.deviceID))
			}
			logger.Debugw("ONU-data", log.Fields{"sOnuPersistentData": oo.sOnuPersistentData,
				"device-id": oo.deviceID})
		} else {
			logger.Debugw("no ONU-data found", log.Fields{"path": oo.onuKVStorePath, "device-id": oo.deviceID})
			return fmt.Errorf("no-ONU-data-found")
		}
	} else {
		logger.Errorw("unable to read from KVstore", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf(fmt.Sprintf("unable-to-read-from-KVstore-%s", oo.deviceID))
	}
	return nil
}

func (oo *OnuDeviceEntry) deleteDataFromOnuKvStore(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if oo.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data delete aborted: onuKVStore not set")
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chOnuKvProcessingStep
	go oo.deletePersistentData(ctx, processingStep)
	if !oo.waitForTimeoutOrCompletion(ctx, oo.chOnuKvProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw("ONU-data not deleted - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data delete aborted: during kv-access")
		return
	}
}

func (oo *OnuDeviceEntry) deletePersistentData(ctx context.Context, aProcessingStep uint8) {

	logger.Debugw("delete and clear internal persistency data", log.Fields{"device-id": oo.deviceID})
	oo.sOnuPersistentData.PersUniConfig = nil                                             //releasing all UniConfig entries to garbage collector
	oo.sOnuPersistentData = onuPersistentData{0, 0, "", "", "", make([]uniPersConfig, 0)} //default entry

	logger.Debugw("delete ONU-data from KVStore", log.Fields{"device-id": oo.deviceID})
	err := oo.onuKVStore.Delete(ctx, oo.onuKVStorePath)
	if err != nil {
		logger.Errorw("unable to delete in KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	oo.chOnuKvProcessingStep <- aProcessingStep //done
}

func (oo *OnuDeviceEntry) updateOnuKvStore(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if oo.onuKVStore == nil {
		logger.Debugw("onuKVStore not set - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data update aborted: onuKVStore not set")
		return
	}
	var processingStep uint8 = 1 // used to synchronize the different processing steps with chOnuKvProcessingStep
	go oo.storeDataInOnuKvStore(ctx, processingStep)
	if !oo.waitForTimeoutOrCompletion(ctx, oo.chOnuKvProcessingStep, processingStep) {
		//timeout or error detected
		logger.Debugw("ONU-data not written - abort", log.Fields{"device-id": oo.deviceID})
		oo.onuKVStoreprocResult = errors.New("onu-data update aborted: during writing process")
		return
	}
}

func (oo *OnuDeviceEntry) storeDataInOnuKvStore(ctx context.Context, aProcessingStep uint8) {

	//assign values which are not already present when newOnuDeviceEntry() is called
	oo.sOnuPersistentData.PersOnuID = oo.baseDeviceHandler.pOnuIndication.OnuId
	oo.sOnuPersistentData.PersIntfID = oo.baseDeviceHandler.pOnuIndication.IntfId
	oo.sOnuPersistentData.PersSnr = oo.baseDeviceHandler.pOnuOmciDevice.serialNumber
	//TODO: verify usage of these values during restart UC
	oo.sOnuPersistentData.PersAdminState = "up"
	oo.sOnuPersistentData.PersOperState = "active"

	logger.Debugw("Update ONU-data in KVStore", log.Fields{"device-id": oo.deviceID, "sOnuPersistentData": oo.sOnuPersistentData})

	Value, err := json.Marshal(oo.sOnuPersistentData)
	if err != nil {
		logger.Errorw("unable to marshal ONU-data", log.Fields{"sOnuPersistentData": oo.sOnuPersistentData,
			"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	err = oo.onuKVStore.Put(ctx, oo.onuKVStorePath, Value)
	if err != nil {
		logger.Errorw("unable to write ONU-data into KVstore", log.Fields{"device-id": oo.deviceID, "err": err})
		oo.chOnuKvProcessingStep <- 0 //error indication
		return
	}
	oo.chOnuKvProcessingStep <- aProcessingStep //done
}

func (oo *OnuDeviceEntry) updateOnuUniTpPath(aUniID uint8, aTpId uint8, aPathString string) bool {
	/* within some specific InterAdapter processing request write/read access to data is ensured to be sequentially,
	   as also the complete sequence is ensured to 'run to completion' before some new request is accepted
	   no specific concurrency protection to sOnuPersistentData is required here
	*/
	for k, v := range oo.sOnuPersistentData.PersUniConfig {
		if v.PersUniID == aUniID {
			logger.Debugw("PersUniConfig-entry already exists", log.Fields{"device-id": oo.deviceID, "uniID": aUniID})
			existingPath, ok := oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpId]
			if !ok {
				logger.Errorw("tp-id-not-found-in-map", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "tpID": aTpId, "path": aPathString})
				return false
			}
			if existingPath != aPathString {
				if aPathString == "" {
					//existing entry to be deleted
					logger.Debugw("UniTp delete path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpId] = ""
				} else {
					//existing entry to be modified
					logger.Debugw("UniTp modify path value", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
					oo.sOnuPersistentData.PersUniConfig[k].PersTpPathMap[aTpId] = aPathString
				}
				return true
			}
			//entry already exists
			if aPathString == "" {
				//no active TechProfile
				logger.Debugw("UniTp path has already been removed - no AniSide config to be removed", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID})
				// attention 201105: this block is at the moment entered for each of subsequent GemPortDeletes and TContDelete
				//   as the path is already cleared with the first GemPort - this will probably change with the upcoming real
				//   TechProfile removal (still TODO), but anyway the reasonUpdate initiated here should not harm overall behavior
				go oo.baseDeviceHandler.deviceProcStatusUpdate(OmciAniResourceRemoved)
				// no flow config pending on 'remove' so far
			} else {
				//the given TechProfile already exists and is assumed to be active - update devReason as if the config has been done here
				//was needed e.g. in voltha POD Tests:Validate authentication on a disabled ONU
				//  (as here the TechProfile has not been removed with the disable-device before the new enable-device)
				logger.Debugw("UniTp path already exists - TechProfile supposed to be active", log.Fields{
					"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
				//no deviceReason update (deviceProcStatusUpdate) here to ensure 'omci_flows_pushed' state within disable/enable procedure of ATT scenario
				//  (during which the flows are removed/re-assigned but the techProf is left active)
				//and as the TechProfile is regarded as active we have to verify, if some flow configuration still waits on it
				//  (should not be the case, but should not harm or be more robust ...)
				// and to be sure, that for some reason the corresponding TpDelete was lost somewhere in history
				//  we also reset a possibly outstanding delete request - repeated TpConfig is regarded as valid for waiting flow config
				if oo.baseDeviceHandler.pOnuTP != nil {
					oo.baseDeviceHandler.pOnuTP.setProfileToDelete(aUniID, aTpId, false)
				}
				go oo.baseDeviceHandler.VerifyVlanConfigRequest(aUniID)
			}
			return false //indicate 'no change' - nothing more to do, TechProf inter-adapter message is return with success anyway here
		}
	}
	//no entry exists for uniId

	if aPathString == "" {
		//delete request in non-existing state , accept as no change
		logger.Debugw("UniTp path already removed", log.Fields{"device-id": oo.deviceID, "uniID": aUniID})
		return false
	}
	//new entry to be created
	logger.Debugw("New UniTp path set", log.Fields{"device-id": oo.deviceID, "uniID": aUniID, "path": aPathString})
	perSubTpPathMap := make(map[uint8]string)
	perSubTpPathMap[aTpId] = aPathString
	oo.sOnuPersistentData.PersUniConfig =
		append(oo.sOnuPersistentData.PersUniConfig, uniPersConfig{PersUniID: aUniID, PersTpPathMap: perSubTpPathMap, PersFlowParams: make([]uniVlanFlowParams, 0)})
	return true
}

func (oo *OnuDeviceEntry) updateOnuUniFlowConfig(aUniID uint8, aUniVlanFlowParams *[]uniVlanFlowParams) {

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
		logger.Warnw("processing not completed in-time!",
			log.Fields{"device-id": oo.deviceID, "error": ctx.Err()})
		return false
	case rxStep := <-aChOnuProcessingStep:
		if rxStep == aProcessingStep {
			return true
		}
		//all other values are not accepted - including 0 for error indication
		logger.Warnw("Invalid processing step received: abort!",
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

func (oo *OnuDeviceEntry) lockOnuKVStoreMutex() {
	oo.onuKVStoreMutex.Lock()
}

func (oo *OnuDeviceEntry) unlockOnuKVStoreMutex() {
	oo.onuKVStoreMutex.Unlock()
}
