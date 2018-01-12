// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// zedAgent interfaces with zedcloud for
//   * config sync
//   * metric/info pubish
// app instance config is pushed to zedmanager for orchestration
// event based app instance/device info published to ZedCloud
// periodic status/metric published to zedCloud
// zeagent orchestrates base os/certs installation

// zedagent handles the following orchestration
//   * base os config/status          <zedagent>   / <baseos> / <config | status>
//   * certs config/status            <zedagent>   / certs>   / <config | status>
//   * base os download config/status <downloader> / <baseos> / <config | status>
//   * certs download config/status   <downloader> / <certs>  / <config | status>
//   * base os verifier config/status <verifier>   / <baseos> / <config | status>
// <base os>
//   <zedagent>   <baseos> <config> --> <zedagent>    <baseos> <status>
//				<download>...       --> <downloader>  <baseos> <config>
//   <downloader> <baseos> <config> --> <downloader>  <baseos> <status>
//				<downloaded>...     --> <downloader>  <baseos> <status>
//								    --> <zedagent>    <baseos> <status>
//								    --> <verifier>    <baseos> <config>
//				<verified>  ...     --> <verifier>    <baseos> <status>
//								    --> <zedagent>    <baseos> <status>
// <certs>
//   <zedagent>   <certs> <config> --> <zedagent>    <certs> <status>
//				<download>...      --> <downloader>  <certs> <config>
//   <downloader> <certs> <config> --> <downloader>  <certs> <status>
//				<downloaded>...    --> <downloader>  <certs> <status>
//								   --> <zedagent>    <certs> <status>
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
	globalObj = ""
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"

	downloaderModulename = "downloader"
	verifierModulename = "verifier"
	zedagentModulename = "zedagent"
	zedmanagerModulename = "zedmanager"

	zedBaseDirname     = "/var/tmp"
	zedRunDirname      = "/var/run"
	baseDirname        = "/var/tmp/zedagent"
	runDirname         = "/var/run/zedagent"
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
	downloaderBaseOsStatusDirname = downloaderRunDirname + "/" + baseOsObj + "/status"
	downloaderCertObjStatusDirname = downloaderRunDirname + "/" + certObj + "/status"

	verifierBaseOsStatusDirname = verifierRunDirname + "/" + baseOsObj + "/status"
	certObjPendingDirname = objDownloadDirname + "/" + certObj + "/pending"
)

var verifierRestarted = false

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
	watch.SignalRestart("zedagent")

	// Tell ourselves to go ahead
	// initialize the module specifig stuff
	handleInit()

	var restartFn watch.ConfigRestartHandler = handleRestart

	fileChanges := make(chan string)
	appInstanceStatusChanges := make(chan string)
	baseOsConfigStatusChanges := make(chan string)
	baseOsDownloaderChanges := make(chan string)
	baseOsVerifierChanges := make(chan string)
	certObjConfigStatusChanges := make(chan string)
	certObjDownloaderChanges := make(chan string)

