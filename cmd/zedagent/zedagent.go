// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

// zedAgent interfaces with zedcloud for
//   * config sync
//   * metric/info pubish
// app instance config is pushed to zedmanager for orchestration
// baseos instance config is pushed to baseosmgr for orchestration
// event based app instance/device info published to ZedCloud
// periodic status/metric published to zedCloud

package main

import (
	"flag"
	"fmt"
	"github.com/zededa/go-provision/adapters"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/hardware"
	"github.com/zededa/go-provision/pidfile"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"github.com/zededa/go-provision/zboot"
	"log"
	"os"
	"time"
)

// Keeping status in /var/run to be clean after a crash/reboot
const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"
	agentName = "zedagent"

	downloaderModulename = "downloader"
	verifierModulename   = "verifier"
	zedagentModulename   = agentName
	zedmanagerModulename = "zedmanager"
	baseOsMgrModulename  = "baseosmgr"

	zedBaseDirname = "/var/tmp"
	zedRunDirname  = "/var/run"
	baseDirname    = zedBaseDirname + "/" + agentName
	runDirname     = zedRunDirname + "/" + agentName

	configDir          = "/config"
	persistDir         = "/persist"
	certificateDirname = persistDir + "/certs"

	zedagentConfigDirname = baseDirname + "/config"
	zedagentStatusDirname = runDirname + "/status"

	// app instance config/status holder
	zedmanagerConfigDirname = zedBaseDirname + "/" + zedmanagerModulename + "/config"
	zedmanagerStatusDirname = zedRunDirname + "/" + zedmanagerModulename + "/status"

	// base os config/status holder
	baseOsMgrBaseOsConfigDirname = zedBaseDirname + "/" + baseOsMgrModulename + "/" + baseOsObj + "/config"
	baseOsMgrBaseOsStatusDirname = zedRunDirname + "/" + baseOsMgrModulename + "/" + baseOsObj + "/status"

	// certificate config/status holder
	baseOsMgrCertConfigDirname = zedBaseDirname + "/" + baseOsMgrModulename + "/" + certObj + "/config"
	baseOsMgrCertStatusDirname = zedRunDirname + "/" + baseOsMgrModulename + "/" + certObj + "/status"

	DNSDirname          = "/var/run/zedrouter/DeviceNetworkStatus"
	domainStatusDirname = "/var/run/domainmgr/status"
)

// Set from Makefile
var Version = "No version specified"

var deviceNetworkStatus types.DeviceNetworkStatus

// Dummy used when we don't have anything to pass
type dummyContext struct {
}

// Context for handleDNSModify
type DNSContext struct {
	usableAddressCount int
	triggerGetConfig   bool
}

// Information from handleVerifierRestarted
type verifierContext struct {
	verifierRestarted bool
}

// Information for handleDomainStatus*
type domainContext struct {
	TriggerDeviceInfo bool
}

// Information for handleBaseOsCreate/Modify/Delete and handleAppInstanceStatus*
type deviceContext struct {
	assignableAdapters *types.AssignableAdapters
}

var debug = false

