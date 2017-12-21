// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
	"fmt"
	"github.com/zededa/go-provision/types"
	"log"
	"os"
	"reflect"
	"time"
)

// zedagent punlishes these config/status files
var baseOsConfigMap map[string]types.BaseOsConfig
var baseOsStatusMap map[string]types.BaseOsStatus

func initBaseOsMaps() {

	if baseOsConfigMap == nil {
		fmt.Printf("create baseOsConfig map\n")
		baseOsConfigMap = make(map[string]types.BaseOsConfig)
	}

	if baseOsStatusMap == nil {
		fmt.Printf("create baseOsStatus map\n")
		baseOsStatusMap = make(map[string]types.BaseOsStatus)
	}
}

func addOrUpdateBaseOsConfig(uuidStr string, config types.BaseOsConfig) {

	changed := false
	added := false

	if m, ok := baseOsConfigMap[uuidStr]; ok {
		// XXX or just compare version like elsewhere?
		if !reflect.DeepEqual(m, config) {
			fmt.Printf("baseOs config changed for %s\n", uuidStr)
			changed = true
		}
	} else {
		fmt.Printf("baseOs config add for %s\n", uuidStr)
		changed = true
		added = true
	}
	if changed {
		baseOsConfigMap[uuidStr] = config
	}

	if added {

		status := types.BaseOsStatus{
			UUIDandVersion: config.UUIDandVersion,
			DisplayName:    config.DisplayName,
			BaseOsVersion:  config.BaseOsVersion,
			ConfigSha256:   config.ConfigSha256,
		}

		status.StorageStatusList = make([]types.StorageStatus,
			len(config.StorageConfigList))

		for i, sc := range config.StorageConfigList {
			ss := &status.StorageStatusList[i]
			ss.DownloadURL = sc.DownloadURL
			ss.ImageSha256 = sc.ImageSha256
			ss.Target = sc.Target
			status.ConfigSha256 = sc.ImageSha256 // XXX:FIXME
		}

		baseOsStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentBaseOsStatusDirname, uuidStr)
		writeBaseOsStatus(&status, statusFilename)
	}

	if changed {
		baseOsHandleStatusUpdate(uuidStr)
	}
}

func baseOsHandleStatusUpdateSafename(safename string) {

	log.Printf("baseOsStatusUpdateSafename for %s\n", safename)

	for _, baseOsConfig := range baseOsConfigMap {

		for _, sc := range baseOsConfig.StorageConfigList {

			safename1 := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

			if safename == safename1 {

				uuidStr := baseOsConfig.UUIDandVersion.UUID.String()
				log.Printf("found baseOs %s for %s\n", uuidStr, safename)

				baseOsHandleStatusUpdate(uuidStr)
				break
			}
		}
	}
}

func baseOsHandleStatusUpdate(uuidStr string) {

	config, ok := baseOsConfigMap[uuidStr]
	if !ok {
		log.Printf("baseOsHandleStatusUpdate config is missing %s\n", uuidStr)
		return
	}

	status, ok := baseOsStatusMap[uuidStr]
	if !ok {
		log.Printf("baseOsHandleStatusUpdate for %s: Missing Status\n",
			uuidStr)
		return
	}

	changed := doBaseOsStatusUpdate(uuidStr, config, &status)

	if changed {
		log.Printf("baseOsHandleStatusUpdate status change for %s\n",
			uuidStr)
		baseOsStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentBaseOsStatusDirname, uuidStr)
		writeBaseOsStatus(&status, statusFilename)
	}
}

func doBaseOsStatusUpdate(uuidStr string, config types.BaseOsConfig,
	status *types.BaseOsStatus) bool {
	log.Printf("doBaseOsUpdate for %s\n", uuidStr)

	changed, proceed := doBaseOsInstall(uuidStr, config, status)
	if !proceed {
		return changed
	}

	if !config.Activate {
		doBaseOsInactivate(uuidStr, status)

		log.Printf("config.Activate is still not set for %s\n", uuidStr)
		return changed
	}

	log.Printf("config.Activate is set for %s\n", uuidStr)
	changed = doBaseOsActivate(uuidStr, config, status)
	log.Printf("doBaseOsStatusUpdate done for %s\n", uuidStr)
	return changed
}

