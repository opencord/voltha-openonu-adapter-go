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

// Package swupg provides the utilities for onu sw upgrade
package swupg

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-protos/v5/go/voltha"
)

// OnuImageStatus implements methods to get status info of onu images
type OnuImageStatus struct {
	pDeviceHandler         cmn.IdeviceHandler
	pDevEntry              cmn.IonuDeviceEntry
	pOmciCC                *cmn.OmciCC
	requestedAttributes    me.AttributeValueMap
	respChannel            chan cmn.Message
	pLastTxMeInstance      *me.ManagedEntity
	deviceID               string
	mutexWaitingForResp    sync.RWMutex
	mutexPLastTxMeInstance sync.RWMutex
	waitingForResp         bool
	isExtendedOmci         bool
}

const cResponse = "response: "

// NewOnuImageStatus creates a new instance of OnuImageStatus
func NewOnuImageStatus(apDeviceHandler cmn.IdeviceHandler, apDevEntry cmn.IonuDeviceEntry) *OnuImageStatus {
	return &OnuImageStatus{
		deviceID:            apDeviceHandler.GetDeviceID(),
		pDeviceHandler:      apDeviceHandler,
		pDevEntry:           apDevEntry,
		pOmciCC:             apDevEntry.GetDevOmciCC(),
		requestedAttributes: make(me.AttributeValueMap),
		waitingForResp:      false,
		respChannel:         make(chan cmn.Message),
		isExtendedOmci:      apDevEntry.GetPersIsExtOmciSupported(),
	}
}

// GetOnuImageStatus - TODO: add comment
func (oo *OnuImageStatus) GetOnuImageStatus(ctx context.Context) (*voltha.OnuImages, error) {

	if !oo.pDeviceHandler.IsReadyForOmciConfig() {
		logger.Errorw(ctx, "command rejected - improper device state", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("command-rejected-improper-device-state")
	}
	if oo.pOmciCC == nil {
		logger.Errorw(ctx, "omciCC not ready to receive omci messages", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("omciCC-not-ready-to-receive-omci-messages")
	}
	var images voltha.OnuImages

	for i := cmn.FirstSwImageMeID; i <= cmn.SecondSwImageMeID; i++ {
		logger.Debugw(ctx, "GetOnuImageStatus for image id", log.Fields{"image-id": i, "device-id": oo.deviceID})

		var image voltha.OnuImage

		// TODO: Since the summed length of the attributes exceeds the capacity of a single response,
		// it is distributed on several requests here. It should be discussed whether, in the course of a refactoring,
		// a global mechanism should be implemented that automates this distribution - which would entail quite some
		// changes on the respective receiver sides.

		oo.requestedAttributes = me.AttributeValueMap{me.SoftwareImage_Version: "", me.SoftwareImage_IsCommitted: 0, me.SoftwareImage_IsActive: 0, me.SoftwareImage_IsValid: 0}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		oo.requestedAttributes = me.AttributeValueMap{me.SoftwareImage_ProductCode: ""}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		oo.requestedAttributes = me.AttributeValueMap{me.SoftwareImage_ImageHash: 0}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		images.Items = append(images.Items, &image)
	}
	logger.Debugw(ctx, "images of the ONU", log.Fields{"images": images})
	oo.updateOnuSwImagePersistentData(ctx)
	return &images, nil
}

func (oo *OnuImageStatus) requestOnuImageAttributes(ctx context.Context, imageID uint16, image *voltha.OnuImage) error {
	oo.mutexPLastTxMeInstance.Lock()
	meInstance, err := oo.pOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID,
		imageID, oo.requestedAttributes, oo.pDeviceHandler.GetOmciTimeout(), true, oo.respChannel, oo.isExtendedOmci)
	if err != nil {
		oo.mutexPLastTxMeInstance.Unlock()
		logger.Errorw(ctx, "can't send omci request to get data for image id", log.Fields{"image-id": imageID, "device-id": oo.deviceID})
		return fmt.Errorf("can't-send-omci-request-to-get-data-for-image-id-%d", imageID)
	}
	oo.pLastTxMeInstance = meInstance
	oo.mutexPLastTxMeInstance.Unlock()

	if err = oo.waitForGetOnuImageStatus(ctx, image); err != nil {
		logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oo.deviceID})
		return err
	}
	return nil
}

