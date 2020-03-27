//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"context"
	//"errors"
	//"sync"
	//"time"

	"github.com/opencord/omci-lib-go"
	me "github.com/opencord/omci-lib-go/generated"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

//OmciTestRequest structure holds the information for the OMCI test
type OmciTestRequest struct {
	deviceID     string
	omciAgent    OpenOMCIAgent
	started      bool
	result       bool
	exclusive_cc bool
	allowFailure bool
}

//NewOmciTestRequest returns a new instance of OmciTestRequest
func NewOmciTestRequest(ctx context.Context,
	device_id string, omci_agent OpenOMCIAgent,
	exclusive bool, allow_failure bool) *OmciTestRequest {
	log.Info("omciTestRequest-init")
	var omciTestRequest OmciTestRequest
	omciTestRequest.deviceID = device_id
	omciTestRequest.omciAgent = omci_agent
	omciTestRequest.started = false
	omciTestRequest.result = false
	omciTestRequest.exclusive_cc = exclusive
	omciTestRequest.allowFailure = allow_failure

	return &omciTestRequest
}

//
func (oo *OmciTestRequest) PerformOmciTest(ctx context.Context) {
	log.Info("omciTestRequest-start-test")

	deviceEntry := oo.omciAgent.GetDevice(oo.deviceID)
	if deviceEntry != nil {
		//mibReset, _ := omcilib.CreateMibResetRequest(o.getNextTid(false))
		//sendOmciMsg(mibReset, o.PonPortID, o.ID, o.SerialNumber, "mibReset", client)
		// test functionality is limited to ONU-2G get request for the moment
		// without yet checking the received response automatically here (might be improved ??)
		onu2gBaseGet, _ := oo.CreateOnu2gBaseGet(deviceEntry.DevOmciCC.GetNextTid(false))
		log.Infow("performOmciTest-start sending frame", log.Fields{"for deviceId": oo.deviceID})
		// send with default timeout and normal prio
		go deviceEntry.DevOmciCC.Send(ctx, onu2gBaseGet, ConstDefaultOmciTimeout, 0, false)

	} else {
		log.Errorw("performOmciTest: Device does not exist", log.Fields{"for deviceId": oo.deviceID})
	}
}

// these are OMCI related functions, could/should be collected in a separate file? TODO!!!
// for a simple start just included in here
//basic approach copied from bbsim, cmp /devices/onu.go and /internal/common/omci/mibpackets.go

func (oo *OmciTestRequest) CreateOnu2gBaseGet(tid uint16) ([]byte, error) {

	request := &omci.GetRequest{
		MeBasePacket: omci.MeBasePacket{
			EntityClass:    me.Onu2GClassID,
			EntityInstance: 0, //there is only the 0 instance of ONU2-G (still hard-coded - TODO!!!)
		},
		AttributeMask: 0xE000, //example hardcoded (TODO!!!) request EquId, OmccVersion, VendorCode
	}

	pkt, err := serialize(omci.GetRequestType, request, tid)
	if err != nil {
		//omciLogger.WithFields(log.Fields{ ...
		log.Errorw("Cannot serialize Onu2-G GetRequest", log.Fields{"Err": err})
		return nil, err
	}
	return hexEncode(pkt)
}
