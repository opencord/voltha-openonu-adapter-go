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

// TrafficDescriptorClassID is the 16-bit ID for the OMCI
// Managed entity Traffic descriptor
const TrafficDescriptorClassID ClassID = ClassID(280)

var trafficdescriptorBME *ManagedEntityDefinition

// TrafficDescriptor (class ID #280)
//	The traffic descriptor is a profile that allows for traffic management. A priority controlled
//	ONU can point from a MAC bridge port configuration data ME to a traffic descriptor in order to
//	implement traffic management (marking, policing). A rate controlled ONU can point to a traffic
//	descriptor from either a MAC bridge port configuration data ME or a GEM port network CTP to
//	implement traffic management (marking, shaping).
//
//	Packets are determined to be green, yellow or red as a function of the ingress packet rate and
//	the settings in this ME. The colour indicates drop precedence (eligibility), subsequently used
//	by the priority queue ME to drop packets conditionally during congestion conditions. Packet
//	colour is also used by the optional mode 1 DBA status reporting function described in [ITUT
//	G.984.3]. Red packets are dropped immediately. Yellow packets are marked as drop eligible, and
//	green packets are marked as not drop eligible, according to the egress colour marking attribute.
//
//	The algorithm used to determine the colour marking is specified by the meter type attribute. If
//	[bIETF RFC 4115] is used, then:
//
//	CIR4115-=-CIR
//
//	EIR4115-=-PIR - CIR (EIR: excess information rate)
//
//	CBS4115-=-CBS
//
//	EBS4115-=-PBS - CBS.
//
//	Relationships
//		This ME is associated with a GEM port network CTP or a MAC bridge port configuration data ME.
//
//	Attributes
//		Managed Entity Id
//			Managed entity ID: This attribute uniquely identifies each instance of this ME. (R, setbycreate)
//			(mandatory) (2-bytes)
//
//		Cir
//			CIR:	This attribute specifies the committed information rate, in bytes per second. The default
//			is 0. (R,-W, setbycreate) (optional) (4-bytes)
//
//		Pir
//			PIR:	This attribute specifies the peak information rate, in bytes per second. The default value
//			0 accepts the ONU's factory policy. (R,-W, setbycreate) (optional) (4-bytes)
//
//		Cbs
//			CBS:	This attribute specifies the committed burst size, in bytes. The default is 0. (R,-W,
//			setbycreate) (optional) (4-bytes)
//
//		Pbs
//			PBS:	This attribute specifies the peak burst size, in bytes. The default value 0 accepts the
//			ONU's factory policy. (R,-W, setbycreate) (optional) (4-bytes)
//
//		Colour Mode
//			(R,-W, setbycreate) (optional) (1-byte)
//
//		Ingress Colour Marking
//			(R,-W, setbycreate) (optional) (1-byte)
//
//		Egress Colour Marking
//			(R,-W, setbycreate) (optional) (1-byte)
//
//		Meter Type
//			(R, setbycreate) (optional) (1-byte)
//
type TrafficDescriptor struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	trafficdescriptorBME = &ManagedEntityDefinition{
		Name:    "TrafficDescriptor",
		ClassID: 280,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			Set,
		),
		AllowedAttributeMask: 0xff00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1: Uint32Field("Cir", UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 1),
			2: Uint32Field("Pir", UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 2),
			3: Uint32Field("Cbs", UnsignedIntegerAttributeType, 0x2000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 3),
			4: Uint32Field("Pbs", UnsignedIntegerAttributeType, 0x1000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 4),
			5: ByteField("ColourMode", EnumerationAttributeType, 0x0800, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 5),
			6: ByteField("IngressColourMarking", EnumerationAttributeType, 0x0400, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 6),
			7: ByteField("EgressColourMarking", EnumerationAttributeType, 0x0200, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, true, false, 7),
			8: ByteField("MeterType", EnumerationAttributeType, 0x0100, 0, mapset.NewSetWith(Read, SetByCreate), false, true, false, 8),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
	}
}

// NewTrafficDescriptor (class ID 280) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewTrafficDescriptor(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*trafficdescriptorBME, params...)
}