//Package adaptercoreont provides the utility for olt devices, flows and statistics
package adaptercoreont

import (
	"context"
	"reflect"
	"runtime"

	"github.com/opencord/voltha-lib-go/v3/pkg/log"
)

// DeviceState ONT Device state
type DeviceState int

//ONT device state machine may have to be adapted to specific ONT Adapter device needd ... !!!!!

const (
	// deviceStateNull OLT is not instantiated
	deviceStateNull DeviceState = iota
	// deviceStateInit OLT is instantiated
	deviceStateInit
	// deviceStateConnected Grpc session established with OLT
	deviceStateConnected
	// deviceStateUp Admin state of OLT is UP
	deviceStateUp
	// deviceStateDown Admin state of OLT is down
	deviceStateDown
)

// Trigger for changing the state
type Trigger int

const (
	// DeviceInit Go to Device init state
	DeviceInit Trigger = iota
	// GrpcConnected Go to connected state
	GrpcConnected
	// DeviceUpInd Go to Device up state
	DeviceUpInd
	// DeviceDownInd Go to Device down state
	DeviceDownInd
	// GrpcDisconnected Go to Device init state
	GrpcDisconnected
)

// TransitionHandler function type for handling transition
type TransitionHandler func(ctx context.Context) error

// Transition to store state machine
type Transition struct {
	previousState []DeviceState
	currentState  DeviceState
	before        []TransitionHandler
	after         []TransitionHandler
}

// TransitionMap to store all the states and current device state
type TransitionMap struct {
	transitions        map[Trigger]Transition
	currentDeviceState DeviceState
}

//    OpenOnuDevice state machine:
//
//        null ----> init ------> connected -----> up -----> down
//                   ^ ^             |             ^         | |
//                   | |             |             |         | |
//                   | +-------------+             +---------+ |
//                   |                                         |
//                   +-----------------------------------------+

// NewTransitionMap create a new state machine with all the transitions
func NewTransitionMap(dh *DeviceHandler) *TransitionMap {
	var transitionMap TransitionMap
	transitionMap.currentDeviceState = deviceStateNull
	transitionMap.transitions = make(map[Trigger]Transition)
	// In doInit create the Pon port, then set the device communication
	transitionMap.transitions[DeviceInit] =
		Transition{
			previousState: []DeviceState{deviceStateNull, deviceStateDown},
			currentState:  deviceStateInit,
			before:        []TransitionHandler{dh.doStateInit},
			after:         []TransitionHandler{dh.postInit}}
	// not yet relevant?
	transitionMap.transitions[GrpcDisconnected] =
		Transition{
			previousState: []DeviceState{deviceStateConnected, deviceStateDown},
			currentState:  deviceStateInit,
			before:        []TransitionHandler{dh.doStateInit},
			after:         []TransitionHandler{dh.postInit}}
	// in doConnected, create logical device and read the indications
	transitionMap.transitions[GrpcConnected] =
		Transition{
			previousState: []DeviceState{deviceStateInit},
			currentState:  deviceStateConnected,
			before:        []TransitionHandler{dh.doStateConnected}}

	// Once the olt UP is indication received, then do state up
	transitionMap.transitions[DeviceUpInd] =
		Transition{
			previousState: []DeviceState{deviceStateConnected, deviceStateDown},
			currentState:  deviceStateUp,
			before:        []TransitionHandler{dh.doStateUp}}
	// If olt DOWN indication comes then do sate down
	transitionMap.transitions[DeviceDownInd] =
		Transition{
			previousState: []DeviceState{deviceStateUp},
			currentState:  deviceStateDown,
			before:        []TransitionHandler{dh.doStateDown}}

	return &transitionMap
}

// funcName gets the handler function name
func funcName(f interface{}) string {
	p := reflect.ValueOf(f).Pointer()
	rf := runtime.FuncForPC(p)
	return rf.Name()
}

// isValidTransition checks for the new state transition is valid from current state
func (tMap *TransitionMap) isValidTransition(trigger Trigger) bool {
	// Validate the state transition
	for _, state := range tMap.transitions[trigger].previousState {
		if tMap.currentDeviceState == state {
			return true
		}
	}
	return false
}

// Handle moves the state machine to next state based on the trigger and invokes the before and
// after handlers if the transition is a valid transition
func (tMap *TransitionMap) Handle(ctx context.Context, trigger Trigger) {

	// Check whether the transtion is valid from current state
	if !tMap.isValidTransition(trigger) {
		log.Errorw("Invalid transition triggered ", log.Fields{"CurrentState": tMap.currentDeviceState, "Trigger": trigger})
		return
	}

	// Invoke the before handlers
	beforeHandlers := tMap.transitions[trigger].before
	if beforeHandlers == nil {
		log.Debugw("No handlers for before", log.Fields{"trigger": trigger})
	}
	for _, handler := range beforeHandlers {
		log.Debugw("running-before-handler", log.Fields{"handler": funcName(handler)})
		if err := handler(ctx); err != nil {
			// TODO handle error
			log.Error(err)
			return
		}
	}

	// Update the state
	tMap.currentDeviceState = tMap.transitions[trigger].currentState
	log.Debugw("Updated device state ", log.Fields{"CurrentDeviceState": tMap.currentDeviceState})

	// Invoke the after handlers
	afterHandlers := tMap.transitions[trigger].after
	if afterHandlers == nil {
		log.Debugw("No handlers for after", log.Fields{"trigger": trigger})
	}
	for _, handler := range afterHandlers {
		log.Debugw("running-after-handler", log.Fields{"handler": funcName(handler)})
		if err := handler(ctx); err != nil {
			// TODO handle error
			log.Error(err)
			return
		}
	}
}
