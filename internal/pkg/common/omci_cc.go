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

//Package common provides global definitions
package common

import (
	"container/list"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time" //by now for testing

	"github.com/google/gopacket"
	// TODO!!! Some references could be resolved auto, but some need specific context ....
	gp "github.com/google/gopacket"

	"github.com/opencord/omci-lib-go/v2"
	me "github.com/opencord/omci-lib-go/v2/generated"
	oframe "github.com/opencord/omci-lib-go/v2/meframe"

	vgrpc "github.com/opencord/voltha-lib-go/v7/pkg/grpc"

	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	"github.com/opencord/voltha-protos/v5/go/common"
	ia "github.com/opencord/voltha-protos/v5/go/inter_adapter"
)

// ### OMCI related definitions - retrieved from Python adapter code/trace ####

const maxGemPayloadSize = uint16(48)
const connectivityModeValue = uint8(5)

//const defaultTPID = uint16(0x8100)
//const broadComDefaultVID = uint16(4091)

// UnusedTcontAllocID - TODO: add comment
const UnusedTcontAllocID = uint16(0xFFFF) //common unused AllocId for G.984 and G.987 systems

const cOmciBaseMessageTrailerLen = 40

// tOmciReceiveError - enum type for detected problems/errors in the received OMCI message (format)
type tOmciReceiveError uint8

const (
	// cOmciMessageReceiveNoError - default start state
	cOmciMessageReceiveNoError tOmciReceiveError = iota
	// Error indication wrong trailer length within the message
	cOmciMessageReceiveErrorTrailerLen
	// Error indication missing trailer within the message
	cOmciMessageReceiveErrorMissTrailer
)

// CDefaultRetries - TODO: add comment
const CDefaultRetries = 2

// ### OMCI related definitions - end

//CallbackPairEntry to be used for OMCI send/receive correlation
type CallbackPairEntry struct {
	CbRespChannel chan Message
	CbFunction    func(context.Context, *omci.OMCI, *gp.Packet, chan Message) error
	FramePrint    bool //true for printing
}

//CallbackPair to be used for ReceiveCallback init
type CallbackPair struct {
	CbKey   uint16
	CbEntry CallbackPairEntry
}

// OmciTransferStructure - TODO: add comment
type OmciTransferStructure struct {
	txFrame        []byte
	timeout        int
	retries        int
	highPrio       bool
	withFramePrint bool
	cbPair         CallbackPair
	chSuccess      chan bool
}

//OmciCC structure holds information needed for OMCI communication (to/from OLT Adapter)
type OmciCC struct {
	enabled            bool
	pBaseDeviceHandler IdeviceHandler
	pOnuDeviceEntry    IonuDeviceEntry
	pOnuAlarmManager   IonuAlarmManager
	deviceID           string
	coreClient         *vgrpc.Client
	supportExtMsg      bool
	rxOmciFrameError   tOmciReceiveError

	txFrames, txOnuFrames                uint32
	rxFrames, rxOnuFrames, rxOnuDiscards uint32

	// OMCI params
	mutexTid       sync.Mutex
	tid            uint16
	mutexHpTid     sync.Mutex
	hpTid          uint16
	UploadSequNo   uint16
	UploadNoOfCmds uint16

	mutexSendQueuedRequests sync.Mutex
	mutexLowPrioTxQueue     sync.Mutex
	lowPrioTxQueue          *list.List
	mutexHighPrioTxQueue    sync.Mutex
	highPrioTxQueue         *list.List
	mutexRxSchedMap         sync.Mutex
	rxSchedulerMap          map[uint16]CallbackPairEntry
	mutexMonReq             sync.RWMutex
	monitoredRequests       map[uint16]OmciTransferStructure
}

var responsesWithMibDataSync = []omci.MessageType{
	omci.CreateResponseType,
	omci.DeleteResponseType,
	omci.SetResponseType,
	omci.StartSoftwareDownloadResponseType,
	omci.EndSoftwareDownloadResponseType,
	omci.ActivateSoftwareResponseType,
	omci.CommitSoftwareResponseType,
}

//NewOmciCC constructor returns a new instance of a OmciCC
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOmciCC(ctx context.Context, deviceID string, deviceHandler IdeviceHandler,
	onuDeviceEntry IonuDeviceEntry, onuAlarmManager IonuAlarmManager,
	coreClient *vgrpc.Client) *OmciCC {
	logger.Debugw(ctx, "init-omciCC", log.Fields{"device-id": deviceID})
	var omciCC OmciCC
	omciCC.enabled = false
	omciCC.pBaseDeviceHandler = deviceHandler
	omciCC.pOnuAlarmManager = onuAlarmManager
	omciCC.pOnuDeviceEntry = onuDeviceEntry
	omciCC.deviceID = deviceID
	omciCC.coreClient = coreClient
	omciCC.supportExtMsg = false
	omciCC.rxOmciFrameError = cOmciMessageReceiveNoError
	omciCC.txFrames = 0
	omciCC.txOnuFrames = 0
	omciCC.rxFrames = 0
	omciCC.rxOnuFrames = 0
	omciCC.rxOnuDiscards = 0
	omciCC.tid = 0x1
	omciCC.hpTid = 0x8000
	omciCC.UploadSequNo = 0
	omciCC.UploadNoOfCmds = 0
	omciCC.lowPrioTxQueue = list.New()
	omciCC.highPrioTxQueue = list.New()
	omciCC.rxSchedulerMap = make(map[uint16]CallbackPairEntry)
	omciCC.monitoredRequests = make(map[uint16]OmciTransferStructure)

	return &omciCC
}

//Stop stops/resets the omciCC
func (oo *OmciCC) Stop(ctx context.Context) error {
	logger.Debugw(ctx, "omciCC-stopping", log.Fields{"device-id": oo.deviceID})
	//reseting all internal data, which might also be helpful for discarding any lingering tx/rx requests
	oo.CancelRequestMonitoring(ctx)
	// clear the tx queues
	oo.mutexHighPrioTxQueue.Lock()
	oo.highPrioTxQueue.Init()
	oo.mutexHighPrioTxQueue.Unlock()
	oo.mutexLowPrioTxQueue.Lock()
	oo.lowPrioTxQueue.Init()
	oo.mutexLowPrioTxQueue.Unlock()
	//clear the scheduler map
	oo.mutexRxSchedMap.Lock()
	for k := range oo.rxSchedulerMap {
		delete(oo.rxSchedulerMap, k)
	}
	oo.mutexRxSchedMap.Unlock()
	//reset the high prio transactionId
	oo.mutexHpTid.Lock()
	oo.hpTid = 0x8000
	oo.mutexHpTid.Unlock()
	//reset the low prio transactionId
	oo.mutexTid.Lock()
	oo.tid = 1
	oo.mutexTid.Unlock()
	//reset control values
	oo.UploadSequNo = 0
	oo.UploadNoOfCmds = 0
	oo.rxOmciFrameError = cOmciMessageReceiveNoError
	//reset the stats counter - which might be topic of discussion ...
	oo.txFrames = 0
	oo.txOnuFrames = 0
	oo.rxFrames = 0
	oo.rxOnuFrames = 0
	oo.rxOnuDiscards = 0

	return nil
}

// Rx handler for omci messages
func (oo *OmciCC) receiveOnuMessage(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet) error {
	logger.Debugw(ctx, "rx-onu-autonomous-message", log.Fields{"omciMsgType": omciMsg.MessageType,
		"payload": hex.EncodeToString(omciMsg.Payload)})
	switch omciMsg.MessageType {
	case omci.AlarmNotificationType:
		data := OmciMessage{
			OmciMsg:    omciMsg,
			OmciPacket: packet,
		}
		go oo.pOnuAlarmManager.HandleOmciAlarmNotificationMessage(ctx, data)
		return nil
	default:
		return fmt.Errorf("receiveOnuMessageType %s unimplemented", omciMsg.MessageType.String())
	}
	/*
			msgType = rxFrame.fields["message_type"] //assumed OmciOperationsValue
			rxOnuFrames++

			switch msgType {
			case AlarmNotification:
				{
					logger.Info("Unhandled: received-onu-alarm-message")
					// python code was:
					//if msg_type == EntityOperations.AlarmNotification.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.Alarm_Notification)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxAlarmNotification unimplemented")
				}
			case AttributeValueChange:
				{
					logger.Info("Unhandled: received-attribute-value-change")
					// python code was:
					//elif msg_type == EntityOperations.AttributeValueChange.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.AVC_Notification)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxAttributeValueChange unimplemented")
				}
			case TestResult:
				{
					logger.Info("Unhandled: received-test-result")
					// python code was:
					//elif msg_type == EntityOperations.TestResult.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.Test_Result)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxTestResult unimplemented")
				}
			default:
				{
					logger.Errorw(ctx,"rx-onu-unsupported-autonomous-message", log.Fields{"msgType": msgType})
					rxOnuDiscards++
					return errors.New("RxOnuMsgType unimplemented")
				}
		    }
	*/
}

func (oo *OmciCC) printRxMessage(ctx context.Context, rxMsg []byte) {
	//assuming omci message content is hex coded!
	// with restricted output of 16bytes would be ...rxMsg[:16]
	logger.Debugw(ctx, "omci-message-received:", log.Fields{
		"RxOmciMessage": hex.EncodeToString(rxMsg),
		"device-id":     oo.deviceID})
}

