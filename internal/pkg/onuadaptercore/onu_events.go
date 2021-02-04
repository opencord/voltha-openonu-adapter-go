/*
 * Copyright 2021-present Open Networking Foundation

 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at

 * http://www.apache.org/licenses/LICENSE-2.0

 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package adaptercoreonu

import (
	"context"
	"errors"
	"github.com/opencord/voltha-lib-go/v4/pkg/events/eventif"
	"github.com/opencord/voltha-protos/v4/go/voltha"
	me "github.com/opencord/omci-lib-go/generated"
)

type onuEvents map[onuDevice]onuDeviceEvent
type onuDevice struct {
	classID me.ClassID
	alarmno uint8
}
type onuDeviceEvent struct {
	EventName        string
	EventCategory    eventif.EventCategory
	EventSubCategory eventif.EventSubCategory
	EventDescription string
}

const (
	CircuitPackClassID                             = me.CircuitPackClassID
	PhysicalPathTerminationPointEthernetUniClassID = me.PhysicalPathTerminationPointEthernetUniClassID
	OnuGClassID                                    = me.OnuGClassID
	AniGClassID                                    = me.AniGClassID
)

func getOnuEventDetailsByClassIDAndAlarmNo(classID me.ClassID, alarmNo uint8) onuDeviceEvent {
	onuEventList := func() onuEvents {
		onuEventsList := make(map[onuDevice]onuDeviceEvent)
		onuEventsList[onuDevice{classID: CircuitPackClassID, alarmno: 0}] = onuDeviceEvent{EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Equipment alarm"}
		onuEventsList[onuDevice{classID: CircuitPackClassID, alarmno: 2}] = onuDeviceEvent{EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Self-test failure"}
		onuEventsList[onuDevice{classID: CircuitPackClassID, alarmno: 3}] = onuDeviceEvent{EventName: "ONU_LASER_EOL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Laser end of life"}
		onuEventsList[onuDevice{classID: CircuitPackClassID, alarmno: 4}] = onuDeviceEvent{EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Temperature yellow"}
		onuEventsList[onuDevice{classID: CircuitPackClassID, alarmno: 5}] = onuDeviceEvent{EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Temperature red"}
		onuEventsList[onuDevice{classID: PhysicalPathTerminationPointEthernetUniClassID, alarmno: 0}] =
			onuDeviceEvent{EventName: "ONU_Ethernet_UNI", EventCategory: voltha.EventCategory_EQUIPMENT,
				EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "LAN Loss Of Signal"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 0}] = onuDeviceEvent{EventName: "ONU_EQUIPMENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Equipment alarm"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 6}] = onuDeviceEvent{EventName: "ONU_SELF_TEST_FAIL",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Self-test failure"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 7}] = onuDeviceEvent{EventName: "ONU_DYING_GASP",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Dying gasp"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 8}] = onuDeviceEvent{EventName: "ONU_TEMP_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Temperature yellow"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 9}] = onuDeviceEvent{EventName: "ONU_TEMP_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Temperature red"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 10}] = onuDeviceEvent{EventName: "ONU_VOLTAGE_YELLOW",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Voltage yellow"}
		onuEventsList[onuDevice{classID: OnuGClassID, alarmno: 11}] = onuDeviceEvent{EventName: "ONU_VOLTAGE_RED",
			EventCategory: voltha.EventCategory_ENVIRONMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Voltage red"}
		onuEventsList[onuDevice{classID: AniGClassID, alarmno: 0}] = onuDeviceEvent{EventName: "ONU_LOW_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Low received optical power"}
		onuEventsList[onuDevice{classID: AniGClassID, alarmno: 1}] = onuDeviceEvent{EventName: "ONU_HIGH_RX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "High received optical power"}
		onuEventsList[onuDevice{classID: AniGClassID, alarmno: 4}] = onuDeviceEvent{EventName: "ONU_LOW_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Low transmit optical power"}
		onuEventsList[onuDevice{classID: AniGClassID, alarmno: 5}] = onuDeviceEvent{EventName: "ONU_HIGH_TX_OPTICAL",
			EventCategory: voltha.EventCategory_COMMUNICATION, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "High transmit optical power"}
		onuEventsList[onuDevice{classID: AniGClassID, alarmno: 6}] = onuDeviceEvent{EventName: "ONU_LASER_BIAS_CURRENT",
			EventCategory: voltha.EventCategory_EQUIPMENT, EventSubCategory: voltha.EventSubCategory_ONU, EventDescription: "Laser bias current"}
		return onuEventsList
	}()
	return onuEventList[onuDevice{classID: classID, alarmno: alarmNo}]
}

// getDeviceEventData returns the event data for a device
func getDeviceEventData(ctx context.Context, classID me.ClassID, alarmNo uint8) (onuDeviceEvent, error) {
	onuEventDetails := getOnuEventDetailsByClassIDAndAlarmNo(classID, alarmNo)
	if onuEventDetails == (onuDeviceEvent{}) {
		return onuEventDetails, errors.New("onu Event Detail not found")
	}
	return onuEventDetails, nil
}

