// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// zedCloud interfacing for config, baseos/app-instance image
// Pull AppInstanceConfig from ZedCloud, make it available for zedmanager
// publish AppInstanceStatus to ZedCloud.

package main

import (
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

	baseDirname    = "/var/tmp/zedagent"
	runDirname     = "/var/run/zedagent"
	objDnldDirname = "/var/tmp/zedmanager/downloads"
	certsDirname   = "/var/tmp/zedmanager/certs"

	zedagentConfigDirname = baseDirname + "/config"
	zedagentStatusDirname = runDirname + "/status"

	zedmanagerConfigDirname = "/var/tmp/zedmanager/config"
	zedmanagerStatusDirname = "/var/run/zedmanager/status"

	downloaderConfigBaseDirname = "/var/tmp/downloader"
	downloaderStatusBaseDirname = "/var/run/downloader"

	verifierConfigBaseDirname = "/var/tmp/verifier"
	verifierStatusBaseDirname = "/var/run/verifier"

	certBaseDirname   = downloaderConfigBaseDirname + "/" + certObj
	certRunDirname    = downloaderStatusBaseDirname + "/" + certObj
	certConfigDirname = certBaseDirname + "/config"
	certStatusDirname = certRunDirname + "/status"

	zedagentBaseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	zedagentBaseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"

	downloaderBaseOsConfigDirname = downloaderConfigBaseDirname + "/" + baseOsObj + "/config"
	downloaderBaseOsStatusDirname = downloaderStatusBaseDirname + "/" + baseOsObj + "/status"

	verifierBaseOsConfigDirname = verifierConfigBaseDirname + "/" + baseOsObj + "/config"
	verifierBaseOsStatusDirname = verifierStatusBaseDirname + "/" + baseOsObj + "/status"

	certsCatalogDirname  = objDnldDirname + "/" + certObj
	certsPendingDirname  = certsCatalogDirname + "/pending"
	certsVerifierDirname = certsCatalogDirname + "/verifier"
	certsVerifiedDirname = certsCatalogDirname + "/verified"

	baseOsCatalogDirname  = objDnldDirname + "/" + baseOsObj
	baseOsPendingDirname  = baseOsCatalogDirname + "/pending"
	baseOsVerifierDirname = baseOsCatalogDirname + "/verifier"
	baseOsVerifiedDirname = baseOsCatalogDirname + "/verified"
)

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	log.Printf("Starting zedagent\n")
	watch.CleanupRestarted("zedagent")

	dirs := []string{

		baseDirname,
		runDirname,
		objDnldDirname,

		zedagentConfigDirname,
		zedagentStatusDirname,

		zedmanagerConfigDirname,
		zedmanagerStatusDirname,

		zedagentBaseOsConfigDirname,
		zedagentBaseOsStatusDirname,

		certConfigDirname,
		certStatusDirname,

		downloaderBaseOsConfigDirname,
		downloaderBaseOsStatusDirname,

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

	getCloudUrls()
	go metricsTimerTask()
	go configTimerTask()

	baseOsConfigStatusChanges := make(chan string)
	appInstanceStatusChanges := make(chan string)
	baseOsDownloaderChanges := make(chan string)
	baseOsVerifierChanges := make(chan string)

	// base os config/status event handler
	go watch.WatchConfigStatus(zedagentBaseOsConfigDirname,
		zedagentBaseOsStatusDirname, baseOsConfigStatusChanges)

	// app instance status event watcher
	go watch.WatchConfigStatus(zedmanagerConfigDirname,
		zedmanagerStatusDirname, appInstanceStatusChanges)

	// baseOs download watcher
	go watch.WatchStatus(downloaderBaseOsStatusDirname,
		baseOsDownloaderChanges)

	// baseOs verification watcher
	go watch.WatchStatus(verifierBaseOsStatusDirname,
		baseOsVerifierChanges)

	for {
		select {

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

		case change := <-baseOsDownloaderChanges:
			{
				go watch.HandleStatusEvent(change,
					downloaderBaseOsStatusDirname,
					&types.DownloaderStatus{},
					handleBaseOsConfigDownloadModify,
					handleBaseOsConfigDownloadDelete, nil)
				continue
			}

		case change := <-baseOsVerifierChanges:
			{
				go watch.HandleStatusEvent(change,
					verifierBaseOsStatusDirname,
					&types.VerifyImageStatus{},
					handleBaseOsConfigVerifierModify,
					handleBaseOsConfigVerifierDelete, nil)
				continue
			}
		}
	}
}

func handleBaseOsCreate(statusFilename string,
	configArg interface{}) {

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

func handleBaseOsConfigDownloadModify(statusFilename string,
	statusArg interface{}) {

	var status *types.DownloaderStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle DownloaderStatus")
	case *types.DownloaderStatus:
		status = statusArg.(*types.DownloaderStatus)
	}

	log.Printf("handleBaseOsDownloadModify for %s\n",
		status.Safename)
	updateDownloaderStatus(baseOsObj, status)
}

func handleBaseOsConfigDownloadDelete(statusFilename string) {

	log.Printf("handleBaseOsDownloadDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(baseOsObj, statusFilename)
}

func handleBaseOsConfigVerifierModify(statusFilename string,
	statusArg interface{}) {
	var status *types.VerifyImageStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageStatus")
	case *types.VerifyImageStatus:
		status = statusArg.(*types.VerifyImageStatus)
	}

	log.Printf("handleBaseOsVeriferModify for %s\n",
		status.Safename)
	updateVerifierStatus(baseOsObj, status)
}

func handleBaseOsConfigVerifierDelete(statusFilename string) {

	log.Printf("handleBaseOsVeriferDelete for %s\n",
		statusFilename)
	removeVerifierStatus(baseOsObj, statusFilename)
}
