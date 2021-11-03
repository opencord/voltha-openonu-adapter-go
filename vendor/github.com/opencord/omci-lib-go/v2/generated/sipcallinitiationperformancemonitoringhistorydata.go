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

// SipCallInitiationPerformanceMonitoringHistoryDataClassID is the 16-bit ID for the OMCI
// Managed entity SIP call initiation performance monitoring history data
const SipCallInitiationPerformanceMonitoringHistoryDataClassID = ClassID(152) // 0x0098

var sipcallinitiationperformancemonitoringhistorydataBME *ManagedEntityDefinition

// SipCallInitiationPerformanceMonitoringHistoryData (Class ID: #152 / 0x0098)
//	This ME collects PM data related to call initiations of a VoIP SIP agent. Instances of this ME
//	are created and deleted by the OLT.
//
//	For a complete discussion of generic PM architecture, refer to clause I.4.
//
//	Relationships
//		An instance of this ME is associated with an instance of the SIP agent config data or SIP config
//		portal ME.
//
//	Attributes
//		Managed Entity Id
//			This attribute uniquely identifies each instance of this ME. Through an identical ID, this ME is
//			implicitly linked to an instance of the SIP agent config data or the SIP config portal ME. If a
//			nonOMCI configuration method is used for VoIP, there can be only one live ME instance,
//			associated with the SIP config portal, and with ME ID 0. (R, setbycreate) (mandatory) (2-bytes)
//
//		Interval End Time
//			This attribute identifies the most recently finished 15-min interval. (R) (mandatory) (1-byte)
//
//		Threshold Data 1_2 Id
//			Threshold data 1/2 ID: This attribute points to an instance of the threshold data 1 ME that
//			contains PM threshold values. Since no threshold value attribute number exceeds 7, a threshold
//			data 2 ME is optional. (R,-W, setbycreate) (mandatory) (2-bytes)
//
//		Failed To Connect Counter
//			This attribute counts the number of times that the SIP UA failed to reach/connect its TCP/UDP
//			peer during SIP call initiations. (R) (mandatory) (4-bytes)
//
//		Failed To Validate Counter
//			This attribute counts the number of times that the SIP UA failed to validate its peer during SIP
//			call initiations. (R) (mandatory) (4-bytes)
//
//		Timeout Counter
//			This attribute counts the number of times that the SIP UA timed out during SIP call initiations.
//			(R) (mandatory) (4-bytes)
//
//		Failure Received Counter
//			This attribute counts the number of times that the SIP UA received a failure error code during
//			SIP call initiations. (R) (mandatory) (4-bytes)
//
//		Failed To Authenticate Counter
//			This attribute counts the number of times that the SIP UA failed to authenticate itself during
//			SIP call initiations. (R) (mandatory) (4-bytes)
//
type SipCallInitiationPerformanceMonitoringHistoryData struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	sipcallinitiationperformancemonitoringhistorydataBME = &ManagedEntityDefinition{
		Name:    "SipCallInitiationPerformanceMonitoringHistoryData",
		ClassID: 152,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			Set,
			GetCurrentData,
		),
		AllowedAttributeMask: 0xfe00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: ByteField("IntervalEndTime", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read), false, false, false, 1),
			2: Uint16Field("ThresholdData12Id", UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3: Uint32Field("FailedToConnectCounter", CounterAttributeType, 0x2000, 0, mapset.NewSetWith(Read), false, false, false, 3),
			4: Uint32Field("FailedToValidateCounter", CounterAttributeType, 0x1000, 0, mapset.NewSetWith(Read), false, false, false, 4),
			5: Uint32Field("TimeoutCounter", CounterAttributeType, 0x0800, 0, mapset.NewSetWith(Read), false, false, false, 5),
			6: Uint32Field("FailureReceivedCounter", CounterAttributeType, 0x0400, 0, mapset.NewSetWith(Read), false, false, false, 6),
			7: Uint32Field("FailedToAuthenticateCounter", CounterAttributeType, 0x0200, 0, mapset.NewSetWith(Read), false, false, false, 7),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
		Alarms: AlarmMap{
			0: "SIP call PM failed connect",
			1: "SIP call PM failed to validate",
			2: "SIP call PM timeout",
			3: "SIP call PM failure error code received",
			4: "SIP call PM failed to authenticate",
		},
	}
}

// NewSipCallInitiationPerformanceMonitoringHistoryData (class ID 152) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewSipCallInitiationPerformanceMonitoringHistoryData(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*sipcallinitiationperformancemonitoringhistorydataBME, params...)
}