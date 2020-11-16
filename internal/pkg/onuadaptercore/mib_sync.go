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

func (oo *OnuDeviceEntry) enterStartingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start processing MibSync-msgs in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.pOnuDB = newOnuDeviceDB(context.TODO(), oo)
	go oo.processMibSyncMessages()
}

func (oo *OnuDeviceEntry) enterResettingMibState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibTemplate processing in State": e.FSM.Current(), "device-id": oo.deviceID})

	logger.Debugw("MibSync FSM", log.Fields{"send mibReset in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibReset(context.TODO(), ConstDefaultOmciTimeout, true)

	//TODO: needs to handle timeouts
}

func (oo *OnuDeviceEntry) enterGettingVendorAndSerialState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting VendorId and SerialNumber in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"VendorId": "", "SerialNumber": 0}
	meInstance := oo.PDevOmciCC.sendGetMe(context.TODO(), me.OnuGClassID, onugMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingEquipmentIDState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting EquipmentId in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"EquipmentId": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(context.TODO(), me.Onu2GClassID, onu2gMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingFirstSwVersionState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting IsActive and Version of first SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(context.TODO(), me.SoftwareImageClassID, firstSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingSecondSwVersionState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting IsActive and Version of second SW-image in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"IsActive": 0, "Version": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(context.TODO(), me.SoftwareImageClassID, secondSwImageMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMacAddressState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start getting MacAddress in State": e.FSM.Current(), "device-id": oo.deviceID})
	requestedAttributes := me.AttributeValueMap{"MacAddress": ""}
	meInstance := oo.PDevOmciCC.sendGetMe(context.TODO(), me.IpHostConfigDataClassID, ipHostConfigDataMeID, requestedAttributes, ConstDefaultOmciTimeout, true)
	//accept also nil as (error) return value for writing to LastTx
	//  - this avoids misinterpretation of new received OMCI messages
	oo.PDevOmciCC.pLastTxMeInstance = meInstance
}

func (oo *OnuDeviceEntry) enterGettingMibTemplate(e *fsm.Event) {

	for i := firstSwImageMeID; i <= secondSwImageMeID; i++ {
		if oo.swImages[i].isActive > 0 {
			oo.activeSwVersion = oo.swImages[i].version
		}
	}

	meStoredFromTemplate := false
	oo.mibTemplatePath = fmt.Sprintf(cSuffixMibTemplateKvStore, oo.vendorID, oo.equipmentID, oo.activeSwVersion)
	logger.Debugw("MibSync FSM - MibTemplate - etcd search string", log.Fields{"path": oo.mibTemplatePath})
	Value, err := oo.mibTemplateKVStore.Get(context.TODO(), oo.mibTemplatePath)
	if err == nil {
		if Value != nil {
			logger.Debugf("MibSync FSM - MibTemplate read: Key: %s, Value: %s  %s", Value.Key, Value.Value)

			// swap out tokens with specific data
			mibTmpString, _ := kvstore.ToString(Value.Value)
			mibTmpString2 := strings.Replace(mibTmpString, "%SERIAL_NUMBER%", oo.serialNumber, -1)
			mibTmpString = strings.Replace(mibTmpString2, "%MAC_ADDRESS%", oo.macAddress, -1)
			mibTmpBytes := []byte(mibTmpString)
			logger.Debugf("MibSync FSM - MibTemplate tokens swapped out: %s", mibTmpBytes)

			var fistLevelMap map[string]interface{}
			if err = json.Unmarshal(mibTmpBytes, &fistLevelMap); err != nil {
				logger.Errorw("MibSync FSM - Failed to unmarshal template", log.Fields{"error": err, "device-id": oo.deviceID})
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
											oo.pOnuDB.PutMe(meClassID, meEntityID, attributesMap)
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
			logger.Debugw("No MIB template found", log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
		}
	} else {
		logger.Errorf("Get from kvstore operation failed for path",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	}
	if meStoredFromTemplate {
		logger.Debug("MibSync FSM - valid MEs stored from template")
		oo.pOnuDB.logMeDb()
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
	oo.pMibUploadFsm.commChan <- mibSyncMsg
}

func (oo *OnuDeviceEntry) enterUploadingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send MibUpload in State": e.FSM.Current(), "device-id": oo.deviceID})
	_ = oo.PDevOmciCC.sendMibUpload(context.TODO(), ConstDefaultOmciTimeout, true)
}

func (oo *OnuDeviceEntry) enterInSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"send notification to core in State": e.FSM.Current(), "device-id": oo.deviceID})
	oo.transferSystemEvent(MibDatabaseSync)
}

func (oo *OnuDeviceEntry) enterExaminingMdsState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start GetMds processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug("function not implemented yet")
}

func (oo *OnuDeviceEntry) enterResynchronizingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug("function not implemented yet")
}

func (oo *OnuDeviceEntry) enterAuditingState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start MibResync processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug("function not implemented yet")
}

