// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// zedAgent interfaces with zedcloud for
// 1. config sync
// 2. status/metric/info pubish
// zeagent orchestrates base os installation
// app instance config is pushed to zedmanager for further
// orchestration
// event based app instance/device info published to ZedCloud

package main

import (
	"flag"
	"fmt"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"log"
	"os"
)

// Keeping status in /var/run to be clean after a crash/reboot
const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"

	zedagentModulename = "zedagent"
	baseDirname        = "/var/tmp/" + zedagentModulename
	runDirname         = "/var/run/" + zedagentModulename
	objDownloadDirname = "/var/tmp/zedmanager/downloads"
	certsDirname       = "/var/tmp/zedmanager/certs"

	downloaderBaseDirname = "/var/tmp/downloader"
	downloaderRunDirname  = "/var/run/downloader"

	verifierBaseDirname = "/var/tmp/verifier"
	verifierRunDirname  = "/var/run/verifier"

	zedagentConfigDirname = baseDirname + "/config"
	zedagentStatusDirname = runDirname + "/status"

	zedmanagerConfigDirname = "/var/tmp/zedmanager/config"
	zedmanagerStatusDirname = "/var/run/zedmanager/status"

	// base os config/status holder
	zedagentBaseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	zedagentBaseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"

	// certificate config/status holder
	zedagentCertObjConfigDirname = baseDirname + "/" + certObj + "/config"
	zedagentCertObjStatusDirname = runDirname + "/" + certObj + "/status"

	// base os download config/status holder
	downloaderBaseOsConfigDirname = downloaderBaseDirname + "/" + baseOsObj + "/config"
	downloaderBaseOsStatusDirname = downloaderRunDirname + "/" + baseOsObj + "/status"

	// certificate download config/status holder
	downloaderCertObjConfigDirname = downloaderBaseDirname + "/" + certObj + "/config"
	downloaderCertObjStatusDirname = downloaderRunDirname + "/" + certObj + "/status"

	// certificate verifier config/status holder
	verifierBaseOsConfigDirname = verifierBaseDirname + "/" + baseOsObj + "/config"
	verifierBaseOsStatusDirname = verifierRunDirname + "/" + baseOsObj + "/status"

	// base os object holder
	baseOsCatalogDirname  = objDownloadDirname + "/" + baseOsObj
	baseOsPendingDirname  = baseOsCatalogDirname + "/pending"
	baseOsVerifierDirname = baseOsCatalogDirname + "/verifier"
	baseOsVerifiedDirname = baseOsCatalogDirname + "/verified"

	// cerificate object holder
	certObjCatalogDirname  = objDownloadDirname + "/" + certObj
	certObjPendingDirname  = certObjCatalogDirname + "/pending"
	certObjVerifierDirname = certObjCatalogDirname + "/verifier"
	certObjVerifiedDirname = certObjCatalogDirname + "/verified"
)

