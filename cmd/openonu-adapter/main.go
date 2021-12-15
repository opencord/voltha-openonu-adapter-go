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

//Package main -> this is the entry point of the OpenOnuAdapter
package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	conf "github.com/opencord/voltha-lib-go/v7/pkg/config"
	"github.com/opencord/voltha-lib-go/v7/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v7/pkg/events"
	"github.com/opencord/voltha-lib-go/v7/pkg/events/eventif"
	vgrpc "github.com/opencord/voltha-lib-go/v7/pkg/grpc"
	"github.com/opencord/voltha-lib-go/v7/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v7/pkg/log"
	"github.com/opencord/voltha-lib-go/v7/pkg/probe"
	"github.com/opencord/voltha-lib-go/v7/pkg/version"
	"github.com/opencord/voltha-protos/v5/go/adapter_service"
	"github.com/opencord/voltha-protos/v5/go/core_service"
	"github.com/opencord/voltha-protos/v5/go/onu_inter_adapter_service"
	"github.com/opencord/voltha-protos/v5/go/voltha"
	"google.golang.org/grpc"

	"github.com/opencord/voltha-protos/v5/go/core_adapter"

	"github.com/opencord/voltha-openonu-adapter-go/internal/pkg/config"
	ac "github.com/opencord/voltha-openonu-adapter-go/internal/pkg/core"
)

const (
	clusterMessagingService = "cluster-message-service"
	onuAdapterService       = "onu-adapter-service"
	kvService               = "kv-service"
	coreService             = "core-service"
)

type adapter struct {
	//defaultAppName   string
	instanceID      string
	config          *config.AdapterFlags
	kafkaClient     kafka.Client
	kvClient        kvstore.Client
	eventProxy      eventif.EventProxy
	grpcServer      *vgrpc.GrpcServer
	onuAdapter      *ac.OpenONUAC
	onuInterAdapter *ac.OpenONUACInterAdapter
	coreClient      *vgrpc.Client
}

func newAdapter(cf *config.AdapterFlags) *adapter {
	var a adapter
	a.instanceID = cf.InstanceID
	a.config = cf
	return &a
}

func (a *adapter) start(ctx context.Context) error {
	logger.Info(ctx, "Starting Core Adapter components")
	var err error

	var p *probe.Probe
	if value := ctx.Value(probe.ProbeContextKey); value != nil {
		if _, ok := value.(*probe.Probe); ok {
			p = value.(*probe.Probe)
			p.RegisterService(
				ctx,
				clusterMessagingService,
				kvService,
				onuAdapterService,
				coreService,
			)
		}
	}

	// Setup KV Client
	logger.Debugw(ctx, "create-kv-client", log.Fields{"kvstore": a.config.KVStoreType})
	if err = a.setKVClient(ctx); err != nil {
		logger.Fatalw(ctx, "error-setting-kv-client", log.Fields{"error": err})
	}

	// Setup Log Config
	cm := conf.NewConfigManager(ctx, a.kvClient, a.config.KVStoreType, a.config.KVStoreAddress, a.config.KVStoreTimeout)
	go conf.StartLogLevelConfigProcessing(cm, ctx)

	// Setup Kafka Client
	if a.kafkaClient, err = newKafkaClient(ctx, "sarama", a.config.KafkaClusterAddress); err != nil {
		logger.Fatalw(ctx, "Unsupported-common-client", log.Fields{"error": err})
	}

	// Start kafka communication with the broker
	if err := kafka.StartAndWaitUntilKafkaConnectionIsUp(ctx, a.kafkaClient, a.config.HeartbeatCheckInterval, clusterMessagingService); err != nil {
		logger.Fatal(ctx, "unable-to-connect-to-kafka")
	}

	// Wait until connection to KV store is established
	if err := WaitUntilKvStoreConnectionIsUp(ctx, a.kvClient, a.config.KVStoreTimeout, kvService); err != nil {
		logger.Fatal(ctx, "unable-to-connect-to-kv-store")
	}

	// Create the event proxy to post events to KAFKA
	a.eventProxy = events.NewEventProxy(events.MsgClient(a.kafkaClient), events.MsgTopic(kafka.Topic{Name: a.config.EventTopic}))
	go func() {
		if err := a.eventProxy.Start(); err != nil {
			logger.Fatalw(ctx, "event-proxy-cannot-start", log.Fields{"error": err})
		}
	}()

	// Create the Core client to handle requests to the Core.  Note that the coreClient is an interface and needs to be
	// cast to the appropriate grpc client by invoking GetCoreGrpcClient on the a.coreClient
	if a.coreClient, err = vgrpc.NewClient(
		a.config.AdapterEndpoint,
		a.config.CoreEndpoint,
		"core_service.CoreService",
		a.coreRestarted); err != nil {
		logger.Fatal(ctx, "grpc-client-not-created")
	}
	// Start the core grpc client
	go a.coreClient.Start(ctx, getCoreServiceClientHandler)

	// Create the open ONU interface adapter
	if a.onuAdapter, err = a.startONUAdapter(ctx, a.coreClient, a.eventProxy, a.config, cm); err != nil {
		logger.Fatalw(ctx, "error-starting-startONUAdapter", log.Fields{"error": err})
	}

	// Create the open ONU Inter adapter
	if a.onuInterAdapter, err = a.startONUInterAdapter(ctx, a.onuAdapter); err != nil {
		logger.Fatalw(ctx, "error-starting-startONUInterAdapter", log.Fields{"error": err})
	}

	// Create and start the grpc server
	a.grpcServer = vgrpc.NewGrpcServer(a.config.GrpcAddress, nil, false, p)

	//Register the  adapter  service
	a.addAdapterService(ctx, a.grpcServer, a.onuAdapter)

	//Register the onu inter adapter  service
	a.addOnuInterAdapterService(ctx, a.grpcServer, a.onuInterAdapter)

	go a.startGRPCService(ctx, a.grpcServer, onuAdapterService)

	// Register this adapter to the Core - retries indefinitely
	if err = a.registerWithCore(ctx, coreService, -1); err != nil {
		logger.Fatalw(ctx, "error-registering-with-core", log.Fields{"error": err})
	}

	// Start the readiness and liveliness check and update the probe status
	a.checkServicesReadiness(ctx)
	return err
}

