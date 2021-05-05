/*
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

//Package adaptercoreonu provides the utility for onu devices, flows and statistics
package adaptercoreonu

import (
	"encoding/binary"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// GetTpIDFromTpPath extracts TpID from the TpPath.
// On success it returns a valid TpID and nil error.
// On failure it returns TpID as 0 and the error.
func GetTpIDFromTpPath(tpPath string) (uint8, error) {
	// tpPath is of the format  <technology>/<table_id>/olt-{}/pon-{}/onu-{}/uni-{}
	// A sample tpPath is ==> XGS-PON/64/olt-{12345abcd}/pon-{0}/onu-{1}/uni-{1}
	var tpPathFormat = regexp.MustCompile(`^[a-zA-Z\-_]+/[0-9]+/olt-{[a-z0-9\-]+}/pon-{[0-9]+}/onu-{[0-9]+}/uni-{[0-9]+}$`)

	// Ensure tpPath is of the format  <technology>/<table_id>/<uni_port_name>
	if !tpPathFormat.Match([]byte(tpPath)) {
		return 0, errors.New("tp-path-not-confirming-to-format")
	}
	// Extract the TP table-id field.
	tpID, err := strconv.Atoi(strings.Split(tpPath, "/")[1])
	// Atoi returns uint64 and need to be type-casted to uint8 as tpID is uint8 size.
	return uint8(tpID), err
}

//IPToInt32 transforms an IP of net.Ip type to int32
func IPToInt32(ip net.IP) uint32 {
	if len(ip) == 16 {
		return binary.BigEndian.Uint32(ip[12:16])
	}
	return binary.BigEndian.Uint32(ip)
}

//AsByteSlice transforms a string of manually set bits to a byt array
func AsByteSlice(bitString string) []byte {
	var out []byte
	var str string

	for i := len(bitString); i > 0; i -= 8 {
		if i-8 < 0 {
			str = bitString[0:i]
		} else {
			str = bitString[i-8 : i]
		}
		v, err := strconv.ParseUint(str, 2, 8)
		if err != nil {
			panic(err)
		}
		out = append([]byte{byte(v)}, out...)
	}
	return out
}

// TwosComplementToSignedInt16 convert 2s complement to signed int16
func TwosComplementToSignedInt16(val uint16) int16 {
	var uint16MsbMask uint16 = 0x8000
	if val&uint16MsbMask == uint16MsbMask {
		return int16(^val+1) * -1
	}

	return int16(val)
}