func doBaseOsActivate(uuidStr string, config types.BaseOsConfig,
	status *types.BaseOsStatus) bool {

	log.Printf("doBaseOsActivate for %s\n", uuidStr)
	changed := false

	// XXX:FIXME do the stuff here
	// flip the currently active baseOs
	// to backup and adjust the baseOS
	// state accordingly

	if status.State == types.INSTALLED {
		if status.Activated == false {
			status.Activated = true
			changed = true
		}
	}

	return changed
}

func doBaseOsInstall(uuidStr string, config types.BaseOsConfig,
	status *types.BaseOsStatus) (bool, bool) {

	log.Printf("doBaseOsInstall for %s\n", uuidStr)
	changed := false

	if len(config.StorageConfigList) != len(status.StorageStatusList) {
		errString := fmt.Sprintf("storageConfig length mismatch: %d vs %d\n",
			len(config.StorageConfigList),
			len(status.StorageStatusList))
		status.Error = errString
		status.ErrorTime = time.Now()
		return changed, false
	}

	for i, sc := range config.StorageConfigList {
		ss := &status.StorageStatusList[i]
		if ss.DownloadURL != sc.DownloadURL ||
			ss.ImageSha256 != sc.ImageSha256 {
			// Report to zedcloud
			errString := fmt.Sprintf("Mismatch in storageConfig vs. Status:\n\t%s\n\t%s\n\t%s\n\t%s\n\n",
				sc.DownloadURL, ss.DownloadURL,
				sc.ImageSha256, ss.ImageSha256)
			log.Println(errString)
			status.Error = errString
			status.ErrorTime = time.Now()
			changed = true
			return changed, false
		}
	}

	downloadchange, downloaded :=
		checkBaseOsStorageDownloadStatus(uuidStr, config, status)

	if downloaded == false {
		return changed || downloadchange, false
	}

	verifychange, verified :=
		checkBaseOsStorageVerifyStatus(uuidStr, config, status)

	if verified == false {
		return changed || verifychange, false
	}

	installDownloadedObjects(baseOsObj, config.StorageConfigList,
		status.StorageStatusList)

	// Automatically move from DELIVERED to INSTALLED
	status.State = types.INSTALLED
	changed = true
	log.Printf("doInstall done for %s\n", uuidStr)
	return changed, true
}

func checkBaseOsStorageDownloadStatus(uuidStr string,
	config types.BaseOsConfig,
	status *types.BaseOsStatus) (bool, bool) {

	changed, minState, allErrors, errorTime := checkStorageDownloadStatus(baseOsObj,
		config.StorageConfigList, status.StorageStatusList)

	if minState == types.MAXSTATE {
		minState = types.DOWNLOADED
	}

	status.State = minState
	status.Error = allErrors
	status.ErrorTime = errorTime

	if minState == types.INITIAL {
		log.Printf("Download error for %s\n", uuidStr)
		return changed, false
	}

	if minState < types.DOWNLOADED {
		log.Printf("Waiting for all downloads for %s\n", uuidStr)
		return changed, false
	}

	log.Printf("Done with downloads for %s\n", uuidStr)
	return changed, true
}

func checkBaseOsStorageVerifyStatus(uuidStr string,
	config types.BaseOsConfig,
	status *types.BaseOsStatus) (bool, bool) {

	changed, minState, allErrors, errorTime := checkStorageVerifierStatus(baseOsObj,
		uuidStr, config.StorageConfigList, status.StorageStatusList)

	if minState == types.MAXSTATE {
		// Odd; no StorageConfig in list
		minState = types.DELIVERED
	}
	status.State = minState
	status.Error = allErrors
	status.ErrorTime = errorTime
	if minState == types.INITIAL {
		log.Printf("Verify error for %s\n", uuidStr)
		return changed, false
	}

	if minState < types.DELIVERED {
		log.Printf("Waiting for all verifications for %s\n", uuidStr)
		return changed, false
	}
	log.Printf("Done with verifications for %s\n", uuidStr)
	return changed, true
}