// ReceiveMessage - Rx handler for onu messages
//    e.g. would call ReceiveOnuMessage() in case of TID=0 or Action=test ...
func (oo *OmciCC) ReceiveMessage(ctx context.Context, rxMsg []byte) error {
	//logger.Debugw(ctx,"cc-receive-omci-message", log.Fields{"RxOmciMessage-x2s": hex.EncodeToString(rxMsg)})
	if len(rxMsg) >= 44 { // then it should normally include the BaseFormat trailer Len
		// NOTE: autocorrection only valid for OmciBaseFormat, which is not specifically verified here!!!
		//  (an extendedFormat message could be destroyed this way!)
		trailerLenData := rxMsg[42:44]
		trailerLen := binary.BigEndian.Uint16(trailerLenData)
		//logger.Debugw(ctx,"omci-received-trailer-len", log.Fields{"Length": trailerLen})
		if trailerLen != cOmciBaseMessageTrailerLen { // invalid base Format entry -> autocorrect
			binary.BigEndian.PutUint16(rxMsg[42:44], cOmciBaseMessageTrailerLen)
			if oo.rxOmciFrameError != cOmciMessageReceiveErrorTrailerLen {
				//do just one error log, expectation is: if seen once it should appear regularly - avoid to many log entries
				logger.Errorw(ctx, "wrong omci-message trailer length: trailer len auto-corrected",
					log.Fields{"trailer-length": trailerLen, "device-id": oo.deviceID})
				oo.rxOmciFrameError = cOmciMessageReceiveErrorTrailerLen
			}
		}
	} else if len(rxMsg) >= cOmciBaseMessageTrailerLen { // workaround for Adtran OLT Sim, which currently does not send trailer bytes at all!
		// NOTE: autocorrection only valid for OmciBaseFormat, which is not specifically verified here!!!
		//  (an extendedFormat message could be destroyed this way!)
		// extend/overwrite with trailer
		trailer := make([]byte, 8)
		binary.BigEndian.PutUint16(trailer[2:], cOmciBaseMessageTrailerLen) //set the defined baseline length
		rxMsg = append(rxMsg[:cOmciBaseMessageTrailerLen], trailer...)
		if oo.rxOmciFrameError != cOmciMessageReceiveErrorMissTrailer {
			//do just one error log, expectation is: if seen once it should appear regularly - avoid to many log entries
			logger.Errorw(ctx, "omci-message to short to include trailer len: trailer auto-corrected (added)",
				log.Fields{"message-length": len(rxMsg), "device-id": oo.deviceID})
			oo.rxOmciFrameError = cOmciMessageReceiveErrorMissTrailer
		}
	} else {
		logger.Errorw(ctx, "received omci-message too small for OmciBaseFormat - abort",
			log.Fields{"Length": len(rxMsg), "device-id": oo.deviceID})
		oo.printRxMessage(ctx, rxMsg)
		return fmt.Errorf("rxOmciMessage too small for BaseFormat %s", oo.deviceID)
	}

	packet := gopacket.NewPacket(rxMsg, omci.LayerTypeOMCI, gopacket.NoCopy)
	if packet == nil {
		logger.Errorw(ctx, "omci-message could not be decoded", log.Fields{"device-id": oo.deviceID})
		oo.printRxMessage(ctx, rxMsg)
		return fmt.Errorf("could not decode rxMsg as OMCI %s", oo.deviceID)
	}
	omciLayer := packet.Layer(omci.LayerTypeOMCI)
	if omciLayer == nil {
		logger.Errorw(ctx, "omci-message could not decode omci layer", log.Fields{"device-id": oo.deviceID})
		oo.printRxMessage(ctx, rxMsg)
		return fmt.Errorf("could not decode omci layer %s", oo.deviceID)
	}
	// insert some check on detected OMCI decoding issues and log them
	// e.g. should indicate problems when detecting some unknown attribute mask content (independent from message type)
	//   even though allowed from omci-lib due to set relaxed decoding
	// application may dig into further details if wanted/needed on their own [but info is not transferred from here so far]
	//   (compare mib_sync.go unknownAttrLayer)
	errLayer := packet.Layer(gopacket.LayerTypeDecodeFailure)
	if failure, decodeOk := errLayer.(*gopacket.DecodeFailure); decodeOk {
		errMsg := failure.Error()
		logger.Warnw(ctx, "Detected decode issue on received OMCI frame", log.Fields{
			"device-id": oo.deviceID, "issue": errMsg})
	}
	//anyway try continue OMCI decoding further on message type layer
	omciMsg, ok := omciLayer.(*omci.OMCI)
	if !ok {
		logger.Errorw(ctx, "omci-message could not assign omci layer", log.Fields{"device-id": oo.deviceID})
		oo.printRxMessage(ctx, rxMsg)
		return fmt.Errorf("could not assign omci layer %s", oo.deviceID)
	}
	logger.Debugw(ctx, "omci-message-decoded:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "DeviceIdent": omciMsg.DeviceIdentifier})

	// TestResult is asynchronous indication that carries the same TID as the TestResponse.
	// We expect to find the TID in the oo.rxSchedulerMap
	if byte(omciMsg.MessageType)&me.AK == 0 && omciMsg.MessageType != omci.TestResultType {
		// Not a response
		oo.printRxMessage(ctx, rxMsg)
		logger.Debug(ctx, "RxMsg is no Omci Response Message")
		if omciMsg.TransactionID == 0 {
			return oo.receiveOnuMessage(ctx, omciMsg, &packet)
		}
		logger.Errorw(ctx, "Unexpected TransCorrId != 0  not accepted for autonomous messages",
			log.Fields{"msgType": omciMsg.MessageType, "payload": hex.EncodeToString(omciMsg.Payload),
				"device-id": oo.deviceID})
		return fmt.Errorf("autonomous Omci Message with TranSCorrId != 0 not acccepted %s", oo.deviceID)
	}
	//logger.Debug(ctx,"RxMsg is a Omci Response Message: try to schedule it to the requester")
	oo.mutexRxSchedMap.Lock()
	rxCallbackEntry, ok := oo.rxSchedulerMap[omciMsg.TransactionID]
	if ok && rxCallbackEntry.CbFunction != nil {
		if rxCallbackEntry.FramePrint {
			oo.printRxMessage(ctx, rxMsg)
		}
		//disadvantage of decoupling: error verification made difficult, but anyway the question is
		// how to react on erroneous frame reception, maybe can simply be ignored
		go rxCallbackEntry.CbFunction(ctx, omciMsg, &packet, rxCallbackEntry.CbRespChannel)
		if isSuccessfulResponseWithMibDataSync(omciMsg, &packet) {
			oo.pOnuDeviceEntry.IncrementMibDataSync(ctx)
		}

		// If omciMsg.MessageType is omci.TestResponseType, we still expect the TestResult OMCI message,
		// so do not clean up the TransactionID in that case.
		if omciMsg.MessageType != omci.TestResponseType {
			// having posted the response the request is regarded as 'done'
			delete(oo.rxSchedulerMap, omciMsg.TransactionID)
		}
		oo.mutexRxSchedMap.Unlock()
		return nil
	}
	oo.mutexRxSchedMap.Unlock()
	logger.Errorw(ctx, "omci-message-response for not registered transCorrId", log.Fields{"device-id": oo.deviceID})
	oo.printRxMessage(ctx, rxMsg)
	return fmt.Errorf("could not find registered response handler tor transCorrId %s", oo.deviceID)

	/* py code was:
	           Receive and OMCI message from the proxy channel to the OLT.

	           Call this from your ONU Adapter on a new OMCI Rx on the proxy channel
	           :param msg: (str) OMCI binary message (used as input to Scapy packet decoder)
	           """
	           if not self.enabled:
	               return

	           try:
	               now = arrow.utcnow()
	               d = None

	               # NOTE: Since we may need to do an independent ME map on a per-ONU basis
	               #       save the current value of the entity_id_to_class_map, then
	               #       replace it with our custom one before decode, and then finally
	               #       restore it later. Tried other ways but really made the code messy.
	               saved_me_map = omci_entities.entity_id_to_class_map
	               omci_entities.entity_id_to_class_map = self._me_map

	               try:
	                   rx_frame = msg if isinstance(msg, OmciFrame) else OmciFrame(msg)
	                   self.logger.debug('recv-omci-msg', omci_msg=hexlify(msg))
	               except KeyError as e:
	                   # Unknown, Unsupported, or vendor-specific ME. Key is the unknown classID
	                   self.logger.debug('frame-decode-key-error', omci_msg=hexlify(msg), e=e)
	                   rx_frame = self._decode_unknown_me(msg)
	                   self._rx_unknown_me += 1

	               except Exception as e:
	                   self.logger.exception('frame-decode', omci_msg=hexlify(msg), e=e)
	                   return

	               finally:
	                   omci_entities.entity_id_to_class_map = saved_me_map     # Always restore it.

	               rx_tid = rx_frame.fields['transaction_id']
	               msg_type = rx_frame.fields['message_type']
	               self.logger.debug('Received message for rx_tid', rx_tid = rx_tid, msg_type = msg_type)
	               # Filter the Test Result frame and route through receive onu
	               # message method.
	               if rx_tid == 0 or msg_type == EntityOperations.TestResult.value:
	                   self.logger.debug('Receive ONU message', rx_tid=0)
	                   return self._receive_onu_message(rx_frame)

	               # Previously unreachable if this is the very first round-trip Rx or we
	               # have been running consecutive errors
	               if self._rx_frames == 0 or self._consecutive_errors != 0:
	                   self.logger.debug('Consecutive errors for rx', err = self._consecutive_errors)
	                   self.reactor.callLater(0, self._publish_connectivity_event, True)

	               self._rx_frames += 1
	               self._consecutive_errors = 0

	               try:
	                   high_priority = self._tid_is_high_priority(rx_tid)
	                   index = self._get_priority_index(high_priority)

	                   # (timestamp, defer, frame, timeout, retry, delayedCall)
	                   last_tx_tuple = self._tx_request[index]

	                   if last_tx_tuple is None or \
	                           last_tx_tuple[OMCI_CC.REQUEST_FRAME].fields.get('transaction_id') != rx_tid:
	                       # Possible late Rx on a message that timed-out
	                       if last_tx_tuple:
	                           self.logger.debug('Unknown message', rx_tid=rx_tid,
	                                          tx_id=last_tx_tuple[OMCI_CC.REQUEST_FRAME].fields.get('transaction_id'))
	                       self._rx_unknown_tid += 1
	                       self._rx_late += 1
	                       return

	                   ts, d, tx_frame, timeout, retry, dc = last_tx_tuple
	                   if dc is not None and not dc.cancelled and not dc.called:
	                       dc.cancel()

	                   _secs = self._update_rx_tx_stats(now, ts)

	                   # Late arrival already serviced by a timeout?
	                   if d.called:
	                       self._rx_late += 1
	                       self.logger.debug('Serviced by timeout. Late arrival', rx_late = self._rx_late)
	                       return

	               except Exception as e:
	                   self.logger.exception('frame-match', msg=hexlify(msg), e=e)
	                   if d is not None:
	                       return d.errback(failure.Failure(e))
	                   return

	               # Publish Rx event to listeners in a different task
	               self.logger.debug('Publish rx event', rx_tid = rx_tid,
	                              tx_tid = tx_frame.fields['transaction_id'])
	               reactor.callLater(0, self._publish_rx_frame, tx_frame, rx_frame)

	               # begin success callback chain (will cancel timeout and queue next Tx message)
	               self._rx_response[index] = rx_frame
	               d.callback(rx_frame)

	           except Exception as e:
	   			self.logger.exception('rx-msg', e=e)
	*/
}

/*
func (oo *omciCC) publishRxResponseFrame(ctx context.Context, txFrame []byte, rxFrame []byte) error {
	return errors.New("publishRxResponseFrame unimplemented")
		//def _publish_rx_frame(self, tx_frame, rx_frame):
}
*/

// ReleaseTid releases OMCI transaction identifier from rxSchedulerMap
func (oo *OmciCC) ReleaseTid(ctx context.Context, tid uint16) {
	logger.Debugw(ctx, "releasing tid from rxSchedulerMap", log.Fields{"tid": tid})
	delete(oo.rxSchedulerMap, tid)
}

// Send - Queue the OMCI Frame for a transmit to the ONU via the proxy_channel
func (oo *OmciCC) Send(ctx context.Context, txFrame []byte, timeout int, retry int, highPrio bool,
	receiveCallbackPair CallbackPair) error {

	if timeout != 0 {
		logger.Debugw(ctx, "register-response-callback:", log.Fields{"for TansCorrId": receiveCallbackPair.CbKey})
		oo.mutexRxSchedMap.Lock()
		// it could be checked, if the callback key is already registered - but simply overwrite may be acceptable ...
		oo.rxSchedulerMap[receiveCallbackPair.CbKey] = receiveCallbackPair.CbEntry
		oo.mutexRxSchedMap.Unlock()
	} //else timeout 0 indicates that no response is expected - fire and forget

	printFrame := receiveCallbackPair.CbEntry.FramePrint //printFrame true means debug print of frame is requested
	//just use a simple list for starting - might need some more effort, especially for multi source write access
	omciTxRequest := OmciTransferStructure{
		txFrame,
		timeout,
		retry,
		highPrio,
		printFrame,
		receiveCallbackPair,
		nil,
	}
	oo.mutexMonReq.Lock()
	defer oo.mutexMonReq.Unlock()
	if _, exist := oo.monitoredRequests[receiveCallbackPair.CbKey]; !exist {
		// do not call processRequestMonitoring in background here to ensure correct sequencing
		// of requested messages into txQueue (especially for non-response-supervised messages)
		oo.processRequestMonitoring(ctx, omciTxRequest)
		return nil
	}
	logger.Errorw(ctx, "A message with this tid is processed already!",
		log.Fields{"tid": receiveCallbackPair.CbKey, "device-id": oo.deviceID})
	return fmt.Errorf("message with tid is processed already %s", oo.deviceID)
}

func (oo *OmciCC) sendQueuedRequests(ctx context.Context) {
	// Avoid accessing the txQueues from parallel send routines to block
	// parallel omci send requests at least until SendIAP is 'committed'.
	// To guarantee window size 1 for one ONU it would be necessary to wait
	// for the corresponding response too (t.b.d.).
	oo.mutexSendQueuedRequests.Lock()
	defer oo.mutexSendQueuedRequests.Unlock()
	if err := oo.sendQueuedHighPrioRequests(ctx); err != nil {
		logger.Errorw(ctx, "Error during sending high prio requests!",
			log.Fields{"err": err, "device-id": oo.deviceID})
		return
	}
	if err := oo.sendQueuedLowPrioRequests(ctx); err != nil {
		logger.Errorw(ctx, "Error during sending low prio requests!",
			log.Fields{"err": err, "device-id": oo.deviceID})
		return
	}
}

func (oo *OmciCC) sendQueuedHighPrioRequests(ctx context.Context) error {
	oo.mutexHighPrioTxQueue.Lock()
	defer oo.mutexHighPrioTxQueue.Unlock()
	for oo.highPrioTxQueue.Len() > 0 {
		queueElement := oo.highPrioTxQueue.Front() // First element
		if err := oo.sendOMCIRequest(ctx, queueElement.Value.(OmciTransferStructure)); err != nil {
			return err
		}
		oo.highPrioTxQueue.Remove(queueElement) // Dequeue
	}
	return nil
}

func (oo *OmciCC) sendQueuedLowPrioRequests(ctx context.Context) error {
	oo.mutexLowPrioTxQueue.Lock()
	for oo.lowPrioTxQueue.Len() > 0 {
		queueElement := oo.lowPrioTxQueue.Front() // First element
		if err := oo.sendOMCIRequest(ctx, queueElement.Value.(OmciTransferStructure)); err != nil {
			oo.mutexLowPrioTxQueue.Unlock()
			return err
		}
		oo.lowPrioTxQueue.Remove(queueElement) // Dequeue
		// Interrupt the sending of low priority requests to process any high priority requests
		// that may have arrived in the meantime
		oo.mutexLowPrioTxQueue.Unlock()
		if err := oo.sendQueuedHighPrioRequests(ctx); err != nil {
			return err
		}
		oo.mutexLowPrioTxQueue.Lock()
	}

	oo.mutexLowPrioTxQueue.Unlock()
	return nil
}

func (oo *OmciCC) sendOMCIRequest(ctx context.Context, omciTxRequest OmciTransferStructure) error {
	if omciTxRequest.withFramePrint {
		logger.Debugw(ctx, "omci-message-to-send:", log.Fields{
			"TxOmciMessage": hex.EncodeToString(omciTxRequest.txFrame),
			"device-id":     oo.deviceID,
			"toDeviceType":  oo.pBaseDeviceHandler.GetProxyAddressType(),
			"proxyDeviceID": oo.pBaseDeviceHandler.GetProxyAddressID(),
			"proxyAddress":  oo.pBaseDeviceHandler.GetProxyAddress()})
	}
	omciMsg := &ia.OmciMessage{
		ParentDeviceId: oo.pBaseDeviceHandler.GetProxyAddressID(),
		ChildDeviceId:  oo.deviceID,
		Message:        omciTxRequest.txFrame,
		ProxyAddress:   oo.pBaseDeviceHandler.GetProxyAddress(),
		ConnectStatus:  common.ConnectStatus_REACHABLE, // If we are sending OMCI messages means we are connected, else we should not be here
	}
	sendErr := oo.pBaseDeviceHandler.SendOMCIRequest(ctx, oo.pBaseDeviceHandler.GetProxyAddress().AdapterEndpoint, omciMsg)
	if sendErr != nil {
		logger.Errorw(ctx, "send omci request error", log.Fields{"ChildId": oo.deviceID, "error": sendErr})
		return sendErr
	}
	return nil
}

// GetNextTid - TODO: add comment
func (oo *OmciCC) GetNextTid(highPriority bool) uint16 {
	var next uint16
	if highPriority {
		oo.mutexHpTid.Lock()
		next = oo.hpTid
		oo.hpTid++
		if oo.hpTid < 0x8000 {
			oo.hpTid = 0x8000
		}
		oo.mutexHpTid.Unlock()
	} else {
		oo.mutexTid.Lock()
		next = oo.tid
		oo.tid++
		if oo.tid >= 0x8000 {
			oo.tid = 1
		}
		oo.mutexTid.Unlock()
	}
	return next
}

// ###################################################################################
// # utility methods provided to work on OMCI messages

// Serialize - TODO: add comment
func Serialize(ctx context.Context, msgType omci.MessageType, request gopacket.SerializableLayer, tid uint16) ([]byte, error) {
	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   msgType,
	}
	return serializeOmciLayer(ctx, omciLayer, request)
}

