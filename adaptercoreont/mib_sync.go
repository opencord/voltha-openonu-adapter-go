//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"context"
	"errors"

	"github.com/looplab/fsm"

	//"sync"
	//"time"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

func (onuDeviceEntry *OnuDeviceEntry) logStateChange(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"event name": string(e.Event), "src state": string(e.Src), "dst state": string(e.Dst)})
}

func (onuDeviceEntry *OnuDeviceEntry) enterStartingState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start in State": e.FSM.Current()})

	// create channel and start go routine for processing of MibSync messages
	onuDeviceEntry.MibSyncChan = make(chan Message, 2048)
	go onuDeviceEntry.ProcessMibSyncMessages()
}

func (onuDeviceEntry *OnuDeviceEntry) enterLoadingMibTemplateState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start MibTemplateTask in State": e.FSM.Current()})
	onuDeviceEntry.MibTemplateTask()
}

func (onuDeviceEntry *OnuDeviceEntry) enterUploadingState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start MibUpload in State": e.FSM.Current()})
	onuDeviceEntry.MibUploadTask()
}

func (onuDeviceEntry *OnuDeviceEntry) enterExaminingMdsState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start GetMdsTask in State": e.FSM.Current()})
	onuDeviceEntry.GetMdsTask()
}

func (onuDeviceEntry *OnuDeviceEntry) enterResynchronizingState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start MibResyncTask in State": e.FSM.Current()})
	onuDeviceEntry.MibResyncTask()
}

func (onuDeviceEntry *OnuDeviceEntry) enterAuditingState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start MibResyncTask in State": e.FSM.Current()})
	onuDeviceEntry.MibResyncTask()
}

func (onuDeviceEntry *OnuDeviceEntry) enterOutOfSyncState(e *fsm.Event) {
	log.Debugw("MibSync FSM", log.Fields{"Start  MibReconcileTask in State": e.FSM.Current()})
	onuDeviceEntry.MibReconcileTask()
}

func (onuDeviceEntry *OnuDeviceEntry) ProcessMibSyncMessages( /*ctx context.Context*/ ) {
	log.Debugw("MibSync Msg", log.Fields{"Start routine to process OMCI-messages for device-id": onuDeviceEntry.deviceID})
loop:
	for {
		select {
		// case <-ctx.Done():
		// 	log.Info("MibSync Msg", log.Fields{"Message handling canceled via context for device-id": onuDeviceEntry.deviceID})
		// 	break loop
		case message, ok := <-onuDeviceEntry.MibSyncChan:
			if !ok {
				log.Info("MibSync Msg", log.Fields{"Message couldn't be read from channel for device-id": onuDeviceEntry.deviceID})
				break loop
			}
			log.Debugw("MibSync Msg", log.Fields{"Received message on ONU MibSyncChan for device-id": onuDeviceEntry.deviceID})

			switch message.Type {
			case TestMsg:
				msg, _ := message.Data.(TestMessage)
				onuDeviceEntry.handleTestMsg(msg)
			case OMCI:
				msg, _ := message.Data.(OmciMessage)
				onuDeviceEntry.handleOmciMessage(msg)
			default:
				log.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "message.Type": message.Type})
			}
		}
	}
	log.Info("MibSync Msg", log.Fields{"Stopped handling of MibSyncChan for device-id": onuDeviceEntry.deviceID})
	// TODO: rest FSM
}

func (onuDeviceEntry *OnuDeviceEntry) handleTestMsg(msg TestMessage) {

	log.Debugw("MibSync Msg", log.Fields{"TestMessage received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})

	switch msg.TestMessageVal {
	case AnyTriggerForMibSyncUploadMib:
		onuDeviceEntry.MibSyncFsm.Event("upload_mib")
		log.Debugw("MibSync Msg", log.Fields{"state": string(onuDeviceEntry.MibSyncFsm.Current())})
	default:
		log.Warn("MibSync Msg", log.Fields{"Unknown message type received for device-id": onuDeviceEntry.deviceID, "msg.TestMessageVal": msg.TestMessageVal})
	}
}

func (onuDeviceEntry *OnuDeviceEntry) handleOmciMessage(msg OmciMessage) {

	log.Debugw("MibSync Msg", log.Fields{"OmciMessage received for device-id": onuDeviceEntry.deviceID, "msg.OmciMsg": msg.OmciMsg})
}

func (onuDeviceEntry *OnuDeviceEntry) MibDbVolatileDict() error {
	log.Debug("MibVolatileDict- running")
	return errors.New("not_implemented")
}

func (onuDeviceEntry *OnuDeviceEntry) MibTemplateTask() error {
	return errors.New("not_implemented")
}
func (onuDeviceEntry *OnuDeviceEntry) MibUploadTask() error {
	log.Debugw("MibSync Msg", log.Fields{"send mibReset for device-id": onuDeviceEntry.deviceID})
	onuDeviceEntry.DevOmciCC.sendMibReset(context.TODO(), 30, false)

	return errors.New("partly_implemented")
}
func (onuDeviceEntry *OnuDeviceEntry) GetMdsTask() error {
	return errors.New("not_implemented")
}
func (onuDeviceEntry *OnuDeviceEntry) MibResyncTask() error {
	return errors.New("not_implemented")
}
func (onuDeviceEntry *OnuDeviceEntry) MibReconcileTask() error {
	return errors.New("not_implemented")
}
