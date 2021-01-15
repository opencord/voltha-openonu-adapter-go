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
	"context"
	"errors"
	"sync"

	//"time"

	"github.com/opencord/voltha-protos/v4/go/voltha"

	"github.com/opencord/voltha-lib-go/v4/pkg/log"
)

// ### downloadToAdapter related definitions  ####

//not yet defined to go with sca..., later also some configure options ??
//const defaultDownloadTimeout = 60 // (?) Seconds
//const localImgPath = "/home/lcui/work/tmp"

// ### downloadToAdapter  			    - end ####

//adapterDownloadManager structure holds information needed for downloading to and storing images within the adapter
type adapterDownloadManager struct {
	mutexDownloadImageDsc sync.RWMutex
	downloadImageDscSlice []*voltha.ImageDownload
}

//newAdapterDownloadManager constructor returns a new instance of a adapterDownloadManager
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newAdapterDownloadManager(ctx context.Context) *adapterDownloadManager {
	logger.Debug(ctx, "init-adapterDownloadManager")
	var localDnldMgr adapterDownloadManager
	localDnldMgr.downloadImageDscSlice = make([]*voltha.ImageDownload, 0)

	return &localDnldMgr
}

//imageExists returns true if the requested image already exists within the adapter
func (dm *adapterDownloadManager) imageExists(ctx context.Context, apImageDsc *voltha.ImageDownload) bool {
	logger.Debugw(ctx, "checking on existence of the image", log.Fields{"image-name": (*apImageDsc).Name})
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()

	for _, pDnldImgDsc := range dm.downloadImageDscSlice {
		if (*pDnldImgDsc).Name == (*apImageDsc).Name {
			//image found (by name)
			return true
		}
	}
	//image not found (by name)
	return false
}

//startDownload returns true if the download of the requested image could be started
func (dm *adapterDownloadManager) startDownload(ctx context.Context, apImageDsc *voltha.ImageDownload) error {
	logger.Debugw(ctx, "image download requested", log.Fields{"image-name": apImageDsc.Name})
	//so far just return error
	return errors.New("could not start downloading")
}
