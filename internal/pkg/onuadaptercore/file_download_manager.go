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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/opencord/voltha-lib-go/v4/pkg/log"
)

const cDefaultLocalDir = "/tmp" //this is the default local dir to download to

type fileState uint32

//nolint:varcheck, deadcode
const (
	cFileStateUnknown fileState = iota
	cFileStateDlStarted
	cFileStateDlSucceeded
	cFileStateDlFailed
	cFileStateDlAborted
	cFileStateDlInvalid
)

type downloadImageParams struct {
	downloadImageName  string
	downloadImageState fileState
	downloadImageLen   int64
	downloadImageCrc   uint32
}

type requesterChannelMap map[chan<- bool]struct{} //using an empty structure map for easier (unique) element appending

//fileDownloadManager structure holds information needed for downloading to and storing images within the adapter
type fileDownloadManager struct {
	mutexDownloadImageDsc sync.RWMutex
	downloadImageDscSlice []downloadImageParams
	dnldImgReadyWaiting   map[string]requesterChannelMap
	dlToAdapterTimeout    time.Duration
}

//newFileDownloadManager constructor returns a new instance of a fileDownloadManager
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func newFileDownloadManager(ctx context.Context) *fileDownloadManager {
	logger.Debug(ctx, "init-fileDownloadManager")
	var localDnldMgr fileDownloadManager
	localDnldMgr.downloadImageDscSlice = make([]downloadImageParams, 0)
	localDnldMgr.dnldImgReadyWaiting = make(map[string]requesterChannelMap)
	localDnldMgr.dlToAdapterTimeout = 10 * time.Second //default timeout, should be overwritten immediately after start
	return &localDnldMgr
}

//SetDownloadTimeout configures the timeout used to supervice the download of the image to the adapter (assumed in seconds)
func (dm *fileDownloadManager) SetDownloadTimeout(ctx context.Context, aDlTimeout time.Duration) {
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	logger.Debugw(ctx, "setting download timeout", log.Fields{"timeout": aDlTimeout})
	dm.dlToAdapterTimeout = aDlTimeout
}

//GetDownloadTimeout delivers the timeout used to supervice the download of the image to the adapter (assumed in seconds)
func (dm *fileDownloadManager) GetDownloadTimeout(ctx context.Context) time.Duration {
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()
	return dm.dlToAdapterTimeout
}

//ImageExists returns true if the requested image already exists within the adapter
func (dm *fileDownloadManager) ImageExists(ctx context.Context, aImageName string) bool {
	logger.Debugw(ctx, "checking on existence of the image", log.Fields{"image-name": aImageName})
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()

	for _, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name)
			return true
		}
	}
	//image not found (by name)
	return false
}

//StartDownload returns true if the download of the requested image could be started for the given file name and URL
func (dm *fileDownloadManager) StartDownload(ctx context.Context, aImageName string, aURLCommand string) error {
	logger.Infow(ctx, "image download-to-adapter requested", log.Fields{
		"image-name": aImageName, "url-command": aURLCommand})
	loDownloadImageParams := downloadImageParams{
		downloadImageName: aImageName, downloadImageState: cFileStateDlStarted,
		downloadImageLen: 0, downloadImageCrc: 0}
	dm.mutexDownloadImageDsc.Lock()
	dm.downloadImageDscSlice = append(dm.downloadImageDscSlice, loDownloadImageParams)
	dm.mutexDownloadImageDsc.Unlock()
	//try to download from http
	err := dm.downloadFile(ctx, aURLCommand, cDefaultLocalDir, aImageName)
	//return the result of the start-request to comfort the core processing even though the complete download may go on in background
	return err
}

//GetImageBufferLen returns the length of the specified file in bytes (file size) - as detected after download
func (dm *fileDownloadManager) GetImageBufferLen(ctx context.Context, aFileName string) (int64, error) {
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()
	for _, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aFileName && dnldImgDsc.downloadImageState == cFileStateDlSucceeded {
			//image found (by name) and fully downloaded
			return dnldImgDsc.downloadImageLen, nil
		}
	}
	return 0, fmt.Errorf("no downloaded image found: %s", aFileName)
}

