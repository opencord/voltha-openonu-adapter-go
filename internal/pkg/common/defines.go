/*
 * Copyright 2018-present Open Networking Foundation

 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at

 * http://www.apache.org/licenses/LICENSE-2.0

 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//Package common provides global definitions
package common

import (
	"context"
	"time"

	gp "github.com/google/gopacket"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go/v2"
	vc "github.com/opencord/voltha-protos/v5/go/common"
	ofp "github.com/opencord/voltha-protos/v5/go/openflow_13"
	"github.com/opencord/voltha-protos/v5/go/voltha"
)

// MessageType - Message Protocol Type
type MessageType uint8

const (
	// TestMsg - Message type for non OMCI messages
	TestMsg MessageType = iota
	//OMCI - OMCI protocol type msg
	OMCI
)

// String - Return the text representation of the message type based on integer
func (m MessageType) String() string {
	names := [...]string{
		"TestMsg",
		"OMCI",
	}
	return names[m]
}

// Message - message type and data(OMCI)
type Message struct {
	Type MessageType
	Data interface{}
}

//TestMessageType - message data for various events
type TestMessageType uint8

const (
	// LoadMibTemplateOk - message data for getting mib template successfully
	LoadMibTemplateOk TestMessageType = iota + 1
	// LoadMibTemplateFailed - message data for failure for getting mib template
	LoadMibTemplateFailed
	// TimeOutOccurred - message data for timeout
	TimeOutOccurred
	// AbortMessageProcessing - message data for aborting running message
	AbortMessageProcessing
)

//TestMessage - Struct to hold the message data
//TODO: place holder to have a second interface variant - to be replaced by real variant later on
type TestMessage struct {
	TestMessageVal TestMessageType
}

//OmciMessage - OMCI protocol messages for managing and monitoring ONUs
type OmciMessage struct {
	//OnuSN   *openolt.SerialNumber
	//OnuID   uint32
	OmciMsg    *omci.OMCI
	OmciPacket *gp.Packet
}

///////////////////////////////////////////////////////////

// device reasons
const (
	DrUnset                            = 0
	DrActivatingOnu                    = 1
	DrStartingOpenomci                 = 2
	DrDiscoveryMibsyncComplete         = 3
	DrInitialMibDownloaded             = 4
	DrTechProfileConfigDownloadSuccess = 5
	DrOmciFlowsPushed                  = 6
	DrOmciAdminLock                    = 7
	DrOnuReenabled                     = 8
	DrStoppingOpenomci                 = 9
	DrRebooting                        = 10
	DrOmciFlowsDeleted                 = 11
	DrTechProfileConfigDeleteSuccess   = 12
	DrReconcileFailed                  = 13
	DrReconcileMaxTimeout              = 14
	DrReconcileCanceled                = 15
	DrTechProfileConfigDownloadFailed  = 16
)

// DeviceReasonMap holds device reason strings
var DeviceReasonMap = map[uint8]string{
	DrUnset:                            "unset",
	DrActivatingOnu:                    "activating-onu",
	DrStartingOpenomci:                 "starting-openomci",
	DrDiscoveryMibsyncComplete:         "discovery-mibsync-complete",
	DrInitialMibDownloaded:             "initial-mib-downloaded",
	DrTechProfileConfigDownloadSuccess: "tech-profile-config-download-success",
	DrTechProfileConfigDownloadFailed:  "tech-profile-config-download-failed",
	DrOmciFlowsPushed:                  "omci-flows-pushed",
	DrOmciAdminLock:                    "omci-admin-lock",
	DrOnuReenabled:                     "onu-reenabled",
	DrStoppingOpenomci:                 "stopping-openomci",
	DrRebooting:                        "rebooting",
	DrOmciFlowsDeleted:                 "omci-flows-deleted",
	DrTechProfileConfigDeleteSuccess:   "tech-profile-config-delete-success",
	DrReconcileFailed:                  "reconcile-failed",
	DrReconcileMaxTimeout:              "reconcile-max-timeout",
	DrReconcileCanceled:                "reconciling-canceled",
}

// UsedOmciConfigFsms type for FSMs dealing with OMCI messages
type UsedOmciConfigFsms int

// FSMs dealing with OMCI messages
const (
	CUploadFsm UsedOmciConfigFsms = iota
	CDownloadFsm
	CUniLockFsm
	CUniUnLockFsm
	CAniConfigFsm
	CUniVlanConfigFsm
	CL2PmFsm
	COnuUpgradeFsm
)

// OnuDeviceEvent - TODO: add comment
type OnuDeviceEvent int

// Events of interest to Device Adapters and OpenOMCI State Machines
const (
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
	// UniEnableStateFailed - Uni ports admin set to unlock failure based on device re-enable
	UniEnableStateFailed
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

///////////////////////////////////////////////////////////

//definitions as per G.988 softwareImage::valid ME IDs
const (
	FirstSwImageMeID  = 0
	SecondSwImageMeID = 1
)

//definitions as per G.988 softwareImage::IsCommitted
const (
	SwIsUncommitted = 0
	SwIsCommitted   = 1
)

//definitions as per G.988 softwareImage::IsActive
const (
	SwIsInactive = 0
	SwIsActive   = 1
)

//definitions as per G.988 softwareImage::IsValid
const (
	SwIsInvalid = 0
	SwIsValid   = 1
)

// SEntrySwImageIndication - TODO: add comment
type SEntrySwImageIndication struct {
	Valid       bool
	EntityID    uint16
	Version     string
	IsCommitted uint8
}

// SswImageIndications - TODO: add comment
type SswImageIndications struct {
	ActiveEntityEntry   SEntrySwImageIndication
	InActiveEntityEntry SEntrySwImageIndication
}

///////////////////////////////////////////////////////////

type activityDescr struct {
	DatabaseClass func(context.Context) error
	//advertiseEvents bool
	AuditInterval time.Duration
	//tasks           map[string]func() error
}

// OmciDeviceFsms - FSM event mapping to database class and time to wait between audits
type OmciDeviceFsms map[string]activityDescr

// AdapterFsm - Adapter FSM details including channel, event and  device
type AdapterFsm struct {
	fsmName  string
	deviceID string
	CommChan chan Message
	PFsm     *fsm.FSM
}

//CErrWaitAborted - AdapterFsm related error string
//error string could be checked on waitforOmciResponse() e.g. to avoid misleading error log
// but not used that way so far (permit error log even for wanted cancellation)
const CErrWaitAborted = "waitResponse aborted"

///////////////////////////////////////////////////////////

// UniPortType holds possible UNI port types
type UniPortType uint8

// UniPPTP Interface type - re-use values from G.988 (Chapter 9.3.4) TP type definition (directly used in OMCI!)
const (
	// UniPPTP relates to PPTP
	UniPPTP UniPortType = 1 // relates to PPTP
	// UniVEIP relates to VEIP
	UniVEIP UniPortType = 11 // relates to VEIP
	// UniPPTPPots relates to PPTP POTS
	UniPPTPPots UniPortType = 4 // relates to IP host config data (for Voice Services)
)

//OnuUniPort structure holds information about the ONU attached Uni Ports
type OnuUniPort struct {
	Enabled    bool
	Name       string
	PortNo     uint32
	PortType   UniPortType
	OfpPortNo  string
	UniID      uint8
	MacBpNo    uint8
	EntityID   uint16
	AdminState vc.AdminState_Types
	OperState  vc.OperStatus_Types
	PPort      *voltha.Port
}

// OnuUniPortMap - TODO: add comment
type OnuUniPortMap map[uint32]*OnuUniPort

///////////////////////////////////////////////////////////

const (
	tpIDStart = 64
	tpIDEnd   = 256
	tpRange   = tpIDEnd - tpIDStart
	maxUni    = 256
)

// TODO
const (
	IeeMaperServiceProfileBaseEID = uint16(0x1001)
	MacBridgePortAniBaseEID       = uint16(0x1001)
	MacBridgePortUniBaseEID       = uint16(0x201)
	MacBridgePortAniMcastBaseEID  = uint16(0xA01)
	VoipUniBaseEID                = uint16(0x2001)
	GalEthernetEID                = uint16(1)
	MacBridgeServiceProfileEID    = uint16(0x201)
)

// UniVlanRuleParams - TODO: add comment
type UniVlanRuleParams struct {
	TpID         uint8  `json:"tp_id"`
	MatchVid     uint32 `json:"match_vid"` //use uint32 types for allowing immediate bitshifting
	MatchPcp     uint32 `json:"match_pcp"`
	TagsToRemove uint32 `json:"tags_to_remove"`
	SetVid       uint32 `json:"set_vid"`
	SetPcp       uint32 `json:"set_pcp"`
}

// UniVlanFlowParams - TODO: add comment
type UniVlanFlowParams struct {
	CookieSlice    []uint64            `json:"cookie_slice"`
	VlanRuleParams UniVlanRuleParams   `json:"vlan_rule_params"`
	Meter          *ofp.OfpMeterConfig `json:"flow_meter"`
	RespChan       *chan error         `json:"-"`
}

///////////////////////////////////////////////////////////

//definitions as per G.988
const (
	OnuDataMeID          = 0
	Onu2gMeID            = 0
	OnugMeID             = 0
	IPHostConfigDataMeID = 1
	OnugSerialNumberLen  = 8
	OmciMacAddressLen    = 6
)

///////////////////////////////////////////////////////////

// CBasePathOnuKVStore - kv store path of ONU specific data
const CBasePathOnuKVStore = "%s/openonu"
