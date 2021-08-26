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

//Package swupg provides the utilities for onu sw upgrade
package swupg

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/boguslaw-wojcik/crc32a"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v5/pkg/log"
	cmn "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common"
	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

const cMaxUint32 = ^uint32(0)

const (
	// internal predefined values - some off them should later be configurable (perhaps with theses as defaults)
	cOmciDownloadSectionSize     = 31 //in bytes
	cOmciDownloadWindowSizeLimit = 31 //in sections for window offset (windowSize(32)-1)
	//cOmciDownloadWindowRetryMax  = 2    // max attempts for a specific window
	cOmciSectionInterleaveMilliseconds = 0  //DownloadSection interleave time in milliseconds (0 for no delay)
	cOmciEndSwDlDelaySeconds           = 1  //End Software Download delay after last section (may be also configurable?)
	cWaitCountEndSwDl                  = 6  //maximum number of EndSwDl requests
	cWaitDelayEndSwDlSeconds           = 10 //duration, how long is waited before next request on EndSwDl
	//cOmciDownloadCompleteTimeout = 5400 //in s for the complete timeout (may be better scale to image size/ noOfWindows)
)

// events of config PON ANI port FSM
const (
	UpgradeEvStart              = "UpgradeEvStart"
	UpgradeEvAdapterDownload    = "UpgradeEvAdapterDownload"
	UpgradeEvPrepareSwDownload  = "UpgradeEvPrepareSwDownload"
	UpgradeEvRxStartSwDownload  = "UpgradeEvRxStartSwDownload"
	UpgradeEvWaitWindowAck      = "UpgradeEvWaitWindowAck"
	UpgradeEvContinueNextWindow = "UpgradeEvContinueNextWindow"
	UpgradeEvEndSwDownload      = "UpgradeEvEndSwDownload"
	UpgradeEvWaitEndDownload    = "UpgradeEvWaitEndDownload"
	UpgradeEvContinueFinalize   = "UpgradeEvContinueFinalize"
	UpgradeEvCheckImageName     = "UpgradeEvCheckImageName"
	UpgradeEvWaitForActivate    = "UpgradeEvWaitForActivate"
	UpgradeEvRequestActivate    = "UpgradeEvRequestActivate"
	UpgradeEvActivationDone     = "UpgradeEvActivationDone"
	UpgradeEvWaitForCommit      = "UpgradeEvWaitForCommit"
	UpgradeEvCommitSw           = "UpgradeEvCommitSw"
	UpgradeEvCheckCommitted     = "UpgradeEvCheckCommitted"

	//UpgradeEvTimeoutSimple  = "UpgradeEvTimeoutSimple"
	//UpgradeEvTimeoutMids    = "UpgradeEvTimeoutMids"
	UpgradeEvReset   = "UpgradeEvReset"
	UpgradeEvAbort   = "UpgradeEvAbort"
	UpgradeEvRestart = "UpgradeEvRestart"
)

// states of config PON ANI port FSM
const (
	UpgradeStDisabled           = "UpgradeStDisabled"
	UpgradeStStarting           = "UpgradeStStarting"
	UpgradeStWaitingAdapterDL   = "UpgradeStWaitingAdapterDL"
	UpgradeStPreparingDL        = "UpgradeStPreparingDL"
	UpgradeStDLSection          = "UpgradeStDLSection"
	UpgradeStVerifyWindow       = "UpgradeStVerifyWindow"
	UpgradeStFinalizeDL         = "UpgradeStFinalizeDL"
	UpgradeStWaitEndDL          = "UpgradeStWaitEndDL"
	UpgradeStCheckImageName     = "UpgradeStCheckImageName"
	UpgradeStWaitForActivate    = "UpgradeStWaitForActivate"
	UpgradeStRequestingActivate = "UpgradeStRequestingActivate"
	UpgradeStActivated          = "UpgradeStActivated"
	UpgradeStWaitForCommit      = "UpgradeStWaitForCommit"
	UpgradeStCommitSw           = "UpgradeStCommitSw"
	UpgradeStCheckCommitted     = "UpgradeStCheckCommitted"
	UpgradeStResetting          = "UpgradeStResetting"
)

//COnuUpgradeFsmIdleState - required definition for IdleState detection for activities on OMCI
const COnuUpgradeFsmIdleState = UpgradeStWaitForCommit

//OnuUpgradeFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type OnuUpgradeFsm struct {
	pDeviceHandler   cmn.IdeviceHandler
	pDownloadManager *AdapterDownloadManager
	pFileManager     *FileDownloadManager //used from R2.8 with new API version
	deviceID         string
	pDevEntry        cmn.IonuDeviceEntry
	pOmciCC          *cmn.OmciCC
	pOnuDB           *devdb.OnuDeviceDB
	requestEvent     cmn.OnuDeviceEvent
	//omciMIdsResponseReceived chan bool //seperate channel needed for checking multiInstance OMCI message responses
	PAdaptFsm                        *cmn.AdapterFsm
	pImageDsc                        *voltha.ImageDownload
	imageBuffer                      []byte
	origImageLength                  uint32        //as also limited by OMCI
	imageCRC                         uint32        //as per OMCI - ITU I.363.5 crc
	imageLength                      uint32        //including last bytes padding
	omciDownloadWindowSizeLimit      uint8         //windowSize-1 in sections
	omciDownloadWindowSizeLast       uint8         //number of sections in last window
	noOfSections                     uint32        //uint32 range for sections should be sufficient for very long images
	nextDownloadSectionsAbsolute     uint32        //number of next section to download in overall image
	nextDownloadSectionsWindow       uint8         //number of next section to download within current window
	noOfWindows                      uint32        //uint32 range for windows should be sufficient for very long images
	nextDownloadWindow               uint32        //number of next window to download
	InactiveImageMeID                uint16        //ME-ID of the inactive image
	downloadToOnuTimeout4MB          time.Duration //timeout for downloading the image to the ONU for a 4MB image slice
	omciSectionInterleaveDelay       time.Duration //DownloadSectionInterleave delay in milliseconds
	delayEndSwDl                     bool          //flag to provide a delay between last section and EndSwDl
	pLastTxMeInstance                *me.ManagedEntity
	waitCountEndSwDl                 uint8         //number, how often is waited for EndSwDl at maximum
	waitDelayEndSwDl                 time.Duration //duration, how long is waited before next request on EndSwDl
	chReceiveExpectedResponse        chan bool
	useAPIVersion43                  bool         //flag for indication on which API version is used (and accordingly which specific methods)
	mutexUpgradeParams               sync.RWMutex //mutex to protect members for parallel function requests and omci response processing
	imageVersion                     string       //name of the image as used within OMCI (and on extrenal API interface)
	imageIdentifier                  string       //name of the image as used in the adapter
	mutexIsAwaitingAdapterDlResponse sync.RWMutex
	chAdapterDlReady                 chan bool
	isWaitingForAdapterDlResponse    bool
	chOnuDlReady                     chan bool
	activateImage                    bool
	commitImage                      bool
	mutexAbortRequest                sync.RWMutex
	abortRequested                   voltha.ImageState_ImageFailureReason
	conditionalCancelRequested       bool
	volthaDownloadState              voltha.ImageState_ImageDownloadState
	volthaDownloadReason             voltha.ImageState_ImageFailureReason
	volthaImageState                 voltha.ImageState_ImageActivationState
}