func serializeOmciLayer(ctx context.Context, aOmciLayer *omci.OMCI, aRequest gopacket.SerializableLayer) ([]byte, error) {
	var options gopacket.SerializeOptions
	options.FixLengths = true

	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, aOmciLayer, aRequest)
	if err != nil {
		logger.Errorw(ctx, "Could not create goPacket Omci serial buffer", log.Fields{"Err": err})
		return nil, err
	}
	return buffer.Bytes(), nil
}

/*
func hexEncode(omciPkt []byte) ([]byte, error) {
	dst := make([]byte, hex.EncodedLen(len(omciPkt)))
	hex.Encode(dst, omciPkt)
	return dst, nil
}
*/

//supply a response handler for omci response messages to be transferred to the requested FSM
func (oo *OmciCC) receiveOmciResponse(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet, respChan chan Message) error {

	logger.Debugw(ctx, "omci-message-response - transfer on omciRespChannel", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "device-id": oo.deviceID})

	if oo.pOnuDeviceEntry == nil {
		logger.Errorw(ctx, "Abort receiving OMCI response, DeviceEntryPointer is nil", log.Fields{
			"device-id": oo.deviceID})
		return fmt.Errorf("deviceEntryPointer is nil %s", oo.deviceID)
	}
	oo.mutexMonReq.RLock()
	if _, exist := oo.monitoredRequests[omciMsg.TransactionID]; exist {
		//implement non-blocking channel send to avoid blocking on mutexMonReq later
		select {
		case oo.monitoredRequests[omciMsg.TransactionID].chSuccess <- true:
		default:
			logger.Debugw(ctx, "response not send on omciRespChannel (no receiver)", log.Fields{
				"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "device-id": oo.deviceID})
		}
	} else {
		logger.Infow(ctx, "reqMon: map entry does not exist!",
			log.Fields{"tid": omciMsg.TransactionID, "device-id": oo.deviceID})
	}
	oo.mutexMonReq.RUnlock()

	// no further test on SeqNo is done here, assignment from rxScheduler is trusted
	// MibSync responses are simply transferred via deviceEntry to MibSync, no specific analysis here
	omciRespMsg := Message{
		Type: OMCI,
		Data: OmciMessage{
			OmciMsg:    omciMsg,
			OmciPacket: packet,
		},
	}
	//logger.Debugw(ctx,"Message to be sent into channel:", log.Fields{"mibSyncMsg": mibSyncMsg})
	respChan <- omciRespMsg

	return nil
}

// SendMibReset sends MibResetRequest
func (oo *OmciCC) SendMibReset(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw(ctx, "send MibReset-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibResetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.MibResetRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibResetRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibUploadFsmCommChan(), oo.receiveOmciResponse, true},
	}
	return oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
}

// SendReboot sends RebootRequest
func (oo *OmciCC) SendReboot(ctx context.Context, timeout int, highPrio bool, responseChannel chan Message) error {
	logger.Debugw(ctx, "send reboot-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.RebootRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuGClassID,
		},
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.RebootRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetOmciRebootMsgRevChan(), oo.receiveOmciResponse, true},
	}

	err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	err = oo.pOnuDeviceEntry.WaitForRebootResponse(ctx, responseChannel)
	if err != nil {
		logger.Errorw(ctx, "aborting ONU reboot!", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	return nil
}

// SendMibUpload sends MibUploadRequest
func (oo *OmciCC) SendMibUpload(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send MibUpload-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibUploadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.MibUploadRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibUploadRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.UploadSequNo = 0
	oo.UploadNoOfCmds = 0

	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibUploadFsmCommChan(), oo.receiveOmciResponse, true},
	}
	return oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
}

