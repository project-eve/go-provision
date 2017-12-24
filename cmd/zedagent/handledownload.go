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

// zedagent publishes for these config files
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

	initDownloaderMaps()

	key := formLookupKey(objType, safename)
	log.Printf("createDownloaderConfig for %s\n", key)

	if m, ok := downloaderConfigMap[key]; ok {
		log.Printf("createDownloaderConfig for %s, Downloader config exists, refcount %d\n",
			key, m.RefCount)
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

	log.Printf("createDownloaderConfig for %s, Done\n", key)
}

func updateDownloaderStatus(objType string, status *types.DownloaderStatus) {

	key := formLookupKey(objType, status.Safename)
	log.Printf("updataDownloaderStatus for %s\n", key)

	initDownloaderMaps()

	// Ignore if any Pending* flag is set
	if status.PendingAdd || status.PendingModify || status.PendingDelete {
		log.Printf("updataDownloaderStatus for %s, Skipping due to Pending*\n", key)
		return
	}

	changed := false
	if m, ok := downloaderStatusMap[key]; ok {
		if status.State != m.State {
			log.Printf("updareDownloaderStatus for %s, Downloader state changed from %v to %v\n", key, m.State, status.State)
			changed = true
		}
	} else {
		log.Printf("updateDownloaderStatus for %s, Downloader map add for %v\n", key, status.State)
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
			log.Fatal("updateDownloaderStatus for %s, Unsupported objType <%s>\n",
				status.Safename, objType)
			return
		}
	}

	log.Printf("updateDownloaderStatus for %s, Done\n", key)
}

func removeDownloaderConfig(objType string, safename string) {

	key := formLookupKey(objType, safename)

	log.Printf("removeDownloaderConfig for %s\n", key)

	if _, ok := downloaderConfigMap[key]; !ok {
		log.Printf("removeDownloaderConfig for %s, Config absent\n", key)
		return
	}

	log.Printf("removeDownloaderConfig for %s, Downloader config map delete\n", key)
	delete(downloaderConfigMap, key)

	configFilename := fmt.Sprintf("%s/%s/config/%s.json",
		downloaderBaseDirname, objType, safename)

	if err := os.Remove(configFilename); err != nil {
		log.Println(err)
	}
	log.Printf("removeDownloaderConfig for %s, Done\n", key)
}

func removeDownloaderStatus(objType string, statusFilename string) {

	key := formLookupKey(objType, statusFilename)

	log.Printf("removeDownloaderStatus for %s\n", key)

	if _, ok := downloaderStatusMap[key]; !ok {
		log.Printf("removeDownloaderStatus for %s, Downloader Status Map absent\n",
			key)
		return
	}
	fmt.Printf("removeDownloaderStatus for %s, Downloader status map delete\n", key)
	delete(downloaderStatusMap, key)

	log.Printf("removeDownloaderStatus done for %s\n", key)
}

func lookupDownloaderStatus(objType string, safename string) (types.DownloaderStatus, error) {

	key := formLookupKey(objType, safename)

	if m, ok := downloaderStatusMap[key]; ok {
		return m, nil
	}
	return types.DownloaderStatus{}, errors.New("No DownloaderStatus")
}

func checkStorageDownloadStatus(objType string, uuidStr string,
	config []types.StorageConfig, status []types.StorageStatus) (bool, types.SwState, string, time.Time) {

	key := formLookupKey(objType, uuidStr)
	log.Printf("checkStorageDownloaderStatus for %s\n", key)

	allErrors := ""
	var errorTime time.Time

	changed := false
	minState := types.MAXSTATE

	for i, sc := range config {

		ss := &status[i]
		safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

		fmt.Printf("checkStorageDownloaderStatus for %s, Found StorageConfig URL %s\n", 
			key, sc.DownloadURL)

		if sc.NeedVerification {
			// Shortcut if image is already verified
			vs, err := lookupVerificationStatusAny(objType, safename, sc.ImageSha256)
			if err == nil && vs.State == types.DELIVERED {
				log.Printf("checkStorageDownloaderStatus for %s, Found verified image sha %s\n",
					key, sc.ImageSha256)
				// If we don't already have a RefCount add one
				if !ss.HasVerifierRef {
					log.Printf("checkStorageDownloaderStatus for %s, !HasVerifierRef RefCount %d\n",
						key, vs.RefCount)
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
				log.Printf("checkStorageDownloaderStatus for %s, !HasDownloaderRef\n", key)
			createDownloaderConfig(objType, safename, &sc)
			ss.HasDownloaderRef = true
			changed = true
		}

		ds, err := lookupDownloaderStatus(objType, safename)
		if err != nil {
			log.Printf("checkStorageDownloaderStatus for %s, Download Map absent %s \n",
				key, err)
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
			log.Printf("checkStorageDownloaderStatus for %s, Received error from downloader for %s\n",
				key, ds.LastErr)
			ss.Error = ds.LastErr
			allErrors = appendError(allErrors, "downloader",
				ds.LastErr)
			ss.ErrorTime = ds.LastErrTime
			changed = true
		case types.DOWNLOAD_STARTED:
			// Nothing to do
		case types.DOWNLOADED:

			// if verification is needed
			if sc.NeedVerification {
				// start verifier for this object
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

func installDownloadedObjects(objType string, uuidStr string,
	config []types.StorageConfig, status []types.StorageStatus) {

	key := formLookupKey(objType, uuidStr)
	log.Printf("installDownloadedObjects for %s, %s\n",key)

	for i, sc := range config {

		ss := &status[i]

		safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

		// if final installation directory is defined
		// and the object is ready for installation
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

	key := formLookupKey(objType, safename)

	log.Printf("installDownloadedObject for %s\n", key)

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
		log.Fatal("installDownloadedObject for %s, invalid state %d",key, status.State)
		return
	}

	if config.ImageSha256 != "" {
		srcFilename = srcFilename + config.ImageSha256 + "/"
	}

	srcFilename = srcFilename + safename

	// ensure the file is present
	if _, err := os.Stat(srcFilename); err != nil {
		log.Printf("installDownloadedObject for %s, %s file absent\n", key, srcFilename)
		return
	}

	// create the destination directory
	if _, err := os.Stat(dstFilename); err == nil {
		if err := os.MkdirAll(dstFilename, 0700); err != nil {
			log.Fatal("installDownloadedObject for %s, Failed directory make %s\n", key, dstFilename)
		}
	}

	dstFilename = dstFilename + "/" + types.SafenameToFilename(safename)

	log.Printf("installDownloadedObject for %s, writing %s to %s\n", key, srcFilename, dstFilename)

	// move final installation point
	os.Rename(srcFilename, dstFilename)
	status.State = types.INSTALLED
	log.Printf("installDownloadedObject for %s, Done\n", key)
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
