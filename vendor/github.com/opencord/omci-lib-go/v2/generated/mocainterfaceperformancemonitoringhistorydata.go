/*
 * Copyright (c) 2018 - present.  Boling Consulting Solutions (bcsw.net)
 * Copyright 2020-present Open Networking Foundation

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
/*
 * NOTE: This file was generated, manual edits will be overwritten!
 *
 * Generated by 'goCodeGenerator.py':
 *              https://github.com/cboling/OMCI-parser/README.md
 */

package generated

import "github.com/deckarep/golang-set"

// MocaInterfacePerformanceMonitoringHistoryDataClassID is the 16-bit ID for the OMCI
// Managed entity MoCA interface performance monitoring history data
const MocaInterfacePerformanceMonitoringHistoryDataClassID = ClassID(164) // 0x00a4

var mocainterfaceperformancemonitoringhistorydataBME *ManagedEntityDefinition

// MocaInterfacePerformanceMonitoringHistoryData (Class ID: #164 / 0x00a4)
//	This ME collects PM data for an MoCA interface. Instances of this ME are created and deleted by
//	the OLT.
//
//	NOTE - The structure of this ME is an exception to the normal definition of PM MEs and normal PM
//	behaviour (clause I.4). It should not be used as a guide for the definition of future MEs. Among
//	other exceptions, this ME contains only current values, which are retrievable by get and get
//	next operations; no history is retained.
//
//	Relationships
//		An instance of this ME is associated with an instance of the PPTP MoCA UNI ME.
//
//	Attributes
//		Managed Entity Id
//			This attribute uniquely identifies each instance of this ME. Through an identical ID, this ME is
//			implicitly linked to an instance of the PPTP MoCA UNI. (R, setbycreate) (mandatory) (2-bytes)
//
//		Interval End Time
//			This attribute identifies the most recently finished 15-min interval. (R) (mandatory) (1-byte)
//
//		Threshold Data 1_2 Id
//			Threshold data 1/2 ID: This attribute points to an instance of the threshold data 1 ME that
//			contains PM threshold values. Since no threshold value attribute number exceeds 7, a threshold
//			data 2 ME is optional. (R,-W, setbycreate) (mandatory) (2-bytes)
//
//		Phy T X Broadcast Rate
//			PHY Tx broadcast rate: This attribute indicates the MoCA PHY broadcast transmit rate from the
//			ONU MoCA interface to all the nodes in bits per second. (R) (optional) (4-bytes)
//
//		Node Table
//			Rx packet: Number of packets received from the node. (4-bytes)
//
//			Rx errored and missed: Number of errored and missed packets received from the node. The sum of
//			this field across all entries in the node table contributes to the Rx errored and missed TCA.
//			This field is reset to 0 on 15-min boundaries. (4-bytes)
//
//			Rx errored: Number of errored packets received from the node. The sum of this field across all
//			entries in the node table contributes to the Rx errored TCA. This field is reset to 0 on 15-min
//			boundaries. (optional) (4-bytes)
//
//			(R) (mandatory) (37 * N bytes, where N is the number of nodes in the node table)
//
//			This attribute lists current nodes in the node table. The table contains MAC addresses and
//			statistics for those nodes. These table attributes are further described in the following. Space
//			for nonsupported optional fields must be allocated in table records, and filled with zero bytes.
//
//			MAC address: A unique identifier of a node within the table. (6-bytes)
//
//			PHY Tx rate: MoCA PHY unicast transmit rate from the ONU MoCA interface to the node identified
//			by the MAC address, in bits per second. (4-bytes)
//
//			Tx power control reduction: The reduction in transmitter level due to power control, in
//			decibels. Valid values range from 0 (full power) to 60. (1-byte)
//
//			PHY Rx rate: MoCA PHY unicast receive rate to the ONU MoCA interface from the node identified by
//			the MAC address, in bits per second. (optional) (4-bytes)
//
//			Rx power level: The power level received at the ONU MoCA interface from the node identified by
//			the MAC address, in decibel-milliwatts, represented as a 2s complement integer. Valid values
//			range from +10 (0x0A) to -80 (0xB0). (1-byte)
//
//			PHY Rx broadcast rate: MoCA PHY broadcast receive rate to the ONU MoCA interface from the node
//			identified by MAC address, in bits per second. (optional) (4-bytes)
//
//			Rx broadcast power level: The power level received at the ONU MoCA interface from the node
//			identified by the MAC address, in decibel-milliwatts, represented as a 2s complement integer.
//			Valid values range from +10-(0x0A) to -80 (0xB0). (1-byte)
//
//			Tx packet: Number of packets transmitted to the node. (4-bytes)
//
type MocaInterfacePerformanceMonitoringHistoryData struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	mocainterfaceperformancemonitoringhistorydataBME = &ManagedEntityDefinition{
		Name:    "MocaInterfacePerformanceMonitoringHistoryData",
		ClassID: 164,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			GetNext,
			Set,
		),
		AllowedAttributeMask: 0xf000,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: ByteField("IntervalEndTime", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read), false, false, false, 1),
			2: Uint16Field("ThresholdData12Id", UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3: Uint32Field("PhyTXBroadcastRate", CounterAttributeType, 0x2000, 0, mapset.NewSetWith(Read), false, true, false, 3),
			4: TableField("NodeTable", TableAttributeType, 0x1000, TableInfo{nil, 37}, mapset.NewSetWith(Read), false, false, false, 4),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
		Alarms: AlarmMap{
			0: "Total rx errored and missed",
			1: "Total rx errored",
		},
	}
}

// NewMocaInterfacePerformanceMonitoringHistoryData (class ID 164) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewMocaInterfacePerformanceMonitoringHistoryData(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*mocainterfaceperformancemonitoringhistorydataBME, params...)
}
