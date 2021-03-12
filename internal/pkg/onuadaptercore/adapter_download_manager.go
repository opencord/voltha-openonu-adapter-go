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
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
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
	// maybe just for test purpose
	arrayFileFragment [32]byte
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

//imageLocallyDownloaded returns true if the requested image already exists within the adapter
func (dm *adapterDownloadManager) imageLocallyDownloaded(ctx context.Context, apImageDsc *voltha.ImageDownload) bool {
	logger.Debugw(ctx, "checking if image is fully downloaded", log.Fields{"image-name": (*apImageDsc).Name})
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()

	for _, pDnldImgDsc := range dm.downloadImageDscSlice {
		if (*pDnldImgDsc).Name == (*apImageDsc).Name {
			//image found (by name)
			if (*pDnldImgDsc).DownloadState == voltha.ImageDownload_DOWNLOAD_SUCCEEDED {
				logger.Debugw(ctx, "image has been fully downloaded", log.Fields{"image-name": (*apImageDsc).Name})
				return true
			}
			logger.Debugw(ctx, "image not yet fully downloaded", log.Fields{"image-name": (*apImageDsc).Name})
			return false
		}
	}
	//image not found (by name)
	logger.Errorw(ctx, "image does not exist", log.Fields{"image-name": (*apImageDsc).Name})
	return false
}

//startDownload returns true if the download of the requested image could be started
func (dm *adapterDownloadManager) startDownload(ctx context.Context, apImageDsc *voltha.ImageDownload) error {
	if apImageDsc.LocalDir != "" {
		logger.Infow(ctx, "image download-to-adapter requested", log.Fields{
			"image-path": apImageDsc.LocalDir, "image-name": apImageDsc.Name})
		newImageDscPos := len(dm.downloadImageDscSlice)
		dm.downloadImageDscSlice = append(dm.downloadImageDscSlice, apImageDsc)
		dm.downloadImageDscSlice[newImageDscPos].DownloadState = voltha.ImageDownload_DOWNLOAD_STARTED
		if apImageDsc.LocalDir == "/intern" {
			//just for initial 'internal' test verification
			//just some basic test file simulation
			dm.downloadImageDscSlice[newImageDscPos].LocalDir = "/tmp"
			go dm.writeFileToLFS(ctx, "/tmp", apImageDsc.Name)
			return nil
		}
		//try to download from http
		urlName := apImageDsc.Url + "/" + apImageDsc.Name
		go dm.downloadFile(ctx, urlName, apImageDsc.LocalDir, apImageDsc.Name)
		//return success to comfort the core processing during integration
		return nil
	}
	// we can use the missing local path temporary also to test some failure behavior (system reation on failure)
	// with updated control API's or at some adequate time we could also set some defined fixed localPath internally
	logger.Errorw(ctx, "could not start download: no valid local directory to write to", log.Fields{"image-name": (*apImageDsc).Name})
	return errors.New("could not start download: no valid local directory to write to")
}

//downloadFile downloads the specified file from the given http location
func (dm *adapterDownloadManager) downloadFile(ctx context.Context, aURLName string, aFilePath string, aFileName string) {
	// Get the data
	logger.Infow(ctx, "downloading from http", log.Fields{"url": aURLName, "localPath": aFilePath})
	localURL := "http://" + aURLName
	urlBase, err1 := url.Parse(localURL)
	if err1 != nil {
		logger.Errorw(ctx, "could not set base url command", log.Fields{"url": aURLName, "error": err1})
	}
	urlParams := url.Values{}
	urlBase.RawQuery = urlParams.Encode()
	resp, err2 := http.Get(urlBase.String())
	if err2 != nil {
		logger.Errorw(ctx, "could not http get from url", log.Fields{"url": urlBase.String(), "error": err2})
		return
	}
	defer func() {
		deferredErr := resp.Body.Close()
		if deferredErr != nil {
			logger.Errorw(ctx, "error at closing http response body", log.Fields{"url": urlBase.String(), "error": deferredErr})
		}
	}()

	// Create the file
	aLocalPathName := aFilePath + "/" + aFileName
	file, err := os.Create(aLocalPathName)
	if err != nil {
		logger.Errorw(ctx, "could not create local file", log.Fields{"path_file": aLocalPathName, "error": err})
		return
	}
	defer func() {
		deferredErr := file.Close()
		if deferredErr != nil {
			logger.Errorw(ctx, "error at closing new file", log.Fields{"path_file": aLocalPathName, "error": deferredErr})
		}
	}()

	// Write the body to file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		logger.Errorw(ctx, "could not copy file content", log.Fields{"url": urlBase.String(), "file": aLocalPathName, "error": err})
		return
	}

	fileStats, statsErr := file.Stat()
	if err != nil {
		logger.Errorw(ctx, "created file can't be accessed", log.Fields{"file": aLocalPathName, "stat-error": statsErr})
	}
	logger.Infow(ctx, "written file size is", log.Fields{"file": aLocalPathName, "length": fileStats.Size()})

	for _, pDnldImgDsc := range dm.downloadImageDscSlice {
		if (*pDnldImgDsc).Name == aFileName {
			//image found (by name)
			(*pDnldImgDsc).DownloadState = voltha.ImageDownload_DOWNLOAD_SUCCEEDED
			return //can leave directly
		}
	}
}

