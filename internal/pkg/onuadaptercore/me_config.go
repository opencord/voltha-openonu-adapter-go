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
	"fmt"
)

const (
	tpIDStart                     = 64
	tpIDEnd                       = 256
	tpRange                       = tpIDEnd - tpIDStart
	maxUni                        = 318
	ieeMaperServiceProfileBaseEID = uint16(0x1001)
	macBridgePortAniBaseEID       = uint16(0x1001)
	macBridgePortUniBaseEID       = uint16(0x201)
	macBridgePortAniMcastBaseEID  = uint16(0xA01)
	galEthernetEID                = uint16(1)
	macBridgeServiceProfileEID    = uint16(0x201)
)

func generateIeeMaperServiceProfileEID(uniPortMacBpNo uint16, tpID uint16) (uint16, error) {
	if tpID < tpIDStart || tpID >= tpIDEnd {
		return 0, fmt.Errorf("tech profile id out of range - %d", tpID)
	}
	if uniPortMacBpNo > maxUni {
		return 0, fmt.Errorf("uni macbpno out of range - %d", uniPortMacBpNo)
	}
	return (ieeMaperServiceProfileBaseEID + uniPortMacBpNo*tpRange + tpID - tpIDStart), nil
}

func generateANISideMBPCDEID(uniPortMacBpNo uint16, tpID uint16) (uint16, error) {
	if tpID < tpIDStart || tpID >= tpIDEnd {
		return 0, fmt.Errorf("tech profile id out of range - %d", tpID)
	}
	if uniPortMacBpNo > maxUni {
		return 0, fmt.Errorf("uni macbpno out of range - %d", uniPortMacBpNo)
	}
	return (macBridgePortAniBaseEID + uniPortMacBpNo*tpRange + tpID - tpIDStart), nil
}

func generateUNISideMBPCDEID(uniPortMacBpNo uint16) (uint16, error) {
	if uniPortMacBpNo > maxUni {
		return 0, fmt.Errorf("uni macbpno out of range - %d", uniPortMacBpNo)
	}
	return (macBridgePortUniBaseEID + uniPortMacBpNo), nil
}

func generateMcastANISideMBPCDEID(uniPortMacBpNo uint16) (uint16, error) {

	if uniPortMacBpNo > maxUni {
		return 0, fmt.Errorf("uni macbpno out of range - %d", uniPortMacBpNo)
	}
	return (macBridgePortAniMcastBaseEID + uniPortMacBpNo), nil
}