// XXX temporary hack for writeBaseOsStatus
var devCtx deviceContext

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
	appInstanceStatusChanges := make(chan string)
	baseOsInstanceStatusChanges := make(chan string)

	// Pick up (mostly static) AssignableAdapters before we report
	// any device info
	model := hardware.GetHardwareModel()
	log.Printf("HardwareModel %s\n", model)
	aa := types.AssignableAdapters{}
	aaChanges, aaFunc, aaCtx := adapters.Init(&aa, model)

	devCtx = deviceContext{assignableAdapters: &aa}
	domainCtx := domainContext{}

	select {
	case change := <-aaChanges:
		aaFunc(&aaCtx, change)
	}

	DNSctx := DNSContext{}
	DNSctx.usableAddressCount = types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus)

	networkStatusChanges := make(chan string)
	go watch.WatchStatus(DNSDirname, networkStatusChanges)

	// Context to pass around
	getconfigCtx := getconfigContext{}
	upgradeInprogress := zboot.IsAvailable() && zboot.IsCurrentPartitionStateInProgress()
	time1 := time.Duration(configItemCurrent.resetIfCloudGoneTime)
	t1 := time.NewTimer(time1 * time.Second)
	log.Printf("Started timer for reset for %d seconds\n", time1)
	time2 := time.Duration(configItemCurrent.fallbackIfCloudGoneTime)
	log.Printf("Started timer for fallback (%v) reset for %d seconds\n",
		upgradeInprogress, time2)
	t2 := time.NewTimer(time2 * time.Second)

	log.Printf("Waiting until we have some uplinks with usable addresses\n")
	waited := false
	for types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus) == 0 ||
		!aaCtx.Found {
		log.Printf("Waiting - have %d addresses; aaCtx %v\n",
			types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus),
			aaCtx.Found)
		waited = true

		select {
		case change := <-networkStatusChanges:
			watch.HandleStatusEvent(change, &DNSctx,
				DNSDirname,
				&types.DeviceNetworkStatus{},
				handleDNSModify, handleDNSDelete,
				nil)
		case change := <-aaChanges:
			aaFunc(&aaCtx, change)
		case <-t1.C:
			log.Printf("Exceeded outage for cloud connectivity - rebooting\n")
			zboot.ExecReboot(true)
		case <-t2.C:
			if upgradeInprogress {
				log.Printf("Exceeded fallback outage for cloud connectivity - rebooting\n")
				zboot.ExecReboot(true)
			}
		}
	}
	t1.Stop()
	t2.Stop()
	log.Printf("Have %d uplinks addresses to use; aaCtx %v\n",
		types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus),
		aaCtx.Found)
	if waited {
		// Inform ledmanager that we have uplink addresses
		types.UpdateLedManagerConfig(2)
		getconfigCtx.ledManagerCount = 2
	}

	// Publish initial device info. Retries all addresses on all uplinks.
	PublishDeviceInfoToZedCloud(baseOsStatusMap, devCtx.assignableAdapters)

	// start the metrics/config fetch tasks
	handleChannel := make(chan interface{})
	go configTimerTask(handleChannel, &getconfigCtx)
	log.Printf("Waiting for flexticker handle\n")
	configTickerHandle := <-handleChannel
	go metricsTimerTask(handleChannel)
	metricsTickerHandle := <-handleChannel
	// XXX close handleChannels?
	getconfigCtx.configTickerHandle = configTickerHandle
	getconfigCtx.metricsTickerHandle = metricsTickerHandle

	// app instance status event watcher
	go watch.WatchStatus(zedmanagerStatusDirname,
		appInstanceStatusChanges)

	// base os instance status event watcher
	go watch.WatchStatus(baseOsMgrBaseOsStatusDirname,
		baseOsInstanceStatusChanges)

	// for restart flag handling
	go watch.WatchStatus(zedagentStatusDirname, restartChanges)

	domainStatusChanges := make(chan string)
	go watch.WatchStatus(domainStatusDirname, domainStatusChanges)

	for {
		select {

		case change := <-restartChanges:
			// restart only, place holder
			watch.HandleStatusEvent(change, &devCtx,
				zedagentStatusDirname,
				&types.AppInstanceStatus{},
				handleAppInstanceStatusModify,
				handleAppInstanceStatusDelete, &restartFn)

		case change := <-appInstanceStatusChanges:
			watch.HandleStatusEvent(change, &devCtx,
				zedmanagerStatusDirname,
				&types.AppInstanceStatus{},
				handleAppInstanceStatusModify,
				handleAppInstanceStatusDelete, nil)

		case change := <-baseOsInstanceStatusChanges:
			watch.HandleStatusEvent(change, &devCtx,
				baseOsMgrBaseOsStatusDirname,
				&types.BaseOsStatus{},
				handleBaseOsModify,
				handleBaseOsDelete, nil)

		case change := <-networkStatusChanges:
			watch.HandleStatusEvent(change, &DNSctx,
				DNSDirname,
				&types.DeviceNetworkStatus{},
				handleDNSModify, handleDNSDelete,
				nil)
			if DNSctx.triggerGetConfig {
				triggerGetConfig(configTickerHandle)
				DNSctx.triggerGetConfig = false
			}
			// IP/DNS in device info could have changed
			// XXX could compare in handleDNSModify as we do
			// for handleDomainStatus
			log.Printf("NetworkStatus triggered PublishDeviceInfo\n")
			PublishDeviceInfoToZedCloud(baseOsStatusMap,
				devCtx.assignableAdapters)

		case change := <-domainStatusChanges:
			watch.HandleStatusEvent(change, &domainCtx,
				domainStatusDirname,
				&types.DomainStatus{},
				handleDomainStatusModify, handleDomainStatusDelete,
				nil)
			// UsedByUUID could have changed ...
			if domainCtx.TriggerDeviceInfo {
				log.Printf("Triggered PublishDeviceInfo\n")
				PublishDeviceInfoToZedCloud(baseOsStatusMap,
					devCtx.assignableAdapters)
				domainCtx.TriggerDeviceInfo = false
			}

		case change := <-aaChanges:
			aaFunc(&aaCtx, change)
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

func handleInit() {
	initializeDirs()
	initBaseOsMaps()
	handleConfigInit()
}

func initializeDirs() {

	noObjTypes := []string{}
	baseOsMgrObjTypes := []string{baseOsObj, certObj}

	// create the module object based config/status dirs
	createConfigStatusDirs(baseOsMgrModulename, baseOsMgrObjTypes)
	createConfigStatusDirs(zedmanagerModulename, noObjTypes)
	createConfigStatusDirs(zedagentModulename, noObjTypes)

	// create persistent holder directory
	if _, err := os.Stat(persistDir); err != nil {
		if err := os.MkdirAll(persistDir, 0700); err != nil {
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

// app instance event watch to capture transitions
// and publish to zedCloud

func handleAppInstanceStatusModify(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.AppInstanceStatus)
	ctx := ctxArg.(*deviceContext)
	uuidStr := status.UUIDandVersion.UUID.String()
	PublishAppInfoToZedCloud(uuidStr, status, ctx.assignableAdapters)
}

func handleAppInstanceStatusDelete(ctxArg interface{}, statusFilename string) {
	// statusFilename == key aka UUIDstr?
	ctx := ctxArg.(*deviceContext)
	uuidStr := statusFilename
	PublishAppInfoToZedCloud(uuidStr, nil, ctx.assignableAdapters)
}

func handleDNSModify(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.DeviceNetworkStatus)
	ctx := ctxArg.(*DNSContext)

	if statusFilename != "global" {
		log.Printf("handleDNSModify: ignoring %s\n", statusFilename)
		return
	}
	log.Printf("handleDNSModify for %s\n", statusFilename)
	deviceNetworkStatus = *status
	// Did we (re-)gain the first usable address?
	// XXX should we also trigger if the count increases?
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus)
	if newAddrCount != 0 && ctx.usableAddressCount == 0 {
		log.Printf("DeviceNetworkStatus from %d to %d addresses\n",
			newAddrCount, ctx.usableAddressCount)
		ctx.triggerGetConfig = true
	}
	ctx.usableAddressCount = newAddrCount
	log.Printf("handleDNSModify done for %s\n", statusFilename)
}

func handleDNSDelete(ctxArg interface{}, statusFilename string) {
	log.Printf("handleDNSDelete for %s\n", statusFilename)
	ctx := ctxArg.(*DNSContext)

	if statusFilename != "global" {
		log.Printf("handleDNSDelete: ignoring %s\n", statusFilename)
		return
	}
	deviceNetworkStatus = types.DeviceNetworkStatus{}
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus)
	ctx.usableAddressCount = newAddrCount
	log.Printf("handleDNSDelete done for %s\n", statusFilename)
}

// base os config modify event
func handleBaseOsModify(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.BaseOsStatus)
	ctx := ctxArg.(*deviceContext)
	uuidStr := status.UUIDandVersion.UUID.String()

	log.Printf("handleBaseOsModify(%s) for %s\n", status.BaseOsVersion, uuidStr)
	addOrUpdateBaseOsStatus(uuidStr, status)
	PublishDeviceInfoToZedCloud(baseOsStatusMap, ctx.assignableAdapters)
}

// base os config delete event
func handleBaseOsDelete(ctxArg interface{}, statusFilename string) {
	ctx := ctxArg.(*deviceContext)
	uuidStr := statusFilename
	log.Printf("handleBaseOsDelete for %s\n", uuidStr)
	removeBaseOsStatus(uuidStr)
	PublishDeviceInfoToZedCloud(baseOsStatusMap, ctx.assignableAdapters)
}