//NewOnuUpgradeFsm is the 'constructor' for the state machine to config the PON ANI ports
//  of ONU UNI ports via OMCI
func NewOnuUpgradeFsm(ctx context.Context, apDeviceHandler cmn.IdeviceHandler,
	apDevEntry cmn.IonuDeviceEntry, apOnuDB *devdb.OnuDeviceDB,
	aRequestEvent cmn.OnuDeviceEvent, aName string, aCommChannel chan cmn.Message) *OnuUpgradeFsm {
	instFsm := &OnuUpgradeFsm{
		pDeviceHandler:              apDeviceHandler,
		deviceID:                    apDeviceHandler.GetDeviceID(),
		pDevEntry:                   apDevEntry,
		pOmciCC:                     apDevEntry.GetDevOmciCC(),
		pOnuDB:                      apOnuDB,
		requestEvent:                aRequestEvent,
		omciDownloadWindowSizeLimit: cOmciDownloadWindowSizeLimit,
		omciSectionInterleaveDelay:  cOmciSectionInterleaveMilliseconds,
		downloadToOnuTimeout4MB:     apDeviceHandler.GetDlToOnuTimeout4M(),
		waitCountEndSwDl:            cWaitCountEndSwDl,
		waitDelayEndSwDl:            cWaitDelayEndSwDlSeconds,
		volthaDownloadState:         voltha.ImageState_DOWNLOAD_STARTED, //if FSM created we can assume that the download (to adapter) really started
		volthaDownloadReason:        voltha.ImageState_NO_ERROR,
		volthaImageState:            voltha.ImageState_IMAGE_UNKNOWN,
		abortRequested:              voltha.ImageState_NO_ERROR,
	}
	instFsm.chReceiveExpectedResponse = make(chan bool)
	instFsm.chAdapterDlReady = make(chan bool)
	instFsm.chOnuDlReady = make(chan bool)

	instFsm.PAdaptFsm = cmn.NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.PAdaptFsm == nil {
		logger.Errorw(ctx, "OnuUpgradeFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}
	instFsm.PAdaptFsm.PFsm = fsm.NewFSM(
		UpgradeStDisabled,
		fsm.Events{
			{Name: UpgradeEvStart, Src: []string{UpgradeStDisabled}, Dst: UpgradeStStarting},
			{Name: UpgradeEvAdapterDownload, Src: []string{UpgradeStStarting}, Dst: UpgradeStWaitingAdapterDL},
			{Name: UpgradeEvPrepareSwDownload, Src: []string{UpgradeStStarting, UpgradeStWaitingAdapterDL}, Dst: UpgradeStPreparingDL},
			{Name: UpgradeEvRxStartSwDownload, Src: []string{UpgradeStPreparingDL}, Dst: UpgradeStDLSection},
			{Name: UpgradeEvWaitWindowAck, Src: []string{UpgradeStDLSection}, Dst: UpgradeStVerifyWindow},
			{Name: UpgradeEvContinueNextWindow, Src: []string{UpgradeStVerifyWindow}, Dst: UpgradeStDLSection},
			{Name: UpgradeEvEndSwDownload, Src: []string{UpgradeStVerifyWindow}, Dst: UpgradeStFinalizeDL},
			{Name: UpgradeEvWaitEndDownload, Src: []string{UpgradeStFinalizeDL}, Dst: UpgradeStWaitEndDL},
			{Name: UpgradeEvContinueFinalize, Src: []string{UpgradeStWaitEndDL}, Dst: UpgradeStFinalizeDL},
			//UpgradeStCheckImageName only used with useAPIVersion43
			{Name: UpgradeEvCheckImageName, Src: []string{UpgradeStWaitEndDL}, Dst: UpgradeStCheckImageName},
			//UpgradeEvWaitForActivate state transitions depend on useAPIVersion43
			{Name: UpgradeEvWaitForActivate, Src: []string{UpgradeStWaitEndDL, UpgradeStCheckImageName}, Dst: UpgradeStWaitForActivate},
			//UpgradeEvRequestActivate state transitions depend on useAPIVersion43
			{Name: UpgradeEvRequestActivate, Src: []string{UpgradeStStarting, UpgradeStWaitEndDL, UpgradeStCheckImageName,
				UpgradeStWaitForActivate}, Dst: UpgradeStRequestingActivate}, //allows also for direct activation (without download) [TODO!!!]
			{Name: UpgradeEvActivationDone, Src: []string{UpgradeStRequestingActivate}, Dst: UpgradeStActivated},
			{Name: UpgradeEvWaitForCommit, Src: []string{UpgradeStRequestingActivate}, Dst: UpgradeStWaitForCommit},
			{Name: UpgradeEvCommitSw, Src: []string{UpgradeStStarting, UpgradeStRequestingActivate, UpgradeStWaitForCommit,
				UpgradeStActivated}, Dst: UpgradeStCommitSw}, //allows also for direct commitment (without download) [TODO!!!]
			{Name: UpgradeEvCheckCommitted, Src: []string{UpgradeStCommitSw}, Dst: UpgradeStCheckCommitted},

			/*
				{Name: UpgradeEvTimeoutSimple, Src: []string{
					UpgradeStCreatingDot1PMapper, UpgradeStCreatingMBPCD, UpgradeStSettingTconts, UpgradeStSettingDot1PMapper}, Dst: UpgradeStStarting},
				{Name: UpgradeEvTimeoutMids, Src: []string{
					UpgradeStCreatingGemNCTPs, UpgradeStCreatingGemIWs, UpgradeStSettingPQs}, Dst: UpgradeStStarting},
			*/
			// exceptional treatments
			//on UpgradeEvReset: UpgradeStRequestingActivate, UpgradeStWaitForCommit and UpgradeStActivated are not reset
			// (to let the FSM survive the expected OnuDown indication)
			{Name: UpgradeEvReset, Src: []string{UpgradeStStarting, UpgradeStWaitingAdapterDL, UpgradeStPreparingDL, UpgradeStDLSection,
				UpgradeStVerifyWindow, UpgradeStDLSection, UpgradeStFinalizeDL, UpgradeStWaitEndDL, UpgradeStCheckImageName,
				UpgradeStWaitForActivate,
				UpgradeStCommitSw, UpgradeStCheckCommitted},
				Dst: UpgradeStResetting},
			{Name: UpgradeEvAbort, Src: []string{UpgradeStStarting, UpgradeStWaitingAdapterDL, UpgradeStPreparingDL, UpgradeStDLSection,
				UpgradeStVerifyWindow, UpgradeStDLSection, UpgradeStFinalizeDL, UpgradeStWaitEndDL, UpgradeStCheckImageName,
				UpgradeStWaitForActivate,
				UpgradeStRequestingActivate, UpgradeStActivated, UpgradeStWaitForCommit,
				UpgradeStCommitSw, UpgradeStCheckCommitted},
				Dst: UpgradeStResetting},
			{Name: UpgradeEvRestart, Src: []string{UpgradeStResetting}, Dst: UpgradeStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                          func(e *fsm.Event) { instFsm.PAdaptFsm.LogFsmStateChange(ctx, e) },
			"enter_" + UpgradeStStarting:           func(e *fsm.Event) { instFsm.enterStarting(ctx, e) },
			"enter_" + UpgradeStWaitingAdapterDL:   func(e *fsm.Event) { instFsm.enterWaitingAdapterDL(ctx, e) },
			"enter_" + UpgradeStPreparingDL:        func(e *fsm.Event) { instFsm.enterPreparingDL(ctx, e) },
			"enter_" + UpgradeStDLSection:          func(e *fsm.Event) { instFsm.enterDownloadSection(ctx, e) },
			"enter_" + UpgradeStVerifyWindow:       func(e *fsm.Event) { instFsm.enterVerifyWindow(ctx, e) },
			"enter_" + UpgradeStFinalizeDL:         func(e *fsm.Event) { instFsm.enterFinalizeDL(ctx, e) },
			"enter_" + UpgradeStWaitEndDL:          func(e *fsm.Event) { instFsm.enterWaitEndDL(ctx, e) },
			"enter_" + UpgradeStCheckImageName:     func(e *fsm.Event) { instFsm.enterCheckImageName(ctx, e) },
			"enter_" + UpgradeStRequestingActivate: func(e *fsm.Event) { instFsm.enterActivateSw(ctx, e) },
			"enter_" + UpgradeStCommitSw:           func(e *fsm.Event) { instFsm.enterCommitSw(ctx, e) },
			"enter_" + UpgradeStCheckCommitted:     func(e *fsm.Event) { instFsm.enterCheckCommitted(ctx, e) },
			"enter_" + UpgradeStResetting:          func(e *fsm.Event) { instFsm.enterResetting(ctx, e) },
			"enter_" + UpgradeStDisabled:           func(e *fsm.Event) { instFsm.enterDisabled(ctx, e) },
		},
	)
	if instFsm.PAdaptFsm.PFsm == nil {
		logger.Errorw(ctx, "OnuUpgradeFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Debugw(ctx, "OnuUpgradeFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//SetDownloadParams configures the needed parameters for a specific download to the ONU
//  called from 'old' API Activate_image_update()
func (oFsm *OnuUpgradeFsm) SetDownloadParams(ctx context.Context, aInactiveImageID uint16,
	apImageDsc *voltha.ImageDownload, apDownloadManager *AdapterDownloadManager) error {
	pBaseFsm := oFsm.PAdaptFsm.PFsm
	if pBaseFsm != nil && pBaseFsm.Is(UpgradeStStarting) {
		oFsm.mutexUpgradeParams.Lock()
		logger.Debugw(ctx, "OnuUpgradeFsm Parameter setting", log.Fields{
			"device-id": oFsm.deviceID, "image-description": apImageDsc})
		oFsm.InactiveImageMeID = aInactiveImageID //upgrade state machines run on configured inactive ImageId
		oFsm.pImageDsc = apImageDsc
		oFsm.pDownloadManager = apDownloadManager
		oFsm.activateImage = true
		oFsm.commitImage = true
		oFsm.mutexUpgradeParams.Unlock()

		go func(aPBaseFsm *fsm.FSM) {
			// let the upgrade FSM proceed to PreparingDL
			_ = aPBaseFsm.Event(UpgradeEvPrepareSwDownload)
		}(pBaseFsm)
		return nil
	}
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer or state", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer or state for device-id: %s", oFsm.deviceID))
}

//SetDownloadParamsAfterDownload configures the needed parameters for a specific download to the ONU according to
//  updated API interface with R2.8: start download to ONU if the image is downloaded to the adapter
//  called from 'new' API Download_onu_image
func (oFsm *OnuUpgradeFsm) SetDownloadParamsAfterDownload(ctx context.Context, aInactiveImageID uint16,
	apImageRequest *voltha.DeviceImageDownloadRequest, apDownloadManager *FileDownloadManager,
	aImageIdentifier string) error {
	oFsm.mutexUpgradeParams.Lock()
	var pBaseFsm *fsm.FSM = nil
	if oFsm.PAdaptFsm != nil {
		pBaseFsm = oFsm.PAdaptFsm.PFsm
	}
	if pBaseFsm != nil && pBaseFsm.Is(UpgradeStStarting) {
		logger.Debugw(ctx, "OnuUpgradeFsm Parameter setting", log.Fields{
			"device-id": oFsm.deviceID, "image-description": apImageRequest})
		oFsm.useAPIVersion43 = true
		oFsm.InactiveImageMeID = aInactiveImageID //upgrade state machines run on configured inactive ImageId
		oFsm.pFileManager = apDownloadManager
		oFsm.imageIdentifier = aImageIdentifier
		oFsm.imageVersion = apImageRequest.Image.Version
		oFsm.activateImage = apImageRequest.ActivateOnSuccess
		oFsm.commitImage = apImageRequest.CommitOnSuccess
		//TODO: currently straightforward options activate and commit are expected to be set and (unconditionally) done
		//  for separate handling of these options the FSM must accordingly branch from the concerned states - later
		oFsm.mutexUpgradeParams.Unlock()
		_ = pBaseFsm.Event(UpgradeEvAdapterDownload) //no need to call the FSM event in background here
		return nil
	}
	oFsm.mutexUpgradeParams.Unlock()
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer or state", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer or state for device-id: %s", oFsm.deviceID))
}

//SetActivationParamsRunning sets the activate and commit flags for a running download to the ONU according to adapters rpc call
//  called from 'new' API Activate_onu_image
func (oFsm *OnuUpgradeFsm) SetActivationParamsRunning(ctx context.Context,
	aImageIdentifier string, aCommit bool) error {
	oFsm.mutexUpgradeParams.Lock()
	//set activate/commit independent from state, if FSM is already beyond concerned states, then it does not matter anyway
	//  (as long as the Imageidentifier is correct)
	logger.Debugw(ctx, "OnuUpgradeFsm activate/commit parameter setting", log.Fields{
		"device-id": oFsm.deviceID, "image-id": aImageIdentifier, "commit": aCommit})
	if aImageIdentifier != oFsm.imageIdentifier {
		logger.Errorw(ctx, "OnuUpgradeFsm abort: mismatching upgrade image", log.Fields{
			"device-id": oFsm.deviceID, "request-image": aImageIdentifier, "fsm-image": oFsm.imageIdentifier})
		oFsm.mutexUpgradeParams.Unlock()
		return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm params ignored: requested image-name not used in current upgrade for device-id: %s",
			oFsm.deviceID))
	}
	oFsm.activateImage = true
	oFsm.commitImage = aCommit
	var pBaseFsm *fsm.FSM = nil
	if oFsm.PAdaptFsm != nil {
		pBaseFsm = oFsm.PAdaptFsm.PFsm
	}
	if pBaseFsm != nil {
		if pBaseFsm.Is(UpgradeStWaitForActivate) {
			oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_STARTED //better choice would be 'UpgradeState=Started'
			oFsm.mutexUpgradeParams.Unlock()
			logger.Debugw(ctx, "OnuUpgradeFsm finish waiting for activate", log.Fields{"device-id": oFsm.deviceID})
			_ = pBaseFsm.Event(UpgradeEvRequestActivate) //no need to call the FSM event in background here
		} else {
			oFsm.mutexUpgradeParams.Unlock()
			logger.Debugw(ctx, "OnuUpgradeFsm not (yet?) waiting for activate", log.Fields{
				"device-id": oFsm.deviceID, "current FsmState": pBaseFsm.Current()})
		}
		return nil
	}
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer for device-id: %s", oFsm.deviceID))
}