//GetDownloadImageBuffer returns the content of the requested file as byte slice
func (dm *fileDownloadManager) GetDownloadImageBuffer(ctx context.Context, aFileName string) ([]byte, error) {
	//nolint:gosec
	file, err := os.Open(cDefaultLocalDir + "/" + aFileName)
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

//RequestDownloadReady receives a channel that has to be used to inform the requester in case the concerned file is downloaded
func (dm *fileDownloadManager) RequestDownloadReady(ctx context.Context, aFileName string, aWaitChannel chan<- bool) {
	if dm.imageLocallyDownloaded(ctx, aFileName) {
		//image found (by name) and fully downloaded
		logger.Debugw(ctx, "file ready - immediate response", log.Fields{"image-name": aFileName})
		aWaitChannel <- true
		return
	}
	//when we are here the image was not yet found or not fully downloaded -
	//  add the device specific channel to the list of waiting requesters
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	if loRequesterChannelMap, ok := dm.dnldImgReadyWaiting[aFileName]; ok {
		//entry for the file name already exists
		if _, exists := loRequesterChannelMap[aWaitChannel]; !exists {
			// requester channel does not yet exist for the image
			loRequesterChannelMap[aWaitChannel] = struct{}{}
			dm.dnldImgReadyWaiting[aFileName] = loRequesterChannelMap
			logger.Debugw(ctx, "file not ready - adding new requester", log.Fields{
				"image-name": aFileName, "number-of-requesters": len(dm.dnldImgReadyWaiting[aFileName])})
		}
	} else {
		//entry for the file name does not even exist
		addRequesterChannelMap := make(map[chan<- bool]struct{})
		addRequesterChannelMap[aWaitChannel] = struct{}{}
		dm.dnldImgReadyWaiting[aFileName] = addRequesterChannelMap
		logger.Debugw(ctx, "file not ready - setting first requester", log.Fields{
			"image-name": aFileName})
	}
}

//RemoveReadyRequest removes the specified channel from the requester(channel) map for the given file name
func (dm *fileDownloadManager) RemoveReadyRequest(ctx context.Context, aFileName string, aWaitChannel chan bool) {
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	for imageName, channelMap := range dm.dnldImgReadyWaiting {
		if imageName == aFileName {
			for channel := range channelMap {
				if channel == aWaitChannel {
					delete(dm.dnldImgReadyWaiting[imageName], channel)
					logger.Debugw(ctx, "channel removed from the requester map", log.Fields{
						"image-name": aFileName, "new number-of-requesters": len(dm.dnldImgReadyWaiting[aFileName])})
					return //can leave directly
				}
			}
			return //can leave directly
		}
	}
}

// FileDownloadManager private (unexported) methods -- start

//imageLocallyDownloaded returns true if the requested image already exists within the adapter
func (dm *fileDownloadManager) imageLocallyDownloaded(ctx context.Context, aImageName string) bool {
	logger.Debugw(ctx, "checking if image is fully downloaded to adapter", log.Fields{"image-name": aImageName})
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()

	for _, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name)
			if dnldImgDsc.downloadImageState == cFileStateDlSucceeded {
				logger.Debugw(ctx, "image has been fully downloaded", log.Fields{"image-name": aImageName})
				return true
			}
			logger.Debugw(ctx, "image not yet fully downloaded", log.Fields{"image-name": aImageName})
			return false
		}
	}
	//image not found (by name)
	logger.Errorw(ctx, "image does not exist", log.Fields{"image-name": aImageName})
	return false
}