func (oo *OnuImageStatus) waitForGetOnuImageStatus(ctx context.Context, image *voltha.OnuImage) error {
	oo.setWaitingForResp(true)
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	case <-ctx.Done():
		logger.Errorw(ctx, "waitForGetOnuImageStatus context done", log.Fields{"device-id": oo.deviceID})
		oo.setWaitingForResp(false)
		return fmt.Errorf("wait-for-image-status-context-done")
	case <-time.After(oo.pOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "waitForGetOnuImageStatus timeout", log.Fields{"device-id": oo.deviceID})
		oo.setWaitingForResp(false)
		return fmt.Errorf("wait-for-image-status-timeout")
	case message, ok := <-oo.respChannel:
		if !ok {
			logger.Errorw(ctx, "waitForGetOnuImageStatus response error", log.Fields{"device-id": oo.deviceID})
			oo.setWaitingForResp(false)
			return fmt.Errorf("wait-for-image-status-response-error")
		}
		switch message.Type {
		case cmn.OMCI:
			msg, _ := message.Data.(cmn.OmciMessage)
			oo.setWaitingForResp(false)
			return oo.processGetOnuImageStatusResp(ctx, msg, image)
		case cmn.TestMsg:
			msg, _ := message.Data.(cmn.TestMessage)
			if msg.TestMessageVal == cmn.AbortMessageProcessing {
				logger.Info(ctx, "waitForGetOnuImageStatus abort msg received", log.Fields{"device-id": oo.deviceID})
				oo.setWaitingForResp(false)
				return fmt.Errorf("wait-for-image-status-abort-msg-received")
			}
		default:
			logger.Errorw(ctx, "waitForGetOnuImageStatus wrong msg type received", log.Fields{"msgType": message.Type, "device-id": oo.deviceID})
			oo.setWaitingForResp(false)
			return fmt.Errorf("wait-for-image-status-response-error")
		}
	}
	logger.Errorw(ctx, "waitForGetOnuImageStatus processing error", log.Fields{"device-id": oo.deviceID})
	oo.setWaitingForResp(false)
	return fmt.Errorf("wait-for-image-status-processing-error")

}

func (oo *OnuImageStatus) processGetOnuImageStatusResp(ctx context.Context, msg cmn.OmciMessage, image *voltha.OnuImage) error {
	if msg.OmciMsg.MessageType != omci.GetResponseType {
		logger.Errorw(ctx, "processGetOnuImageStatusResp wrong response type received", log.Fields{"respType": msg.OmciMsg.MessageType, "device-id": oo.deviceID})
		return fmt.Errorf("process-image-status-response-error")
	}
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "processGetOnuImageStatusResp omci Msg layer not found", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("process-image-status-response-error")
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "processGetOnuImageStatusResp omci msgObj layer could not be found", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("process-image-status-response-error")
	}
	oo.mutexPLastTxMeInstance.RLock()
	if oo.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oo.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oo.pLastTxMeInstance.GetEntityID() {
			oo.mutexPLastTxMeInstance.RUnlock()
			if err := oo.processAttributesReceived(ctx, msgObj, image); err != nil {
				logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oo.deviceID})
				return err
			}
			return nil
		}
		oo.mutexPLastTxMeInstance.RUnlock()
		logger.Errorw(ctx, "processGetOnuImageStatusResp wrong MeInstance received", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("process-image-status-response-error")
	}
	oo.mutexPLastTxMeInstance.RUnlock()
	logger.Errorw(ctx, "processGetOnuImageStatusResp pLastTxMeInstance is nil", log.Fields{"device-id": oo.deviceID})
	return fmt.Errorf("process-image-status-response-error")
}

