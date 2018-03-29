// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

// baseOsMgr orchestrates base os/certs installation

// basOsMgr handles the following orchestration
//   * base os config/status          <baseOsMgr>  / <baseos> / <config | status>
//   * certs config/status            <baseOsMgr>  / certs>   / <config | status>
//   * base os download config/status <downloader> / <baseos> / <config | status>
//   * certs download config/status   <downloader> / <certs>  / <config | status>
//   * base os verifier config/status <verifier>   / <baseos> / <config | status>
//
// <base os>
//   <baseOsMgr>  <baseos> <config> --> <baseOsMgr>   <baseos> <status>
//				<download>...       --> <downloader>  <baseos> <config>
//   <downloader> <baseos> <config> --> <downloader>  <baseos> <status>
//				<downloaded>...     --> <downloader>  <baseos> <status>
//								    --> <baseOsMgr>   <baseos> <status>
//								    --> <verifier>    <baseos> <config>
// <verifier>	<verified>  ...     --> <verifier>    <baseos> <status>
//								    --> <baseosmgr>   <baseos> <status>
// <certs>
//   <baseOsMgr>  <certs> <config> --> <baseosmgr>   <certs> <status>
//				<download>...      --> <downloader>  <certs> <config>
//   <downloader> <certs> <config> --> <downloader>  <certs> <status>
//				<downloaded>...    --> <downloader>  <certs> <status>
//								   --> <baseosmgr>   <certs> <status>

package main

import (
	"flag"
	"fmt"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/pidfile"
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
	agentName = "baseosmgr"

	downloaderModulename = "downloader"
	verifierModulename   = "verifier"
	baseOsMgrModulename  = agentName

	zedBaseDirname = "/var/tmp"
	zedRunDirname  = "/var/run"
	baseDirname    = zedBaseDirname + "/" + agentName
	runDirname     = zedRunDirname + "/" + agentName

	configDir             = "/config"
	persistDir            = "/persist"
	objectDownloadDirname = persistDir + "/downloads"
	certificateDirname    = persistDir + "/certs"

	downloaderBaseDirname = zedBaseDirname + "/" + downloaderModulename
	downloaderRunDirname  = zedRunDirname + "/" + downloaderModulename

	verifierBaseDirname = zedBaseDirname + "/" + verifierModulename
	verifierRunDirname  = zedRunDirname + "/" + verifierModulename

	baseOsMgrConfigDirname = baseDirname + "/config"
	baseOsMgrStatusDirname = runDirname + "/status"

	// base os config/status holder
	baseOsMgrBaseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	baseOsMgrBaseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"

	// certificate config/status holder
	baseOsMgrCertConfigDirname = baseDirname + "/" + certObj + "/config"
	baseOsMgrCertStatusDirname = runDirname + "/" + certObj + "/status"

	// base os download config/status holder
	downloaderBaseOsStatusDirname  = downloaderRunDirname + "/" + baseOsObj + "/status"
	downloaderCertObjStatusDirname = downloaderRunDirname + "/" + certObj + "/status"

	// base os verifier status holder
	verifierBaseOsConfigDirname = verifierBaseDirname + "/" + baseOsObj + "/config"
	verifierBaseOsStatusDirname = verifierRunDirname + "/" + baseOsObj + "/status"
)

// Set from Makefile
var Version = "No version specified"

var deviceNetworkStatus types.DeviceNetworkStatus

// Dummy used when we don't have anything to pass
type dummyContext struct {
}

// Information from handleVerifierRestarted
type verifierContext struct {
	verifierRestarted bool
}

var debug = false

