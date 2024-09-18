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

// ReAniGClassID is the 16-bit ID for the OMCI
// Managed entity RE ANI-G
const ReAniGClassID = ClassID(313) // 0x0139

var reanigBME *ManagedEntityDefinition

// ReAniG (Class ID: #313 / 0x0139)
//	This ME organizes data associated with each R'/S' physical interface of an RE if the RE supports
//	OEO regeneration in either direction. The management ONU automatically creates one instance of
//	this ME for each R'/S' physical port (uni- or bidirectional) as follows.
//
//	o	When the RE has mid-span PON RE ANI interface ports built into its 	factory configuration.
//
//	o	When a cardholder is provisioned to expect a circuit pack of the mid-span PON RE ANI type.
//
//	o	When a cardholder provisioned for plug-and-play is equipped with a circuit pack of the midspan
//	PON RE ANI type. Note that the installation of a plug-and-play card may indicate the presence of
//	a mid-span PON RE ANI port via equipment ID as well as its type attribute, and indeed may cause
//	the management ONU to instantiate a port-mapping package to specify the ports precisely.
//
//	The management ONU automatically deletes instances of this ME when a cardholder is neither
//	provisioned to expect a mid-span PON RE ANI circuit pack, nor is it equipped with a mid-span PON
//	RE ANI circuit pack.
//
//	As illustrated in Figure 8.2.10-4, an RE ANI-G may share the physical port with an RE downstream
//	amplifier. The ONU declares a shared configuration through the port-mapping package combined
//	port table, whose structure defines one ME as the master. It is recommended that the RE ANI-G be
//	the master, with the RE downstream amplifier as a secondary ME.
//
//	The administrative state, operational state and ARC attributes of the master ME override similar
//	attributes in secondary MEs associated with the same port. In the secondary ME, these attributes
//	are present, but cause no action when written and have undefined values when read. The RE
//	downstream amplifier should use its provisionable downstream alarm thresholds and should declare
//	downstream alarms as necessary; other isomorphic alarms should be declared by the RE ANI-G. The
//	test action should be addressed to the master ME.
//
//	Relationships
//		An instance of this ME is associated with each R'/S' physical interface of an RE that includes
//		OEO regeneration in either direction, and with one or more instances of the PPTP RE UNI. It may
//		also be associated with an RE downstream amplifier.
//
//	Attributes
//		Managed Entity Id
//			This attribute uniquely identifies each instance of this ME. Its value indicates the physical
//			position of the R'/S' interface. The first byte is the slot ID (defined in clause 9.1.5). The
//			second byte is the port ID. (R) (mandatory) (2-bytes)
//
//			NOTE 1 - This ME ID may be identical to that of an RE downstream amplifier if it shares the same
//			physical slot and port.
//
//		Administrative State
//			This attribute locks (1) and unlocks (0) the functions performed by this ME. Administrative
//			state is further described in clause A.1.6. (R,-W) (mandatory) (1-byte)
//
//			NOTE 2 - When an RE supports multiple PONs, or protected access to a single PON, its primary
//			ANI-G cannot be completely shut down, due to a loss of the management communications capability.
//			Complete blocking of service and removal of power may nevertheless be appropriate for secondary
//			RE ANI-Gs. Administrative lock suppresses alarms and notifications for an RE ANI-G, be it either
//			primary or secondary.
//
//		Operational State
//			This attribute indicates whether the ME is capable of performing its function. Valid values are
//			enabled (0) and disabled (1). (R) (optional) (1-byte)
//
//		Arc
//			See clause A.1.4.3. (R,-W) (optional) (1-byte)
//
//		Arc Interval
//			See clause A.1.4.3. (R,-W) (optional) (1-byte)
//
//		Optical Signal Level
//			This attribute reports the current measurement of total downstream optical power. Its value is a
//			2s complement integer referred to 1-mW (i.e., dBm), with 0.002-dB granularity. (Coding -32768 to
//			+32767, where 0x00 = 0-dBm, 0x03e8 = +2-dBm, etc.) (R) (optional) (2-bytes)
//
//		Lower Optical Threshold
//			This attribute specifies the optical level that the RE uses to declare the downstream low
//			received optical power alarm. Valid values are  -127-dBm (coded as 254) to 0-dBm (coded as 0) in
//			0.5-dB increments. The default value 0xFF selects the RE's internal policy. (R,-W) (optional)
//			(1-byte)
//
//		Upper Optical Threshold
//			This attribute specifies the optical level that the RE uses to declare the downstream high
//			received optical power alarm. Valid values are  -127-dBm (coded as 254) to 0-dBm (coded as 0) in
//			0.5 dB increments. The default value 0xFF selects the RE's internal policy. (R,-W) (optional)
//			(1-byte)
//
//		Transmit Optical Level
//			This attribute reports the current measurement of mean optical launch power. Its value is a 2s
//			complement integer referred to 1-mW (i.e., dBm), with 0.002-dB granularity. (Coding -32768 to
//			+32767, where 0x00 = 0-dBm, 0x03e8 = +2-dBm, etc.) (R) (optional) (2-bytes)
//
//		Lower Transmit Power Threshold
//			This attribute specifies the minimum mean optical launch power that the RE uses to declare the
//			low transmit optical power alarm. Its value is a 2s-complement integer referred to 1-mW (i.e.,
//			dBm), with 0.5-dB granularity. The default value 0x7F selects the RE's internal policy. (R,-W)
//			(optional) (1-byte)
//
//		Upper Transmit Power Threshold
//			This attribute specifies the maximum mean optical launch power that the RE uses to declare the
//			high transmit optical power alarm. Its value is a 2s-complement integer referred to 1-mW (i.e.,
//			dBm), with 0.5-dB granularity. The default value 0x7F selects the RE's internal policy. (R,-W)
//			(optional) (1-byte)
//
//		Usage Mode
//			In a mid-span PON RE, an R'/S' interface may be used as the PON interface for the embedded
//			management ONU or the uplink interface for an S'/R' interface. This attribute specifies the
//			usage of the R'/S' interface. (R,-W) (mandatory) (1-byte)
//
//			0	Disable
//
//			1	This R'/S' interface is used as the uplink for the embedded management ONU
//
//			2	This R'/S' interface is used as the uplink for one or more PPTP RE UNI(s)
//
//			3	This R'/S' interface is used as the uplink for both the embedded management ONU and one or
//			more PPTP RE UNI(s) (in a time division fashion).
//
//		Target Upstream Frequency
//			This attribute specifies the frequency of the converted upstream signal on the optical trunk
//			line (OTL), in gigahertz. The converted frequency must conform to the frequency plan specified
//			in [ITUT G.984.6]. The value 0 means that the upstream signal frequency remains the same as the
//			original frequency; no frequency conversion is done. If the RE does not support provisionable
//			upstream frequency (wavelength), this attribute should take the fixed value representing the
//			RE's capability and the RE should deny attempts to set the value of the attribute. If the RE
//			does support provisionable upstream frequency conversion, the default value of this attribute is
//			0. (R, W) (optional) (4 bytes).
//
//		Target Downstream Frequency
//			This attribute specifies the frequency of the downstream signal received by the RE on the OTL,
//			in gigahertz. The incoming frequency must conform to the frequency plan specified in [ITUT
//			G.984.6]. The default value 0 means that the downstream frequency remains the same as its
//			original frequency; no frequency conversion is done. If the RE does not support provisionable
//			downstream frequency selectivity, this attribute should take the fixed value representing the
//			RE's capability, and the RE should deny attempts to set the value of the attribute. If the RE
//			does support provisionable downstream frequency selectivity, the default value of this attribute
//			is 0. (R, W) (optional) (4 bytes).
//
//		Upstream Signal Transmission Mode
//			When true, this Boolean attribute enables conversion from burst mode to continuous mode. The
//			default value false specifies burst mode upstream transmission. If the RE does not have the
//			ability to convert from burst to continuous mode transmission, it should deny attempts to set
//			this attribute to true. (R, W) (optional) (1 byte)
//
type ReAniG struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

