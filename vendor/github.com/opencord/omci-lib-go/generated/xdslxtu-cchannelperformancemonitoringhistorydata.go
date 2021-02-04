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

// XdslXtuCChannelPerformanceMonitoringHistoryDataClassID is the 16-bit ID for the OMCI
// Managed entity xDSL xTU-C channel performance monitoring history data
const XdslXtuCChannelPerformanceMonitoringHistoryDataClassID ClassID = ClassID(114)

var xdslxtucchannelperformancemonitoringhistorydataBME *ManagedEntityDefinition

// XdslXtuCChannelPerformanceMonitoringHistoryData (class ID #114)
//	This ME collects PM data of an xTUC to xTUR channel as seen from the xTU-C. Instances of this ME
//	are created and deleted by the OLT.
//
//	For a complete discussion of generic PM architecture, refer to clause I.4.
//
//	Relationships
//		An instance of this ME is associated with an xDSL bearer channel. Several instances may
//		therefore be associated with an xDSL UNI.
//
//	Attributes
//		Managed Entity Id
//			Managed entity ID: This attribute uniquely identifies each instance of this ME. The two MSBs of
//			the first byte are the bearer channel ID. Excluding the first 2-bits of the first byte, the
//			remaining part of the ME ID is identical to that of this ME's parent PPTP xDSL UNI part 1. (R,
//			setbycreate) (mandatory) (2-bytes)
//
//		Interval End Time
//			Interval end time: This attribute identifies the most recently finished 15-min interval. (R)
//			(mandatory) (1-byte)
//
//		Threshold Data 1_2 Id
//			Threshold data 1/2 ID: This attribute points to an instance of the threshold data 1 ME that
//			contains PM threshold values. Since no threshold value attribute number exceeds 7, a threshold
//			data 2 ME is optional. (R,-W, setbycreate) (mandatory) (2-bytes)
//
//		Corrected Blocks
//			Corrected blocks: This attribute counts blocks received with errors that were corrected on this
//			channel. (R) (mandatory) (4-bytes)
//
//		Uncorrected Blocks
//			Uncorrected blocks: This attribute counts blocks received with uncorrectable errors on this
//			channel. (R) (mandatory) (4-bytes)
//
//		Transmitted Blocks
//			Transmitted blocks:	This attribute counts encoded blocks transmitted on this channel. (R)
//			(mandatory) (4-bytes)
//
//		Received Blocks
//			Received blocks: This attribute counts encoded blocks received on this channel. (R) (mandatory)
//			(4-bytes)
//
//		Code Violations
//			Code violations: This attribute counts CRC-8 anomalies in the bearer channel. (R) (mandatory)
//			(2-bytes)
//
//		Forward Error Corrections
//			Forward error corrections: This attribute counts FEC anomalies in the bearer channel. (R)
//			(mandatory) (2-bytes)
//
type XdslXtuCChannelPerformanceMonitoringHistoryData struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	xdslxtucchannelperformancemonitoringhistorydataBME = &ManagedEntityDefinition{
		Name:    "XdslXtuCChannelPerformanceMonitoringHistoryData",
		ClassID: 114,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			Set,
		),
		AllowedAttributeMask: 0xff00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: ByteField("IntervalEndTime", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read), false, false, false, 1),
			2: Uint16Field("ThresholdData12Id", UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3: Uint32Field("CorrectedBlocks", CounterAttributeType, 0x2000, 0, mapset.NewSetWith(Read), false, false, false, 3),
			4: Uint32Field("UncorrectedBlocks", CounterAttributeType, 0x1000, 0, mapset.NewSetWith(Read), false, false, false, 4),
			5: Uint32Field("TransmittedBlocks", CounterAttributeType, 0x0800, 0, mapset.NewSetWith(Read), false, false, false, 5),
			6: Uint32Field("ReceivedBlocks", CounterAttributeType, 0x0400, 0, mapset.NewSetWith(Read), false, false, false, 6),
			7: Uint16Field("CodeViolations", CounterAttributeType, 0x0200, 0, mapset.NewSetWith(Read), false, false, false, 7),
			8: Uint16Field("ForwardErrorCorrections", CounterAttributeType, 0x0100, 0, mapset.NewSetWith(Read), false, false, false, 8),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
		Alarms: AlarmMap{
			0: "Corrected blocks",
			1: "Uncorrected blocks",
			2: "Code violations",
			3: "Forward error corrections",
		},
	}
}

// NewXdslXtuCChannelPerformanceMonitoringHistoryData (class ID 114) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewXdslXtuCChannelPerformanceMonitoringHistoryData(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*xdslxtucchannelperformancemonitoringhistorydataBME, params...)
}
