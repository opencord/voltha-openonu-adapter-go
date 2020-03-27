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

//Package main -> this is the entry point of the OpenAdapter
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/opencord/voltha-lib-go/v3/pkg/adapters"
	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"
	com "github.com/opencord/voltha-lib-go/v3/pkg/adapters/common"
	"github.com/opencord/voltha-lib-go/v3/pkg/db/kvstore"
	"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	"github.com/opencord/voltha-lib-go/v3/pkg/probe"
	ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	"github.com/opencord/voltha-protos/v3/go/voltha"

	ac "test.internal/openadapter/adaptercoreont"
	"test.internal/openadapter/config"
	"test.internal/openadapter/config/version"
)

type adapter struct {
	defaultAppName   string
	instanceID       string
	config           *config.AdapterFlags
	iAdapter         adapters.IAdapter // from Voltha interface adapters
	kafkaClient      kafka.Client
	kvClient         kvstore.Client
	kip              kafka.InterContainerProxy
	coreProxy        adapterif.CoreProxy
	adapterProxy     adapterif.AdapterProxy
	eventProxy       adapterif.EventProxy
	halted           bool
	exitChannel      chan int
	receiverChannels []<-chan *ic.InterContainerMessage //from inter-container
}

//package single start function (run at first package instantiation)
func init() {
	_, _ = log.AddPackage(log.CONSOLE, log.DebugLevel, nil)
}

func newAdapter(cf *config.AdapterFlags) *adapter {
	var a adapter
	a.instanceID = cf.InstanceID
	a.config = cf
	a.halted = false
	a.exitChannel = make(chan int, 1)
	a.receiverChannels = make([]<-chan *ic.InterContainerMessage, 0)
	return &a
}

func (a *adapter) start(ctx context.Context) error {
	log.Info("Starting Core Adapter components")
	var err error

	var p *probe.Probe
	if value := ctx.Value(probe.ProbeContextKey); value != nil {
		if _, ok := value.(*probe.Probe); ok {
			p = value.(*probe.Probe)
			p.RegisterService(
				"message-bus",
				"kv-store",
				"container-proxy",
				"core-request-handler",
				"register-with-core",
			)
		}
	}

	// Setup KV Client
	log.Debugw("create-kv-client", log.Fields{"kvstore": a.config.KVStoreType})
	if err = a.setKVClient(); err != nil {
		log.Fatal("error-setting-kv-client")
	}

	if p != nil {
		p.UpdateStatus("kv-store", probe.ServiceStatusRunning)
	}

	// Setup Kafka Client
	if a.kafkaClient, err = newKafkaClient("sarama", a.config.KafkaAdapterHost, a.config.KafkaAdapterPort); err != nil {
		log.Fatal("Unsupported-common-client")
	}

	if p != nil {
		p.UpdateStatus("message-bus", probe.ServiceStatusRunning)
	}

	// Start the common InterContainer Proxy - retries as per program arguments or indefinitely per default
	if a.kip, err = a.startInterContainerProxy(ctx, a.config.KafkaReconnectRetries); err != nil {
		log.Fatal("error-starting-inter-container-proxy")
		//aborting the complete processing here (does notmake sense after set Retry number [else use -1 for infinite])
		return err
	}

	// Create the core proxy to handle requests to the Core
	a.coreProxy = com.NewCoreProxy(a.kip, a.config.Topic, a.config.CoreTopic)

	// Create the adaptor proxy to handle request between olt and onu
	//a.adapterProxy = com.NewAdapterProxy(a.kip, "brcm_openomci_onu", a.config.CoreTopic)
	a.adapterProxy = com.NewAdapterProxy(a.kip, "openolt", a.config.CoreTopic)

	// Create the event proxy to post events to KAFKA
	a.eventProxy = com.NewEventProxy(com.MsgClient(a.kafkaClient), com.MsgTopic(kafka.Topic{Name: a.config.EventTopic}))

	// Create the open ONT interface adapter
	if a.iAdapter, err = a.startVolthaInterfaceAdapter(ctx, a.kip, a.coreProxy, a.adapterProxy, a.eventProxy,
		a.config); err != nil {
		log.Fatal("error-starting-VolthaInterfaceAdapter for OpenOnt")
	}

	// Register the core request handler
	if err = a.setupRequestHandler(ctx, a.instanceID, a.iAdapter); err != nil {
		log.Fatal("error-setting-core-request-handler")
	}

	// Register this adapter to the Core - retries indefinitely
	if err = a.registerWithCore(ctx, -1); err != nil {
		log.Fatal("error-registering-with-core")
	}

	// check the readiness and liveliness and update the probe status
	a.checkServicesReadiness(ctx)
	return err
}