// TODO:  Any action the adapter needs to do following a Core restart?
func (a *adapter) coreRestarted(ctx context.Context, endPoint string) error {
	logger.Errorw(ctx, "core-restarted", log.Fields{"endpoint": endPoint})
	return nil
}

// getCoreServiceClientHandler is used to setup the remote gRPC service
func getCoreServiceClientHandler(ctx context.Context, conn *grpc.ClientConn) interface{} {
	if conn == nil {
		return nil
	}
	return core_service.NewCoreServiceClient(conn)
}

func (a *adapter) stop(ctx context.Context) {
	// Cleanup the grpc services first
	if err := a.onuAdapter.Stop(ctx); err != nil {
		logger.Errorw(ctx, "failure-stopping-onu-adapter-service", log.Fields{"error": err, "adapter": a.config.AdapterEndpoint})
	}
	if err := a.onuInterAdapter.Stop(ctx); err != nil {
		logger.Errorw(ctx, "failure-stopping-onu-inter-adapter-service", log.Fields{"error": err, "adapter": a.config.AdapterEndpoint})
	}
	// Cleanup - applies only if we had a kvClient
	if a.kvClient != nil {
		// Release all reservations
		if err := a.kvClient.ReleaseAllReservations(ctx); err != nil {
			logger.Infow(ctx, "fail-to-release-all-reservations", log.Fields{"error": err})
		}
		// Close the DB connection
		a.kvClient.Close(ctx)
	}

	if a.eventProxy != nil {
		a.eventProxy.Stop()
	}

	if a.kafkaClient != nil {
		a.kafkaClient.Stop(ctx)
	}

	// Stop core client
	if a.coreClient != nil {
		a.coreClient.Stop(ctx)
	}

	// TODO:  More cleanup
}

// #############################################
// Adapter Utility methods ##### begin #########

func newKVClient(ctx context.Context, storeType, address string, timeout time.Duration) (kvstore.Client, error) {
	logger.Infow(ctx, "kv-store-type", log.Fields{"store": storeType})
	switch storeType {
	case "etcd":
		return kvstore.NewEtcdClient(ctx, address, timeout, log.FatalLevel)
	}
	return nil, errors.New("unsupported-kv-store")
}

func newKafkaClient(ctx context.Context, clientType, addr string) (kafka.Client, error) {

	logger.Infow(ctx, "common-client-type", log.Fields{"client": clientType})

	switch clientType {
	case "sarama":
		return kafka.NewSaramaClient(
			kafka.Address(addr),
			kafka.ProducerReturnOnErrors(true),
			kafka.ProducerReturnOnSuccess(true),
			kafka.ProducerMaxRetries(6),
			kafka.ProducerRetryBackoff(time.Millisecond*30),
			kafka.MetadatMaxRetries(15)), nil
	}

	return nil, errors.New("unsupported-client-type")
}

