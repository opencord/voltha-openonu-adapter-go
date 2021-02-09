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
	"fmt"
	"strconv"
	"time"

	"github.com/boguslaw-wojcik/crc32a"
	"github.com/looplab/fsm"
	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	"github.com/opencord/voltha-protos/v4/go/voltha"
)

const cMaxUint32 = ^uint32(0)

const (
	// internal predefined values - some off them should later be configurable (perhaps with theses as defaults)
	cOmciDownloadSectionSize     = 31 //in bytes
	cOmciDownloadWindowSizeLimit = 31 //in sections for window offset (windowSize(32)-1)
	//cOmciDownloadWindowRetryMax  = 2    // max attempts for a specific window
	cOmciSectionInterleaveMilliseconds = 100 //DownloadSection interleave time in milliseconds
	cOmciEndSwDlDelaySeconds           = 1   //End Software Download delay after last section (may be also configurable?)
	//cOmciDownloadCompleteTimeout = 5400 //in s for the complete timeout (may be better scale to image size/ noOfWindows)
)

const (
	// events of config PON ANI port FSM
	upgradeEvStart              = "upgradeEvStart"
	upgradeEvPrepareSwDownload  = "upgradeEvPrepareSwDownload"
	upgradeEvRxStartSwDownload  = "upgradeEvRxStartSwDownload"
	upgradeEvWaitWindowAck      = "upgradeEvWaitWindowAck"
	upgradeEvContinueNextWindow = "upgradeEvContinueNextWindow"
	upgradeEvEndSwDownload      = "upgradeEvEndSwDownload"
	upgradeEvRequestActivate    = "upgradeEvRequestActivate"
	upgradeEvWaitForCommit      = "upgradeEvWaitForCommit"
	upgradeEvCommitSw           = "upgradeEvCommitSw"

	//upgradeEvTimeoutSimple  = "upgradeEvTimeoutSimple"
	//upgradeEvTimeoutMids    = "upgradeEvTimeoutMids"
	upgradeEvReset   = "upgradeEvReset"
	upgradeEvAbort   = "upgradeEvAbort"
	upgradeEvRestart = "upgradeEvRestart"
)

const (
	// states of config PON ANI port FSM
	upgradeStDisabled           = "upgradeStDisabled"
	upgradeStStarting           = "upgradeStStarting"
	upgradeStPreparingDL        = "upgradeStPreparingDL"
	upgradeStDLSection          = "upgradeStDLSection"
	upgradeStVerifyWindow       = "upgradeStVerifyWindow"
	upgradeStFinalizeDL         = "upgradeStFinalizeDL"
	upgradeStRequestingActivate = "upgradeStRequestingActivate"
	upgradeStWaitForCommit      = "upgradeStWaitForCommit"
	upgradeStCommitSw           = "upgradeStCommitSw"
	upgradeStResetting          = "upgradeStResetting"
)

//required definition for IdleState detection for activities on OMCI
const cOnuUpgradeFsmIdleState = upgradeStWaitForCommit

//OnuUpgradeFsm defines the structure for the state machine to config the PON ANI ports of ONU UNI ports via OMCI
type OnuUpgradeFsm struct {
	pDeviceHandler   *deviceHandler
	pDownloadManager *adapterDownloadManager
	deviceID         string
	pOmciCC          *omciCC
	pOnuDB           *onuDeviceDB
	requestEvent     OnuDeviceEvent
	//omciMIdsResponseReceived chan bool //seperate channel needed for checking multiInstance OMCI message responses
	pAdaptFsm                         *AdapterFsm
	pImageDsc                         *voltha.ImageDownload
	imageBuffer                       []byte
	origImageLength                   uint32        //as also limited by OMCI
	imageLength                       uint32        //including last bytes padding
	omciDownloadWindowSizeLimit       uint8         //windowSize-1
	noOfSections                      uint32        //uint32 range for sections should be sufficient for very long images
	nextDownloadSectionsAbsolute      uint32        //number of next section to download in overall image
	nextDownloadSectionsWindow        uint8         //number of next section to download within current window
	noOfWindows                       uint32        //uint32 range for windows should be sufficient for very long images
	nextDownloadWindow                uint32        //number of next window to download
	inactiveImageMeID                 uint16        //ME-ID of the inactive image
	omciSectionInterleaveMilliseconds time.Duration //DownloadSectionInterleave delay in milliseconds
	delayEndSwDl                      bool          //flag to provide a delay between last section and EndSwDl
}