//writeFileToLFS writes the downloaded file to the local file system
//  this is just an internal test function and can be removed if other download capabilities exist
func (dm *adapterDownloadManager) writeFileToLFS(ctx context.Context, aLocalPath string, aFileName string) {
	// by now just a simulation to write a file with predefined 'variable' content
	totalFileLength := 0
	logger.Debugw(ctx, "writing fixed size simulation file locally", log.Fields{
		"image-name": aFileName, "image-path": aLocalPath})
	file, err := os.Create(aLocalPath + "/" + aFileName)
	if err == nil {
		// write 32KB test file
		for totalFileLength < 32*1024 {
			if written, wrErr := file.Write(dm.getIncrementalSliceContent(ctx)); wrErr == nil {
				totalFileLength += written
			} else {
				logger.Errorw(ctx, "could not write to file", log.Fields{"create-error": wrErr})
				break //stop writing
			}
		}
	} else {
		logger.Errorw(ctx, "could not create file", log.Fields{"create-error": err})
	}

	fileStats, statsErr := file.Stat()
	if err != nil {
		logger.Errorw(ctx, "created file can't be accessed", log.Fields{"stat-error": statsErr})
	}
	logger.Infow(ctx, "written file size is", log.Fields{"file": aLocalPath + "/" + aFileName, "length": fileStats.Size()})

	defer func() {
		deferredErr := file.Close()
		if deferredErr != nil {
			logger.Errorw(ctx, "error at closing test file", log.Fields{"file": aLocalPath + "/" + aFileName, "error": deferredErr})
		}
	}()

	for _, pDnldImgDsc := range dm.downloadImageDscSlice {
		if (*pDnldImgDsc).Name == aFileName {
			//image found (by name)
			(*pDnldImgDsc).DownloadState = voltha.ImageDownload_DOWNLOAD_SUCCEEDED
			return //can leave directly
		}
	}
}

//getImageBufferLen returns the length of the specified file in bytes (file size)
func (dm *adapterDownloadManager) getImageBufferLen(ctx context.Context, aFileName string,
	aLocalPath string) (int64, error) {
	//maybe we can also use FileSize from dm.downloadImageDscSlice - future option?

	//nolint:gosec
	file, err := os.Open(aLocalPath + "/" + aFileName)
	if err != nil {
		return 0, err
	}
	//nolint:errcheck
	defer file.Close()

	stats, statsErr := file.Stat()
	if statsErr != nil {
		return 0, statsErr
	}

	return stats.Size(), nil
}

//getDownloadImageBuffer returns the content of the requested file as byte slice
func (dm *adapterDownloadManager) getDownloadImageBuffer(ctx context.Context, aFileName string,
	aLocalPath string) ([]byte, error) {
	//nolint:gosec
	file, err := os.Open(aLocalPath + "/" + aFileName)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck
	defer file.Close()

	stats, statsErr := file.Stat()
	if statsErr != nil {
		return nil, statsErr
	}

	var size int64 = stats.Size()
	bytes := make([]byte, size)

	buffer := bufio.NewReader(file)
	_, err = buffer.Read(bytes)

	return bytes, err
}

//getIncrementalSliceContent returns a byte slice of incremented bytes of internal array (used for file emulation)
// (used for file emulation)
func (dm *adapterDownloadManager) getIncrementalSliceContent(ctx context.Context) []byte {
	lastValue := dm.arrayFileFragment[len(dm.arrayFileFragment)-1]
	for index := range dm.arrayFileFragment {
		lastValue++
		dm.arrayFileFragment[index] = lastValue
	}
	return dm.arrayFileFragment[:]
}
