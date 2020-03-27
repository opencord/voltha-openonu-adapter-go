//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"container/list"
	"context"
	"encoding/hex"
	"errors"
	"sync"

	//"time"

	"github.com/google/gopacket"

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

type omciTransferStructure struct {
	txFrame  []byte
	timeout  int
	retry    int
	highPrio bool
}

//OmciCC structure holds information needed for OMCI communication (to/from OLT Adapter)
type OmciCC struct {
	enabled           bool
	deviceID          string
	baseDeviceHandler DeviceHandler
	coreProxy         adapterif.CoreProxy
	adapterProxy      adapterif.AdapterProxy
	supportExtMsg     bool
	//txRequest
	//rxResponse
	//pendingRequest
	txFrames, txOnuFrames                uint32
	rxFrames, rxOnuFrames, rxOnuDiscards uint32

	// OMCI params
	tid   uint16
	hpTid uint16

	txQueue      *list.List
	mutexTxQueue sync.Mutex
}

//OmciCC constructor returns a new instance of a OmciCC
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewOmciCC(ctx context.Context,
	device_id string, device_handler DeviceHandler,
	core_proxy adapterif.CoreProxy, adapter_proxy adapterif.AdapterProxy) *OmciCC {
	log.Infow("init-omciCC", log.Fields{"deviceId": device_id})
	var omciCC OmciCC
	omciCC.enabled = false
	omciCC.deviceID = device_id
	omciCC.baseDeviceHandler = device_handler
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

	omciCC.txQueue = list.New()

	return &omciCC
}

// Rx handler for omci messages
func (oo *OmciCC) ReceiveOnuMessage(ctx context.Context, rxFrame []byte) error {
	log.Debug("rx-onu-frame")

	/*
			msgType = rxFrame.fields["message_type"] //assumed OmciOperationsValue
			rxOnuFrames++

			switch msgType {
			case AlarmNotification:
				{
					log.Info("Unhandled: received-onu-alarm-message")
					// python code was:
					//if msg_type == EntityOperations.AlarmNotification.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.Alarm_Notification)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxAlarmNotification unimplemented")
				}
			case AttributeValueChange:
				{
					log.Info("Unhandled: received-attribute-value-change")
					// python code was:
					//elif msg_type == EntityOperations.AttributeValueChange.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.AVC_Notification)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxAttributeValueChange unimplemented")
				}
			case TestResult:
				{
					log.Info("Unhandled: received-test-result")
					// python code was:
					//elif msg_type == EntityOperations.TestResult.value:
					//	topic = OMCI_CC.event_bus_topic(self._device_id, RxEvent.Test_Result)
					//	self.reactor.callLater(0,  self.event_bus.publish, topic, msg)
					//
					return errors.New("RxTestResult unimplemented")
				}
			default:
				{
					log.Errorw("rx-onu-unsupported-autonomous-message", log.Fields{"msgType": msgType})
					rxOnuDiscards++
					return errors.New("RxOnuMsgType unimplemented")
				}
		    }
	*/
	return errors.New("ReceiveOnuMessage unimplemented")
}