//NewOnuUpgradeFsm is the 'constructor' for the state machine to config the PON ANI ports
//  of ONU UNI ports via OMCI
func NewOnuUpgradeFsm(ctx context.Context, apDeviceHandler *deviceHandler,
	apDevOmciCC *omciCC, apOnuDB *onuDeviceDB,
	aRequestEvent OnuDeviceEvent, aName string, aCommChannel chan Message) *OnuUpgradeFsm {
	instFsm := &OnuUpgradeFsm{
		pDeviceHandler:                    apDeviceHandler,
		deviceID:                          apDeviceHandler.deviceID,
		pOmciCC:                           apDevOmciCC,
		pOnuDB:                            apOnuDB,
		requestEvent:                      aRequestEvent,
		omciDownloadWindowSizeLimit:       cOmciDownloadWindowSizeLimit,
		omciSectionInterleaveMilliseconds: cOmciSectionInterleaveMilliseconds,
	}

	instFsm.pAdaptFsm = NewAdapterFsm(aName, instFsm.deviceID, aCommChannel)
	if instFsm.pAdaptFsm == nil {
		logger.Errorw(ctx, "OnuUpgradeFsm's AdapterFsm could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}
	instFsm.pAdaptFsm.pFsm = fsm.NewFSM(
		upgradeStDisabled,
		fsm.Events{
			{Name: upgradeEvStart, Src: []string{upgradeStDisabled}, Dst: upgradeStStarting},
			{Name: upgradeEvPrepareSwDownload, Src: []string{upgradeStStarting}, Dst: upgradeStPreparingDL},
			{Name: upgradeEvRxStartSwDownload, Src: []string{upgradeStPreparingDL}, Dst: upgradeStDLSection},
			{Name: upgradeEvWaitWindowAck, Src: []string{upgradeStDLSection}, Dst: upgradeStVerifyWindow},
			{Name: upgradeEvContinueNextWindow, Src: []string{upgradeStVerifyWindow}, Dst: upgradeStDLSection},
			{Name: upgradeEvEndSwDownload, Src: []string{upgradeStVerifyWindow}, Dst: upgradeStFinalizeDL},
			{Name: upgradeEvRequestActivate, Src: []string{upgradeStFinalizeDL}, Dst: upgradeStRequestingActivate},
			{Name: upgradeEvWaitForCommit, Src: []string{upgradeStRequestingActivate}, Dst: upgradeStWaitForCommit},
			{Name: upgradeEvCommitSw, Src: []string{upgradeStStarting, upgradeStWaitForCommit},
				Dst: upgradeStCommitSw},

			/*
				{Name: upgradeEvTimeoutSimple, Src: []string{
					upgradeStCreatingDot1PMapper, upgradeStCreatingMBPCD, upgradeStSettingTconts, upgradeStSettingDot1PMapper}, Dst: upgradeStStarting},
				{Name: upgradeEvTimeoutMids, Src: []string{
					upgradeStCreatingGemNCTPs, upgradeStCreatingGemIWs, upgradeStSettingPQs}, Dst: upgradeStStarting},
			*/
			// exceptional treatments
			{Name: upgradeEvReset, Src: []string{upgradeStStarting, upgradeStPreparingDL, upgradeStDLSection,
				upgradeStVerifyWindow, upgradeStDLSection, upgradeStFinalizeDL, upgradeStRequestingActivate,
				upgradeStCommitSw}, //upgradeStWaitForCommit is not reset (later perhaps also not upgradeStWaitActivate)
				Dst: upgradeStResetting},
			{Name: upgradeEvAbort, Src: []string{upgradeStStarting, upgradeStPreparingDL, upgradeStDLSection,
				upgradeStVerifyWindow, upgradeStDLSection, upgradeStFinalizeDL, upgradeStRequestingActivate,
				upgradeStWaitForCommit, upgradeStCommitSw},
				Dst: upgradeStResetting},
			{Name: upgradeEvRestart, Src: []string{upgradeStResetting}, Dst: upgradeStDisabled},
		},
		fsm.Callbacks{
			"enter_state":                          func(e *fsm.Event) { instFsm.pAdaptFsm.logFsmStateChange(ctx, e) },
			"enter_" + upgradeStStarting:           func(e *fsm.Event) { instFsm.enterStarting(ctx, e) },
			"enter_" + upgradeStPreparingDL:        func(e *fsm.Event) { instFsm.enterPreparingDL(ctx, e) },
			"enter_" + upgradeStDLSection:          func(e *fsm.Event) { instFsm.enterDownloadSection(ctx, e) },
			"enter_" + upgradeStVerifyWindow:       func(e *fsm.Event) { instFsm.enterVerifyWindow(ctx, e) },
			"enter_" + upgradeStFinalizeDL:         func(e *fsm.Event) { instFsm.enterFinalizeDL(ctx, e) },
			"enter_" + upgradeStRequestingActivate: func(e *fsm.Event) { instFsm.enterActivateSw(ctx, e) },
			"enter_" + upgradeStCommitSw:           func(e *fsm.Event) { instFsm.enterCommitSw(ctx, e) },
			"enter_" + upgradeStResetting:          func(e *fsm.Event) { instFsm.enterResetting(ctx, e) },
			"enter_" + upgradeStDisabled:           func(e *fsm.Event) { instFsm.enterDisabled(ctx, e) },
		},
	)
	if instFsm.pAdaptFsm.pFsm == nil {
		logger.Errorw(ctx, "OnuUpgradeFsm's Base FSM could not be instantiated!!", log.Fields{
			"device-id": instFsm.deviceID})
		return nil
	}

	logger.Debugw(ctx, "OnuUpgradeFsm created", log.Fields{"device-id": instFsm.deviceID})
	return instFsm
}

//SetDownloadParams configures the needed parameters for a specific download to the ONU
func (oFsm *OnuUpgradeFsm) SetDownloadParams(ctx context.Context, apImageDsc *voltha.ImageDownload,
	apDownloadManager *adapterDownloadManager) error {
	pBaseFsm := oFsm.pAdaptFsm.pFsm
	if pBaseFsm != nil && pBaseFsm.Is(upgradeStStarting) {
		logger.Debugw(ctx, "OnuUpgradeFsm Parameter setting", log.Fields{
			"device-id": oFsm.deviceID, "image-description": apImageDsc})
		oFsm.pImageDsc = apImageDsc
		oFsm.pDownloadManager = apDownloadManager

		go func(aPBaseFsm *fsm.FSM) {
			// let the upgrade FSm proceed to PreparinDL
			_ = aPBaseFsm.Event(upgradeEvPrepareSwDownload)
		}(pBaseFsm)
		return nil
	}
	logger.Errorw(ctx, "OnuUpgradeFsm abort: invalid FSM base pointer or state", log.Fields{
		"device-id": oFsm.deviceID})
	return fmt.Errorf(fmt.Sprintf("OnuUpgradeFsm abort: invalid FSM base pointer or state for device-id: %s", oFsm.deviceID))
}

func (oFsm *OnuUpgradeFsm) enterStarting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm start", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})

	// start go routine for processing of LockState messages
	go oFsm.processOmciUpgradeMessages(ctx)
}

func (oFsm *OnuUpgradeFsm) enterPreparingDL(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm prepare Download to Onu", log.Fields{"in state": e.FSM.Current(),
		"device-id": oFsm.deviceID})

	fileLen, err := oFsm.pDownloadManager.getImageBufferLen(ctx, oFsm.pImageDsc.Name, oFsm.pImageDsc.LocalDir)
	if err != nil || fileLen > int64(cMaxUint32) {
		logger.Errorw(ctx, "OnuUpgradeFsm abort: problems getting image buffer length", log.Fields{
			"device-id": oFsm.deviceID, "error": err, "length": fileLen})
		pBaseFsm := oFsm.pAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvReset)
		}(pBaseFsm)
		return
	}

	oFsm.imageBuffer = make([]byte, fileLen)
	oFsm.imageBuffer, err = oFsm.pDownloadManager.getDownloadImageBuffer(ctx, oFsm.pImageDsc.Name, oFsm.pImageDsc.LocalDir)
	if err != nil {
		logger.Errorw(ctx, "OnuUpgradeFsm abort: can't get image buffer", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		pBaseFsm := oFsm.pAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvReset)
		}(pBaseFsm)
		return
	}

	oFsm.noOfSections = uint32(fileLen / cOmciDownloadSectionSize)
	if fileLen%cOmciDownloadSectionSize > 0 {
		bufferPadding := make([]byte, cOmciDownloadSectionSize-uint32(fileLen%cOmciDownloadSectionSize))
		//expand the imageBuffer to exactly fit multiples of cOmciDownloadSectionSize with padding
		oFsm.imageBuffer = append(oFsm.imageBuffer[:fileLen], bufferPadding...)
		oFsm.noOfSections++
	}
	oFsm.origImageLength = uint32(fileLen)
	oFsm.imageLength = uint32(len(oFsm.imageBuffer))
	oFsm.inactiveImageMeID = uint16(1) //just to start with, must be detected from upload data or even determined per new request?

	logger.Infow(ctx, "OnuUpgradeFsm starts with StartSwDl values", log.Fields{
		"MeId": oFsm.inactiveImageMeID, "windowSizeLimit": oFsm.omciDownloadWindowSizeLimit,
		"ImageSize": oFsm.imageLength, "original file size": fileLen})
	//"NumberOfCircuitPacks": oFsm.numberCircuitPacks, "CircuitPacks MeId": 0}) //parallel circuit packs download not supported
	err = oFsm.pOmciCC.sendStartSoftwareDownload(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, false,
		oFsm.pAdaptFsm.commChan, oFsm.inactiveImageMeID, oFsm.omciDownloadWindowSizeLimit, oFsm.origImageLength)
	if err != nil {
		logger.Errorw(ctx, "StartSwDl abort: can't send section", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		//TODO!!!: define some more sophisticated error treatment with some repetition, for now just reset the FSM
		pBaseFsm := oFsm.pAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvReset)
		}(pBaseFsm)
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
	framePrint := false //default no printing of downloadSection frames
	if oFsm.nextDownloadSectionsAbsolute == 0 {
		//debug print of first section frame
		framePrint = true
	}

	for {
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
			//logical error -- reset the FSM
			pBaseFsm := oFsm.pAdaptFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *AdapterFsm) {
				_ = a_pAFsm.pFsm.Event(vlanEvReset)
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
			framePrint = true //debug print of last frame
			logger.Debugw(ctx, "DlSection expect Response for last window (section)", log.Fields{
				"device-id": oFsm.deviceID, "DlSectionNoAbsolute": oFsm.nextDownloadSectionsAbsolute})
		}
		err := oFsm.pOmciCC.sendDownloadSection(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, false,
			oFsm.pAdaptFsm.commChan, oFsm.inactiveImageMeID, windowAckRequest, oFsm.nextDownloadSectionsWindow, downloadSection, framePrint)
		if err != nil {
			logger.Errorw(ctx, "DlSection abort: can't send section", log.Fields{
				"device-id": oFsm.deviceID, "error": err})
			//TODO!!!: define some more sophisticated error treatment with some repetition, for now just reset the FSM
			pBaseFsm := oFsm.pAdaptFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *AdapterFsm) {
				_ = a_pAFsm.pFsm.Event(vlanEvReset)
			}(pBaseFsm)
			return
		}

		oFsm.nextDownloadSectionsAbsolute++ //always increase the absolute section counter after having sent one
		if windowAckRequest == 1 {
			pBaseFsm := oFsm.pAdaptFsm
			// Can't call FSM Event directly, decoupling it
			go func(a_pAFsm *AdapterFsm) {
				_ = a_pAFsm.pFsm.Event(upgradeEvWaitWindowAck) //state transition to upgradeStVerifyWindow
			}(pBaseFsm)
			return
		}
		framePrint = false                //for the next Section frame (if wanted, can be enabled in logic before sendXXX())
		oFsm.nextDownloadSectionsWindow++ //increase the window related section counter only if not in the last section
		if oFsm.omciSectionInterleaveMilliseconds > 0 {
			//ensure a defined intersection-time-gap to leave space for further processing, other ONU's ...
			time.Sleep(oFsm.omciSectionInterleaveMilliseconds * time.Millisecond)
		}
	}
}