// SendMibUploadNext sends MibUploadNextRequest
func (oo *OmciCC) SendMibUploadNext(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send MibUploadNext-msg to:", log.Fields{"device-id": oo.deviceID, "UploadSequNo": oo.UploadSequNo})
	request := &omci.MibUploadNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: oo.UploadSequNo,
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.MibUploadNextRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibUploadNextRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.UploadSequNo++

	omciRxCallbackPair := CallbackPair{
		CbKey: tid,
		//frame printing for MibUpload frames disabled now per default to avoid log file abort situations (size/speed?)
		// if wanted, rx frame printing should be specifically done within the MibUpload FSM or controlled via extra parameter
		// compare also software upgrade download section handling
		CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibUploadFsmCommChan(), oo.receiveOmciResponse, true},
	}
	return oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
}

// SendGetAllAlarm gets all alarm ME instances
func (oo *OmciCC) SendGetAllAlarm(ctx context.Context, alarmRetreivalMode uint8, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send GetAllAlarms-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.GetAllAlarmsRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		AlarmRetrievalMode: byte(alarmRetreivalMode),
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.GetAllAlarmsRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize GetAllAlarmsRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.pOnuAlarmManager.ResetAlarmUploadCounters()

	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{oo.pOnuAlarmManager.GetAlarmMgrEventChannel(), oo.receiveOmciResponse, true},
	}
	return oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
}

// SendGetAllAlarmNext gets next alarm ME instance
func (oo *OmciCC) SendGetAllAlarmNext(ctx context.Context, timeout int, highPrio bool) error {
	alarmUploadSeqNo := oo.pOnuAlarmManager.GetAlarmUploadSeqNo()
	logger.Debugw(ctx, "send SendGetAllAlarmNext-msg to:", log.Fields{"device-id": oo.deviceID,
		"alarmUploadSeqNo": alarmUploadSeqNo})
	request := &omci.GetAllAlarmsNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: alarmUploadSeqNo,
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := Serialize(ctx, omci.GetAllAlarmsNextRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize GetAllAlarmsNextRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.pOnuAlarmManager.IncrementAlarmUploadSeqNo()

	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{oo.pOnuAlarmManager.GetAlarmMgrEventChannel(), oo.receiveOmciResponse, true},
	}
	return oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
}

