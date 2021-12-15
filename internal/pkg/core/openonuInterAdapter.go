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

//OpenONUAC structure holds the ONU core information
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

// GetHealthStatus is used as a service readiness validation as a grpc connection
func (oo *OpenONUACInterAdapter) GetHealthStatus(ctx context.Context, conn *common.Connection) (*health.HealthStatus, error) {
	logger.Infow(ctx, "get-health-status", log.Fields{"remote": conn})
	return &health.HealthStatus{State: health.HealthStatus_HEALTHY}, nil
}
func (oo *OpenONUACInterAdapter) OnuIndication(ctx context.Context, onuInd *ia.OnuIndicationMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OnuIndication(ctx, onuInd)
}
func (oo *OpenONUACInterAdapter) OmciIndication(ctx context.Context, msg *ia.OmciMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OmciIndication(ctx, msg)
}
func (oo *OpenONUACInterAdapter) DownloadTechProfile(ctx context.Context, tProfile *ia.TechProfileDownloadMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DownloadTechProfile(ctx, tProfile)
}
func (oo *OpenONUACInterAdapter) DeleteGemPort(ctx context.Context, gPort *ia.DeleteGemPortMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteGemPort(ctx, gPort)
}
func (oo *OpenONUACInterAdapter) DeleteTCont(ctx context.Context, tConf *ia.DeleteTcontMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteTCont(ctx, tConf)
}

//stop terminates the session
func (oo *OpenONUACInterAdapter) Stop(ctx context.Context) error {
	close(oo.exitChannel)
	logger.Info(ctx, "openonu-inter-adapter-stopped")
	return nil
}

// KeepAlive is used by the voltha core to open a stream connection with the onu adapter.
func (oo *OpenONUACInterAdapter) KeepAlive(remote onu_inter_adapter_service.OnuInterAdapterService_KeepAliveServer) error {
	ctx := context.Background()
	logger.Debugw(ctx, "receive-stream-connection", log.Fields{"remote": remote})

	if remote == nil {
		return fmt.Errorf("conn-is-nil %v", remote)
	}
	initialRequestTime := time.Now()
	var err error
loop:
	for {
		select {
		case <-remote.Context().Done():
			logger.Infow(ctx, "stream-keep-alive-context-done", log.Fields{"remote": remote, "error": remote.Context().Err()})
			break loop
		case <-oo.exitChannel:
			logger.Warnw(ctx, "received-stop", log.Fields{"remote": remote, "initial-conn-time": initialRequestTime})
			break loop
		default:
		}

		remote, err := remote.Recv()
		if err != nil {
			logger.Warnw(ctx, "received-stream-error", log.Fields{"remote": remote, "error": err})
			break loop
		}
		oo.onuAdapter.updateReachabilityFromRemote(context.Background(), remote)
		logger.Debugw(ctx, "received-keep-alive", log.Fields{"remote": remote})
	}
	logger.Errorw(ctx, "connection-down", log.Fields{"remote": remote, "error": err, "initial-conn-time": initialRequestTime})
	return err
}
