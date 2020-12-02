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

// TwdmChannelPloamPerformanceMonitoringHistoryDataPart1ClassID is the 16-bit ID for the OMCI
// Managed entity TWDM channel PLOAM performance monitoring history data part 1
const TwdmChannelPloamPerformanceMonitoringHistoryDataPart1ClassID ClassID = ClassID(446)

var twdmchannelploamperformancemonitoringhistorydatapart1BME *ManagedEntityDefinition

// TwdmChannelPloamPerformanceMonitoringHistoryDataPart1 (class ID #446)
//	This ME collects certain PLOAM-related PM data associated with the slot/circuit pack, hosting
//	one or more ANI-G MEs, for a specific TWDM channel. Instances of this ME are created and deleted
//	by the OLT.
//
//	The downstream PLOAM message counts of this ME include only the received PLOAM messages
//	pertaining to the given ONU, i.e.:
//
//	-	unicast PLOAM messages, addressed by ONU-ID;
//
//	-	broadcast PLOAM messages, addressed by serial number;
//
//	-	broadcast PLOAM messages, addressed to all ONUs on the PON.
//
//	This ME includes all PLOAM PM counters characterized as mandatory in clause 14 of [ITU-
//	T-G.989.3].
//
//	For a complete discussion of generic PM architecture, refer to clause I.4.
//
//	Relationships
//		An instance of this ME is associated with an instance of TWDM channel ME.
//
//	Attributes
//		Managed Entity Id
//			Managed entity ID: This attribute uniquely identifies each instance of this ME. Through an
//			identical ID, this ME is implicitly linked to an instance of the TWDM channel ME. (R,
//			setbycreate) (mandatory) (2-bytes)
//
//		Interval End Time
//			Interval end time: This attribute identifies the most recently finished 15-min interval. (R)
//			(mandatory) (1-byte)
//
//		Threshold Data 1_2 Id
//			Threshold data 1/2 ID: This attribute points to an instance of the threshold data 1 and 2 MEs
//			that contains PM threshold values. (R,-W, setbycreate) (mandatory) (2-bytes)
//
//		Ploam Mic Errors
//			PLOAM MIC errors: The counter of received PLOAM messages that remain unparsable due to MIC
//			error. (R) (mandatory) (4-byte)
//
//		Downstream Ploam Message Count
//			Downstream PLOAM message count: The counter of received broadcast and unicast PLOAM messages
//			pertaining to the given ONU. (R) (mandatory) (4-byte)
//
//		Ranging_Time Message Count
//			Ranging_Time message count: The counter of received Ranging_Time PLOAM messages. (R) (mandatory)
//			(4-byte)
//
//		Protection_Control Message Count
//			Protection_Control message count: The counter of received Protection_Control PLOAM messages. (R)
//			(mandatory) (4-byte)
//
//		Adjust_Tx_Wavelength Message Count
//			Adjust_Tx_Wavelength message count: The counter of received Adjust_Tx_Wavelength PLOAM messages.
//			(R) (mandatory) (4-byte)
//
//		Adjust_Tx_Wavelength Adjustment Amplitude
//			Adjust_Tx_Wavelength adjustment amplitude: An estimator of the absolute value of the
//			transmission wavelength adjustment. (R) (mandatory) (4-byte)
//
type TwdmChannelPloamPerformanceMonitoringHistoryDataPart1 struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	twdmchannelploamperformancemonitoringhistorydatapart1BME = &ManagedEntityDefinition{
		Name:    "TwdmChannelPloamPerformanceMonitoringHistoryDataPart1",
		ClassID: 446,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			GetCurrentData,
			Set,
		),
		AllowedAttributeMask: 0xff00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: ByteField("IntervalEndTime", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read), false, false, false, 1),
			2: Uint16Field("ThresholdData12Id", UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3: Uint32Field("PloamMicErrors", CounterAttributeType, 0x2000, 0, mapset.NewSetWith(Read), false, false, false, 3),
			4: Uint32Field("DownstreamPloamMessageCount", CounterAttributeType, 0x1000, 0, mapset.NewSetWith(Read), false, false, false, 4),
			5: Uint32Field("RangingTimeMessageCount", CounterAttributeType, 0x0800, 0, mapset.NewSetWith(Read), false, false, false, 5),
			6: Uint32Field("ProtectionControlMessageCount", CounterAttributeType, 0x0400, 0, mapset.NewSetWith(Read), false, false, false, 6),
			7: Uint32Field("AdjustTxWavelengthMessageCount", CounterAttributeType, 0x0200, 0, mapset.NewSetWith(Read), false, false, false, 7),
			8: Uint32Field("AdjustTxWavelengthAdjustmentAmplitude", CounterAttributeType, 0x0100, 0, mapset.NewSetWith(Read), false, false, false, 8),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
	}
}

// NewTwdmChannelPloamPerformanceMonitoringHistoryDataPart1 (class ID 446) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewTwdmChannelPloamPerformanceMonitoringHistoryDataPart1(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*twdmchannelploamperformancemonitoringhistorydatapart1BME, params...)
}