// Rx handler for onu messages
func (oo *OmciCC) ReceiveMessage(ctx context.Context, rxMsg []byte) error {
	// see below, e.g. would call ReceiveOnuMessage() in case of TID=0 or Action=test ...
	return errors.New("RxOmciMsg unimplemented")
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
	                   self.log.debug('recv-omci-msg', omci_msg=hexlify(msg))
	               except KeyError as e:
	                   # Unknown, Unsupported, or vendor-specific ME. Key is the unknown classID
	                   self.log.debug('frame-decode-key-error', omci_msg=hexlify(msg), e=e)
	                   rx_frame = self._decode_unknown_me(msg)
	                   self._rx_unknown_me += 1

	               except Exception as e:
	                   self.log.exception('frame-decode', omci_msg=hexlify(msg), e=e)
	                   return

	               finally:
	                   omci_entities.entity_id_to_class_map = saved_me_map     # Always restore it.

	               rx_tid = rx_frame.fields['transaction_id']
	               msg_type = rx_frame.fields['message_type']
	               self.log.debug('Received message for rx_tid', rx_tid = rx_tid, msg_type = msg_type)
	               # Filter the Test Result frame and route through receive onu
	               # message method.
	               if rx_tid == 0 or msg_type == EntityOperations.TestResult.value:
	                   self.log.debug('Receive ONU message', rx_tid=0)
	                   return self._receive_onu_message(rx_frame)

	               # Previously unreachable if this is the very first round-trip Rx or we
	               # have been running consecutive errors
	               if self._rx_frames == 0 or self._consecutive_errors != 0:
	                   self.log.debug('Consecutive errors for rx', err = self._consecutive_errors)
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
	                           self.log.debug('Unknown message', rx_tid=rx_tid,
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
	                       self.log.debug('Serviced by timeout. Late arrival', rx_late = self._rx_late)
	                       return

	               except Exception as e:
	                   self.log.exception('frame-match', msg=hexlify(msg), e=e)
	                   if d is not None:
	                       return d.errback(failure.Failure(e))
	                   return

	               # Publish Rx event to listeners in a different task
	               self.log.debug('Publish rx event', rx_tid = rx_tid,
	                              tx_tid = tx_frame.fields['transaction_id'])
	               reactor.callLater(0, self._publish_rx_frame, tx_frame, rx_frame)

	               # begin success callback chain (will cancel timeout and queue next Tx message)
	               self._rx_response[index] = rx_frame
	               d.callback(rx_frame)

	           except Exception as e:
	   			self.log.exception('rx-msg', e=e)
	*/
}

func (oo *OmciCC) PublishRxResponseFrame(ctx context.Context, txFrame []byte, rxFrame []byte) error {
	return errors.New("PublishRxResponseFrame unimplemented")
	/*
		def _publish_rx_frame(self, tx_frame, rx_frame):
	*/
}

//Queue the OMCI Frame for a transmit to the ONU via the proxy_channel
func (oo *OmciCC) Send(ctx context.Context, txFrame []byte, timeout int, retry int, highPrio bool) error {
	// TODO verify timeout, queue the frame and initiate real frame transmission based on status of queue/transfer channel status

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
			log.Debugw("omci indication", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
			var deviceType string
			var deviceID string
			var proxyDeviceID string

			onuKey := dh.formOnuKey(omciInd.IntfId, omciInd.OnuId)

			if onuInCache, ok := dh.onus.Load(onuKey); !ok {

				log.Debugw("omci indication for a device not in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
				ponPort := IntfIDToPortNo(omciInd.GetIntfId(), voltha.Port_PON_OLT)
				kwargs := make(map[string]interface{})
				kwargs["onu_id"] = omciInd.OnuId
				kwargs["parent_port_no"] = ponPort

				onuDevice, err := dh.coreProxy.GetChildDevice(context.TODO(), dh.device.Id, kwargs)
				if err != nil {
					log.Errorw("onu not found", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId, "error": err})
					return
				}
				deviceType = onuDevice.Type
				deviceID = onuDevice.Id
				proxyDeviceID = onuDevice.ProxyAddress.DeviceId
				//if not exist in cache, then add to cache.
				dh.onus.Store(onuKey, NewOnuDevice(deviceID, deviceType, onuDevice.SerialNumber, omciInd.OnuId, omciInd.IntfId, proxyDeviceID))
			} else {
				//found in cache
				log.Debugw("omci indication for a device in cache.", log.Fields{"intfID": omciInd.IntfId, "onuID": omciInd.OnuId})
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

		self.log.debug('sent-omci-msg', tid=tx_tid, omci_msg=hexlify(bytes(frame)))

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
			oo.baseDeviceHandler.deviceID, oo.deviceID) //parent, child
		if err != nil || device == nil {
			/*TODO: needs to handle error scenarios */
			log.Errorw("Failed to fetch device", log.Fields{"err": err, "ParentId": oo.baseDeviceHandler.deviceID,
				"ChildId": oo.deviceID})
			return errors.New("failed to fetch device")
		}

		log.Infow("omci-message-sending", log.Fields{"fromDeviceType": oo.baseDeviceHandler.DeviceType,
			"toDeviceType": oo.baseDeviceHandler.ProxyAddressType,
			"onuDeviceID":  oo.deviceID, "proxyDeviceID": oo.baseDeviceHandler.ProxyAddressID})

		omciMsg := &ic.InterAdapterOmciMessage{Message: omciTxRequest.txFrame}
		if sendErr := oo.adapterProxy.SendInterAdapterMessage(context.Background(), omciMsg,
			ic.InterAdapterMessageType_OMCI_REQUEST,
			//fromType,toType,toDevId, ProxyDevId
			oo.baseDeviceHandler.DeviceType, oo.baseDeviceHandler.ProxyAddressType,
			oo.deviceID, oo.baseDeviceHandler.ProxyAddressID, ""); sendErr != nil {
			log.Errorw("send omci request error", log.Fields{"error": sendErr})
			return sendErr
		}
		oo.txQueue.Remove(queueElement) // Dequeue
	}
	oo.mutexTxQueue.Unlock()
	return nil
}

func (oo *OmciCC) GetNextTid(highPriority ...bool) uint16 {
	var next uint16
	if len(highPriority) > 0 && highPriority[0] {
		next = oo.hpTid
		oo.hpTid += 1
		if oo.hpTid < 0x8000 {
			oo.hpTid = 0x8000
		}
	} else {
		next = oo.tid
		oo.tid += 1
		if oo.tid >= 0x8000 {
			oo.tid = 1
		}
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

func (oo *OmciCC) sendMibReset(ctx context.Context, timeout int, highPrio bool) error {

	request := &omci.MibResetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass: me.OnuDataClassID,
		},
	}
	pkt, err := serialize(omci.MibResetRequestType, request, oo.GetNextTid(highPrio))
	if err != nil {
		log.Errorw("Cannot serialize MibResetRequest", log.Fields{"Err": err})
		return err
	}
	return oo.Send(ctx, pkt, timeout, 0, highPrio)
}

func (oo *OmciCC) sendMibUpload(ctx context.Context, timeout int, highPrio bool) error {
	log.Error("sendMibUpload unimplemented")
	txFrame := make([]byte, 48)
	return oo.Send(ctx, txFrame, timeout, 0, highPrio)
}
func (oo *OmciCC) sendMibUploadNext(ctx context.Context, sequNo int, timeout int, highPrio bool) error {
	log.Error("sendMibUploadNext unimplemented")
	txFrame := make([]byte, 48)
	return oo.Send(ctx, txFrame, timeout, 0, highPrio)
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
