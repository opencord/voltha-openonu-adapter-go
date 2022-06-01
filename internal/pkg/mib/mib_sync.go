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

//Package mib provides the utilities for managing the onu mib
package mib

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/looplab/fsm"

	"time"

	"github.com/google/gopacket"
	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	devdb "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb"
	otst "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/omcitst"
	"github.com/opencord/voltha-protos/v5/go/inter_adapter"
)

type sLastTxMeParameter struct {
	lastTxMessageType omci.MessageType
	pLastTxMeInstance *me.ManagedEntity
	repeatCount       uint8
}

var supportedClassIds = []me.ClassID{
	me.CardholderClassID,                              // 5
	me.CircuitPackClassID,                             // 6
	me.SoftwareImageClassID,                           // 7
	me.PhysicalPathTerminationPointEthernetUniClassID, // 11
	me.PhysicalPathTerminationPointPotsUniClassID,     // 53
	me.OltGClassID,                                    // 131
	me.OnuPowerSheddingClassID,                        // 133
	me.IpHostConfigDataClassID,                        // 134
	me.OnuGClassID,                                    // 256
	me.Onu2GClassID,                                   // 257
	me.TContClassID,                                   // 262
	me.AniGClassID,                                    // 263
	me.UniGClassID,                                    // 264
	me.PriorityQueueClassID,                           // 277
	me.TrafficSchedulerClassID,                        // 278
	me.VirtualEthernetInterfacePointClassID,           // 329
	me.EnhancedSecurityControlClassID,                 // 332
	me.OnuDynamicPowerManagementControlClassID,        // 336
	// 347 // definitions for ME "IPv6 host config data" are currently missing in omci-lib-go!
}

var omccVersionSupportsExtendedOmciFormat = map[uint8]bool{
	0x80: false,
	0x81: false,
	0x82: false,
	0x83: false,
	0x84: false,
	0x85: false,
	0x86: false,
	0xA0: false,
	0xA1: false,
	0xA2: false,
	0xA3: false,
	0x96: true,
	0xB0: true,
	0xB1: true,
	0xB2: true,
	0xB3: true,
	0xB4: true,
}

var fsmMsg cmn.TestMessageType

func (oo *OnuDeviceEntry) enterStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.pOnuDB = devdb.NewOnuDeviceDB(log.WithSpanFromContext(context.TODO(), ctx), oo.deviceID)
	go oo.processMibSyncMessages(ctx)
}

func (oo *OnuDeviceEntry) enterResettingMibState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": oo.deviceID})

	if (!oo.IsNewOnu() && !oo.baseDeviceHandler.IsReconciling()) || //use case: re-auditing failed
		oo.baseDeviceHandler.IsSkipOnuConfigReconciling() { //use case: reconciling without omci-config failed
		oo.baseDeviceHandler.PrepareReconcilingWithActiveAdapter(ctx)
		oo.devState = cmn.DeviceStatusInit
	}
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.mutexLastTxParamStruct.Lock()
	_ = oo.PDevOmciCC.SendMibReset(log.WithSpanFromContext(context.TODO(), ctx), oo.baseDeviceHandler.GetOmciTimeout(), true)
	//TODO: needs to handle timeouts
	//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
	//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
	oo.lastTxParamStruct.lastTxMessageType = omci.MibResetRequestType
	oo.lastTxParamStruct.repeatCount = 0
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterGettingVendorAndSerialState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting VendorId and SerialNumber in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.OnuG_VendorId: "", me.OnuG_SerialNumber: 0}
	oo.mutexLastTxParamStruct.Lock()
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.OnuGClassID, cmn.OnugMeID, requestedAttributes,
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "ONU-G get failed, aborting MibSync FSM", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterGettingEquipIDAndOmccVersState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting EquipmentId and OMCC version in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.Onu2G_EquipmentId: "", me.Onu2G_OpticalNetworkUnitManagementAndControlChannelOmccVersion: 0}
	oo.mutexLastTxParamStruct.Lock()
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.Onu2GClassID, cmn.Onu2gMeID, requestedAttributes,
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "ONU2-G get failed, aborting MibSync FSM!", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterTestingExtOmciSupportState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start testing extended OMCI msg in State": e.FSM.Current(), "device-id": oo.deviceID})
	omciVerify := otst.NewOmciTestRequest(log.WithSpanFromContext(context.TODO(), ctx),
		oo.deviceID, oo.PDevOmciCC, true, true, true)
	verifyExec := make(chan bool)
	omciVerify.PerformOmciTest(log.WithSpanFromContext(context.TODO(), ctx), verifyExec)

	// If verification of test message in extended OMCI format fails, reset ONU capability to OMCI baseline format
	select {
	case <-time.After(((cmn.CDefaultRetries+1)*otst.CTestRequestOmciTimeout + 1) * time.Second):
		logger.Warnw(ctx, "testing extended OMCI msg format timed out - reset to baseline format", log.Fields{"device-id": oo.deviceID})
		oo.MutexPersOnuConfig.Lock()
		oo.SOnuPersistentData.PersIsExtOmciSupported = false
		oo.MutexPersOnuConfig.Unlock()
	case success := <-verifyExec:
		if success {
			logger.Debugw(ctx, "testing extended OMCI msg format succeeded", log.Fields{"device-id": oo.deviceID})
		} else {
			logger.Warnw(ctx, "testing extended OMCI msg format failed - reset to baseline format", log.Fields{"device-id": oo.deviceID, "result": success})
			oo.MutexPersOnuConfig.Lock()
			oo.SOnuPersistentData.PersIsExtOmciSupported = false
			oo.MutexPersOnuConfig.Unlock()
		}
	}
	pMibUlFsm := oo.PMibUploadFsm
	if pMibUlFsm != nil {
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetFirstSwVersion)
		}(pMibUlFsm)
	}
}

func (oo *OnuDeviceEntry) enterGettingFirstSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of first SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.SoftwareImage_IsCommitted: 0, me.SoftwareImage_IsActive: 0, me.SoftwareImage_Version: ""}
	oo.mutexLastTxParamStruct.Lock()
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, cmn.FirstSwImageMeID, requestedAttributes,
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "SoftwareImage get failed, aborting MibSync FSM", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterGettingSecondSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of second SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.SoftwareImage_IsCommitted: 0, me.SoftwareImage_IsActive: 0, me.SoftwareImage_Version: ""}
	oo.mutexLastTxParamStruct.Lock()
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, cmn.SecondSwImageMeID, requestedAttributes,
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "SoftwareImage get failed, aborting MibSync FSM", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterGettingMacAddressState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting MacAddress in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.IpHostConfigData_MacAddress: ""}
	oo.mutexLastTxParamStruct.Lock()
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.IpHostConfigDataClassID, cmn.IPHostConfigDataMeID, requestedAttributes,
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "IpHostConfigData get failed, aborting MibSync FSM", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterGettingMibTemplateState(ctx context.Context, e *fsm.Event) {

	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.ActiveEntityEntry.Valid {
		oo.MutexPersOnuConfig.Lock()
		oo.SOnuPersistentData.PersActiveSwVersion = oo.onuSwImageIndications.ActiveEntityEntry.Version
		oo.MutexPersOnuConfig.Unlock()
		oo.mutexOnuSwImageIndications.RUnlock()
	} else {
		oo.mutexOnuSwImageIndications.RUnlock()
		logger.Errorw(ctx, "get-mib-template: no active SW version found, working with empty SW version, which might be untrustworthy",
			log.Fields{"device-id": oo.deviceID})
	}
	if oo.getMibFromTemplate(ctx) {
		logger.Debug(ctx, "MibSync FSM - valid MEs stored from template")
		oo.pOnuDB.LogMeDb(ctx)
		fsmMsg = cmn.LoadMibTemplateOk
	} else {
		logger.Debug(ctx, "MibSync FSM - no valid MEs stored from template - perform MIB-upload!")
		fsmMsg = cmn.LoadMibTemplateFailed

		oo.pOpenOnuAc.LockMutexMibTemplateGenerated()
		if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.GetMibTemplatesGenerated(oo.mibTemplatePath); exist {
			if mibTemplateIsGenerated {
				logger.Debugw(ctx,
					"MibSync FSM - template was successfully generated before, but doesn't exist or isn't usable anymore - reset flag in map",
					log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
				oo.pOpenOnuAc.SetMibTemplatesGenerated(oo.mibTemplatePath, false)
			}
		}
		oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()
	}
	mibSyncMsg := cmn.Message{
		Type: cmn.TestMsg,
		Data: cmn.TestMessage{
			TestMessageVal: fsmMsg,
		},
	}
	oo.PMibUploadFsm.CommChan <- mibSyncMsg
}

func (oo *OnuDeviceEntry) enterUploadingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send MibUpload in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.SendMibUpload(log.WithSpanFromContext(context.TODO(), ctx),
		oo.baseDeviceHandler.GetOmciTimeout(), true, oo.GetPersIsExtOmciSupported())
	//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
	//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
	oo.mutexLastTxParamStruct.Lock()
	oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadRequestType
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) enterUploadDoneState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.transferSystemEvent(ctx, cmn.MibDatabaseSync)
	go func() {
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
	}()
}

