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

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v4/pkg/adapters/adapterif"

	//"github.com/opencord/voltha-lib-go/v4/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v4/pkg/log"
	ic "github.com/opencord/voltha-protos/v4/go/inter_container"
	//"github.com/opencord/voltha-protos/v4/go/openflow_13"
	//"github.com/opencord/voltha-protos/v4/go/voltha"
)

// ### OMCI related definitions - retrieved from Python adapter code/trace ####

const galEthernetEID = uint16(1)
const maxGemPayloadSize = uint16(48)
const connectivityModeValue = uint8(5)

//const defaultTPID = uint16(0x8100)
//const broadComDefaultVID = uint16(4091)
const macBridgeServiceProfileEID = uint16(0x201) // TODO: most all these need better definition or tuning
const ieeeMapperServiceProfileEID = uint16(0x8001)
const macBridgePortAniEID = uint16(0x2102)

const unusedTcontAllocID = uint16(0xFFFF) //common unused AllocId for G.984 and G.987 systems

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

// ### OMCI related definitions - end

//callbackPairEntry to be used for OMCI send/receive correlation
type callbackPairEntry struct {
	cbRespChannel chan Message
	cbFunction    func(context.Context, *omci.OMCI, *gp.Packet, chan Message) error
	framePrint    bool //true for printing
}

//callbackPair to be used for ReceiveCallback init
type callbackPair struct {
	cbKey   uint16
	cbEntry callbackPairEntry
}

type omciTransferStructure struct {
	txFrame        []byte
	timeout        int
	retry          int
	highPrio       bool
	withFramePrint bool
}