func (oFsm *OnuUpgradeFsm) enterVerifyWindow(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm verify DL window ack", log.Fields{
		"for window": oFsm.nextDownloadWindow, "device-id": oFsm.deviceID})
}

func (oFsm *OnuUpgradeFsm) enterFinalizeDL(ctx context.Context, e *fsm.Event) {
	imageCRC := crc32a.Checksum(oFsm.imageBuffer[:int(oFsm.origImageLength)]) //ITU I.363.5 crc
	logger.Infow(ctx, "OnuUpgradeFsm finalize DL", log.Fields{
		"device-id": oFsm.deviceID, "crc": strconv.FormatInt(int64(imageCRC), 16), "delay": oFsm.delayEndSwDl})

	if oFsm.delayEndSwDl {
		//give the ONU some time for image evaluation (hoping it does not base that on first EndSwDl itself)
		// should not be set in case this state is used for real download abort (not yet implemented)
		time.Sleep(cOmciEndSwDlDelaySeconds * time.Second)
	}

	err := oFsm.pOmciCC.sendEndSoftwareDownload(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, false,
		oFsm.pAdaptFsm.commChan, oFsm.inactiveImageMeID, oFsm.origImageLength, imageCRC)
	if err != nil {
		logger.Errorw(ctx, "EndSwDl abort: can't send section", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		//TODO!!!: define some more sophisticated error treatment with some repetition, for now just reset the FSM
		pBaseFsm := oFsm.pAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvReset)
		}(pBaseFsm)
		return
	}
}

