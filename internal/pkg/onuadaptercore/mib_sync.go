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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/looplab/fsm"

	//"sync"
	"time"

	//"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	//ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
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

func (oo *OnuDeviceEntry) enterStartingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.pOnuDB = newOnuDeviceDB(log.WithSpanFromContext(context.TODO(), ctx), oo)
	go oo.processMibSyncMessages(ctx)
}

func (oo *OnuDeviceEntry) enterResettingMibState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": oo.deviceID})

	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibReset(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true)

	//TODO: needs to handle timeouts
}

func (oo *OnuDeviceEntry) enterGettingVendorAndSerialState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting VendorId and SerialNumber in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"VendorId": "", "SerialNumber": 0}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.OnuGClassID, onugMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingEquipmentIDState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting EquipmentId in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"EquipmentId": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.Onu2GClassID, onu2gMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingFirstSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of first SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, firstSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingSecondSwVersionState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting IsActive and Version of second SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID, secondSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMacAddressState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start getting MacAddress in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"MacAddress": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.IpHostConfigDataClassID, ipHostConfigDataMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMibTemplate(ctx context.Context, e *fsm.Event) {

	for i := firstSwImageMeID; i <= secondSwImageMeID; i++ {
		if oo.swImages[i].isActive > 0 {
			oo.activeSwVersion = oo.swImages[i].version
		}
	}

	meStoredFromTemplate := false
	oo.mibTemplatePath = fmt.Sprintf(cSuffixMibTemplateKvStore, oo.vendorID, oo.equipmentID, oo.activeSwVersion)
	logger.Debugw(ctx, "MibSync FSM - MibTemplate - etcd search string", log.Fields{"path": fmt.Sprintf("%s/%s", cBasePathMibTemplateKvStore, oo.mibTemplatePath)})
	Value, err := oo.mibTemplateKVStore.Get(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath)
	if err == nil {
		if Value != nil {
			logger.Debugf(ctx, "MibSync FSM - MibTemplate read: Key: %s, Value: %s  %s", Value.Key, Value.Value)

			// swap out tokens with specific data
			mibTmpString, _ := kvstore.ToString(Value.Value)
			mibTmpString2 := strings.Replace(mibTmpString, "%SERIAL_NUMBER%", oo.serialNumber, -1)
			mibTmpString = strings.Replace(mibTmpString2, "%MAC_ADDRESS%", oo.macAddress, -1)
			mibTmpBytes := []byte(mibTmpString)
			logger.Debugf(ctx, "MibSync FSM - MibTemplate tokens swapped out: %s", mibTmpBytes)

			var fistLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &fistLevelMap); err != nil {
				logger.Errorw(ctx, "MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": oo.deviceID})
			} else {
				for fistLevelKey, firstLevelValue := range fistLevelMap {
					logger.Debugw(ctx, "MibSync FSM - fistLevelKey", log.Fields{"fistLevelKey": fistLevelKey})
					if uint16ValidNumber, err := strconv.ParseUint(fistLevelKey, 10, 16); err == nil {
						meClassID := me.ClassID(uint16ValidNumber)
						logger.Debugw(ctx, "MibSync FSM - fistLevelKey is a number in uint16-range", log.Fields{"uint16ValidNumber": uint16ValidNumber})
						if isSupportedClassID(meClassID) {
							logger.Debugw(ctx, "MibSync FSM - fistLevelKey is a supported classID", log.Fields{"meClassID": meClassID})
							secondLevelMap := firstLevelValue.(map[string]interface{})
							for secondLevelKey, secondLevelValue := range secondLevelMap {
								logger.Debugw(ctx, "MibSync FSM - secondLevelKey", log.Fields{"secondLevelKey": secondLevelKey})
								if uint16ValidNumber, err := strconv.ParseUint(secondLevelKey, 10, 16); err == nil {
									meEntityID := uint16(uint16ValidNumber)
									logger.Debugw(ctx, "MibSync FSM - secondLevelKey is a number and a valid EntityId", log.Fields{"meEntityID": meEntityID})
									thirdLevelMap := secondLevelValue.(map[string]interface{})
									for thirdLevelKey, thirdLevelValue := range thirdLevelMap {
										if thirdLevelKey == "Attributes" {
											logger.Debugw(ctx, "MibSync FSM - thirdLevelKey refers to attributes", log.Fields{"thirdLevelKey": thirdLevelKey})
											attributesMap := thirdLevelValue.(map[string]interface{})
											logger.Debugw(ctx, "MibSync FSM - attributesMap", log.Fields{"attributesMap": attributesMap})
											oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, attributesMap)
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
			logger.Debugw(ctx, "No MIB template found", log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
		}
	} else {
		logger.Errorf(ctx, "Get from kvstore operation failed for path",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	if meStoredFromTemplate {
		logger.Debug(ctx, "MibSync FSM - valid MEs stored from template")
		oo.pOnuDB.logMeDb(ctx)
		fsmMsg = LoadMibTemplateOk
	} else {
		logger.Debug(ctx, "MibSync FSM - no valid MEs stored from template - perform MIB-upload!")
		fsmMsg = LoadMibTemplateFailed

		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath]; exist {
			if mibTemplateIsGenerated {
				logger.Debugw(ctx,
					"MibSync FSM - template was successfully generated before, but doesn't exist or isn't usable anymore - reset flag in map",
					log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
				oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
			}
		}
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
	}
	mibSyncMsg := Message{
		Type: TestMsg,
		Data: TestMessage{
			TestMessageVal: fsmMsg,
		},
	}
	oo.pMibUploadFsm.commChan <- mibSyncMsg
}

func (oo *OnuDeviceEntry) enterUploadingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send MibUpload in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibUpload(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true)
}

func (oo *OnuDeviceEntry) enterInSyncState(ctx context.Context, e *fsm.Event) {
	oo.mibLastDbSync = uint32(time.Now().Unix())
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.transferSystemEvent(ctx, MibDatabaseSync)
}

func (oo *OnuDeviceEntry) enterExaminingMdsState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.requestMdsValue(ctx)
}

func (oo *OnuDeviceEntry) enterResynchronizingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
}

func (oo *OnuDeviceEntry) enterAuditingState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
}

func (oo *OnuDeviceEntry) enterOutOfSyncState(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug(ctx, "function not implemented yet")
}

func (oo *OnuDeviceEntry) processMibSyncMessages(ctx context.Context) {
	logger.Debugw(ctx, "MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": oo.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		message, ok := <-oo.pMibUploadFsm.commChan
		if !ok {
			logger.Info(ctx, "MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": oo.deviceID})
			break loop
		}
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": oo.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			oo.handleTestMsg(ctx, msg)
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oo.handleOmciMessage(ctx, msg)
		default:
			logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "message.Type": message.Type})
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	// TODO: only this action?
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (oo *OnuDeviceEntry) handleTestMsg(ctx context.Context, msg TestMessage) {

	logger.Debugw(ctx, "MibSync Msg", log.Fields{"TestMessage received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case LoadMibTemplateFailed:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvUploadMib)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	case LoadMibTemplateOk:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		logger.Debugw(ctx, "MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn(ctx, "MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (oo *OnuDeviceEntry) handleOmciMibResetResponseMessage(ctx context.Context, msg OmciMessage) {
	if oo.pMibUploadFsm.pFsm.Is(ulStResettingMib) {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer != nil {
			msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
			if msgOk {
				logger.Debugw(ctx, "MibResetResponse Data", log.Fields{"data-fields": msgObj})
				if msgObj.Result == me.Success {
					oo.mibDataSyncAdpt = 0
					// trigger retrieval of VendorId and SerialNumber
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetVendorAndSerial)
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
		logger.Errorw(ctx, "Wrong Omci MibResetResponse received", log.Fields{"in state ": oo.pMibUploadFsm.pFsm.Current,
			"device-id": oo.deviceID})
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)

}

func (oo *OnuDeviceEntry) handleOmciMibUploadResponseMessage(ctx context.Context, msg OmciMessage) {
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
	oo.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true)
	} else {
		logger.Errorw(ctx, "Invalid number of commands received for:", log.Fields{"device-id": oo.deviceID, "uploadNoOfCmds": oo.PDevOmciCC.uploadNoOfCmds})
		//TODO right action?
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvTimeout)
	}
}

func (oo *OnuDeviceEntry) handleOmciMibUploadNextResponseMessage(ctx context.Context, msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)

	if msgLayer == nil {
		logger.Errorw(ctx, "Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
	if !msgOk {
		logger.Errorw(ctx, "Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
		return
	}
	meName := msgObj.ReportedME.GetName()
	if meName == "UnknownItuG988ManagedEntity" || meName == "UnknownVendorSpecificManagedEntity" {
		logger.Debugw(ctx, "MibUploadNextResponse Data for unknown ME received - temporary workaround is to ignore it!",
			log.Fields{"device-id": oo.deviceID, "data-fields": msgObj, "meName": meName})
	} else {
		logger.Debugw(ctx, "MibUploadNextResponse Data for:",
			log.Fields{"device-id": oo.deviceID, "meName": meName, "data-fields": msgObj})
		meClassID := msgObj.ReportedME.GetClassID()
		meEntityID := msgObj.ReportedME.GetEntityID()
		meAttributes := msgObj.ReportedME.GetAttributeValueMap()
		oo.pOnuDB.PutMe(ctx, meClassID, meEntityID, meAttributes)
	}
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, true)
	} else {
		oo.pOnuDB.logMeDb(ctx)
		err := oo.createAndPersistMibTemplate(ctx)
		if err != nil {
			logger.Errorw(ctx, "MibSync - MibTemplate - Failed to create and persist the mib template", log.Fields{"error": err, "device-id": oo.deviceID})
		}

		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseMessage(ctx context.Context, msg OmciMessage) error {
	var err error = nil
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	logger.Debugw(ctx, "MibSync FSM - GetResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		entityID := oo.PDevOmciCC.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.PDevOmciCC.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meAttributes := msgObj.Attributes
			meInstance := oo.PDevOmciCC.pLastTxMeInstance.GetName()
			logger.Debugf(ctx, "MibSync FSM - GetResponse Data for %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
			switch meInstance {
			case "OnuG":
				oo.vendorID = trimStringFromInterface(meAttributes["VendorId"])
				snBytes, _ := me.InterfaceToOctets(meAttributes["SerialNumber"])
				if onugSerialNumberLen == len(snBytes) {
					snVendorPart := fmt.Sprintf("%s", snBytes[:4])
					snNumberPart := hex.EncodeToString(snBytes[4:])
					oo.serialNumber = snVendorPart + snNumberPart
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-G - VendorId/SerialNumber", log.Fields{"device-id": oo.deviceID,
						"onuDeviceEntry.vendorID": oo.vendorID, "onuDeviceEntry.serialNumber": oo.serialNumber})
				} else {
					logger.Infow(ctx, "MibSync FSM - SerialNumber has wrong length - fill serialNumber with zeros", log.Fields{"device-id": oo.deviceID, "length": len(snBytes)})
					oo.serialNumber = cEmptySerialNumberString
				}
				// trigger retrieval of EquipmentId
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetEquipmentID)
				return nil
			case "Onu2G":
				oo.equipmentID = trimStringFromInterface(meAttributes["EquipmentId"])
				logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu2-G - EquipmentId", log.Fields{"device-id": oo.deviceID,
					"onuDeviceEntry.equipmentID": oo.equipmentID})
				// trigger retrieval of 1st SW-image info
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetFirstSwVersion)
				return nil
			case "SoftwareImage":
				if entityID <= secondSwImageMeID {
					oo.swImages[entityID].version = trimStringFromInterface(meAttributes["Version"])
					oo.swImages[entityID].isActive = meAttributes["IsActive"].(uint8)
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for SoftwareImage - Version/IsActive",
						log.Fields{"device-id": oo.deviceID, "entityID": entityID,
							"version": oo.swImages[entityID].version, "isActive": oo.swImages[entityID].isActive})
				} else {
					err = fmt.Errorf("mibSync FSM - Failed to GetResponse Data for SoftwareImage: %s", oo.deviceID)
				}
				if firstSwImageMeID == entityID {
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetSecondSwVersion)
					return nil
				} else if secondSwImageMeID == entityID {
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMacAddress)
					return nil
				}
			case "IpHostConfigData":
				macBytes, _ := me.InterfaceToOctets(meAttributes["MacAddress"])
				if omciMacAddressLen == len(macBytes) {
					oo.macAddress = hex.EncodeToString(macBytes[:])
					logger.Debugw(ctx, "MibSync FSM - GetResponse Data for IpHostConfigData - MacAddress", log.Fields{"device-id": oo.deviceID,
						"onuDeviceEntry.macAddress": oo.macAddress})
				} else {
					logger.Infow(ctx, "MibSync FSM - MacAddress wrong length - fill macAddress with zeros", log.Fields{"device-id": oo.deviceID, "length": len(macBytes)})
					oo.macAddress = cEmptyMacAddrString
				}
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			case "OnuData":
				mibDataSyncOnu := meAttributes["MibDataSync"].(uint8)
				logger.Debugw(ctx, "MibSync FSM - GetResponse Data for Onu-Data - MibDataSync", log.Fields{"device-id": oo.deviceID,
					"mibDataSyncOnu": mibDataSyncOnu, "oo.mibDataSyncAdpt": oo.mibDataSyncAdpt})
				if oo.pMibUploadFsm.pFsm.Is(ulStExaminingMds) {
					// Examine MDS value
					if oo.mibDataSyncAdpt == mibDataSyncOnu {
						_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
					} else {
						_ = oo.pMibUploadFsm.pFsm.Event(ulEvMismatch)
					}
				}
				return nil
			}
		} else {
			logger.Warnf(ctx, "MibSync FSM - Received GetResponse Data for %s with wrong classID or entityID ", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.EntityClass)
		}
	} else {
		if err = oo.handleOmciGetResponseErrors(ctx, msgObj); err == nil {
			return nil
		}
	}
	logger.Info(ctx, "MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
	return err
}

