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
	"fmt"
	"sync"
	"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

//OnuImageStatus implements methods to get status info of onu images
type OnuImageStatus struct {
	pDevEntry              *OnuDeviceEntry
	deviceID               string
	requestedAttributes    me.AttributeValueMap
	omciRespChn            chan Message
	mutexPLastTxMeInstance sync.RWMutex
	pLastTxMeInstance      *me.ManagedEntity
}

//NewOnuImageStatus creates a new instance of OnuImageStatus
func NewOnuImageStatus(pDevEntry *OnuDeviceEntry) *OnuImageStatus {
	return &OnuImageStatus{
		pDevEntry:           pDevEntry,
		deviceID:            pDevEntry.deviceID,
		requestedAttributes: make(me.AttributeValueMap),
		omciRespChn:         make(chan Message),
	}
}

const (
	cImgVersion     = "Version"
	cImgIsCommitted = "IsCommitted"
	cImgIsActive    = "IsActive"
	cImgIsValid     = "IsValid"
	cImgProductCode = "ProductCode"
	cImgImageHash   = "ImageHash"
)

func (oo *OnuImageStatus) getOnuImageStatus(ctx context.Context) (*voltha.OnuImages, error) {

	var images voltha.OnuImages

	if oo.pDevEntry.PDevOmciCC == nil {
		logger.Errorw(ctx, "omciCC not ready to receive omci messages", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("omciCC-not-ready-to-receive-omci-messages")
	}
	for i := firstSwImageMeID; i <= secondSwImageMeID; i++ {
		logger.Debugw(ctx, "getOnuImageStatus for image id", log.Fields{"image-id": i, "device-id": oo.deviceID})

		var image voltha.OnuImage

		// TODO: Since the summed length of the attributes exceeds the capacity of a single response,
		// it is distributed on several requests here. It should be discussed whether, in the course of a refactoring,
		// a global mechanism should be implemented that automates this distribution - which would entail quite some
		// changes on the respective receiver sides.

		oo.requestedAttributes = me.AttributeValueMap{cImgVersion: "", cImgIsCommitted: 0, cImgIsActive: 0, cImgIsValid: 0}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		oo.requestedAttributes = me.AttributeValueMap{cImgProductCode: ""}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		oo.requestedAttributes = me.AttributeValueMap{cImgImageHash: 0}
		if err := oo.requestOnuImageAttributes(ctx, uint16(i), &image); err != nil {
			logger.Errorw(ctx, err.Error(), log.Fields{"requestedAttributes": oo.requestedAttributes, "device-id": oo.deviceID})
			return nil, err
		}
		images.Items = append(images.Items, &image)
	}
	logger.Debugw(ctx, "images of the ONU", log.Fields{"images": images})
	return &images, nil
}

func (oo *OnuImageStatus) requestOnuImageAttributes(ctx context.Context, imageID uint16, image *voltha.OnuImage) error {
	oo.mutexPLastTxMeInstance.Lock()
	meInstance, err := oo.pDevEntry.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID,
		imageID, oo.requestedAttributes, oo.pDevEntry.pOpenOnuAc.omciTimeout, true, oo.omciRespChn)
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

	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	case <-ctx.Done():
		logger.Errorw(ctx, "waitForGetOnuImageStatus context done", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("wait-for-image-status-context-done")
	case <-time.After(oo.pDevEntry.PDevOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "waitForGetOnuImageStatus timeout", log.Fields{"device-id": oo.deviceID})
		return fmt.Errorf("wait-for-image-status-timeout")
	case omciMsg, ok := <-oo.omciRespChn:
		if !ok {
			logger.Errorw(ctx, "waitForGetOnuImageStatus response error", log.Fields{"device-id": oo.deviceID})
			return fmt.Errorf("wait-for-image-status-response-error")
		}
		if omciMsg.Type != OMCI {
			logger.Errorw(ctx, "waitForGetOnuImageStatus wrong msg type received", log.Fields{"msgType": omciMsg.Type, "device-id": oo.deviceID})
			return fmt.Errorf("wait-for-image-status-response-error")
		}
		msg, _ := omciMsg.Data.(OmciMessage)
		return oo.processGetOnuImageStatusResp(ctx, msg, image)
	}
}

func (oo *OnuImageStatus) processGetOnuImageStatusResp(ctx context.Context, msg OmciMessage, image *voltha.OnuImage) error {
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

			meAttributes := msgObj.Attributes
			logger.Debugw(ctx, "processGetOnuImageStatusResp omci attributes received", log.Fields{"attributes": meAttributes, "device-id": oo.deviceID})

			for k := range oo.requestedAttributes {
				switch k {
				case cImgIsCommitted:
					if meAttributes[cImgIsCommitted].(uint8) == swIsCommitted {
						image.IsCommited = true
					}
				case cImgIsActive:
					if meAttributes[cImgIsActive].(uint8) == swIsActive {
						image.IsActive = true
					}
				case cImgIsValid:
					if meAttributes[cImgIsValid].(uint8) == swIsValid {
						image.IsValid = true
					}
				case cImgVersion:
					image.Version = TrimStringFromMeOctet(meAttributes[cImgVersion])
				case cImgProductCode:
					image.ProductCode = TrimStringFromMeOctet(meAttributes[cImgProductCode])
				case cImgImageHash:
					bytes, _ := me.InterfaceToOctets(meAttributes[cImgImageHash])
					image.Hash = hex.EncodeToString(bytes)
				}
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