// Set from Makefile
var Version = "No version specified"

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	versionPtr := flag.Bool("v", false, "Version")
	flag.Parse()
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	log.Printf("Starting zedagent\n")
	watch.CleanupRestarted("zedagent")

	dirs := []string{

		baseDirname,
		runDirname,
		objDownloadDirname,

		zedagentConfigDirname,
		zedagentStatusDirname,

		zedmanagerConfigDirname,
		zedmanagerStatusDirname,

		zedagentBaseOsConfigDirname,
		zedagentBaseOsStatusDirname,

		zedagentCertObjConfigDirname,
		zedagentCertObjStatusDirname,

		downloaderBaseOsConfigDirname,
		downloaderBaseOsStatusDirname,

		downloaderCertObjConfigDirname,
		downloaderCertObjStatusDirname,

		verifierBaseOsConfigDirname,
		verifierBaseOsStatusDirname,
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			if err := os.MkdirAll(dir, 0700); err != nil {
				log.Fatal(err)
			}
		}
	}

	// Tell ourselves to go ahead
	watch.SignalRestart("zedagent")

	initMaps()

	getCloudUrls()
	go metricsTimerTask()
	go configTimerTask()

	appInstanceStatusChanges := make(chan string)
	baseOsConfigStatusChanges := make(chan string)
	baseOsDownloaderChanges := make(chan string)
	baseOsVerifierChanges := make(chan string)
	certObjConfigStatusChanges := make(chan string)
	certObjDownloaderChanges := make(chan string)

	// app instance status event watcher
	go watch.WatchConfigStatus(zedmanagerConfigDirname,
		zedmanagerStatusDirname, appInstanceStatusChanges)

	// base os config/status event handler
	go watch.WatchConfigStatus(zedagentBaseOsConfigDirname,
		zedagentBaseOsStatusDirname, baseOsConfigStatusChanges)

	// cert object config/status event handler
	go watch.WatchConfigStatus(zedagentCertObjConfigDirname,
		zedagentCertObjStatusDirname, certObjConfigStatusChanges)

	// baseOs download watcher
	go watch.WatchStatus(downloaderBaseOsStatusDirname,
		baseOsDownloaderChanges)

	// baseOs verification watcher
	go watch.WatchStatus(verifierBaseOsStatusDirname,
		baseOsVerifierChanges)

	// cert download watcher
	go watch.WatchStatus(downloaderCertObjStatusDirname,
		certObjDownloaderChanges)

	for {
		select {

		case change := <-appInstanceStatusChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					zedmanagerConfigDirname,
					zedmanagerStatusDirname,
					&types.AppInstanceConfig{},
					&types.AppInstanceStatus{},
					handleAppInstanceStatusCreate,
					handleAppInstanceStatusModify,
					handleAppInstanceStatusDelete, nil)
				continue
			}

		case change := <-baseOsConfigStatusChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					zedagentBaseOsConfigDirname,
					zedagentBaseOsStatusDirname,
					&types.BaseOsConfig{},
					&types.BaseOsStatus{},
					handleBaseOsCreate,
					handleBaseOsModify,
					handleBaseOsDelete, nil)
				continue
			}

		case change := <-certObjConfigStatusChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					zedagentBaseOsConfigDirname,
					zedagentBaseOsStatusDirname,
					&types.CertObjConfig{},
					&types.CertObjStatus{},
					handleCertObjCreate,
					handleCertObjModify,
					handleCertObjDelete, nil)
				continue
			}

		case change := <-baseOsDownloaderChanges:
			{
				go watch.HandleStatusEvent(change,
					downloaderBaseOsStatusDirname,
					&types.DownloaderStatus{},
					handleBaseOsDownloadStatusModify,
					handleBaseOsDownloadStatusDelete, nil)
				continue
			}

		case change := <-baseOsVerifierChanges:
			{
				go watch.HandleStatusEvent(change,
					verifierBaseOsStatusDirname,
					&types.VerifyImageStatus{},
					handleBaseOsVerifierStatusModify,
					handleBaseOsVerifierStatusDelete, nil)
				continue
			}

		case change := <-certObjDownloaderChanges:
			{
				go watch.HandleStatusEvent(change,
					downloaderCertObjStatusDirname,
					&types.DownloaderStatus{},
					handleCertObjDownloadStatusModify,
					handleCertObjDownloadStatusDelete, nil)
				continue
			}
		}
	}
}

func handleAppInstanceStatusCreate(statusFilename string,
	configArg interface{}) {

	var config *types.AppInstanceConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceConfig")
	case *types.AppInstanceConfig:
		config = configArg.(*types.AppInstanceConfig)
	}
	log.Printf("handleCreate for %s\n", config.DisplayName)
}

func handleAppInstanceStatusModify(statusFilename string,
	configArg interface{}, statusArg interface{}) {

	var status *types.AppInstanceStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceStatus")
	case *types.AppInstanceStatus:
		status = statusArg.(*types.AppInstanceStatus)
	}

	PublishAppInfoToZedCloud(status)
}

func handleAppInstanceStatusDelete(statusFilename string,
	statusArg interface{}) {

	var status *types.AppInstanceStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceStatus")
	case *types.AppInstanceStatus:
		status = statusArg.(*types.AppInstanceStatus)
	}

	PublishAppInfoToZedCloud(status)
}

func handleBaseOsCreate(statusFilename string, configArg interface{}) {

	var config *types.BaseOsConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsConfig")
	case *types.BaseOsConfig:
		config = configArg.(*types.BaseOsConfig)
	}

	log.Printf("handleBaseOsCreate for %s\n", config.BaseOsVersion)
	addOrUpdateBaseOsConfig(config.UUIDandVersion.UUID.String(), *config)
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

