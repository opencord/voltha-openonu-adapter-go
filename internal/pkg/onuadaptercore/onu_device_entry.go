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
	"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"

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
	OmciAniConfigDone    OnuDeviceEvent = 9 // AniSide config according to TechProfile done
	// Add other events here as needed (alarms separate???)
)

type activityDescr struct {
	databaseClass func() error
	//advertiseEvents bool
	auditDelay uint16
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

const (
	FirstSwImageMeID  = 0
	SecondSwImageMeID = 1
)
const OnugMeID = 0
const Onu2gMeID = 0
const IPHostConfigDataMeID = 1
const OnugSerialNumberLen = 8
const OmciMacAddressLen = 6

type SwImages struct {
	version  string
	isActive uint8
}

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
	swImages           [SecondSwImageMeID + 1]SwImages
	activeSwVersion    string
	macAddress         string
	//lockDeviceEntries           sync.RWMutex
	mibDbClass    func() error
	supportedFsms OmciDeviceFsms
	devState      OnuDeviceEvent
	// for mibUpload
	mibAuditDelay uint16
	mibDebugLevel string

	// for mibUpload
	pMibUploadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	// for mibDownload
	pMibDownloadFsm *AdapterFsm //could be handled dynamically and more general as pAdapterFsm - perhaps later
	//remark: general usage of pAdapterFsm would require generalization of commChan  usage and internal event setting
	//  within the FSM event procedures
	omciMessageReceived              chan bool    //seperate channel needed by DownloadFsm
	omciRebootMessageReceivedChannel chan Message // channel needed by Reboot request
}

//OnuDeviceEntry returns a new instance of a OnuDeviceEntry
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOnuDeviceEntry(ctx context.Context, device_id string, kVStoreHost string, kVStorePort int, kvStoreType string, device_Handler *DeviceHandler,
	core_proxy adapterif.CoreProxy, adapter_proxy adapterif.AdapterProxy,
	supported_Fsms_Ptr *OmciDeviceFsms) *OnuDeviceEntry {
	logger.Infow("init-onuDeviceEntry", log.Fields{"device-id": device_id})
	var onuDeviceEntry OnuDeviceEntry
	onuDeviceEntry.started = false
	onuDeviceEntry.deviceID = device_id
	onuDeviceEntry.baseDeviceHandler = device_Handler
	onuDeviceEntry.coreProxy = core_proxy
	onuDeviceEntry.adapterProxy = adapter_proxy
	onuDeviceEntry.devState = DeviceStatusInit
	onuDeviceEntry.omciRebootMessageReceivedChannel = make(chan Message, 2048)
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

	onuDeviceEntry.mibDebugLevel = "normal" //set to "verbose" if you want to have all output, possibly later also per config option!
	// Omci related Mib upload sync state machine
	mibUploadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibUploadFsm = NewAdapterFsm("MibUpload", device_id, mibUploadChan)
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
			"enter_state":                           func(e *fsm.Event) { onuDeviceEntry.pMibUploadFsm.logFsmStateChange(e) },
			("enter_" + ulStStarting):               func(e *fsm.Event) { onuDeviceEntry.enterStartingState(e) },
			("enter_" + ulStResettingMib):           func(e *fsm.Event) { onuDeviceEntry.enterResettingMibState(e) },
			("enter_" + ulStGettingVendorAndSerial): func(e *fsm.Event) { onuDeviceEntry.enterGettingVendorAndSerialState(e) },
			("enter_" + ulStGettingEquipmentID):     func(e *fsm.Event) { onuDeviceEntry.enterGettingEquipmentIDState(e) },
			("enter_" + ulStGettingFirstSwVersion):  func(e *fsm.Event) { onuDeviceEntry.enterGettingFirstSwVersionState(e) },
			("enter_" + ulStGettingSecondSwVersion): func(e *fsm.Event) { onuDeviceEntry.enterGettingSecondSwVersionState(e) },
			("enter_" + ulStGettingMacAddress):      func(e *fsm.Event) { onuDeviceEntry.enterGettingMacAddressState(e) },
			("enter_" + ulStGettingMibTemplate):     func(e *fsm.Event) { onuDeviceEntry.enterGettingMibTemplate(e) },
			("enter_" + ulStUploading):              func(e *fsm.Event) { onuDeviceEntry.enterUploadingState(e) },
			("enter_" + ulStExaminingMds):           func(e *fsm.Event) { onuDeviceEntry.enterExaminingMdsState(e) },
			("enter_" + ulStResynchronizing):        func(e *fsm.Event) { onuDeviceEntry.enterResynchronizingState(e) },
			("enter_" + ulStAuditing):               func(e *fsm.Event) { onuDeviceEntry.enterAuditingState(e) },
			("enter_" + ulStOutOfSync):              func(e *fsm.Event) { onuDeviceEntry.enterOutOfSyncState(e) },
			("enter_" + ulStInSync):                 func(e *fsm.Event) { onuDeviceEntry.enterInSyncState(e) },
		},
	)
	// Omci related Mib download state machine
	mibDownloadChan := make(chan Message, 2048)
	onuDeviceEntry.pMibDownloadFsm = NewAdapterFsm("MibDownload", device_id, mibDownloadChan)
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
			"enter_state":                 func(e *fsm.Event) { onuDeviceEntry.pMibDownloadFsm.logFsmStateChange(e) },
			("enter_" + dlStStarting):     func(e *fsm.Event) { onuDeviceEntry.enterDLStartingState(e) },
			("enter_" + dlStCreatingGal):  func(e *fsm.Event) { onuDeviceEntry.enterCreatingGalState(e) },
			("enter_" + dlStSettingOnu2g): func(e *fsm.Event) { onuDeviceEntry.enterSettingOnu2gState(e) },
			("enter_" + dlStBridgeInit):   func(e *fsm.Event) { onuDeviceEntry.enterBridgeInitState(e) },
			("enter_" + dlStDownloaded):   func(e *fsm.Event) { onuDeviceEntry.enterDownloadedState(e) },
			("enter_" + dlStResetting):    func(e *fsm.Event) { onuDeviceEntry.enterResettingState(e) },
		},
	)
	if onuDeviceEntry.pMibDownloadFsm == nil || onuDeviceEntry.pMibDownloadFsm.pFsm == nil {
		logger.Error("MibDownloadFsm could not be instantiated!!")
		// some specifc error treatment - or waiting for crash ???
	}

	onuDeviceEntry.mibTemplateKVStore = onuDeviceEntry.baseDeviceHandler.SetBackend(cBasePathMibTemplateKvStore)
	if onuDeviceEntry.mibTemplateKVStore == nil {
		logger.Errorw("Failed to setup mibTemplateKVStore", log.Fields{"device-id": device_id})
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
		logger.Errorw("Could not create devOmciCc - abort", log.Fields{"for device-id": oo.deviceID})
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
	// maybe also the omciCC should be stopped here - for now not as no real processing is expected here - maybe needs consolidation
	logger.Info("OnuDeviceEntry-stopped")
	return nil
}