func (a *adapter) stop(ctx context.Context) {
	// Stop leadership tracking
	a.halted = true

	// send exit signal
	a.exitChannel <- 0

	// Cleanup - applies only if we had a kvClient
	if a.kvClient != nil {
		// Release all reservations
		if err := a.kvClient.ReleaseAllReservations(ctx); err != nil {
			log.Infow("fail-to-release-all-reservations", log.Fields{"error": err})
		}
		// Close the DB connection
		a.kvClient.Close()
	}

	// TODO:  More cleanup
}

// #############################################
// Adapter Utility methods ##### begin #########

func (a *adapter) getAdapterInstance() string {
	return a.instanceID
}

func newKVClient(storeType, address string, timeout int) (kvstore.Client, error) {

	log.Infow("kv-store-type", log.Fields{"store": storeType})
	switch storeType {
	case "consul":
		return kvstore.NewConsulClient(address, timeout)
	case "etcd":
		return kvstore.NewEtcdClient(address, timeout)
	}
	return nil, errors.New("unsupported-kv-store")
}

func newKafkaClient(clientType, host string, port int) (kafka.Client, error) {

	log.Infow("common-client-type", log.Fields{"client": clientType})
	switch clientType {
	case "sarama":
		return kafka.NewSaramaClient(
			kafka.Host(host),
			kafka.Port(port),
			kafka.ProducerReturnOnErrors(true),
			kafka.ProducerReturnOnSuccess(true),
			kafka.ProducerMaxRetries(6),
			kafka.ProducerRetryBackoff(time.Millisecond*30),
			kafka.MetadatMaxRetries(15)), nil
	}

	return nil, errors.New("unsupported-client-type")
}

func (a *adapter) setKVClient() error {
	addr := a.config.KVStoreHost + ":" + strconv.Itoa(a.config.KVStorePort)
	client, err := newKVClient(a.config.KVStoreType, addr, a.config.KVStoreTimeout)
	if err != nil {
		a.kvClient = nil
		log.Error(err)
		return err
	}
	a.kvClient = client
	return nil
}

func (a *adapter) startInterContainerProxy(ctx context.Context, retries int) (kafka.InterContainerProxy, error) {
	log.Infow("starting-intercontainer-messaging-proxy", log.Fields{"host": a.config.KafkaAdapterHost,
		"port": a.config.KafkaAdapterPort, "topic": a.config.Topic})
	var err error
	kip := kafka.NewInterContainerProxy(
		kafka.InterContainerHost(a.config.KafkaAdapterHost),
		kafka.InterContainerPort(a.config.KafkaAdapterPort),
		kafka.MsgClient(a.kafkaClient),
		kafka.DefaultTopic(&kafka.Topic{Name: a.config.Topic}))
	var count uint8 = 0
	for {
		if err = kip.Start(); err != nil {
			log.Warnw("error-starting-messaging-proxy", log.Fields{"error": err, "retry": retries, "count": count})
			if retries == int(count) {
				return nil, err
			}
			count++
			// Take a nap before retrying
			time.Sleep(2 * time.Second)
		} else {
			break
		}
	}
	probe.UpdateStatusFromContext(ctx, "container-proxy", probe.ServiceStatusRunning)
	log.Info("common-messaging-proxy-created")
	return kip, nil
}

/*
func (a *adapter) startOpenOLT(ctx context.Context, kip kafka.InterContainerProxy,
	cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep adapterif.EventProxy,
	cfg *config.AdapterFlags) (*ac.OpenOLT, error) {
	log.Info("starting-open-olt")
	var err error
	sOLT := ac.NewOpenOLT(ctx, a.kip, cp, ap, ep, cfg)

	if err = sOLT.Start(ctx); err != nil {
		log.Fatalw("error-starting-messaging-proxy", log.Fields{"error": err})
		return nil, err
	}

	log.Info("open-olt-started")
	return sOLT, nil
}
func (a *adapter) startVolthaInterfaceAdapter(ctx context.Context, kip kafka.InterContainerProxy,
	cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep adapterif.EventProxy,
	cfg *config.AdapterFlags) (adapters.IAdapter, error) {
*/

func (a *adapter) startVolthaInterfaceAdapter(ctx context.Context, kip kafka.InterContainerProxy,
	cp adapterif.CoreProxy, ap adapterif.AdapterProxy, ep adapterif.EventProxy,
	cfg *config.AdapterFlags) (*ac.OpenONUAC, error) {
	var err error
	sAcONT := ac.NewOpenONUAC(ctx, a.kip, cp, ap, ep, cfg)

	if err = sAcONT.Start(ctx); err != nil {
		log.Fatalw("error-starting-messaging-proxy", log.Fields{"error": err})
		return nil, err
	}

	log.Info("open-ont-adaptercore-started")
	return sAcONT, nil
}

