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

//Package swupg provides the utilities for onu sw upgrade
package swupg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/opencord/voltha-lib-go/v5/pkg/log"
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
	downloadImageName       string
	downloadImageState      fileState
	downloadImageLen        int64
	downloadImageCrc        uint32
	downloadActive          bool
	downloadContextCancelFn context.CancelFunc
}

type requesterChannelMap map[chan<- bool]struct{} //using an empty structure map for easier (unique) element appending

//FileDownloadManager structure holds information needed for downloading to and storing images within the adapter
type FileDownloadManager struct {
	mutexDownloadImageDsc sync.RWMutex
	downloadImageDscSlice []downloadImageParams
	dnldImgReadyWaiting   map[string]requesterChannelMap
	dlToAdapterTimeout    time.Duration
}

//NewFileDownloadManager constructor returns a new instance of a FileDownloadManager
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewFileDownloadManager(ctx context.Context) *FileDownloadManager {
	logger.Debug(ctx, "init-FileDownloadManager")
	var localDnldMgr FileDownloadManager
	localDnldMgr.downloadImageDscSlice = make([]downloadImageParams, 0)
	localDnldMgr.dnldImgReadyWaiting = make(map[string]requesterChannelMap)
	localDnldMgr.dlToAdapterTimeout = 10 * time.Second //default timeout, should be overwritten immediately after start
	return &localDnldMgr
}

//SetDownloadTimeout configures the timeout used to supervice the download of the image to the adapter (assumed in seconds)
func (dm *FileDownloadManager) SetDownloadTimeout(ctx context.Context, aDlTimeout time.Duration) {
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	logger.Debugw(ctx, "setting download timeout", log.Fields{"timeout": aDlTimeout})
	dm.dlToAdapterTimeout = aDlTimeout
}

//GetDownloadTimeout delivers the timeout used to supervice the download of the image to the adapter (assumed in seconds)
func (dm *FileDownloadManager) GetDownloadTimeout(ctx context.Context) time.Duration {
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()
	return dm.dlToAdapterTimeout
}