func (oFsm *OnuUpgradeFsm) enterActivateSw(ctx context.Context, e *fsm.Event) {
	logger.Infow(ctx, "OnuUpgradeFsm activate SW", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.inactiveImageMeID})

	err := oFsm.pOmciCC.sendActivateSoftware(log.WithSpanFromContext(context.TODO(), ctx), ConstDefaultOmciTimeout, false,
		oFsm.pAdaptFsm.commChan, oFsm.inactiveImageMeID)
	if err != nil {
		logger.Errorw(ctx, "ActivateSw abort: can't send activate frame", log.Fields{
			"device-id": oFsm.deviceID, "error": err})
		//TODO!!!: define some more sophisticated error treatment with some repetition, for now just reset the FSM
		pBaseFsm := oFsm.pAdaptFsm
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			_ = a_pAFsm.pFsm.Event(vlanEvReset)
		}(pBaseFsm)
		return
	}
}

func (oFsm *OnuUpgradeFsm) enterCommitSw(ctx context.Context, e *fsm.Event) {
	logger.Infow(ctx, "OnuUpgradeFsm commit SW - not yet implemented", log.Fields{
		"device-id": oFsm.deviceID, "me-id": oFsm.inactiveImageMeID})
	//here should be the sending of the software commit message and staying here while waiting for the response
}

