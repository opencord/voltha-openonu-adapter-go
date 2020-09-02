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
	"errors"
	"strconv"
	"sync"

	//"time"

	"github.com/google/gopacket"
	// TODO!!! Some references could be resolved auto, but some need specific context ....
	gp "github.com/google/gopacket"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

// ### OMCI related definitions - retrieved from Python adapter code/trace ####

//ConstDefaultOmciTimeout - Default OMCI Timeout
const ConstDefaultOmciTimeout = 10 // ( 3 ?) Seconds

const galEthernetEID = uint16(1)
const maxGemPayloadSize = uint16(48)
const connectivityModeValue = uint8(5)

//const defaultTPID = uint16(0x8100)
//const broadComDefaultVID = uint16(4091)
const macBridgeServiceProfileEID = uint16(0x201) // TODO: most all these need better definition or tuning
const ieeeMapperServiceProfileEID = uint16(0x8001)
const macBridgePortAniEID = uint16(0x2102)

// ### OMCI related definitions - end

//callbackPairEntry to be used for OMCI send/receive correlation
type callbackPairEntry struct {
	cbRespChannel chan Message
	cbFunction    func(*omci.OMCI, *gp.Packet, chan Message) error
}

//callbackPair to be used for ReceiveCallback init
type callbackPair struct {
	cbKey   uint16
	cbEntry callbackPairEntry
}

type omciTransferStructure struct {
	txFrame  []byte
	timeout  int
	retry    int
	highPrio bool
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
	//txRequest
	//rxResponse
	//pendingRequest
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

//newOmciCC constructor returns a new instance of a OmciCC
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newOmciCC(ctx context.Context, onuDeviceEntry *OnuDeviceEntry,
	deviceID string, deviceHandler *deviceHandler,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy) *omciCC {
	logger.Infow("init-omciCC", log.Fields{"device-id": deviceID})
	var omciCC omciCC
	omciCC.enabled = false
	omciCC.pOnuDeviceEntry = onuDeviceEntry
	omciCC.deviceID = deviceID
	omciCC.pBaseDeviceHandler = deviceHandler
	omciCC.coreProxy = coreProxy
	omciCC.adapterProxy = adapterProxy
	omciCC.supportExtMsg = false
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

// Rx handler for omci messages
func (oo *omciCC) receiveOnuMessage(ctx context.Context, omciMsg *omci.OMCI) error {
	logger.Debugw("rx-onu-autonomous-message", log.Fields{"omciMsgType": omciMsg.MessageType,
		"payload": hex.EncodeToString(omciMsg.Payload)})
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
					logger.Errorw("rx-onu-unsupported-autonomous-message", log.Fields{"msgType": msgType})
					rxOnuDiscards++
					return errors.New("RxOnuMsgType unimplemented")
				}
		    }
	*/
	return errors.New("receiveOnuMessage unimplemented")
}