/*
	var verifierRestartedFn watch.StatusRestartHandler = handleVerifierRestarted
	// First we process the verifierStatus to avoid downloading
	// an base image we already have in place
	log.Printf("Handling initial verifier Status\n")
	done := false
	for !done {
		select {
		case change := <-baseOsVerifierChanges:
			{
				watch.HandleStatusEvent(change,
					verifierBaseOsStatusDirname,
					&types.VerifyImageStatus{},
					handleBaseOsVerifierStatusModify,
					handleBaseOsVerifierStatusDelete,
					&verifierRestartedFn)
				if verifierRestarted {
					log.Printf("Verifier reported restarted\n")
					done = true
					break
				}
			}
		}
	}
*/

	// start the metrics/config fetch tasks
	go metricsTimerTask()
	go configTimerTask()
	// app instance status event watcher
	go watch.WatchConfigStatus(zedmanagerConfigDirname,
		zedmanagerStatusDirname, appInstanceStatusChanges)

	// base os config/status event handler
	go watch.WatchConfigStatus(zedagentBaseOsConfigDirname,
		zedagentBaseOsStatusDirname, baseOsConfigStatusChanges)

	// cert object config/status event handler
	go watch.WatchConfigStatus(zedagentCertObjConfigDirname,
		zedagentCertObjStatusDirname, certObjConfigStatusChanges)

	// baseOs download status watcher
	go watch.WatchStatus(downloaderBaseOsStatusDirname,
		baseOsDownloaderChanges)

	// baseOs verification status watcher
	go watch.WatchStatus(verifierBaseOsStatusDirname,
		baseOsVerifierChanges)

	// certificate download status watcher
	go watch.WatchStatus(downloaderCertObjStatusDirname,
		certObjDownloaderChanges)

	// for restart flag handling
	go watch.WatchConfigStatus(zedagentConfigDirname,
		 zedagentStatusDirname, fileChanges)

	for {
		select {

		case change := <-fileChanges:
			{
				// restart only, handling place holder
				watch.HandleConfigStatusEvent(change,
					zedagentConfigDirname, zedagentStatusDirname,
					&types.AppInstanceConfig{},
					&types.AppInstanceStatus{},
					handleAppInstanceStatusCreate,
					handleAppInstanceStatusModify,
					handleAppInstanceStatusDelete, &restartFn)
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
					zedagentCertObjConfigDirname,
					zedagentCertObjStatusDirname,
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

// signal zedmanager, to restart
// it would take care of orchestrating
// all other module restart
func handleRestart(done bool) {
	log.Printf("handleRestart(%v)\n", done)
	if done {
		watch.SignalRestart("zedmanager")
	}
}

func handleVerifierRestarted(done bool) {
	log.Printf("handleVerifierRestarted(%v)\n", done)
	if done {
		verifierRestarted = true
	}
}

func handleInit() {

	initializeDirs()
	initMaps()
	getCloudUrls()
}

func initializeDirs() {

	zedagentObjTypes := []string{baseOsObj, certObj}
	zedmanagerObjTypes := []string{appImgObj}

	// create the module object based config/status dirs
	watch.CreateConfigStatusDirs(downloaderModulename, zedagentObjTypes)
	watch.CreateConfigStatusDirs(zedagentModulename, zedagentObjTypes)
	watch.CreateConfigStatusDirs(zedmanagerModulename, zedmanagerObjTypes)
	watch.CreateConfigStatusDirs(verifierModulename, zedagentObjTypes)
}

// app instance event watch to capture tranisions
// and publish to zedCloud

func handleAppInstanceStatusCreate(statusFilename string,
	configArg interface{}) {

	var config *types.AppInstanceConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceConfig")
	case *types.AppInstanceConfig:
		config = configArg.(*types.AppInstanceConfig)
	}
	log.Printf("handleAppInstanceStatusCreate for %s\n", config.DisplayName)
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

// base os config/status event handlers
// base os config create event
func handleBaseOsCreate(statusFilename string, configArg interface{}) {

	var config *types.BaseOsConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsConfig")
	case *types.BaseOsConfig:
		config = configArg.(*types.BaseOsConfig)
	}
	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleBaseOsCreate for %s\n", uuidStr)
	addOrUpdateBaseOsConfig(uuidStr, *config)
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

// base os config modify event
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

	uuidStr := config.UUIDandVersion.UUID.String()

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsStatus")
	case *types.BaseOsStatus:
		status = statusArg.(*types.BaseOsStatus)
		log.Printf("handleBaseOsModify for %s\n", status.BaseOsVersion)
	}

	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		log.Printf("Same version %s for %s\n",
			config.UUIDandVersion.Version, uuidStr)
		return
	}

	// update the version field, uuis being the same
	status.UUIDandVersion = config.UUIDandVersion
	writeBaseOsStatus(status, statusFilename)

	addOrUpdateBaseOsConfig(uuidStr, *config)
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

// base os config delete event
func handleBaseOsDelete(statusFilename string,
	statusArg interface{}) {

	var status *types.BaseOsStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle BaseOsStatus")
	case *types.BaseOsStatus:
		status = statusArg.(*types.BaseOsStatus)
	}

	log.Printf("handleBaseOsDelete for %s\n", status.BaseOsVersion)

	removeBaseOsConfig(status.UUIDandVersion.UUID.String())
	PublishDeviceInfoToZedCloud(baseOsStatusMap)
}

// certificate config/status event handlers
// certificate config create event
func handleCertObjCreate(statusFilename string, configArg interface{}) {

	var config *types.CertObjConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle CertObjConfig")
	case *types.CertObjConfig:
		config = configArg.(*types.CertObjConfig)
	}

	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjCreate for %s\n", uuidStr)
	addOrUpdateCertObjConfig(uuidStr, *config)
}

// certificate config modify event
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

	uuidStr := config.UUIDandVersion.UUID.String()

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle CertObjStatus")
	case *types.CertObjStatus:
		status = statusArg.(*types.CertObjStatus)
	}

	log.Printf("handleCertObjModify for %s\n", uuidStr)

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

// certificate config delete event
func handleCertObjDelete(statusFilename string, statusArg interface{}) {

	var status *types.CertObjStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle CertObjStatus")
	case *types.CertObjStatus:
		status = statusArg.(*types.CertObjStatus)
	}
	uuidStr := status.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjDelete for %s\n", uuidStr)

	removeCertObjConfig(uuidStr)
}

// base os download status change event
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

// base os download status delete event
func handleBaseOsDownloadStatusDelete(statusFilename string) {

	log.Printf("handleBaseOsDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(baseOsObj, statusFilename)
}

// base os verification status change event
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

// base os verification status delete event
func handleBaseOsVerifierStatusDelete(statusFilename string) {

	log.Printf("handleBaseOsVeriferStatusDelete for %s\n",
		statusFilename)
	removeVerifierStatus(baseOsObj, statusFilename)
}

// cerificate download status change event
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

// cerificate download status delete event
func handleCertObjDownloadStatusDelete(statusFilename string) {

	log.Printf("handleCertObjDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(certObj, statusFilename)
}
