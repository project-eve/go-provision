// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zededa/go-provision/types"
	"io/ioutil"
	"log"
	"os"
	"time"
)

// zedagent is the publishes for these config files
var downloaderConfigMap map[string]types.DownloaderConfig

// zedagent is the subscriber for these status files
var downloaderStatusMap map[string]types.DownloaderStatus

func initDownloaderMaps() {

	if downloaderConfigMap == nil {
		fmt.Printf("create downloaderConfig map\n")
		downloaderConfigMap = make(map[string]types.DownloaderConfig)
	}

	if downloaderStatusMap == nil {
		fmt.Printf("create downloadetStatus map\n")
		downloaderStatusMap = make(map[string]types.DownloaderStatus)
	}
}

func createDownloaderConfig(objType string, safename string,
	sc *types.StorageConfig) {

	if downloaderConfigMap == nil {
		log.Printf("create downloader config map\n")
		downloaderConfigMap = make(map[string]types.DownloaderConfig)
	}

	key := objType + "x" + safename

	if m, ok := downloaderConfigMap[key]; ok {
		log.Printf("downloader config exists for %s refcount %d\n",
			safename, m.RefCount)
		m.RefCount += 1
	} else {
		n := types.DownloaderConfig{
			Safename:         safename,
			DownloadURL:      sc.DownloadURL,
			MaxSize:          sc.MaxSize,
			TransportMethod:  sc.TransportMethod,
			Dpath:            sc.Dpath,
			ApiKey:           sc.ApiKey,
			Password:         sc.Password,
			ImageSha256:      sc.ImageSha256,
			ObjType:          objType,
			DownloadObjDir:   objDownloadDirname + "/" + objType,
			FinalObjDir:      sc.FinalObjDir,
			NeedVerification: sc.NeedVerification,
			RefCount:         1,
		}
		downloaderConfigMap[key] = n
	}

	configFilename := fmt.Sprintf("%s/%s/config/%s.json",
		downloaderBaseDirname, objType, safename)

	writeDownloaderConfig(downloaderConfigMap[key], configFilename)

	log.Printf("createDownloaderConfig done for %s\n",
		safename)
}

func updateDownloaderStatus(objType string, status *types.DownloaderStatus) {

	if downloaderStatusMap == nil {
		log.Printf("create downloader status map\n")
		downloaderStatusMap = make(map[string]types.DownloaderStatus)
	}

	// Ignore if any Pending* flag is set
	if status.PendingAdd || status.PendingModify || status.PendingDelete {
		log.Printf("updataDownloaderStatus skipping due to Pending* for %s\n",
			status.Safename)
		return
	}

	key := objType + "x" + status.Safename

	changed := false
	if m, ok := downloaderStatusMap[key]; ok {
		if status.State != m.State {
			log.Printf("downloader map changed from %v to %v\n",
				m.State, status.State)
			changed = true
		}
	} else {
		log.Printf("downloader map add for %v\n", status.State)
		changed = true
	}

	if changed {

		downloaderStatusMap[key] = *status

		switch objType {
		case baseOsObj:
			baseOsHandleStatusUpdateSafename(status.Safename)
		case certObj:
			certObjHandleStatusUpdate(status.Safename)
		default:
			log.Printf("unsupported objType <%s> <%s>\n",
				objType, status.Safename)
		}
	}

	log.Printf("updateDownloaderStatus done for %s\n", status.Safename)
}

func removeDownloaderConfig(objType string, safename string) {

	key := objType + "x" + safename

	if _, ok := downloaderConfigMap[key]; !ok {
		log.Printf("removeDownloaderConfig for %s - not found\n", key)
		return
	}

	log.Printf("downloader config map delete for %s\n", key)
	delete(downloaderConfigMap, key)

	configFilename := fmt.Sprintf("%s/%s/config/%s.json",
		downloaderBaseDirname, objType, safename)

	if err := os.Remove(configFilename); err != nil {
		log.Println(err)
	}
	log.Printf("removeDownloaderConfig done for %s\n", key)
}

func removeDownloaderStatus(objType string, statusFilename string) {

	key := objType + "x" + statusFilename

	if _, ok := downloaderStatusMap[key]; !ok {
		log.Printf("removeDownloaderStatus for %s - not found\n",
			key)
		return
	}
	fmt.Printf("downloader status map delete for %s\n", key)
	delete(downloaderStatusMap, key)

	log.Printf("removeDownloaderStatus done for %s\n", key)
}

func lookupDownloaderStatus(objType string, safename string) (types.DownloaderStatus, error) {

	key := objType + "x" + safename

	if m, ok := downloaderStatusMap[key]; ok {
		return m, nil
	}
	return types.DownloaderStatus{}, errors.New("No DownloaderStatus")
}