func removeBaseOsConfig(uuidStr string) {

	log.Printf("removeBaseOsConfig for %s\n", uuidStr)

	if _, ok := baseOsConfigMap[uuidStr]; !ok {
		log.Printf("baseOs config missing %s for delete\n", uuidStr)
		return
	}
	delete(baseOsConfigMap, uuidStr)
	removeBaseOsStatus(uuidStr)

	log.Printf("removeBaseOSConfig  %s done\n", uuidStr)
}

func removeBaseOsStatus(uuidStr string) {

	status, ok := baseOsStatusMap[uuidStr]
	if !ok {
		log.Printf("removeBaseOsStatus for %s, missing Status\n", uuidStr)
		return
	}

	changed, del := doBaseOsRemove(uuidStr, &status)
	if changed {
		log.Printf("removeBaseOsStatus change for %s\n", uuidStr)
		baseOsStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentBaseOsStatusDirname, uuidStr)
		writeBaseOsStatus(&status, statusFilename)
	}

	if del {
		log.Printf("removeBaseOsStatus done for %s\n", uuidStr)

		// Write out what we modified to AppInstanceStatus aka delete
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentBaseOsStatusDirname, uuidStr)
		if err := os.Remove(statusFilename); err != nil {
			log.Println(err)
		}
		delete(baseOsStatusMap, uuidStr)
	}
}

func doBaseOsRemove(uuidStr string, status *types.BaseOsStatus) (bool, bool) {

	log.Printf("doBaseOsRemove for %s\n", uuidStr)

	changed := false
	del := false

	if status.Activated {
		changed = doBaseOsInactivate(uuidStr, status)
	}

	if !status.Activated {
		changed, del = doBaseOsUninstall(uuidStr, status)
	}

	log.Printf("doBaseOsRemove done for %s\n", uuidStr)
	return changed, del
}

func doBaseOsInactivate(uuidStr string, status *types.BaseOsStatus) bool {

	changed := false

	// XXX:FIXME do the stuff here
	// flip the currently active baseOs
	// to backup and adjust the baseOS
	// state accordingly

	if status.Activated {
		status.Activated = false
		changed = true
	}

	return changed
}

func doBaseOsUninstall(uuidStr string, status *types.BaseOsStatus) (bool, bool) {

	del := false
	changed := false
	removedAll := true

	for i, _ := range status.StorageStatusList {

		ss := &status.StorageStatusList[i]

		// Decrease refcount if we had increased it
		if ss.HasVerifierRef {
			removeBaseOsVerifierConfig(ss.ImageSha256)
			ss.HasVerifierRef = false
			changed = true
		}

		_, err := lookupBaseOsVerificationStatusSha256(ss.ImageSha256)

		// XXX if additional refs it will not go away
		if false && err == nil {
			log.Printf("lookupBaseOsVerificationStatus %s not yet gone\n",
				ss.ImageSha256)
			removedAll = false
			continue
		}
	}

	if !removedAll {
		log.Printf("Waiting for all verifier removes for %s\n", uuidStr)
		return changed, del
	}

	removedAll = true

	for i, _ := range status.StorageStatusList {

		ss := &status.StorageStatusList[i]
		safename := types.UrlToSafename(ss.DownloadURL, ss.ImageSha256)
		fmt.Printf("Found StorageStatus URL %s safename %s\n",
			ss.DownloadURL, safename)
		// Decrease refcount if we had increased it
		if ss.HasDownloaderRef {
			removeBaseOsDownloaderConfig(safename)
			ss.HasDownloaderRef = false
			changed = true
		}

		_, err := lookupBaseOsDownloaderStatus(ss.ImageSha256)
		// XXX if additional refs it will not go away
		if false && err == nil {
			log.Printf("LookupDownloaderStatus %s not yet gone\n",
				safename)
			removedAll = false
			continue
		}
	}

	if !removedAll {
		log.Printf("Waiting for all downloader removes for %s\n", uuidStr)
		return changed, del
	}

	// XXX:FIXME, fill up the details
	if status.State == types.INITIAL {
		del = false
	}
	status.State = types.INITIAL

	return changed, del
}