func (oo *OnuDeviceEntry) enterInSyncState(ctx context.Context, e *fsm.Event) {
	oo.MutexPersOnuConfig.Lock()
	oo.SOnuPersistentData.PersMibLastDbSync = uint32(time.Now().Unix())
	oo.MutexPersOnuConfig.Unlock()
	if oo.mibAuditInterval > 0 {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"trigger next Audit in State": e.FSM.Current(), "oo.mibAuditInterval": oo.mibAuditInterval, "device-id": oo.deviceID})
		go func() {
			time.Sleep(oo.mibAuditInterval)
			if err := oo.PMibUploadFsm.PFsm.Event(UlEvAuditMib); err != nil {
				logger.Debugw(ctx, "MibSyncFsm: Can't go to state auditing", log.Fields{"device-id": oo.deviceID, "err": err})
			}
		}()
	}
}

func (oo *OnuDeviceEntry) enterVerifyingAndStoringTPsState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start verifying and storing TPs in State": e.FSM.Current(), "device-id": oo.deviceID})

	if oo.getAllStoredTpInstFromParentAdapter(ctx) {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"reconciling - verifying TPs successful": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		}()
	} else {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"reconciling - verifying TPs not successful": e.FSM.Current(), "device-id": oo.deviceID})
		oo.baseDeviceHandler.SetReconcilingReasonUpdate(true)
		go func() {
			if err := oo.baseDeviceHandler.StorePersistentData(ctx); err != nil {
				logger.Warnw(ctx, "reconciling - store persistent data error - continue for now as there will be additional write attempts",
					log.Fields{"device-id": oo.deviceID, "err": err})
			}
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
		}()
	}
}

func (oo *OnuDeviceEntry) enterExaminingMdsState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.requestMdsValue(ctx)
}

func (oo *OnuDeviceEntry) enterResynchronizingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
	// TODOs:
	// VOL-3805 - Provide exclusive OMCI channel for one FSM
	// VOL-3785 - New event notifications and corresponding performance counters for openonu-adapter-go
	// VOL-3792 - Support periodical audit via mib resync
	// VOL-3793 - ONU-reconcile handling after adapter restart based on mib resync
}

func (oo *OnuDeviceEntry) enterExaminingMdsSuccessState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM",
		log.Fields{"Start processing on examining MDS success in State": e.FSM.Current(), "device-id": oo.deviceID})

	if oo.getMibFromTemplate(ctx) {
		oo.baseDeviceHandler.StartReconciling(ctx, true)
		oo.baseDeviceHandler.AddAllUniPorts(ctx)
		_ = oo.baseDeviceHandler.ReasonUpdate(ctx, cmn.DrInitialMibDownloaded, oo.baseDeviceHandler.IsReconcilingReasonUpdate())
		oo.baseDeviceHandler.SetReadyForOmciConfig(true)

		if !oo.baseDeviceHandler.GetCollectorIsRunning() {
			var waitForOmciProcess sync.WaitGroup
			waitForOmciProcess.Add(1)
			// Start PM collector routine
			go oo.baseDeviceHandler.StartCollector(ctx, &waitForOmciProcess)
			waitForOmciProcess.Wait()
		}
		if !oo.baseDeviceHandler.GetAlarmManagerIsRunning(ctx) {
			go oo.baseDeviceHandler.StartAlarmManager(ctx)
		}

		for _, uniPort := range *oo.baseDeviceHandler.GetUniEntityMap() {
			// only if this port was enabled for use by the operator at startup
			if (1<<uniPort.UniID)&oo.baseDeviceHandler.GetUniPortMask() == (1 << uniPort.UniID) {
				if !oo.baseDeviceHandler.GetFlowMonitoringIsRunning(uniPort.UniID) {
					go oo.baseDeviceHandler.PerOnuFlowHandlerRoutine(uniPort.UniID)
				}
			}
		}
		oo.MutexPersOnuConfig.RLock()
		if oo.SOnuPersistentData.PersUniDisableDone {
			oo.MutexPersOnuConfig.RUnlock()
			oo.baseDeviceHandler.DisableUniPortStateUpdate(ctx)
			_ = oo.baseDeviceHandler.ReasonUpdate(ctx, cmn.DrOmciAdminLock, oo.baseDeviceHandler.IsReconcilingReasonUpdate())
		} else {
			oo.MutexPersOnuConfig.RUnlock()
			oo.baseDeviceHandler.EnableUniPortStateUpdate(ctx)
		}

		// no need to reconcile additional data for MibDownloadFsm, LockStateFsm, or UnlockStateFsm

		if oo.baseDeviceHandler.ReconcileDeviceTechProf(ctx) {
			// start go routine with select() on reconciling flow channel before
			// starting flow reconciling process to prevent loss of any signal
			syncChannel := make(chan struct{})
			go func(aSyncChannel chan struct{}) {
				// In multi-ONU/multi-flow environment stopping reconcilement has to be delayed until
				// we get a signal that the processing of the last step to rebuild the adapter internal
				// flow data is finished.
				expiry := oo.baseDeviceHandler.GetReconcileExpiryVlanConfigAbort()
				oo.setReconcilingFlows(true)
				aSyncChannel <- struct{}{}
				select {
				case success := <-oo.chReconcilingFlowsFinished:
					if success {
						logger.Debugw(ctx, "reconciling flows has been finished in time",
							log.Fields{"device-id": oo.deviceID})
						_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)

					} else {
						logger.Debugw(ctx, "wait for reconciling flows aborted",
							log.Fields{"device-id": oo.deviceID})
					}
				case <-time.After(expiry):
					logger.Errorw(ctx, "timeout waiting for reconciling flows to be finished!",
						log.Fields{"device-id": oo.deviceID, "expiry": expiry})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
				}
				oo.setReconcilingFlows(false)
			}(syncChannel)
			// block further processing until the above Go routine has really started
			// and is ready to receive values from chReconcilingFlowsFinished
			<-syncChannel
			oo.baseDeviceHandler.ReconcileDeviceFlowConfig(ctx)
		}
	} else {
		logger.Debugw(ctx, "MibSync FSM",
			log.Fields{"Getting MIB from template not successful": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			//switch to reconciling with OMCI config
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
		}()
	}
}

func (oo *OnuDeviceEntry) enterAuditingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibAudit processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	if oo.baseDeviceHandler.CheckAuditStartCondition(ctx, cmn.CUploadFsm) {
		oo.requestMdsValue(ctx)
	} else {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"Configuration is ongoing or missing - skip auditing!": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		}()
	}
}

func (oo *OnuDeviceEntry) enterReAuditingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start retest MdsValue processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	if oo.baseDeviceHandler.CheckAuditStartCondition(ctx, cmn.CUploadFsm) {
		oo.requestMdsValue(ctx)
	} else {
		logger.Debugw(ctx, "MibSync FSM", log.Fields{"Configuration is ongoing or missing - skip re-auditing!": e.FSM.Current(), "device-id": oo.deviceID})
		go func() {
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		}()
	}
}

func (oo *OnuDeviceEntry) enterOutOfSyncState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
}

