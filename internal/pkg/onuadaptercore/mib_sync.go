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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/looplab/fsm"

	//"sync"
	"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
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
	onuDeviceEntry.pOnuDB = newOnuDeviceDB(context.TODO(), onuDeviceEntry)
	go onuDeviceEntry.processMibSyncMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterResettingMibState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})

	logger.Debugw("MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	_ = onuDeviceEntry.PDevOmciCC.sendMibReset(context.TODO(), ConstDefaultOmciTimeout, true)

	//TODO: needs to handle timeouts
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingVendorAndSerialState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting VendorId and SerialNumber in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	requestedAttributes := me.AttributeValueMap{"VendorId": "", "SerialNumber": 0}
	meInstance := onuDeviceEntry.PDevOmciCC.sendGetMe(context.TODO(), me.OnuGClassID, onugMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingEquipmentIDState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting EquipmentId in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	requestedAttributes := me.AttributeValueMap{"EquipmentId": ""}
	meInstance := onuDeviceEntry.PDevOmciCC.sendGetMe(context.TODO(), me.Onu2GClassID, onu2gMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingFirstSwVersionState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting IsActive and Version of first SW-image in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := onuDeviceEntry.PDevOmciCC.sendGetMe(context.TODO(), me.SoftwareImageClassID, firstSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingSecondSwVersionState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting IsActive and Version of second SW-image in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := onuDeviceEntry.PDevOmciCC.sendGetMe(context.TODO(), me.SoftwareImageClassID, secondSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingMacAddressState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting MacAddress in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	requestedAttributes := me.AttributeValueMap{"MacAddress": ""}
	meInstance := onuDeviceEntry.PDevOmciCC.sendGetMe(context.TODO(), me.IpHostConfigDataClassID, ipHostConfigDataMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	onuDeviceEntry.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (onuDeviceEntry *OnuDeviceEntry) enterGettingMibTemplate(e *fsm.Event) {

	for i := firstSwImageMeID; i <= secondSwImageMeID; i++ {
		if onuDeviceEntry.swImages[i].isActive > 0 {
			onuDeviceEntry.activeSwVersion = onuDeviceEntry.swImages[i].version
		}
	}

	meStoredFromTemplate := false
	path := fmt.Sprintf(cSuffixMibTemplateKvStore, onuDeviceEntry.vendorID, onuDeviceEntry.equipmentID, onuDeviceEntry.activeSwVersion)
	logger.Debugw("MibSync FSM - MibTemplate - etcd search string", log.Fields{"path": path})
	Value, err := onuDeviceEntry.mibTemplateKVStore.Get(context.TODO(), path)
	if err == nil {
		if Value != nil {
			logger.Debugf("MibSync FSM - MibTemplate read: Key: %s, Value: %s  %s", Value.Key, Value.Value)

			// swap out tokens with specific data
			mibTmpString, _ := kvstore.ToString(Value.Value)
			mibTmpString2 := strings.Replace(mibTmpString, "%SERIAL_NUMBER%", onuDeviceEntry.serialNumber, -1)
			mibTmpString = strings.Replace(mibTmpString2, "%MAC_ADDRESS%", onuDeviceEntry.macAddress, -1)
			mibTmpBytes := []byte(mibTmpString)
			logger.Debugf("MibSync FSM - MibTemplate tokens swapped out: %s", mibTmpBytes)

			var fistLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &fistLevelMap); err != nil {
				logger.Error("MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": onuDeviceEntry.deviceID})
			} else {
				for fistLevelKey, firstLevelValue := range fistLevelMap {
					logger.Debugw("MibSync FSM - fistLevelKey", log.Fields{"fistLevelKey": fistLevelKey})
					if uint16ValidNumber, err := strconv.ParseUint(fistLevelKey, 10, 16); err == nil {
						meClassID := me.ClassID(uint16ValidNumber)
						logger.Debugw("MibSync FSM - fistLevelKey is a number in uint16-range", log.Fields{"uint16ValidNumber": uint16ValidNumber})
						if isSupportedClassID(meClassID) {
							logger.Debugw("MibSync FSM - fistLevelKey is a supported classID", log.Fields{"meClassID": meClassID})
							secondLevelMap := firstLevelValue.(map[string]interface{})
							for secondLevelKey, secondLevelValue := range secondLevelMap {
								logger.Debugw("MibSync FSM - secondLevelKey", log.Fields{"secondLevelKey": secondLevelKey})
								if uint16ValidNumber, err := strconv.ParseUint(secondLevelKey, 10, 16); err == nil {
									meEntityID := uint16(uint16ValidNumber)
									logger.Debugw("MibSync FSM - secondLevelKey is a number and a valid EntityId", log.Fields{"meEntityID": meEntityID})
									thirdLevelMap := secondLevelValue.(map[string]interface{})
									for thirdLevelKey, thirdLevelValue := range thirdLevelMap {
										if thirdLevelKey == "Attributes" {
											logger.Debugw("MibSync FSM - thirdLevelKey refers to attributes", log.Fields{"thirdLevelKey": thirdLevelKey})
											attributesMap := thirdLevelValue.(map[string]interface{})
											logger.Debugw("MibSync FSM - attributesMap", log.Fields{"attributesMap": attributesMap})
											onuDeviceEntry.pOnuDB.PutMe(meClassID, meEntityID, attributesMap)
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
			logger.Debugw("No MIB template found", log.Fields{"path": path, "device-id": onuDeviceEntry.deviceID})
		}
	} else {
		logger.Errorf("Get from kvstore operation failed for path %s", path)
	}
	if meStoredFromTemplate {
		logger.Debug("MibSync FSM - valid MEs stored from template")
		onuDeviceEntry.pOnuDB.logMeDb()
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
	logger.Debugw("MibSync FSM", log.Fields{"send MibUpload in State": e.FSM.Current(), "device-id": onuDeviceEntry.deviceID})
	_ = onuDeviceEntry.PDevOmciCC.sendMibUpload(context.TODO(), ConstDefaultOmciTimeout, true)
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

func (onuDeviceEntry *OnuDeviceEntry) processMibSyncMessages( /*ctx context.Context*/ ) {
	logger.Debugw("MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		message, ok := <-onuDeviceEntry.pMibUploadFsm.commChan
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
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	// TODO: only this action?
	_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (onuDeviceEntry *OnuDeviceEntry) handleTestMsg(msg TestMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"TestMessage received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case LoadMibTemplateFailed:
		_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvUploadMib)
		logger.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.pMibUploadFsm.pFsm.Current())})
	case LoadMibTemplateOk:
		_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		logger.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibResetResponseMessage(msg OmciMessage) {
	if onuDeviceEntry.pMibUploadFsm.pFsm.Is(ulStResettingMib) {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer != nil {
			msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
			if msgOk {
				logger.Debugw("MibResetResponse Data", log.Fields{"data-fields": msgObj})
				if msgObj.Result == me.Success {
					// trigger retrieval of VendorId and SerialNumber
					_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetVendorAndSerial)
					return
				}
				logger.Errorw("Omci MibResetResponse Error", log.Fields{"deviceId": onuDeviceEntry.deviceID, "Error": msgObj.Result})
			} else {
				logger.Error("Omci Msg layer could not be assigned", log.Fields{"deviceId": onuDeviceEntry.deviceID})
			}
		} else {
			logger.Error("Omci Msg layer could not be detected", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		}
	} else {
		logger.Errorw("Omci MibResetResponse received", log.Fields{"in state ": onuDeviceEntry.pMibUploadFsm.pFsm.Current})
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvStop)

}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibUploadResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
	if msgLayer == nil {
		logger.Error("Omci Msg layer could not be detected", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
	if !msgOk {
		logger.Error("Omci Msg layer could not be assigned", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		return
	}
	logger.Debugw("MibUploadResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
	/* to be verified / reworked !!! */
	onuDeviceEntry.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
	if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
		_ = onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
	} else {
		logger.Error("Invalid number of commands received for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "uploadNoOfCmds": onuDeviceEntry.PDevOmciCC.uploadNoOfCmds})
		//TODO right action?
		_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvTimeout)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMibUploadNextResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)
	if msgLayer == nil {
		logger.Error("Omci Msg layer could not be detected", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
	if !msgOk {
		logger.Error("Omci Msg layer could not be assigned", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		return
	}

	logger.Debugw("MibUploadNextResponse Data for:", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
	meClassID := msgObj.ReportedME.GetClassID()
	meEntityID := msgObj.ReportedME.GetEntityID()
	meAttributes := msgObj.ReportedME.GetAttributeValueMap()

	onuDeviceEntry.pOnuDB.PutMe(meClassID, meEntityID, meAttributes)

	if onuDeviceEntry.PDevOmciCC.uploadSequNo < onuDeviceEntry.PDevOmciCC.uploadNoOfCmds {
		_ = onuDeviceEntry.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
	} else {
		onuDeviceEntry.pOnuDB.logMeDb()
		err := onuDeviceEntry.createAndPersistMibTemplate()
		if err != nil {
			logger.Errorw("MibSync - MibTemplate - Failed to create and persist the mib template", log.Fields{"error": err, "device-id": onuDeviceEntry.deviceID})
		}

		_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvSuccess)
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciGetResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer != nil {
		msgObj, msgOk := msgLayer.(*omci.GetResponse)
		if msgOk {
			logger.Debugw("MibSync FSM - GetResponse Data", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj})
			if msgObj.Result == me.Success {
				entityID := onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetEntityID()
				if msgObj.EntityClass == onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
					meAttributes := msgObj.Attributes
					meInstance := onuDeviceEntry.PDevOmciCC.pLastTxMeInstance.GetName()
					logger.Debugf("MibSync FSM - GetResponse Data for %s", log.Fields{"deviceId": onuDeviceEntry.deviceID, "data-fields": msgObj}, meInstance)
					switch meInstance {
					case "OnuG":
						onuDeviceEntry.vendorID = fmt.Sprintf("%s", meAttributes["VendorId"])
						snBytes, _ := me.InterfaceToOctets(meAttributes["SerialNumber"])
						if onugSerialNumberLen == len(snBytes) {
							snVendorPart := fmt.Sprintf("%s", snBytes[:4])
							snNumberPart := hex.EncodeToString(snBytes[4:])
							onuDeviceEntry.serialNumber = snVendorPart + snNumberPart
							logger.Debugw("MibSync FSM - GetResponse Data for Onu-G - VendorId/SerialNumber", log.Fields{"deviceId": onuDeviceEntry.deviceID,
								"onuDeviceEntry.vendorID": onuDeviceEntry.vendorID, "onuDeviceEntry.serialNumber": onuDeviceEntry.serialNumber})
						} else {
							logger.Errorw("MibSync FSM - SerialNumber has wrong length", log.Fields{"deviceId": onuDeviceEntry.deviceID, "length": len(snBytes)})
						}
						// trigger retrieval of EquipmentId
						_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetEquipmentID)
						return
					case "Onu2G":
						onuDeviceEntry.equipmentID = fmt.Sprintf("%s", meAttributes["EquipmentId"])
						logger.Debugw("MibSync FSM - GetResponse Data for Onu2-G - EquipmentId", log.Fields{"deviceId": onuDeviceEntry.deviceID,
							"onuDeviceEntry.equipmentID": onuDeviceEntry.equipmentID})
						// trigger retrieval of 1st SW-image info
						_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetFirstSwVersion)
						return
					case "SoftwareImage":
						if entityID <= secondSwImageMeID {
							onuDeviceEntry.swImages[entityID].version = fmt.Sprintf("%s", meAttributes["Version"])
							onuDeviceEntry.swImages[entityID].isActive = meAttributes["IsActive"].(uint8)
							logger.Debugw("MibSync FSM - GetResponse Data for SoftwareImage - Version/IsActive",
								log.Fields{"deviceId": onuDeviceEntry.deviceID, "entityID": entityID,
									"version": onuDeviceEntry.swImages[entityID].version, "isActive": onuDeviceEntry.swImages[entityID].isActive})
						} else {
							//TODO: error handling
							logger.Errorw("MibSync FSM - Failed to GetResponse Data for SoftwareImage", log.Fields{"deviceId": onuDeviceEntry.deviceID})

						}
						if firstSwImageMeID == entityID {
							_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetSecondSwVersion)
							return
						} else if secondSwImageMeID == entityID {
							_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetMacAddress)
							return
						}
					case "IpHostConfigData":
						macBytes, _ := me.InterfaceToOctets(meAttributes["MacAddress"])
						if omciMacAddressLen == len(macBytes) {
							onuDeviceEntry.macAddress = hex.EncodeToString(macBytes[:])
							logger.Debugw("MibSync FSM - GetResponse Data for IpHostConfigData - MacAddress", log.Fields{"deviceId": onuDeviceEntry.deviceID,
								"onuDeviceEntry.macAddress": onuDeviceEntry.macAddress})
						} else {
							logger.Errorw("MibSync FSM - MacAddress wrong length", log.Fields{"deviceId": onuDeviceEntry.deviceID, "length": len(macBytes)})
						}
						// trigger retrieval of mib template
						_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
						return
					}
				}
			} else {
				logger.Errorw("Omci GetResponse Error", log.Fields{"deviceId": onuDeviceEntry.deviceID, "Error": msgObj.Result})
			}
		} else {
			logger.Error("Omci Msg layer could not be assigned for GetResponse", log.Fields{"deviceId": onuDeviceEntry.deviceID})
		}
	} else {
		logger.Error("Omci Msg layer could not be detected for GetResponse", log.Fields{"deviceId": onuDeviceEntry.deviceID})
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	_ = onuDeviceEntry.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMessage(msg OmciMessage) {
	logger.Debugw("MibSync Msg", log.Fields{"OmciMessage received for device-id": onuDeviceEntry.deviceID,
		"msgType": msg.OmciMsg.MessageType, "msg": msg})
	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		onuDeviceEntry.handleOmciMibResetResponseMessage(msg)

	case omci.MibUploadResponseType:
		onuDeviceEntry.handleOmciMibUploadResponseMessage(msg)

	case omci.MibUploadNextResponseType:
		onuDeviceEntry.handleOmciMibUploadNextResponseMessage(msg)

	case omci.GetResponseType:
		onuDeviceEntry.handleOmciGetResponseMessage(msg)

	default:
		log.Warnw("Unknown Message Type", log.Fields{"msgType": msg.OmciMsg.MessageType})

	}
}

func isSupportedClassID(meClassID me.ClassID) bool {
	for _, v := range supportedClassIds {
		if v == meClassID {
			return true
		}
	}
	return false
}

func (onuDeviceEntry *OnuDeviceEntry) mibDbVolatileDict() error {
	logger.Debug("MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// createAndPersistMibTemplate method creates a mib template for the device id when operator enables the ONU device for the first time.
// We are creating a placeholder for "SerialNumber" for ME Class ID 6 and 256 and "MacAddress" for ME Class ID 134 in the template
// and then storing the template into etcd "service/voltha/omci_mibs/templates/verdor_id/equipment_id/software_version" path.
func (onuDeviceEntry *OnuDeviceEntry) createAndPersistMibTemplate() error {
	path := fmt.Sprintf(cSuffixMibTemplateKvStore, onuDeviceEntry.vendorID, onuDeviceEntry.equipmentID, onuDeviceEntry.activeSwVersion)
	logger.Debugw("MibSync - MibTemplate - key name", log.Fields{"path": path})
	currentTime := time.Now()

	templateMap := make(map[string]interface{})
	templateMap["TemplateName"] = path
	templateMap["TemplateCreated"] = currentTime.Format("2006-01-02 15:04:05.000000")

	firstLevelMap := onuDeviceEntry.pOnuDB.meDb
	for firstLevelKey, firstLevelValue := range firstLevelMap {
		logger.Debugw("MibSync - MibTemplate - firstLevelKey", log.Fields{"firstLevelKey": firstLevelKey})
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
		logger.Errorw("MibSync - MibTemplate - Failed to marshal mibTemplate", log.Fields{"error": err, "device-id": onuDeviceEntry.deviceID})
		return err
	}
	err = onuDeviceEntry.mibTemplateKVStore.Put(context.TODO(), path, string(mibTemplate))
	if err != nil {
		logger.Errorw("MibSync - MibTemplate - Failed to store template in etcd", log.Fields{"error": err, "device-id": onuDeviceEntry.deviceID})
		return err
	}
	logger.Debugw("MibSync - MibTemplate - Stored the template to etcd", log.Fields{"device-id": onuDeviceEntry.deviceID})
	return nil
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
