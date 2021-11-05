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
	"github.com/opencord/voltha-protos/v5/go/adapter_services"
	"github.com/opencord/voltha-protos/v5/go/common"
	ic "github.com/opencord/voltha-protos/v5/go/inter_container"
	"github.com/opencord/voltha-protos/v5/go/voltha"
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
func (oo *OpenONUACInterAdapter) GetHealthStatus(ctx context.Context, empty *empty.Empty) (*voltha.HealthStatus, error) {
	return &voltha.HealthStatus{State: voltha.HealthStatus_HEALTHY}, nil
}

func (oo *OpenONUACInterAdapter) OnuIndication(ctx context.Context, onuInd *ic.OnuIndicationMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OnuIndication(ctx, onuInd)
}

func (oo *OpenONUACInterAdapter) OmciIndication(ctx context.Context, msg *ic.OmciMessage) (*empty.Empty, error) {
	return oo.onuAdapter.OmciIndication(ctx, msg)
}

func (oo *OpenONUACInterAdapter) DownloadTechProfile(ctx context.Context, tProfile *ic.TechProfileDownloadMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DownloadTechProfile(ctx, tProfile)
}

func (oo *OpenONUACInterAdapter) DeleteGemPort(ctx context.Context, gPort *ic.DeleteGemPortMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteGemPort(ctx, gPort)
}

func (oo *OpenONUACInterAdapter) DeleteTCont(ctx context.Context, tConf *ic.DeleteTcontMessage) (*empty.Empty, error) {
	return oo.onuAdapter.DeleteTCont(ctx, tConf)
}

//stop terminates the session
func (oo *OpenONUACInterAdapter) Stop(ctx context.Context) error {
	close(oo.exitChannel)
	logger.Info(ctx, "openonu-inter-adapter-stopped")
	return nil
}

func (oo *OpenONUACInterAdapter) KeepAliveConnection(conn *common.Connection, remote adapter_services.OnuInterAdapterService_KeepAliveConnectionServer) error {
	logger.Debugw(context.Background(), "receive-stream-connection", log.Fields{"connection": conn})

	if conn == nil {
		return fmt.Errorf("conn-is-nil %v", conn)
	}
	var err error
	oo.onuAdapter.updateParentStreamConnection(conn.Endpoint, true)
loop:
	for {
		keepAliveTimer := time.NewTimer(time.Duration(conn.KeepAliveInterval))
		select {
		case <-keepAliveTimer.C:
			if err = remote.Send(&common.Connection{Endpoint: oo.onuAdapter.config.AdapterEndpoint}); err != nil {
				break loop
			}
		case <-oo.exitChannel:
			logger.Warnw(context.Background(), "received-stop", log.Fields{"remote": conn.Endpoint})
			break loop
		}
	}
	oo.onuAdapter.updateParentStreamConnection(conn.Endpoint, false)

	logger.Errorw(context.Background(), "connection-down", log.Fields{"remote": conn.Endpoint, "error": err})
	return err
}