//downloadFile downloads the specified file from the given http location
func (dm *fileDownloadManager) downloadFile(ctx context.Context, aURLCommand string, aFilePath string, aFileName string) error {
	// Get the data
	logger.Infow(ctx, "downloading with URL", log.Fields{"url": aURLCommand, "localPath": aFilePath})
	// verifying the complete URL by parsing it to its URL elements
	urlBase, err1 := url.Parse(aURLCommand)
	if err1 != nil {
		logger.Errorw(ctx, "could not set base url command", log.Fields{"url": aURLCommand, "error": err1})
		return fmt.Errorf("could not set base url command: %s, error: %s", aURLCommand, err1)
	}
	urlParams := url.Values{}
	urlBase.RawQuery = urlParams.Encode()

	//pre-check on file existence - assuming http location here
	reqExist, errExist2 := http.NewRequest("HEAD", urlBase.String(), nil)
	if errExist2 != nil {
		logger.Errorw(ctx, "could not generate http head request", log.Fields{"url": urlBase.String(), "error": errExist2})
		return fmt.Errorf("could not  generate http head request: %s, error: %s", aURLCommand, errExist2)
	}
	ctxExist, cancelExist := context.WithDeadline(ctx, time.Now().Add(3*time.Second)) //waiting for some fast answer
	defer cancelExist()
	_ = reqExist.WithContext(ctxExist)
	respExist, errExist3 := http.DefaultClient.Do(reqExist)
	if errExist3 != nil || respExist.StatusCode != http.StatusOK {
		logger.Infow(ctx, "could not http head from url", log.Fields{"url": urlBase.String(),
			"error": errExist3, "status": respExist.StatusCode})
		//if head is not supported by server we cannot use this test and just try to continue
		if respExist.StatusCode != http.StatusMethodNotAllowed {
			logger.Errorw(ctx, "http head from url: file does not exist here, aborting", log.Fields{"url": urlBase.String(),
				"error": errExist3, "status": respExist.StatusCode})
			return fmt.Errorf("http head from url: file does not exist here, aborting: %s, error: %s, status: %d",
				aURLCommand, errExist2, respExist.StatusCode)
		}
	}
	defer func() {
		deferredErr := respExist.Body.Close()
		if deferredErr != nil {
			logger.Errorw(ctx, "error at closing http head response body", log.Fields{"url": urlBase.String(), "error": deferredErr})
		}
	}()

	//trying to download - do it in background as it may take some time ...
	go func() {
		req, err2 := http.NewRequest("GET", urlBase.String(), nil)
		if err2 != nil {
			logger.Errorw(ctx, "could not generate http request", log.Fields{"url": urlBase.String(), "error": err2})
			return
		}
		ctx, cancel := context.WithDeadline(ctx, time.Now().Add(dm.dlToAdapterTimeout)) //timeout as given from SetDownloadTimeout()
		defer cancel()
		_ = req.WithContext(ctx)
		resp, err3 := http.DefaultClient.Do(req)
		if err3 != nil || respExist.StatusCode != http.StatusOK {
			logger.Errorw(ctx, "could not http get from url", log.Fields{"url": urlBase.String(),
				"error": err3, "status": respExist.StatusCode})
			return
		}
		defer func() {
			deferredErr := resp.Body.Close()
			if deferredErr != nil {
				logger.Errorw(ctx, "error at closing http get response body", log.Fields{"url": urlBase.String(), "error": deferredErr})
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
		fileSize := fileStats.Size()
		logger.Infow(ctx, "written file size is", log.Fields{"file": aLocalPathName, "length": fileSize})

		dm.mutexDownloadImageDsc.Lock()
		defer dm.mutexDownloadImageDsc.Unlock()
		for imgKey, dnldImgDsc := range dm.downloadImageDscSlice {
			if dnldImgDsc.downloadImageName == aFileName {
				//image found (by name) - need to write changes on the original map
				dm.downloadImageDscSlice[imgKey].downloadImageState = cFileStateDlSucceeded
				dm.downloadImageDscSlice[imgKey].downloadImageLen = fileSize
				//in case upgrade process(es) was/were waiting for the file, inform them
				for imageName, channelMap := range dm.dnldImgReadyWaiting {
					if imageName == aFileName {
						for channel := range channelMap {
							// use all found channels to inform possible requesters about the existence of the file
							channel <- true
							delete(dm.dnldImgReadyWaiting[imageName], channel) //requester served
						}
						return //can leave directly
					}
				}
				return //can leave directly
			}
		}
		//TODO:!!! further extension could be provided here, e.g. already computing and possibly comparing the CRC, vendor check
	}()
	return nil
}