func main() {
	logf, err := agentlog.Init(agentName)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	flag.Parse()
	debug = *debugPtr
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
		log.Fatal(err)
	}

	log.Printf("Starting %s\n", agentName)
	watch.CleanupRestarted(agentName)

	// Tell ourselves to go ahead
	// initialize the module specifig stuff
	handleInit()

	watch.SignalRestart(agentName)
	var restartFn watch.StatusRestartHandler = handleRestart

	restartChanges := make(chan string)
	baseOsConfigStatusChanges := make(chan string)
	baseOsDownloaderChanges := make(chan string)
	baseOsVerifierChanges := make(chan string)
	certObjConfigStatusChanges := make(chan string)
	certObjDownloaderChanges := make(chan string)

	var verifierRestartedFn watch.StatusRestartHandler = handleVerifierRestarted

	// baseOs verification status watcher
	go watch.WatchStatus(verifierBaseOsStatusDirname,
		baseOsVerifierChanges)

	verifierCtx := verifierContext{}

	// First we process the verifierStatus to avoid downloading
	// an base image we already have in place
	log.Printf("Handling initial verifier Status\n")
	for !verifierCtx.verifierRestarted {
		select {
		case change := <-baseOsVerifierChanges:
			watch.HandleStatusEvent(change, &verifierCtx,
				verifierBaseOsStatusDirname,
				&types.VerifyImageStatus{},
				handleBaseOsVerifierStatusModify,
				handleBaseOsVerifierStatusDelete,
				&verifierRestartedFn)
			if verifierCtx.verifierRestarted {
				log.Printf("Verifier reported restarted\n")
				break
			}
		}
	}

	// base os config/status event handler
	go watch.WatchConfigStatus(baseOsMgrBaseOsConfigDirname,
		baseOsMgrBaseOsStatusDirname, baseOsConfigStatusChanges)

	// cert object config/status event handler
	go watch.WatchConfigStatus(baseOsMgrCertConfigDirname,
		baseOsMgrCertStatusDirname, certObjConfigStatusChanges)

	// baseOs download status watcher
	go watch.WatchStatus(downloaderBaseOsStatusDirname,
		baseOsDownloaderChanges)

	// certificate download status watcher
	go watch.WatchStatus(downloaderCertObjStatusDirname,
		certObjDownloaderChanges)

	// for restart flag handling
	go watch.WatchStatus(baseOsMgrStatusDirname, restartChanges)

	for {
		select {

		case change := <-restartChanges:
			// restart only, place holder
			watch.HandleStatusEvent(change, dummyContext{},
				baseOsMgrStatusDirname,
				&types.AppInstanceStatus{},
				handleRestartStatusModify,
				handleRestartStatusDelete, &restartFn)

		case change := <-certObjConfigStatusChanges:
			watch.HandleConfigStatusEvent(change, dummyContext{},
				baseOsMgrCertConfigDirname,
				baseOsMgrCertStatusDirname,
				&types.CertObjConfig{},
				&types.CertObjStatus{},
				handleCertObjCreate,
				handleCertObjModify,
				handleCertObjDelete, nil)

		case change := <-baseOsConfigStatusChanges:
			watch.HandleConfigStatusEvent(change, dummyContext{},
				baseOsMgrBaseOsConfigDirname,
				baseOsMgrBaseOsStatusDirname,
				&types.BaseOsConfig{},
				&types.BaseOsStatus{},
				handleBaseOsCreate,
				handleBaseOsModify,
				handleBaseOsDelete, nil)

		case change := <-baseOsDownloaderChanges:
			watch.HandleStatusEvent(change, dummyContext{},
				downloaderBaseOsStatusDirname,
				&types.DownloaderStatus{},
				handleBaseOsDownloadStatusModify,
				handleBaseOsDownloadStatusDelete, nil)

		case change := <-baseOsVerifierChanges:
			watch.HandleStatusEvent(change, dummyContext{},
				verifierBaseOsStatusDirname,
				&types.VerifyImageStatus{},
				handleBaseOsVerifierStatusModify,
				handleBaseOsVerifierStatusDelete, nil)

		case change := <-certObjDownloaderChanges:
			watch.HandleStatusEvent(change, dummyContext{},
				downloaderCertObjStatusDirname,
				&types.DownloaderStatus{},
				handleCertObjDownloadStatusModify,
				handleCertObjDownloadStatusDelete, nil)
		}
	}
}

// signal zedmanager, to restart
// it would take care of orchestrating
// all other module restart
func handleRestart(ctxArg interface{}, done bool) {
	log.Printf("handleRestart(%v)\n", done)
	if done {
		watch.SignalRestart("zedmanager")
	}
}

func handleVerifierRestarted(ctxArg interface{}, done bool) {
	ctx := ctxArg.(*verifierContext)
	log.Printf("handleVerifierRestarted(%v)\n", done)
	if done {
		ctx.verifierRestarted = true
	}
}

func handleInit() {
	initializeDirs()
	initMaps()
}

