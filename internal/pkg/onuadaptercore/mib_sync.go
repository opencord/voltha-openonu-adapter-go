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
	"strconv"

	"github.com/looplab/fsm"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

var supportedClassIds = []me.ClassID{
	me.CardholderClassID,                              // 5
	me.CircuitPackClassID,                             // 6
	me.SoftwareImageClassID,                           // 7
	me.PhysicalPathTerminationPointEthernetUniClassID, // 11
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

var fsmMsg TestMessageType

func (onuDeviceEntry *OnuDeviceEntry) enterStartingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})

	onuDeviceEntry.pOnuDB = NewOnuDeviceDB(context.TODO(), onuDeviceEntry)

	go onuDeviceEntry.ProcessMibSyncMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterLoadingMibTemplateState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})

	meStoredFromTemplate := false

	//TODO: perform MIB-reset
	//TODO: needs to handle timeouts

	//TODO: etrieve these values via OMCI GetRequests
	//OltGClassID
	onuDeviceEntry.vendorID = "BBSM"
	onuDeviceEntry.serialNumber = "BBSM00000001"
	//Onu2GClassID
	onuDeviceEntry.equipmentID = "12345123451234512345"
	//SoftwareImageClassID
	onuDeviceEntry.activeSwVersion = "00000000000001"
	//IpHostConfigDataClassID
	onuDeviceEntry.macAddress = "00:00:00:00:00:00"

	Path := fmt.Sprintf(SuffixMibTemplateKvStore, onuDeviceEntry.vendorID, onuDeviceEntry.equipmentID, onuDeviceEntry.activeSwVersion)
	Value, err := onuDeviceEntry.mibTemplateKVStore.Get(context.TODO(), Path)
	if err == nil {
		if Value != nil {
			logger.Debugf("MibSync FSM - MibTemplate read: Key: %s, Value: %s  %s", Value.Key, Value.Value)
			mibTmpBytes, _ := kvstore.ToByte(Value.Value)

			var fistLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &fistLevelMap); err != nil {
				logger.Error("MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": onuDeviceEntry.deviceID})
			} else {
				for fistLevelKey, firstLevelValue := range fistLevelMap {
					logger.Debugw("MibSync FSM - fistLevelKey", log.Fields{"fistLevelKey": fistLevelKey})
					if uint16ValidNumber, err := strconv.ParseUint(fistLevelKey, 10, 16); err == nil {
						meClassId := me.ClassID(uint16ValidNumber)
						logger.Debugw("MibSync FSM - fistLevelKey is a number in uint16-range", log.Fields{"uint16ValidNumber": uint16ValidNumber})
						if IsSupportedClassId(meClassId) {
							logger.Debugw("MibSync FSM - fistLevelKey is a supported classId", log.Fields{"meClassId": meClassId})
							secondLevelMap := firstLevelValue.(map[string]interface{})
							for secondLevelKey, secondLevelValue := range secondLevelMap {
								logger.Debugw("MibSync FSM - secondLevelKey", log.Fields{"secondLevelKey": secondLevelKey})
								if uint16ValidNumber, err := strconv.ParseUint(secondLevelKey, 10, 16); err == nil {
									meEntityId := uint16(uint16ValidNumber)
									logger.Debugw("MibSync FSM - secondLevelKey is a numberand a valid EntityId", log.Fields{"meEntityId": meEntityId})
									thirdLevelMap := secondLevelValue.(map[string]interface{})
									for thirdLevelKey, thirdLevelValue := range thirdLevelMap {
										if thirdLevelKey == "attributes" {
											logger.Debugw("MibSync FSM - thirdLevelKey refers to attributes", log.Fields{"thirdLevelKey": thirdLevelKey})
											attributesMap := thirdLevelValue.(map[string]interface{})
											logger.Debugw("MibSync FSM - attributesMap", log.Fields{"attributesMap": attributesMap})
											onuDeviceEntry.pOnuDB.StoreMe(meClassId, meEntityId, attributesMap)
											meStoredFromTemplate = true
										}
									}
								}
							}
						}
					}
				}
			}
		} else {
			logger.Debugw("No MIB template found", log.Fields{"device-id": onuDeviceEntry.deviceID})
		}
	} else {
		logger.Errorf("Get from kvstore operation failed for path %s", Path)
	}
	if meStoredFromTemplate {
		logger.Debug("MibSync FSM - valid MEs stored from template")
		onuDeviceEntry.pOnuDB.LogMeDb()
		fsmMsg = LoadMibTemplateOk
	} else {
		logger.Debug("MibSync FSM - no valid MEs stored from template - perform MIB-upload!")
		fsmMsg = LoadMibTemplateFailed
	}

	mibSyncMsg := Message{
		Type: TestMsg,
		Data: TestMessage{
			TestMessageVal: fsmMsg,
		},
	}
	onuDeviceEntry.pMibUploadFsm.commChan <- mibSyncMsg
}

func (onuDeviceEntry *OnuDeviceEntry) enterUploadingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.PDevOmciCC.sendMibReset(context.TODO(), ConstDefaultOmciTimeout, true)
}

func (onuDeviceEntry *OnuDeviceEntry) enterInSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.transferSystemEvent(MibDatabaseSync)
}

