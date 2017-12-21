// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
	"encoding/json"
	"fmt"
	"github.com/zededa/go-provision/types"
	"io/ioutil"
	"log"
	"os"
	"reflect"
	"time"
)

// zedagent punlishes these config/status files
var certObjConfigMap map[string]types.CertObjConfig
var certObjStatusMap map[string]types.CertObjStatus

func initCertObjMaps() {

	if certObjConfigMap == nil {
		fmt.Printf("create certObjConfig map\n")
		certObjConfigMap = make(map[string]types.CertObjConfig)
	}

	if certObjStatusMap == nil {
		fmt.Printf("create certObjStatus map\n")
		certObjStatusMap = make(map[string]types.CertObjStatus)
	}
}

func certObjHandleStatusUpdateSafename(safename string) {

	log.Printf("certObjHandleStatusUpdateSafename for %s\n", safename)

	for _, certObjConfig := range certObjConfigMap {

		for _, sc := range certObjConfig.StorageConfigList {

			safename1 := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

			if safename == safename1 {

				uuidStr := certObjConfig.UUIDandVersion.UUID.String()
				log.Printf("found certObj %s for %s\n", uuidStr, safename)

				certObjHandleStatusUpdate(uuidStr)
				break
			}
		}
	}
}

func addOrUpdateCertObjConfig(uuidStr string, config types.CertObjConfig) {

	changed := false
	added := false

	if m, ok := certObjConfigMap[uuidStr]; ok {
		// XXX or just compare version like elsewhere?
		if !reflect.DeepEqual(m, config) {
			fmt.Printf("certObj config changed for %s\n", uuidStr)
			changed = true
		}
	} else {
		fmt.Printf("certObj config add for %s\n", uuidStr)
		changed = true
		added = true
	}
	if changed {
		certObjConfigMap[uuidStr] = config
	}

	if added {

		status := types.CertObjStatus{
			UUIDandVersion: config.UUIDandVersion,
			ConfigSha256:   config.ConfigSha256,
		}

		status.StorageStatusList = make([]types.StorageStatus,
			len(config.StorageConfigList))

		for i, sc := range config.StorageConfigList {
			ss := &status.StorageStatusList[i]
			ss.DownloadURL = sc.DownloadURL
			ss.ImageSha256 = sc.ImageSha256
		}

		certObjStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		writeCertObjStatus(&status, statusFilename)
	}

	if changed {
		certObjHandleStatusUpdate(uuidStr)
	}
}

func certObjHandleStatusUpdate(safename string) {

	log.Printf("certObjHandleStatusUpdate for %s\n", safename)

	config, ok := certObjConfigMap[safename]
	if !ok {
		log.Printf("certObjHandleStatusUpdate config is missing %s\n", safename)
		return
	}

	status, ok := certObjStatusMap[safename]
	if !ok {
		log.Printf("certObjHandleStatusUpdate for %s: Missing Status\n",
			safename)
		return
	}

	changed := doCertObjStatusUpdate(safename, config, &status)

	if changed {
		log.Printf("certObjHandleStatusUpdate status change for %s\n",
			safename)
		certObjStatusMap[safename] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, safename)
		writeCertObjStatus(&status, statusFilename)
	}
}

func doCertObjStatusUpdate(safename string, config types.CertObjConfig,
	status *types.CertObjStatus) bool {
	log.Printf("doCertObjUpdate for %s\n", safename)

	changed, proceed := doCertObjInstall(safename, config, status)
	if !proceed {
		return changed
	}

	log.Printf("doCertObjStatusUpdate done for %s\n", safename)
	return changed
}

func doCertObjInstall(safename string, config types.CertObjConfig,
	status *types.CertObjStatus) (bool, bool) {

	log.Printf("doCertObjInstall for %s\n", safename)
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
		checkCertObjStorageDownloadStatus(safename, config, status)

	if downloaded == false {
		return changed || downloadchange, false
	}

	installDownloadedObjects(certObj, config.StorageConfigList,
		status.StorageStatusList)

	// Automatically move from DOWNLOADED to INSTALLED
	status.State = types.INSTALLED
	changed = true
	log.Printf("doInstall done for %s\n", safename)
	return changed, true
}

func checkCertObjStorageDownloadStatus(uuidStr string,
	config types.CertObjConfig,
	status *types.CertObjStatus) (bool, bool) {

	changed, minState, allErrors, errorTime := checkStorageDownloadStatus(certObj,
		config.StorageConfigList, status.StorageStatusList)

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

func removeCertObjConfig(uuidStr string) {
	log.Printf("removeCertObjConfig for %s\n", uuidStr)

	if _, ok := certObjConfigMap[uuidStr]; !ok {
		log.Printf("baseOs config missing %s for delete\n", uuidStr)
		return
	}

	delete(certObjConfigMap, uuidStr)

	removeCertObjStatus(uuidStr)

	log.Printf("removeCertObjConfig  %s done\n", uuidStr)
}

func removeCertObjStatus(uuidStr string) {

	status, ok := certObjStatusMap[uuidStr]
	if !ok {
		log.Printf("removeCertObjStatus for %s, missing Status\n", uuidStr)
		return
	}

	changed, del := doCertObjRemove(uuidStr, &status)
	if changed {
		log.Printf("removeCertObjStatus change for %s\n", uuidStr)
		certObjStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		writeCertObjStatus(&status, statusFilename)
	}

	if del {
		log.Printf("removeCertObjStatus done for %s\n", uuidStr)

		// Write out what we modified to AppInstanceStatus aka delete
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		if err := os.Remove(statusFilename); err != nil {
			log.Println(err)
		}
		delete(certObjStatusMap, uuidStr)
	}
}

func doCertObjRemove(uuidStr string, status *types.CertObjStatus) (bool, bool) {

	log.Printf("doCertObjRemove for %s\n", uuidStr)

	changed := false
	del := false

	changed, del = doCertObjUninstall(uuidStr, status)

	log.Printf("doCertObjRemove done for %s\n", uuidStr)
	return changed, del
}

func doCertObjUninstall(uuidStr string, status *types.CertObjStatus) (bool, bool) {

	del := false
	changed := false
	removedAll := true

	removedAll = true

	for i, _ := range status.StorageStatusList {

		ss := &status.StorageStatusList[i]
		safename := types.UrlToSafename(ss.DownloadURL, ss.ImageSha256)
		fmt.Printf("Found StorageStatus URL %s safename %s\n",
			ss.DownloadURL, safename)
		// Decrease refcount if we had increased it
		if ss.HasDownloaderRef {
			removeCertObjDownloaderConfig(safename)
			ss.HasDownloaderRef = false
			changed = true
		}

		_, err := lookupCertObjDownloaderStatus(safename)
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

func writeCertObjStatus(status *types.CertObjStatus, statusFilename string) {
	bytes, err := json.Marshal(status)
	if err != nil {
		log.Fatal(err, "json Marshal certObjStatus")
	}

	err = ioutil.WriteFile(statusFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}