//SetActivationParamsStart starts upgrade processing with immediate activation
//  called from 'new' API Activate_onu_image
func (oFsm *OnuUpgradeFsm) SetActivationParamsStart(ctx context.Context, aImageVersion string, aInactiveImageID uint16, aCommit bool) error {
	oFsm.mutexUpgradeParams.Lock()
	var pBaseFsm *fsm.FSM = nil
	if oFsm.PAdaptFsm != nil {
		pBaseFsm = oFsm.PAdaptFsm.PFsm
	}
	if pBaseFsm != nil && pBaseFsm.Is(UpgradeStStarting) {
		logger.Debugw(ctx, "OnuUpgradeFsm Parameter setting to start with activation", log.Fields{
			"device-id": oFsm.deviceID, "image-version": aImageVersion})
		oFsm.useAPIVersion43 = true
		oFsm.InactiveImageMeID = aInactiveImageID //upgrade state machines run on configured inactive ImageId
		oFsm.imageVersion = aImageVersion
		oFsm.activateImage = true
		oFsm.commitImage = aCommit
		// indicate start of the upgrade activity
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_STARTED //better choice would be 'UpgradeState=Started'
		oFsm.volthaImageState = voltha.ImageState_IMAGE_INACTIVE      //as simply applied for inactive image
		oFsm.mutexUpgradeParams.Unlock()
		//directly request the FSM to activate the image
		_ = pBaseFsm.Event(UpgradeEvRequestActivate) //no need to call the FSM event in background here
		return nil
	}
	oFsm.mutexUpgradeParams.Unlock()
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer or state", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer or state for device-id: %s", oFsm.deviceID))
}

//SetCommitmentParamsRunning sets the commit flag for a running download to the ONU according to adapters rpc call
//  called from 'new' API Commit_onu_image
func (oFsm *OnuUpgradeFsm) SetCommitmentParamsRunning(ctx context.Context,
	aImageIdentifier string, aImageVersion string) error {
	oFsm.mutexUpgradeParams.Lock()
	//set commit independent from state, if FSM is already beyond commit state (just ready), then it does not matter anyway
	//  (as long as the Imageidentifier is correct)
	logger.Debugw(ctx, "OnuUpgradeFsm commit parameter setting", log.Fields{
		"device-id": oFsm.deviceID, "image-id": aImageIdentifier})
	if (aImageIdentifier != oFsm.imageIdentifier) && (aImageVersion != oFsm.imageVersion) {
		logger.Errorw(ctx, "OnuUpgradeFsm abort: mismatching upgrade image", log.Fields{
			"device-id": oFsm.deviceID, "request-identifier": aImageIdentifier, "fsm-identifier": oFsm.imageIdentifier,
			"request-version": aImageVersion, "fsm-version": oFsm.imageVersion})
		oFsm.mutexUpgradeParams.Unlock()
		return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm params ignored: requested image-name not used in current upgrade for device-id: %s",
			oFsm.deviceID))
	}
	oFsm.commitImage = true
	oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_STARTED //better choice would be 'UpgradeState=Started'
	oFsm.mutexUpgradeParams.Unlock()
	var pBaseFsm *fsm.FSM = nil
	if oFsm.PAdaptFsm != nil {
		pBaseFsm = oFsm.PAdaptFsm.PFsm
	}
	if pBaseFsm != nil {
		//let the FSM decide if it is ready to process the event
		logger.Debugw(ctx, "OnuUpgradeFsm requesting commit",
			log.Fields{"device-id": oFsm.deviceID, "current FsmState": pBaseFsm.Current()})
		_ = pBaseFsm.Event(UpgradeEvCommitSw) //no need to call the FSM event in background here
		return nil
	}
	//should never occur
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer for device-id: %s", oFsm.deviceID))
}

//SetCommitmentParamsStart starts upgrade processing with immediate commitment
//  called from 'new' API Commit_onu_image
func (oFsm *OnuUpgradeFsm) SetCommitmentParamsStart(ctx context.Context, aImageVersion string, aActiveImageID uint16) error {
	oFsm.mutexUpgradeParams.Lock()
	var pBaseFsm *fsm.FSM = nil
	if oFsm.PAdaptFsm != nil {
		pBaseFsm = oFsm.PAdaptFsm.PFsm
	}
	if pBaseFsm != nil && pBaseFsm.Is(UpgradeStStarting) {
		logger.Debugw(ctx, "OnuUpgradeFsm Parameter setting to start with commitment", log.Fields{
			"device-id": oFsm.deviceID, "image-version": aImageVersion})
		oFsm.useAPIVersion43 = true
		oFsm.InactiveImageMeID = aActiveImageID //upgrade state machines inactive ImageId is the new active ImageId
		oFsm.imageVersion = aImageVersion
		oFsm.commitImage = true
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_STARTED //better choice would be 'UpgradeState=Started'
		oFsm.volthaImageState = voltha.ImageState_IMAGE_ACTIVE        //as simply applied for active image
		oFsm.mutexUpgradeParams.Unlock()
		//directly request the FSM to commit the image
		_ = pBaseFsm.Event(UpgradeEvCommitSw) //no need to call the FSM event in background here
		return nil
	}
	oFsm.mutexUpgradeParams.Unlock()
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer or state", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer or state for device-id: %s", oFsm.deviceID))
}

//GetCommitFlag delivers the commit flag that was configured here
func (oFsm *OnuUpgradeFsm) GetCommitFlag(ctx context.Context) bool {
	oFsm.mutexUpgradeParams.RLock()
	defer oFsm.mutexUpgradeParams.RUnlock()
	return oFsm.commitImage
}

//GetImageStates delivers the download/image states as per device proto buf definition
func (oFsm *OnuUpgradeFsm) GetImageStates(ctx context.Context,
	aImageIdentifier string, aVersion string) *voltha.ImageState {
	pImageState := &voltha.ImageState{}
	pImageState.Version = aVersion //version as requested
	// check if the request refers to some active image/version of the processing
	oFsm.mutexUpgradeParams.RLock()
	if (aImageIdentifier == oFsm.imageIdentifier) || (aVersion == oFsm.imageVersion) {
		pImageState.DownloadState = oFsm.volthaDownloadState
		pImageState.Reason = oFsm.volthaDownloadReason
		pImageState.ImageState = oFsm.volthaImageState
	} else {
		pImageState.DownloadState = voltha.ImageState_DOWNLOAD_UNKNOWN
		pImageState.Reason = voltha.ImageState_NO_ERROR
		pImageState.ImageState = voltha.ImageState_IMAGE_UNKNOWN
	}
	oFsm.mutexUpgradeParams.RUnlock()
	return pImageState
}

//GetSpecificImageState delivers ImageState of the download/image states as per device proto buf definition
func (oFsm *OnuUpgradeFsm) GetSpecificImageState(ctx context.Context) voltha.ImageState_ImageActivationState {
	oFsm.mutexUpgradeParams.RLock()
	imageState := oFsm.volthaImageState
	oFsm.mutexUpgradeParams.RUnlock()
	return imageState
}

//SetImageStateActive sets the FSM internal volthaImageState to ImageState_IMAGE_ACTIVE
func (oFsm *OnuUpgradeFsm) SetImageStateActive(ctx context.Context) {
	oFsm.mutexUpgradeParams.Lock()
	defer oFsm.mutexUpgradeParams.Unlock()
	oFsm.volthaImageState = voltha.ImageState_IMAGE_ACTIVE
	if !oFsm.commitImage {
		//if commit is not additionally set, regard the upgrade activity as successful
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_SUCCEEDED //better choice would be 'UpgradeState=Succeeded'
	}
}

//GetImageVersion delivers image-version of the running upgrade
func (oFsm *OnuUpgradeFsm) GetImageVersion(ctx context.Context) string {
	oFsm.mutexUpgradeParams.RLock()
	imageVersion := oFsm.imageVersion
	oFsm.mutexUpgradeParams.RUnlock()
	return imageVersion
}