// Attribute name constants

const ReAniG_AdministrativeState = "AdministrativeState"
const ReAniG_OperationalState = "OperationalState"
const ReAniG_Arc = "Arc"
const ReAniG_ArcInterval = "ArcInterval"
const ReAniG_OpticalSignalLevel = "OpticalSignalLevel"
const ReAniG_LowerOpticalThreshold = "LowerOpticalThreshold"
const ReAniG_UpperOpticalThreshold = "UpperOpticalThreshold"
const ReAniG_TransmitOpticalLevel = "TransmitOpticalLevel"
const ReAniG_LowerTransmitPowerThreshold = "LowerTransmitPowerThreshold"
const ReAniG_UpperTransmitPowerThreshold = "UpperTransmitPowerThreshold"
const ReAniG_UsageMode = "UsageMode"
const ReAniG_TargetUpstreamFrequency = "TargetUpstreamFrequency"
const ReAniG_TargetDownstreamFrequency = "TargetDownstreamFrequency"
const ReAniG_UpstreamSignalTransmissionMode = "UpstreamSignalTransmissionMode"

func init() {
	reanigBME = &ManagedEntityDefinition{
		Name:    "ReAniG",
		ClassID: ReAniGClassID,
		MessageTypes: mapset.NewSetWith(
			Get,
			Set,
		),
		AllowedAttributeMask: 0xfffc,
		AttributeDefinitions: AttributeDefinitionMap{
			0:  Uint16Field(ManagedEntityID, PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read), false, false, false, 0),
			1:  ByteField(ReAniG_AdministrativeState, UnsignedIntegerAttributeType, 0x8000, 0, mapset.NewSetWith(Read, Write), false, false, false, 1),
			2:  ByteField(ReAniG_OperationalState, UnsignedIntegerAttributeType, 0x4000, 0, mapset.NewSetWith(Read), true, true, false, 2),
			3:  ByteField(ReAniG_Arc, UnsignedIntegerAttributeType, 0x2000, 0, mapset.NewSetWith(Read, Write), true, true, false, 3),
			4:  ByteField(ReAniG_ArcInterval, UnsignedIntegerAttributeType, 0x1000, 0, mapset.NewSetWith(Read, Write), false, true, false, 4),
			5:  Uint16Field(ReAniG_OpticalSignalLevel, UnsignedIntegerAttributeType, 0x0800, 0, mapset.NewSetWith(Read), false, true, false, 5),
			6:  ByteField(ReAniG_LowerOpticalThreshold, UnsignedIntegerAttributeType, 0x0400, 0, mapset.NewSetWith(Read, Write), false, true, false, 6),
			7:  ByteField(ReAniG_UpperOpticalThreshold, UnsignedIntegerAttributeType, 0x0200, 0, mapset.NewSetWith(Read, Write), false, true, false, 7),
			8:  Uint16Field(ReAniG_TransmitOpticalLevel, UnsignedIntegerAttributeType, 0x0100, 0, mapset.NewSetWith(Read), false, true, false, 8),
			9:  ByteField(ReAniG_LowerTransmitPowerThreshold, UnsignedIntegerAttributeType, 0x0080, 0, mapset.NewSetWith(Read, Write), false, true, false, 9),
			10: ByteField(ReAniG_UpperTransmitPowerThreshold, UnsignedIntegerAttributeType, 0x0040, 0, mapset.NewSetWith(Read, Write), false, true, false, 10),
			11: ByteField(ReAniG_UsageMode, UnsignedIntegerAttributeType, 0x0020, 0, mapset.NewSetWith(Read, Write), false, false, false, 11),
			12: Uint32Field(ReAniG_TargetUpstreamFrequency, UnsignedIntegerAttributeType, 0x0010, 0, mapset.NewSetWith(Read, Write), false, true, false, 12),
			13: Uint32Field(ReAniG_TargetDownstreamFrequency, UnsignedIntegerAttributeType, 0x0008, 0, mapset.NewSetWith(Read, Write), false, true, false, 13),
			14: ByteField(ReAniG_UpstreamSignalTransmissionMode, UnsignedIntegerAttributeType, 0x0004, 0, mapset.NewSetWith(Read, Write), false, true, false, 14),
		},
		Access:  CreatedByOnu,
		Support: UnknownSupport,
		Alarms: AlarmMap{
			0: "Low received optical power",
			1: "High received optical power",
			2: "Low transmit optical power",
			3: "High transmit optical power",
			4: "High laser bias current",
		},
	}
}

// NewReAniG (class ID 313) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewReAniG(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*reanigBME, params...)
}