// SendCreateGalEthernetProfile creates GalEthernetProfile ME instance
func (oo *OmciCC) SendCreateGalEthernetProfile(ctx context.Context, timeout int, highPrio bool) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GalEnetProfile-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	meParams := me.ParamData{
		EntityID:   GalEthernetEID,
		Attributes: me.AttributeValueMap{"MaximumGemPayloadSize": maxGemPayloadSize},
	}
	meInstance, omciErr := me.NewGalEthernetProfile(meParams)
	if omciErr.GetError() == nil {
		//all setByCreate parameters already set, no default option required ...
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GalEnetProfileInstance for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibDownloadFsmCommChan(), oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GalEnetProfile-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GalEnetProfileInstance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetOnu2g sets Onu2G ME instance
// might be needed to extend for parameter arguments, here just for setting the ConnectivityMode!!
func (oo *OmciCC) SendSetOnu2g(ctx context.Context, timeout int, highPrio bool) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send ONU2-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// ONU-G ME-ID is defined to be 0, but we could verify, if the ONU really supports the desired
	//   connectivity mode 5 (in ConnCap)
	// By now we just use fix values to fire - this is anyway what the python adapter does
	// read ONU-2G from DB ???? //TODO!!!
	meParams := me.ParamData{
		EntityID:   0,
		Attributes: me.AttributeValueMap{"CurrentConnectivityMode": connectivityModeValue},
	}
	meInstance, omciErr := me.NewOnu2G(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ONU2-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibDownloadFsmCommChan(), oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send ONU2-G-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate ONU2-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMBServiceProfile creates MacBridgeServiceProfile ME instance
func (oo *OmciCC) SendCreateMBServiceProfile(ctx context.Context,
	aPUniPort *OnuUniPort, timeout int, highPrio bool) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	instID := MacBridgeServiceProfileEID + uint16(aPUniPort.MacBpNo)
	logger.Debugw(ctx, "send MBSP-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16)})

	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"Priority":                   0x8000,
			"MaxAge":                     20 * 256, //20s
			"HelloTime":                  2 * 256,  //2s
			"ForwardDelay":               15 * 256, //15s
			"DynamicFilteringAgeingTime": 0,
		},
	}

	meInstance, omciErr := me.NewMacBridgeServiceProfile(meParams)
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBSP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibDownloadFsmCommChan(), oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MBSP-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MBSP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMBPConfigDataUniSide creates MacBridgePortConfigurationData ME instance
func (oo *OmciCC) SendCreateMBPConfigDataUniSide(ctx context.Context,
	aPUniPort *OnuUniPort, timeout int, highPrio bool) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	instID, idErr := GenerateUNISideMBPCDEID(uint16(aPUniPort.MacBpNo))
	if idErr != nil {
		logger.Errorw(ctx, "Cannot generate MBPCD entity id", log.Fields{
			"Err": idErr, "device-id": oo.deviceID})
		return nil, idErr
	}
	logger.Debugw(ctx, "send MBPCD-Create-msg  for uni side:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16), "macBpNo": aPUniPort.MacBpNo})

	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"BridgeIdPointer": MacBridgeServiceProfileEID + uint16(aPUniPort.MacBpNo),
			"PortNum":         aPUniPort.MacBpNo,
			"TpType":          uint8(aPUniPort.PortType),
			"TpPointer":       aPUniPort.EntityID,
		},
	}
	meInstance, omciErr := me.NewMacBridgePortConfigurationData(meParams)
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibDownloadFsmCommChan(), oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MBPCD-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateEVTOConfigData creates ExtendedVlanTaggingOperationConfigurationData ME instance
func (oo *OmciCC) SendCreateEVTOConfigData(ctx context.Context,
	aPUniPort *OnuUniPort, timeout int, highPrio bool) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	//same entityId is used as for MBSP (see there), but just arbitrary ...
	instID := MacBridgeServiceProfileEID + uint16(aPUniPort.MacBpNo)
	logger.Debugw(ctx, "send EVTOCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16)})

	// compare python adapter code WA VOL-1311: this is not done here!
	//   (setting TPID values for the create would probably anyway be ignored by the omci lib)
	//    but perhaps we have to be aware of possible problems at get(Next) Request handling for EVTOOCD tables later ...
	assType := uint8(2) // default AssociationType is PPTPEthUni
	if aPUniPort.PortType == UniVEIP {
		assType = uint8(10) // for VEIP
	}
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"AssociationType":     assType,
			"AssociatedMePointer": aPUniPort.EntityID,
			//EnhancedMode not yet supported, used with default options
		},
	}
	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(meParams)
	if omciErr.GetError() == nil {
		//all setByCreate parameters already set, no default option required ...
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{oo.pOnuDeviceEntry.GetMibDownloadFsmCommChan(), oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send EVTOCD-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetOnuGLS sets OnuG ME instance
func (oo *OmciCC) SendSetOnuGLS(ctx context.Context, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send ONU-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// ONU-G ME-ID is defined to be 0, no need to perform a DB lookup
	meParams := me.ParamData{
		EntityID:   0,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewOnuG(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ONU-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send ONU-G-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate ONU-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetPptpEthUniLS sets PhysicalPathTerminationPointEthernetUni ME instance
func (oo *OmciCC) SendSetPptpEthUniLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send PPTPEthUni-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// PPTPEthUni ME-ID is taken from Mib Upload stored OnuUniPort instance (argument)
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewPhysicalPathTerminationPointEthernetUni(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode PPTPEthUni instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize PPTPEthUni-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send PPTPEthUni-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send PPTPEthUni-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate PPTPEthUni", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

/* UniG obsolete by now, left here in case it should be needed once again
   UniG AdminState anyway should be ignored by ONU acc. to G988
func (oo *omciCC) sendSetUniGLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx,"send UNI-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// UNI-G ME-ID is taken from Mib Upload stored OnuUniPort instance (argument)
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewUniG(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx,"Cannot encode UNI-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx,"Cannot serialize UNI-G-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx,"Cannot send UNIG-G-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx,"send UNI-G-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx,"Cannot generate UNI-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}
*/

// SendSetVeipLS sets VirtualEthernetInterfacePoint ME instance
func (oo *OmciCC) SendSetVeipLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VEIP-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// ONU-G ME-ID is defined to be 0, no need to perform a DB lookup
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewVirtualEthernetInterfacePoint(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VEIP instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VEIP-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VEIP", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendGetMe gets ME instance
func (oo *OmciCC) SendGetMe(ctx context.Context, classID me.ClassID, entityID uint16, requestedAttributes me.AttributeValueMap,
	timeout int, highPrio bool, rxChan chan Message) (*me.ManagedEntity, error) {

	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send get-request-msg", log.Fields{"classID": classID, "device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	meParams := me.ParamData{
		EntityID:   entityID,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.LoadManagedEntityDefinition(classID, meParams)
	if omciErr.GetError() == nil {
		meClassIDName := meInstance.GetName()
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.GetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorf(ctx, "Cannot encode instance for get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send get-request-msg", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debugw(ctx, "send get-request-msg done", log.Fields{"meClassIDName": meClassIDName, "device-id": oo.deviceID})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate meDefinition", log.Fields{"classID": classID, "Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendGetMeWithAttributeMask gets ME instance with attribute mask
func (oo *OmciCC) SendGetMeWithAttributeMask(ctx context.Context, classID me.ClassID, entityID uint16, requestedAttributesMask uint16,
	timeout int, highPrio bool, rxChan chan Message) error {

	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send get-request-msg", log.Fields{"classID": classID, "device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	request := &omci.GetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityInstance: entityID,
			EntityClass:    classID,
		},
		AttributeMask: requestedAttributesMask,
	}

	pkt, err := Serialize(ctx, omci.GetRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize get-request", log.Fields{"meClassIDName": classID, "Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := CallbackPair{
		CbKey:   tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send get-request-msg", log.Fields{"meClassIDName": classID, "Err": err, "device-id": oo.deviceID})
		return err
	}
	logger.Debugw(ctx, "send get-request-msg done", log.Fields{"meClassIDName": classID, "device-id": oo.deviceID})
	return nil
}

// SendCreateDot1PMapper creates Ieee8021PMapperServiceProfile ME instance
func (oo *OmciCC) SendCreateDot1PMapper(ctx context.Context, timeout int, highPrio bool,
	aInstID uint16, rxChan chan Message) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send .1pMapper-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{
		EntityID: aInstID,
		Attributes: me.AttributeValueMap{
			//workaround for unsuitable omci-lib default values, cmp VOL-3729
			"TpPointer":                          0xFFFF,
			"InterworkTpPointerForPBitPriority0": 0xFFFF,
			"InterworkTpPointerForPBitPriority1": 0xFFFF,
			"InterworkTpPointerForPBitPriority2": 0xFFFF,
			"InterworkTpPointerForPBitPriority3": 0xFFFF,
			"InterworkTpPointerForPBitPriority4": 0xFFFF,
			"InterworkTpPointerForPBitPriority5": 0xFFFF,
			"InterworkTpPointerForPBitPriority6": 0xFFFF,
			"InterworkTpPointerForPBitPriority7": 0xFFFF,
		},
	}
	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(meParams)
	if omciErr.GetError() == nil {
		//we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode .1pMapper for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send .1pMapper-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate .1pMapper", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMBPConfigDataVar creates MacBridgePortConfigurationData ME instance
func (oo *OmciCC) SendCreateMBPConfigDataVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MBPCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMacBridgePortConfigurationData(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MBPCD-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateGemNCTPVar creates GemPortNetworkCtp ME instance
func (oo *OmciCC) SendCreateGemNCTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GemNCTP-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemPortNetworkCtp(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemNCTP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GemNCTP-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GemNCTP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetGemNCTPVar sets GemPortNetworkCtp ME instance
func (oo *OmciCC) SendSetGemNCTPVar(ctx context.Context, timeout int, highPrio bool, rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GemNCTP-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemPortNetworkCtp(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemNCTP for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemNCTP set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemNCTP set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GemNCTP-Set-msg done", log.Fields{"device-id": oo.deviceID})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GemNCTP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateGemIWTPVar creates GemInterworkingTerminationPoint ME instance
func (oo *OmciCC) SendCreateGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GemIwTp-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemIwTp for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GemIwTp-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GemIwTp Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetTcontVar sets TCont ME instance
func (oo *OmciCC) SendSetTcontVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TCont-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTCont(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TCont for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TCont-set msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TCont Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetPrioQueueVar sets PriorityQueue ME instance
func (oo *OmciCC) SendSetPrioQueueVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send PrioQueue-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewPriorityQueue(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode PrioQueue for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send PrioQueue-set msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate PrioQueue Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetDot1PMapperVar sets Ieee8021PMapperServiceProfile ME instance
func (oo *OmciCC) SendSetDot1PMapperVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send 1PMapper-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode 1PMapper for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send 1PMapper-set msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate 1PMapper Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateVtfdVar creates VlanTaggingFilterData ME instance
func (oo *OmciCC) SendCreateVtfdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVlanTaggingFilterData(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VTFD-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// nolint: unused
func (oo *OmciCC) sendSetVtfdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVlanTaggingFilterData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VTFD-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateEvtocdVar creates ExtendedVlanTaggingOperationConfigurationData ME instance
func (oo *OmciCC) SendCreateEvtocdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		//EnhancedMode not yet supported, used with default options
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType,
			oframe.TransactionID(tid), oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send EVTOCD-set msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetEvtocdVar sets ExtendedVlanTaggingOperationConfigurationData ME instance
func (oo *OmciCC) SendSetEvtocdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send EVTOCD-set msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteEvtocd deletes ExtendedVlanTaggingOperationConfigurationData ME instance
func (oo *OmciCC) SendDeleteEvtocd(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send EVTOCD-delete msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteVtfd deletes VlanTaggingFilterData ME instance
func (oo *OmciCC) SendDeleteVtfd(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVlanTaggingFilterData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VTFD-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateTDVar creates TrafficDescriptor ME instance
func (oo *OmciCC) SendCreateTDVar(ctx context.Context, timeout int, highPrio bool, rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})
	meInstance, omciErr := me.NewTrafficDescriptor(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TD-Create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// nolint: unused
func (oo *OmciCC) sendSetTDVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTrafficDescriptor(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TD-Set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()

}

// SendDeleteTD - TODO: add comment
func (oo *OmciCC) SendDeleteTD(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewTrafficDescriptor(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TD-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()

}

// SendDeleteGemIWTP deletes GemInterworkingTerminationPoint ME instance
func (oo *OmciCC) SendDeleteGemIWTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GemIwTp-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewGemInterworkingTerminationPoint(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemIwTp for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GemIwTp-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GemIwTp Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteMulticastGemIWTP deletes MulticastGemInterworkingTerminationPoint ME instance
func (oo *OmciCC) SendDeleteMulticastGemIWTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastGemIwTp-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewMulticastGemInterworkingTerminationPoint(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastGemIwTp for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastGemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastGemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastGemIwTp-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastGemIwTp Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteGemNCTP deletes GemPortNetworkCtp ME instance
func (oo *OmciCC) SendDeleteGemNCTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send GemNCtp-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewGemPortNetworkCtp(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemNCtp for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemNCtp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemNCtp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send GemNCtp-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate GemNCtp Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteDot1PMapper deletes Ieee8021PMapperServiceProfile ME instance
func (oo *OmciCC) SendDeleteDot1PMapper(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send .1pMapper-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode .1pMapper for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize .1pMapper delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send .1pMapper delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send .1pMapper-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate .1pMapper Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteMBPConfigData deletes MacBridgePortConfigurationData ME instance
func (oo *OmciCC) SendDeleteMBPConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MBPCD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewMacBridgePortConfigurationData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MBPCD-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMulticastGemIWTPVar creates MulticastGemInterworkingTerminationPoint ME instance
func (oo *OmciCC) SendCreateMulticastGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastGemIWTP-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastGEMIWTP for create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastGEMIWTP-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastGEMIWTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetMulticastGemIWTPVar sets MulticastGemInterworkingTerminationPoint ME instance
func (oo *OmciCC) SendSetMulticastGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastGemIWTP-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastGEMIWTP for set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastGEMIWTP set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastGEMIWTP-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastGEMIWTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMulticastOperationProfileVar creates MulticastOperationsProfile ME instance
func (oo *OmciCC) SendCreateMulticastOperationProfileVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastOperationProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastOperationsProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastOperationProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastOperationProfile-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastOperationProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetMulticastOperationProfileVar sets MulticastOperationsProfile ME instance
func (oo *OmciCC) SendSetMulticastOperationProfileVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastOperationProfile-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastOperationsProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastOperationProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastOperationProfile-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastOperationProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateMulticastSubConfigInfoVar creates MulticastSubscriberConfigInfo ME instance
func (oo *OmciCC) SendCreateMulticastSubConfigInfoVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastSubConfigInfo-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastSubscriberConfigInfo(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastSubConfigInfo for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastSubConfigInfo create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastSubConfigInfo create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send MulticastSubConfigInfo-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate MulticastSubConfigInfo Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateVoipVoiceCTP nolint: unused
func (oo *OmciCC) SendCreateVoipVoiceCTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipVoiceCTP-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipVoiceCtp(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipVoiceCTP for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipVoiceCTP create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipVoiceCTP create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipVoiceCTP-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipVoiceCTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetVoipVoiceCTP nolint: unused
func (oo *OmciCC) SendSetVoipVoiceCTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipVoiceCTP-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipVoiceCtp(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipVoiceCTP for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipVoiceCTP set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipVoiceCTP set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipVoiceCTP-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipVoiceCTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteVoipVoiceCTP nolint: unused
func (oo *OmciCC) SendDeleteVoipVoiceCTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipVoiceCTP-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVoipVoiceCtp(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipVoiceCTP for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipVoiceCTP delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipVoiceCTP delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipVoiceCTP-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipVoiceCTP Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateVoipMediaProfile nolint: unused
func (oo *OmciCC) SendCreateVoipMediaProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipMediaProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipMediaProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipMediaProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipMediaProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipMediaProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipMediaProfile-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipMediaProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetVoipMediaProfile nolint: unused
func (oo *OmciCC) SendSetVoipMediaProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipMediaProfile-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipMediaProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipMediaProfile for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipMediaProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipMediaProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipMediaProfile-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipMediaProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteVoipMediaProfile nolint: unused
func (oo *OmciCC) SendDeleteVoipMediaProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipMediaProfile-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVoipMediaProfile(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipMediaProfile for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipMediaProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipMediaProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipMediaProfile-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipMediaProfile Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateVoiceServiceProfile nolint: unused
func (oo *OmciCC) SendCreateVoiceServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoiceServiceProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoiceServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoiceServiceProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoiceServiceProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoiceServiceProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoiceServiceProfile-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoiceServiceProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetVoiceServiceProfile nolint: unused
func (oo *OmciCC) SendSetVoiceServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoiceServiceProfile-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoiceServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoiceServiceProfile for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoiceServiceProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoiceServiceProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoiceServiceProfile-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoiceServiceProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteVoiceServiceProfile nolint: unused
func (oo *OmciCC) SendDeleteVoiceServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoiceServiceProfile-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVoiceServiceProfile(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoiceServiceProfile for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoiceServiceProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoiceServiceProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoiceServiceProfile-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoiceServiceProfile Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateSIPUserData nolint: unused
func (oo *OmciCC) SendCreateSIPUserData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPUserData-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewSipUserData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPUserData for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPUserData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPUserData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPUserData-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPUserData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetSIPUserData nolint: unused
func (oo *OmciCC) SendSetSIPUserData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPUserData-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewSipUserData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPUserData for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPUserData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPUserData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPUserData-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPUserData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteSIPUserData nolint: unused
func (oo *OmciCC) SendDeleteSIPUserData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPUserData-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewSipUserData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPUserData for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPUserData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPUserData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPUserData-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPUserData Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateVoipApplicationServiceProfile nolint: unused
func (oo *OmciCC) SendCreateVoipApplicationServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipApplicationServiceProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipApplicationServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipApplicationServiceProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipApplicationServiceProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipApplicationServiceProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipApplicationServiceProfile-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipApplicationServiceProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetVoipApplicationServiceProfile nolint: unused
func (oo *OmciCC) SendSetVoipApplicationServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send VoipApplicationServiceProfile-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVoipApplicationServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VoipApplicationServiceProfile for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VoipApplicationServiceProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VoipApplicationServiceProfile set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send VoipApplicationServiceProfile-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate VoipApplicationServiceProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteVoipApplicationServiceProfile nolint: unused
func (oo *OmciCC) SendDeleteVoipApplicationServiceProfile(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPVoipApplicationServiceProfile-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVoipApplicationServiceProfile(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPVoipApplicationServiceProfile for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPVoipApplicationServiceProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPVoipApplicationServiceProfile delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPVoipApplicationServiceProfile-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPVoipApplicationServiceProfile Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateSIPAgentConfigData nolint: unused
func (oo *OmciCC) SendCreateSIPAgentConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPAgentConfigData-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewSipAgentConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPAgentConfigData for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPAgentConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPAgentConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPAgentConfigData-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPAgentConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetSIPAgentConfigData nolint: unused
func (oo *OmciCC) SendSetSIPAgentConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPAgentConfigData-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewSipAgentConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPAgentConfigData for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPAgentConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPAgentConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPAgentConfigData-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPAgentConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteSIPAgentConfigData nolint: unused
func (oo *OmciCC) SendDeleteSIPAgentConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send SIPAgentConfigData-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewSipAgentConfigData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode SIPAgentConfigData for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize SIPAgentConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send SIPAgentConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send SIPAgentConfigData-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPAgentConfigData Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateTCPUDPConfigData nolint: unused
func (oo *OmciCC) SendCreateTCPUDPConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TCPUDPConfigData-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTcpUdpConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TCPUDPConfigData for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TCPUDPConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TCPUDPConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TCPUDPConfigData-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TCPUDPConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetTCPUDPConfigData nolint: unused
func (oo *OmciCC) SendSetTCPUDPConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TCPUDPConfigData-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTcpUdpConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TCPUDPConfigData for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TCPUDPConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TCPUDPConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TCPUDPConfigData-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate TCPUDPConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteTCPUDPConfigData nolint: unused
func (oo *OmciCC) SendDeleteTCPUDPConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send TCPUDPConfigData-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewTcpUdpConfigData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TCPUDPConfigData for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TCPUDPConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TCPUDPConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send TCPUDPConfigData-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate SIPAgentConfigData Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateIPHostConfigData nolint: unused
func (oo *OmciCC) SendCreateIPHostConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send IPHostConfigData-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewIpHostConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode IPHostConfigData for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize IPHostConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send IPHostConfigData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send IPHostConfigData-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate IPHostConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetIPHostConfigData nolint: unused
func (oo *OmciCC) SendSetIPHostConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send IPHostConfigData-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewIpHostConfigData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode IPHostConfigData for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize IPHostConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send IPHostConfigData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send IPHostConfigData-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate IPHostConfigData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteIPHostConfigData nolint: unused
func (oo *OmciCC) SendDeleteIPHostConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send IPHostConfigData-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewIpHostConfigData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode IPHostConfigData for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize IPHostConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send IPHostConfigData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send IPHostConfigData-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate IPHostConfigData Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateRTPProfileData nolint: unused
func (oo *OmciCC) SendCreateRTPProfileData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send RTPProfileData-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewRtpProfileData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode RTPProfileData for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize RTPProfileData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send RTPProfileData create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send RTPProfileData-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate RTPProfileData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetRTPProfileData nolint: unused
func (oo *OmciCC) SendSetRTPProfileData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send RTPProfileData-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewRtpProfileData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode RTPProfileData for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize RTPProfileData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send RTPProfileData set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send RTPProfileData-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate RTPProfileData Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteRTPProfileData nolint: unused
func (oo *OmciCC) SendDeleteRTPProfileData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send RTPProfileData-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewRtpProfileData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode RTPProfileData for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize RTPProfileData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send RTPProfileData delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send RTPProfileData-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate RTPProfileData Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendCreateNetworkDialPlanTable nolint: unused
func (oo *OmciCC) SendCreateNetworkDialPlanTable(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send NetworkDialPlanTable-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewNetworkDialPlanTable(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode NetworkDialPlanTable for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize NetworkDialPlanTable create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send NetworkDialPlanTable create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send NetworkDialPlanTable-create-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate NetworkDialPlanTable Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSetNetworkDialPlanTable nolint: unused
func (oo *OmciCC) SendSetNetworkDialPlanTable(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send NetworkDialPlanTable-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewNetworkDialPlanTable(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid),
			oframe.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode NetworkDialPlanTable for set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize NetworkDialPlanTable set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send NetworkDialPlanTable set", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send NetworkDialPlanTable-set-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate NetworkDialPlanTable Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendDeleteNetworkDialPlanTable nolint: unused
func (oo *OmciCC) SendDeleteNetworkDialPlanTable(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send NetworkDialPlanTable-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewNetworkDialPlanTable(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.DeleteRequestType,
			oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode NetworkDialPlanTable for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize NetworkDialPlanTable delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send NetworkDialPlanTable delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil, err
		}
		logger.Debug(ctx, "send NetworkDialPlanTable-Delete-msg done")
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate NetworkDialPlanTable Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil, omciErr.GetError()
}

// SendSyncTime sends SynchronizeTimeRequest
func (oo *OmciCC) SendSyncTime(ctx context.Context, timeout int, highPrio bool, rxChan chan Message) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send synchronize time request:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.SynchronizeTimeRequestType,
		// DeviceIdentifier: omci.BaselineIdent,        // Optional, defaults to Baseline
		// Length:           0x28,                      // Optional, defaults to 40 octets
	}
	utcTime := time.Now().UTC()
	request := &omci.SynchronizeTimeRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuGClassID,
			// Default Instance ID is 0
		},
		Year:   uint16(utcTime.Year()),
		Month:  uint8(utcTime.Month()),
		Day:    uint8(utcTime.Day()),
		Hour:   uint8(utcTime.Hour()),
		Minute: uint8(utcTime.Minute()),
		Second: uint8(utcTime.Second()),
	}

	pkt, err := serializeOmciLayer(ctx, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize synchronize time request", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send synchronize time request", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send synchronize time request done")
	return nil
}

// SendCreateOrDeleteEthernetPerformanceMonitoringHistoryME creates or deletes EthernetFramePerformanceMonitoringHistoryData ME instance
func (oo *OmciCC) SendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(ctx context.Context, timeout int, highPrio bool,
	upstream bool, create bool, rxChan chan Message, entityID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send ethernet-performance-monitoring-history-me-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16), "create": create, "upstream": upstream})
	meParam := me.ParamData{EntityID: entityID}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	if upstream {
		meInstance, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataUpstream(meParam)
	} else {
		meInstance, omciErr = me.NewEthernetFramePerformanceMonitoringHistoryDataDownstream(meParam)
	}
	if omciErr.GetError() == nil {
		var omciLayer *omci.OMCI
		var msgLayer gopacket.SerializableLayer
		var err error
		if create {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send ethernet frame performance monitoring history data ME done",
			log.Fields{"device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate ethernet frame performance monitoring history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// SendCreateOrDeleteEthernetUniHistoryME creates or deletes EthernetPerformanceMonitoringHistoryData ME instance
func (oo *OmciCC) SendCreateOrDeleteEthernetUniHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send ethernet-uni-history-me-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16), "create": create})
	meParam := me.ParamData{EntityID: entityID}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	meInstance, omciErr = me.NewEthernetPerformanceMonitoringHistoryData(meParam)

	if omciErr.GetError() == nil {
		var omciLayer *omci.OMCI
		var msgLayer gopacket.SerializableLayer
		var err error
		if create {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send ethernet uni history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate ethernet uni history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// SendCreateOrDeleteFecHistoryME creates or deletes FecPerformanceMonitoringHistoryData ME instance
func (oo *OmciCC) SendCreateOrDeleteFecHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send fec-history-me-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16), "create": create})
	meParam := me.ParamData{EntityID: entityID}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	meInstance, omciErr = me.NewFecPerformanceMonitoringHistoryData(meParam)

	if omciErr.GetError() == nil {
		var omciLayer *omci.OMCI
		var msgLayer gopacket.SerializableLayer
		var err error
		if create {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send fec history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate fec history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// SendCreateOrDeleteGemPortHistoryME deletes GemPortNetworkCtpPerformanceMonitoringHistoryData ME instance
func (oo *OmciCC) SendCreateOrDeleteGemPortHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send gemport-history-me-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16), "create": create})
	meParam := me.ParamData{EntityID: entityID}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	meInstance, omciErr = me.NewGemPortNetworkCtpPerformanceMonitoringHistoryData(meParam)

	if omciErr.GetError() == nil {
		var omciLayer *omci.OMCI
		var msgLayer gopacket.SerializableLayer
		var err error
		if create {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send gemport history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "Cannot generate gemport history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// SendStartSoftwareDownload sends StartSoftwareDownloadRequest
func (oo *OmciCC) SendStartSoftwareDownload(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aDownloadWindowSize uint8, aFileLen uint32) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send StartSwDlRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16)})

	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.StartSoftwareDownloadRequestType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	request := &omci.StartSoftwareDownloadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.SoftwareImageClassID,
			EntityInstance: aImageMeID, //inactive image
		},
		WindowSize:           aDownloadWindowSize,
		ImageSize:            aFileLen,
		NumberOfCircuitPacks: 1,           //parallel download to multiple circuit packs not supported
		CircuitPacks:         []uint16{0}, //circuit pack indication don't care for NumberOfCircuitPacks=1, but needed by omci-lib
	}

	var options gopacket.SerializeOptions
	options.FixLengths = true
	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize StartSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send StartSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send StartSwDlRequest done")
	return nil
}

// SendDownloadSection sends DownloadSectionRequestWithResponse
func (oo *OmciCC) SendDownloadSection(ctx context.Context, aTimeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aAckRequest uint8, aDownloadSectionNo uint8, aSection []byte, aPrint bool) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send DlSectionRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16), "omci-ack": aAckRequest})

	//TODO!!!: don't know by now on how to generate the possibly needed AR (or enforce it to 0) with current omci-lib
	//    by now just try to send it as defined by omci-lib
	msgType := omci.DownloadSectionRequestType
	var timeout int = 0 //default value for no response expected
	if aAckRequest > 0 {
		msgType = omci.DownloadSectionRequestWithResponseType
		timeout = aTimeout
	}
	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   msgType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	localSectionData := make([]byte, len(aSection))

	copy(localSectionData[:], aSection) // as long as DownloadSectionRequest defines array for SectionData we need to copy into the array
	request := &omci.DownloadSectionRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.SoftwareImageClassID,
			EntityInstance: aImageMeID, //inactive image
		},
		SectionNumber: aDownloadSectionNo,
		SectionData:   localSectionData,
	}

	var options gopacket.SerializeOptions
	options.FixLengths = true
	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize DlSectionRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	//for initial debug purpose overrule the requested print state for some frames
	printFrame := aPrint
	if aAckRequest > 0 || aDownloadSectionNo == 0 {
		printFrame = true
	}

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		// the callback is set even though no response might be required here, the tid (key) setting is needed here anyway
		//   (used to avoid retransmission of frames with the same TID)
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, printFrame /*aPrint*/},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send DlSectionRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send DlSectionRequest done")
	return nil
}

//SendEndSoftwareDownload sends EndSoftwareDownloadRequest
func (oo *OmciCC) SendEndSoftwareDownload(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aFileLen uint32, aImageCrc uint32) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send EndSwDlRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16)})

	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.EndSoftwareDownloadRequestType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	request := &omci.EndSoftwareDownloadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.SoftwareImageClassID,
			EntityInstance: aImageMeID, //inactive image
		},
		CRC32:             aImageCrc,
		ImageSize:         aFileLen,
		NumberOfInstances: 1,           //parallel download to multiple circuit packs not supported
		ImageInstances:    []uint16{0}, //don't care for NumberOfInstances=1, but probably needed by omci-lib as in startSwDlRequest
	}

	var options gopacket.SerializeOptions
	options.FixLengths = true
	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize EndSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send EndSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send EndSwDlRequest done")
	return nil
}

// SendActivateSoftware sends ActivateSoftwareRequest
func (oo *OmciCC) SendActivateSoftware(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send ActivateSwRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16)})

	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.ActivateSoftwareRequestType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	request := &omci.ActivateSoftwareRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.SoftwareImageClassID,
			EntityInstance: aImageMeID, //inactive image
		},
		ActivateFlags: 0, //unconditionally reset as the only relevant option here (regardless of VOIP)
	}

	var options gopacket.SerializeOptions
	options.FixLengths = true
	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize ActivateSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send ActivateSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send ActivateSwRequest done")
	return nil
}

// SendCommitSoftware sends CommitSoftwareRequest
func (oo *OmciCC) SendCommitSoftware(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send CommitSwRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16)})

	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.CommitSoftwareRequestType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	request := &omci.CommitSoftwareRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.SoftwareImageClassID,
			EntityInstance: aImageMeID, //inactive image
		},
	}

	var options gopacket.SerializeOptions
	options.FixLengths = true
	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize CommitSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send CommitSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send CommitSwRequest done")
	return nil
}

//SendSelfTestReq sends TestRequest
func (oo *OmciCC) SendSelfTestReq(ctx context.Context, classID me.ClassID, instdID uint16, timeout int, highPrio bool, rxChan chan Message) error {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send self test request:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(instdID), 16)})
	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   omci.TestRequestType,
		// DeviceIdentifier: omci.BaselineIdent,    // Optional, defaults to Baseline
		// Length:           0x28,                                      // Optional, defaults to 40 octets
	}

	var request *omci.OpticalLineSupervisionTestRequest
	switch classID {
	case me.AniGClassID:
		request = &omci.OpticalLineSupervisionTestRequest{
			MeBasePacket: omci.MeBasePacket{
				EntityClass:    classID,
				EntityInstance: instdID,
			},
			SelectTest:               uint8(7), // self test
			GeneralPurposeBuffer:     uint16(0),
			VendorSpecificParameters: uint16(0),
		}
	default:
		logger.Errorw(ctx, "unsupported class id for self test request", log.Fields{"device-id": oo.deviceID, "classID": classID})
		return fmt.Errorf("unsupported-class-id-for-self-test-request-%v", classID)
	}
	// Test serialization back to former string
	var options gopacket.SerializeOptions
	options.FixLengths = true

	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize self test request", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	outgoingPacket := buffer.Bytes()

	omciRxCallbackPair := CallbackPair{CbKey: tid,
		CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.Send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send self test request", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send self test request done")
	return nil
}