func (oo *OnuImageStatus) processAttributesReceived(ctx context.Context, msgObj *omci.GetResponse, image *voltha.OnuImage) error {
	meAttributes := msgObj.Attributes
	logger.Debugw(ctx, "processAttributesReceived", log.Fields{"attributes": meAttributes, "device-id": oo.deviceID})

	if _, ok := oo.requestedAttributes[me.SoftwareImage_Version]; ok {
		if msgObj.Result != me.Success {
			logger.Errorw(ctx, "processAttributesReceived - retrieval of mandatory attributes not successful",
				log.Fields{"device-id": oo.deviceID})
			return fmt.Errorf("retrieve-mandatory-attributes-not-successful")
		}
		if !oo.pDevEntry.HandleSwImageIndications(ctx, msgObj.EntityInstance, meAttributes) {
			logger.Errorw(ctx, "processAttributesReceived - not all mandatory attributes present in SoftwareImage instance", log.Fields{"device-id": oo.deviceID})
			return fmt.Errorf("not-all-mandatory-attributes-present")
		}
	}
	for k := range oo.requestedAttributes {
		switch k {
		// mandatory attributes
		case me.SoftwareImage_IsCommitted:
			if meAttributes[me.SoftwareImage_IsCommitted].(uint8) == cmn.SwIsCommitted {
				image.IsCommited = true
			} else {
				image.IsCommited = false
			}
		case me.SoftwareImage_IsActive:
			if meAttributes[me.SoftwareImage_IsActive].(uint8) == cmn.SwIsActive {
				image.IsActive = true
			} else {
				image.IsActive = false
			}
		case me.SoftwareImage_IsValid:
			if meAttributes[me.SoftwareImage_IsValid].(uint8) == cmn.SwIsValid {
				image.IsValid = true
			} else {
				image.IsValid = false
			}
		case me.SoftwareImage_Version:
			image.Version = cmn.TrimStringFromMeOctet(meAttributes[me.SoftwareImage_Version])

		// optional attributes
		case me.SoftwareImage_ProductCode:
			// Check if me.SoftwareImage_ImageHash exists in meAttributes
			if prodAttr, found := meAttributes[me.SoftwareImage_ProductCode]; found {
				// Process the prod attribute if msgObj.Result is successful
				if msgObj.Result == me.Success {
					image.ProductCode = cmn.TrimStringFromMeOctet(prodAttr)
				} else {
					sResult := msgObj.Result.String()
					logger.Infow(ctx, "processAttributesReceived - ProductCode",
						log.Fields{"result": sResult, "unsupported attribute mask": msgObj.UnsupportedAttributeMask, "device-id": oo.deviceID})
					image.ProductCode = cResponse + sResult
				}
			}
		case me.SoftwareImage_ImageHash:
			if msgObj.Result == me.Success {
				bytes, _ := me.InterfaceToOctets(meAttributes[me.SoftwareImage_ImageHash])
				image.Hash = hex.EncodeToString(bytes)
			} else {
				sResult := msgObj.Result.String()
				logger.Infow(ctx, "processAttributesReceived - ImageHash",
					log.Fields{"result": sResult, "unsupported attribute mask": msgObj.UnsupportedAttributeMask, "device-id": oo.deviceID})
				image.Hash = cResponse + sResult
			}
		}
	}
	return nil
}

func (oo *OnuImageStatus) updateOnuSwImagePersistentData(ctx context.Context) {

	activeImageVersion := oo.pDevEntry.GetActiveImageVersion(ctx)
	persActiveSwVersion := oo.pDevEntry.GetPersActiveSwVersion()
	if persActiveSwVersion != activeImageVersion {
		logger.Infow(ctx, "Active SW version has been changed at ONU - update persistent data",
			log.Fields{"old version": persActiveSwVersion,
				"new version": activeImageVersion, "device-id": oo.deviceID})
		oo.pDevEntry.SetPersActiveSwVersion(activeImageVersion)
		if err := oo.pDeviceHandler.StorePersistentData(ctx); err != nil {
			logger.Warnw(ctx, "store persistent data error - continue for now as there will be additional write attempts",
				log.Fields{"device-id": oo.deviceID, "err": err})
		}
		return
	}
}

func (oo *OnuImageStatus) setWaitingForResp(value bool) {
	oo.mutexWaitingForResp.Lock()
	oo.waitingForResp = value
	oo.mutexWaitingForResp.Unlock()
}

func (oo *OnuImageStatus) isWaitingForResp() bool {
	oo.mutexWaitingForResp.RLock()
	value := oo.waitingForResp
	oo.mutexWaitingForResp.RUnlock()
	return value
}

// CancelProcessing ensures that interrupted processing is canceled while waiting for a response
func (oo *OnuImageStatus) CancelProcessing(ctx context.Context) {
	logger.Debugw(ctx, "CancelProcessing entered", log.Fields{"device-id": oo.deviceID})
	if oo.isWaitingForResp() {
		abortMsg := cmn.Message{
			Type: cmn.TestMsg,
			Data: cmn.TestMessage{
				TestMessageVal: cmn.AbortMessageProcessing,
			},
		}
		oo.respChannel <- abortMsg
	}
}
