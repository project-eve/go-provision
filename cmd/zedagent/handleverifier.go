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
var verifierConfigMap map[string]types.VerifyImageConfig

// zedagent is the subscriber for these status files
var verifierStatusMap map[string]types.VerifyImageStatus

func initVerifierMaps() {

	if verifierConfigMap == nil {
		fmt.Printf("create verifierConfig map\n")
		verifierConfigMap = make(map[string]types.VerifyImageConfig)
	}

	if verifierStatusMap == nil {
		fmt.Printf("create verifierStatus map\n")
		verifierStatusMap = make(map[string]types.VerifyImageStatus)
	}
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
			ObjType:          objType,
			RefCount:         1,
		}
		verifierConfigMap[key] = n
	}

	configFilename := fmt.Sprintf("%s/%s/config/%s.json",
		verifierBaseDirname, objType, safename)

	writeVerifierConfig(verifierConfigMap[key], configFilename)

	log.Printf("createVerifierConfig done for %s\n",
		safename)
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

func removeVerifierConfig(objType string, safename string) {

	key := objType + "x" + safename

	if _, ok := verifierConfigMap[key]; !ok {
		log.Printf("removeVerifierConfig for %s - not found\n", key)
		return
	}
	fmt.Printf("verifier config map delete for %s\n", key)
	delete(verifierConfigMap, key)

	configFilename := fmt.Sprintf("%s/%s/config/%s.json",
		verifierBaseDirname, objType, safename)

	if err := os.Remove(configFilename); err != nil {
		log.Println(err)
	}

	log.Printf("removeVerifierConfig done for %s\n", key)
}

func removeVerifierStatus(objType string, safename string) {

	key := objType + "x" + safename

	if _, ok := verifierStatusMap[key]; !ok {
		log.Printf("removeVerifierStatus for %s - not found\n",
			key)
		return
	}

	fmt.Printf("verifier status map delete for %s\n", key)
	delete(verifierStatusMap, key)

	log.Printf("removeVerifierStatus done for %s\n", key)
}

func lookupVerificationStatusSha256Internal(objType string, sha256 string) (*types.VerifyImageStatus, error) {

	for _, m := range verifierStatusMap {
		if m.ImageSha256 == sha256 {
			return &m, nil
		}
	}

	return nil, errors.New("No verificationStatusMap for sha")
}

func lookupVerificationStatus(objType string, safename string) (types.VerifyImageStatus, error) {

	key := objType + "x" + safename

	if m, ok := verifierStatusMap[key]; ok {

		log.Printf("lookupVerifyImageStatus: found based on safename %s\n",
			safename)
		return m, nil
	}
	return types.VerifyImageStatus{},
		errors.New("No verificationStatusMap for safename")
}

func lookupVerificationStatusSha256(objType string, sha256 string) (types.VerifyImageStatus, error) {

	m, err := lookupVerificationStatusSha256Internal(objType, sha256)
	if err != nil {
		return types.VerifyImageStatus{}, err
	} else {
		log.Printf("found status based on sha256 %s safename %s\n",
			sha256, m.Safename)
		return *m, nil
	}
}

func lookupVerificationStatusAny(objType string, safename string, sha256 string) (types.VerifyImageStatus, error) {

	m0, err := lookupVerificationStatus(objType, safename)
	if err == nil {
		return m0, nil
	}
	m1, err := lookupVerificationStatusSha256Internal(objType, sha256)
	if err == nil {
		log.Printf("lookupVerifyImageStatusAny: found based on sha %s\n",
			sha256)
		return *m1, nil
	}
	return types.VerifyImageStatus{},
		errors.New("No verification status for safename nor sha")
}

func checkStorageVerifierStatus(objType string, uuidStr string,
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

		vs, err := lookupVerificationStatusAny(objType, safename, sc.ImageSha256)

		if err != nil {
			log.Printf("lookupVerificationStatusAny %s sha %s failed %v\n",
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
			ss.ActiveFileLocation = objDownloadDirname + "/" + objType + "/" + vs.Safename

			log.Printf("Update SSL ActiveFileLocation for %s: %s\n",
				uuidStr, ss.ActiveFileLocation)
			changed = true
		}
	}

	if minState == types.MAXSTATE {
		// Odd; no StorageConfig in list
		minState = types.DELIVERED
	}
	return changed, minState, allErrors, errorTime
}

func writeVerifierConfig(config types.VerifyImageConfig, configFilename string) {

	bytes, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal VerifyImageConfig")
	}

	err = ioutil.WriteFile(configFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}
