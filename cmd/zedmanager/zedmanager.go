// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Get AppInstanceConfig from zedagent, drive config to Downloader, Verifier,
// IdentityMgr, and Zedrouter. Collect status from those services and make
// the combined AppInstanceStatus available to zedagent.
//
// This reads AppInstanceConfig from /var/tmp/zedmanager/config/*.json and
// produces AppInstanceStatus in /var/run/zedmanager/status/*.json.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"io/ioutil"
	"log"
	"os"
)

// Keeping status in /var/run to be clean after a crash/reboot
const (
	appImgObj             = "appImg.obj"
	domainMgrModulename   = "domainmgr"
	downloaderModulename  = "downloader"
	identityMgrModulename = "identitymgr"
	zedmanagerModulename  = "zedmanager"
	zedrouterModulename   = "zedrouter"
	verifierModulename    = "verifier"

	moduleName     = "zedmanager"
	zedBaseDirname = "/var/tmp"
	zedRunDirname  = "/var/run"
	baseDirname    = zedBaseDirname + "/" + moduleName
	runDirname     = zedRunDirname + "/" + moduleName

	zedmanagerConfigDirname = baseDirname + "/config"
	zedmanagerStatusDirname = runDirname + "/status"

	domainmgrConfigDirname   = zedBaseDirname + "/" + domainMgrModulename + "/config"
	zedrouterConfigDirname   = zedBaseDirname + "/" + zedrouterModulename + "/config"
	identitymgrConfigDirname = zedBaseDirname + "/" + identityMgrModulename + "/config"
	verifierConfigDirname    = zedBaseDirname + "/" + verifierModulename + "/" + appImgObj + "/config"
	downloaderConfigDirname  = zedBaseDirname + "/" + downloaderModulename + "/" + appImgObj + "/config"
	DNSDirname		 = "/var/run/zedrouter/DeviceNetworkStatus"
)

// Set from Makefile
var Version = "No version specified"

