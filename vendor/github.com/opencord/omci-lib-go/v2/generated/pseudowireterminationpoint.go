/*
 * Copyright (c) 2018 - present.  Boling Consulting Solutions (bcsw.net)
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
/*
 * NOTE: This file was generated, manual edits will be overwritten!
 *
 * Generated by 'goCodeGenerator.py':
 *              https://github.com/cboling/OMCI-parser/README.md
 */

package generated

import "github.com/deckarep/golang-set"

// PseudowireTerminationPointClassID is the 16-bit ID for the OMCI
// Managed entity Pseudowire termination point
const PseudowireTerminationPointClassID = ClassID(282) // 0x011a

var pseudowireterminationpointBME *ManagedEntityDefinition

// PseudowireTerminationPoint (Class ID: #282 / 0x011a)
//	The pseudowire TP supports packetized (rather than TDM) transport of TDM services, transported
//	either directly over Ethernet, over UDP/IP or over MPLS. Instances of this ME are created and
//	deleted by the OLT.
//
//	Relationships
//		One pseudowire TP ME exists for each distinct TDM service that is mapped to a pseudowire.
//
//	Attributes
//		Managed Entity Id
//			This attribute uniquely identifies each instance of this ME. (R, setbycreate) (mandatory)
//			(2-bytes)
//
//		Underlying Transport
//			2	MPLS
//
//			(R,-W, setbycreate) (mandatory) (1-byte)
//
//			Underlying transport:
//
//			0	Ethernet, MEF 8
//
//			1	UDP/IP
//
//		Service Type
//			This attribute specifies the basic service type, either a transparent bit pipe or an
//			encapsulation that recognizes the underlying structure of the payload.
//
//			0	Basic unstructured (also known as structure agnostic)
//
//			1	Octet-aligned unstructured, structure agnostic. Applicable only to DS1, a mode in which each
//			frame of 193 bits is encapsulated in 25 bytes with 7 padding bits.
//
//			2	Structured (structure-locked)
//
//			(R,-W, setbycreate) (mandatory) (1-byte)
//
//		Signalling
//			1	CAS, to be carried in the same packet stream as the payload
//
//			2	CAS, to be carried in a separate signalling channel
//
//			(R,-W, setbycreate) (mandatory for structured service type) (1-byte)
//
//				0	No signalling visible at this layer
//
//		Tdm Uni Pointer
//			If service type-= structured, this attribute points to a logical N-* 64-kbit/s subport CTP.
//			Otherwise, this attribute points to a PPTP CES UNI. (R,-W, setbycreate) (mandatory) (2-bytes)
//
//		North_Side Pointer
//			North-side pointer: When the pseudowire service is transported via IP, as indicated by the
//			underlying transport attribute, the northside pointer attribute points to an instance of the
//			TCP/UDP config data ME. When the pseudowire service is transported directly over Ethernet, the
//			north-side pointer attribute is not used - the linkage to the Ethernet flow TP is implicit in
//			the ME IDs. When the pseudowire service is transported over MPLS, the northside pointer
//			attribute points to an instance of the MPLS PW TP. (R, W, setbycreate) (mandatory) (2 bytes)
//
//		Far_End Ip Info
//			A null pointer is appropriate if the pseudowire is not transported via IP. (R,-W, setbycreate)
//			(mandatory for IP transport) (2-bytes)
//
//			Far-end IP info: When the pseudowire service is transported via IP, this attribute points to a
//			large string ME that contains the URI of the far-end TP, e.g.,
//
//			udp://192.168.100.221:5000
//
//			udp://pwe3srvr.int.example.net:2222
//
//		Payload Size
//			Number of payload bytes per packet. Valid only if service type-= basic unstructured or octet-
//			aligned unstructured. Valid choices depend on the TDM service, but must include the following.
//			Other choices are at the vendor's discretion.
//
//			DS1	192
//
//			DS1	200, required only if an octet-aligned unstructured service is supported
//
//			E1	256
//
//			DS3	1024
//
//			E3	1024
//
//			(R,-W, setbycreate) (mandatory for unstructured service) (2-bytes)
//
//		Payload Encapsulation Delay
//			Number of 125-us frames to be encapsulated in each pseudowire packet. Valid only if service
//			type-= structured. The minimum set of choices for various TDM services is listed in the
//			following table, and is affected by the possible presence of in-band signalling. Other choices
//			are at the vendor's discretion.
//
//			(R,-W, setbycreate) (mandatory for structured service) (1-byte)
//
//		Timing Mode
//			(R,-W) (mandatory) (1-byte)
//
//			This attribute selects the timing mode of the TDM service. If RTP is used, this attribute must
//			be set to be consistent with the value of the RTP timestamp mode attribute in the RTP pseudowire
//			parameters ME, or its equivalent, at the far end.
//
//			0	Network timing (default)
//
//			1	Differential timing
//
//			2	Adaptive timing
//
//			3	Loop timing: local TDM transmit clock derived from local TDM receive stream
//
//		Transmit Circuit Id
//			This attribute is a pair of emulated circuit ID (ECID) values that the ONU transmits in the
//			direction from the TDM termination towards the packet-switched network (PSN). MEF 8 ECIDs lie in
//			the range 1..1048575 (220-- 1). To allow for the possibility of other transport (L2TP) in the
//			future, each ECID is allocated 4-bytes.
//
//			The first value is used for the payload ECID; the second is used for the optional separate
//			signalling ECID. The first ECID is required for all MEF 8 pseudowires; the second is required
//			only if signalling is to be carried in a distinct channel. If signalling is not present, or is
//			carried in the same channel as the payload, the second ECID should be set to 0.
//
//			(R,-W) (mandatory for MEF 8 transport) (8-bytes)
//
//		Expected Circuit Id
//			This attribute is a pair of ECID values that the ONU can expect in the direction from the PSN
//			towards the TDM termination. Checking ECIDs may be a way to detect circuit misconnection. MEF 8
//			ECIDs lie in the range 1..1048575 (220-- 1). To allow for the possibility of other transport
//			(L2TP) in the future, each ECID is allocated 4-bytes.
//
//			The first value is used for the payload ECID; the second is used for the optional separate
//			signalling ECID. In both cases, the default value 0 indicates that no ECID checking is expected.
//
//			(R,-W) (optional for MEF 8 transport) (8-bytes)
//
//		Received Circuit Id
//			This attribute indicates the actual ECID(s) received on the payload and signalling channels,
//			respectively. It may be used for diagnostic purposes. (R) (optional for MEF 8 transport)
//			(8-bytes)
//
//		Exception Policy
//			This attribute points to an instance of the pseudowire maintenance profile ME. If the pointer
//			has its default value 0, the ONU's internal defaults apply. (R,-W) (optional) (2-bytes)
//
//		Arc
//			See clause A.1.4.3. (R,-W) (optional) (1-byte)
//
//		Arc Interval
//			See clause A.1.4.3. (R,-W) (optional) (1-byte)
//
type PseudowireTerminationPoint struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

