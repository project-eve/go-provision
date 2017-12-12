// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
	"fmt"
	"log"
	"errors"
	"io/ioutil"
	"encoding/json"
	"reflect"
	"os"
	"time"
	"github.com/zededa/go-provision/types"
)

// zedagent punlishes these config/status files
var baseOsConfigMap map[string]types.BaseOsConfig
var baseOsStatusMap map[string]types.BaseOsStatus

// zedagent is the publishes for these config files
var downloaderConfigMap map[string]types.DownloaderConfig
var verifierConfigMap map[string]types.VerifyImageConfig

// zedagent is the subscriber for these status files
var downloaderStatusMap map[string]types.DownloaderStatus
var verifierStatusMap map[string]types.VerifyImageStatus

func initMaps() {

    if baseOsConfigMap == nil {
        fmt.Printf("create baseOsConfig map\n")
        baseOsConfigMap = make(map[string]types.BaseOsConfig)
    }

    if baseOsStatusMap == nil {
        fmt.Printf("create baseOsStatus map\n")
        baseOsStatusMap = make(map[string]types.BaseOsStatus)
    }

	if downloaderConfigMap == nil {
        fmt.Printf("create downloaderConfig map\n")
        downloaderConfigMap = make(map[string]types.DownloaderConfig)
	}

	if verifierConfigMap == nil {
        fmt.Printf("create verifierConfig map\n")
        verifierConfigMap = make(map[string]types.VerifyImageConfig)
	}

	if downloaderStatusMap == nil {
        fmt.Printf("create downloadetStatus map\n")
        downloaderStatusMap = make(map[string]types.DownloaderStatus)
	}

	if verifierStatusMap == nil {
        fmt.Printf("create verifierStatus map\n")
        verifierStatusMap = make(map[string]types.VerifyImageStatus)
	}
}

