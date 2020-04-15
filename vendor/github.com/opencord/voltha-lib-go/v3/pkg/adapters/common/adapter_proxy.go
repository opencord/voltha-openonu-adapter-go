/*
 * Copyright 2018-present Open Networking Foundation

 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at

 * http://www.apache.org/licenses/LICENSE-2.0

 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package common

import (
	"context"
	"github.com/opencord/voltha-lib-go/v3/pkg/db"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/google/uuid"
	"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
)

type AdapterProxy struct {
	kafkaICProxy kafka.InterContainerProxy
	adapterTopic string
	coreTopic    string
	endpointMgr  kafka.EndpointManager
}

func NewAdapterProxy(kafkaProxy kafka.InterContainerProxy, adapterTopic string, coreTopic string, backend *db.Backend) *AdapterProxy {
	proxy := AdapterProxy{
		kafkaICProxy: kafkaProxy,
		adapterTopic: adapterTopic,
		coreTopic:    coreTopic,
		endpointMgr:  kafka.NewEndpointManager(backend),
	}
	logger.Debugw("topics", log.Fields{"core": proxy.coreTopic, "adapter": proxy.adapterTopic})
	return &proxy
}

func (ap *AdapterProxy) SendInterAdapterMessage(ctx context.Context,
	msg proto.Message,
	msgType ic.InterAdapterMessageType_Types,
	fromAdapter string,
	toAdapter string,
	toDeviceId string,
	proxyDeviceId string,
	messageId string) error {
	logger.Debugw("sending-inter-adapter-message", log.Fields{"type": msgType, "from": fromAdapter,
		"to": toAdapter, "toDevice": toDeviceId, "proxyDevice": proxyDeviceId})

	//Marshal the message
	var marshalledMsg *any.Any
	var err error
	if marshalledMsg, err = ptypes.MarshalAny(msg); err != nil {
		logger.Warnw("cannot-marshal-msg", log.Fields{"error": err})
		return err
	}

	//Build the inter adapter message
	header := &ic.InterAdapterHeader{
		Type:          msgType,
		FromTopic:     fromAdapter,
		ToTopic:       toAdapter,
		ToDeviceId:    toDeviceId,
		ProxyDeviceId: proxyDeviceId,
	}
	if messageId != "" {
		header.Id = messageId
	} else {
		header.Id = uuid.New().String()
	}
	header.Timestamp = time.Now().Unix()
	iaMsg := &ic.InterAdapterMessage{
		Header: header,
		Body:   marshalledMsg,
	}
	args := make([]*kafka.KVArg, 1)
	args[0] = &kafka.KVArg{
		Key:   "msg",
		Value: iaMsg,
	}

	// Set up the required rpc arguments
	endpoint, err := ap.endpointMgr.GetEndpoint(toDeviceId, toAdapter)
	if err != nil {
		return err
	}
	topic := kafka.Topic{Name: string(endpoint)}
	replyToTopic := kafka.Topic{Name: fromAdapter}
	rpc := "process_inter_adapter_message"

	success, result := ap.kafkaICProxy.InvokeRPC(ctx, rpc, &topic, &replyToTopic, true, proxyDeviceId, args...)
	logger.Debugw("inter-adapter-msg-response", log.Fields{"replyTopic": replyToTopic, "success": success})
	return unPackResponse(rpc, "", success, result)
}