//omciCC structure holds information needed for OMCI communication (to/from OLT Adapter)
type omciCC struct {
	enabled            bool
	pOnuDeviceEntry    *OnuDeviceEntry
	deviceID           string
	pBaseDeviceHandler *deviceHandler
	coreProxy          adapterif.CoreProxy
	adapterProxy       adapterif.AdapterProxy
	supportExtMsg      bool
	rxOmciFrameError   tOmciReceiveError

	txFrames, txOnuFrames                uint32
	rxFrames, rxOnuFrames, rxOnuDiscards uint32

	// OMCI params
	mutexTid       sync.Mutex
	tid            uint16
	mutexHpTid     sync.Mutex
	hpTid          uint16
	uploadSequNo   uint16
	uploadNoOfCmds uint16

	mutexTxQueue      sync.Mutex
	txQueue           *list.List
	mutexRxSchedMap   sync.Mutex
	rxSchedulerMap    map[uint16]callbackPairEntry
	pLastTxMeInstance *me.ManagedEntity
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

//newOmciCC constructor returns a new instance of a OmciCC
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newOmciCC(ctx context.Context, onuDeviceEntry *OnuDeviceEntry,
	deviceID string, deviceHandler *deviceHandler,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy) *omciCC {
	logger.Debugw(ctx, "init-omciCC", log.Fields{"device-id": deviceID})
	var omciCC omciCC
	omciCC.enabled = false
	omciCC.pOnuDeviceEntry = onuDeviceEntry
	omciCC.deviceID = deviceID
	omciCC.pBaseDeviceHandler = deviceHandler
	omciCC.coreProxy = coreProxy
	omciCC.adapterProxy = adapterProxy
	omciCC.supportExtMsg = false
	omciCC.rxOmciFrameError = cOmciMessageReceiveNoError
	omciCC.txFrames = 0
	omciCC.txOnuFrames = 0
	omciCC.rxFrames = 0
	omciCC.rxOnuFrames = 0
	omciCC.rxOnuDiscards = 0
	omciCC.tid = 0x1
	omciCC.hpTid = 0x8000
	omciCC.uploadSequNo = 0
	omciCC.uploadNoOfCmds = 0
	omciCC.txQueue = list.New()
	omciCC.rxSchedulerMap = make(map[uint16]callbackPairEntry)

	return &omciCC
}

//stop stops/resets the omciCC
func (oo *omciCC) stop(ctx context.Context) error {
	logger.Debugw(ctx, "omciCC-stopping", log.Fields{"device-id": oo.deviceID})
	//reseting all internal data, which might also be helpful for discarding any lingering tx/rx requests
	oo.mutexTxQueue.Lock()
	oo.txQueue.Init() // clear the tx queue
	oo.mutexTxQueue.Unlock()
	oo.mutexRxSchedMap.Lock()
	for k := range oo.rxSchedulerMap {
		delete(oo.rxSchedulerMap, k) //clear the scheduler map
	}
	oo.mutexRxSchedMap.Unlock()
	oo.mutexHpTid.Lock()
	oo.hpTid = 0x8000 //reset the high prio transactionId
	oo.mutexHpTid.Unlock()
	oo.mutexTid.Lock()
	oo.tid = 1 //reset the low prio transactionId
	oo.mutexTid.Unlock()
	//reset control values
	oo.uploadSequNo = 0
	oo.uploadNoOfCmds = 0
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
func (oo *omciCC) receiveOnuMessage(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet) error {
	logger.Debugw(ctx, "rx-onu-autonomous-message", log.Fields{"omciMsgType": omciMsg.MessageType,
		"payload": hex.EncodeToString(omciMsg.Payload)})
	switch omciMsg.MessageType {
	case omci.AlarmNotificationType:
		data := OmciMessage{
			OmciMsg:    omciMsg,
			OmciPacket: packet,
		}
		go oo.pBaseDeviceHandler.pAlarmMgr.handleOmciAlarmNotificationMessage(ctx, data)
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

func (oo *omciCC) printRxMessage(ctx context.Context, rxMsg []byte) {
	//assuming omci message content is hex coded!
	// with restricted output of 16bytes would be ...rxMsg[:16]
	logger.Debugw(ctx, "omci-message-received:", log.Fields{
		"RxOmciMessage": hex.EncodeToString(rxMsg),
		"device-id":     oo.deviceID})
}

// Rx handler for onu messages
//    e.g. would call ReceiveOnuMessage() in case of TID=0 or Action=test ...
func (oo *omciCC) receiveMessage(ctx context.Context, rxMsg []byte) error {
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
	omciMsg, ok := omciLayer.(*omci.OMCI)
	if !ok {
		logger.Errorw(ctx, "omci-message could not assign omci layer", log.Fields{"device-id": oo.deviceID})
		oo.printRxMessage(ctx, rxMsg)
		return fmt.Errorf("could not assign omci layer %s", oo.deviceID)
	}
	logger.Debugw(ctx, "omci-message-decoded:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "DeviceIdent": omciMsg.DeviceIdentifier})
	if byte(omciMsg.MessageType)&me.AK == 0 {
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
	if ok && rxCallbackEntry.cbFunction != nil {
		if rxCallbackEntry.framePrint {
			oo.printRxMessage(ctx, rxMsg)
		}
		//disadvantage of decoupling: error verification made difficult, but anyway the question is
		// how to react on erroneous frame reception, maybe can simply be ignored
		go rxCallbackEntry.cbFunction(ctx, omciMsg, &packet, rxCallbackEntry.cbRespChannel)
		if isSuccessfulResponseWithMibDataSync(omciMsg, &packet) {
			oo.pOnuDeviceEntry.incrementMibDataSync(ctx)
		}

		// having posted the response the request is regarded as 'done'
		delete(oo.rxSchedulerMap, omciMsg.TransactionID)
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

//Queue the OMCI Frame for a transmit to the ONU via the proxy_channel
func (oo *omciCC) send(ctx context.Context, txFrame []byte, timeout int, retry int, highPrio bool,
	receiveCallbackPair callbackPair) error {

	logger.Debugw(ctx, "register-response-callback:", log.Fields{"for TansCorrId": receiveCallbackPair.cbKey})
	// it could be checked, if the callback keay is already registered - but simply overwrite may be acceptable ...
	oo.mutexRxSchedMap.Lock()
	oo.rxSchedulerMap[receiveCallbackPair.cbKey] = receiveCallbackPair.cbEntry
	printFrame := receiveCallbackPair.cbEntry.framePrint //printFrame true means debug print of frame is requested
	oo.mutexRxSchedMap.Unlock()

	//just use a simple list for starting - might need some more effort, especially for multi source write access
	omciTxRequest := omciTransferStructure{
		txFrame,
		timeout,
		retry,
		highPrio,
		printFrame,
	}
	oo.mutexTxQueue.Lock()
	oo.txQueue.PushBack(omciTxRequest) // enqueue
	oo.mutexTxQueue.Unlock()

	// for first test just bypass and send directly:
	go oo.sendNextRequest(ctx)
	return nil
}

//Pull next tx request and send it
func (oo *omciCC) sendNextRequest(ctx context.Context) error {
	//	return errors.New("sendNextRequest unimplemented")

	// just try to get something transferred !!
	// avoid accessing the txQueue from parallel send requests
	// block parallel omci send requests at least until SendIAP is 'committed'
	// that should be feasible for an onu instance as on OMCI anyway window size 1 is assumed
	oo.mutexTxQueue.Lock()
	defer oo.mutexTxQueue.Unlock()
	for oo.txQueue.Len() > 0 {
		queueElement := oo.txQueue.Front() // First element
		omciTxRequest := queueElement.Value.(omciTransferStructure)
		/* compare olt device handler code:
		func (dh *DeviceHandler) omciIndication(omciInd *oop.OmciIndication) {
			logger.Debugw(ctx,"omci indication", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
			var deviceType string
			var deviceID string
			var proxyDeviceID string

			onuKey := dh.formOnuKey(omciInd.IntfId, omciInd.OnuId)

			if onuInCache, ok := dh.onus.Load(onuKey); !ok {

				logger.Debugw(ctx,"omci indication for a device not in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
				ponPort := IntfIDToPortNo(omciInd.GetIntfId(), voltha.Port_PON_OLT)
				kwargs := make(map[string]interface{})
				kwargs["onu_id"] = omciInd.OnuId
				kwargs["parent_port_no"] = ponPort

				onuDevice, err := dh.coreProxy.GetChildDevice(log.WithSpanFromContext(context.TODO(), ctx), dh.device.Id, kwargs)
				if err != nil {
					logger.Errorw(ctx,"onu not found", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId, "error": err})
					return
				}
				deviceType = onuDevice.Type
				deviceID = onuDevice.Id
				proxyDeviceID = onuDevice.ProxyAddress.DeviceId
				//if not exist in cache, then add to cache.
				dh.onus.Store(onuKey, NewOnuDevice(deviceID, deviceType, onuDevice.SerialNumber, omciInd.OnuId, omciInd.IntfId, proxyDeviceID))
			} else {
				//found in cache
				logger.Debugw(ctx,"omci indication for a device in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
				deviceType = onuInCache.(*OnuDevice).deviceType
				deviceID = onuInCache.(*OnuDevice).deviceID
				proxyDeviceID = onuInCache.(*OnuDevice).proxyDeviceID
			}
		*/
		/* and compare onu_adapter py code:
		omci_msg = InterAdapterOmciMessage(
			message=bytes(frame),
			proxy_address=self._proxy_address,
			connect_status=self._device.connect_status)

		self.logger.debug('sent-omci-msg', tid=tx_tid, omci_msg=hexlify(bytes(frame)))

		yield self._adapter_proxy.send_inter_adapter_message(
			msg=omci_msg,
			type=InterAdapterMessageType.OMCI_REQUEST,
			from_adapter=self._device.type,
			to_adapter=self._proxy_address.device_type,
			to_device_id=self._device_id,
			proxy_device_id=self._proxy_address.device_id
		)
		*/
		device, err := oo.coreProxy.GetDevice(ctx,
			oo.pBaseDeviceHandler.deviceID, oo.deviceID) //parent, child
		if err != nil || device == nil {
			/*TODO: needs to handle error scenarios */
			logger.Errorw(ctx, "Failed to fetch device", log.Fields{"err": err, "ParentId": oo.pBaseDeviceHandler.deviceID,
				"ChildId": oo.deviceID})
			return fmt.Errorf("failed to fetch device %s", oo.deviceID)
		}

		if omciTxRequest.withFramePrint {
			logger.Debugw(ctx, "omci-message-to-send:", log.Fields{
				"TxOmciMessage": hex.EncodeToString(omciTxRequest.txFrame),
				"device-id":     oo.deviceID,
				"toDeviceType":  oo.pBaseDeviceHandler.ProxyAddressType,
				"proxyDeviceID": oo.pBaseDeviceHandler.ProxyAddressID})
		}
		omciMsg := &ic.InterAdapterOmciMessage{Message: omciTxRequest.txFrame}
		if sendErr := oo.adapterProxy.SendInterAdapterMessage(log.WithSpanFromContext(context.Background(), ctx), omciMsg,
			ic.InterAdapterMessageType_OMCI_REQUEST,
			//fromTopic,toType,toDevId, ProxyDevId
			oo.pOnuDeviceEntry.baseDeviceHandler.pOpenOnuAc.config.Topic, oo.pBaseDeviceHandler.ProxyAddressType,
			oo.deviceID, oo.pBaseDeviceHandler.ProxyAddressID, ""); sendErr != nil {
			logger.Errorw(ctx, "send omci request error", log.Fields{"ChildId": oo.deviceID, "error": sendErr})
			return sendErr
		}
		oo.txQueue.Remove(queueElement) // Dequeue
	}
	return nil
}

func (oo *omciCC) getNextTid(highPriority bool) uint16 {
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
func serialize(ctx context.Context, msgType omci.MessageType, request gopacket.SerializableLayer, tid uint16) ([]byte, error) {
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
func (oo *omciCC) receiveOmciResponse(ctx context.Context, omciMsg *omci.OMCI, packet *gp.Packet, respChan chan Message) error {

	logger.Debugw(ctx, "omci-message-response - transfer on omciRespChannel", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "device-id": oo.deviceID})

	if oo.pOnuDeviceEntry == nil {
		logger.Errorw(ctx, "Abort receiving OMCI response, DeviceEntryPointer is nil", log.Fields{
			"device-id": oo.deviceID})
		return fmt.Errorf("deviceEntryPointer is nil %s", oo.deviceID)
	}

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

func (oo *omciCC) sendMibReset(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw(ctx, "send MibReset-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibResetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.MibResetRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibResetRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse, true},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendReboot(ctx context.Context, timeout int, highPrio bool, responseChannel chan Message) error {
	logger.Debugw(ctx, "send Reboot-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.RebootRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuGClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.RebootRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{oo.pOnuDeviceEntry.omciRebootMessageReceivedChannel, oo.receiveOmciResponse, true},
	}

	err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	err = oo.pOnuDeviceEntry.waitForRebootResponse(ctx, responseChannel)
	if err != nil {
		logger.Errorw(ctx, "aborting ONU Reboot!", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	return nil
}

func (oo *omciCC) sendMibUpload(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send MibUpload-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibUploadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.MibUploadRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibUploadRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.uploadSequNo = 0
	oo.uploadNoOfCmds = 0

	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse, true},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendMibUploadNext(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send MibUploadNext-msg to:", log.Fields{"device-id": oo.deviceID, "uploadSequNo": oo.uploadSequNo})
	request := &omci.MibUploadNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: oo.uploadSequNo,
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.MibUploadNextRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize MibUploadNextRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.uploadSequNo++

	omciRxCallbackPair := callbackPair{
		cbKey: tid,
		//frame printing for MibUpload frames disabled now per default to avoid log file abort situations (size/speed?)
		// if wanted, rx frame printing should be specifically done within the MibUpload FSM or controlled via extra parameter
		// compare also software upgrade download section handling
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse, false},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendGetAllAlarm(ctx context.Context, alarmRetreivalMode uint8, timeout int, highPrio bool) error {
	logger.Debugw(ctx, "send GetAllAlarms-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.GetAllAlarmsRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		AlarmRetrievalMode: byte(alarmRetreivalMode),
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.GetAllAlarmsRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize GetAllAlarmsRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.pBaseDeviceHandler.pAlarmMgr.alarmUploadSeqNo = 0
	oo.pBaseDeviceHandler.pAlarmMgr.alarmUploadNoOfCmds = 0

	omciRxCallbackPair := callbackPair{
		cbKey: tid,
		cbEntry: callbackPairEntry{(*oo.pBaseDeviceHandler.pAlarmMgr).eventChannel,
			oo.receiveOmciResponse, true},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendGetAllAlarmNext(ctx context.Context, timeout int, highPrio bool) error {
	alarmUploadSeqNo := oo.pBaseDeviceHandler.pAlarmMgr.alarmUploadSeqNo
	logger.Debugw(ctx, "send sendGetAllAlarmNext-msg to:", log.Fields{"device-id": oo.deviceID,
		"alarmUploadSeqNo": alarmUploadSeqNo})
	request := &omci.GetAllAlarmsNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: alarmUploadSeqNo,
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(ctx, omci.GetAllAlarmsNextRequestType, request, tid)
	if err != nil {
		logger.Errorw(ctx, "Cannot serialize GetAllAlarmsNextRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.pBaseDeviceHandler.pAlarmMgr.alarmUploadSeqNo++

	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pBaseDeviceHandler.pAlarmMgr).eventChannel, oo.receiveOmciResponse, true},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendCreateGalEthernetProfile(ctx context.Context, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send GalEnetProfile-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	meParams := me.ParamData{
		EntityID:   galEthernetEID,
		Attributes: me.AttributeValueMap{"MaximumGemPayloadSize": maxGemPayloadSize},
	}
	meInstance, omciErr := me.NewGalEthernetProfile(meParams)
	if omciErr.GetError() == nil {
		//all setByCreate parameters already set, no default option required ...
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GalEnetProfileInstance for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send GalEnetProfile-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate GalEnetProfileInstance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

// might be needed to extend for parameter arguments, here just for setting the ConnectivityMode!!
func (oo *omciCC) sendSetOnu2g(ctx context.Context, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ONU2-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send ONU2-G-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate ONU2-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBServiceProfile(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	instID := macBridgeServiceProfileEID + uint16(aPUniPort.macBpNo)
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
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBSP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MBSP-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MBSP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBPConfigData(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	instID := macBridgePortAniEID + aPUniPort.entityID
	logger.Debugw(ctx, "send MBPCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16)})

	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"BridgeIdPointer": macBridgeServiceProfileEID + uint16(aPUniPort.macBpNo),
			"PortNum":         aPUniPort.macBpNo,
			"TpType":          uint8(aPUniPort.portType),
			"TpPointer":       aPUniPort.entityID,
		},
	}
	meInstance, omciErr := me.NewMacBridgePortConfigurationData(meParams)
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MBPCD-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateEVTOConfigData(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	//same entityId is used as for MBSP (see there), but just arbitrary ...
	instID := macBridgeServiceProfileEID + uint16(aPUniPort.macBpNo)
	logger.Debugw(ctx, "send EVTOCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16)})

	// compare python adapter code WA VOL-1311: this is not done here!
	//   (setting TPID values for the create would probably anyway be ignored by the omci lib)
	//    but perhaps we have to be aware of possible problems at get(Next) Request handling for EVTOOCD tables later ...
	assType := uint8(2) // default AssociationType is PPTPEthUni
	if aPUniPort.portType == uniVEIP {
		assType = uint8(10) // for VEIP
	}
	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"AssociationType":     assType,
			"AssociatedMePointer": aPUniPort.entityID,
		},
	}
	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(meParams)
	if omciErr.GetError() == nil {
		//all setByCreate parameters already set, no default option required ...
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send EVTOCD-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetOnuGLS(ctx context.Context, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send ONU-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// ONU-G ME-ID is defined to be 0, no need to perform a DB lookup
	meParams := me.ParamData{
		EntityID:   0,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewOnuG(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ONU-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send ONU-G-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate ONU-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetPptpEthUniLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send PPTPEthUni-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// PPTPEthUni ME-ID is taken from Mib Upload stored OnuUniPort instance (argument)
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewPhysicalPathTerminationPointEthernetUni(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode PPTPEthUni instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize PPTPEthUni-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send PPTPEthUni-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send PPTPEthUni-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate PPTPEthUni", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

/* UniG obsolete by now, left here in case it should be needed once again
   UniG AdminState anyway should be ignored by ONU acc. to G988
func (oo *omciCC) sendSetUniGLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx,"send UNI-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// UNI-G ME-ID is taken from Mib Upload stored OnuUniPort instance (argument)
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewUniG(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
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

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
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

func (oo *omciCC) sendSetVeipLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send VEIP-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	// ONU-G ME-ID is defined to be 0, no need to perform a DB lookup
	meParams := me.ParamData{
		EntityID:   aInstNo,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.NewVirtualEthernetInterfacePoint(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VEIP instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send VEIP-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate VEIP", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendGetMe(ctx context.Context, classID me.ClassID, entityID uint16, requestedAttributes me.AttributeValueMap,
	timeout int, highPrio bool, rxChan chan Message) *me.ManagedEntity {

	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send get-request-msg", log.Fields{"classID": classID, "device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16)})

	meParams := me.ParamData{
		EntityID:   entityID,
		Attributes: requestedAttributes,
	}
	meInstance, omciErr := me.LoadManagedEntityDefinition(classID, meParams)
	if omciErr.GetError() == nil {
		meClassIDName := meInstance.GetName()
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.GetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorf(ctx, "Cannot encode instance for get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send get-request-msg", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debugw(ctx, "send get-request-msg done", log.Fields{"meClassIDName": meClassIDName, "device-id": oo.deviceID})
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate meDefinition", log.Fields{"classID": classID, "Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateDot1PMapper(ctx context.Context, timeout int, highPrio bool,
	aInstID uint16, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode .1pMapper for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send .1pMapper-create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate .1pMapper", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBPConfigDataVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MBPCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMacBridgePortConfigurationData(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MBPCD-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateGemNCTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send GemNCTP-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemPortNetworkCtp(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemNCTP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send GemNCTP-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate GemNCTP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send GemIwTp-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemIwTp for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send GemIwTp-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate GemIwTp Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetTcontVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send TCont-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTCont(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TCont for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send TCont-set msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate TCont Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetPrioQueueVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send PrioQueue-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewPriorityQueue(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode PrioQueue for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send PrioQueue-set msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate PrioQueue Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetDot1PMapperVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send 1PMapper-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode 1PMapper for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send 1PMapper-set msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate 1PMapper Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateVtfdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVlanTaggingFilterData(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send VTFD-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

// nolint: unused
func (oo *omciCC) sendSetVtfdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVlanTaggingFilterData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send VTFD-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateEvtocdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send EVTOCD-set msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetEvtocdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send EVTOCD-set msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendDeleteEvtocd(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send EVTOCD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode EVTOCD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize EVTOCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send EVTOCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send EVTOCD-delete msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendDeleteVtfd(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send VTFD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewVlanTaggingFilterData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode VTFD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize VTFD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send VTFD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send VTFD-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate VTFD Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

// nolint: unused
func (oo *omciCC) sendCreateTDVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})
	meInstance, omciErr := me.NewTrafficDescriptor(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send TD-Create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

// nolint: unused
func (oo *omciCC) sendSetTDVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTrafficDescriptor(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send TD-Set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil

}

// nolint: unused
func (oo *omciCC) sendDeleteTD(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send TD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewTrafficDescriptor(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode TD for delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize TD delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send TD delete", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send TD-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate TD Instance", log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil

}

func (oo *omciCC) sendDeleteGemIWTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send GemIwTp-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewGemInterworkingTerminationPoint(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemIwTp for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemIwTp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send GemIwTp-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate GemIwTp Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendDeleteGemNCTP(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send GemNCtp-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewGemPortNetworkCtp(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode GemNCtp for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize GemNCtp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send GemNCtp delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send GemNCtp-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate GemNCtp Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendDeleteDot1PMapper(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send .1pMapper-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode .1pMapper for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize .1pMapper delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send .1pMapper delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send .1pMapper-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate .1pMapper Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendDeleteMBPConfigData(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aInstID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MBPCD-Delete-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{EntityID: aInstID}
	meInstance, omciErr := me.NewMacBridgePortConfigurationData(meParams)
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.DeleteRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MBPCD for delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MBPCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MBPCD delete", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MBPCD-Delete-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MBPCD Instance for delete", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMulticastGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastGemIWTP-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
			omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastGEMIWTP for create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MulticastGEMIWTP-create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MulticastGEMIWTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetMulticastGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastGemIWTP-set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid),
			omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastGEMIWTP for set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastGEMIWTP create", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastGEMIWTP set", log.Fields{"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MulticastGEMIWTP-set-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MulticastGEMIWTP Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMulticastOperationProfileVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastOperationProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastOperationsProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
			omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastOperationProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MulticastOperationProfile-create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MulticastOperationProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetMulticastOperationProfileVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastOperationProfile-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastOperationsProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid),
			omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastOperationProfile for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastOperationProfile create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MulticastOperationProfile-create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MulticastOperationProfile Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMulticastSubConfigInfoVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send MulticastSubConfigInfo-create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMulticastSubscriberConfigInfo(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
			omci.AddDefaults(true))
		if err != nil {
			logger.Errorw(ctx, "Cannot encode MulticastSubConfigInfo for create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize MulticastSubConfigInfo create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send MulticastSubConfigInfo create", log.Fields{"Err": err,
				"device-id": oo.deviceID})
			return nil
		}
		logger.Debug(ctx, "send MulticastSubConfigInfo-create-msg done")
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate MulticastSubConfigInfo Instance", log.Fields{"Err": omciErr.GetError(),
		"device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSyncTime(ctx context.Context, timeout int, highPrio bool, rxChan chan Message) error {
	tid := oo.getNextTid(highPrio)
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send synchronize time request", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send synchronize time request done")
	return nil
}

func (oo *omciCC) sendCreateOrDeleteEthernetPerformanceMonitoringHistoryME(ctx context.Context, timeout int, highPrio bool,
	upstream bool, create bool, rxChan chan Message, entityID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet frame performance monitoring history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}
		logger.Debugw(ctx, "send ethernet frame performance monitoring history data ME done",
			log.Fields{"device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate ethernet frame performance monitoring history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "upstream": upstream, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil
}

func (oo *omciCC) sendCreateOrDeleteEthernetUniHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send ethernet uni history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}
		logger.Debugw(ctx, "send ethernet uni history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate ethernet uni history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil
}

func (oo *omciCC) sendCreateOrDeleteFecHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send fec history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}
		logger.Debugw(ctx, "send fec history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate fec history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil
}

func (oo *omciCC) sendCreateOrDeleteGemPortHistoryME(ctx context.Context, timeout int, highPrio bool,
	create bool, rxChan chan Message, entityID uint16) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
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
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.CreateRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		} else {
			omciLayer, msgLayer, err = omci.EncodeFrame(meInstance, omci.DeleteRequestType, omci.TransactionID(tid),
				omci.AddDefaults(true))
		}
		if err != nil {
			logger.Errorw(ctx, "Cannot encode gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		pkt, err := serializeOmciLayer(ctx, omciLayer, msgLayer)
		if err != nil {
			logger.Errorw(ctx, "Cannot serialize gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}

		omciRxCallbackPair := callbackPair{cbKey: tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw(ctx, "Cannot send gemport history data ME",
				log.Fields{"Err": err, "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
			return nil
		}
		logger.Debugw(ctx, "send gemport history data ME done",
			log.Fields{"device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
		return meInstance
	}
	logger.Errorw(ctx, "Cannot generate gemport history data ME Instance",
		log.Fields{"Err": omciErr.GetError(), "device-id": oo.deviceID, "create": create, "InstId": strconv.FormatInt(int64(entityID), 16)})
	return nil
}

func (oo *omciCC) sendStartSoftwareDownload(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aDownloadWindowSize uint8, aFileLen uint32) error {
	tid := oo.getNextTid(highPrio)
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send StartSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send StartSwDlRequest done")
	return nil
}

func (oo *omciCC) sendDownloadSection(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aAckRequest uint8, aDownloadSectionNo uint8, aSection []byte, aPrint bool) error {
	tid := oo.getNextTid(highPrio)
	logger.Debugw(ctx, "send DlSectionRequest:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(aImageMeID), 16), "omci-ack": aAckRequest})

	//TODO!!!: don't know by now on how to generate the possibly needed AR (or enforce it to 0) with current omci-lib
	//    by now just try to send it as defined by omci-lib
	msgType := omci.DownloadSectionRequestType
	if aAckRequest > 0 {
		msgType = omci.DownloadSectionRequestWithResponseType
	}
	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   msgType,
		// DeviceIdentifier: omci.BaselineIdent,		// Optional, defaults to Baseline
		// Length:           0x28,						// Optional, defaults to 40 octets
	}
	var localSectionData [31]byte
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, printFrame /*aPrint*/},
	}
	err = oo.send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send DlSectionRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send DlSectionRequest done")
	return nil
}

func (oo *omciCC) sendEndSoftwareDownload(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16, aFileLen uint32, aImageCrc uint32) error {
	tid := oo.getNextTid(highPrio)
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send EndSwDlRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send EndSwDlRequest done")
	return nil
}

func (oo *omciCC) sendActivateSoftware(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16) error {
	tid := oo.getNextTid(highPrio)
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send ActivateSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send ActivateSwRequest done")
	return nil
}

func (oo *omciCC) sendCommitSoftware(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, aImageMeID uint16) error {
	tid := oo.getNextTid(highPrio)
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

	omciRxCallbackPair := callbackPair{cbKey: tid,
		cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse, true},
	}
	err = oo.send(ctx, outgoingPacket, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw(ctx, "Cannot send CommitSwRequest", log.Fields{"Err": err,
			"device-id": oo.deviceID})
		return err
	}
	logger.Debug(ctx, "send CommitSwRequest done")
	return nil
}

func isSuccessfulResponseWithMibDataSync(omciMsg *omci.OMCI, packet *gp.Packet) bool {
	for _, v := range responsesWithMibDataSync {
		if v == omciMsg.MessageType {
			nextLayer, _ := omci.MsgTypeToNextLayer(v)
			msgLayer := (*packet).Layer(nextLayer)
			switch nextLayer {
			case omci.LayerTypeCreateResponse:
				if msgLayer.(*omci.CreateResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeDeleteResponse:
				if msgLayer.(*omci.DeleteResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeSetResponse:
				if msgLayer.(*omci.SetResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeStartSoftwareDownloadResponse:
				if msgLayer.(*omci.StartSoftwareDownloadResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeEndSoftwareDownloadResponse:
				if msgLayer.(*omci.EndSoftwareDownloadResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeActivateSoftwareResponse:
				if msgLayer.(*omci.ActivateSoftwareResponse).Result == me.Success {
					return true
				}
			case omci.LayerTypeCommitSoftwareResponse:
				if msgLayer.(*omci.CommitSoftwareResponse).Result == me.Success {
					return true
				}
			}
		}
	}
	return false
}
