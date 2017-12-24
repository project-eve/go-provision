// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// cert object event handlers
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

// handle Storage(download/verification) (Config/Status) events
func certObjHandleStatusUpdateSafename(safename string) {

	log.Printf("certObjHandleStatusUpdateSafename for %s\n", safename)

	for _, certObjConfig := range certObjConfigMap {

		for _, sc := range certObjConfig.StorageConfigList {

			safename1 := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

			if safename == safename1 {

				uuidStr := certObjConfig.UUIDandVersion.UUID.String()
				log.Printf("certObjHandleStatusUpdateSafename for %s, Found certObj %s\n", safename, uuidStr)

				certObjHandleStatusUpdate(uuidStr)
			}
		}
	}
}

func addOrUpdateCertObjConfig(uuidStr string, config types.CertObjConfig) {

	added := false
	changed := false

	if m, ok := certObjConfigMap[uuidStr]; ok {
		// XXX or just compare version like elsewhere?
		if !reflect.DeepEqual(m, config) {
			fmt.Printf("addOrUpdateCertObjConfig for %s, Config change\n", uuidStr)
			changed = true
		}
	} else {
		fmt.Printf("addOrUpdateCertObjConfig for %s, Config add\n", uuidStr)
		added = true
		changed = true
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

func certObjHandleStatusUpdate(uuidStr string) {

	log.Printf("certObjHandleStatusUpdate for %s\n", uuidStr)

	config, ok := certObjConfigMap[uuidStr]
	if !ok {
		log.Printf("certObjHandleStatusUpdate for %s, Config absent\n", uuidStr)
		return
	}

	status, ok := certObjStatusMap[uuidStr]
	if !ok {
		log.Printf("certObjHandleStatusUpdate for %s, Status absent\n",
			uuidStr)
		return
	}

	changed := doCertObjStatusUpdate(uuidStr, config, &status)

	if changed {
		log.Printf("certObjHandleStatusUpdate for %s, Status changed\n",
			uuidStr)
		certObjStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		writeCertObjStatus(&status, statusFilename)
	}
}

func doCertObjStatusUpdate(uuidStr string, config types.CertObjConfig,
	status *types.CertObjStatus) bool {

	log.Printf("doCertObjUpdate for %s\n", uuidStr)

	changed, proceed := doCertObjInstall(uuidStr, config, status)
	if !proceed {
		return changed
	}

	log.Printf("doCertObjStatusUpdate for %s, Done\n", uuidStr)
	return changed
}

func doCertObjInstall(uuidStr string, config types.CertObjConfig,
	status *types.CertObjStatus) (bool, bool) {

	log.Printf("doCertObjInstall for %s\n", uuidStr)
	changed := false

	if len(config.StorageConfigList) != len(status.StorageStatusList) {
		errString := fmt.Sprintf("doCertObjInstall for %s, Storage length mismatch: %d vs %d\n", uuidStr,
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
			errString := fmt.Sprintf("doCertObjInstall for %s, Storage config mismatch:\n\t%s\n\t%s\n\t%s\n\t%s\n\n", uuidStr,
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
		checkCertObjStorageDownloadStatus(uuidStr, config, status)

	if downloaded == false {
		return changed || downloadchange, false
	}

	// install the certs now
	installDownloadedObjects(certObj, uuidStr, config.StorageConfigList,
		status.StorageStatusList)

	// Automatically move from DOWNLOADED to INSTALLED
	status.State = types.INSTALLED
	changed = true
	log.Printf("doCertObjInstall for %s, Done\n", uuidStr)
	return changed, true
}

func checkCertObjStorageDownloadStatus(uuidStr string,
	config types.CertObjConfig,
	status *types.CertObjStatus) (bool, bool) {

	changed, minState, allErrors, errorTime := checkStorageDownloadStatus(certObj, uuidStr, config.StorageConfigList, status.StorageStatusList)

	status.State = minState
	status.Error = allErrors
	status.ErrorTime = errorTime

	if minState == types.INITIAL {
		log.Printf("checkCertObjDownloadStatus for %s, Download erro\n", uuidStr)
		return changed, false
	}

	if minState < types.DOWNLOADED {
		log.Printf("checkCertObjDownloaStatus %s, Waiting for downloads\n", uuidStr)
		return changed, false
	}

	log.Printf("checkCertObjDownloadStatus for %s, Downloads done\n", uuidStr)
	return changed, true
}

func removeCertObjConfig(uuidStr string) {

	log.Printf("removeCertObjConfig for %s\n", uuidStr)

	if _, ok := certObjConfigMap[uuidStr]; !ok {
		log.Printf("removeCertObjConfig for %s, Config absent\n", uuidStr)
		return
	}

	delete(certObjConfigMap, uuidStr)

	removeCertObjStatus(uuidStr)

	log.Printf("removeCertObjConfig for %s, Done\n", uuidStr)
}

func removeCertObjStatus(uuidStr string) {

	status, ok := certObjStatusMap[uuidStr]
	if !ok {
		log.Printf("removeCertObjStatus for %s, Status absent\n", uuidStr)
		return
	}

	changed, del := doCertObjRemove(uuidStr, &status)
	if changed {
		log.Printf("removeCertObjStatus for %s, Status changed\n", uuidStr)
		certObjStatusMap[uuidStr] = status
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		writeCertObjStatus(&status, statusFilename)
	}

	if del {

		// Write out what we modified to AppInstanceStatus aka delete
		statusFilename := fmt.Sprintf("%s/%s.json",
			zedagentCertObjStatusDirname, uuidStr)
		if err := os.Remove(statusFilename); err != nil {
			log.Println(err)
		}

		delete(certObjStatusMap, uuidStr)
		log.Printf("removeCertObjStatus for %s: Done\n", uuidStr)
	}
}

func doCertObjRemove(uuidStr string, status *types.CertObjStatus) (bool, bool) {

	log.Printf("doCertObjRemove for %s\n", uuidStr)

	changed := false
	del := false

	changed, del = doCertObjUninstall(uuidStr, status)

	log.Printf("doCertObjRemove for %s, Done\n", uuidStr)
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
		fmt.Printf("doCertObjUninstall for %s, Found StorageStatus safename %s\n", uuidStr, safename)
		// Decrease refcount if we had increased it
		if ss.HasDownloaderRef {
			removeCertObjDownloaderConfig(safename)
			ss.HasDownloaderRef = false
			changed = true
		}

		_, err := lookupCertObjDownloaderStatus(safename)
		// XXX if additional refs it will not go away
		if false && err == nil {
			log.Printf("doCertObjIninstall for %s, Download %s not yet gone\n",
				uuidStr, safename)
			removedAll = false
			continue
		}
	}

	if !removedAll {
		log.Printf("doCertObjUninstall for %s, Waiting for download purge\n", uuidStr)
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