// Rx handler for onu messages
//    e.g. would call ReceiveOnuMessage() in case of TID=0 or Action=test ...
func (oo *omciCC) receiveMessage(ctx context.Context, rxMsg []byte) error {
	//logger.Debugw("cc-receive-omci-message", log.Fields{"RxOmciMessage-x2s": hex.EncodeToString(rxMsg)})
	if len(rxMsg) >= 44 { // then it should normally include the BaseFormat trailer Len
		// NOTE: autocorrection only valid for OmciBaseFormat, which is not specifically verified here!!!
		//  (am extendedFormat message could be destroyed this way!)
		trailerLenData := rxMsg[42:44]
		trailerLen := binary.BigEndian.Uint16(trailerLenData)
		//logger.Debugw("omci-received-trailer-len", log.Fields{"Length": trailerLen})
		if trailerLen != 40 { // invalid base Format entry -> autocorrect
			binary.BigEndian.PutUint16(rxMsg[42:44], 40)
			logger.Debug("cc-corrected-omci-message: trailer len inserted")
		}
	} else {
		logger.Errorw("received omci-message too small for OmciBaseFormat - abort", log.Fields{"Length": len(rxMsg)})
		return errors.New("rxOmciMessage too small for BaseFormat")
	}

	packet := gopacket.NewPacket(rxMsg, omci.LayerTypeOMCI, gopacket.NoCopy)
	if packet == nil {
		logger.Error("omci-message could not be decoded")
		return errors.New("could not decode rxMsg as OMCI")
	}
	omciLayer := packet.Layer(omci.LayerTypeOMCI)
	if omciLayer == nil {
		logger.Error("omci-message could not decode omci layer")
		return errors.New("could not decode omci layer")
	}
	omciMsg, ok := omciLayer.(*omci.OMCI)
	if !ok {
		logger.Error("omci-message could not assign omci layer")
		return errors.New("could not assign omci layer")
	}
	logger.Debugw("omci-message-decoded:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "DeviceIdent": omciMsg.DeviceIdentifier})
	if byte(omciMsg.MessageType) & ^me.AK == 0 {
		// Not a response
		logger.Debug("RxMsg is no Omci Response Message")
		if omciMsg.TransactionID == 0 {
			return oo.receiveOnuMessage(ctx, omciMsg)
		}
		logger.Errorw("Unexpected TransCorrId != 0  not accepted for autonomous messages",
			log.Fields{"msgType": omciMsg.MessageType, "payload": hex.EncodeToString(omciMsg.Payload)})
		return errors.New("autonomous Omci Message with TranSCorrId != 0 not acccepted")

	}
	//logger.Debug("RxMsg is a Omci Response Message: try to schedule it to the requester")
	oo.mutexRxSchedMap.Lock()
	rxCallbackEntry, ok := oo.rxSchedulerMap[omciMsg.TransactionID]
	if ok && rxCallbackEntry.cbFunction != nil {
		//disadvantage of decoupling: error verification made difficult, but anyway the question is
		// how to react on erroneous frame reception, maybe can simply be ignored
		go rxCallbackEntry.cbFunction(omciMsg, &packet, rxCallbackEntry.cbRespChannel)
		// having posted the response the request is regarded as 'done'
		delete(oo.rxSchedulerMap, omciMsg.TransactionID)
		oo.mutexRxSchedMap.Unlock()
	} else {
		oo.mutexRxSchedMap.Unlock()
		logger.Error("omci-message-response for not registered transCorrId")
		return errors.New("could not find registered response handler tor transCorrId")
	}

	return nil
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

	logger.Debugw("register-response-callback:", log.Fields{"for TansCorrId": receiveCallbackPair.cbKey})
	// it could be checked, if the callback keay is already registered - but simply overwrite may be acceptable ...
	oo.mutexRxSchedMap.Lock()
	oo.rxSchedulerMap[receiveCallbackPair.cbKey] = receiveCallbackPair.cbEntry
	oo.mutexRxSchedMap.Unlock()

	//just use a simple list for starting - might need some more effort, especially for multi source write access
	omciTxRequest := omciTransferStructure{
		txFrame,
		timeout,
		retry,
		highPrio,
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
			logger.Debugw("omci indication", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
			var deviceType string
			var deviceID string
			var proxyDeviceID string

			onuKey := dh.formOnuKey(omciInd.IntfId, omciInd.OnuId)

			if onuInCache, ok := dh.onus.Load(onuKey); !ok {

				logger.Debugw("omci indication for a device not in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
				ponPort := IntfIDToPortNo(omciInd.GetIntfId(), voltha.Port_PON_OLT)
				kwargs := make(map[string]interface{})
				kwargs["onu_id"] = omciInd.OnuId
				kwargs["parent_port_no"] = ponPort

				onuDevice, err := dh.coreProxy.GetChildDevice(context.TODO(), dh.device.Id, kwargs)
				if err != nil {
					logger.Errorw("onu not found", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId, "error": err})
					return
				}
				deviceType = onuDevice.Type
				deviceID = onuDevice.Id
				proxyDeviceID = onuDevice.ProxyAddress.DeviceId
				//if not exist in cache, then add to cache.
				dh.onus.Store(onuKey, NewOnuDevice(deviceID, deviceType, onuDevice.SerialNumber, omciInd.OnuId, omciInd.IntfId, proxyDeviceID))
			} else {
				//found in cache
				logger.Debugw("omci indication for a device in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
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
			logger.Errorw("Failed to fetch device", log.Fields{"err": err, "ParentId": oo.pBaseDeviceHandler.deviceID,
				"ChildId": oo.deviceID})
			return errors.New("failed to fetch device")
		}

		logger.Debugw("omci-message-sending", log.Fields{"fromDeviceType": oo.pBaseDeviceHandler.DeviceType,
			"toDeviceType": oo.pBaseDeviceHandler.ProxyAddressType,
			"onuDeviceID":  oo.deviceID, "proxyDeviceID": oo.pBaseDeviceHandler.ProxyAddressID})
		logger.Debugw("omci-message-to-send:",
			log.Fields{"TxOmciMessage": hex.EncodeToString(omciTxRequest.txFrame)})

		omciMsg := &ic.InterAdapterOmciMessage{Message: omciTxRequest.txFrame}
		if sendErr := oo.adapterProxy.SendInterAdapterMessage(context.Background(), omciMsg,
			ic.InterAdapterMessageType_OMCI_REQUEST,
			//fromType,toType,toDevId, ProxyDevId
			oo.pBaseDeviceHandler.DeviceType, oo.pBaseDeviceHandler.ProxyAddressType,
			oo.deviceID, oo.pBaseDeviceHandler.ProxyAddressID, ""); sendErr != nil {
			logger.Errorw("send omci request error", log.Fields{"error": sendErr})
			return sendErr
		}
		oo.txQueue.Remove(queueElement) // Dequeue
	}
	return nil
}

func (oo *omciCC) getNextTid(highPriority bool) uint16 {
	var next uint16
	if highPriority {
		oo.mutexTid.Lock()
		next = oo.hpTid
		oo.hpTid++
		if oo.hpTid < 0x8000 {
			oo.hpTid = 0x8000
		}
		oo.mutexTid.Unlock()
	} else {
		oo.mutexHpTid.Lock()
		next = oo.tid
		oo.tid++
		if oo.tid >= 0x8000 {
			oo.tid = 1
		}
		oo.mutexHpTid.Unlock()
	}
	return next
}

// ###################################################################################
// # utility methods provided to work on OMCI messages
func serialize(msgType omci.MessageType, request gopacket.SerializableLayer, tid uint16) ([]byte, error) {
	omciLayer := &omci.OMCI{
		TransactionID: tid,
		MessageType:   msgType,
	}
	return serializeOmciLayer(omciLayer, request)
}

func serializeOmciLayer(aOmciLayer *omci.OMCI, aRequest gopacket.SerializableLayer) ([]byte, error) {
	var options gopacket.SerializeOptions
	options.FixLengths = true

	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, aOmciLayer, aRequest)
	if err != nil {
		logger.Errorw("Could not create goPacket Omci serial buffer", log.Fields{"Err": err})
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
func (oo *omciCC) receiveOmciResponse(omciMsg *omci.OMCI, packet *gp.Packet, respChan chan Message) error {

	logger.Debugw("omci-message-response - transfer on omciRespChannel", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": strconv.FormatInt(int64(omciMsg.TransactionID), 16), "device-id": oo.deviceID})

	if oo.pOnuDeviceEntry == nil {
		logger.Errorw("Abort receiving OMCI response, DeviceEntryPointer is nil", log.Fields{
			"device-id": oo.deviceID})
		return errors.New("deviceEntryPointer is nil")
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
	//logger.Debugw("Message to be sent into channel:", log.Fields{"mibSyncMsg": mibSyncMsg})
	respChan <- omciRespMsg

	return nil
}

func (oo *omciCC) sendMibReset(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw("send MibReset-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibResetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(omci.MibResetRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibResetRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendReboot(ctx context.Context, timeout int, highPrio bool, responseChannel chan Message) error {
	logger.Debugw("send Reboot-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.RebootRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuGClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(omci.RebootRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{oo.pOnuDeviceEntry.omciRebootMessageReceivedChannel, oo.receiveOmciResponse},
	}

	err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
	if err != nil {
		logger.Errorw("Cannot send RebootRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	err = oo.pOnuDeviceEntry.waitForRebootResponse(responseChannel)
	if err != nil {
		logger.Error("aborting ONU Reboot!")
		_ = oo.pOnuDeviceEntry.pMibDownloadFsm.pFsm.Event("reset")
		return err
	}
	return nil
}

func (oo *omciCC) sendMibUpload(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw("send MibUpload-msg to:", log.Fields{"device-id": oo.deviceID})
	request := &omci.MibUploadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(omci.MibUploadRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibUploadRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.uploadSequNo = 0
	oo.uploadNoOfCmds = 0

	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendMibUploadNext(ctx context.Context, timeout int, highPrio bool) error {
	logger.Debugw("send MibUploadNext-msg to:", log.Fields{"device-id": oo.deviceID, "uploadSequNo": oo.uploadSequNo})
	request := &omci.MibUploadNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: oo.uploadSequNo,
	}
	tid := oo.getNextTid(highPrio)
	pkt, err := serialize(omci.MibUploadNextRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibUploadNextRequest", log.Fields{
			"Err": err, "device-id": oo.deviceID})
		return err
	}
	oo.uploadSequNo++

	omciRxCallbackPair := callbackPair{
		cbKey:   tid,
		cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse},
	}
	return oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *omciCC) sendCreateGalEthernetProfile(ctx context.Context, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send GalEnetProfile-Create-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode GalEnetProfileInstance for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send GalEnetProfile create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send GalEnetProfile-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate GalEnetProfileInstance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

// might be needed to extend for parameter arguments, here just for setting the ConnectivityMode!!
func (oo *omciCC) sendSetOnu2g(ctx context.Context, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send ONU2-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode ONU2-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send ONU2-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send ONU2-G-Set-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate ONU2-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBServiceProfile(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	instID := macBridgeServiceProfileEID + uint16(aPUniPort.macBpNo)
	logger.Debugw("send MBSP-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(instID), 16)})

	meParams := me.ParamData{
		EntityID: instID,
		Attributes: me.AttributeValueMap{
			"Priority":     0x8000,
			"MaxAge":       20 * 256, //20s
			"HelloTime":    2 * 256,  //2s
			"ForwardDelay": 15 * 256, //15s
			//note: DynamicFilteringAgeingTime is taken from omci lib default as
			//  which is obviously different from default value used in python lib,
			//  where the value seems to be 0 (ONU defined)  - to be considered in case of test artifacts ...
		},
	}

	meInstance, omciErr := me.NewMacBridgeServiceProfile(meParams)
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw("Cannot encode MBSP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send MBSP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send MBSP-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate MBSP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBPConfigData(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	instID := macBridgePortAniEID + aPUniPort.entityID
	logger.Debugw("send MBPCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send MBPCD-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateEVTOConfigData(ctx context.Context,
	aPUniPort *onuUniPort, timeout int, highPrio bool) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	//same entityId is used as for MBSP (see there), but just arbitrary ...
	instID := macBridgeServiceProfileEID + uint16(aPUniPort.macBpNo)
	logger.Debugw("send EVTOCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode EVTOCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibDownloadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send EVTOCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send EVTOCD-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetOnuGLS(ctx context.Context, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send ONU-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode ONU-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send ONU-G set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send ONU-G-Set-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate ONU-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetUniGLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send UNI-G-Set-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode UNI-G instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize UNI-G-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send UNIG-G-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send UNI-G-Set-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate UNI-G", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetVeipLS(ctx context.Context, aInstNo uint16, timeout int,
	highPrio bool, requestedAttributes me.AttributeValueMap, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send VEIP-Set-msg:", log.Fields{"device-id": oo.deviceID,
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
			logger.Errorw("Cannot encode VEIP instance for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send VEIP-Set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send VEIP-Set-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate VEIP", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendGetMe(ctx context.Context, classID me.ClassID, entityID uint16, requestedAttributes me.AttributeValueMap,
	timeout int, highPrio bool) *me.ManagedEntity {

	tid := oo.getNextTid(highPrio)
	logger.Debugw("send get-request-msg", log.Fields{"classID": classID, "device-id": oo.deviceID,
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
			logger.Errorf("Cannot encode instance for get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize get-request", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{(*oo.pOnuDeviceEntry).pMibUploadFsm.commChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send get-request-msg", log.Fields{"meClassIDName": meClassIDName, "Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debugw("send get-request-msg done", log.Fields{"meClassIDName": meClassIDName, "device-id": oo.deviceID})
		return meInstance
	}
	logger.Errorw("Cannot generate meDefinition", log.Fields{"classID": classID, "Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateDot1PMapper(ctx context.Context, timeout int, highPrio bool,
	aInstID uint16, rxChan chan Message) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send .1pMapper-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16), "InstId": strconv.FormatInt(int64(aInstID), 16)})

	meParams := me.ParamData{
		EntityID:   aInstID,
		Attributes: me.AttributeValueMap{},
	}
	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(meParams)
	if omciErr.GetError() == nil {
		//we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw("Cannot encode .1pMapper for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send .1pMapper create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send .1pMapper-create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate .1pMapper", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateMBPConfigDataVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send MBPCD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewMacBridgePortConfigurationData(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw("Cannot encode MBPCD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send MBPCD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send MBPCD-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate MBPCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateGemNCTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send GemNCTP-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemPortNetworkCtp(params[0])
	if omciErr.GetError() == nil {
		//obviously we have to set all 'untouched' parameters to default by some additional option parameter!!
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid), omci.AddDefaults(true))
		if err != nil {
			logger.Errorw("Cannot encode GemNCTP for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send GemNCTP create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send GemNCTP-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate GemNCTP Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateGemIWTPVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send GemIwTp-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewGemInterworkingTerminationPoint(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode GemIwTp for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send GemIwTp create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send GemIwTp-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate GemIwTp Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetTcontVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send TCont-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewTCont(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode TCont for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send TCont set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send TCont-set msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate TCont Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetPrioQueueVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send PrioQueue-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewPriorityQueue(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode PrioQueue for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send PrioQueue set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send PrioQueue-set msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate PrioQueue Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetDot1PMapperVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send 1PMapper-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewIeee8021PMapperServiceProfile(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode 1PMapper for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send 1PMapper set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send 1PMapper-set msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate 1PMapper Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendCreateVtfdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send VTFD-Create-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewVlanTaggingFilterData(params[0])
	if omciErr.GetError() == nil {
		//all SetByCreate Parameters (assumed to be) set here, for optimisation no 'AddDefaults'
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.CreateRequestType,
			omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode VTFD for create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			//TODO!!: refactoring improvement requested, here as an example for [VOL-3457]:
			//  return (dual format) error code that can be used at caller for immediate error treatment
			//  (relevant to all used sendXX() methods and their error conditions)
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send VTFD create", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send VTFD-Create-msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate VTFD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}

func (oo *omciCC) sendSetEvtocdVar(ctx context.Context, timeout int, highPrio bool,
	rxChan chan Message, params ...me.ParamData) *me.ManagedEntity {
	tid := oo.getNextTid(highPrio)
	logger.Debugw("send EVTOCD-Set-msg:", log.Fields{"device-id": oo.deviceID,
		"SequNo": strconv.FormatInt(int64(tid), 16),
		"InstId": strconv.FormatInt(int64(params[0].EntityID), 16)})

	meInstance, omciErr := me.NewExtendedVlanTaggingOperationConfigurationData(params[0])
	if omciErr.GetError() == nil {
		omciLayer, msgLayer, err := omci.EncodeFrame(meInstance, omci.SetRequestType, omci.TransactionID(tid))
		if err != nil {
			logger.Errorw("Cannot encode EVTOCD for set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		pkt, err := serializeOmciLayer(omciLayer, msgLayer)
		if err != nil {
			logger.Errorw("Cannot serialize EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}

		omciRxCallbackPair := callbackPair{
			cbKey:   tid,
			cbEntry: callbackPairEntry{rxChan, oo.receiveOmciResponse},
		}
		err = oo.send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
		if err != nil {
			logger.Errorw("Cannot send EVTOCD set", log.Fields{
				"Err": err, "device-id": oo.deviceID})
			return nil
		}
		logger.Debug("send EVTOCD-set msg done")
		return meInstance
	}
	logger.Errorw("Cannot generate EVTOCD Instance", log.Fields{
		"Err": omciErr.GetError(), "device-id": oo.deviceID})
	return nil
}
