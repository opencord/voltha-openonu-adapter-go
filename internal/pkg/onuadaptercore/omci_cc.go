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

const ConstDefaultOmciTimeout = 10 // ( 3 ?) Seconds

//CallbackPair to be used for ReceiveCallback init
type CallbackPair struct {
	cbKey      uint16
	cbFunction func(*omci.OMCI, *gp.Packet) error
}

type omciTransferStructure struct {
	txFrame  []byte
	timeout  int
	retry    int
	highPrio bool
}

//OmciCC structure holds information needed for OMCI communication (to/from OLT Adapter)
type OmciCC struct {
	enabled            bool
	pOnuDeviceEntry    *OnuDeviceEntry
	deviceID           string
	pBaseDeviceHandler *DeviceHandler
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

	mutexTxQueue    sync.Mutex
	txQueue         *list.List
	mutexRxSchedMap sync.Mutex
	rxSchedulerMap  map[uint16]func(*omci.OMCI, *gp.Packet) error
}

//NewOmciCC constructor returns a new instance of a OmciCC
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOmciCC(ctx context.Context, onu_device_entry *OnuDeviceEntry,
	device_id string, device_handler *DeviceHandler,
	core_proxy adapterif.CoreProxy, adapter_proxy adapterif.AdapterProxy) *OmciCC {
	logger.Infow("init-omciCC", log.Fields{"deviceId": device_id})
	var omciCC OmciCC
	omciCC.enabled = false
	omciCC.pOnuDeviceEntry = onu_device_entry
	omciCC.deviceID = device_id
	omciCC.pBaseDeviceHandler = device_handler
	omciCC.coreProxy = core_proxy
	omciCC.adapterProxy = adapter_proxy
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
	omciCC.rxSchedulerMap = make(map[uint16]func(*omci.OMCI, *gp.Packet) error)

	return &omciCC
}

// Rx handler for omci messages
func (oo *OmciCC) ReceiveOnuMessage(ctx context.Context, omciMsg *omci.OMCI) error {
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
	return errors.New("ReceiveOnuMessage unimplemented")
}