func handleBaseOsModify(statusFilename string,
	configArg interface{}, statusArg interface{}) {

	var config *types.BaseOsConfig
	var status *types.BaseOsStatus

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsConfig")
	case *types.BaseOsConfig:
		config = configArg.(*types.BaseOsConfig)
		log.Printf("handleBaseOsModify for %s\n", config.BaseOsVersion)
	}

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsStatus")
	case *types.BaseOsStatus:
		status = statusArg.(*types.BaseOsStatus)
		log.Printf("handleBaseOsModify for %s\n", status.BaseOsVersion)
	}

	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		log.Printf("Same version %s for %s\n",
			config.UUIDandVersion.Version, statusFilename)
		return
	}

	status.UUIDandVersion = config.UUIDandVersion

	writeBaseOsStatus(status, statusFilename)

	addOrUpdateBaseOsConfig(config.UUIDandVersion.UUID.String(), *config)
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

func handleBaseOsDelete(statusFilename string,
	statusArg interface{}) {

	var status *types.BaseOsStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsStatus")
	case *types.BaseOsStatus:
		status = statusArg.(*types.BaseOsStatus)
	}

	log.Printf("handleBaseOsDelete for %s\n", status.DisplayName)

	removeBaseOsConfig(status.UUIDandVersion.UUID.String())
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

func handleCertObjCreate(statusFilename string, configArg interface{}) {

	var config *types.CertObjConfig
	var uuidStr string = config.UUIDandVersion.UUID.String()

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle CertObjConfig")
	case *types.CertObjConfig:
		config = configArg.(*types.CertObjConfig)
	}

	log.Printf("handleCertObjCreate for %s\n", uuidStr)
	addOrUpdateCertObjConfig(uuidStr, *config)
}

func handleCertObjModify(statusFilename string,
	configArg interface{}, statusArg interface{}) {

	var config *types.CertObjConfig
	var status *types.CertObjStatus

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle CertObjConfig")
	case *types.CertObjConfig:
		config = configArg.(*types.CertObjConfig)
	}

	var uuidStr string = config.UUIDandVersion.UUID.String()
	log.Printf("handleCertObjModify for %s\n", uuidStr)

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle CertObjStatus")
	case *types.CertObjStatus:
		status = statusArg.(*types.CertObjStatus)
		log.Printf("handleCertObjModify for %s\n", uuidStr)
	}

	// XXX:FIXME, do we
	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		log.Printf("Same version %s for %s\n",
			config.UUIDandVersion.Version, statusFilename)
		return
	}

	status.UUIDandVersion = config.UUIDandVersion

	writeCertObjStatus(status, statusFilename)
	addOrUpdateCertObjConfig(uuidStr, *config)
}

func handleCertObjDelete(statusFilename string, statusArg interface{}) {

	var status *types.CertObjStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle CertObjStatus")
	case *types.CertObjStatus:
		status = statusArg.(*types.CertObjStatus)
	}
	var uuidStr string = status.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjDelete for %s\n", uuidStr)

	removeCertObjConfig(uuidStr)
}

func handleBaseOsDownloadStatusModify(statusFilename string,
	statusArg interface{}) {

	var status *types.DownloaderStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle DownloaderStatus")
	case *types.DownloaderStatus:
		status = statusArg.(*types.DownloaderStatus)
	}

	log.Printf("handleBaseOsDownloadStatusModify for %s\n",
		status.Safename)
	updateDownloaderStatus(baseOsObj, status)
}

func handleBaseOsDownloadStatusDelete(statusFilename string) {

	log.Printf("handleBaseOsDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(baseOsObj, statusFilename)
}

func handleBaseOsVerifierStatusModify(statusFilename string,
	statusArg interface{}) {
	var status *types.VerifyImageStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageStatus")
	case *types.VerifyImageStatus:
		status = statusArg.(*types.VerifyImageStatus)
	}

	log.Printf("handleBaseOsVeriferStatusModify for %s\n",
		status.Safename)
	updateVerifierStatus(baseOsObj, status)
}

func handleBaseOsVerifierStatusDelete(statusFilename string) {

	log.Printf("handleBaseOsVeriferStatusDelete for %s\n",
		statusFilename)
	removeVerifierStatus(baseOsObj, statusFilename)
}

func handleCertObjDownloadStatusModify(statusFilename string,
	statusArg interface{}) {

	var status *types.DownloaderStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle DownloaderStatus")
	case *types.DownloaderStatus:
		status = statusArg.(*types.DownloaderStatus)
	}

	log.Printf("handleCertObjDownloadStatusModify for %s\n",
		status.Safename)
	updateDownloaderStatus(certObj, status)
}

func handleCertObjDownloadStatusDelete(statusFilename string) {

	log.Printf("handleCertObjDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(certObj, statusFilename)
}
