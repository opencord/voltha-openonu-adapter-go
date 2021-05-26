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
	omciRespChn            chan Message
	mutexPLastTxMeInstance sync.RWMutex
	pLastTxMeInstance      *me.ManagedEntity
}

//NewOnuImageStatus creates a new instance of OnuImageStatus
func NewOnuImageStatus(pDevEntry *OnuDeviceEntry) *OnuImageStatus {
	return &OnuImageStatus{
		pDevEntry:   pDevEntry,
		deviceID:    pDevEntry.deviceID,
		omciRespChn: make(chan Message),
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
const (
	cMinNoOfAttribbs   = 4 // number of mandatory attributes
	cAttribNotProvided = "not provided"
)

func (oo *OnuImageStatus) getOnuImageStatus(ctx context.Context) (*voltha.OnuImages, error) {

	var images voltha.OnuImages
	var pCurrImages [secondSwImageMeID + 1]*voltha.OnuImage

	if oo.pDevEntry.PDevOmciCC == nil {
		logger.Errorw(ctx, "omciCC not ready to receive omci messages", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("omciCC-not-ready-to-receive-omci-messages")
	}
	for i := firstSwImageMeID; i <= secondSwImageMeID; i++ {
		logger.Debugw(ctx, "getOnuImageStatus for image id", log.Fields{"image-id": i, "device-id": oo.deviceID})
		oo.mutexPLastTxMeInstance.Lock()
		requestedAttributes := me.AttributeValueMap{
			cImgVersion: "", cImgIsCommitted: 0, cImgIsActive: 0, cImgIsValid: 0, cImgProductCode: "", cImgImageHash: ""}
		meInstance, err := oo.pDevEntry.PDevOmciCC.sendGetMe(log.WithSpanFromContext(context.TODO(), ctx), me.SoftwareImageClassID,
			uint16(i), requestedAttributes, oo.pDevEntry.pOpenOnuAc.omciTimeout, true, oo.omciRespChn)
		if err != nil {
			oo.mutexPLastTxMeInstance.Unlock()
			logger.Errorw(ctx, "can't send omci request to get data for image id", log.Fields{"image-id": i, "device-id": oo.deviceID})
			return nil, fmt.Errorf("can't-send-omci-request-to-get-data-for-image-id-%d", i)
		}
		oo.pLastTxMeInstance = meInstance
		oo.mutexPLastTxMeInstance.Unlock()

		if pCurrImages[i], err = oo.waitForGetOnuImageStatus(ctx); err == nil {
			images.Items = append(images.Items, pCurrImages[i])
		} else {
			logger.Errorw(ctx, err.Error(), log.Fields{"device-id": oo.deviceID})
			return nil, err
		}
	}
	logger.Debugw(ctx, "images of the ONU", log.Fields{"images": images})
	return &images, nil
}

func (oo *OnuImageStatus) waitForGetOnuImageStatus(ctx context.Context) (*voltha.OnuImage, error) {

	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	case <-ctx.Done():
		logger.Errorw(ctx, "waitForGetOnuImageStatus context done", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("wait-for-image-status-context-done")
	case <-time.After(oo.pDevEntry.PDevOmciCC.GetMaxOmciTimeoutWithRetries() * time.Second):
		logger.Errorw(ctx, "waitForGetOnuImageStatus timeout", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("wait-for-image-status-timeout")
	case omciMsg, ok := <-oo.omciRespChn:
		if !ok {
			logger.Errorw(ctx, "waitForGetOnuImageStatus response error", log.Fields{"device-id": oo.deviceID})
			return nil, fmt.Errorf("wait-for-image-status-response-error-1")
		}
		if omciMsg.Type != OMCI {
			logger.Errorw(ctx, "waitForGetOnuImageStatus wrong msg type received", log.Fields{"msgType": omciMsg.Type, "device-id": oo.deviceID})
			return nil, fmt.Errorf("wait-for-image-status-response-error-2")
		}
		msg, _ := omciMsg.Data.(OmciMessage)
		return oo.processGetOnuImageStatusResp(ctx, msg)
	}
}

func (oo *OnuImageStatus) processGetOnuImageStatusResp(ctx context.Context, msg OmciMessage) (*voltha.OnuImage, error) {
	if msg.OmciMsg.MessageType != omci.GetResponseType {
		logger.Errorw(ctx, "processGetOnuImageStatusResp wrong response type received", log.Fields{"respType": msg.OmciMsg.MessageType, "device-id": oo.deviceID})
		return nil, fmt.Errorf("process-image-status-response-error-1")
	}
	msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
	if msgLayer == nil {
		logger.Errorw(ctx, "processGetOnuImageStatusResp omci Msg layer not found", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("process-image-status-response-error-2")
	}
	msgObj, msgOk := msgLayer.(*omci.GetResponse)
	if !msgOk {
		logger.Errorw(ctx, "processGetOnuImageStatusResp omci msgObj layer could not be found", log.Fields{"device-id": oo.deviceID})
		return nil, fmt.Errorf("process-image-status-response-error-3")
	}
	oo.mutexPLastTxMeInstance.RLock()
	if oo.pLastTxMeInstance != nil {
		if msgObj.EntityClass == oo.pLastTxMeInstance.GetClassID() &&
			msgObj.EntityInstance == oo.pLastTxMeInstance.GetEntityID() {
			oo.mutexPLastTxMeInstance.RUnlock()

			meAttributes := msgObj.Attributes
			logger.Debugw(ctx, "processGetOnuImageStatusResp omci attributes received", log.Fields{"attributes": meAttributes, "device-id": oo.deviceID})

			if len(meAttributes) < cMinNoOfAttribbs {
				logger.Errorw(ctx, "processGetOnuImageStatusResp too less attributes received", log.Fields{"device-id": oo.deviceID})
				return nil, fmt.Errorf("process-image-status-response-error-4")
			}

			var image voltha.OnuImage

			// read mandatory attibutes
			image.Version = TrimStringFromMeOctet(meAttributes[cImgVersion])

			if meAttributes[cImgIsCommitted].(uint8) == swIsCommitted {
				image.IsCommited = true
			}
			if meAttributes[cImgIsActive].(uint8) == swIsActive {
				image.IsActive = true
			}
			if meAttributes[cImgIsValid].(uint8) == swIsValid {
				image.IsValid = true
			}
			// read optional attributes
			if _, ok := meAttributes[cImgProductCode]; ok {
				image.ProductCode = TrimStringFromMeOctet(meAttributes[cImgProductCode])
			} else {
				image.ProductCode = cAttribNotProvided
			}
			if _, ok := meAttributes[cImgImageHash]; ok {
				bytes, _ := me.InterfaceToOctets(meAttributes[cImgImageHash])
				image.Hash = hex.EncodeToString(bytes)
			} else {
				image.Hash = cAttribNotProvided
			}
			return &image, nil
		}
	}
	oo.mutexPLastTxMeInstance.RUnlock()
	logger.Errorw(ctx, "processGetOnuImageStatusResp wrong MeInstance received", log.Fields{"device-id": oo.deviceID})
	return nil, fmt.Errorf("process-image-status-response-error-5")
}