func (a *adapter) setKVClient(ctx context.Context) error {
	client, err := newKVClient(ctx, a.config.KVStoreType, a.config.KVStoreAddress, a.config.KVStoreTimeout)
	if err != nil {
		a.kvClient = nil
		logger.Errorw(ctx, "error-starting-KVClient", log.Fields{"error": err})
		return err
	}
	a.kvClient = client
	return nil
}

func (a *adapter) startONUAdapter(ctx context.Context, cc *vgrpc.Client, ep eventif.EventProxy,
	cfg *config.AdapterFlags, cm *conf.ConfigManager) (*ac.OpenONUAC, error) {
	var err error
	sAcONU := ac.NewOpenONUAC(ctx, cc, ep, a.kvClient, cfg, cm)

	if err = sAcONU.Start(ctx); err != nil {
		logger.Fatalw(ctx, "error-starting-OpenOnuAdapterCore", log.Fields{"error": err})
		return nil, err
	}

	logger.Info(ctx, "open-ont-OpenOnuAdapterCore-started")
	return sAcONU, nil
}

func (a *adapter) startONUInterAdapter(ctx context.Context, onuA *ac.OpenONUAC) (*ac.OpenONUACInterAdapter, error) {
	var err error
	sAcONUInterAdapter := ac.NewOpenONUACAdapter(ctx, onuA)

	if err = sAcONUInterAdapter.Start(ctx); err != nil {
		logger.Fatalw(ctx, "error-starting-OpenONUACInterAdapter", log.Fields{"error": err})
		return nil, err
	}

	logger.Info(ctx, "OpenONUACInterAdapter-started")
	return sAcONUInterAdapter, nil
}

func (a *adapter) registerWithCore(ctx context.Context, serviceName string, retries int) error {
	adapterID := fmt.Sprintf("brcm_openomci_onu_%d", a.config.CurrentReplica)
	vendorIdsList := strings.Split(a.config.OnuVendorIds, ",")
	logger.Infow(ctx, "registering-with-core", log.Fields{
		"adapterID":      adapterID,
		"currentReplica": a.config.CurrentReplica,
		"totalReplicas":  a.config.TotalReplicas,
		"onuVendorIds":   vendorIdsList,
	})
	adapterDescription := &voltha.Adapter{
		Id:             adapterID, // Unique name for the device type ->exact type required for OLT comm????
		Vendor:         "VOLTHA OpenONUGo",
		Version:        version.VersionInfo.Version,
		Endpoint:       a.config.AdapterEndpoint,
		Type:           "brcm_openomci_onu",
		CurrentReplica: int32(a.config.CurrentReplica),
		TotalReplicas:  int32(a.config.TotalReplicas),
	}
	types := []*voltha.DeviceType{{Id: "brcm_openomci_onu",
		VendorIds:                   vendorIdsList,
		AdapterType:                 "brcm_openomci_onu", // Type of adapter that handles this device type
		Adapter:                     "brcm_openomci_onu", // Deprecated attribute
		AcceptsBulkFlowUpdate:       false,               // Currently openolt adapter does not support bulk flow handling
		AcceptsAddRemoveFlowUpdates: true}}
	deviceTypes := &voltha.DeviceTypes{Items: types}
	count := 0
	for {
		gClient, err := a.coreClient.GetCoreServiceClient()
		if gClient != nil {
			if gClient != nil {
				if _, err = gClient.RegisterAdapter(log.WithSpanFromContext(context.TODO(), ctx), &core_adapter.AdapterRegistration{
					Adapter: adapterDescription,
					DTypes:  deviceTypes}); err == nil {
					break
				}
			}
			logger.Warnw(ctx, "registering-with-core-failed", log.Fields{"endpoint": a.config.CoreEndpoint, "error": err, "count": count, "gclient": gClient})
			if retries == count {
				return err
			}
			count++
			// Take a power nap before retrying
			time.Sleep(2 * time.Second)

		}
	}
	probe.UpdateStatusFromContext(ctx, serviceName, probe.ServiceStatusRunning)
	logger.Info(ctx, "registered-with-core")
	return nil
}

// startGRPCService creates the grpc service handlers, registers it to the grpc server and starts the server
func (a *adapter) startGRPCService(ctx context.Context, server *vgrpc.GrpcServer, serviceName string) {
	logger.Infow(ctx, "service-created", log.Fields{"service": serviceName})

	probe.UpdateStatusFromContext(ctx, serviceName, probe.ServiceStatusRunning)
	logger.Infow(ctx, "service-started", log.Fields{"service": serviceName})

	server.Start(ctx)
	probe.UpdateStatusFromContext(ctx, serviceName, probe.ServiceStatusStopped)
}