func (a *adapter) setupRequestHandler(ctx context.Context, coreInstanceID string, iadapter adapters.IAdapter) error {
	log.Info("setting-request-handler")
	requestProxy := com.NewRequestHandlerProxy(coreInstanceID, iadapter, a.coreProxy)
	if err := a.kip.SubscribeWithRequestHandlerInterface(kafka.Topic{Name: a.config.Topic}, requestProxy); err != nil {
		log.Errorw("request-handler-setup-failed", log.Fields{"error": err})
		return err

	}
	probe.UpdateStatusFromContext(ctx, "core-request-handler", probe.ServiceStatusRunning)
	log.Info("request-handler-setup-done")
	return nil
}

func (a *adapter) registerWithCore(ctx context.Context, retries int) error {
	log.Info("registering-with-core")
	/*
		adapterDescription := &voltha.Adapter{Id: "openolt", // Unique name for the device type
			Vendor:  "VOLTHA OpenOLT",
			Version: version.VersionInfo.Version}
		types := []*voltha.DeviceType{{Id: "openolt",
			Adapter:                     "openolt", // Name of the adapter that handles device type
			AcceptsBulkFlowUpdate:       false,     // Currently openolt adapter does not support bulk flow handling
			AcceptsAddRemoveFlowUpdates: true}}
	*/
	adapterDescription := &voltha.Adapter{Id: "brcm_openomci_onu", // Unique name for the device type ->exact type required for OLT comm????
		Vendor:  "VOLTHA OpenONU", //Vendor:  "VOLTHA OpenONUGo",
		Version: version.VersionInfo.Version}
	types := []*voltha.DeviceType{{Id: "brcm_openomci_onu",
		VendorIds:                   []string{"OPEN", "ALCL", "BRCM", "TWSH", "ALPH", "ISKT", "SFAA", "BBSM", "SCOM"},
		Adapter:                     "brcm_openomci_onu", // Name of the adapter that handles device type
		AcceptsBulkFlowUpdate:       false,               // Currently openolt adapter does not support bulk flow handling
		AcceptsAddRemoveFlowUpdates: true}}
	deviceTypes := &voltha.DeviceTypes{Items: types}
	count := 0
	for {
		if err := a.coreProxy.RegisterAdapter(context.TODO(), adapterDescription, deviceTypes); err != nil {
			log.Warnw("registering-with-core-failed", log.Fields{"error": err})
			if retries == count {
				return err
			}
			count++
			// Take a nap before retrying
			time.Sleep(2 * time.Second)
		} else {
			break
		}
	}
	probe.UpdateStatusFromContext(ctx, "register-with-core", probe.ServiceStatusRunning)
	log.Info("registered-with-core")
	return nil
}

/**
This function checks the liveliness and readiness of the kakfa and kv-client services
and update the status in the probe.
*/
func (a *adapter) checkServicesReadiness(ctx context.Context) {
	// checks the kafka readiness
	go a.checkKafkaReadiness(ctx)

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

	// Default false to check the liveliness.
	kvStoreChannel <- false
	for {
		timeoutTimer := time.NewTimer(timeout)
		select {
		case liveliness := <-kvStoreChannel:
			if !liveliness {
				// kv-store not reachable or down, updating the status to not ready state
				probe.UpdateStatusFromContext(ctx, "kv-store", probe.ServiceStatusNotReady)
				timeout = a.config.NotLiveProbeInterval
			} else {
				// kv-store is reachable , updating the status to running state
				probe.UpdateStatusFromContext(ctx, "kv-store", probe.ServiceStatusRunning)
				timeout = a.config.LiveProbeInterval / 2
			}
			// Check if the timer has expired or not
			if !timeoutTimer.Stop() {
				<-timeoutTimer.C
			}
		case <-timeoutTimer.C:
			// Check the status of the kv-store
			log.Info("kv-store liveliness-recheck")
			if a.kvClient.IsConnectionUp(ctx) {
				kvStoreChannel <- true
			} else {
				kvStoreChannel <- false
			}
		}
	}
}