func (oo *OnuDeviceEntry) processMibSyncMessages(ctx context.Context) {
	logger.Debugw(ctx, "MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": oo.deviceID})
	oo.mutexMibSyncMsgProcessorRunning.Lock()
	oo.mibSyncMsgProcessorRunning = true
	oo.mutexMibSyncMsgProcessorRunning.Unlock()
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		message, ok := <-oo.PMibUploadFsm.CommChan
		if !ok {
			logger.Info(ctx, "MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": oo.deviceID})
			oo.mutexMibSyncMsgProcessorRunning.Lock()
			oo.mibSyncMsgProcessorRunning = false
			oo.mutexMibSyncMsgProcessorRunning.Unlock()
			break loop
		}
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": oo.deviceID})

		switch message.Type {
		case cmn.TestMsg:
			msg, _ := message.Data.(cmn.TestMessage)
			if msg.TestMessageVal == cmn.AbortMessageProcessing {
				logger.Debugw(ctx, "MibSync Msg abort ProcessMsg", log.Fields{"for device-id": oo.deviceID})
				oo.mutexMibSyncMsgProcessorRunning.Lock()
				oo.mibSyncMsgProcessorRunning = false
				oo.mutexMibSyncMsgProcessorRunning.Unlock()
				break loop
			}
			oo.handleTestMsg(ctx, msg)
		case cmn.OMCI:
			msg, _ := message.Data.(cmn.OmciMessage)
			oo.handleOmciMessage(ctx, msg)
		default:
			logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "message.Type": message.Type})
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	// TODO: only this action?
	_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
}

func (oo *OnuDeviceEntry) handleTestMsg(ctx context.Context, msg cmn.TestMessage) {

	logger.Debugw(ctx, "MibSync Msg", log.Fields{"TestMessage received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case cmn.LoadMibTemplateFailed:
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvUploadMib)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.PMibUploadFsm.PFsm.Current())})
	case cmn.LoadMibTemplateOk:
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.PMibUploadFsm.PFsm.Current())})
	default:
		logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (oo *OnuDeviceEntry) handleOmciMibResetResponseMessage(ctx context.Context, msg cmn.OmciMessage) {
	if oo.PMibUploadFsm.PFsm.Is(UlStResettingMib) {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer != nil {
			msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
			if msgOk {
				logger.Debugw(ctx, "MibResetResponse Data", log.Fields{"data-fields": msgObj})
				if msgObj.Result == me.Success {
					oo.MutexPersOnuConfig.Lock()
					oo.SOnuPersistentData.PersMibDataSyncAdpt = cmn.MdsDefaultMib
					oo.MutexPersOnuConfig.Unlock()
					// trigger retrieval of VendorId and SerialNumber
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetVendorAndSerial)
					return
				}
				logger.Errorw(ctx, "Omci MibResetResponse Error", log.Fields{"device-id": oo.deviceID, "Error": msgObj.Result})
			} else {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
			}
		} else {
			logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		}
	} else {
		//in case the last request was MdsGetRequest this issue may appear if the ONU was online before and has received the MIB reset
		//  with Sequence number 0x8000 as last request before - so it may still respond to that
		//  then we may force the ONU to react on the MdsGetRequest with a new message that uses an increased Sequence number
		oo.mutexLastTxParamStruct.Lock()
		if oo.lastTxParamStruct.lastTxMessageType == omci.GetRequestType && oo.lastTxParamStruct.repeatCount == 0 {
			logger.Debugw(ctx, "MibSync FSM - repeat MdsGetRequest (updated SequenceNumber)", log.Fields{"device-id": oo.deviceID})
			requestedAttributes := me.AttributeValueMap{me.OnuData_MibDataSync: ""}
			_, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
				me.OnuDataClassID, cmn.OnuDataMeID, requestedAttributes, oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
			if err != nil {
				oo.mutexLastTxParamStruct.Unlock()
				logger.Errorw(ctx, "ONUData get failed, aborting MibSync", log.Fields{"device-id": oo.deviceID})
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
				return
			}
			//TODO: needs extra handling of timeouts
			oo.lastTxParamStruct.repeatCount = 1
			oo.mutexLastTxParamStruct.Unlock()
			return
		}
		oo.mutexLastTxParamStruct.Unlock()
		logger.Errorw(ctx, "unexpected MibResetResponse - ignoring", log.Fields{"device-id": oo.deviceID})
		//perhaps some still lingering message from some prior activity, let's wait for the real response
		return
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
}

func (oo *OnuDeviceEntry) handleOmciMibUploadResponseMessage(ctx context.Context, msg cmn.OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
		return
	}
	logger.Debugw(ctx, "MibUploadResponse Data for:", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	/* to be verified / reworked !!! */
	oo.PDevOmciCC.UploadNoOfCmds = msgObj.NumberOfCommands
	if oo.PDevOmciCC.UploadSequNo < oo.PDevOmciCC.UploadNoOfCmds {
		_ = oo.PDevOmciCC.SendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx),
			oo.baseDeviceHandler.GetOmciTimeout(), true, oo.GetPersIsExtOmciSupported())
		//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
		//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
		oo.mutexLastTxParamStruct.Lock()
		oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadNextRequestType
		oo.mutexLastTxParamStruct.Unlock()
	} else {
		logger.Errorw(ctx, "Invalid number of commands received for:", log.Fields{"device-id": oo.deviceID, "UploadNoOfCmds": oo.PDevOmciCC.UploadNoOfCmds})
		//TODO right action?
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvTimeout)
	}
}

func (oo *OnuDeviceEntry) handleOmciMibUploadNextResponseMessage(ctx context.Context, msg cmn.OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)
	if msgLayer != nil {
		msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
		if !msgOk {
			logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
			return
		}
		meName := msgObj.ReportedME.GetName()
		meClassID := msgObj.ReportedME.GetClassID()
		meEntityID := msgObj.ReportedME.GetEntityID()

		logger.Debugw(ctx, "MibUploadNextResponse Data for:", log.Fields{"device-id": oo.deviceID, "meName": meName, "data-fields": msgObj})

		if meName == devdb.CUnknownItuG988ManagedEntity || meName == devdb.CUnknownVendorSpecificManagedEntity {
			logger.Debugw(ctx, "MibUploadNextResponse contains unknown ME", log.Fields{"device-id": oo.deviceID,
				"Me-Name": devdb.UnknownMeOrAttribName(meName), "Me-ClassId": meClassID, "Me-InstId": meEntityID,
				"unknown mask": msgObj.ReportedME.GetAttributeMask(), "unknown attributes": msgObj.BaseLayer.Payload})
			oo.pOnuDB.PutUnknownMeOrAttrib(ctx, devdb.UnknownMeOrAttribName(meName), meClassID, meEntityID,
				msgObj.ReportedME.GetAttributeMask(), msgObj.BaseLayer.Payload[devdb.CStartUnknownMeAttribsInBaseLayerPayload:])
		} else {
			//with relaxed decoding set in the OMCI-LIB we have the chance to detect if there are some unknown attributes appended which we cannot decode
			if unknownAttrLayer := (*msg.OmciPacket).Layer(omci.LayerTypeUnknownAttributes); unknownAttrLayer != nil {
				logger.Warnw(ctx, "MibUploadNextResponse contains unknown attributes", log.Fields{"device-id": oo.deviceID})
				if unknownAttributes, ok := unknownAttrLayer.(*omci.UnknownAttributes); ok {
					// provide a loop over several ME's here already in preparation of OMCI extended message format
					for _, unknown := range unknownAttributes.Attributes {
						unknownAttrClassID := unknown.EntityClass // ClassID
						unknownAttrInst := unknown.EntityInstance // uint16
						unknownAttrMask := unknown.AttributeMask  // ui
						unknownAttrBlob := unknown.AttributeData  // []byte
						logger.Warnw(ctx, "unknown attributes detected for", log.Fields{"device-id": oo.deviceID,
							"Me-ClassId": unknownAttrClassID, "Me-InstId": unknownAttrInst, "unknown mask": unknownAttrMask,
							"unknown attributes": unknownAttrBlob})
						oo.pOnuDB.PutUnknownMeOrAttrib(ctx, devdb.CUnknownAttributesManagedEntity, unknown.EntityClass, unknown.EntityInstance,
							unknown.AttributeMask, unknown.AttributeData)
					} // for all included ME's with unknown attributes
				} else {
					logger.Errorw(ctx, "unknownAttrLayer could not be decoded", log.Fields{"device-id": oo.deviceID})
				}
			}
			oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, msgObj.ReportedME.GetAttributeValueMap())
		}
		if msg.OmciMsg.DeviceIdentifier == omci.ExtendedIdent {
			for _, additionalME := range msgObj.AdditionalMEs {
				meName := additionalME.GetName()
				meClassID := additionalME.GetClassID()
				meEntityID := additionalME.GetEntityID()
				attributes := additionalME.GetAttributeValueMap()

				if meName == devdb.CUnknownItuG988ManagedEntity || meName == devdb.CUnknownVendorSpecificManagedEntity {
					attribMask := additionalME.GetAttributeMask()
					logger.Debugw(ctx, "MibUploadNextResponse AdditionalData contains unknown ME", log.Fields{"device-id": oo.deviceID,
						"Me-Name": devdb.UnknownMeOrAttribName(meName), "Me-ClassId": meClassID, "Me-InstId": meEntityID,
						"unknown mask": attribMask})

					attribValues := make([]byte, 0)
					for key, value := range attributes {
						if key != cmn.CGenericManagedEntityIDName {
							data, err := me.InterfaceToOctets(value)
							if err != nil {
								logger.Infow(ctx, "MibUploadNextResponse unknown ME AdditionalData attrib - could not decode", log.Fields{"device-id": oo.deviceID, "key": key})
							} else {
								attribValues = append(attribValues[:], data[:]...)
								logger.Debugw(ctx, "MibUploadNextResponse unknown ME AdditionalData attrib", log.Fields{"device-id": oo.deviceID, "attribValues": attribValues, "data": data, "key": key})
							}
						}
					}
					oo.pOnuDB.PutUnknownMeOrAttrib(ctx, devdb.UnknownMeOrAttribName(meName), meClassID, meEntityID, attribMask, attribValues)
				} else {
					logger.Debugw(ctx, "MibUploadNextResponse AdditionalData for:", log.Fields{"device-id": oo.deviceID, "meName": meName, "meEntityID": meEntityID, "attributes": attributes})
					oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, attributes)
				}
			}
		}
	} else {
		logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		//as long as omci-lib does not support decoding of table attribute as 'unknown/unspecified' attribute
		//  we have to verify, if this failure is from table attribute and try to go forward with ignoring the complete message
		errLayer := (*msg.OmciPacket).Layer(gopacket.LayerTypeDecodeFailure)
		if failure, decodeOk := errLayer.(*gopacket.DecodeFailure); decodeOk {
			errMsg := failure.String()
			if !strings.Contains(strings.ToLower(errMsg), "table decode") {
				//something still unexected happened, needs deeper investigation - stop complete MIB upload process (timeout)
				return
			}
			logger.Warnw(ctx, "Decode issue on received MibUploadNextResponse frame - found table attribute(s) (message ignored)",
				log.Fields{"device-id": oo.deviceID, "issue": errMsg})
		}
	}
	if oo.PDevOmciCC.UploadSequNo < oo.PDevOmciCC.UploadNoOfCmds {
		_ = oo.PDevOmciCC.SendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx),
			oo.baseDeviceHandler.GetOmciTimeout(), true, oo.GetPersIsExtOmciSupported())
		//even though lastTxParameters are currently not used for checking the ResetResponse message we have to ensure
		//  that the lastTxMessageType is correctly set to avoid misinterpreting other responses
		oo.mutexLastTxParamStruct.Lock()
		oo.lastTxParamStruct.lastTxMessageType = omci.MibUploadNextRequestType
		oo.mutexLastTxParamStruct.Unlock()
	} else {
		oo.pOnuDB.LogMeDb(ctx)
		err := oo.createAndPersistMibTemplate(ctx)
		if err != nil {
			logger.Errorw(ctx, "MibSync - MibTemplate - Failed to create and persist the mib template", log.Fields{"error": err, "device-id": oo.deviceID})
		}

		_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
	}
}