//CancelProcessing ensures that suspended processing at waiting on some response is aborted and reset of FSM
func (oFsm *OnuUpgradeFsm) CancelProcessing(ctx context.Context, abCompleteAbort bool,
	aReason voltha.ImageState_ImageFailureReason) {
	oFsm.mutexAbortRequest.Lock()
	oFsm.abortRequested = aReason //possibly abort the sectionDownload loop
	oFsm.mutexAbortRequest.Unlock()
	//mutex protection is required for possible concurrent access to FSM members
	//attention: for an unbuffered channel the sender is blocked until the value is received (processed)!
	// accordingly the mutex must be released before sending to channel here (mutex acquired in receiver)
	oFsm.mutexIsAwaitingAdapterDlResponse.RLock()
	if oFsm.isWaitingForAdapterDlResponse {
		oFsm.mutexIsAwaitingAdapterDlResponse.RUnlock()
		//use channel to indicate that the download response waiting shall be aborted for this device (channel)
		oFsm.chAdapterDlReady <- false
	} else {
		oFsm.mutexIsAwaitingAdapterDlResponse.RUnlock()
	}
	//chOnuDlReady is cleared as part of the FSM reset processing (from enterResetting())

	// in any case (even if it might be automatically requested by above cancellation of waiting) ensure resetting the FSM
	// specific here: See definition of state changes: some states are excluded from reset for possible later commit
	PAdaptFsm := oFsm.PAdaptFsm
	if PAdaptFsm != nil {
		// calling FSM events in background to avoid blocking of the caller
		go func(aPAFsm *cmn.AdapterFsm) {
			if aPAFsm.PFsm != nil {
				if aPAFsm.PFsm.Is(UpgradeStWaitEndDL) {
					oFsm.chReceiveExpectedResponse <- false //which aborts the FSM in WaitEndDL state
				}
				// in case of state-conditional request the

				var err error
				if abCompleteAbort {
					oFsm.mutexUpgradeParams.Lock()
					//any previous lingering conditional cancelRequest is superseded by this abortion
					oFsm.conditionalCancelRequested = false
					if aReason == voltha.ImageState_CANCELLED_ON_REQUEST {
						oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_CANCELLED
					} else {
						oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
					}
					oFsm.volthaDownloadReason = aReason
					oFsm.mutexUpgradeParams.Unlock()
					err = aPAFsm.PFsm.Event(UpgradeEvAbort) //as unconditional default FSM cancellation
				} else {
					//at conditional request the image states are set when reaching the reset state
					oFsm.conditionalCancelRequested = true
					err = aPAFsm.PFsm.Event(UpgradeEvReset) //as state-conditional default FSM cleanup
				}
				if err != nil {
					//error return is expected in case of conditional request and no state transition
					logger.Debugw(ctx, "onu upgrade fsm could not cancel with abort/reset event", log.Fields{
						"device-id": oFsm.deviceID, "error": err})
				}
			} //else the FSM seems already to be in some released state
		}(PAdaptFsm)
	}
}

func (oFsm *OnuUpgradeFsm) enterStarting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})

	// start go routine for processing of LockState messages
	go oFsm.processOmciUpgradeMessages(ctx)
}

//enterWaitingAdapterDL state can only be reached with useAPIVersion43
func (oFsm *OnuUpgradeFsm) enterWaitingAdapterDL(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm waiting for adapter download", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})
	syncChannel := make(chan struct{})
	go oFsm.waitOnDownloadToAdapterReady(ctx, syncChannel, oFsm.chAdapterDlReady)
	//block until the wait routine is really blocked on chAdapterDlReady
	<-syncChannel
	go oFsm.pFileManager.RequestDownloadReady(ctx, oFsm.imageIdentifier, oFsm.chAdapterDlReady)
}

func (oFsm *OnuUpgradeFsm) enterPreparingDL(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm prepare Download to Onu", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})

	var fileLen int64
	var err error
	oFsm.mutexUpgradeParams.Lock()
	if oFsm.useAPIVersion43 {
		//with the new API structure download to adapter is implicit and we have to wait until the image is available
		fileLen, err = oFsm.pFileManager.GetImageBufferLen(ctx, oFsm.imageIdentifier)
	} else {
		fileLen, err = oFsm.pDownloadManager.getImageBufferLen(ctx, oFsm.pImageDsc.Name, oFsm.pImageDsc.LocalDir)
	}
	if err != nil || fileLen > int64(cMaxUint32) {
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_UNKNOWN_ERROR //something like 'LOCAL_FILE_ERROR' would be better (proto)
		oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN
		oFsm.mutexUpgradeParams.Unlock()
		logger.Errorw(ctx, "OnuUpgradeFsm abort: problems getting image buffer length", log.Fields{
			"device-id": oFsm.deviceID, "error": err, "length": fileLen})
		pBaseFsm := oFsm.PAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
		}(pBaseFsm)
		return
	}

	//copy file content to buffer
	oFsm.imageBuffer = make([]byte, fileLen)
	if oFsm.useAPIVersion43 {
		oFsm.imageBuffer, err = oFsm.pFileManager.GetDownloadImageBuffer(ctx, oFsm.imageIdentifier)
	} else {
		oFsm.imageBuffer, err = oFsm.pDownloadManager.getDownloadImageBuffer(ctx, oFsm.pImageDsc.Name, oFsm.pImageDsc.LocalDir)
	}
	if err != nil {
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_UNKNOWN_ERROR //something like 'LOCAL_FILE_ERROR' would be better (proto)
		oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN
		oFsm.mutexUpgradeParams.Unlock()
		logger.Errorw(ctx, "OnuUpgradeFsm abort: can't get image buffer", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		pBaseFsm := oFsm.PAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
		}(pBaseFsm)
		return
	}

	oFsm.noOfSections = uint32(fileLen / cOmciDownloadSectionSize)
	if fileLen%cOmciDownloadSectionSize > 0 {
		bufferPadding := make([]byte, cOmciDownloadSectionSize-uint32((fileLen)%cOmciDownloadSectionSize))
		//expand the imageBuffer to exactly fit multiples of cOmciDownloadSectionSize with padding
		oFsm.imageBuffer = append(oFsm.imageBuffer[:(fileLen)], bufferPadding...)
		oFsm.noOfSections++
	}
	oFsm.origImageLength = uint32(fileLen)
	oFsm.imageLength = uint32(len(oFsm.imageBuffer))
	logger.Infow(ctx, "OnuUpgradeFsm starts with StartSwDl values", log.Fields{
		"MeId": oFsm.InactiveImageMeID, "windowSizeLimit": oFsm.omciDownloadWindowSizeLimit,
		"ImageSize": oFsm.imageLength, "original file size": fileLen})
	//"NumberOfCircuitPacks": oFsm.numberCircuitPacks, "CircuitPacks MeId": 0}) //parallel circuit packs download not supported

	oFsm.mutexUpgradeParams.Unlock()

	// flush commMetricsChan
	select {
	case <-oFsm.chOnuDlReady:
		logger.Debug(ctx, "flushed OnuDlReady channel")
	default:
	}
	go oFsm.waitOnDownloadToOnuReady(ctx, oFsm.chOnuDlReady) // start supervision of the complete download-to-ONU procedure

	err = oFsm.pOmciCC.SendStartSoftwareDownload(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), false,
		oFsm.PAdaptFsm.CommChan, oFsm.InactiveImageMeID, oFsm.omciDownloadWindowSizeLimit, oFsm.origImageLength)
	if err != nil {
		logger.Errorw(ctx, "StartSwDl abort: can't send section", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
		return
	}
}

