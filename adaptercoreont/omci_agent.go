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

//Package adaptercoreont provides the utility for ont devices, flows and statistics
package adaptercoreont

import (
	"context"
	//"errors"
	//"sync"
	//"time"

	"github.com/opencord/voltha-lib-go/v3/pkg/adapters/adapterif"

	//"github.com/opencord/voltha-lib-go/v3/pkg/kafka"
	"github.com/opencord/voltha-lib-go/v3/pkg/log"
	//ic "github.com/opencord/voltha-protos/v3/go/inter_container"
	//"github.com/opencord/voltha-protos/v3/go/openflow_13"
	//"github.com/opencord/voltha-protos/v3/go/voltha"
)

/*
OpenOmciAgentDefaults = {
    'mib-synchronizer': {
        'state-machine': MibSynchronizer,  # Implements the MIB synchronization state machine
        'database': MibDbVolatileDict,     # Implements volatile ME MIB database
        # 'database': MibDbExternal,         # Implements persistent ME MIB database
        'advertise-events': True,          # Advertise events on OpenOMCI event bus
        'audit-delay': 60,                 # Time to wait between MIB audits.  0 to disable audits.
        'tasks': {
            'mib-upload': MibUploadTask,
            'mib-template': MibTemplateTask,
            'get-mds': GetMdsTask,
            'mib-audit': GetMdsTask,
            'mib-resync': MibResyncTask,
            'mib-reconcile': MibReconcileTask
        }
    },
    'omci-capabilities': {
        'state-machine': OnuOmciCapabilities,   # Implements OMCI capabilities state machine
        'advertise-events': False,              # Advertise events on OpenOMCI event bus
        'tasks': {
            'get-capabilities': OnuCapabilitiesTask # Get supported ME and Commands
        }
    },
    'performance-intervals': {
        'state-machine': PerformanceIntervals,  # Implements PM Intervals State machine
        'advertise-events': False,              # Advertise events on OpenOMCI event bus
        'tasks': {
            'sync-time': SyncTimeTask,
            'collect-data': IntervalDataTask,
            'create-pm': OmciCreatePMRequest,
            'delete-pm': OmciDeletePMRequest,
        },
    },
    'alarm-synchronizer': {
        'state-machine': AlarmSynchronizer,    # Implements the Alarm sync state machine
        'database': AlarmDbExternal,           # For any State storage needs
        'advertise-events': True,              # Advertise events on OpenOMCI event bus
        'tasks': {
            'alarm-resync': AlarmResyncTask
        }
     },
    'image_downloader': {
        'state-machine': ImageDownloadeSTM,
        'advertise-event': True,
        'tasks': {
            'download-file': FileDownloadTask
        }
    },
    'image_upgrader': {
        'state-machine': OmciSoftwareImageDownloadSTM,
        'advertise-event': True,
        'tasks': {
            'omci_upgrade_task': OmciSwImageUpgradeTask
        }
    }
    # 'image_activator': {
    #     'state-machine': OmciSoftwareImageActivateSTM,
    #     'advertise-event': True,
    # }
}
*/

//OpenOMCIAgent structure holds the ONT core information
type OpenOMCIAgent struct {
	coreProxy     adapterif.CoreProxy
	adapterProxy  adapterif.AdapterProxy
	started       bool
	deviceEntries map[string]*OnuDeviceEntry
	mibDbClass    func() error
}

//NewOpenOMCIAgent returns a new instance of OpenOMCIAgent
func NewOpenOMCIAgent(ctx context.Context,
	coreProxy adapterif.CoreProxy, adapterProxy adapterif.AdapterProxy) *OpenOMCIAgent {
	log.Info("init-openOmciAgent")
	var openomciagent OpenOMCIAgent
	openomciagent.started = false
	openomciagent.coreProxy = coreProxy
	openomciagent.adapterProxy = adapterProxy
	openomciagent.deviceEntries = make(map[string]*OnuDeviceEntry)
	return &openomciagent
}

//Start starts (logs) the omci agent
func (oo *OpenOMCIAgent) Start(ctx context.Context) error {
	log.Info("starting-openOmciAgent")
	//TODO .....
	//mib_db.start()
	oo.started = true
	log.Info("openOmciAgent-started")
	return nil
}

//Stop terminates the session
func (oo *OpenOMCIAgent) Stop(ctx context.Context) error {
	log.Info("stopping-openOmciAgent")
	oo.started = false
	//oo.exitChannel <- 1
	log.Info("openOmciAgent-stopped")
	return nil
}

//
//Add a new ONU to be managed.

//To provide vendor-specific or custom Managed Entities, create your own Entity
//  ID to class mapping dictionary.

//Since ONU devices can be added at any time (even during Device Handler
//  startup), the ONU device handler is responsible for calling start()/stop()
//  for this object.

//:param device_id: (str) Device ID of ONU to add
//:param core_proxy: (CoreProxy) Remote API to VOLTHA core
//:param adapter_proxy: (AdapterProxy) Remote API to other adapters via VOLTHA core
//:param custom_me_map: (dict) Additional/updated ME to add to class map
//:param support_classes: (dict) State machines and tasks for this ONU

//:return: (OnuDeviceEntry) The ONU device
//
func (oo *OpenOMCIAgent) Add_device(ctx context.Context, device_id string,
	dh *DeviceHandler) (*OnuDeviceEntry, error) {
	log.Info("openOmciAgent-adding-deviceEntry")

	deviceEntry := oo.GetDevice(device_id)
	if deviceEntry == nil {
		/* costum_me_map in python code seems always to be None,
		   we omit that here first (declaration unclear) -> todo at Adapter specialization ...*/
		/* also no 'clock' argument - usage open ...*/
		/* and no alarm_db yet (oo.alarm_db)  */
		deviceEntry = NewOnuDeviceEntry(ctx, device_id, *dh, *oo, oo.coreProxy, oo.adapterProxy,
			oo.mibDbClass, nil)
		oo.deviceEntries[device_id] = deviceEntry
		log.Infow("openOmciAgent-OnuDeviceEntry-added", log.Fields{"for deviceId": device_id})
	} else {
		log.Infow("openOmciAgent-OnuDeviceEntry-add: Device already exists", log.Fields{"for deviceId": device_id})
	}
	// might be updated with some error handling !!!
	return deviceEntry, nil
}

// Get ONU device entry for a specific Id
func (oo *OpenOMCIAgent) GetDevice(device_id string) *OnuDeviceEntry {
	if _, exist := oo.deviceEntries[device_id]; !exist {
		return nil
	} else {
		return oo.deviceEntries[device_id]
	}
}