//nolint: gocyclo
func isSuccessfulResponseWithMibDataSync(omciMsg *omci.OMCI, packet *gp.Packet) bool {
	for _, v := range responsesWithMibDataSync {
		if v == omciMsg.MessageType {
			nextLayer, _ := omci.MsgTypeToNextLayer(v, false)
			msgLayer := (*packet).Layer(nextLayer)
			switch nextLayer {
			case omci.LayerTypeCreateResponse:
				if resp := msgLayer.(*omci.CreateResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeDeleteResponse:
				if resp := msgLayer.(*omci.DeleteResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeSetResponse:
				if resp := msgLayer.(*omci.SetResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeStartSoftwareDownloadResponse:
				if resp := msgLayer.(*omci.StartSoftwareDownloadResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeEndSoftwareDownloadResponse:
				if resp := msgLayer.(*omci.EndSoftwareDownloadResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeActivateSoftwareResponse:
				if resp := msgLayer.(*omci.ActivateSoftwareResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			case omci.LayerTypeCommitSoftwareResponse:
				if resp := msgLayer.(*omci.CommitSoftwareResponse); resp != nil {
					if resp.Result == me.Success {
						return true
					}
				}
			}
		}
	}
	return false
}

func (oo *OmciCC) processRequestMonitoring(ctx context.Context, aOmciTxRequest OmciTransferStructure) {
	timeout := aOmciTxRequest.timeout
	if timeout == 0 {
		//timeout 0 indicates that no response is expected - fire and forget
		// enqueue
		if aOmciTxRequest.highPrio {
			oo.mutexHighPrioTxQueue.Lock()
			oo.highPrioTxQueue.PushBack(aOmciTxRequest)
			oo.mutexHighPrioTxQueue.Unlock()
		} else {
			oo.mutexLowPrioTxQueue.Lock()
			oo.lowPrioTxQueue.PushBack(aOmciTxRequest)
			oo.mutexLowPrioTxQueue.Unlock()
		}
		go oo.sendQueuedRequests(ctx)
	} else {
		//the supervised sending with waiting on the response (based on TID) is called in background
		//  to avoid blocking of the sender for the complete OMCI handshake procedure
		//  to stay consistent with the processing tested so far, sending of next messages of the same control procedure
		//  is ensured by the according control instances (FSM's etc.) (by waiting for the respective responses there)
		go oo.sendWithRxSupervision(ctx, aOmciTxRequest, timeout)
	}
}

func (oo *OmciCC) sendWithRxSupervision(ctx context.Context, aOmciTxRequest OmciTransferStructure, aTimeout int) {
	chSuccess := make(chan bool)
	aOmciTxRequest.chSuccess = chSuccess
	tid := aOmciTxRequest.cbPair.CbKey
	oo.mutexMonReq.Lock()
	oo.monitoredRequests[tid] = aOmciTxRequest
	oo.mutexMonReq.Unlock()

	retries := aOmciTxRequest.retries
	retryCounter := 0
loop:
	for retryCounter <= retries {
		// enqueue
		if aOmciTxRequest.highPrio {
			oo.mutexHighPrioTxQueue.Lock()
			oo.highPrioTxQueue.PushBack(aOmciTxRequest)
			oo.mutexHighPrioTxQueue.Unlock()
		} else {
			oo.mutexLowPrioTxQueue.Lock()
			oo.lowPrioTxQueue.PushBack(aOmciTxRequest)
			oo.mutexLowPrioTxQueue.Unlock()
		}
		go oo.sendQueuedRequests(ctx)

		select {
		case success := <-chSuccess:
			if success {
				logger.Debugw(ctx, "reqMon: response received in time",
					log.Fields{"tid": tid, "device-id": oo.deviceID})
			} else {
				logger.Debugw(ctx, "reqMon: wait for response aborted",
					log.Fields{"tid": tid, "device-id": oo.deviceID})
			}
			break loop
		case <-time.After(time.Duration(aTimeout) * time.Second):
			if retryCounter == retries {
				logger.Errorw(ctx, "reqMon: timeout waiting for response - no of max retries reached!",
					log.Fields{"tid": tid, "retries": retryCounter, "device-id": oo.deviceID})
				break loop
			} else {
				logger.Infow(ctx, "reqMon: timeout waiting for response - retry",
					log.Fields{"tid": tid, "retries": retryCounter, "device-id": oo.deviceID})
			}
		}
		retryCounter++
	}
	oo.mutexMonReq.Lock()
	delete(oo.monitoredRequests, tid)
	oo.mutexMonReq.Unlock()
}

//CancelRequestMonitoring terminates monitoring of outstanding omci requests
func (oo *OmciCC) CancelRequestMonitoring(ctx context.Context) {
	oo.mutexMonReq.RLock()
	for k := range oo.monitoredRequests {
		//implement non-blocking channel send to avoid blocking on mutexMonReq later
		select {
		case oo.monitoredRequests[k].chSuccess <- false:
		default:
			logger.Debugw(ctx, "cancel not send on omciRespChannel (no receiver)", log.Fields{
				"index": k, "device-id": oo.deviceID})
		}
	}
	oo.mutexMonReq.RUnlock()
}

//GetMaxOmciTimeoutWithRetries provides a timeout value greater than the maximum
//time consumed for retry processing of a particular OMCI-request
func (oo *OmciCC) GetMaxOmciTimeoutWithRetries() time.Duration {
	return time.Duration((CDefaultRetries+1)*oo.pBaseDeviceHandler.GetOmciTimeout() + 1)
}

// SendCreateOrDeleteEthernetFrameExtendedPMME deletes EthernetFrameExtendedPm ME instance
func (oo *OmciCC) SendCreateOrDeleteEthernetFrameExtendedPMME(ctx context.Context, timeout int, highPrio bool,
	upstream bool, create bool, rxChan chan Message, entityID uint16, classID me.ClassID, controlBlock []uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send-ethernet-frame-extended-pm-me-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16), "create": create, "upstream": upstream})

	meParam := me.ParamData{EntityID: entityID,
		Attributes: me.AttributeValueMap{"ControlBlock": controlBlock},
	}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	if classID == me.EthernetFrameExtendedPmClassID {
		meInstance, omciErr = me.NewEthernetFrameExtendedPm(meParam)
	} else {
		meInstance, omciErr = me.NewEthernetFrameExtendedPm64Bit(meParam)
	}

	if omciErr.GetError() == nil {
		var omciLayer *omci.OMCI
		var msgLayer gopacket.SerializableLayer
		var err error
		if create {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.CreateRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = oframe.EncodeFrame(meInstance, omci.DeleteRequestType, oframe.TransactionID(tid),
				oframe.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "cannot-encode-ethernet-frame-extended-pm-me",
				log.Fields{"err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "cannot-serialize-ethernet-frame-extended-pm-me",
				log.Fields{"err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}

		omciRxCallbackPair := CallbackPair{CbKey: tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet-frame-extended-pm-me",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send-ethernet-frame-extended-pm-me-done",
			log.Fields{"device-id": oo.deviceID, "upstream": upstream, "create": create, "inst-id": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "cannot-generate-ethernet-frame-extended-pm-me-instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "upstream": upstream, "create": create, "inst-id": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// RLockMutexMonReq lock read access to monitoredRequests
func (oo *OmciCC) RLockMutexMonReq() {
	oo.mutexMonReq.RLock()
}

// RUnlockMutexMonReq unlock read access to monitoredRequests
func (oo *OmciCC) RUnlockMutexMonReq() {
	oo.mutexMonReq.RUnlock()
}

// GetMonitoredRequest get OmciTransferStructure for an omciTransID
func (oo *OmciCC) GetMonitoredRequest(omciTransID uint16) (value OmciTransferStructure, exist bool) {
	value, exist = oo.monitoredRequests[omciTransID]
	return value, exist
}

// SetChMonitoredRequest sets chSuccess to indicate whether response was received or not
func (oo *OmciCC) SetChMonitoredRequest(omciTransID uint16, chVal bool) {
	oo.monitoredRequests[omciTransID].chSuccess <- chVal
}

// SendSetEthernetFrameExtendedPMME sends the set request for ethernet frame extended type me
func (oo *OmciCC) SendSetEthernetFrameExtendedPMME(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, entityID uint16, classID me.ClassID, controlBlock []uint16) (*me.ManagedEntity, error) {
	tid := oo.GetNextTid(highPrio)
	logger.Debugw(ctx, "send-set-ethernet-frame-extended-pm-me-control-block:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(entityID), 16)})

	meParams := me.ParamData{EntityID: entityID,
		Attributes: me.AttributeValueMap{"ControlBlock": controlBlock},
	}
	var meInstance *me.ManagedEntity
	var omciErr me.OmciErrors
	if classID == me.EthernetFrameExtendedPmClassID {
		meInstance, omciErr = me.NewEthernetFrameExtendedPm(meParams)
	} else {
		meInstance, omciErr = me.NewEthernetFrameExtendedPm64Bit(meParams)
	}

	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := oframe.EncodeFrame(meInstance, omci.SetRequestType, oframe.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "cannot-encode-ethernet-frame-extended-pm-me",
				log.Fields{"err": err, "device-id": oo.deviceID, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "cannot-serialize-ethernet-frame-extended-pm-me-set-msg",
				log.Fields{"err": err, "device-id": oo.deviceID, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		omciRxCallbackPair := CallbackPair{
			CbKey:   tid,
			CbEntry: CallbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.Send(ctx, pkt, timeout, CDefaultRetries, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet-frame-extended-pm-me",
				log.Fields{"Err": err, "device-id": oo.deviceID, "inst-id": strconv.FormatInt(int64(entityID), 16)})
			return nil, err
		}
		logger.Debugw(ctx, "send-ethernet-frame-extended-pm-me-set-msg-done",
			log.Fields{"device-id": oo.deviceID, "inst-id": strconv.FormatInt(int64(entityID), 16)})
		return meInstance, nil
	}
	logger.Errorw(ctx, "cannot-generate-ethernet-frame-extended-pm-me-instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "inst-id": strconv.FormatInt(int64(entityID), 16)})
	return nil, omciErr.GetError()
}

// PrepareForGarbageCollection - remove references to prepare for garbage collection
func (oo *OmciCC) PrepareForGarbageCollection(ctx context.Context, aDeviceID string) {
	logger.Debugw(ctx, "prepare for garbage collection", log.Fields{"device-id": aDeviceID})
	oo.pBaseDeviceHandler = nil
	oo.pOnuDeviceEntry = nil
	oo.pOnuAlarmManager = nil
}