func (oFsm *OnuUpgradeFsm) enterResetting(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm resetting", log.Fields{"device-id": oFsm.deviceID})

	pConfigupgradeStateAFsm := oFsm.pAdaptFsm
	if pConfigupgradeStateAFsm != nil {
		// abort running message processing
		fsmAbortMsg := Message{
			Type: TestMsg,
			Data: TestMessage{
				TestMessageVal: AbortMessageProcessing,
			},
		}
		pConfigupgradeStateAFsm.commChan <- fsmAbortMsg

		//try to restart the FSM to 'disabled'
		// Can't call FSM Event directly, decoupling it
		go func(a_pAFsm *AdapterFsm) {
			if a_pAFsm != nil && a_pAFsm.pFsm != nil {
				_ = a_pAFsm.pFsm.Event(upgradeEvRestart)
			}
		}(pConfigupgradeStateAFsm)
	}
}

func (oFsm *OnuUpgradeFsm) enterDisabled(ctx context.Context, e *fsm.Event) {
	logger.Debugw(ctx, "OnuUpgradeFsm enters disabled state", log.Fields{"device-id": oFsm.deviceID})
	if oFsm.pDeviceHandler != nil {
		//request removal of 'reference' in the Handler (completely clear the FSM and its data)
		go oFsm.pDeviceHandler.removeOnuUpgradeFsm(ctx)
	}
}

