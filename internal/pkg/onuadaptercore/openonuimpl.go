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

//Package adaptercoreonu provides the utility for onu devices, flows and statistics
package adaptercoreonu

import (
	"context"
	"errors"
	//"github.com/opencord/voltha-lib-go/v4/pkg/log"
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

// do not use MibAudit
const cMibAuditDelayImpl = 0

//suppose global methods per adapter ...
func mibDbVolatileDictImpl(ctx context.Context) error {
	logger.Debug(ctx, "MibVolatileDict-called")
	return errors.New("not_implemented")
}

/*
func alarmDbDictImpl() error {
	logger.Debug("AlarmDb-called")
	return errors.New("not_implemented")
}
*/