func (a *adapter) addAdapterService(ctx context.Context, server *vgrpc.GrpcServer, handler adapter_service.AdapterServiceServer) {
	logger.Info(ctx, "adding-adapter-service")

	server.AddService(func(gs *grpc.Server) {
		adapter_service.RegisterAdapterServiceServer(gs, handler)
	})
}

func (a *adapter) addOnuInterAdapterService(ctx context.Context, server *vgrpc.GrpcServer, handler onu_inter_adapter_service.OnuInterAdapterServiceServer) {
	logger.Info(ctx, "adding-onu-inter-adapter-service")

	server.AddService(func(gs *grpc.Server) {
		onu_inter_adapter_service.RegisterOnuInterAdapterServiceServer(gs, handler)
	})
}

/**
This function checks the liveliness and readiness of the kakfa and kv-client services
and update the status in the probe.
*/
func (a *adapter) checkServicesReadiness(ctx context.Context) {
	// checks the kafka readiness
	go kafka.MonitorKafkaReadiness(ctx, a.kafkaClient, a.config.LiveProbeInterval, a.config.NotLiveProbeInterval, clusterMessagingService)

	// checks the kv-store readiness
	go a.checkKvStoreReadiness(ctx)
}

/**
This function checks the liveliness and readiness of the kv-store service
and update the status in the probe.
*/
func (a *adapter) checkKvStoreReadiness(ctx context.Context) {
	// dividing the live probe interval by 2 to get updated status every 30s
	timeout := a.config.LiveProbeInterval / 2
	kvStoreChannel := make(chan bool, 1)

	// Default true - we are here only after we already had a KV store connection
	kvStoreChannel <- true
	for {
		timeoutTimer := time.NewTimer(timeout)
		select {
		case liveliness := <-kvStoreChannel:
			if !liveliness {
				// kv-store not reachable or down, updating the status to not ready state
				probe.UpdateStatusFromContext(ctx, kvService, probe.ServiceStatusNotReady)
				timeout = a.config.NotLiveProbeInterval
			} else {
				// kv-store is reachable , updating the status to running state
				probe.UpdateStatusFromContext(ctx, kvService, probe.ServiceStatusRunning)
				timeout = a.config.LiveProbeInterval / 2
			}
			// Check if the timer has expired or not
			if !timeoutTimer.Stop() {
				<-timeoutTimer.C
			}
		case <-timeoutTimer.C:
			// Check the status of the kv-store
			logger.Info(ctx, "kv-store liveliness-recheck")
			if a.kvClient.IsConnectionUp(ctx) {
				kvStoreChannel <- true
			} else {
				kvStoreChannel <- false
			}
		}
	}
}