func (oFsm *OnuUpgradeFsm) enterDownloadSection(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm start downloading sections", log.Fields{
		"device-id": oFsm.deviceID, "absolute window": oFsm.nextDownloadWindow})

	var windowAckRequest uint8 = 0
	var bufferStartOffset uint32
	var bufferEndOffset uint32
	var downloadSection []byte
	FramePrint := false //default no printing of downloadSection frames
	oFsm.mutexUpgradeParams.Lock()
	if oFsm.nextDownloadSectionsAbsolute == 0 {
		//debug print of first section frame
		FramePrint = true
		oFsm.volthaImageState = voltha.ImageState_IMAGE_DOWNLOADING
	}

	for {
		oFsm.mutexAbortRequest.RLock()
		// this way out of the section download loop on abort request
		if oFsm.abortRequested != voltha.ImageState_NO_ERROR {
			if oFsm.abortRequested == voltha.ImageState_CANCELLED_ON_REQUEST {
				oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_CANCELLED
			} else {
				oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
			}
			oFsm.volthaDownloadReason = oFsm.abortRequested
			oFsm.mutexAbortRequest.RUnlock()
			oFsm.mutexUpgradeParams.Unlock()
			pBaseFsm := oFsm.PAdaptFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
			}(pBaseFsm)
			return
		}
		oFsm.mutexAbortRequest.RUnlock()

		bufferStartOffset = oFsm.nextDownloadSectionsAbsolute * cOmciDownloadSectionSize
		bufferEndOffset = bufferStartOffset + cOmciDownloadSectionSize - 1 //for representing cOmciDownloadSectionSizeLimit values
		logger.Debugw(ctx, "DlSection values are", log.Fields{
			"DlSectionNoAbsolute": oFsm.nextDownloadSectionsAbsolute,
			"DlSectionWindow":     oFsm.nextDownloadSectionsWindow,
			"startOffset":         bufferStartOffset, "endOffset": bufferEndOffset})
		if bufferStartOffset+1 > oFsm.imageLength || bufferEndOffset+1 > oFsm.imageLength { //should never occur in this state
			logger.Errorw(ctx, "OnuUpgradeFsm buffer error: exceeded length", log.Fields{
				"device-id": oFsm.deviceID, "bufferStartOffset": bufferStartOffset,
				"bufferEndOffset": bufferEndOffset, "imageLength": oFsm.imageLength})
			oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
			oFsm.volthaDownloadReason = voltha.ImageState_UNKNOWN_ERROR //something like 'LOCAL_FILE_ERROR' would be better (proto)
			oFsm.mutexUpgradeParams.Unlock()
			//logical error -- reset the FSM
			pBaseFsm := oFsm.PAdaptFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
			}(pBaseFsm)
			return
		}
		downloadSection = oFsm.imageBuffer[bufferStartOffset : bufferEndOffset+1]
		if oFsm.nextDownloadSectionsWindow == oFsm.omciDownloadWindowSizeLimit {
			windowAckRequest = 1
			logger.Debugw(ctx, "DlSection expect Response for complete window", log.Fields{
				"device-id": oFsm.deviceID, "in window": oFsm.nextDownloadWindow})
		}
		if oFsm.nextDownloadSectionsAbsolute+1 >= oFsm.noOfSections {
			windowAckRequest = 1
			FramePrint = true //debug print of last frame
			oFsm.omciDownloadWindowSizeLast = oFsm.nextDownloadSectionsWindow
			logger.Infow(ctx, "DlSection expect Response for last window (section)", log.Fields{
				"device-id": oFsm.deviceID, "DlSectionNoAbsolute": oFsm.nextDownloadSectionsAbsolute})
		}
		oFsm.mutexUpgradeParams.Unlock() //unlock here to give other functions some chance to process during/after the send request
		err := oFsm.pOmciCC.SendDownloadSection(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), false,
			oFsm.PAdaptFsm.CommChan, oFsm.InactiveImageMeID, windowAckRequest, oFsm.nextDownloadSectionsWindow, downloadSection, FramePrint)
		if err != nil {
			logger.Errorw(ctx, "DlSection abort: can't send section", log.Fields{
				"device-id": oFsm.deviceID, "section absolute": oFsm.nextDownloadSectionsAbsolute, "error": err})
			oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
			return
		}
		oFsm.mutexUpgradeParams.Lock()
		oFsm.nextDownloadSectionsAbsolute++ //always increase the absolute section counter after having sent one
		if windowAckRequest == 1 {
			pBaseFsm := oFsm.PAdaptFsm
			// Can't call FSM Event directly, decoupling it
			oFsm.mutexUpgradeParams.Unlock()
			go func(a_pAFsm *cmn.AdapterFsm) {
				_ = a_pAFsm.PFsm.Event(UpgradeEvWaitWindowAck) //state transition to UpgradeStVerifyWindow
			}(pBaseFsm)
			return
		}
		FramePrint = false                //for the next Section frame (if wanted, can be enabled in logic before sendXXX())
		oFsm.nextDownloadSectionsWindow++ //increase the window related section counter only if not in the last section
		if oFsm.omciSectionInterleaveDelay > 0 {
			//ensure a defined intersection-time-gap to leave space for further processing, other ONU's ...
			oFsm.mutexUpgradeParams.Unlock() //unlock here to give other functions some chance to process during/after the send request
			time.Sleep(oFsm.omciSectionInterleaveDelay * time.Millisecond)
			oFsm.mutexUpgradeParams.Lock()
		}
	}
}

func (oFsm *OnuUpgradeFsm) enterVerifyWindow(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm verify DL window ack", log.Fields{
		"for window": oFsm.nextDownloadWindow, "device-id": oFsm.deviceID})
}

func (oFsm *OnuUpgradeFsm) enterFinalizeDL(ctx context.Context, e *fsm.Event) {
	logger.Infow(ctx, "OnuUpgradeFsm finalize DL", log.Fields{
		"device-id": oFsm.deviceID, "crc": strconv.FormatInt(int64(oFsm.imageCRC), 16), "delay": oFsm.delayEndSwDl})

	oFsm.mutexUpgradeParams.RLock()
	if oFsm.delayEndSwDl {
		oFsm.mutexUpgradeParams.RUnlock()
		//give the ONU some time for image evaluation (hoping it does not base that on first EndSwDl itself)
		// should not be set in case this state is used for real download abort (not yet implemented)
		time.Sleep(cOmciEndSwDlDelaySeconds * time.Second)
	} else {
		oFsm.mutexUpgradeParams.RUnlock()
	}

	pBaseFsm := oFsm.PAdaptFsm
	if pBaseFsm == nil {
		logger.Errorw(ctx, "EndSwDl abort: BaseFsm invalid", log.Fields{"device-id": oFsm.deviceID})
		oFsm.mutexUpgradeParams.Lock()
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_UNKNOWN_ERROR
		oFsm.mutexUpgradeParams.Unlock()
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
		}(pBaseFsm)
		return
	}
	err := oFsm.pOmciCC.SendEndSoftwareDownload(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), false,
		oFsm.PAdaptFsm.CommChan, oFsm.InactiveImageMeID, oFsm.origImageLength, oFsm.imageCRC)
	if err != nil {
		logger.Errorw(ctx, "EndSwDl abort: can't send section", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
		return
	}
	// go waiting for the EndSwDLResponse and check, if the ONU is ready for activation
	// Can't call FSM Event directly, decoupling it
	go func(a_pAFsm *cmn.AdapterFsm) {
		_ = a_pAFsm.PFsm.Event(UpgradeEvWaitEndDownload)
	}(pBaseFsm)
}

func (oFsm *OnuUpgradeFsm) enterWaitEndDL(ctx context.Context, e *fsm.Event) {
	logger.Infow(ctx, "OnuUpgradeFsm WaitEndDl", log.Fields{
		"device-id": oFsm.deviceID, "wait delay": oFsm.waitDelayEndSwDl * time.Second, "wait count": oFsm.waitCountEndSwDl})
	if oFsm.waitCountEndSwDl == 0 {
		logger.Errorw(ctx, "WaitEndDl abort: max limit of EndSwDL reached", log.Fields{
			"device-id": oFsm.deviceID})
		pBaseFsm := oFsm.PAdaptFsm
		if pBaseFsm == nil {
			logger.Errorw(ctx, "WaitEndDl abort: BaseFsm invalid", log.Fields{
				"device-id": oFsm.deviceID})
			return
		}
		oFsm.mutexUpgradeParams.Lock()
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_IMAGE_REFUSED_BY_ONU //something like 'END_DOWNLOAD_TIMEOUT' would be better (proto)
		oFsm.mutexUpgradeParams.Unlock()
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
		}(pBaseFsm)
		return
	}

	oFsm.waitCountEndSwDl--
	select {
	case <-time.After(oFsm.waitDelayEndSwDl * time.Second):
		pBaseFsm := oFsm.PAdaptFsm
		if pBaseFsm == nil {
			logger.Errorw(ctx, "WaitEndDl abort: BaseFsm invalid", log.Fields{
				"device-id": oFsm.deviceID})
			//FSM may be reset already from somewhere else, nothing we can do here anymore
			return
		}
		//retry End SW DL
		oFsm.mutexUpgradeParams.Lock()
		oFsm.delayEndSwDl = false //no more extra delay for the request
		oFsm.mutexUpgradeParams.Unlock()
		go func(a_pAFsm *cmn.AdapterFsm) {
			_ = a_pAFsm.PFsm.Event(UpgradeEvContinueFinalize)
		}(pBaseFsm)
		return
	case success := <-oFsm.chReceiveExpectedResponse:
		logger.Debugw(ctx, "WaitEndDl stop  wait timer", log.Fields{"device-id": oFsm.deviceID})
		pBaseFsm := oFsm.PAdaptFsm
		if pBaseFsm == nil {
			logger.Errorw(ctx, "WaitEndDl abort: BaseFsm invalid", log.Fields{
				"device-id": oFsm.deviceID})
			//FSM may be reset already from somewhere else, nothing we can do here anymore
			return
		}
		if success {
			//answer received with ready indication
			//useAPIVersion43 may not conflict in concurrency in this state function
			if oFsm.useAPIVersion43 { // newer API usage requires verification of downloaded image version
				go func(a_pAFsm *cmn.AdapterFsm) {
					_ = a_pAFsm.PFsm.Event(UpgradeEvCheckImageName)
				}(pBaseFsm)
			} else { // elder API usage does not support image version check -immediately consider download as successful
				if oFsm.activateImage {
					//immediate activation requested
					go func(a_pAFsm *cmn.AdapterFsm) {
						_ = a_pAFsm.PFsm.Event(UpgradeEvRequestActivate)
					}(pBaseFsm)
				} else {
					//have to wait on explicit activation request
					go func(a_pAFsm *cmn.AdapterFsm) {
						_ = a_pAFsm.PFsm.Event(UpgradeEvWaitForActivate)
					}(pBaseFsm)
				}
			}
			return
		}
		//timer was aborted
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
		return
	}
}

func (oFsm *OnuUpgradeFsm) enterCheckImageName(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm checking downloaded image name", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.InactiveImageMeID})
	requestedAttributes := me.AttributeValueMap{"IsCommitted": 0, "IsActive": 0, "Version": ""}
	meInstance, err := oFsm.pOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
		me.SoftwareImageClassID, oFsm.InactiveImageMeID, requestedAttributes, oFsm.pDeviceHandler.GetOmciTimeout(),
		false, oFsm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "OnuUpgradeFsm get Software Image ME result error",
			log.Fields{"device-id": oFsm.deviceID, "Error": err})
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
		return
	}
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *OnuUpgradeFsm) enterActivateSw(ctx context.Context, e *fsm.Event) {
	logger.Infow(ctx, "OnuUpgradeFsm activate SW", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.InactiveImageMeID})

	err := oFsm.pOmciCC.SendActivateSoftware(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), false,
		oFsm.PAdaptFsm.CommChan, oFsm.InactiveImageMeID)
	if err != nil {
		logger.Errorw(ctx, "ActivateSw abort: can't send activate frame", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_ACTIVATION_ABORTED)
		return
	}
	oFsm.mutexUpgradeParams.Lock()
	oFsm.volthaImageState = voltha.ImageState_IMAGE_ACTIVATING
	oFsm.mutexUpgradeParams.Unlock()
}

