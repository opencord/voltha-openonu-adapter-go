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

// OnuManufacturingDataClassID is the 16-bit ID for the OMCI
// Managed entity ONU manufacturing data
const OnuManufacturingDataClassID = ClassID(456) // 0x01c8

var onumanufacturingdataBME *ManagedEntityDefinition

// OnuManufacturingData (Class ID: #456 / 0x01c8)
//	This ME contains additional manufacturing attributes associated with a PON ONU. The
//	manufacturing data is expected to match the content of an ONU label. The ONU automatically
//	creates an instance of this ME. Its attributes are populated according to data within the ONU
//	itself.
//
//	Relationships
//		This ME is paired with the ONU-G entity.
//
//	Attributes
//		Managed Entity Id
//			This attribute uniquely identifies each instance of this ME. There is only one instance, number
//			0. (R) (mandatory) (2-bytes)
//
//		Manufacturer Name
//			This attribute contains the manufacturer name of this physical ONU. The preferred value is the
//			manufacturer name string printed on the ONU itself (if present). (R) (optional) (25 bytes)
//
//		Serial Number Part 1, Serial Number Part 2
//			These two attributes may be regarded as an ASCII string of up to 32 bytes whose length is a left
//			justified manufacturer's serial number for this physical ONU. The preferred value is the
//			manufacturer serial number string printed on the ONU itself (if present). (R) (optional)
//			(25-bytes*2 attributes)
//
//		Model Name
//			This attribute contains the vendor specific model name identifier string. The preferred value is
//			the customer-visible part number which may be printed on the component itself. (R) (optional)
//			(25 bytes)
//
//		Manufacturing Date
//			This attribute contains the date of manufacturer of this physical ONU. The preferred value is
//			the date of the manufacturer printed on the ONU itself (if present). (R) (optional) (25 bytes)
//
//		Hardware_Revision
//			Hardware-revision: This attribute contains the hardware revision of this physical ONU. The
//			preferred value is the hardware revision printed on the ONU itself (if present). (R) (optional)
//			(25 bytes)
//
//		Firmware_Revision
//			Firmware-revision: This attribute contains the vendor specific firmware revision of this
//			physical ONU. (R) (optional) (25 bytes)
//
type OnuManufacturingData struct {
	ManagedEntityDefinition
	Attributes AttributeValueMap
}

func init() {
	onumanufacturingdataBME = &ManagedEntityDefinition{
		Name:    "OnuManufacturingData",
		ClassID: 456,
		MessageTypes: mapset.NewSetWith(
			Get,
		),
		AllowedAttributeMask: 0xfc00,
		AttributeDefinitions: AttributeDefinitionMap{
			0: Uint16Field("ManagedEntityId", PointerAttributeType, 0x0000, 0, mapset.NewSetWith(Read), false, false, false, 0),
			1: MultiByteField("ManufacturerName", OctetsAttributeType, 0x8000, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 1),
			2: MultiByteField("SerialNumberPart1,SerialNumberPart2", OctetsAttributeType, 0x4000, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 2),
			3: MultiByteField("ModelName", OctetsAttributeType, 0x2000, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 3),
			4: MultiByteField("ManufacturingDate", OctetsAttributeType, 0x1000, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 4),
			5: MultiByteField("HardwareRevision", OctetsAttributeType, 0x0800, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 5),
			6: MultiByteField("FirmwareRevision", OctetsAttributeType, 0x0400, 25, toOctets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="), mapset.NewSetWith(Read), false, true, false, 6),
		},
		Access:  CreatedByOnu,
		Support: UnknownSupport,
	}
}

// NewOnuManufacturingData (class ID 456) creates the basic
// Managed Entity definition that is used to validate an ME of this type that
// is received from or transmitted to the OMCC.
func NewOnuManufacturingData(params ...ParamData) (*ManagedEntity, OmciErrors) {
	return NewManagedEntity(*onumanufacturingdataBME, params...)
}