//ImageExists returns true if the requested image already exists within the adapter
func (dm *FileDownloadManager) ImageExists(ctx context.Context, aImageName string) bool {
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
func (dm *FileDownloadManager) StartDownload(ctx context.Context, aImageName string, aURLCommand string) error {
	logger.Infow(ctx, "image download-to-adapter requested", log.Fields{
		"image-name": aImageName, "url-command": aURLCommand})
	loDownloadImageParams := downloadImageParams{
		downloadImageName: aImageName, downloadImageState: cFileStateDlStarted,
		downloadImageLen: 0, downloadImageCrc: 0}
	//try to download from http
	var err error
	if err = dm.downloadFile(ctx, aURLCommand, cDefaultLocalDir, aImageName); err == nil {
		dm.mutexDownloadImageDsc.Lock()
		dm.downloadImageDscSlice = append(dm.downloadImageDscSlice, loDownloadImageParams)
		dm.mutexDownloadImageDsc.Unlock()
	}
	//return the result of the start-request to comfort the core processing even though the complete download may go on in background
	return err
}

//GetImageBufferLen returns the length of the specified file in bytes (file size) - as detected after download
func (dm *FileDownloadManager) GetImageBufferLen(ctx context.Context, aFileName string) (int64, error) {
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
func (dm *FileDownloadManager) GetDownloadImageBuffer(ctx context.Context, aFileName string) ([]byte, error) {
	file, err := os.Open(filepath.Clean(cDefaultLocalDir + "/" + aFileName))
	if err != nil {
		return nil, err
	}
	defer func() {
		err := file.Close()
		if err != nil {
			logger.Errorw(ctx, "failed to close file", log.Fields{"error": err})
		}
	}()

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
func (dm *FileDownloadManager) RequestDownloadReady(ctx context.Context, aFileName string, aWaitChannel chan<- bool) {
	//mutexDownloadImageDsc must already be locked here to avoid an update of the dnldImgReadyWaiting map
	//  just after returning false on imageLocallyDownloaded() (not found) and immediate handling of the
	//  download success (within updateFileState())
	//  so updateFileState() can't interfere here just after imageLocallyDownloaded() before setting the requester map
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	if dm.imageLocallyDownloaded(ctx, aFileName) {
		//image found (by name) and fully downloaded
		logger.Debugw(ctx, "file ready - immediate response", log.Fields{"image-name": aFileName})
		aWaitChannel <- true
		return
	}
	//when we are here the image was not yet found or not fully downloaded -
	//  add the device specific channel to the list of waiting requesters
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
func (dm *FileDownloadManager) RemoveReadyRequest(ctx context.Context, aFileName string, aWaitChannel chan bool) {
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
//  requires mutexDownloadImageDsc to be locked (at least RLocked)
func (dm *FileDownloadManager) imageLocallyDownloaded(ctx context.Context, aImageName string) bool {
	logger.Debugw(ctx, "checking if image is fully downloaded to adapter", log.Fields{"image-name": aImageName})
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

//updateDownloadCancel sets context cancel function to be used in case the download is to be aborted
func (dm *FileDownloadManager) updateDownloadCancel(ctx context.Context,
	aImageName string, aCancelFn context.CancelFunc) {
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	for imgKey, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name) - need to write changes on the original map
			dm.downloadImageDscSlice[imgKey].downloadContextCancelFn = aCancelFn
			dm.downloadImageDscSlice[imgKey].downloadActive = true
			logger.Debugw(ctx, "downloadContextCancelFn set", log.Fields{
				"image-name": aImageName})
			return //can leave directly
		}
	}
}

//updateFileState sets the new active (downloaded) file state and informs possibly waiting requesters on this change
func (dm *FileDownloadManager) updateFileState(ctx context.Context, aImageName string, aFileSize int64) {
	dm.mutexDownloadImageDsc.Lock()
	defer dm.mutexDownloadImageDsc.Unlock()
	for imgKey, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name) - need to write changes on the original map
			dm.downloadImageDscSlice[imgKey].downloadActive = false
			dm.downloadImageDscSlice[imgKey].downloadImageState = cFileStateDlSucceeded
			dm.downloadImageDscSlice[imgKey].downloadImageLen = aFileSize
			logger.Debugw(ctx, "imageState download succeeded", log.Fields{
				"image-name": aImageName, "image-size": aFileSize})
			//in case upgrade process(es) was/were waiting for the file, inform them
			for imageName, channelMap := range dm.dnldImgReadyWaiting {
				if imageName == aImageName {
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
}

//downloadFile downloads the specified file from the given http location
func (dm *FileDownloadManager) downloadFile(ctx context.Context, aURLCommand string, aFilePath string, aFileName string) error {
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
		if respExist == nil {
			logger.Errorw(ctx, "http head from url error - no status, aborting", log.Fields{"url": urlBase.String(),
				"error": errExist3})
			return fmt.Errorf("http head from url error - no status, aborting: %s, error: %s",
				aURLCommand, errExist3)
		}
		logger.Infow(ctx, "could not http head from url", log.Fields{"url": urlBase.String(),
			"error": errExist3, "status": respExist.StatusCode})
		//if head is not supported by server we cannot use this test and just try to continue
		if respExist.StatusCode != http.StatusMethodNotAllowed {
			logger.Errorw(ctx, "http head from url: file does not exist here, aborting", log.Fields{"url": urlBase.String(),
				"error": errExist3, "status": respExist.StatusCode})
			return fmt.Errorf("http head from url: file does not exist here, aborting: %s, error: %s, status: %d",
				aURLCommand, errExist3, respExist.StatusCode)
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
			dm.removeImage(ctx, aFileName, false) //wo FileSystem access
			return
		}
		ctx, cancel := context.WithDeadline(ctx, time.Now().Add(dm.dlToAdapterTimeout)) //timeout as given from SetDownloadTimeout()
		dm.updateDownloadCancel(ctx, aFileName, cancel)
		defer cancel()
		_ = req.WithContext(ctx)
		resp, err3 := http.DefaultClient.Do(req)
		if err3 != nil || resp.StatusCode != http.StatusOK {
			if resp == nil {
				logger.Errorw(ctx, "http get error - no status, aborting", log.Fields{"url": urlBase.String(),
					"error": err3})
			} else {
				logger.Errorw(ctx, "could not http get from url", log.Fields{"url": urlBase.String(),
					"error": err3, "status": resp.StatusCode})
			}
			dm.removeImage(ctx, aFileName, false) //wo FileSystem access
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
			dm.removeImage(ctx, aFileName, false) //wo FileSystem access
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
			dm.removeImage(ctx, aFileName, true)
			return
		}

		fileStats, statsErr := file.Stat()
		if err != nil {
			logger.Errorw(ctx, "created file can't be accessed", log.Fields{"file": aLocalPathName, "stat-error": statsErr})
			return
		}
		fileSize := fileStats.Size()
		logger.Infow(ctx, "written file size is", log.Fields{"file": aLocalPathName, "length": fileSize})

		dm.updateFileState(ctx, aFileName, fileSize)
		//TODO:!!! further extension could be provided here, e.g. already computing and possibly comparing the CRC, vendor check
	}()
	return nil
}

//removeImage deletes the given image according to the Image name from filesystem and downloadImageDscSlice
func (dm *FileDownloadManager) removeImage(ctx context.Context, aImageName string, aDelFs bool) {
	logger.Debugw(ctx, "remove the image from Adapter", log.Fields{"image-name": aImageName})
	dm.mutexDownloadImageDsc.RLock()
	defer dm.mutexDownloadImageDsc.RUnlock()

	tmpSlice := dm.downloadImageDscSlice[:0]
	for _, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name)
			logger.Debugw(ctx, "removing image", log.Fields{"image-name": aImageName})
			if aDelFs {
				//remove the image from filesystem
				aLocalPathName := cDefaultLocalDir + "/" + aImageName
				if err := os.Remove(aLocalPathName); err != nil {
					// might be a temporary situation, when the file was not yet (completely) written
					logger.Debugw(ctx, "image not removed from filesystem", log.Fields{
						"image-name": aImageName, "error": err})
				}
			}
			// and remove from the imageDsc slice by just not appending
		} else {
			tmpSlice = append(tmpSlice, dnldImgDsc)
		}
	}
	dm.downloadImageDscSlice = tmpSlice
	//image not found (by name)
}

//CancelDownload stops the download and clears all entires concerning this aimageName
func (dm *FileDownloadManager) CancelDownload(ctx context.Context, aImageName string) {
	// for the moment that would only support to wait for the download end and remove the image then
	//   further reactions while still downloading can be considered with some effort, but does it make sense (synchronous load here!)
	dm.mutexDownloadImageDsc.RLock()
	for imgKey, dnldImgDsc := range dm.downloadImageDscSlice {
		if dnldImgDsc.downloadImageName == aImageName {
			//image found (by name) - need to to check on ongoing download
			if dm.downloadImageDscSlice[imgKey].downloadActive {
				//then cancel the download using the context cancel function
				dm.downloadImageDscSlice[imgKey].downloadContextCancelFn()
			}
			//and remove possibly stored traces of this image
			dm.mutexDownloadImageDsc.RUnlock()
			go dm.removeImage(ctx, aImageName, true) //including the chance that nothing was yet written to FS, should not matter
			return                                   //can leave directly
		}
	}
	dm.mutexDownloadImageDsc.RUnlock()
}
