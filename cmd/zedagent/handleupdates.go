// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
	"fmt"
	"log"
	"io/ioutil"
	"encoding/json"
	"github.com/zededa/go-provision/types"
)

// zedagent is the publishes for these config files
var downloaderConfigMap map[string]types.DownloaderConfig
var downloaderStatusMap map[string]types.DownloaderStatus

// zedagent is the subscriber for thesestatusconfig files
var verifierConfigMap map[string]types.VerifyImageConfig
var verifierStatusMap map[string]types.VerifyImageStatus

func createDevConfigDownloaderConfig(safename string,
							 sc *types.StorageConfig) {

	createDownloaderConfig(devConfigObj, safename, sc)
}

func createBaseOsDownloaderConfig(safename string,
						sc *types.StorageConfig) {

	createDownloaderConfig(baseOsObj, safename, sc)
}

func createDevConfigVerifierConfig(safename string,
						 sc *types.StorageConfig) {

	createVerifierConfig(devConfigObj, safename, sc)
}

func createBaseOsVerifierConfig(safename string,
						 sc *types.StorageConfig) {

	createVerifierConfig(baseOsObj, safename, sc)
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
		// XXX:FIXME process specific to the object Type	
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
		// XXX:FIXME process specific to the object Type	
	}

	log.Printf("updateVerifierStatus done for %s\n", status.Safename)
}

func removeDownloaderStatus(objType string, statusFilename string) {

	key := objType + "x" + statusFilename

	if m, ok := downloaderStatusMap[key]; !ok {
		log.Printf("removeDownloaderStatus for %s - not found\n",
			key)
	} else {
		fmt.Printf("downloader map delete for %s(%v)\n", key, m.State)
		delete(downloaderStatusMap, key)
		// XXX:FIXME process specific to the object Type	
	}
	log.Printf("removeDownloaderStatus done for %s\n", key)
}

func removeVerifierStatus(objType string, statusFilename string) {

	key := objType + "x" + statusFilename

	if m, ok := verifierStatusMap[key]; !ok {
		log.Printf("removeVerifierStatus for %s - not found\n",
			key)
	} else {
		fmt.Printf("verifier map delete for %s(%v)\n", key, m.State)
		delete(verifierStatusMap, key)
		// XXX:FIXME process specific to the object Type	
	}
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
