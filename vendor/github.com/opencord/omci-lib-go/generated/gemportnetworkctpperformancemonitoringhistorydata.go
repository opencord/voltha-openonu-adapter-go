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

// GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID is the 16-bit ID for the OMCI
// Managed entity GEM port network CTP performance monitoring history data
const GemPortNetworkCtpPerformanceMonitoringHistoryDataClassID ClassID = ClassID(341)

var gemportnetworkctpperformancemonitoringhistorydataBME *ManagedEntityDefinition

// GemPortNetworkCtpPerformanceMonitoringHistoryData (class ID #341)
//	This ME collects GEM frame PM data associated with a GEM port network CTP. Instances of this ME
//	are created and deleted by the OLT.
//
//	NOTE 1 - One might expect to find some form of impaired or discarded frame count associated with
//	a GEM port. However, the only impairment that might be detected at the GEM frame level would be
//	a corrupted GEM frame header. In this case, no part of the header could be considered reliable
//	including the port ID. For this reason, there is no impaired or discarded frame count in this
//	ME.
//
//	NOTE 2 - This ME replaces the GEM port performance history data ME and is preferred for new
//	implementations.
//
//	For a complete discussion of generic PM architecture, refer to clause I.4.
//
//	Relationships
//		An instance of this ME is associated with an instance of the GEM port network CTP ME.
//
//	Attributes
//		Managed Entity Id
//			Managed entity ID: This attribute uniquely identifies each instance of this ME. Through an
//			identical ID, this ME is implicitly linked to an instance of the GEM port network CTP. (R,
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
//		Transmitted Gem Frames
//			Transmitted GEM frames: This attribute counts GEM frames transmitted on the monitored GEM port.
//			(R) (mandatory) (4-bytes)
//
//		Received Gem Frames
//			Received GEM frames: This attribute counts GEM frames received correctly on the monitored GEM
//			port. A correctly received GEM frame is one that does not contain uncorrectable errors and has a
//			valid header error check (HEC). (R) (mandatory) (4-bytes)
//
//		Received Payload Bytes
//			Received payload bytes: This attribute counts user payload bytes received on the monitored GEM
//			port. (R) (mandatory) (8-bytes)
//
//		Transmitted Payload Bytes
//			Transmitted payload bytes: This attribute counts user payload bytes transmitted on the monitored
//			GEM port. (R) (mandatory) (8-bytes)
//
//		Encryption Key Errors
//			NOTE 4 - GEM PM counts each non-idle GEM frame, whether it contains an entire user frame or only
//			a fragment of a user frame.
//
type GemPortNetworkCtpPerformanceMonitoringHistoryData struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	gemportnetworkctpperformancemonitoringhistorydataBME = &ManagedEntityDefinition{
		Name:    "GemPortNetworkCtpPerformanceMonitoringHistoryData",
		ClassID: 341,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			Set,
		),
		AllowedAttributeMask: 0xfe00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: ByteField("IntervalEndTime", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read), false, false, false, 1),
			2: Uint16Field("ThresholdData12Id", PointerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3: Uint32Field("TransmittedGemFrames", CounterAttributeType, 0x2000, 0, mapset.NewSetWith(Read), false, false, false, 3),
			4: Uint32Field("ReceivedGemFrames", CounterAttributeType, 0x1000, 0, mapset.NewSetWith(Read), false, false, false, 4),
			5: Uint64Field("ReceivedPayloadBytes", CounterAttributeType, 0x0800, 0, mapset.NewSetWith(Read), false, false, false, 5),
			6: Uint64Field("TransmittedPayloadBytes", CounterAttributeType, 0x0400, 0, mapset.NewSetWith(Read), false, false, false, 6),
			7: Uint32Field("EncryptionKeyErrors", CounterAttributeType, 0x0200, 0, mapset.NewSetWith(Read), false, true, false, 7),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
		Alarms: AlarmMap{
			1: "Encryption key errors",
		},
	}
}

// NewGemPortNetworkCtpPerformanceMonitoringHistoryData (class ID 341) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewGemPortNetworkCtpPerformanceMonitoringHistoryData(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*gemportnetworkctpperformancemonitoringhistorydataBME, params...)
}