func (oo *OnuDeviceEntry) enterOutOfSyncState(e *fsm.Event) {
	logger.Debugw("MibSync FSM", log.Fields{"Start  MibReconcile processing in State": e.FSM.Current(), "device-id": oo.deviceID})
	logger.Debug("function not implemented yet")
}

func (oo *OnuDeviceEntry) processMibSyncMessages( /*ctx context.Context*/ ) {
	logger.Debugw("MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": oo.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		message, ok := <-oo.pMibUploadFsm.commChan
		if !ok {
			logger.Info("MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": oo.deviceID})
			break loop
		}
		logger.Debugw("MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": oo.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			oo.handleTestMsg(msg)
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oo.handleOmciMessage(msg)
		default:
			logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "message.Type": message.Type})
		}
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	// TODO: only this action?
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
}

func (oo *OnuDeviceEntry) handleTestMsg(msg TestMessage) {

	logger.Debugw("MibSync Msg", log.Fields{"TestMessage received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case LoadMibTemplateFailed:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvUploadMib)
		logger.Debugw("MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	case LoadMibTemplateOk:
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
		logger.Debugw("MibSync Msg", log.Fields{"state": string(oo.pMibUploadFsm.pFsm.Current())})
	default:
		logger.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": oo.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (oo *OnuDeviceEntry) handleOmciMibResetResponseMessage(msg OmciMessage) {
	if oo.pMibUploadFsm.pFsm.Is(ulStResettingMib) {
		msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibResetResponse)
		if msgLayer != nil {
			msgObj, msgOk := msgLayer.(*omci.MibResetResponse)
			if msgOk {
				logger.Debugw("MibResetResponse Data", log.Fields{"data-fields": msgObj})
				if msgObj.Result == me.Success {
					// trigger retrieval of VendorId and SerialNumber
					_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetVendorAndSerial)
					return
				}
				logger.Errorw("Omci MibResetResponse Error", log.Fields{"device-id": oo.deviceID, "Error": msgObj.Result})
			} else {
				logger.Errorw("Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
			}
		} else {
			logger.Errorw("Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		}
	} else {
		logger.Errorw("Wrong Omci MibResetResponse received", log.Fields{"in state ": oo.pMibUploadFsm.pFsm.Current,
			"device-id": oo.deviceID})
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)

}

func (oo *OnuDeviceEntry) handleOmciMibUploadResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadResponse)
	if msgLayer == nil {
		logger.Errorw("Omci Msg layer could not be detected", log.Fields{"device-id": oo.deviceID})
		return
	}
	msgObj, msgOk := msgLayer.(*omci.MibUploadResponse)
	if !msgOk {
		logger.Errorw("Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
		return
	}
	logger.Debugw("MibUploadResponse Data for:", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	/* to be verified / reworked !!! */
	oo.PDevOmciCC.uploadNoOfCmds = msgObj.NumberOfCommands
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
	} else {
		logger.Errorw("Invalid number of commands received for:", log.Fields{"device-id": oo.deviceID, "uploadNoOfCmds": oo.PDevOmciCC.uploadNoOfCmds})
		//TODO right action?
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvTimeout)
	}
}

func (oo *OnuDeviceEntry) handleOmciMibUploadNextResponseMessage(msg OmciMessage) {
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeMibUploadNextResponse)

	//TODO: temporary change due to VOL-3532
	// if msgLayer == nil {
	// 	logger.Errorw("Omci Msg layer could not be detected", log.Fields{"device-id": onuDeviceEntry.deviceID})
	// 	return
	// }
	if msgLayer != nil {
		msgObj, msgOk := msgLayer.(*omci.MibUploadNextResponse)
		if !msgOk {
			logger.Errorw("Omci Msg layer could not be assigned", log.Fields{"device-id": oo.deviceID})
			return
		}
		logger.Debugw("MibUploadNextResponse Data for:", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
		meClassID := msgObj.ReportedME.GetClassID()
		meEntityID := msgObj.ReportedME.GetEntityID()
		meAttributes := msgObj.ReportedME.GetAttributeValueMap()

		oo.pOnuDB.PutMe(meClassID, meEntityID, meAttributes)
	} else {
		logger.Warnw("msgLayer could not be decoded - temporary workaround for VOL-3532 in place!", log.Fields{"device-id": oo.deviceID})
	}
	if oo.PDevOmciCC.uploadSequNo < oo.PDevOmciCC.uploadNoOfCmds {
		_ = oo.PDevOmciCC.sendMibUploadNext(context.TODO(), ConstDefaultOmciTimeout, true)
	} else {
		oo.pOnuDB.logMeDb()
		err := oo.createAndPersistMibTemplate()
		if err != nil {
			logger.Errorw("MibSync - MibTemplate - Failed to create and persist the mib template", log.Fields{"error": err, "device-id": oo.deviceID})
		}

		_ = oo.pMibUploadFsm.pFsm.Event(ulEvSuccess)
	}
}

func (oo *OnuDeviceEntry) handleOmciGetResponseMessage(msg OmciMessage) error {
	var err error = nil
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw("omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be detected for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw("omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped", log.Fields{"device-id": oo.deviceID})
		_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
		return fmt.Errorf("omci Msg layer could not be assigned for GetResponse - handling of MibSyncChan stopped: %s", oo.deviceID)
	}
	logger.Debugw("MibSync FSM - GetResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
	if msgObj.Result == me.Success {
		entityID := oo.PDevOmciCC.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.PDevOmciCC.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meAttributes := msgObj.Attributes
			meInstance := oo.PDevOmciCC.pLastTxMeInstance.GetName()
			logger.Debugf("MibSync FSM - GetResponse Data for %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
			switch meInstance {
			case "OnuG":
				oo.vendorID = trimStringFromInterface(meAttributes["VendorId"])
				snBytes, _ := me.InterfaceToOctets(meAttributes["SerialNumber"])
				if onugSerialNumberLen == len(snBytes) {
					snVendorPart := fmt.Sprintf("%s", snBytes[:4])
					snNumberPart := hex.EncodeToString(snBytes[4:])
					oo.serialNumber = snVendorPart + snNumberPart
					logger.Debugw("MibSync FSM - GetResponse Data for Onu-G - VendorId/SerialNumber", log.Fields{"device-id": oo.deviceID,
						"onuDeviceEntry.vendorID": oo.vendorID, "onuDeviceEntry.serialNumber": oo.serialNumber})
				} else {
					logger.Infow("MibSync FSM - SerialNumber has wrong length - fill serialNumber with zeros", log.Fields{"device-id": oo.deviceID, "length": len(snBytes)})
					oo.serialNumber = cEmptySerialNumberString
				}
				// trigger retrieval of EquipmentId
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetEquipmentID)
				return nil
			case "Onu2G":
				oo.equipmentID = trimStringFromInterface(meAttributes["EquipmentId"])
				logger.Debugw("MibSync FSM - GetResponse Data for Onu2-G - EquipmentId", log.Fields{"device-id": oo.deviceID,
					"onuDeviceEntry.equipmentID": oo.equipmentID})
				// trigger retrieval of 1st SW-image info
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetFirstSwVersion)
				return nil
			case "SoftwareImage":
				if entityID <= secondSwImageMeID {
					oo.swImages[entityID].version = trimStringFromInterface(meAttributes["Version"])
					oo.swImages[entityID].isActive = meAttributes["IsActive"].(uint8)
					logger.Debugw("MibSync FSM - GetResponse Data for SoftwareImage - Version/IsActive",
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
					logger.Debugw("MibSync FSM - GetResponse Data for IpHostConfigData - MacAddress", log.Fields{"device-id": oo.deviceID,
						"onuDeviceEntry.macAddress": oo.macAddress})
				} else {
					logger.Infow("MibSync FSM - MacAddress wrong length - fill macAddress with zeros", log.Fields{"device-id": oo.deviceID, "length": len(macBytes)})
					oo.macAddress = cEmptyMacAddrString
				}
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			}
		}
	} else if msgObj.Result == me.UnknownInstance {
		logger.Debugw("MibSync FSM - Unknown Instance in GetResponse Data", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
		entityID := oo.PDevOmciCC.pLastTxMeInstance.GetEntityID()
		if msgObj.EntityClass == oo.PDevOmciCC.pLastTxMeInstance.GetClassID() && msgObj.EntityInstance == entityID {
			meInstance := oo.PDevOmciCC.pLastTxMeInstance.GetName()
			switch meInstance {
			case "IpHostConfigData":
				logger.Debugf("MibSync FSM - Unknown Instance for IpHostConfigData received - ONU doesn't support ME - fill macAddress with zeros",
					log.Fields{"device-id": oo.deviceID, "data-fields": msgObj})
				oo.macAddress = cEmptyMacAddrString
				// trigger retrieval of mib template
				_ = oo.pMibUploadFsm.pFsm.Event(ulEvGetMibTemplate)
				return nil
			default:
				logger.Debugf("MibSync FSM - Unknown Instance in GetResponse Data for %s", log.Fields{"device-id": oo.deviceID, "data-fields": msgObj}, meInstance)
			}
		}
	} else {
		logger.Errorw("mibSync FSM - Omci GetResponse Error", log.Fields{"Error": msgObj.Result, "device-id": oo.deviceID})
		err = fmt.Errorf("mibSync FSM - Omci GetResponse Error: %s", oo.deviceID)
	}
	logger.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": oo.deviceID})
	_ = oo.pMibUploadFsm.pFsm.Event(ulEvStop)
	return err
}

func (oo *OnuDeviceEntry) handleOmciMessage(msg OmciMessage) {
	logger.Debugw("MibSync Msg", log.Fields{"OmciMessage received for device-id": oo.deviceID,
		"msgType": msg.OmciMsg.MessageType, "msg": msg})
	//further analysis could be done here based on msg.OmciMsg.Payload, e.g. verification of error code ...
	switch msg.OmciMsg.MessageType {
	case omci.MibResetResponseType:
		oo.handleOmciMibResetResponseMessage(msg)

	case omci.MibUploadResponseType:
		oo.handleOmciMibUploadResponseMessage(msg)

	case omci.MibUploadNextResponseType:
		oo.handleOmciMibUploadNextResponseMessage(msg)

	case omci.GetResponseType:
		//TODO: error handling
		_ = oo.handleOmciGetResponseMessage(msg)

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

func trimStringFromInterface(input interface{}) string {
	ifBytes, _ := me.InterfaceToOctets(input)
	return fmt.Sprintf("%s", bytes.Trim(ifBytes, "\x00"))
}

func (oo *OnuDeviceEntry) mibDbVolatileDict() error {
	logger.Debug("MibVolatileDict- running from default Entry code")
	return errors.New("not_implemented")
}

// createAndPersistMibTemplate method creates a mib template for the device id when operator enables the ONU device for the first time.
// We are creating a placeholder for "SerialNumber" for ME Class ID 6 and 256 and "MacAddress" for ME Class ID 134 in the template
// and then storing the template into etcd "service/voltha/omci_mibs/go_templates/verdor_id/equipment_id/software_version" path.
func (oo *OnuDeviceEntry) createAndPersistMibTemplate() error {
	logger.Debugw("MibSync - MibTemplate - path name", log.Fields{"path": oo.mibTemplatePath,
		"device-id": oo.deviceID})

	oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
	if mibTemplateIsGenerated, exist := oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath]; exist {
		if mibTemplateIsGenerated {
			logger.Debugw("MibSync - MibTemplate - another thread has already started to generate it - skip",
				log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
			oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
			return nil
		}
		logger.Debugw("MibSync - MibTemplate - previous generation attempt seems to be failed - try again",
			log.Fields{"path": oo.mibTemplatePath, "device-id": oo.deviceID})
	} else {
		logger.Debugw("MibSync - MibTemplate - first ONU-instance of this kind - start generation",
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
		logger.Errorw("MibSync - MibTemplate - Failed to marshal mibTemplate", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	err = oo.mibTemplateKVStore.Put(context.TODO(), oo.mibTemplatePath, string(mibTemplate))
	if err != nil {
		logger.Errorw("MibSync - MibTemplate - Failed to store template in etcd", log.Fields{"error": err, "device-id": oo.deviceID})
		oo.pOpenOnuAc.lockMibTemplateGenerated.Lock()
		oo.pOpenOnuAc.mibTemplatesGenerated[oo.mibTemplatePath] = false
		oo.pOpenOnuAc.lockMibTemplateGenerated.Unlock()
		return err
	}
	logger.Debugw("MibSync - MibTemplate - Stored the template to etcd", log.Fields{"device-id": oo.deviceID})
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