func (onuDeviceEntry *OnuDeviceEntry) enterExaminingMdsState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterResynchronizingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterAuditingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) enterOutOfSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	logger.Debug("function not implemented yet")
}

func (onuDeviceEntry *OnuDeviceEntry) ProcessMibSyncMessages( /*ctx context.Context*/ ) {
	logger.Debugw("MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		case message, ok := <-onuDeviceEntry.pMibUploadFsm.commChan:
			if !ok {
				logger.Info("MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
				break loop
			}
			logger.Debugw("MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": onuDeviceEntry.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				onuDeviceEntry.handleTestMsg(msg)
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				onuDeviceEntry.handleOmciMessage(msg)
			default:
				logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "message.Type": message.Type})
			}
		}
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	// TODO: only this action?
	onuDeviceEntry.pMibUploadFsm.pFsm.Event("stop")
}

func (onuDeviceEntry *OnuDeviceEntry) handleTestMsg(msg TestMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"TestMessage received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case LoadMibTemplateFailed:
		onuDeviceEntry.pMibUploadFsm.pFsm.Event("upload_mib")
		logger.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.pMibUploadFsm.pFsm.Current())})
	case LoadMibTemplateOk:
		onuDeviceEntry.pMibUploadFsm.pFsm.Event("success")
		logger.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMessage(msg OmciMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"OmciMessage received for device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibResetResponse Data", log.Fields{"data-fields": msgObj})
		if msgObj.Result != me.Success {
			logger.Errorw("Omci MibResetResponse Error - strange - what to do?", log.Fields{"Error": msgObj.Result})
			return
		}
		onuDeviceEntry.PDevOmciCC.sendMibUpload(context.TODO(), ConstDefaultOmciTimeout, true)
	case omci.MibUploadResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibUploadResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
		/* to be verified / reworked !!! */
		onuDeviceEntry.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
		if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
			onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
		} else {
			logger.Error("Invalid number of commands received for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "uploadNoOfCmds": onuDeviceEntry.PDevOmciCC.uploadNoOfCmds})
			//TODO right action?
			onuDeviceEntry.pMibUploadFsm.pFsm.Event("timeout")
		}
	case omci.MibUploadNextResponseType:
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)
		if msgLayer == nil {
			logger.Error("Omci Msg layer could not be detected")
			return
		}
		msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
		if !msgOk {
			logger.Error("Omci Msg layer could not be assigned")
			return
		}
		logger.Debugw("MibUploadNextResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})

		meClassId := msgObj.ReportedME.GetClassID()
		meEntityId := msgObj.ReportedME.GetEntityID()
		meAttributes := msgObj.ReportedME.GetAttributeValueMap()

		onuDeviceEntry.pOnuDB.StoreMe(meClassId, meEntityId, meAttributes)

		if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
			onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
		} else {
			//TODO
			onuDeviceEntry.pOnuDB.LogMeDb()
			onuDeviceEntry.pMibUploadFsm.pFsm.Event("success")
		}
	}
}

func (onuDeviceEntry *OnuDeviceEntry) newKVClient(storeType string, address string, timeout int) (kvstore.Client, error) {
	logger.Infow("kv-store-type", log.Fields{"store": storeType})
	switch storeType {
	case "consul":
		return kvstore.NewConsulClient(address, timeout)
	case "etcd":
		return kvstore.NewEtcdClient(address, timeout, log.FatalLevel)
	}
	return nil, errors.New("unsupported-kv-store")
}

func (onuDeviceEntry *OnuDeviceEntry) SetKVClient(backend string, Host string, Port int, BasePathKvStore string) *db.Backend {
	logger.Debugw("SetKVClient with params:", log.Fields{"backend": backend, "Host": Host, "Port": Port,
		"BasePathKvStore": BasePathKvStore, "deviceId": onuDeviceEntry.deviceID})

	addr := Host + ":" + strconv.Itoa(Port)
	// TODO : Make sure direct call to NewBackend is working fine with backend , currently there is some
	// issue between kv store and backend , core is not calling NewBackend directly
	kvClient, err := onuDeviceEntry.newKVClient(backend, addr, KvstoreTimeout)
	if err != nil {
		logger.Fatalw("Failed to init KV client\n", log.Fields{"err": err})
		return nil
	}

	kvbackend := &db.Backend{
		Client:     kvClient,
		StoreType:  backend,
		Host:       Host,
		Port:       Port,
		Timeout:    KvstoreTimeout,
		PathPrefix: BasePathKvStore}

	return kvbackend
}

func IsSupportedClassId(meClassId me.ClassID) bool {
	for _, v := range supportedClassIds {
		if v == meClassId {
			return true
		}
	}
	return false
}

func (onuDeviceEntry *OnuDeviceEntry) MibDbVolatileDict() error {
	logger.Debug("MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// func (onuDeviceEntry *OnuDeviceEntry) MibTemplateTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibUploadTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) GetMdsTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibResyncTask() error {
// 	return errors.New("not_implemented")
// }
// func (onuDeviceEntry *OnuDeviceEntry) MibReconcileTask() error {
// 	return errors.New("not_implemented")
// }