// WaitUntilKvStoreConnectionIsUp waits until the KV client can establish a connection to the KV server or until the
// context times out.
func WaitUntilKvStoreConnectionIsUp(ctx context.Context, kvClient kvstore.Client, connectionRetryInterval time.Duration, serviceName string) error {
	if kvClient == nil {
		return errors.New("kvclient-is-nil")
	}
	for {
		if !kvClient.IsConnectionUp(ctx) {
			probe.UpdateStatusFromContext(ctx, serviceName, probe.ServiceStatusNotReady)
			logger.Warnw(ctx, "kvconnection-down", log.Fields{"service-name": serviceName, "connect-retry-interval": connectionRetryInterval})
			select {
			case <-time.After(connectionRetryInterval):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		probe.UpdateStatusFromContext(ctx, serviceName, probe.ServiceStatusRunning)
		logger.Info(ctx, "kv-connection-up")
		break
	}
	return nil
}

// Adapter Utility methods ##### end   #########
// #############################################

func getVerifiedCodeVersion(ctx context.Context) string {
	if version.VersionInfo.Version == "unknown-version" {
		content, err := ioutil.ReadFile("VERSION")
		if err == nil {
			return string(content)
		}
		logger.Error(ctx, "'VERSION'-file not readable")
	}
	return version.VersionInfo.Version
}

func printVersion(appName string) {
	fmt.Println(appName)
	fmt.Println(version.VersionInfo.String("  "))
}

func printBanner() {
	fmt.Println("   ____                     ____  ___     ___    _                  ")
	fmt.Println("  / __ \\                   / __ \\| \\ \\   | | |  | |")
	fmt.Println(" | |  | |_ __   ___ _ __  | |  | | |\\ \\  | | |  | |  ____   ____")
	fmt.Println(" | |  | | '_ \\ / _ \\ '_ \\ | |  | | | \\ \\ | | |  | | / '_ \\ / _' \\")
	fmt.Println(" | |__| | |_) | __/| | | || |__| | |  \\ \\| | \\__/ || (__) | (__) |")
	fmt.Println("  \\___ /| .__/ \\___|_| |_| \\____/|_|   \\___|______| \\.___ |\\___./")
	fmt.Println("        | |                                           __| |")
	fmt.Println("        |_|                                          |____/")
	fmt.Println("                                                                    ")
}

func waitForExit(ctx context.Context) int {
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	exitChannel := make(chan int)

	go func() {
		select {
		case <-ctx.Done():
			logger.Infow(ctx, "Adapter run aborted due to internal errors", log.Fields{"context": "done"})
			exitChannel <- 2
		case s := <-signalChannel:
			switch s {
			case syscall.SIGHUP,
				syscall.SIGINT,
				syscall.SIGTERM,
				syscall.SIGQUIT:
				logger.Infow(ctx, "closing-signal-received", log.Fields{"signal": s})
				exitChannel <- 0
			default:
				logger.Infow(ctx, "unexpected-signal-received", log.Fields{"signal": s})
				exitChannel <- 1
			}
		}
	}()

	code := <-exitChannel
	return code
}

func main() {
	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cf := &config.AdapterFlags{}
	cf.ParseCommandArguments(os.Args[1:])

	defaultAppName := cf.InstanceID + "_" + getVerifiedCodeVersion(ctx)

	// Setup logging

	logLevel, err := log.StringToLogLevel(cf.LogLevel)
	if err != nil {
		logger.Fatalf(ctx, "Cannot setup logging, %s", err)
	}

	// Setup default logger - applies for packages that do not have specific logger set
	if _, err := log.SetDefaultLogger(log.JSON, logLevel, log.Fields{"instanceId": cf.InstanceID}); err != nil {
		logger.With(log.Fields{"error": err}).Fatal(ctx, "Cannot setup logging")
	}

	// Update all loggers (provisioned via init) with a common field
	if err := log.UpdateAllLoggers(log.Fields{"instanceId": cf.InstanceID}); err != nil {
		logger.With(log.Fields{"error": err}).Fatal(ctx, "Cannot setup logging")
	}

	log.SetAllLogLevel(logLevel)

	realMain(ctx) //fatal on httpListen(0,6060) ...

	defer func() {
		_ = log.CleanUp()
	}()
	// Print version / build information and exit
	if cf.DisplayVersionOnly {
		printVersion(defaultAppName)
		return
	}
	logger.Infow(ctx, "config", log.Fields{"StartName": defaultAppName})
	logger.Infow(ctx, "config", log.Fields{"BuildVersion": version.VersionInfo.String("  ")})
	logger.Infow(ctx, "config", log.Fields{"Arguments": os.Args[1:]})

	// Print banner if specified
	if cf.Banner {
		printBanner()
	}

	logger.Infow(ctx, "config", log.Fields{"config": *cf})

	ad := newAdapter(cf)

	p := &probe.Probe{}
	logger.Infow(ctx, "resources", log.Fields{"Context": ctx, "Adapter": ad.instanceID, "ProbeCoreState": p.GetStatus("register-with-core")})

	go p.ListenAndServe(ctx, fmt.Sprintf("%s:%d", ad.config.ProbeHost, ad.config.ProbePort))
	logger.Infow(ctx, "probeState", log.Fields{"ProbeCoreState": p.GetStatus("register-with-core")})

	probeCtx := context.WithValue(ctx, probe.ProbeContextKey, p)

	closer, err := log.GetGlobalLFM().InitTracingAndLogCorrelation(cf.TraceEnabled, cf.TraceAgentAddress, cf.LogCorrelationEnabled)
	if err != nil {
		logger.Warnw(ctx, "unable-to-initialize-tracing-and-log-correlation-module", log.Fields{"error": err})
	} else {
		defer log.TerminateTracing(closer)
	}

	go func() {
		err := ad.start(probeCtx)
		// If this operation returns an error
		// cancel all operations using this context
		if err != nil {
			cancel()
		}
	}()

	code := waitForExit(ctx)
	logger.Infow(ctx, "received-a-closing-signal", log.Fields{"code": code})

	// Set the ONU adapter GRPC service as not ready. This will prevent any request from coming to this adapter instance
	probe.UpdateStatusFromContext(probeCtx, onuAdapterService, probe.ServiceStatusStopped)

	// Cleanup before leaving
	ad.stop(ctx)

	elapsed := time.Since(start)
	logger.Infow(ctx, "run-time", log.Fields{"Name": "openadapter", "time": elapsed / time.Microsecond})
	//logger.Infow(ctx,"run-time", log.Fields{"instanceId": ad.config.InstanceID, "time": elapsed / time.Second})
}