func (oFsm *OnuUpgradeFsm) processOmciUpgradeMessages(ctx context.Context) { //ctx context.Context?
	logger.Debugw(ctx, "Start OnuUpgradeFsm Msg processing", log.Fields{"for device-id": oFsm.deviceID})
loop:
	for {
		// case <-ctx.Done():
		// 	logger.Info(ctx,"MibSync Msg", log.Fields{"Message handling canceled via context for device-id": oFsm.deviceID})
		// 	break loop
		message, ok := <-oFsm.pAdaptFsm.commChan
		if !ok {
			logger.Info(ctx, "OnuUpgradeFsm Rx Msg - could not read from channel", log.Fields{"device-id": oFsm.deviceID})
			// but then we have to ensure a restart of the FSM as well - as exceptional procedure
			_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvReset)
			break loop
		}
		logger.Debugw(ctx, "OnuUpgradeFsm Rx Msg", log.Fields{"device-id": oFsm.deviceID})

		switch message.Type {
		case TestMsg:
			msg, _ := message.Data.(TestMessage)
			if msg.TestMessageVal == AbortMessageProcessing {
				logger.Infow(ctx, "OnuUpgradeFsm abort ProcessMsg", log.Fields{"for device-id": oFsm.deviceID})
				break loop
			}
			logger.Warnw(ctx, "OnuUpgradeFsm unknown TestMessage", log.Fields{"device-id": oFsm.deviceID, "MessageVal": msg.TestMessageVal})
		case OMCI:
			msg, _ := message.Data.(OmciMessage)
			oFsm.handleOmciOnuUpgradeMessage(ctx, msg)
		default:
			logger.Warn(ctx, "OnuUpgradeFsm Rx unknown message", log.Fields{"device-id": oFsm.deviceID,
				"message.Type": message.Type})
		}
	}
	logger.Infow(ctx, "End OnuUpgradeFsm Msg processing", log.Fields{"device-id": oFsm.deviceID})
}

