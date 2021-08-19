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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/opencord/voltha-protos/v5/go/voltha"

	"github.com/opencord/voltha-lib-go/v7/pkg/log"
)

// ### downloadToAdapter related definitions  ####

//not yet defined to go with sca..., later also some configure options ??
//const defaultDownloadTimeout = 60 // (?) Seconds
//const localImgPath = "/home/lcui/work/tmp"

// ### downloadToAdapter  			    - end ####

//AdapterDownloadManager structure holds information needed for downloading to and storing images within the adapter
type AdapterDownloadManager struct {
	mutexDownloadImageDsc sync.RWMutex
	downloadImageDscSlice []*voltha.ImageDownload
}

//NewAdapterDownloadManager constructor returns a new instance of a AdapterDownloadManager
//mib_db (as well as not inluded alarm_db not really used in this code? VERIFY!!)
func NewAdapterDownloadManager(ctx context.Context) *AdapterDownloadManager {
	logger.Debug(ctx, "init-AdapterDownloadManager")
	var localDnldMgr AdapterDownloadManager
	localDnldMgr.downloadImageDscSlice = make([]*voltha.ImageDownload, 0)
	return &localDnldMgr
}

//ImageExists returns true if the requested image already exists within the adapter
func (dm *AdapterDownloadManager) ImageExists(ctx context.Context, apImageDsc *voltha.ImageDownload) bool {
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

//ImageLocallyDownloaded returns true if the requested image already exists within the adapter
func (dm *AdapterDownloadManager) ImageLocallyDownloaded(ctx context.Context, apImageDsc *voltha.ImageDownload) bool {
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

//StartDownload returns true if the download of the requested image could be started
func (dm *AdapterDownloadManager) StartDownload(ctx context.Context, apImageDsc *voltha.ImageDownload) error {
	if apImageDsc.LocalDir != "" {
		logger.Infow(ctx, "image download-to-adapter requested", log.Fields{
			"image-path": apImageDsc.LocalDir, "image-name": apImageDsc.Name})
		newImageDscPos := len(dm.downloadImageDscSlice)
		dm.downloadImageDscSlice = append(dm.downloadImageDscSlice, apImageDsc)
		dm.downloadImageDscSlice[newImageDscPos].DownloadState = voltha.ImageDownload_DOWNLOAD_STARTED
		//try to download from http
		urlName := apImageDsc.Url + "/" + apImageDsc.Name
		err := dm.downloadFile(ctx, urlName, apImageDsc.LocalDir, apImageDsc.Name)
		if err != nil {
			return (err)
		}
		//return success to comfort the core processing during integration
		return nil
	}
	// we can use the missing local path temporary also to test some failure behavior (system reation on failure)
	// with updated control API's or at some adequate time we could also set some defined fixed localPath internally
	logger.Errorw(ctx, "could not start download: no valid local directory to write to", log.Fields{"image-name": (*apImageDsc).Name})
	return errors.New("could not start download: no valid local directory to write to")
}

//downloadFile downloads the specified file from the given http location
func (dm *AdapterDownloadManager) downloadFile(ctx context.Context, aURLName string, aFilePath string, aFileName string) error {
	// Get the data
	logger.Infow(ctx, "downloading from http", log.Fields{"url": aURLName, "localPath": aFilePath})
	// http command is already part of the aURLName argument
	urlBase, err1 := url.Parse(aURLName)
	if err1 != nil {
		logger.Errorw(ctx, "could not set base url command", log.Fields{"url": aURLName, "error": err1})
		return fmt.Errorf("could not set base url command: %s, error: %s", aURLName, err1)
	}
	urlParams := url.Values{}
	urlBase.RawQuery = urlParams.Encode()

	//pre-check on file existence
	reqExist, errExist2 := http.NewRequest("HEAD", urlBase.String(), nil)
	if errExist2 != nil {
		logger.Errorw(ctx, "could not generate http head request", log.Fields{"url": urlBase.String(), "error": errExist2})
		return fmt.Errorf("could not  generate http head request: %s, error: %s", aURLName, errExist2)
	}
	ctxExist, cancelExist := context.WithDeadline(ctx, time.Now().Add(3*time.Second)) //waiting for some fast answer
	defer cancelExist()
	_ = reqExist.WithContext(ctxExist)
	respExist, errExist3 := http.DefaultClient.Do(reqExist)
	if errExist3 != nil || (respExist != nil && respExist.StatusCode != http.StatusOK) {
		if respExist != nil {
			logger.Errorw(ctx, "could not http head from url", log.Fields{"url": urlBase.String(),
				"error": errExist3, "status": respExist.StatusCode})
			//if head is not supported by server we cannot use this test and just try to continue
			if respExist.StatusCode != http.StatusMethodNotAllowed {
				logger.Errorw(ctx, "http head from url: file does not exist here, aborting", log.Fields{"url": urlBase.String(),
					"error": errExist3, "status": respExist.StatusCode})
				return fmt.Errorf("http head from url: file does not exist here, aborting: %s, error: %s, status: %d",
					aURLName, errExist2, respExist.StatusCode)
			}
		} else {
			logger.Errorw(ctx, "could not http head from url", log.Fields{"url": urlBase.String(),
				"error": errExist3})
		}
	}

	if errExist3 == nil && respExist != nil {
		defer func() {
			deferredErr := respExist.Body.Close()
			if deferredErr != nil {
				logger.Errorw(ctx, "error at closing http head response body", log.Fields{"url": urlBase.String(), "error": deferredErr})
			}
		}()
	}

	//trying to download - do it in background as it may take some time ...
	go dm.requestDownload(ctx, urlBase, aFilePath, aFileName)
	return nil
}

func (dm *AdapterDownloadManager) requestDownload(ctx context.Context, urlBase *url.URL, aFilePath, aFileName string) {
	req, err2 := http.NewRequest("GET", urlBase.String(), nil)
	if err2 != nil {
		logger.Errorw(ctx, "could not generate http request", log.Fields{"url": urlBase.String(), "error": err2})
		return
	}
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(10*time.Second)) //long timeout for remote server and big file
	defer cancel()
	_ = req.WithContext(ctx)
	resp, err3 := http.DefaultClient.Do(req)
	if err3 != nil {
		logger.Errorw(ctx, "could not http get from url", log.Fields{"url": urlBase.String(), "error": err3})
		return
	}
	defer func() {
		deferredErr := resp.Body.Close()
		if deferredErr != nil {
			logger.Errorw(ctx, "error at closing http get response body", log.Fields{"url": urlBase.String(), "error": deferredErr})
		}
	}()

	if resp.StatusCode != http.StatusOK {
		logger.Errorw(ctx, "could not http get from url", log.Fields{"url": urlBase.String(), "status": resp.StatusCode})
		return
	}

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
	if statsErr != nil {
		logger.Errorw(ctx, "created file can't be accessed", log.Fields{"file": aLocalPathName, "stat-error": statsErr})
	}
	if fileStats != nil {
		logger.Infow(ctx, "written file size is", log.Fields{"file": aLocalPathName, "length": fileStats.Size()})
	}

	for _, pDnldImgDsc := range dm.downloadImageDscSlice {
		if (*pDnldImgDsc).Name == aFileName {
			//image found (by name)
			(*pDnldImgDsc).DownloadState = voltha.ImageDownload_DOWNLOAD_SUCCEEDED
			return //can leave directly
		}
	}
}

//getImageBufferLen returns the length of the specified file in bytes (file size)
func (dm *AdapterDownloadManager) getImageBufferLen(ctx context.Context, aFileName string,
	aLocalPath string) (int64, error) {
	//maybe we can also use FileSize from dm.downloadImageDscSlice - future option?

	file, err := os.Open(filepath.Clean(aLocalPath + "/" + aFileName))
	if err != nil {
		return 0, err
	}
	defer func() {
		err := file.Close()
		if err != nil {
			logger.Errorw(ctx, "failed to close file", log.Fields{"error": err})
		}
	}()

	stats, statsErr := file.Stat()
	if statsErr != nil {
		return 0, statsErr
	}

	return stats.Size(), nil
}

//getDownloadImageBuffer returns the content of the requested file as byte slice
func (dm *AdapterDownloadManager) getDownloadImageBuffer(ctx context.Context, aFileName string,
	aLocalPath string) ([]byte, error) {
	file, err := os.Open(filepath.Clean(aLocalPath + "/" + aFileName))
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