func checkStorageDownloadStatus(objType string,
	config []types.StorageConfig, status []types.StorageStatus) (bool, types.SwState, string, time.Time) {

	allErrors := ""
	var errorTime time.Time

	changed := false
	minState := types.MAXSTATE

	for i, sc := range config {

		ss := &status[i]
		safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

		fmt.Printf("Found StorageConfig URL %s safename %s\n",
			sc.DownloadURL, safename)

		if sc.NeedVerification {
			// Shortcut if image is already verified
			vs, err := lookupVerificationStatusAny(objType, safename, sc.ImageSha256)
			if err == nil && vs.State == types.DELIVERED {
				log.Printf("found verified image for %s sha %s\n",
					safename, sc.ImageSha256)
				// If we don't already have a RefCount add one
				if !ss.HasVerifierRef {
					log.Printf("!HasVerifierRef vs.RefCount %d for %s\n",
						vs.RefCount, safename)
					vs.RefCount += 1
					ss.HasVerifierRef = true
					changed = true
				}
				if minState > vs.State {
					minState = vs.State
				}
				if vs.State != ss.State {
					ss.State = vs.State
					changed = true
				}
				continue
			}
		}

		if !ss.HasDownloaderRef {
			log.Printf("!HasDownloaderRef for %s\n", safename)
			createDownloaderConfig(objType, safename, &sc)
			ss.HasDownloaderRef = true
			changed = true
		}

		ds, err := lookupDownloaderStatus(objType, safename)
		if err != nil {
			log.Printf("LookupDownloaderStatus %s failed %v\n",
				safename, err)
			continue
		}
		if minState > ds.State {
			minState = ds.State
		}
		if ds.State != ss.State {
			ss.State = ds.State
			changed = true
		}

		switch ds.State {
		case types.INITIAL:
			log.Printf("Received error from downloader for %s: %s\n",
				safename, ds.LastErr)
			ss.Error = ds.LastErr
			allErrors = appendError(allErrors, "downloader",
				ds.LastErr)
			ss.ErrorTime = ds.LastErrTime
			changed = true
		case types.DOWNLOAD_STARTED:
			// Nothing to do
		case types.DOWNLOADED:

			// Skip verifier, and install the object
			if sc.NeedVerification {
				// Kick verifier to start if it hasn't already
				if !ss.HasVerifierRef {
					createVerifierConfig(objType, safename, &sc)
					ss.HasVerifierRef = true
					changed = true
				}
			}
		}
	}

	return changed, minState, allErrors, errorTime
}

func installDownloadedObjects(objType string,
	config []types.StorageConfig, status []types.StorageStatus) {

	for i, sc := range config {

		ss := &status[i]

		safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

		// final inatallation directory is defined
		// and the object is ready for installation`
		if sc.FinalObjDir != "" {
			if sc.NeedVerification == false ||
				ss.State == types.DELIVERED {
				installDownloadedObject(objType, safename, sc, ss)
			}
		}
	}
}

// based on download/verification state, if
// the final installation directory is mentioned,
// move the object there
func installDownloadedObject(objType string, safename string,
	config types.StorageConfig, status *types.StorageStatus) {

	var dstFilename string = config.FinalObjDir
	var srcFilename string = config.DownloadObjDir

	log.Printf("installing  %s\n", srcFilename)

	// if the object is in downloaded state,
	// pick from pending directory
	// if ithe object is n delived state,
	//  pick from verified directory
	switch status.State {

	case types.DOWNLOADED:
		srcFilename += "/pending/"

	case types.DELIVERED:
		srcFilename += "/verified/"

	default:
		log.Fatal("invalid state %d", status.State)
		return
	}

	if config.ImageSha256 != "" {
		srcFilename = srcFilename + config.ImageSha256 + "/"
	}

	srcFilename = srcFilename + safename

	// ensure the file is present
	if _, err := os.Stat(srcFilename); err != nil {
		log.Printf("%s file absent\n", srcFilename)
		return
	}

	// create the destination directory
	if _, err := os.Stat(dstFilename); err == nil {
		if err := os.MkdirAll(dstFilename, 0700); err != nil {
			log.Fatal("failed directory make")
		}
	}

	dstFilename = dstFilename + "/" + types.SafenameToFilename(safename)

	log.Printf("writing %s to %s\n", srcFilename, dstFilename)

	// move final installation point
	os.Rename(srcFilename, dstFilename)
	status.State = types.INSTALLED
}

func writeDownloaderConfig(config types.DownloaderConfig, configFilename string) {

	bytes, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal DownloaderConfig")
	}

	err = ioutil.WriteFile(configFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}