func (oFsm *OnuUpgradeFsm) enterCommitSw(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm start commit SW", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.InactiveImageMeID})
	//any abort request (also conditional) is still regarded as valid as the commit indication might not be possible to verify
	// (which is a bit problematic as the ONU might already be in committed state,
	// in this case (committing failed) always 'onuimage list' should be used to verify the real state (if ONU is reachable))
	if activeImageID, err := oFsm.pDevEntry.GetActiveImageMeID(ctx); err == nil {
		oFsm.mutexUpgradeParams.Lock()
		if activeImageID == oFsm.InactiveImageMeID {
			inactiveImageID := oFsm.InactiveImageMeID
			logger.Infow(ctx, "OnuUpgradeFsm commit SW", log.Fields{
				"device-id": oFsm.deviceID, "me-id": inactiveImageID}) //more efficient activeImageID with above check
			oFsm.volthaImageState = voltha.ImageState_IMAGE_COMMITTING
			oFsm.mutexUpgradeParams.Unlock()
			err := oFsm.pOmciCC.SendCommitSoftware(log.WithSpanFromContext(context.TODO(), ctx), oFsm.pDeviceHandler.GetOmciTimeout(), false,
				oFsm.PAdaptFsm.CommChan, inactiveImageID) //more efficient activeImageID with above check
			if err != nil {
				logger.Errorw(ctx, "CommitSw abort: can't send commit sw frame", log.Fields{
					"device-id": oFsm.deviceID, "error": err})
				oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			return
		}
		oFsm.mutexUpgradeParams.Unlock()
		logger.Errorw(ctx, "OnuUpgradeFsm active ImageId <> IdToCommit", log.Fields{
			"device-id": oFsm.deviceID, "active ID": activeImageID, "to commit ID": oFsm.InactiveImageMeID})
	} else {
		logger.Errorw(ctx, "OnuUpgradeFsm can't commit, no valid active image", log.Fields{
			"device-id": oFsm.deviceID})
	}
	oFsm.mutexUpgradeParams.Lock()
	oFsm.conditionalCancelRequested = false //any lingering conditional cancelRequest is superseded by this error
	oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
	oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE
	oFsm.volthaImageState = voltha.ImageState_IMAGE_COMMIT_ABORTED
	oFsm.mutexUpgradeParams.Unlock()
	//TODO!!!: possibly send event information for aborted upgrade (aborted by omci processing)??
	pBaseFsm := oFsm.PAdaptFsm
	// Can't call FSM Event directly, decoupling it
	go func(a_pAFsm *cmn.AdapterFsm) {
		_ = a_pAFsm.PFsm.Event(UpgradeEvAbort)
	}(pBaseFsm)
}

func (oFsm *OnuUpgradeFsm) enterCheckCommitted(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm checking committed SW", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.InactiveImageMeID})
	requestedAttributes := me.AttributeValueMap{"IsCommitted": 0, "IsActive": 0, "Version": ""}
	meInstance, err := oFsm.pOmciCC.SendGetMe(log.WithSpanFromContext(context.TODO(), ctx),
		me.SoftwareImageClassID, oFsm.InactiveImageMeID, requestedAttributes, oFsm.pDeviceHandler.GetOmciTimeout(), false, oFsm.PAdaptFsm.CommChan)
	if err != nil {
		logger.Errorw(ctx, "OnuUpgradeFsm get Software Image ME result error",
			log.Fields{"device-id": oFsm.deviceID, "Error": err})
		oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_COMMIT_ABORTED)
		return
	}
	oFsm.pLastTxMeInstance = meInstance
}

func (oFsm *OnuUpgradeFsm) enterResetting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm resetting", log.Fields{"device-id": oFsm.deviceID})

	// if the reset was conditionally requested
	if oFsm.conditionalCancelRequested {
		oFsm.mutexUpgradeParams.Lock()
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE
		oFsm.mutexUpgradeParams.Unlock()
	}

	// in case the download-to-ONU timer is still running - cancel it
	//use non-blocking channel (to be independent from receiver state)
	select {
	//use channel to indicate that the download response waiting shall be aborted for this device (channel)
	case oFsm.chOnuDlReady <- false:
	default:
	}

	pConfigUpgradeStateAFsm := oFsm.PAdaptFsm
	if pConfigUpgradeStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := cmn.Message{
			Type: cmn.TestMsg,
			Data: cmn.TestMessage{
				TestMessageVal: cmn.AbortMessageProcessing,
			},
		}
		pConfigUpgradeStateAFsm.CommChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled'
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *cmn.AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.PFsm != nil {
				_ = a_pAFsm.PFsm.Event(UpgradeEvRestart)
			}
		}(pConfigUpgradeStateAFsm)
	}
}

func (oFsm *OnuUpgradeFsm) enterDisabled(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm enters disabled state", log.Fields{"device-id": oFsm.deviceID})
	// no need to flush possible channels here, Upgrade FSM will be completely removed, garbage collector should find its way
	if oFsm.pDeviceHandler != nil {
		//request removal of 'reference' in the Handler (completely clear the FSM and its data)
		pLastUpgradeImageState := &voltha.ImageState{
			Version:       oFsm.imageVersion,
			DownloadState: oFsm.volthaDownloadState,
			Reason:        oFsm.volthaDownloadReason,
			ImageState:    oFsm.volthaImageState,
		}
		go oFsm.pDeviceHandler.RemoveOnuUpgradeFsm(ctx, pLastUpgradeImageState)
	}
}