func (oo *OnuDeviceEntry) handleOmciMessage(ctx context.Context, msg OmciMessage) {
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
		logger.Warnw(ctx, "Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})

	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseErrors(ctx context.Context, msgObj *omci.GetResponse) error {
	var err error = nil
	logger.Debugf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
	// Up to now the following erroneous results have been seen for different ONU-types to indicate an unsupported ME
	if msgObj.Result == me.UnknownInstance || msgObj.Result == me.UnknownEntity || msgObj.Result == me.ProcessingError || msgObj.Result == me.NotSupported {
		entityID := oo.PDevOmciCC.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.PDevOmciCC.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meInstance := oo.PDevOmciCC.pLastTxMeInstance.GetName()
			switch meInstance {
			case "IpHostConfigData":
				logger.Debugw(ctx, "MibSync FSM - erroneous result for IpHostConfigData received - ONU doesn't support ME - fill macAddress with zeros",
					log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				oo.macAddress = cEmptyMacAddrString
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			default:
				logger.Warnf(ctx, "MibSync FSM - erroneous result for %s received - no exceptional treatment defined", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
				err = fmt.Errorf("erroneous result for %s received - no exceptional treatment defined: %s", meInstance, oo.deviceID)
			}
		}
	} else {
		logger.Errorf(ctx, "MibSync FSM - erroneous result in GetResponse Data: %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, msgObj.Result)
		err = fmt.Errorf("erroneous result in GetResponse Data: %s - %s", msgObj.Result, oo.deviceID)
	}
	return err
}

func (oo *OnuDeviceEntry) isNewOnu() bool {
	return oo.mibLastDbSync == 0
}

func isSupportedClassID(meClassID me.ClassID) bool {
	for _, v := range supportedClassIds {
		if v == meClassID {
			return true
		}
	}
	return false
}

func trimStringFromInterface(input interface{}) string {
	ifBytes, _ := me.InterfaceToOctets(input)
	return fmt.Sprintf("%s", bytes.Trim(ifBytes, "\x00"))
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

	oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
	if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath]; exist {
		if mibTemplateIsGenerated {
			logger.Debugw(ctx, "MibSync - MibTemplate - another thread has already started to generate it - skip",
				log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
			oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
			return nil
		}
		logger.Debugw(ctx, "MibSync - MibTemplate - previous generation attempt seems to be failed - try again",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	} else {
		logger.Debugw(ctx, "MibSync - MibTemplate - first ONU-instance of this kind - start generation",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = true
	oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()

	currentTime := time.Now()
	templateMap := make(map[string]interface{})
	templateMap["TemplateName"] = oo.mibTemplatePath
	templateMap["TemplateCreated"] = currentTime.Format("2006-01-02 15:04:05.000000")

	firstLevelMap := oo.pOnuDB.meDb
	for firstLevelKey, firstLevelValue := range firstLevelMap {
		logger.Debugw(ctx, "MibSync - MibTemplate - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
		classID := strconv.Itoa(int(firstLevelKey))

		secondLevelMap := make(map[string]interface{})
		for secondLevelKey, secondLevelValue := range firstLevelValue {
			thirdLevelMap := make(map[string]interface{})
			entityID := strconv.Itoa(int(secondLevelKey))
			thirdLevelMap["Attributes"] = secondLevelValue
			thirdLevelMap["InstanceId"] = entityID
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
		secondLevelMap["ClassId"] = classID
		templateMap[classID] = secondLevelMap
	}
	mibTemplate, err := json.Marshal(&templateMap)
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to marshal mibTemplate", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	err = oo.mibTemplateKVStore.Put(log.WithSpanFromContext(context.TODO(), ctx), oo.mibTemplatePath, string(mibTemplate))
	if err != nil {
		logger.Errorw(ctx, "MibSync - MibTemplate - Failed to store template in etcd", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	logger.Debugw(ctx, "MibSync - MibTemplate - Stored the template to etcd", log.Fields{"device-id": oo.deviceID})
	return nil
}

func (oo *OnuDeviceEntry) requestMdsValue(ctx context.Context) {
	logger.Debugw(ctx, "Request MDS value", log.Fields{"device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"MibDataSync": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
		me.OnuDataClassID, onuDataMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
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
