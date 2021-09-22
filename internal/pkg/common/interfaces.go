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

//Package common provides global definitions
package common

import (
	"context"
	"time"

	"github.com/opencord/voltha-lib-go/v5/pkg/adapters/adapterif"
	"github.com/opencord/voltha-lib-go/v5/pkg/db"
	"github.com/opencord/voltha-lib-go/v5/pkg/events/eventif"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb"
	"github.com/opencord/voltha-protos/v4/go/openolt"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

// IopenONUAC interface to openONUAC
type IopenONUAC interface {
	GetSupportedFsms() *OmciDeviceFsms
	LockMutexMibTemplateGenerated()
	UnlockMutexMibTemplateGenerated()
	GetMibTemplatesGenerated(string) (bool, bool)
	SetMibTemplatesGenerated(string, bool)
	RLockMutexDeviceHandlersMap()
	RUnlockMutexDeviceHandlersMap()
	GetDeviceHandler(string) (IdeviceHandler, bool)
}

// IdeviceHandler interface to deviceHandler
type IdeviceHandler interface {
	GetDeviceID() string
	GetLogicalDeviceID() string
	GetDevice() *voltha.Device
	GetDeviceType() string
	GetAdapterTopic() string
	GetProxyAddressID() string
	GetProxyAddressType() string
	GetProxyAddress() *voltha.Device_ProxyAddress
	GetEventProxy() eventif.EventProxy
	GetCoreProxy() adapterif.CoreProxy
	GetOmciTimeout() int
	GetAlarmAuditInterval() time.Duration
	GetDlToOnuTimeout4M() time.Duration
	GetUniEntityMap() *OnuUniPortMap
	GetUniPortMask() int
	GetPonPortNumber() *uint32
	GetOnuIndication() *openolt.OnuIndication
	GetUniVlanConfigFsm(uint8) IuniVlanConfigFsm

	GetDeviceReasonString() string
	SetDeviceReason(uint8)

	GetCollectorIsRunning() bool
	StartCollector(context.Context)
	InitPmConfigs()
	GetPmConfigs() *voltha.PmConfigs
	GetMetricsEnabled() bool
	GetOnuMetricsManager() IonuMetricsManager
	GetOnuAlarmManager() IonuAlarmManager
	GetOnuTP() IonuUniTechProf

	GetAlarmManagerIsRunning(context.Context) bool
	StartAlarmManager(context.Context)

	CheckAuditStartCondition(context.Context, UsedOmciConfigFsms) bool

	RemoveOnuUpgradeFsm(context.Context, *voltha.ImageState)
	DeviceProcStatusUpdate(context.Context, OnuDeviceEvent)

	SetReadyForOmciConfig(bool)
	IsReadyForOmciConfig() bool

	StorePersistentData(context.Context) error
	StorePersUniFlowConfig(context.Context, uint8, *[]UniVlanFlowParams, bool) error

	StartReconciling(context.Context, bool)
	StopReconciling(context.Context, bool)
	IsReconciling() bool
	IsSkipOnuConfigReconciling() bool
	PrepareReconcilingWithActiveAdapter(context.Context)
	ReconcileDeviceTechProf(context.Context)
	ReconcileDeviceFlowConfig(context.Context)

	VerifyUniVlanConfigRequest(context.Context, *OnuUniPort, uint8)
	VerifyVlanConfigRequest(context.Context, uint8, uint8)
	AddAllUniPorts(context.Context)
	RemoveVlanFilterFsm(context.Context, *OnuUniPort)

	EnableUniPortStateUpdate(context.Context)
	DisableUniPortStateUpdate(context.Context)

	SetBackend(context.Context, string) *db.Backend
	GetBackendPathPrefix() string

	RLockMutexDeletionInProgressFlag()
	RUnlockMutexDeletionInProgressFlag()
	GetDeletionInProgress() bool
}

// IonuDeviceEntry interface to onuDeviceEntry
type IonuDeviceEntry interface {
	GetDevOmciCC() *OmciCC
	GetOnuDB() *devdb.OnuDeviceDB
	GetPersSerialNumber() string
	GetPersVendorID() string
	GetPersEquipmentID() string

	GetMibUploadFsmCommChan() chan Message
	GetMibDownloadFsmCommChan() chan Message

	GetOmciRebootMsgRevChan() chan Message
	WaitForRebootResponse(context.Context, chan Message) error

	IncrementMibDataSync(context.Context)

	GetActiveImageMeID(context.Context) (uint16, error)
	LockMutexOnuSwImageIndications()
	UnlockMutexOnuSwImageIndications()
	GetOnuSwImageIndications() SswImageIndications
	SetOnuSwImageIndications(SswImageIndications)
	GetPersActiveSwVersion() string
	SetPersActiveSwVersion(string)
	GetActiveImageVersion(context.Context) string
	ModifySwImageInactiveVersion(context.Context, string)
	ModifySwImageActiveCommit(context.Context, uint8)

	AllocateFreeTcont(context.Context, uint16) (uint16, bool, error)
	FreeTcont(context.Context, uint16)

	LockMutexPersOnuConfig()
	UnlockMutexPersOnuConfig()

	SetReconcilingFlows(bool)
	SetChReconcilingFlowsFinished(bool)
}

// IonuMetricsManager interface to onuMetricsManager
type IonuMetricsManager interface {
	AddGemPortForPerfMonitoring(context.Context, uint16)
	RemoveGemPortForPerfMonitoring(context.Context, uint16)
}

// IonuAlarmManager interface to onuAlarmManager
type IonuAlarmManager interface {
	HandleOmciAlarmNotificationMessage(context.Context, OmciMessage)
	ResetAlarmUploadCounters()
	GetAlarmMgrEventChannel() chan Message
	GetAlarmUploadSeqNo() uint16
	IncrementAlarmUploadSeqNo()
}

// IonuUniTechProf interface to onuUniTechProf
type IonuUniTechProf interface {
	GetAllBidirectionalGemPortIDsForOnu() []uint16
	SetProfileToDelete(uint8, uint8, bool)
}

// IuniVlanConfigFsm interface to uniVlanConfigFsm
type IuniVlanConfigFsm interface {
	IsFlowRemovePending(chan<- bool) bool
}