var globalStatus types.DeviceNetworkStatus

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	versionPtr := flag.Bool("v", false, "Version")
	flag.Parse()
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	log.Printf("Starting zedmanager\n")
	watch.CleanupRestarted("zedmanager")
	watch.CleanupRestart("downloader")
	watch.CleanupRestart("verifier")
	watch.CleanupRestart("identitymgr")
	watch.CleanupRestart("zedrouter")
	watch.CleanupRestart("domainmgr")

	// status dirs
	verifierStatusDirname := zedRunDirname + "/" + verifierModulename + "/status"
	domainmgrStatusDirname := zedRunDirname + "/" + domainMgrModulename + "/status"
	zedrouterStatusDirname := zedRunDirname + "/" + zedrouterModulename + "/status"
	identitymgrStatusDirname := zedRunDirname + "/" + identityMgrModulename + "/status"
	verifierAppImgObjStatusDirname := zedRunDirname + "/" + verifierModulename + "/" + appImgObj + "/status"
	downloaderAppImgObjStatusDirname := zedRunDirname + "/" + downloaderModulename + "/" + appImgObj + "/status"

	// only handle opp image type objects
	var noObjTypes []string
	objTypes := []string{appImgObj}

	// create config/status dirs
	watch.CreateConfigStatusDirs(domainMgrModulename, noObjTypes)
	watch.CreateConfigStatusDirs(identityMgrModulename, noObjTypes)
	watch.CreateConfigStatusDirs(zedmanagerModulename, noObjTypes)
	watch.CreateConfigStatusDirs(zedrouterModulename, noObjTypes)

	// create config/atatus dirs also for app images
	watch.CreateConfigStatusDirs(downloaderModulename, objTypes)
	watch.CreateConfigStatusDirs(verifierModulename, objTypes)

	// Tell ourselves to go ahead
	watch.SignalRestart("zedmanager")

	verifierRestartChanges := make(chan string)
	go watch.WatchStatus(verifierStatusDirname, verifierRestartChanges)

	var configRestartFn watch.ConfigRestartHandler = handleConfigRestart
	var verifierRestartedFn watch.StatusRestartHandler = handleVerifierRestarted
	var identitymgrRestartedFn watch.StatusRestartHandler = handleIdentitymgrRestarted
	var zedrouterRestartedFn watch.StatusRestartHandler = handleZedrouterRestarted

	// First we process the verifierStatus to avoid downloading
	// an image we already have in place
	log.Printf("Handling initial verifier Status\n")
	done := false
	for !done {
		select {
		case change := <-verifierRestartChanges:
			{
				watch.HandleStatusEvent(change,
					verifierStatusDirname,
					&types.VerifyImageStatus{},
					handleVerifyImageStatusModify,
					handleVerifyImageStatusDelete,
					&verifierRestartedFn)
				if verifierRestarted {
					log.Printf("Verifier reported restarted\n")
					done = true
					break
				}
			}
		}
	}

	verifierChanges := make(chan string)
	go watch.WatchStatus(verifierAppImgObjStatusDirname, verifierChanges)
	downloaderChanges := make(chan string)
	go watch.WatchStatus(downloaderAppImgObjStatusDirname, downloaderChanges)
	identitymgrChanges := make(chan string)
	go watch.WatchStatus(identitymgrStatusDirname, identitymgrChanges)
	zedrouterChanges := make(chan string)
	go watch.WatchStatus(zedrouterStatusDirname, zedrouterChanges)
	domainmgrChanges := make(chan string)
	go watch.WatchStatus(domainmgrStatusDirname, domainmgrChanges)
	configChanges := make(chan string)
	go watch.WatchConfigStatus(zedmanagerConfigDirname,
		zedmanagerStatusDirname, configChanges)
	deviceStatusChanges := make(chan string)
	go watch.WatchStatus(DNSDirname, deviceStatusChanges)

	log.Printf("Handling all inputs\n")
	for {
		select {
		case change := <-verifierRestartChanges:
			{
				watch.HandleStatusEvent(change,
					verifierStatusDirname,
					&types.VerifyImageStatus{},
					handleVerifyImageStatusModify,
					handleVerifyImageStatusDelete,
					&verifierRestartedFn)
				continue
			}
		case change := <-downloaderChanges:
			{
				watch.HandleStatusEvent(change,
					downloaderAppImgObjStatusDirname,
					&types.DownloaderStatus{},
					handleDownloaderStatusModify,
					handleDownloaderStatusDelete, nil)
				continue
			}
		case change := <-verifierChanges:
			{
				watch.HandleStatusEvent(change,
					verifierAppImgObjStatusDirname,
					&types.VerifyImageStatus{},
					handleVerifyImageStatusModify,
					handleVerifyImageStatusDelete,
					&verifierRestartedFn)
				continue
			}
		case change := <-identitymgrChanges:
			{
				watch.HandleStatusEvent(change,
					identitymgrStatusDirname,
					&types.EIDStatus{},
					handleEIDStatusModify,
					handleEIDStatusDelete,
					&identitymgrRestartedFn)
				continue
			}
		case change := <-zedrouterChanges:
			{
				watch.HandleStatusEvent(change,
					zedrouterStatusDirname,
					&types.AppNetworkStatus{},
					handleAppNetworkStatusModify,
					handleAppNetworkStatusDelete,
					&zedrouterRestartedFn)
				continue
			}
		case change := <-domainmgrChanges:
			{
				watch.HandleStatusEvent(change,
					domainmgrStatusDirname,
					&types.DomainStatus{},
					handleDomainStatusModify,
					handleDomainStatusDelete, nil)
				continue
			}
		case change := <-configChanges:
			{
				watch.HandleConfigStatusEvent(change,
					zedmanagerConfigDirname,
					zedmanagerStatusDirname,
					&types.AppInstanceConfig{},
					&types.AppInstanceStatus{},
					handleCreate, handleModify,
					handleDelete, &configRestartFn)
				continue
			}
		case change := <-deviceStatusChanges:
			{
				watch.HandleStatusEvent(change,
					DNSDirname,
					&types.DeviceNetworkStatus{},
					handleDNSModify, handleDNSDelete,
					nil)
			}
		}
	}
}

var configRestarted = false
var verifierRestarted = false

// Propagate a seqence of restart//restarted from the zedmanager config
// and verifier status to identitymgr, then from identitymgr to zedrouter,
// and finally from zedrouter to domainmgr.
// This removes the need for extra downloads/verifications and extra copying
// of the rootfs in domainmgr.
func handleConfigRestart(done bool) {
	log.Printf("handleConfigRestart(%v)\n", done)
	if done {
		configRestarted = true
		if verifierRestarted {
			watch.SignalRestart("identitymgr")
		}
	}
}