// nolint: gocyclo
func (oo *OnuDeviceEntry) handleOmciGetResponseMessage(ctx context.Context, msg cmn.OmciMessage) error {
	var err error = nil

	oo.mutexLastTxParamStruct.RLock()
	if oo.lastTxParamStruct.lastTxMessageType != omci.GetRequestType ||
		oo.lastTxParamStruct.pLastTxMeInstance == nil {
		//in case the last request was MibReset this issue may appear if the ONU was online before and has received the MDS GetRequest
		//  with Sequence number 0x8000 as last request before - so it may still respond to that
		//  then we may force the ONU to react on the MIB reset with a new message that uses an increased Sequence number
		if oo.lastTxParamStruct.lastTxMessageType == omci.MibResetRequestType && oo.lastTxParamStruct.repeatCount == 0 {
			logger.Debugw(ctx, "MibSync FSM - repeat mibReset (updated SequenceNumber)", log.Fields{"device-id": oo.deviceID})
			_ = oo.PDevOmciCC.SendMibReset(log.WithSpanFromContext(context.TODO(), ctx), oo.baseDeviceHandler.GetOmciTimeout(), true)
			//TODO: needs extra handling of timeouts
			oo.lastTxParamStruct.repeatCount = 1
			oo.mutexLastTxParamStruct.RUnlock()
			return nil
		}
		oo.mutexLastTxParamStruct.RUnlock()
		logger.Warnw(ctx, "unexpected GetResponse - ignoring", log.Fields{"device-id": oo.deviceID})
		//perhaps some still lingering message from some prior activity, let's wait for the real response
		return nil
	}
	oo.mutexLastTxParamStruct.RUnlock()
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	logger.Debugw(ctx, "MibSync FSM - GetResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		oo.mutexLastTxParamStruct.RLock()
		entityID := oo.lastTxParamStruct.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.lastTxParamStruct.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meAttributes := msgObj.Attributes
			meInstance := oo.lastTxParamStruct.pLastTxMeInstance.GetName()
			logger.Debugf(ctx, "MibSync FSM - GetResponse Data for %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
			switch meInstance {
			case "OnuG":
				oo.mutexLastTxParamStruct.RUnlock()
				if onuGVendorID, ok := meAttributes[me.OnuG_VendorId]; ok {
					vendorID := cmn.TrimStringFromMeOctet(onuGVendorID)
					if vendorID == "" {
						logger.Infow(ctx, "MibSync FSM - mandatory attribute VendorId is empty in OnuG instance - fill with appropriate value", log.Fields{"device-id": oo.deviceID})
						vendorID = cEmptyVendorIDString
					}
					oo.MutexPersOnuConfig.Lock()
					oo.SOnuPersistentData.PersVendorID = vendorID
					oo.MutexPersOnuConfig.Unlock()
				} else {
					logger.Errorw(ctx, "MibSync FSM - mandatory attribute VendorId not present in OnuG instance - handling of MibSyncChan stopped!",
						log.Fields{"device-id": oo.deviceID})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
					return fmt.Errorf("mibSync FSM - mandatory attribute VendorId not present in OnuG instance - handling of MibSyncChan stopped: %s", oo.deviceID)
				}
				if onuGSerialNumber, ok := meAttributes[me.OnuG_SerialNumber]; ok {
					oo.MutexPersOnuConfig.Lock()
					snBytes, _ := me.InterfaceToOctets(onuGSerialNumber)
					if cmn.OnugSerialNumberLen == len(snBytes) {
						snVendorPart := fmt.Sprintf("%s", snBytes[:4])
						snNumberPart := hex.EncodeToString(snBytes[4:])
						oo.SOnuPersistentData.PersSerialNumber = snVendorPart + snNumberPart
						logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-G - VendorId/SerialNumber", log.Fields{"device-id": oo.deviceID,
							"onuDeviceEntry.vendorID": oo.SOnuPersistentData.PersVendorID, "onuDeviceEntry.serialNumber": oo.SOnuPersistentData.PersSerialNumber})
					} else {
						logger.Infow(ctx, "MibSync FSM - SerialNumber has wrong length - fill serialNumber with zeros", log.Fields{"device-id": oo.deviceID, "length": len(snBytes)})
						oo.SOnuPersistentData.PersSerialNumber = cEmptySerialNumberString
					}
					oo.MutexPersOnuConfig.Unlock()
				} else {
					logger.Errorw(ctx, "MibSync FSM - mandatory attribute SerialNumber not present in OnuG instance - handling of MibSyncChan stopped!",
						log.Fields{"device-id": oo.deviceID})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
					return fmt.Errorf("mibSync FSM - mandatory attribute SerialNumber not present in OnuG instance - handling of MibSyncChan stopped: %s", oo.deviceID)
				}
				// trigger retrieval of EquipmentId
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetEquipIDAndOmcc)
				return nil
			case "Onu2G":
				oo.mutexLastTxParamStruct.RUnlock()
				var equipmentID string
				if onu2GEquipmentID, ok := meAttributes[me.Onu2G_EquipmentId]; ok {
					equipmentID = cmn.TrimStringFromMeOctet(onu2GEquipmentID)
					if equipmentID == "" {
						logger.Infow(ctx, "MibSync FSM - optional attribute EquipmentID is empty in Onu2G instance - fill with appropriate value", log.Fields{"device-id": oo.deviceID})
						equipmentID = cEmptyEquipIDString
					}
				} else {
					logger.Infow(ctx, "MibSync FSM - optional attribute EquipmentID not present in Onu2G instance - fill with appropriate value", log.Fields{"device-id": oo.deviceID})
					equipmentID = cNotPresentEquipIDString
				}
				oo.MutexPersOnuConfig.Lock()
				oo.SOnuPersistentData.PersEquipmentID = equipmentID
				logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu2-G - EquipmentId", log.Fields{"device-id": oo.deviceID,
					"onuDeviceEntry.equipmentID": oo.SOnuPersistentData.PersEquipmentID})
				oo.MutexPersOnuConfig.Unlock()

				var omccVersion uint8
				if onu2GOmccVersion, ok := meAttributes[me.Onu2G_OpticalNetworkUnitManagementAndControlChannelOmccVersion]; ok {
					oo.MutexPersOnuConfig.Lock()
					omccVersion = onu2GOmccVersion.(uint8)
					if _, ok := omccVersionSupportsExtendedOmciFormat[omccVersion]; ok {
						oo.SOnuPersistentData.PersIsExtOmciSupported = omccVersionSupportsExtendedOmciFormat[omccVersion]
					} else {
						logger.Infow(ctx, "MibSync FSM - unknown OMCC version in Onu2G instance - disable extended OMCI support",
							log.Fields{"device-id": oo.deviceID})
						oo.SOnuPersistentData.PersIsExtOmciSupported = false
					}
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu2-G - OMCC version", log.Fields{"device-id": oo.deviceID,
						"omccVersion": omccVersion, "isExtOmciSupported": oo.SOnuPersistentData.PersIsExtOmciSupported})
					oo.MutexPersOnuConfig.Unlock()
				} else {
					logger.Errorw(ctx, "MibSync FSM - mandatory attribute OMCC version not present in Onu2G instance - handling of MibSyncChan stopped!",
						log.Fields{"device-id": oo.deviceID})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
					return fmt.Errorf("mibSync FSM - mandatory attribute OMCC version not present in Onu2G instance - handling of MibSyncChan stopped: %s", oo.deviceID)
				}
				oo.MutexPersOnuConfig.RLock()
				if oo.SOnuPersistentData.PersIsExtOmciSupported {
					oo.MutexPersOnuConfig.RUnlock()
					// trigger test of OMCI extended msg format
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvTestExtOmciSupport)
					return nil
				}
				oo.MutexPersOnuConfig.RUnlock()
				// trigger retrieval of 1st SW-image info
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetFirstSwVersion)
				return nil
			case "SoftwareImage":
				oo.mutexLastTxParamStruct.RUnlock()
				if entityID > cmn.SecondSwImageMeID {
					logger.Errorw(ctx, "mibSync FSM - Failed to GetResponse Data for SoftwareImage with expected EntityId",
						log.Fields{"device-id": oo.deviceID, "entity-ID": entityID})
					return fmt.Errorf("mibSync FSM - SwResponse Data with unexpected EntityId: %s %x",
						oo.deviceID, entityID)
				}
				// need to use function for go lint complexity
				if !oo.HandleSwImageIndications(ctx, entityID, meAttributes) {
					logger.Errorw(ctx, "MibSync FSM - Not all mandatory attributes present in in SoftwareImage instance - handling of MibSyncChan stopped!", log.Fields{"device-id": oo.deviceID})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
					return fmt.Errorf("mibSync FSM - Not all mandatory attributes present in in SoftwareImage instance - handling of MibSyncChan stopped: %s", oo.deviceID)
				}
				return nil
			case "IpHostConfigData":
				oo.mutexLastTxParamStruct.RUnlock()
				oo.MutexPersOnuConfig.Lock()
				if ipHostConfigMacAddress, ok := meAttributes[me.IpHostConfigData_MacAddress]; ok {
					macBytes, _ := me.InterfaceToOctets(ipHostConfigMacAddress)
					if cmn.OmciMacAddressLen == len(macBytes) {
						oo.SOnuPersistentData.PersMacAddress = hex.EncodeToString(macBytes[:])
						logger.Debugw(ctx, "MibSync FSM - GetResponse Data for IpHostConfigData - MacAddress", log.Fields{"device-id": oo.deviceID,
							"macAddress": oo.SOnuPersistentData.PersMacAddress})
					} else {
						logger.Infow(ctx, "MibSync FSM - MacAddress wrong length - fill macAddress with zeros", log.Fields{"device-id": oo.deviceID, "length": len(macBytes)})
						oo.SOnuPersistentData.PersMacAddress = cEmptyMacAddrString
					}
				} else {
					// since ONU creates instances of this ME automatically only when IP host services are available, processing continues here despite the error
					logger.Infow(ctx, "MibSync FSM - MacAddress attribute not present in IpHostConfigData instance - fill macAddress with zeros", log.Fields{"device-id": oo.deviceID})
					oo.SOnuPersistentData.PersMacAddress = cEmptyMacAddrString
				}
				oo.MutexPersOnuConfig.Unlock()
				// trigger retrieval of mib template
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetMibTemplate)
				return nil
			case "OnuData":
				oo.mutexLastTxParamStruct.RUnlock()
				if onuDataMibDataSync, ok := meAttributes[me.OnuData_MibDataSync]; ok {
					oo.checkMdsValue(ctx, onuDataMibDataSync.(uint8))
				} else {
					logger.Errorw(ctx, "MibSync FSM - MibDataSync attribute not present in OnuData instance - handling of MibSyncChan stopped!", log.Fields{"device-id": oo.deviceID})
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
					return fmt.Errorf("mibSync FSM - VendorId attribute not present in OnuG instance - handling of MibSyncChan stopped: %s", oo.deviceID)
				}
				return nil
			default:
				oo.mutexLastTxParamStruct.RUnlock()
				logger.Warnw(ctx, "Unsupported ME name received!",
					log.Fields{"ME name": meInstance, "device-id": oo.deviceID})

			}
		} else {
			oo.mutexLastTxParamStruct.RUnlock()
			logger.Warnf(ctx, "MibSync FSM - Received GetResponse Data for %s with wrong classID or entityID ",
				log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.EntityClass)
		}
	} else {
		if err = oo.handleOmciGetResponseErrors(ctx, msgObj); err == nil {
			return nil
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
	return err
}

//HandleSwImageIndications updates onuSwImageIndications with the ONU data just received
func (oo *OnuDeviceEntry) HandleSwImageIndications(ctx context.Context, entityID uint16, meAttributes me.AttributeValueMap) bool {

	var imageVersion string
	var imageIsCommitted, imageIsActive uint8

	allMandAttribsPresent := false
	if softwareImageIsCommitted, ok := meAttributes[me.SoftwareImage_IsCommitted]; ok {
		if softwareImageIsActiveimage, ok := meAttributes[me.SoftwareImage_IsActive]; ok {
			if softwareImageVersion, ok := meAttributes[me.SoftwareImage_Version]; ok {
				imageVersion = cmn.TrimStringFromMeOctet(softwareImageVersion)
				imageIsActive = softwareImageIsActiveimage.(uint8)
				imageIsCommitted = softwareImageIsCommitted.(uint8)
				allMandAttribsPresent = true
			}
		}
	}
	if !allMandAttribsPresent {
		logger.Errorw(ctx, "MibSync FSM - Not all mandatory attributes present in SoftwareImage instance - skip processing!", log.Fields{"device-id": oo.deviceID})
		return allMandAttribsPresent
	}
	oo.MutexPersOnuConfig.RLock()
	logger.Infow(ctx, "MibSync FSM - GetResponse Data for SoftwareImage",
		log.Fields{"device-id": oo.deviceID, "entityID": entityID,
			"version": imageVersion, "isActive": imageIsActive, "isCommitted": imageIsCommitted, "SNR": oo.SOnuPersistentData.PersSerialNumber})
	oo.MutexPersOnuConfig.RUnlock()
	if cmn.FirstSwImageMeID == entityID {
		//always accept the state of the first image (2nd image info should not yet be available)
		oo.mutexOnuSwImageIndications.Lock()
		if imageIsActive == cmn.SwIsActive {
			oo.onuSwImageIndications.ActiveEntityEntry.EntityID = entityID
			oo.onuSwImageIndications.ActiveEntityEntry.Valid = true
			oo.onuSwImageIndications.ActiveEntityEntry.Version = imageVersion
			oo.onuSwImageIndications.ActiveEntityEntry.IsCommitted = imageIsCommitted
			//as the SW version indication may stem from some ONU Down/up event
			//the complementary image state is to be invalidated
			//  (state of the second image is always expected afterwards or just invalid)
			oo.onuSwImageIndications.InActiveEntityEntry.Valid = false
		} else {
			oo.onuSwImageIndications.InActiveEntityEntry.EntityID = entityID
			oo.onuSwImageIndications.InActiveEntityEntry.Valid = true
			oo.onuSwImageIndications.InActiveEntityEntry.Version = imageVersion
			oo.onuSwImageIndications.InActiveEntityEntry.IsCommitted = imageIsCommitted
			//as the SW version indication may stem form some ONU Down/up event
			//the complementary image state is to be invalidated
			//  (state of the second image is always expected afterwards or just invalid)
			oo.onuSwImageIndications.ActiveEntityEntry.Valid = false
		}
		oo.mutexOnuSwImageIndications.Unlock()
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetSecondSwVersion)
		return allMandAttribsPresent
	} else if cmn.SecondSwImageMeID == entityID {
		//2nd image info might conflict with first image info, in which case we priorize first image info!
		oo.mutexOnuSwImageIndications.Lock()
		if imageIsActive == cmn.SwIsActive { //2nd image reported to be active
			if oo.onuSwImageIndications.ActiveEntityEntry.Valid {
				//conflict exists - state of first image is left active
				logger.Warnw(ctx, "mibSync FSM - both ONU images are reported as active - assuming 2nd to be inactive",
					log.Fields{"device-id": oo.deviceID})
				oo.onuSwImageIndications.InActiveEntityEntry.EntityID = entityID
				oo.onuSwImageIndications.InActiveEntityEntry.Valid = true ////to indicate that at least something has been reported
				oo.onuSwImageIndications.InActiveEntityEntry.Version = imageVersion
				oo.onuSwImageIndications.InActiveEntityEntry.IsCommitted = imageIsCommitted
			} else { //first image inactive, this one active
				oo.onuSwImageIndications.ActiveEntityEntry.EntityID = entityID
				oo.onuSwImageIndications.ActiveEntityEntry.Valid = true
				oo.onuSwImageIndications.ActiveEntityEntry.Version = imageVersion
				oo.onuSwImageIndications.ActiveEntityEntry.IsCommitted = imageIsCommitted
			}
		} else { //2nd image reported to be inactive
			if oo.onuSwImageIndications.InActiveEntityEntry.Valid {
				//conflict exists - both images inactive - regard it as ONU failure and assume first image to be active
				logger.Warnw(ctx, "mibSync FSM - both ONU images are reported as inactive, defining first to be active",
					log.Fields{"device-id": oo.deviceID})
				oo.onuSwImageIndications.ActiveEntityEntry.EntityID = cmn.FirstSwImageMeID
				oo.onuSwImageIndications.ActiveEntityEntry.Valid = true //to indicate that at least something has been reported
				//copy active commit/version from the previously stored inactive position
				oo.onuSwImageIndications.ActiveEntityEntry.Version = oo.onuSwImageIndications.InActiveEntityEntry.Version
				oo.onuSwImageIndications.ActiveEntityEntry.IsCommitted = oo.onuSwImageIndications.InActiveEntityEntry.IsCommitted
			}
			//in any case we indicate (and possibly overwrite) the second image indications as inactive
			oo.onuSwImageIndications.InActiveEntityEntry.EntityID = entityID
			oo.onuSwImageIndications.InActiveEntityEntry.Valid = true
			oo.onuSwImageIndications.InActiveEntityEntry.Version = imageVersion
			oo.onuSwImageIndications.InActiveEntityEntry.IsCommitted = imageIsCommitted
		}
		oo.mutexOnuSwImageIndications.Unlock()
		_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetMacAddress)
	}
	return allMandAttribsPresent
}