func initializeDirs() {

	baseOsMgrObjTypes := []string{baseOsObj, certObj}
	baseOsMgrVerifierObjTypes := []string{baseOsObj}

	// create the module object based config/status dirs
	createConfigStatusDirs(downloaderModulename, baseOsMgrObjTypes)
	createConfigStatusDirs(baseOsMgrModulename, baseOsMgrObjTypes)
	createConfigStatusDirs(verifierModulename, baseOsMgrVerifierObjTypes)

	// create persistent holder directory
	if _, err := os.Stat(persistDir); err != nil {
		if err := os.MkdirAll(persistDir, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(certificateDirname); err != nil {
		if err := os.MkdirAll(certificateDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(objectDownloadDirname); err != nil {
		if err := os.MkdirAll(objectDownloadDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
}

// create module and object based config/status directories
func createConfigStatusDirs(moduleName string, objTypes []string) {

	jobDirs := []string{"config", "status"}
	zedBaseDirs := []string{zedBaseDirname, zedRunDirname}
	baseDirs := make([]string, len(zedBaseDirs))

	log.Printf("Creating config/status dirs for %s\n", moduleName)

	for idx, dir := range zedBaseDirs {
		baseDirs[idx] = dir + "/" + moduleName
	}

	for idx, baseDir := range baseDirs {

		dirName := baseDir + "/" + jobDirs[idx]
		if _, err := os.Stat(dirName); err != nil {
			log.Printf("Create %s\n", dirName)
			if err := os.MkdirAll(dirName, 0700); err != nil {
				log.Fatal(err)
			}
		}

		// Creating Object based holder dirs
		for _, objType := range objTypes {
			dirName := baseDir + "/" + objType + "/" + jobDirs[idx]
			if _, err := os.Stat(dirName); err != nil {
				log.Printf("Create %s\n", dirName)
				if err := os.MkdirAll(dirName, 0700); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

// watch for restart only
func handleRestartStatusModify(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
}

func handleRestartStatusDelete(ctxArg interface{}, statusFilename string) {
}

// base os config/status event handlers
// base os config create event
func handleBaseOsCreate(ctxArg interface{}, statusFilename string,
	configArg interface{}) {
	config := configArg.(*types.BaseOsConfig)
	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleBaseOsCreate for %s\n", uuidStr)
	addOrUpdateBaseOsConfig(uuidStr, *config)
}

// base os config modify event
func handleBaseOsModify(ctxArg interface{}, statusFilename string,
	configArg interface{}, statusArg interface{}) {

	config := configArg.(*types.BaseOsConfig)
	status := statusArg.(*types.BaseOsStatus)
	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleBaseOsModify for %s\n", status.BaseOsVersion)
	if config.UUIDandVersion.Version == status.UUIDandVersion.Version &&
		config.Activate == status.Activated {
		log.Printf("Same version %v for %s\n",
			config.UUIDandVersion.Version, uuidStr)
		return
	}

	// update the version field, uuid being the same
	status.UUIDandVersion = config.UUIDandVersion
	writeBaseOsStatus(status, uuidStr)

	addOrUpdateBaseOsConfig(uuidStr, *config)
}

// base os config delete event
func handleBaseOsDelete(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.BaseOsStatus)

	log.Printf("handleBaseOsDelete for %s\n", status.BaseOsVersion)
	removeBaseOsConfig(status.UUIDandVersion.UUID.String())
}

// certificate config/status event handlers
// certificate config create event
func handleCertObjCreate(ctxArg interface{}, statusFilename string,
	configArg interface{}) {
	config := configArg.(*types.CertObjConfig)
	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjCreate for %s\n", uuidStr)
	addOrUpdateCertObjConfig(uuidStr, *config)
}

// certificate config modify event
func handleCertObjModify(ctxArg interface{}, statusFilename string,
	configArg interface{}, statusArg interface{}) {

	config := configArg.(*types.CertObjConfig)
	status := statusArg.(*types.CertObjStatus)
	uuidStr := config.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjModify for %s\n", uuidStr)

	// XXX:FIXME, do we
	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		log.Printf("Same version %v for %s\n",
			config.UUIDandVersion.Version, statusFilename)
		return
	}

	status.UUIDandVersion = config.UUIDandVersion
	writeCertObjStatus(status, uuidStr)
	addOrUpdateCertObjConfig(uuidStr, *config)
}

// certificate config delete event
func handleCertObjDelete(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.CertObjStatus)
	uuidStr := status.UUIDandVersion.UUID.String()

	log.Printf("handleCertObjDelete for %s\n", uuidStr)

	removeCertObjConfig(uuidStr)
}

// base os download status change event
func handleBaseOsDownloadStatusModify(ctxArg interface{},
	statusFilename string, statusArg interface{}) {
	status := statusArg.(*types.DownloaderStatus)

	log.Printf("handleBaseOsDownloadStatusModify for %s\n",
		status.Safename)
	updateDownloaderStatus(baseOsObj, status)
}

// base os download status delete event
func handleBaseOsDownloadStatusDelete(ctxArg interface{},
	statusFilename string) {

	log.Printf("handleBaseOsDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(baseOsObj, statusFilename)
}

// base os verification status change event
func handleBaseOsVerifierStatusModify(ctxArg interface{},
	statusFilename string, statusArg interface{}) {
	status := statusArg.(*types.VerifyImageStatus)

	log.Printf("handleBaseOsVeriferStatusModify for %s\n",
		status.Safename)
	updateVerifierStatus(baseOsObj, status)
}

// base os verification status delete event
func handleBaseOsVerifierStatusDelete(ctxArg interface{},
	statusFilename string) {

	log.Printf("handleBaseOsVeriferStatusDelete for %s\n",
		statusFilename)
	removeVerifierStatus(baseOsObj, statusFilename)
}

// cerificate download status change event
func handleCertObjDownloadStatusModify(ctxArg interface{},
	statusFilename string, statusArg interface{}) {
	status := statusArg.(*types.DownloaderStatus)

	log.Printf("handleCertObjDownloadStatusModify for %s\n",
		status.Safename)
	updateDownloaderStatus(certObj, status)
}

// cerificate download status delete event
func handleCertObjDownloadStatusDelete(ctxArg interface{},
	statusFilename string) {

	log.Printf("handleCertObjDownloadStatusDelete for %s\n",
		statusFilename)
	removeDownloaderStatus(certObj, statusFilename)
}