func (oo *OnuDeviceEntry) Reboot(ctx context.Context) error {
	logger.Info("reboot-OnuDeviceEntry")
	if err := oo.PDevOmciCC.sendReboot(context.TODO(), ConstDefaultOmciTimeout, true, oo.omciRebootMessageReceivedChannel); err != nil {
		logger.Errorw("onu didn't reboot", log.Fields{"for device-id": oo.deviceID})
		return err
	}
	logger.Info("OnuDeviceEntry-reboot")
	return nil
}

func (oo *OnuDeviceEntry) waitForRebootResponse(responseChannel chan Message) error {
	select {
	case <-time.After(3 * time.Second): //3s was detected to be to less in 8*8 bbsim test with debug Info/Debug
		logger.Warnw("Reboot timeout", log.Fields{"for device-id": oo.deviceID})
		return errors.New("RebootTimeout")
	case data := <-responseChannel:
		switch data.Data.(OmciMessage).OmciMsg.MessageType {
		case omci.RebootResponseType:
			{
				msgLayer := (*data.Data.(OmciMessage).OmciPacket).Layer(omci.LayerTypeRebootResponse)
				if msgLayer == nil {
					return errors.New("Omci Msg layer could not be detected for RebootResponseType")
				}
				msgObj, msgOk := msgLayer.(*omci.GetResponse)
				if !msgOk {
					return errors.New("Omci Msg layer could not be assigned for RebootResponseType")
				}
				logger.Debugw("CreateResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				if msgObj.Result != me.Success {
					logger.Errorw("Omci RebootResponseType Error ", log.Fields{"Error": msgObj.Result})
					// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
					return errors.New("Omci RebootResponse Result Error indication")
				}
				return nil
			}
		}
		logger.Warnw("Reboot response error", log.Fields{"for device-id": oo.deviceID})
		return errors.New("Unexpected OmciResponse type received")
	}
}

//Relay the InSync message via Handler to Rw core - Status update
func (oo *OnuDeviceEntry) transferSystemEvent(dev_Event OnuDeviceEvent) {
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
}