func addOrUpdateBaseOsConfig(uuidStr string, config types.BaseOsConfig) {

	initMaps()
	changed := false
	added := false

  if m, ok :=baseOsConfigMap [uuidStr]; ok {
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
	        if _, ok := baseOsConfigMap[uuidStr]; !ok {
            status := types.BaseOsStatus{
                UUIDandVersion: config.UUIDandVersion,
                DisplayName:    config.DisplayName,
            }

            status.StorageStatusList = make([]types.StorageStatus,
                len(config.StorageConfigList))
            for i, sc := range config.StorageConfigList {
                ss := &status.StorageStatusList[i]
                ss.DownloadURL = sc.DownloadURL
                ss.ImageSha256 = sc.ImageSha256
                ss.Target = sc.Target
            }

            baseOsStatusMap[uuidStr] = status
            statusFilename := fmt.Sprintf("%s/%s.json",
                zedagentBaseOsStatusDirname, uuidStr)
            writeBaseOsStatus(&status, statusFilename)
        }
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
	log.Printf("doBsseOsUpdate for %s\n", uuidStr)

	changed, proceed := doBaseOsInstall(uuidStr, config, status)

	if !proceed {
		return changed
	}

	if !config.Activate {
		if status.Activated {
			doBaseOsInactivate(uuidStr, status)
		}

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

	// Automatically move from DELIVERED to INSTALLED
    status.State = types.INSTALLED
    changed = true
    log.Printf("doInstall done for %s\n", uuidStr)
    return changed, true
}

func checkBaseOsStorageDownloadStatus(uuidStr string,
								config types.BaseOsConfig,
								status *types.BaseOsStatus) (bool, bool) {

	allErrors := ""
    var errorTime time.Time

	changed   := false
	minState  := types.MAXSTATE

	for i, sc := range config.StorageConfigList {

		ss := &status.StorageStatusList[i]
		safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)

		fmt.Printf("Found StorageConfig URL %s safename %s\n",
			sc.DownloadURL, safename)

		// Shortcut if image is already verified
		vs, err := lookupBaseOsVerificationStatusAny(safename, sc.ImageSha256)
		if err == nil && vs.State == types.DELIVERED {
			log.Printf("doUpdate found verified image for %s sha %s\n",
				safename, sc.ImageSha256)
			// If we don't already have a RefCount add one
			if !ss.HasVerifierRef {
				log.Printf("doUpdate !HasVerifierRef vs.RefCount %d for %s\n",
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

		if !ss.HasDownloaderRef {
			log.Printf("doBaseOsInstall !HasDownloaderRef for %s\n",
				safename)
			createBaseOsDownloaderConfig(safename, &sc)
			ss.HasDownloaderRef = true
			changed = true
		}
		ds, err := lookupBaseOsDownloaderStatus(safename)
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
			errorTime = ds.LastErrTime
			changed = true
		case types.DOWNLOAD_STARTED:
			// Nothing to do
		case types.DOWNLOADED:
			// Kick verifier to start if it hasn't already
			if !ss.HasVerifierRef {
				createBaseOsVerifierConfig(safename, &sc)
				ss.HasVerifierRef = true
				changed = true
			}
		}
	}

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
	return  changed, true
}

func checkBaseOsStorageVerifyStatus(uuidStr string,
					config types.BaseOsConfig,
					status *types.BaseOsStatus) (bool, bool) {

	allErrors := ""
    var errorTime time.Time
	changed  := false
    minState := types.MAXSTATE

    for i, sc := range config.StorageConfigList {
        ss := &status.StorageStatusList[i]
        safename := types.UrlToSafename(sc.DownloadURL, sc.ImageSha256)
        fmt.Printf("Found StorageConfig URL %s safename %s\n",
            sc.DownloadURL, safename)

        vs, err := lookupBaseOsVerificationStatusAny(safename, sc.ImageSha256)

        if err != nil {
            log.Printf("lookupBaseOsVerificationStatusAny %s sha %s failed %v\n",
                safename, sc.ImageSha256, err)
            continue
        }
        if minState > vs.State {
            minState = vs.State
        }
        if vs.State != ss.State {
            ss.State = vs.State
            changed = true
        }
        switch vs.State {
        case types.INITIAL:
            log.Printf("Received error from verifier for %s: %s\n",
                safename, vs.LastErr)
            ss.Error = vs.LastErr
            allErrors = appendError(allErrors, "verifier",
                vs.LastErr)
            ss.ErrorTime = vs.LastErrTime
            errorTime = vs.LastErrTime
            changed = true
        default:

            ss.ActiveFileLocation = baseOsVerifiedDirname + "/" + vs.Safename
            log.Printf("Update SSL ActiveFileLocation for %s: %s\n",
                uuidStr, ss.ActiveFileLocation)
            changed = true
        }
    }

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
        log.Printf("removeBaseOsStatus for %s, missing Status\n",
            uuidStr)
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

	// XXX:FIXME, fill up the details

	if status.Activated {
		status.Activated  = false
		changed = true
	}

	return changed
}

func doBaseOsUninstall(uuidStr string, status *types.BaseOsStatus) (bool, bool) {

	del        := false
	changed    := false
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

func createBaseOsDownloaderConfig(safename string,
						sc *types.StorageConfig) {

	createDownloaderConfig(baseOsObj, safename, sc)
}

func createBaseOsVerifierConfig(safename string,
						 sc *types.StorageConfig) {

	createVerifierConfig(baseOsObj, safename, sc)
}

func removeBaseOsDownloaderConfig(safename string) {

	removeDownloaderConfig(baseOsObj, safename)
}

func removeBaseOsVerifierConfig(safename string) {

	removeVerifierConfig(baseOsObj, safename)
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
			Safename:			safename,
			DownloadURL:		sc.DownloadURL,
			MaxSize:			sc.MaxSize,
			TransportMethod:	sc.TransportMethod,
			Dpath:				sc.Dpath,
			ApiKey:				sc.ApiKey,
			Password:			sc.Password,
			ImageSha256:		sc.ImageSha256,
			ObjType:			objType,
			DownloadObjDir:		objDnldDirname + "/" + objType,
			FinalObjDir:		sc.FinalObjDir,
			NeedVerification:	sc.NeedVerification,
			RefCount:			1,
		}
		downloaderConfigMap[key] = n
	}

	configFilename := fmt.Sprintf("%s/%s.json",
			downloaderConfigBaseDirname + "/" + objType + "/config", safename)

	writeDownloaderConfig(downloaderConfigMap[key], configFilename)

	log.Printf("createDownloaderConfig done for %s\n",
		safename)
}

func createVerifierConfig(objType string, safename string,
						sc *types.StorageConfig) {

	if verifierConfigMap == nil {
		log.Printf("create verifier config map\n")
		verifierConfigMap = make(map[string]types.VerifyImageConfig)
	}

	key := objType + "x" + safename

	if m, ok := verifierConfigMap[key]; ok {
		log.Printf("downloader config exists for %s refcount %d\n",
			safename, m.RefCount)
		m.RefCount += 1
	} else {
		log.Printf(" dev config verifier config add for %s\n", safename)
		n := types.VerifyImageConfig{
			Safename:         safename,
			DownloadURL:      sc.DownloadURL,
			ImageSha256:      sc.ImageSha256,
			CertificateChain: sc.CertificateChain,
			ImageSignature:   sc.ImageSignature,
			SignatureKey:     sc.SignatureKey,
			ObjType:		  objType,
			RefCount:         1,
		}
		verifierConfigMap[key] = n
	}

	configFilename := fmt.Sprintf("%s/%s.json",
			verifierConfigBaseDirname + "/" + objType + "/config", safename)

	writeVerifierConfig(verifierConfigMap[key], configFilename)

	log.Printf("createVerifierConfig done for %s\n",
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
		baseOsHandleStatusUpdateSafename(status.Safename)
	}

	log.Printf("updateDownloaderStatus done for %s\n", status.Safename)
}

func updateVerifierStatus(objType string, status *types.VerifyImageStatus) {

	if verifierStatusMap == nil {
		log.Printf("create verifier status map\n")
		verifierStatusMap = make(map[string]types.VerifyImageStatus)
	}

	// Ignore if any Pending* flag is set
	if status.PendingAdd || status.PendingModify || status.PendingDelete {
		log.Printf("updataVerifierStatus skipping due to Pending* for %s\n",
			status.Safename)
		return
	}

	key := objType + "x" + status.Safename

	changed := false
	if m, ok := verifierStatusMap[key]; ok {
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
		verifierStatusMap[key] = *status
		baseOsHandleStatusUpdateSafename(status.Safename)
	}

	log.Printf("updateVerifierStatus done for %s\n", status.Safename)
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
        downloaderConfigBaseDirname, objType, safename)

    if err := os.Remove(configFilename); err != nil {
        log.Println(err)
    }
	log.Printf("removeDownloaderConfig done for %s\n", key)
}

func removeVerifierConfig(objType string, safename string) {

	key := objType + "x" + safename

	if _, ok := verifierConfigMap[key]; !ok {
		log.Printf("removeVerifierConfig for %s - not found\n", key)
		return
	}
	fmt.Printf("verifier config map delete for %s\n", key)
	delete(verifierConfigMap, key)

    configFilename := fmt.Sprintf("%s/%s/config/%s.json",
        verifierConfigBaseDirname, objType, safename)

    if err := os.Remove(configFilename); err != nil {
        log.Println(err)
    }

	log.Printf("removeVerifierStatus done for %s\n", key)
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

func removeVerifierStatus(objType string, statusFilename string) {

	key := objType + "x" + statusFilename

	if _, ok := verifierStatusMap[key]; !ok {
		log.Printf("removeVerifierStatus for %s - not found\n",
			key)
		return
	}

	fmt.Printf("verifier status map delete for %s\n", key)
	delete(verifierStatusMap, key)

	log.Printf("removeVerifierStatus done for %s\n", key)
}

func writeDownloaderConfig(config types.DownloaderConfig,
					configFilename string) {

	b, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal DownloaderConfig")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(configFilename, b, 0644)
	if err != nil {
		log.Fatal(err, configFilename)
	}
}

func writeVerifierConfig(config types.VerifyImageConfig,
	configFilename string) {
	b, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal VeriferConfig")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(configFilename, b, 0644)
	if err != nil {
		log.Fatal(err, configFilename)
	}
}

func lookupBaseOsVerificationStatusSha256Internal(sha256 string) (*types.VerifyImageStatus, error) {

    for _, m := range verifierStatusMap {
        if m.ImageSha256 == sha256 {
            return &m, nil
        }
    }

    return nil, errors.New("No verificationStatusMap for sha")
}

func lookupBaseOsVerificationStatus(safename string) (types.VerifyImageStatus, error) {

	key := baseOsObj + "x" + safename

    if m, ok := verifierStatusMap[key]; ok {

        log.Printf("lookupVerifyImageStatus: found based on safename %s\n",
            safename)
        return m, nil
	}
    return types.VerifyImageStatus{},
			 errors.New("No verificationStatusMap for safename")
}

func lookupBaseOsVerificationStatusSha256(sha256 string) (types.VerifyImageStatus, error) {

	m, err := lookupBaseOsVerificationStatusSha256Internal(sha256)
	if err != nil {
		return types.VerifyImageStatus{}, err
	} else {
		log.Printf("found status based on sha256 %s safename %s\n",
			sha256, m.Safename)
		return *m, nil
	}
}

func lookupBaseOsVerificationStatusAny(safename string, sha256 string) (types.VerifyImageStatus, error) {

	m0, err := lookupBaseOsVerificationStatus(safename)
	if err == nil {
		return m0, nil
	}
	m1, err := lookupBaseOsVerificationStatusSha256Internal(sha256)
	if err == nil {
		log.Printf("lookupVerifyImageStatusAny: found based on sha %s\n",
			sha256)
		return *m1, nil
	}
	return types.VerifyImageStatus{},
	       errors.New("No VerifyImageStatus for safename nor sha")
}

func lookupBaseOsDownloaderStatus(safename string) (types.DownloaderStatus, error) {

	key := baseOsObj + "x" + safename

    if m, ok := downloaderStatusMap[key]; ok {
        return m, nil
    }

    return types.DownloaderStatus{}, errors.New("No DownloaderStatus")
}

func appendError(allErrors string, prefix string, lasterr string) string {
    return fmt.Sprintf("%s%s: %s\n\n", allErrors, prefix, lasterr)
}