// Attribute name constants

const PseudowireTerminationPoint_UnderlyingTransport = "UnderlyingTransport"
const PseudowireTerminationPoint_ServiceType = "ServiceType"
const PseudowireTerminationPoint_Signalling = "Signalling"
const PseudowireTerminationPoint_TdmUniPointer = "TdmUniPointer"
const PseudowireTerminationPoint_NorthSidePointer = "NorthSidePointer"
const PseudowireTerminationPoint_FarEndIpInfo = "FarEndIpInfo"
const PseudowireTerminationPoint_PayloadSize = "PayloadSize"
const PseudowireTerminationPoint_PayloadEncapsulationDelay = "PayloadEncapsulationDelay"
const PseudowireTerminationPoint_TimingMode = "TimingMode"
const PseudowireTerminationPoint_TransmitCircuitId = "TransmitCircuitId"
const PseudowireTerminationPoint_ExpectedCircuitId = "ExpectedCircuitId"
const PseudowireTerminationPoint_ReceivedCircuitId = "ReceivedCircuitId"
const PseudowireTerminationPoint_ExceptionPolicy = "ExceptionPolicy"
const PseudowireTerminationPoint_Arc = "Arc"
const PseudowireTerminationPoint_ArcInterval = "ArcInterval"

func init() {
	pseudowireterminationpointBME = &ManagedEntityDefinition{
		Name:    "PseudowireTerminationPoint",
		ClassID: PseudowireTerminationPointClassID,
		MessageTypes: mapset.NewSetWith(
			Create,
			Delete,
			Get,
			Set,
		),
		AllowedAttributeMask: 0xfffe,
		AttributeDefinitions: AttributeDefinitionMap{
			0:  Uint16Field(ManagedEntityID, PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read, SetByCreate), false, false, false, 0),
			1:  ByteField(PseudowireTerminationPoint_UnderlyingTransport, UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 1),
			2:  ByteField(PseudowireTerminationPoint_ServiceType, UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 2),
			3:  ByteField(PseudowireTerminationPoint_Signalling, UnsignedIntegerAttributeType, 0x2000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 3),
			4:  Uint16Field(PseudowireTerminationPoint_TdmUniPointer, UnsignedIntegerAttributeType, 0x1000, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 4),
			5:  Uint16Field(PseudowireTerminationPoint_NorthSidePointer, UnsignedIntegerAttributeType, 0x0800, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 5),
			6:  Uint16Field(PseudowireTerminationPoint_FarEndIpInfo, UnsignedIntegerAttributeType, 0x0400, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 6),
			7:  Uint16Field(PseudowireTerminationPoint_PayloadSize, UnsignedIntegerAttributeType, 0x0200, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 7),
			8:  ByteField(PseudowireTerminationPoint_PayloadEncapsulationDelay, UnsignedIntegerAttributeType, 0x0100, 0, mapset.NewSetWith(Read, SetByCreate, Write), false, false, false, 8),
			9:  ByteField(PseudowireTerminationPoint_TimingMode, UnsignedIntegerAttributeType, 0x0080, 0, mapset.NewSetWith(Read, Write), false, false, false, 9),
			10: Uint64Field(PseudowireTerminationPoint_TransmitCircuitId, UnsignedIntegerAttributeType, 0x0040, 0, mapset.NewSetWith(Read, Write), false, false, false, 10),
			11: Uint64Field(PseudowireTerminationPoint_ExpectedCircuitId, UnsignedIntegerAttributeType, 0x0020, 0, mapset.NewSetWith(Read, Write), false, false, false, 11),
			12: Uint64Field(PseudowireTerminationPoint_ReceivedCircuitId, UnsignedIntegerAttributeType, 0x0010, 0, mapset.NewSetWith(Read), false, false, false, 12),
			13: Uint16Field(PseudowireTerminationPoint_ExceptionPolicy, UnsignedIntegerAttributeType, 0x0008, 0, mapset.NewSetWith(Read, Write), false, true, false, 13),
			14: ByteField(PseudowireTerminationPoint_Arc, UnsignedIntegerAttributeType, 0x0004, 0, mapset.NewSetWith(Read, Write), true, true, false, 14),
			15: ByteField(PseudowireTerminationPoint_ArcInterval, UnsignedIntegerAttributeType, 0x0002, 0, mapset.NewSetWith(Read, Write), false, true, false, 15),
		},
		Access:  CreatedByOlt,
		Support: UnknownSupport,
	}
}

// NewPseudowireTerminationPoint (class ID 282) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewPseudowireTerminationPoint(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*pseudowireterminationpointBME, params...)
}