//nolint: gocyclo
func (oFsm *OnuUpgradeFsm) handleOmciOnuUpgradeMessage(ctx context.Context, msg OmciMessage) {
	logger.Debugw(ctx, "Rx OMCI OnuUpgradeFsm Msg", log.Fields{"device-id": oFsm.deviceID,
		"msgType": msg.OmciMsg.MessageType})

	switch msg.OmciMsg.MessageType {
	case omci.StartSoftwareDownloadResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeStartSoftwareDownloadResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for StartSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.StartSoftwareDownloadResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for StartSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm StartSwDlResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityInstance == oFsm.inactiveImageMeID {
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

				_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvRxStartSwDownload)
				return
			}
			logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			// TODO!!!: possibly repeat the start request (once)?
			return
		} //StartSoftwareDownloadResponseType
	case omci.DownloadSectionResponseType:
		{
			/* TODO!!!: Have to remove the check here as current used omci-lib does not allow msg layering here
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeDownloadSectionResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for DlSectionResponse",
					log.Fields{"device-id": oFsm.deviceID, "omci-message": msg.OmciMsg})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.DownloadSectionResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for DlSectionResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm DlSectionResponse Data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm DlSectionResponse result error - later: repeat window once?", //TODO!!!
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				return
			}
			if msgObj.EntityInstance == oFsm.inactiveImageMeID {
				sectionNumber := msgObj.SectionNumber
			*/
			sectionNumber := oFsm.omciDownloadWindowSizeLimit //as long as access from omci-lib is not given above!!!
			logger.Debugw(ctx, "DlSectionResponse received", log.Fields{
				"window section-number": sectionNumber, "device-id": oFsm.deviceID})
			if sectionNumber != oFsm.omciDownloadWindowSizeLimit {
				logger.Errorw(ctx, "OnuUpgradeFsm DlSectionResponse section error - later: repeat window once?", //TODO!!!
					log.Fields{"device-id": oFsm.deviceID, "window-section-limit": oFsm.omciDownloadWindowSizeLimit})
				return
			}

			oFsm.nextDownloadWindow++
			if oFsm.nextDownloadWindow >= oFsm.noOfWindows {
				oFsm.delayEndSwDl = true //ensure a delay for the EndSwDl message
				_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvEndSwDownload)
				return
			}
			oFsm.nextDownloadSectionsWindow = 0
			_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvContinueNextWindow)
			return
			/* }
			logger.Errorw(ctx, "OnuUpgradeFsm Omci StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			// TODO!!!: possibly repeat the start request (once)?
			return
			*/
		} //DownloadSectionResponseType
	case omci.EndSoftwareDownloadResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeEndSoftwareDownloadResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for EndSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.EndSoftwareDownloadResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for EndSwDlResponse",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm EndSwDlResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				//TODO!!: Busy must be handled to give the ONU time for internal image storage, perhaps also processing error (CRC incorrect)
				logger.Errorw(ctx, "OnuUpgradeFsm EndSwDlResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				// possibly force FSM into abort or ignore some errors for some messages? store error for mgmt display?
				return
			}
			if msgObj.EntityInstance == oFsm.inactiveImageMeID {
				logger.Debugw(ctx, "Expected EndSwDlResponse received", log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvRequestActivate)
				return
			}
			logger.Errorw(ctx, "OnuUpgradeFsm StartSwDlResponse wrong ME instance: try again (later)?",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			// TODO!!!: possibly repeat the end request (once)? or verify ONU upgrade state?
			return
		} //EndSoftwareDownloadResponseType
	case omci.ActivateSoftwareResponseType:
		{
			msgLayer := (*msg.OmciPacket).Layer(omci.LayerTypeActivateSoftwareResponse)
			if msgLayer == nil {
				logger.Errorw(ctx, "Omci Msg layer could not be detected for ActivateSw",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			msgObj, msgOk := msgLayer.(*omci.ActivateSoftwareResponse)
			if !msgOk {
				logger.Errorw(ctx, "Omci Msg layer could not be assigned for ActivateSw",
					log.Fields{"device-id": oFsm.deviceID})
				return
			}
			logger.Debugw(ctx, "OnuUpgradeFsm ActivateSwResponse data", log.Fields{
				"device-id": oFsm.deviceID, "data-fields": msgObj})
			if msgObj.Result != me.Success {
				logger.Errorw(ctx, "OnuUpgradeFsm ActivateSwResponse result error - later: drive FSM to abort state ?",
					log.Fields{"device-id": oFsm.deviceID, "Error": msgObj.Result})
				// TODO!!!: error treatment?, perhaps in the end reset the FSM
				return
			}
			if msgObj.EntityInstance == oFsm.inactiveImageMeID {
				logger.Debugw(ctx, "Expected ActivateSwResponse received", log.Fields{"device-id": oFsm.deviceID})
				_ = oFsm.pAdaptFsm.pFsm.Event(upgradeEvWaitForCommit)
				return
			}
			logger.Errorw(ctx, "OnuUpgradeFsm ActivateSwResponse wrong ME instance: abort",
				log.Fields{"device-id": oFsm.deviceID, "ResponseMeId": msgObj.EntityInstance})
			// TODO!!!: error treatment?, perhaps in the end reset the FSM
			return
		} //ActivateSoftwareResponseType
	default:
		{
			logger.Errorw(ctx, "Rx OMCI unhandled MsgType",
				log.Fields{"omciMsgType": msg.OmciMsg.MessageType, "device-id": oFsm.deviceID})
			return
		}
	}
}

/*
func (oFsm *OnuUpgradeFsm) waitforOmciResponse(ctx context.Context) error {
	select {
	// maybe be also some outside cancel (but no context modeled for the moment ...)
	// case <-ctx.Done():
	// 		logger.Infow(ctx,"LockState-bridge-init message reception canceled", log.Fields{"for device-id": oFsm.deviceID})
	case <-time.After(30 * time.Second): //AS FOR THE OTHER OMCI FSM's
		logger.Warnw(ctx, "OnuUpgradeFsm multi entity timeout", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("OnuUpgradeFsm multi entity timeout %s", oFsm.deviceID)
	case success := <-oFsm.omciMIdsResponseReceived:
		if success {
			logger.Debug(ctx, "OnuUpgradeFsm multi entity response received")
			return nil
		}
		// should not happen so far
		logger.Warnw(ctx, "OnuUpgradeFsm multi entity response error", log.Fields{"for device-id": oFsm.deviceID})
		return fmt.Errorf("OnuUpgradeFsm multi entity responseError %s", oFsm.deviceID)
	}
}
*/