func handleVerifierRestarted(done bool) {
	log.Printf("handleVerifierRestarted(%v)\n", done)
	if done {
		verifierRestarted = true
		if configRestarted {
			watch.SignalRestart("identitymgr")
		}
	}
}

func handleIdentitymgrRestarted(done bool) {
	log.Printf("handleIdentitymgrRestarted(%v)\n", done)
	if done {
		watch.SignalRestart("zedrouter")
	}
}

func handleZedrouterRestarted(done bool) {
	log.Printf("handleZedrouterRestarted(%v)\n", done)
	if done {
		watch.SignalRestart("domainmgr")
	}
}

func writeAICStatus(status *types.AppInstanceStatus,
	statusFilename string) {
	b, err := json.Marshal(status)
	if err != nil {
		log.Fatal(err, "json Marshal AppInstanceStatus")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(statusFilename, b, 0644)
	if err != nil {
		log.Fatal(err, statusFilename)
	}
}

func writeAppInstanceStatus(status *types.AppInstanceStatus,
	statusFilename string) {
	b, err := json.Marshal(status)
	if err != nil {
		log.Fatal(err, "json Marshal AppInstanceStatus")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(statusFilename, b, 0644)
	if err != nil {
		log.Fatal(err, statusFilename)
	}
}

func handleCreate(statusFilename string, configArg interface{}) {
	var config *types.AppInstanceConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceConfig")
	case *types.AppInstanceConfig:
		config = configArg.(*types.AppInstanceConfig)
	}
	log.Printf("handleCreate(%v) for %s\n",
		config.UUIDandVersion, config.DisplayName)

	addOrUpdateConfig(config.UUIDandVersion.UUID.String(), *config)

	// Note that the status is written as we handle updates from the
	// other services
	log.Printf("handleCreate done for %s\n", config.DisplayName)
}

func handleModify(statusFilename string, configArg interface{},
	statusArg interface{}) {
	var config *types.AppInstanceConfig
	var status *types.AppInstanceStatus

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceConfig")
	case *types.AppInstanceConfig:
		config = configArg.(*types.AppInstanceConfig)
	}
	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceStatus")
	case *types.AppInstanceStatus:
		status = statusArg.(*types.AppInstanceStatus)
	}
	log.Printf("handleModify(%v) for %s\n",
		config.UUIDandVersion, config.DisplayName)

	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		fmt.Printf("Same version %s for %s\n",
			config.UUIDandVersion.Version, statusFilename)
		return
	}

	status.UUIDandVersion = config.UUIDandVersion
	writeAppInstanceStatus(status, statusFilename)

	addOrUpdateConfig(config.UUIDandVersion.UUID.String(), *config)
	// Note that the status is written as we handle updates from the
	// other services
	log.Printf("handleModify done for %s\n", config.DisplayName)
}

func handleDelete(statusFilename string, statusArg interface{}) {
	var status *types.AppInstanceStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle AppInstanceStatus")
	case *types.AppInstanceStatus:
		status = statusArg.(*types.AppInstanceStatus)
	}
	log.Printf("handleDelete(%v) for %s\n",
		status.UUIDandVersion, status.DisplayName)

	writeAppInstanceStatus(status, statusFilename)

	removeConfig(status.UUIDandVersion.UUID.String())
	log.Printf("handleDelete done for %s\n", status.DisplayName)
}

func handleDNSModify(statusFilename string,
	statusArg interface{}) {
	var status *types.DeviceNetworkStatus

	if statusFilename != "global" {
		fmt.Printf("handleDNSModify: ignoring %s\n", statusFilename)
		return
	}
	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle DeviceNetworkStatus")
	case *types.DeviceNetworkStatus:
		status = statusArg.(*types.DeviceNetworkStatus)
	}

	log.Printf("handleDNSModify for %s\n", statusFilename)
	globalStatus = *status
	log.Printf("handleDNSModify done for %s\n", statusFilename)
}

func handleDNSDelete(statusFilename string) {
	log.Printf("handleDNSDelete for %s\n", statusFilename)

	if statusFilename != "global" {
		fmt.Printf("handleDNSDelete: ignoring %s\n", statusFilename)
		return
	}
	globalStatus = types.DeviceNetworkStatus{}
	log.Printf("handleDNSDelete done for %s\n", statusFilename)
}