func (oo *OnuDeviceEntry) handleOmciMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "MibSync Msg", log.Fields{"OmciMessage received for device-id": oo.deviceID,
		"msgType": msg.OmciMsg.MessageType, "msg": msg})
	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		oo.handleOmciMibResetResponseMessage(ctx, msg)

	case omci.MibUploadResponseType:
		oo.handleOmciMibUploadResponseMessage(ctx, msg)

	case omci.MibUploadNextResponseType:
		oo.handleOmciMibUploadNextResponseMessage(ctx, msg)

	case omci.GetResponseType:
		//TODO: error handling
		_ = oo.handleOmciGetResponseMessage(ctx, msg)

	default:
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"device-id": oo.deviceID, "msgType": msg.OmciMsg.MessageType})

	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseErrors(ctx context.Context, msgObj *omci.GetResponse) error {
	var err error = nil
	logger.Debugf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
	// Up to now the following erroneous results have been seen for different ONU-types to indicate an unsupported ME
	if msgObj.Result == me.UnknownInstance || msgObj.Result == me.UnknownEntity || msgObj.Result == me.ProcessingError || msgObj.Result == me.NotSupported {
		oo.mutexLastTxParamStruct.RLock()
		if oo.lastTxParamStruct.pLastTxMeInstance != nil {
			entityID := oo.lastTxParamStruct.pLastTxMeInstance.GetEntityID()
			if msgObj.EntityClass == oo.lastTxParamStruct.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
				meInstance := oo.lastTxParamStruct.pLastTxMeInstance.GetName()
				switch meInstance {
				case "IpHostConfigData":
					oo.mutexLastTxParamStruct.RUnlock()
					logger.Debugw(ctx, "MibSync FSM - erroneous result for IpHostConfigData received - ONU doesn't support ME - fill macAddress with zeros",
						log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
					oo.MutexPersOnuConfig.Lock()
					oo.SOnuPersistentData.PersMacAddress = cEmptyMacAddrString
					oo.MutexPersOnuConfig.Unlock()
					// trigger retrieval of mib template
					_ = oo.PMibUploadFsm.PFsm.Event(UlEvGetMibTemplate)
					return nil
				default:
					oo.mutexLastTxParamStruct.RUnlock()
					logger.Warnf(ctx, "MibSync FSM - erroneous result for %s received - no exceptional treatment defined", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
					err = fmt.Errorf("erroneous result for %s received - no exceptional treatment defined: %s", meInstance, oo.deviceID)
				}
			} else {
				oo.mutexLastTxParamStruct.RUnlock()
			}
		} else {
			oo.mutexLastTxParamStruct.RUnlock()
			logger.Warnw(ctx, "Pointer to last Tx MeInstance is nil!", log.Fields{"device-id": oo.deviceID})
		}
	} else {
		logger.Errorf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
		err = fmt.Errorf("erroneous result in GetResponse Data: %s - %s", msgObj.Result, oo.deviceID)
	}
	return err
}

// IsNewOnu - TODO: add comment
func (oo *OnuDeviceEntry) IsNewOnu() bool {
	oo.MutexPersOnuConfig.RLock()
	defer oo.MutexPersOnuConfig.RUnlock()
	return oo.SOnuPersistentData.PersMibLastDbSync == 0
}

func isSupportedClassID(meClassID me.ClassID) bool {
	for _, v := range supportedClassIds {
		if v == meClassID {
			return true
		}
	}
	return false
}

func (oo *OnuDeviceEntry) mibDbVolatileDict(ctx context.Context) error {
	logger.Debug(ctx, "MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// createAndPersistMibTemplate method creates a mib template for the device id when operator enables the ONU device for the first time.
// We are creating a placeholder for "SerialNumber" for ME Class ID 6 and 256 and "MacAddress" for ME Class ID 134 in the template
// and then storing the template into etcd "service/voltha/omci_mibs/go_templates/verdor_id/equipment_id/software_version" path.
func (oo *OnuDeviceEntry) createAndPersistMibTemplate(ctx context.Context) error {
	logger.Debugw(ctx, "MibSync - MibTemplate - path name", log.Fields{"path": oo.mibTemplatePath,
		"device-id": oo.deviceID})

	oo.pOpenOnuAc.LockMutexMibTemplateGenerated()
	if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.GetMibTemplatesGenerated(oo.mibTemplatePath); exist {
		if mibTemplateIsGenerated {
			logger.Debugw(ctx, "MibSync - MibTemplate - another thread has already started to generate it - skip",
				log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
			oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()
			return nil
		}
		logger.Debugw(ctx, "MibSync - MibTemplate - previous generation attempt seems to be failed - try again",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	} else {
		logger.Debugw(ctx, "MibSync - MibTemplate - first ONU-instance of this kind - start generation",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	oo.pOpenOnuAc.SetMibTemplatesGenerated(oo.mibTemplatePath, true)
	oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()

	currentTime := time.Now()
	templateMap := make(map[string]interface{})
	templateMap["TemplateName"] = oo.mibTemplatePath
	templateMap["TemplateCreated"] = currentTime.Format("2006-01-02 15:04:05.000000")

	firstLevelMap := oo.pOnuDB.MeDb
	for firstLevelKey, firstLevelValue := range firstLevelMap {
		logger.Debugw(ctx, "MibSync - MibTemplate - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
		classID := strconv.Itoa(int(firstLevelKey))

		secondLevelMap := make(map[string]interface{})
		for secondLevelKey, secondLevelValue := range firstLevelValue {
			// ManagedEntityId is already key of secondLevelMap - remove this redundant attribute from secondLevelValue
			delete(secondLevelValue, cmn.CGenericManagedEntityIDName)
			thirdLevelMap := make(map[string]interface{})
			entityID := strconv.Itoa(int(secondLevelKey))
			thirdLevelMap["Attributes"] = secondLevelValue
			secondLevelMap[entityID] = thirdLevelMap
			if classID == "6" || classID == "256" {
				forthLevelMap := map[string]interface{}(thirdLevelMap["Attributes"].(me.AttributeValueMap))
				delete(forthLevelMap, "SerialNumber")
				forthLevelMap["SerialNumber"] = "%SERIAL_NUMBER%"

			}
			if classID == "134" {
				forthLevelMap := map[string]interface{}(thirdLevelMap["Attributes"].(me.AttributeValueMap))
				delete(forthLevelMap, "MacAddress")
				forthLevelMap["MacAddress"] = "%MAC_ADDRESS%"
			}
		}
		templateMap[classID] = secondLevelMap
	}
	unknownMeAndAttribMap := oo.pOnuDB.UnknownMeAndAttribDb
	for unknownMeAndAttribMapKey := range unknownMeAndAttribMap {
		templateMap[string(unknownMeAndAttribMapKey)] = unknownMeAndAttribMap[unknownMeAndAttribMapKey]
	}
	mibTemplate, err := json.Marshal(&templateMap)
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to marshal mibTemplate", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.LockMutexMibTemplateGenerated()
		oo.pOpenOnuAc.SetMibTemplatesGenerated(oo.mibTemplatePath, false)
		oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()
		return err
	}
	err = oo.mibTemplateKVStore.Put(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath, string(mibTemplate))
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to store template in etcd", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.LockMutexMibTemplateGenerated()
		oo.pOpenOnuAc.SetMibTemplatesGenerated(oo.mibTemplatePath, false)
		oo.pOpenOnuAc.UnlockMutexMibTemplateGenerated()
		return err
	}
	logger.Debugw(ctx, "MibSync - MibTemplate - Stored the template to etcd", log.Fields{"device-id": oo.deviceID})
	return nil
}

func (oo *OnuDeviceEntry) requestMdsValue(ctx context.Context) {
	logger.Debugw(ctx, "Request MDS value", log.Fields{"device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{me.OnuData_MibDataSync: ""}
	meInstance, err := oo.PDevOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
		me.OnuDataClassID, cmn.OnuDataMeID, requestedAttributes, oo.baseDeviceHandler.GetOmciTimeout(), true, oo.PMibUploadFsm.CommChan)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	if err != nil {
		logger.Errorw(ctx, "ONUData get failed, aborting MibSync FSM!", log.Fields{"device-id": oo.deviceID})
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = oo.PMibUploadFsm.PFsm.Event(UlEvStop)
			}(pMibUlFsm)
		}
		return
	}
	oo.mutexLastTxParamStruct.Lock()
	oo.lastTxParamStruct.lastTxMessageType = omci.GetRequestType
	oo.lastTxParamStruct.pLastTxMeInstance = meInstance
	oo.lastTxParamStruct.repeatCount = 0
	oo.mutexLastTxParamStruct.Unlock()
}

func (oo *OnuDeviceEntry) checkMdsValue(ctx context.Context, mibDataSyncOnu uint8) {
	oo.MutexPersOnuConfig.RLock()
	logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-Data - MibDataSync", log.Fields{"device-id": oo.deviceID,
		"mibDataSyncOnu": mibDataSyncOnu, "PersMibDataSyncAdpt": oo.SOnuPersistentData.PersMibDataSyncAdpt})

	mdsValuesAreEqual := oo.SOnuPersistentData.PersMibDataSyncAdpt == mibDataSyncOnu
	oo.MutexPersOnuConfig.RUnlock()
	if oo.PMibUploadFsm.PFsm.Is(UlStAuditing) {
		if mdsValuesAreEqual {
			logger.Debugw(ctx, "MibSync FSM - mib audit - MDS check ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		} else {
			logger.Warnw(ctx, "MibSync FSM - mib audit - MDS check failed for the first time!", log.Fields{"device-id": oo.deviceID})
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
		}
	} else if oo.PMibUploadFsm.PFsm.Is(UlStReAuditing) {
		if mdsValuesAreEqual {
			logger.Debugw(ctx, "MibSync FSM - mib reaudit - MDS check ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		} else {
			logger.Errorw(ctx, "MibSync FSM - mib reaudit - MDS check failed for the second time - send ONU device event and reconcile!",
				log.Fields{"device-id": oo.deviceID})
			oo.SendOnuDeviceEvent(ctx, cmn.OnuMibAuditFailureMds, cmn.OnuMibAuditFailureMdsDesc)
			// To reconcile ONU with active adapter later on, we have to retrieve TP instances from parent adapter.
			// In the present use case inconsistencies between TP pathes stored in kv store and TP instances retrieved
			// should not occur. Nevertheless, the respective code is inserted to catch the unlikely case.
			if !oo.getAllStoredTpInstFromParentAdapter(ctx) {
				logger.Debugw(ctx, "MibSync FSM - mib reaudit - inconsistencies between TP pathes stored in kv and parent adapter instances",
					log.Fields{"device-id": oo.deviceID})
				oo.baseDeviceHandler.SetReconcilingReasonUpdate(true)
				go func() {
					if err := oo.baseDeviceHandler.StorePersistentData(ctx); err != nil {
						logger.Warnw(ctx,
							"MibSync FSM - mib reaudit - store persistent data error - continue for now as there will be additional write attempts",
							log.Fields{"device-id": oo.deviceID, "err": err})
					}
				}()
			}
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
		}
	} else if oo.PMibUploadFsm.PFsm.Is(UlStExaminingMds) {
		if mdsValuesAreEqual && mibDataSyncOnu != 0 {
			logger.Debugw(ctx, "MibSync FSM - MDS examination ok", log.Fields{"device-id": oo.deviceID})
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvSuccess)
		} else {
			logger.Debugw(ctx, "MibSync FSM - MDS examination failed - new provisioning", log.Fields{"device-id": oo.deviceID})
			_ = oo.PMibUploadFsm.PFsm.Event(UlEvMismatch)
		}
	} else {
		logger.Warnw(ctx, "wrong state for MDS evaluation!", log.Fields{"state": oo.PMibUploadFsm.PFsm.Current(), "device-id": oo.deviceID})
	}
}

//GetActiveImageMeID returns the Omci MeId of the active ONU image together with error code for validity
func (oo *OnuDeviceEntry) GetActiveImageMeID(ctx context.Context) (uint16, error) {
	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.ActiveEntityEntry.Valid {
		value := oo.onuSwImageIndications.ActiveEntityEntry.EntityID
		oo.mutexOnuSwImageIndications.RUnlock()
		return value, nil
	}
	oo.mutexOnuSwImageIndications.RUnlock()
	return 0xFFFF, fmt.Errorf("no valid active image found: %s", oo.deviceID)
}

//GetInactiveImageMeID returns the Omci MeId of the inactive ONU image together with error code for validity
func (oo *OnuDeviceEntry) GetInactiveImageMeID(ctx context.Context) (uint16, error) {
	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.InActiveEntityEntry.Valid {
		value := oo.onuSwImageIndications.InActiveEntityEntry.EntityID
		oo.mutexOnuSwImageIndications.RUnlock()
		return value, nil
	}
	oo.mutexOnuSwImageIndications.RUnlock()
	return 0xFFFF, fmt.Errorf("no valid inactive image found: %s", oo.deviceID)
}

//IsImageToBeCommitted returns true if the active image is still uncommitted
func (oo *OnuDeviceEntry) IsImageToBeCommitted(ctx context.Context, aImageID uint16) bool {
	oo.mutexOnuSwImageIndications.RLock()
	if oo.onuSwImageIndications.ActiveEntityEntry.Valid {
		if oo.onuSwImageIndications.ActiveEntityEntry.EntityID == aImageID {
			if oo.onuSwImageIndications.ActiveEntityEntry.IsCommitted == cmn.SwIsUncommitted {
				oo.mutexOnuSwImageIndications.RUnlock()
				return true
			}
		}
	}
	oo.mutexOnuSwImageIndications.RUnlock()
	return false //all other case are treated as 'nothing to commit
}
func (oo *OnuDeviceEntry) getMibFromTemplate(ctx context.Context) bool {

	oo.mibTemplatePath = oo.buildMibTemplatePath()
	logger.Debugw(ctx, "MibSync FSM - get Mib from template", log.Fields{"path": fmt.Sprintf("%s/%s", cBasePathMibTemplateKvStore, oo.mibTemplatePath),
		"device-id": oo.deviceID})

	restoredFromMibTemplate := false
	Value, err := oo.mibTemplateKVStore.Get(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath)
	if err == nil {
		if Value != nil {
			logger.Debugf(ctx, "MibSync FSM - Mib template read: Key: %s, Value: %s  %s", Value.Key, Value.Value)

			// swap out tokens with specific data
			mibTmpString, _ := kvstore.ToString(Value.Value)
			oo.MutexPersOnuConfig.RLock()
			mibTmpString2 := strings.Replace(mibTmpString, "%SERIAL_NUMBER%", oo.SOnuPersistentData.PersSerialNumber, -1)
			mibTmpString = strings.Replace(mibTmpString2, "%MAC_ADDRESS%", oo.SOnuPersistentData.PersMacAddress, -1)
			oo.MutexPersOnuConfig.RUnlock()
			mibTmpBytes := []byte(mibTmpString)
			logger.Debugf(ctx, "MibSync FSM - Mib template tokens swapped out: %s", mibTmpBytes)

			var firstLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &firstLevelMap); err != nil {
				logger.Errorw(ctx, "MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": oo.deviceID})
			} else {
				for firstLevelKey, firstLevelValue := range firstLevelMap {
					//logger.Debugw(ctx, "MibSync FSM - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
					if uint16ValidNumber, err := strconv.ParseUint(firstLevelKey, 10, 16); err == nil {
						meClassID := me.ClassID(uint16ValidNumber)
						//logger.Debugw(ctx, "MibSync FSM - firstLevelKey is a number in uint16-range", log.Fields{"uint16ValidNumber": uint16ValidNumber})
						if isSupportedClassID(meClassID) {
							//logger.Debugw(ctx, "MibSync FSM - firstLevelKey is a supported classID", log.Fields{"meClassID": meClassID})
							secondLevelMap := firstLevelValue.(map[string]interface{})
							for secondLevelKey, secondLevelValue := range secondLevelMap {
								//logger.Debugw(ctx, "MibSync FSM - secondLevelKey", log.Fields{"secondLevelKey": secondLevelKey})
								if uint16ValidNumber, err := strconv.ParseUint(secondLevelKey, 10, 16); err == nil {
									meEntityID := uint16(uint16ValidNumber)
									//logger.Debugw(ctx, "MibSync FSM - secondLevelKey is a number and a valid EntityId", log.Fields{"meEntityID": meEntityID})
									thirdLevelMap := secondLevelValue.(map[string]interface{})
									for thirdLevelKey, thirdLevelValue := range thirdLevelMap {
										if thirdLevelKey == "Attributes" {
											//logger.Debugw(ctx, "MibSync FSM - thirdLevelKey refers to attributes", log.Fields{"thirdLevelKey": thirdLevelKey})
											attributesMap := thirdLevelValue.(map[string]interface{})
											//logger.Debugw(ctx, "MibSync FSM - attributesMap", log.Fields{"attributesMap": attributesMap})
											oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, attributesMap)
											restoredFromMibTemplate = true
										}
									}
								}
							}
						}
					}
				}
			}
		} else {
			logger.Debugw(ctx, "No MIB template found", log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
		}
	} else {
		logger.Errorf(ctx, "Get from kvstore operation failed for path",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	return restoredFromMibTemplate
}

func (oo *OnuDeviceEntry) getAllStoredTpInstFromParentAdapter(ctx context.Context) bool {

	allTpInstPresent := true
	oo.MutexPersOnuConfig.Lock()
	oo.MutexReconciledTpInstances.Lock()
	for indexUni, uniData := range oo.SOnuPersistentData.PersUniConfig {
		uniID := uniData.PersUniID
		oo.ReconciledTpInstances[uniID] = make(map[uint8]inter_adapter.TechProfileDownloadMessage)
		for tpID, tpPath := range uniData.PersTpPathMap {
			if tpPath != "" {
				// Request the TP instance from the openolt adapter
				iaTechTpInst, err := oo.baseDeviceHandler.GetTechProfileInstanceFromParentAdapter(ctx, uniID, tpPath)
				if err == nil && iaTechTpInst != nil {
					logger.Debugw(ctx, "reconciling - store Tp instance", log.Fields{"uniID": uniID, "tpID": tpID,
						"*iaTechTpInst": iaTechTpInst, "device-id": oo.deviceID})
					oo.ReconciledTpInstances[uniID][tpID] = *iaTechTpInst
				} else {
					// During the absence of the ONU adapter there seem to have been TP specific configurations!
					// The no longer available TP and the associated flows must be deleted from the ONU KV store
					// and after a MIB reset a new reconciling attempt with OMCI configuration must be started.
					allTpInstPresent = false
					logger.Infow(ctx, "reconciling - can't get tp instance - delete tp and associated flows",
						log.Fields{"tp-id": tpID, "tpPath": tpPath, "uni-id": uniID, "device-id": oo.deviceID, "err": err})
					delete(oo.SOnuPersistentData.PersUniConfig[indexUni].PersTpPathMap, tpID)
					flowSlice := oo.SOnuPersistentData.PersUniConfig[indexUni].PersFlowParams
					for indexFlow, flowData := range flowSlice {
						if flowData.VlanRuleParams.TpID == tpID {
							if len(flowSlice) == 1 {
								flowSlice = []cmn.UniVlanFlowParams{}
							} else {
								flowSlice = append(flowSlice[:indexFlow], flowSlice[indexFlow+1:]...)
							}
							oo.SOnuPersistentData.PersUniConfig[indexUni].PersFlowParams = flowSlice
						}
					}
				}
			}
		}
	}
	oo.MutexReconciledTpInstances.Unlock()
	oo.MutexPersOnuConfig.Unlock()
	return allTpInstPresent
}

//CancelProcessing terminates potentially running reconciling processes and stops the FSM
func (oo *OnuDeviceEntry) CancelProcessing(ctx context.Context) {
	logger.Debugw(ctx, "CancelProcessing entered", log.Fields{"device-id": oo.deviceID})
	if oo.isReconcilingFlows() {
		oo.SendChReconcilingFlowsFinished(ctx, false)
	}
	//the MibSync FSM might be active all the ONU-active time,
	// hence it must be stopped unconditionally
	oo.mutexMibSyncMsgProcessorRunning.RLock()
	defer oo.mutexMibSyncMsgProcessorRunning.RUnlock()
	if oo.mibSyncMsgProcessorRunning {
		pMibUlFsm := oo.PMibUploadFsm
		if pMibUlFsm != nil {
			// abort running message processing
			fsmAbortMsg := cmn.Message{
				Type: cmn.TestMsg,
				Data: cmn.TestMessage{
					TestMessageVal: cmn.AbortMessageProcessing,
				},
			}
			pMibUlFsm.CommChan <- fsmAbortMsg
			_ = pMibUlFsm.PFsm.Event(UlEvStop)
		}
	}
}