// Rx handler for onu messages
//    e.g. would call ReceiveOnuMessage() in case of TID=0 or Action=test ...
func (oo *OmciCC) ReceiveMessage(ctx context.Context, rxMsg []byte) error {
	//logger.Debugw("cc-receive-omci-message", log.Fields{"RxOmciMessage-x2s": hex.EncodeToString(rxMsg)})
	if len(rxMsg) >= 44 { // then it should normally include the BaseFormat trailer Len
		// NOTE: autocorrection only valid for OmciBaseFormat, which is not specifically verified here!!!
		//  (am extendedFormat message could be destroyed this way!)
		trailerLenData := rxMsg[42:44]
		trailerLen := binary.BigEndian.Uint16(trailerLenData)
		logger.Infow("omci-received-trailer-len", log.Fields{"Length": trailerLen})
		if trailerLen != 40 { // invalid base Format entry -> autocorrect
			binary.BigEndian.PutUint16(rxMsg[42:44], 40)
			logger.Debug("cc-corrected-omci-message: trailer len inserted")
		}
	} else {
		logger.Errorw("received omci-message to small for OmciBaseFormat - abort", log.Fields{"Length": len(rxMsg)})
		return errors.New("RxOmciMessage to small for BaseFormat")
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
		"transCorrId": omciMsg.TransactionID, "DeviceIdent": omciMsg.DeviceIdentifier})
	if byte(omciMsg.MessageType) & ^me.AK == 0 {
		// Not a response
		logger.Debug("RxMsg is no Omci Response Message")
		if omciMsg.TransactionID == 0 {
			return oo.ReceiveOnuMessage(ctx, omciMsg)
		} else {
			logger.Errorw("Unexpected TransCorrId != 0  not accepted for autonomous messages",
				log.Fields{"msgType": omciMsg.MessageType, "payload": hex.EncodeToString(omciMsg.Payload)})
			return errors.New("Autonomous Omci Message with TranSCorrId != 0 not acccepted")
		}
	} else {
		logger.Debug("RxMsg is a Omci Response Message: try to schedule it to the requester")
		oo.mutexRxSchedMap.Lock()
		rxCallback, ok := oo.rxSchedulerMap[omciMsg.TransactionID]
		if ok && rxCallback != nil {
			//disadvantage of decoupling: error verification made difficult, but anyway the question is
			// how to react on erroneous frame reception, maybe can simply be ignored
			go rxCallback(omciMsg, &packet)
			// having posted the response the request is regarded as 'done'
			delete(oo.rxSchedulerMap, omciMsg.TransactionID)
			oo.mutexRxSchedMap.Unlock()
		} else {
			oo.mutexRxSchedMap.Unlock()
			logger.Error("omci-message-response for not registered transCorrId")
			return errors.New("could not find registered response handler tor transCorrId")
		}
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

func (oo *OmciCC) PublishRxResponseFrame(ctx context.Context, txFrame []byte, rxFrame []byte) error {
	return errors.New("PublishRxResponseFrame unimplemented")
	/*
		def _publish_rx_frame(self, tx_frame, rx_frame):
	*/
}

//Queue the OMCI Frame for a transmit to the ONU via the proxy_channel
func (oo *OmciCC) Send(ctx context.Context, txFrame []byte, timeout int, retry int, highPrio bool,
	receiveCallbackPair CallbackPair) error {

	logger.Debugw("register-response-callback:", log.Fields{"for TansCorrId": receiveCallbackPair.cbKey})
	// it could be checked, if the callback keay is already registered - but simply overwrite may be acceptable ...
	oo.mutexRxSchedMap.Lock()
	oo.rxSchedulerMap[receiveCallbackPair.cbKey] = receiveCallbackPair.cbFunction
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
func (oo *OmciCC) sendNextRequest(ctx context.Context) error {
	//	return errors.New("sendNextRequest unimplemented")

	// just try to get something transferred !!
	// avoid accessing the txQueue from parallel send requests
	// block parallel omci send requests at least until SendIAP is 'committed'
	// that should be feasible for an onu instance as on OMCI anyway window size 1 is assumed
	oo.mutexTxQueue.Lock()
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
	oo.mutexTxQueue.Unlock()
	return nil
}

func (oo *OmciCC) GetNextTid(highPriority bool) uint16 {
	var next uint16
	if highPriority {
		oo.mutexTid.Lock()
		next = oo.hpTid
		oo.hpTid += 1
		if oo.hpTid < 0x8000 {
			oo.hpTid = 0x8000
		}
		oo.mutexTid.Unlock()
	} else {
		oo.mutexHpTid.Lock()
		next = oo.tid
		oo.tid += 1
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
	var options gopacket.SerializeOptions
	options.FixLengths = true

	buffer := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(buffer, options, omciLayer, request)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func hexEncode(omciPkt []byte) ([]byte, error) {
	dst := make([]byte, hex.EncodedLen(len(omciPkt)))
	hex.Encode(dst, omciPkt)
	return dst, nil
}

// ###################################################################################
// # MIB Action shortcuts  - still dummies - TODO!!!!!

//supply a response handler for the MibSync omci response messages
func (oo *OmciCC) receiveMibSyncResponse(omciMsg *omci.OMCI, packet *gp.Packet) error {

	logger.Debugw("mib-sync-omci-message-response received:", log.Fields{"omciMsgType": omciMsg.MessageType,
		"transCorrId": omciMsg.TransactionID, "deviceId": oo.deviceID})

	if oo.pOnuDeviceEntry == nil {
		logger.Error("Abort Receive MibSync OMCI, DeviceEntryPointer is nil")
		return errors.New("DeviceEntryPointer is nil")
	}

	// no further test on SeqNo is done here, assignment from rxScheduler is trusted
	// MibSync responses are simply transferred via deviceEntry to MibSync, no specific analysis here
	mibSyncMsg := Message{
		Type: OMCI,
		Data: OmciMessage{
			OmciMsg:    omciMsg,
			OmciPacket: packet,
		},
	}
	//logger.Debugw("Message to be sent into channel:", log.Fields{"mibSyncMsg": mibSyncMsg})
	(*oo.pOnuDeviceEntry).MibSyncChan <- mibSyncMsg

	return nil
}

func (oo *OmciCC) sendMibReset(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw("send MibReset-msg to:", log.Fields{"deviceId": oo.deviceID})
	request := &omci.MibResetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := serialize(omci.MibResetRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibResetRequest", log.Fields{"Err": err})
		return err
	}
	omciRxCallbackPair := CallbackPair{tid, oo.receiveMibSyncResponse}
	return oo.Send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *OmciCC) sendMibUpload(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw("send MibUpload-msg to:", log.Fields{"deviceId": oo.deviceID})
	request := &omci.MibUploadRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := serialize(omci.MibUploadRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibUploadRequest", log.Fields{"Err": err})
		return err
	}
	oo.uploadSequNo = 0
	oo.uploadNoOfCmds = 0

	omciRxCallbackPair := CallbackPair{tid, oo.receiveMibSyncResponse}
	return oo.Send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

func (oo *OmciCC) sendMibUploadNext(ctx context.Context, timeout int, highPrio bool) error {

	logger.Debugw("send MibUploadNext-msg to:", log.Fields{"deviceId": oo.deviceID, "uploadSequNo": oo.uploadSequNo})
	request := &omci.MibUploadNextRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
		CommandSequenceNumber: oo.uploadSequNo,
	}
	tid := oo.GetNextTid(highPrio)
	pkt, err := serialize(omci.MibUploadNextRequestType, request, tid)
	if err != nil {
		logger.Errorw("Cannot serialize MibUploadNextRequest", log.Fields{"Err": err})
		return err
	}
	oo.uploadSequNo++

	omciRxCallbackPair := CallbackPair{tid, oo.receiveMibSyncResponse}
	return oo.Send(ctx, pkt, timeout, 0, highPrio, omciRxCallbackPair)
}

/* py code example
...
def send_mib_upload(self, timeout=DEFAULT_OMCI_TIMEOUT, high_priority=False):
	frame = OntDataFrame().mib_upload()
	return self.send(frame, timeout=timeout, high_priority=high_priority)

def send_mib_upload_next(self, seq_no, timeout=DEFAULT_OMCI_TIMEOUT, high_priority=False):
	frame = OntDataFrame(sequence_number=seq_no).mib_upload_next()
	return self.send(frame, timeout=timeout, high_priority=high_priority)
...
*/
