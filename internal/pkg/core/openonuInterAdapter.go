/*
 * Copyright 2022-present Open Networking Foundation
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

//Package core provides the utility for onu devices, flows and statistics
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	"github.com/opencord/voltha-protos/v5/go/common"
	"github.com/opencord/voltha-protos/v5/go/health"
	ia "github.com/opencord/voltha-protos/v5/go/inter_adapter"
	"github.com/opencord/voltha-protos/v5/go/onu_inter_adapter_service"
)

//OpenONUACInterAdapter structure holds a reference to ONU adapter
type OpenONUACInterAdapter struct {
	onuAdapter  *OpenONUAC
	exitChannel chan struct{}
}

//NewOpenONUACAdapter returns a new instance of OpenONUACAdapter
func NewOpenONUACAdapter(ctx context.Context, onuAdapter *OpenONUAC) *OpenONUACInterAdapter {
	return &OpenONUACInterAdapter{onuAdapter: onuAdapter, exitChannel: make(chan struct{})}
}

//Start starts (logs) the adapter
func (oo *OpenONUACInterAdapter) Start(ctx context.Context) error {
	logger.Info(ctx, "starting-openonu-inter-adapter")
	return nil
}

//OnuIndication redirects the request the the core ONU adapter handler
func (oo *OpenONUACInterAdapter) OnuIndication(ctx context.Context, onuInd *ia.OnuIndicationMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OnuIndication(ctx, onuInd)
}

//OmciIndication redirects the request the the core ONU adapter handler
func (oo *OpenONUACInterAdapter) OmciIndication(ctx context.Context, msg *ia.OmciMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OmciIndication(ctx, msg)
}

//DownloadTechProfile redirects the request the the core ONU adapter handler
func (oo *OpenONUACInterAdapter) DownloadTechProfile(ctx context.Context, tProfile *ia.TechProfileDownloadMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DownloadTechProfile(ctx, tProfile)
}

//DeleteGemPort redirects the request the the core ONU adapter handler
func (oo *OpenONUACInterAdapter) DeleteGemPort(ctx context.Context, gPort *ia.DeleteGemPortMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteGemPort(ctx, gPort)
}

//DeleteTCont redirects the request the the core ONU adapter handler
func (oo *OpenONUACInterAdapter) DeleteTCont(ctx context.Context, tConf *ia.DeleteTcontMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteTCont(ctx, tConf)
}

//Stop terminates the session
func (oo *OpenONUACInterAdapter) Stop(ctx context.Context) error {
	close(oo.exitChannel)
	logger.Info(ctx, "openonu-inter-adapter-stopped")
	return nil
}

// GetHealthStatus is used by a OnuInterAdapterService client to detect a connection
// lost with the gRPC server hosting the OnuInterAdapterService service
func (oo *OpenONUACInterAdapter) GetHealthStatus(stream onu_inter_adapter_service.OnuInterAdapterService_GetHealthStatusServer) error {
	ctx := context.Background()
	logger.Debugw(ctx, "receive-stream-connection", log.Fields{"stream": stream})

	if stream == nil {
		return fmt.Errorf("conn-is-nil %v", stream)
	}
	initialRequestTime := time.Now()
	var remoteClient *common.Connection
	var tempClient *common.Connection
	var err error
loop:
	for {
		tempClient, err = stream.Recv()
		if err != nil {
			logger.Warnw(ctx, "received-stream-error", log.Fields{"remote-client": remoteClient, "error": err})
			break loop
		}
		// Send a response back
		err = stream.Send(&health.HealthStatus{State: health.HealthStatus_HEALTHY})
		if err != nil {
			logger.Warnw(ctx, "sending-stream-error", log.Fields{"remote-client": remoteClient, "error": err})
			break loop
		}

		remoteClient = tempClient
		logger.Debugw(ctx, "received-keep-alive", log.Fields{"remote-client": remoteClient})
		oo.onuAdapter.updateReachabilityFromRemote(context.Background(), remoteClient)

		select {
		case <-stream.Context().Done():
			logger.Infow(ctx, "stream-keep-alive-context-done", log.Fields{"remote-client": remoteClient, "error": stream.Context().Err()})
			break loop
		case <-oo.exitChannel:
			logger.Warnw(ctx, "received-stop", log.Fields{"remote-client": remoteClient, "initial-conn-time": initialRequestTime})
			break loop
		default:
		}
	}
	logger.Errorw(ctx, "connection-down", log.Fields{"remote-client": remoteClient, "error": err, "initial-conn-time": initialRequestTime})
	return err
}