func (oFsm *OnuUpgradeFsm) processOmciUpgradeMessages(ctx context.Context) { //ctx context.Context?
	logger.Debugw(ctx, "Start OnuUpgradeFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info(ctx,"MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.PAdaptFsm.CommChan
		if !ok {
			logger.Info(ctx, "OnuUpgradeFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			oFsm.abortOnOmciError(ctx, true, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
			break loop
		}
		logger.Debugw(ctx, "OnuUpgradeFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case cmn.TestMsg:
			msg, _ := message.Data.(cmn.TestMessage)
			if msg.TestMessageVal == cmn.AbortMessageProcessing {
				logger.Infow(ctx, "OnuUpgradeFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "OnuUpgradeFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case cmn.OMCI:
			msg, _ := message.Data.(cmn.OmciMessage)
			oFsm.handleOmciOnuUpgradeMessage(ctx, msg)
		default:
			logger.Warn(ctx, "OnuUpgradeFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Infow(ctx, "End OnuUpgradeFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

//nolint: gocyclo
func (oFsm *OnuUpgradeFsm) handleOmciOnuUpgradeMessage(ctx context.Context, msg cmn.OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI OnuUpgradeFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.StartSoftwareDownloadResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeStartSoftwareDownloadResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for StartSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.StartSoftwareDownloadResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for StartSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm StartSwDlResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}

			oFsm.mutexUpgradeParams.Lock()
			if msgObj.EntityInstance == oFsm.InactiveImageMeID {
				logger.Debugw(ctx, "Expected StartSwDlResponse received", log.Fields{"device-id": oFsm.deviceID})
				if msgObj.WindowSize != oFsm.omciDownloadWindowSizeLimit {
					// also response WindowSize = 0 is a valid number for used Window size 1
					logger.Debugw(ctx, "different StartSwDlResponse window size requested by ONU", log.Fields{
						"acceptedOnuWindowSizeLimit": msgObj.WindowSize, "device-id": oFsm.deviceID})
					oFsm.omciDownloadWindowSizeLimit = msgObj.WindowSize
				}
				oFsm.noOfWindows = oFsm.noOfSections / uint32(oFsm.omciDownloadWindowSizeLimit+1)
				if oFsm.noOfSections%uint32(oFsm.omciDownloadWindowSizeLimit+1) > 0 {
					oFsm.noOfWindows++
				}
				logger.Debugw(ctx, "OnuUpgradeFsm will use", log.Fields{
					"windows": oFsm.noOfWindows, "sections": oFsm.noOfSections,
					"at WindowSizeLimit": oFsm.omciDownloadWindowSizeLimit})
				oFsm.nextDownloadSectionsAbsolute = 0
				oFsm.nextDownloadSectionsWindow = 0
				oFsm.nextDownloadWindow = 0

				oFsm.mutexUpgradeParams.Unlock()
				_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvRxStartSwDownload)
				return
			}
			oFsm.mutexUpgradeParams.Unlock()
			logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
			return
		} //StartSoftwareDownloadResponseType
	case omci.DownloadSectionResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeDownloadSectionResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for DlSectionResponse",
					log.Fields{"device-id": oFsm.deviceID, "omci-message": msg.OmciMsg})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.DownloadSectionResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for DlSectionResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm DlSectionResponse Data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm DlSectionResponse result error - later: repeat window once?", //TODO!!!
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			oFsm.mutexUpgradeParams.Lock()
			if msgObj.EntityInstance == oFsm.InactiveImageMeID {
				sectionNumber := msgObj.SectionNumber
				logger.Infow(ctx, "DlSectionResponse received", log.Fields{
					"window section-number": sectionNumber, "window": oFsm.nextDownloadWindow, "device-id": oFsm.deviceID})

				oFsm.nextDownloadWindow++
				if oFsm.nextDownloadWindow >= oFsm.noOfWindows {
					if sectionNumber != oFsm.omciDownloadWindowSizeLast {
						logger.Errorw(ctx, "OnuUpgradeFsm DlSectionResponse section error last window - later: repeat window once?", //TODO!!!
							log.Fields{"device-id": oFsm.deviceID, "actual section": sectionNumber,
								"expected section": oFsm.omciDownloadWindowSizeLast})
						oFsm.mutexUpgradeParams.Unlock()
						oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
						return
					}
					oFsm.delayEndSwDl = true //ensure a delay for the EndSwDl message
					//CRC computation for all data bytes of the file
					imageCRC := crc32a.Checksum(oFsm.imageBuffer[:int(oFsm.origImageLength)]) //store internal for multiple usage
					//revert the retrieved CRC Byte Order (seems not to deliver NetworkByteOrder)
					var byteSlice []byte = make([]byte, 4)
					binary.LittleEndian.PutUint32(byteSlice, uint32(imageCRC))
					oFsm.imageCRC = binary.BigEndian.Uint32(byteSlice)
					oFsm.mutexUpgradeParams.Unlock()
					_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvEndSwDownload)
					return
				}
				if sectionNumber != oFsm.omciDownloadWindowSizeLimit {
					logger.Errorw(ctx, "OnuUpgradeFsm DlSectionResponse section error - later: repeat window once?", //TODO!!!
						log.Fields{"device-id": oFsm.deviceID, "actual-section": sectionNumber,
							"expected section": oFsm.omciDownloadWindowSizeLimit})
					oFsm.mutexUpgradeParams.Unlock()
					oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
					return
				}
				oFsm.nextDownloadSectionsWindow = 0
				oFsm.mutexUpgradeParams.Unlock()
				_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvContinueNextWindow)
				return
			}
			oFsm.mutexUpgradeParams.Unlock()
			logger.Errorw(ctx, "OnuUpgradeFsm Omci StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
			return
		} //DownloadSectionResponseType
	case omci.EndSoftwareDownloadResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeEndSoftwareDownloadResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for EndSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.EndSoftwareDownloadResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for EndSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm EndSwDlResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				if msgObj.Result == me.DeviceBusy {
					//ONU indicates it is still processing the image - let the FSM just wait and then repeat the request
					logger.Debugw(ctx, "OnuUpgradeFsm EndSwDlResponse busy: waiting before sending new request", log.Fields{
						"device-id": oFsm.deviceID})
					return
				}
				logger.Errorw(ctx, "OnuUpgradeFsm EndSwDlResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
				return
			}
			oFsm.mutexUpgradeParams.Lock()
			if msgObj.EntityInstance == oFsm.InactiveImageMeID {
				//EndSwDownloadSuccess is used to indicate 'DOWNLOAD_SUCCEEDED'
				oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_SUCCEEDED
				if !oFsm.useAPIVersion43 {
					//in the older API version the image version check was not possible
					//  - assume new loaded image as valid-inactive immediately
					oFsm.volthaImageState = voltha.ImageState_IMAGE_INACTIVE
					oFsm.mutexUpgradeParams.Unlock()
					//use non-blocking channel (to be independent from receiver state)
					select {
					//use non-blocking channel to indicate that the download to ONU was successful
					case oFsm.chOnuDlReady <- true:
					default:
					}
				} else {
					oFsm.mutexUpgradeParams.Unlock()
				}
				logger.Debugw(ctx, "Expected EndSwDlResponse received", log.Fields{"device-id": oFsm.deviceID})
				//use non-blocking channel to let the FSM proceed from the waitState
				select {
				case oFsm.chReceiveExpectedResponse <- true:
				default:
				}
				return
			}
			oFsm.mutexUpgradeParams.Unlock()
			logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_DOWNLOADING)
			return
		} //EndSoftwareDownloadResponseType
	case omci.ActivateSoftwareResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeActivateSoftwareResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for ActivateSw",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_ACTIVATION_ABORTED)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.ActivateSoftwareResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for ActivateSw",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_ACTIVATION_ABORTED)
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm ActivateSwResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm ActivateSwResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_ACTIVATION_ABORTED)
				return
			}
			oFsm.mutexUpgradeParams.Lock()
			if msgObj.EntityInstance == oFsm.InactiveImageMeID {
				// the image is regarded as active really only after ONU reboot and according indication (ONU down/up procedure)
				oFsm.mutexUpgradeParams.Unlock()
				logger.Infow(ctx, "Expected ActivateSwResponse received",
					log.Fields{"device-id": oFsm.deviceID, "commit": oFsm.commitImage})
				if oFsm.commitImage {
					_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvWaitForCommit)
				} else {
					_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvActivationDone) // let the FSM wait for external commit request
				}
				return
			}
			oFsm.mutexUpgradeParams.Unlock()
			logger.Errorw(ctx, "OnuUpgradeFsm ActivateSwResponse wrong ME instance: abort",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_ACTIVATION_ABORTED)
			return
		} //ActivateSoftwareResponseType
	case omci.CommitSoftwareResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeCommitSoftwareResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for CommitResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.CommitSoftwareResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for CommitResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm SwImage CommitResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			oFsm.mutexUpgradeParams.RLock()
			if msgObj.EntityInstance == oFsm.InactiveImageMeID {
				oFsm.mutexUpgradeParams.RUnlock()
				logger.Debugw(ctx, "OnuUpgradeFsm Expected SwImage CommitResponse received", log.Fields{"device-id": oFsm.deviceID})
				//verifying committed image
				_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvCheckCommitted)
				return
			}
			oFsm.mutexUpgradeParams.RUnlock()
			logger.Errorw(ctx, "OnuUpgradeFsm SwImage CommitResponse  wrong ME instance: abort",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
			return
		} //CommitSoftwareResponseType
	case omci.GetResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeGetResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for SwImage GetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			msgObj, msgOk := msgLayer.(*omci.GetResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for SwImage GetResponse",
					log.Fields{"device-id": oFsm.deviceID})
				oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm SwImage GetResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.EntityClass == oFsm.pLastTxMeInstance.GetClassID() &&
				msgObj.EntityInstance == oFsm.pLastTxMeInstance.GetEntityID() {
				if msgObj.Result != me.Success {
					logger.Errorw(ctx, "OnuUpgradeFsm SwImage GetResponse result error - later: drive FSM to abort state ?",
						log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
					oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_COMMIT_ABORTED)
					return
				}
			} else {
				logger.Warnw(ctx, "OnuUpgradeFsm SwImage unexpected Entity GetResponse data - ignore",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}

			meAttributes := msgObj.Attributes
			imageIsCommitted := meAttributes["IsCommitted"].(uint8)
			imageIsActive := meAttributes["IsActive"].(uint8)
			imageVersion := cmn.TrimStringFromMeOctet(meAttributes["Version"])
			logger.Debugw(ctx, "OnuUpgradeFsm - GetResponse Data for SoftwareImage",
				log.Fields{"device-id": oFsm.deviceID, "entityID": msgObj.EntityInstance,
					"version": imageVersion, "isActive": imageIsActive, "isCommitted": imageIsCommitted})

			if oFsm.PAdaptFsm.PFsm.Current() == UpgradeStCheckImageName {
				//image name check after EndSwDownload, this state (and block) can only be taken if APIVersion43 is used
				oFsm.mutexUpgradeParams.Lock()
				if msgObj.EntityInstance == oFsm.InactiveImageMeID && imageIsActive == cmn.SwIsInactive &&
					imageIsCommitted == cmn.SwIsUncommitted {
					if imageVersion != oFsm.imageVersion {
						//new stored inactive version indicated on OMCI from ONU is not the expected version
						logger.Errorw(ctx, "OnuUpgradeFsm SwImage GetResponse version indication not matching requested upgrade",
							log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance,
								"onu-version": imageVersion, "expected-version": oFsm.imageVersion})
						oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED         //not the expected image was downloaded
						oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE //something like 'UNEXPECTED_VERSION' would be better - proto def
						oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN              //something like 'DOWNLOADED' would be better - proto def
						oFsm.mutexUpgradeParams.Unlock()
						//stop the running ONU download timer
						//use non-blocking channel (to be independent from receiver state)
						select {
						//use channel to indicate that the download response waiting shall be aborted for this device (channel)
						case oFsm.chOnuDlReady <- false:
						default:
						}
						// TODO!!!: error treatment?
						//TODO!!!: possibly send event information for aborted upgrade (aborted by wrong version)?
						_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
						return
					}
					//with APIVersion43 this is the point to consider the newly loaded image as valid (and inactive)
					oFsm.volthaImageState = voltha.ImageState_IMAGE_INACTIVE
					//store the new inactive version to onuSwImageIndications (to keep them in sync)
					oFsm.pDevEntry.ModifySwImageInactiveVersion(ctx, oFsm.imageVersion)
					//proceed within upgrade FSM
					if oFsm.activateImage {
						//immediate activation requested
						oFsm.mutexUpgradeParams.Unlock()
						logger.Debugw(ctx, "OnuUpgradeFsm - expected ONU image version indicated by the ONU, continue with activation",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvRequestActivate)
					} else {
						//have to wait on explicit activation request
						// but a previously requested download activity (without activation) was successful here
						oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_SUCCEEDED
						oFsm.mutexUpgradeParams.Unlock()
						logger.Infow(ctx, "OnuUpgradeFsm - expected ONU image version indicated by the ONU, wait for activate request",
							log.Fields{"device-id": oFsm.deviceID})
						_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvWaitForActivate)
					}
					//use non-blocking channel (to be independent from receiver state)
					select {
					//use non-blocking channel to indicate that the download to ONU was successful
					case oFsm.chOnuDlReady <- true:
					default:
					}
					return
				}
				//not the expected image/image state
				oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
				oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE
				oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN //real image state not known
				oFsm.mutexUpgradeParams.Unlock()
				logger.Errorw(ctx, "OnuUpgradeFsm SwImage GetResponse indications not matching requested upgrade",
					log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
				// TODO!!!: error treatment?
				//TODO!!!: possibly send event information for aborted upgrade (aborted by ONU state indication)?
				_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
				return
			}

			//assumed only relevant state here is UpgradeStCheckCommitted
			oFsm.mutexUpgradeParams.Lock()
			oFsm.conditionalCancelRequested = false //getting here any set (conditional) cancelRequest is not relevant anymore
			if msgObj.EntityInstance == oFsm.InactiveImageMeID && imageIsActive == cmn.SwIsActive {
				//a check on the delivered image version is not done, the ONU delivered version might be different from what might have been
				//  indicated in the download image version string (version must be part of the image content itself)
				//  so checking that might be quite unreliable
				//but with new API this was changed, assumption is that omci image version is known at download request and exactly that is used
				//  in all the API references, so it can and should be checked here now
				if oFsm.useAPIVersion43 {
					if imageVersion != oFsm.imageVersion {
						//new active version indicated on OMCI from ONU is not the expected version
						logger.Errorw(ctx, "OnuUpgradeFsm image-version not matching the requested upgrade",
							log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance,
								"onu-version": imageVersion, "expected-version": oFsm.imageVersion})
						// TODO!!!: error treatment?
						//TODO!!!: possibly send event information for aborted upgrade (aborted by wrong version)?
						oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED         //not the expected image was committed
						oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE //something like 'UNEXPECTED_VERSION' would be better - proto def
						oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN              //expected image not known
						oFsm.mutexUpgradeParams.Unlock()
						_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
						return
					}
					logger.Debugw(ctx, "OnuUpgradeFsm - expected ONU image version indicated by the ONU",
						log.Fields{"device-id": oFsm.deviceID})
				}
				if imageIsCommitted == cmn.SwIsCommitted {
					oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_SUCCEEDED
					oFsm.volthaImageState = voltha.ImageState_IMAGE_COMMITTED
					//store the new commit flag to onuSwImageIndications (to keep them in sync)
					oFsm.pDevEntry.ModifySwImageActiveCommit(ctx, imageIsCommitted)
					logger.Infow(ctx, "requested SW image committed, releasing OnuUpgrade", log.Fields{"device-id": oFsm.deviceID})
					//deviceProcStatusUpdate not used anymore,
					// replaced by transferring the last (more) upgrade state information within RemoveOnuUpgradeFsm
					oFsm.mutexUpgradeParams.Unlock()
					//releasing the upgrade FSM on success
					_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
					return
				}
				//if not committed, abort upgrade as failed. There is no implementation here that would trigger this test again
			}
			oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
			oFsm.volthaDownloadReason = voltha.ImageState_CANCELLED_ON_ONU_STATE
			oFsm.volthaImageState = voltha.ImageState_IMAGE_COMMIT_ABORTED
			oFsm.mutexUpgradeParams.Unlock()
			logger.Errorw(ctx, "OnuUpgradeFsm SwImage GetResponse indications not matching requested upgrade",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			// TODO!!!: error treatment?
			//TODO!!!: possibly send event information for aborted upgrade (aborted by omci processing)??
			_ = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
			return
		} //GetResponseType
	default:
		{
			logger.Errorw(ctx, "Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

//abortOnOmciError aborts the upgrade processing with OMCI_TRANSFER_ERROR indication
func (oFsm *OnuUpgradeFsm) abortOnOmciError(ctx context.Context, aAsync bool,
	aImageState voltha.ImageState_ImageActivationState) {
	oFsm.mutexUpgradeParams.Lock()
	oFsm.conditionalCancelRequested = false //any conditional cancelRequest is superseded by this abortion
	oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
	oFsm.volthaDownloadReason = voltha.ImageState_OMCI_TRANSFER_ERROR
	if aImageState != voltha.ImageState_IMAGE_UNKNOWN {
		// update image state only in case some explicite state is given (otherwise the existing state is used)
		oFsm.volthaImageState = aImageState
	}
	oFsm.mutexUpgradeParams.Unlock()
	//TODO!!!: possibly send event information for aborted upgrade (aborted by omci processing)??
	if oFsm.PAdaptFsm != nil {
		var err error
		if aAsync { //asynchronous call requested to ensure state transition
			go func(a_pAFsm *cmn.AdapterFsm) {
				if a_pAFsm.PFsm != nil {
					err = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
				}
			}(oFsm.PAdaptFsm)
		} else {
			if oFsm.PAdaptFsm.PFsm != nil {
				err = oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
			}
		}
		if err != nil {
			logger.Warnw(ctx, "onu upgrade fsm could not abort on omci error", log.Fields{
				"device-id": oFsm.deviceID, "error": err})
		}
	}
}

//waitOnDownloadToAdapterReady state can only be reached with useAPIVersion43 (usage of pFileManager)
//  precondition: mutexIsAwaitingAdapterDlResponse is lockek on call
func (oFsm *OnuUpgradeFsm) waitOnDownloadToAdapterReady(ctx context.Context, aSyncChannel chan<- struct{},
	aWaitChannel chan bool) {
	oFsm.mutexIsAwaitingAdapterDlResponse.Lock()
	downloadToAdapterTimeout := oFsm.pFileManager.GetDownloadTimeout(ctx)
	oFsm.isWaitingForAdapterDlResponse = true
	oFsm.mutexIsAwaitingAdapterDlResponse.Unlock()
	aSyncChannel <- struct{}{}
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("OnuUpgradeFsm-waitOnDownloadToAdapterReady canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(downloadToAdapterTimeout): //10s should be enough for downloading some image to the adapter
		logger.Warnw(ctx, "OnuUpgradeFsm Waiting-adapter-download timeout", log.Fields{
			"for device-id": oFsm.deviceID, "image-id": oFsm.imageIdentifier, "timeout": downloadToAdapterTimeout})
		oFsm.pFileManager.RemoveReadyRequest(ctx, oFsm.imageIdentifier, aWaitChannel)
		//running into timeout here may still have the download to adapter active -> abort
		oFsm.pFileManager.CancelDownload(ctx, oFsm.imageIdentifier)
		oFsm.mutexIsAwaitingAdapterDlResponse.Lock()
		oFsm.isWaitingForAdapterDlResponse = false
		oFsm.mutexIsAwaitingAdapterDlResponse.Unlock()
		oFsm.mutexUpgradeParams.Lock()
		oFsm.conditionalCancelRequested = false //any conditional cancelRequest is superseded by this abortion
		oFsm.volthaDownloadState = voltha.ImageState_DOWNLOAD_FAILED
		oFsm.volthaDownloadReason = voltha.ImageState_UNKNOWN_ERROR //something like 'DOWNLOAD_TO_ADAPTER_TIMEOUT' would be better (proto)
		oFsm.volthaImageState = voltha.ImageState_IMAGE_UNKNOWN     //something like 'IMAGE_DOWNLOAD_ABORTED' would be better (proto)
		oFsm.mutexUpgradeParams.Unlock()
		//TODO!!!: possibly send event information for aborted upgrade (aborted by omci processing)??
		if oFsm.PAdaptFsm != nil && oFsm.PAdaptFsm.PFsm != nil {
			err := oFsm.PAdaptFsm.PFsm.Event(UpgradeEvAbort)
			if err != nil {
				logger.Warnw(ctx, "onu upgrade fsm could not abort on omci error", log.Fields{
					"device-id": oFsm.deviceID, "error": err})
			}
		}
		return

	case success := <-aWaitChannel:
		if success {
			logger.Debugw(ctx, "OnuUpgradeFsm image-downloaded received", log.Fields{"device-id": oFsm.deviceID})
			oFsm.mutexIsAwaitingAdapterDlResponse.Lock()
			oFsm.isWaitingForAdapterDlResponse = false
			oFsm.mutexIsAwaitingAdapterDlResponse.Unlock()
			//let the upgrade process proceed
			pUpgradeFsm := oFsm.PAdaptFsm
			if pUpgradeFsm != nil {
				_ = pUpgradeFsm.PFsm.Event(UpgradeEvPrepareSwDownload)
			} else {
				logger.Errorw(ctx, "pUpgradeFsm is nil", log.Fields{"device-id": oFsm.deviceID})
			}
			return
		}
		// waiting was aborted (assumed here to be caused by
		//   error detection or cancel at download after upgrade FSM reset/abort with according image states set there)
		logger.Debugw(ctx, "OnuUpgradeFsm Waiting-adapter-download aborted", log.Fields{"device-id": oFsm.deviceID})
		oFsm.pFileManager.RemoveReadyRequest(ctx, oFsm.imageIdentifier, aWaitChannel)
		oFsm.mutexIsAwaitingAdapterDlResponse.Lock()
		oFsm.isWaitingForAdapterDlResponse = false
		oFsm.mutexIsAwaitingAdapterDlResponse.Unlock()
		return
	}
}

//waitOnDownloadToOnuReady state can only be reached with useAPIVersion43 (usage of pFileManager)
func (oFsm *OnuUpgradeFsm) waitOnDownloadToOnuReady(ctx context.Context, aWaitChannel chan bool) {
	downloadToOnuTimeout := time.Duration(1+(oFsm.imageLength/0x400000)) * oFsm.downloadToOnuTimeout4MB
	logger.Debugw(ctx, "OnuUpgradeFsm start download-to-ONU timer", log.Fields{"device-id": oFsm.deviceID,
		"duration": downloadToOnuTimeout})
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow("OnuUpgradeFsm-waitOnDownloadToOnuReady canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(downloadToOnuTimeout): //using an image-size depending timout (in minutes)
		logger.Warnw(ctx, "OnuUpgradeFsm Waiting-ONU-download timeout", log.Fields{
			"for device-id": oFsm.deviceID, "image-id": oFsm.imageIdentifier, "timeout": downloadToOnuTimeout})
		//the upgrade process has to be aborted
		oFsm.abortOnOmciError(ctx, false, voltha.ImageState_IMAGE_UNKNOWN) //no ImageState update
		return

	case success := <-aWaitChannel:
		if success {
			logger.Debugw(ctx, "OnuUpgradeFsm image-downloaded on ONU received", log.Fields{"device-id": oFsm.deviceID})
			//all fine, let the FSM proceed like defined from the sender of this event
			return
		}
		// waiting was aborted (assumed here to be caused by
		//   error detection or cancel at download after upgrade FSM reset/abort with according image states set there)
		logger.Debugw(ctx, "OnuUpgradeFsm Waiting-ONU-download aborted", log.Fields{"device-id": oFsm.deviceID})
		return
	}
}