/**
This function checks the liveliness and readiness of the kafka service
and update the status in the probe.
*/
func (a *adapter) checkKafkaReadiness(ctx context.Context) {
	livelinessChannel := a.kafkaClient.EnableLivenessChannel(true)
	healthinessChannel := a.kafkaClient.EnableHealthinessChannel(true)
	timeout := a.config.LiveProbeInterval
	for {
		timeoutTimer := time.NewTimer(timeout)

		select {
		case healthiness := <-healthinessChannel:
			if !healthiness {
				// log.Fatal will call os.Exit(1) to terminate
				log.Fatal("Kafka service has become unhealthy")
			}
		case liveliness := <-livelinessChannel:
			if !liveliness {
				// kafka not reachable or down, updating the status to not ready state
				probe.UpdateStatusFromContext(ctx, "message-bus", probe.ServiceStatusNotReady)
				timeout = a.config.NotLiveProbeInterval
			} else {
				// kafka is reachable , updating the status to running state
				probe.UpdateStatusFromContext(ctx, "message-bus", probe.ServiceStatusRunning)
				timeout = a.config.LiveProbeInterval
			}
			// Check if the timer has expired or not
			if !timeoutTimer.Stop() {
				<-timeoutTimer.C
			}
		case <-timeoutTimer.C:
			log.Info("kafka-proxy-liveness-recheck")
			// send the liveness probe in a goroutine; we don't want to deadlock ourselves as
			// the liveness probe may wait (and block) writing to our channel.
			err := a.kafkaClient.SendLiveness()
			if err != nil {
				// Catch possible error case if sending liveness after Sarama has been stopped.
				log.Warnw("error-kafka-send-liveness", log.Fields{"error": err})
			}
		}
	}
}

// Adapter Utility methods ##### end   #########
// #############################################

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
			log.Infow("Adapter run aborted due to internal errors", log.Fields{"context": "done"})
			exitChannel <- 2
		case s := <-signalChannel:
			switch s {
			case syscall.SIGHUP,
				syscall.SIGINT,
				syscall.SIGTERM,
				syscall.SIGQUIT:
				log.Infow("closing-signal-received", log.Fields{"signal": s})
				exitChannel <- 0
			default:
				log.Infow("unexpected-signal-received", log.Fields{"signal": s})
				exitChannel <- 1
			}
		}
	}()

	code := <-exitChannel
	return code
}

func main() {
	start := time.Now()

	cf := config.NewAdapterFlags()
	defaultAppName := cf.InstanceID + "_" + version.GetCodeVersion()
	cf.ParseCommandArguments()

	// Setup logging

	loglevel := log.StringToInt(cf.LogLevel)

	// Setup default logger - applies for packages that do not have specific logger set
	if _, err := log.SetDefaultLogger(log.JSON, loglevel, log.Fields{"instanceId": cf.InstanceID}); err != nil {
		log.With(log.Fields{"error": err}).Fatal("Cannot setup logging")
	}

	// Update all loggers (provisionned via init) with a common field
	if err := log.UpdateAllLoggers(log.Fields{"instanceId": cf.InstanceID}); err != nil {
		log.With(log.Fields{"error": err}).Fatal("Cannot setup logging")
	}

	log.SetAllLogLevel(loglevel)

	defer log.CleanUp()

	// Print version / build information and exit
	if cf.DisplayVersionOnly {
		printVersion(defaultAppName)
		return
	} else {
		log.Infow("config", log.Fields{"StartName": defaultAppName})
		log.Infow("config", log.Fields{"BuildVersion": version.VersionInfo.String("  ")})
		log.Infow("config", log.Fields{"Arguments": os.Args[1:]})
	}

	// Print banner if specified
	if cf.Banner {
		printBanner()
	}

	log.Infow("config", log.Fields{"config": *cf})

	ctx, cancel := context.WithCancel(context.Background())
	//defer cancel()

	ad := newAdapter(cf)

	p := &probe.Probe{}
	log.Infow("resources", log.Fields{"Context": ctx, "Adapter": ad.getAdapterInstance(), "ProbeCoreState": p.GetStatus("register-with-core")})

	go p.ListenAndServe(fmt.Sprintf("%s:%d", ad.config.ProbeHost, ad.config.ProbePort))
	log.Infow("probeState", log.Fields{"ProbeCoreState": p.GetStatus("register-with-core")})

	probeCtx := context.WithValue(ctx, probe.ProbeContextKey, p)

	go func() {
		err := ad.start(probeCtx)
		// If this operation returns an error
		// cancel all operations using this context
		if err != nil {
			cancel()
		}
	}()

	code := waitForExit(ctx)
	log.Infow("received-a-closing-signal", log.Fields{"code": code})

	// Cleanup before leaving
	ad.stop(ctx)

	elapsed := time.Since(start)
	log.Infow("run-time", log.Fields{"Name": "openadapter", "time": elapsed / time.Microsecond})
	//log.Infow("run-time", log.Fields{"instanceId": ad.config.InstanceID, "time": elapsed / time.Second})
}
