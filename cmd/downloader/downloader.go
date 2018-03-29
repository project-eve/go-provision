// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Process input changes from a config directory containing json encoded files
// with DownloaderConfig and compare against DownloaderStatus in the status
// dir.
// ZedManager can stop the download by removing from config directory.
//
// Input directory with config (URL, refcount, maxLength, dstDir)
// Output directory with status (URL, refcount, state, ModTime, lastErr, lastErrTime, retryCount)
// refCount -> 0 means delete from dstDir? Who owns dstDir? Separate mount.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/zededa/api/zconfig"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/flextimer"
	"github.com/zededa/go-provision/pidfile"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"github.com/zededa/go-provision/wrap"
	"github.com/zededa/go-provision/zedcloud"
	"github.com/zededa/shared/libs/zedUpload"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"
	agentName = "downloader"

	moduleName            = agentName
	zedBaseDirname        = "/var/tmp"
	zedRunDirname         = "/var/run"
	baseDirname           = zedBaseDirname + "/" + moduleName
	runDirname            = zedRunDirname + "/" + moduleName
	persistDir            = "/persist"
	objectDownloadDirname = persistDir + "/downloads"
	DNSDirname            = "/var/run/zedrouter/DeviceNetworkStatus"

	downloaderConfigDirname = baseDirname + "/config"
	downloaderStatusDirname = runDirname + "/status"

	appImgConfigDirname = baseDirname + "/" + appImgObj + "/config"
	appImgStatusDirname = runDirname + "/" + appImgObj + "/status"

	baseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	baseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"

	certObjConfigDirname = baseDirname + "/" + certObj + "/config"
	certObjStatusDirname = runDirname + "/" + certObj + "/status"
)

// Go doesn't like this as a constant
var (
	downloaderObjTypes = []string{appImgObj, baseOsObj, certObj}
)

// Set from Makefile
var Version = "No version specified"

type downloaderContext struct {
	dCtx *zedUpload.DronaCtx
}

// Dummy since where we don't have anything to pass
type dummyContext struct {
}

var deviceNetworkStatus types.DeviceNetworkStatus

func main() {
	logf, err := agentlog.Init(agentName)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	versionPtr := flag.Bool("v", false, "Version")
	flag.Parse()
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
		log.Fatal(err)
	}
	log.Printf("Starting %s\n", agentName)
	for _, ot := range downloaderObjTypes {
		watch.CleanupRestartedObj(agentName, ot)
	}

	cms := zedcloud.GetCloudMetrics() // Need type of data
	pub, err := pubsub.Publish(agentName, cms)
	if err != nil {
		log.Fatal(err)
	}

	// Publish send metrics for zedagent every 10 seconds
	interval := time.Duration(10 * time.Second)
	max := float64(interval)
	min := max * 0.3
	publishTimer := flextimer.NewRangeTicker(time.Duration(min),
		time.Duration(max))

	// Any state needed by handler functions
	ctx := downloaderContext{}
	ctx.dCtx = downloaderInit()

	networkStatusChanges := make(chan string)
	go watch.WatchStatus(DNSDirname, networkStatusChanges)

	// First wait to have some uplinks with addresses
	// Looking at any uplinks since we can do baseOS download over all
	for types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus) == 0 {
		select {
		case change := <-networkStatusChanges:
			watch.HandleStatusEvent(change, dummyContext{},
				DNSDirname,
				&types.DeviceNetworkStatus{},
				handleDNSModify, handleDNSDelete,
				nil)
		}
	}
	log.Printf("Have %d uplinks addresses to use\n",
		types.CountLocalAddrAnyNoLinkLocal(deviceNetworkStatus))

	appImgChanges := make(chan string)
	baseOsChanges := make(chan string)
	certObjChanges := make(chan string)

	go watch.WatchConfigStatus(appImgConfigDirname,
		appImgStatusDirname, appImgChanges)

	go watch.WatchConfigStatus(baseOsConfigDirname,
		baseOsStatusDirname, baseOsChanges)

	go watch.WatchConfigStatus(certObjConfigDirname,
		certObjStatusDirname, certObjChanges)

	for {
		select {

		case change := <-networkStatusChanges:
			watch.HandleStatusEvent(change, dummyContext{},
				DNSDirname,
				&types.DeviceNetworkStatus{},
				handleDNSModify, handleDNSDelete,
				nil)

		case change := <-certObjChanges:
			watch.HandleConfigStatusEvent(change, &ctx,
				certObjConfigDirname,
				certObjStatusDirname,
				&types.DownloaderConfig{},
				&types.DownloaderStatus{},
				handleCertObjCreate,
				handleCertObjModify,
				handleCertObjDelete, nil)

		case change := <-appImgChanges:
			watch.HandleConfigStatusEvent(change, &ctx,
				appImgConfigDirname,
				appImgStatusDirname,
				&types.DownloaderConfig{},
				&types.DownloaderStatus{},
				handleAppImgObjCreate,
				handleAppImgObjModify,
				handleAppImgObjDelete, nil)

		case change := <-baseOsChanges:
			watch.HandleConfigStatusEvent(change, &ctx,
				baseOsConfigDirname,
				baseOsStatusDirname,
				&types.DownloaderConfig{},
				&types.DownloaderStatus{},
				handleBaseOsObjCreate,
				handleBaseOsObjModify,
				handleBaseOsObjDelete, nil)
		case <-publishTimer.C:
			pub.Publish("global", zedcloud.GetCloudMetrics())
		}
	}
}

// Object handlers
func handleAppImgObjCreate(ctxArg interface{}, statusFilename string,
	configArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectCreate: %s\n", config.DownloadURL)
	handleCreate(ctx, appImgObj, *config, statusFilename)
}

func handleBaseOsObjCreate(ctxArg interface{}, statusFilename string,
	configArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectCreate: %s\n", config.DownloadURL)
	handleCreate(ctx, baseOsObj, *config, statusFilename)
}

func handleCertObjCreate(ctxArg interface{}, statusFilename string,
	configArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectCreate: %s\n", config.DownloadURL)
	handleCreate(ctx, certObj, *config, statusFilename)
}

func handleAppImgObjModify(ctxArg interface{}, statusFilename string,
	configArg interface{}, statusArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectModify(%v) for %s\n",
		config.Safename, config.DownloadURL)
	handleModify(ctx, appImgObj, *config, *status, statusFilename)
}

func handleBaseOsObjModify(ctxArg interface{}, statusFilename string,
	configArg interface{}, statusArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectModify(%v) for %s\n",
		config.Safename, config.DownloadURL)
	handleModify(ctx, baseOsObj, *config, *status, statusFilename)
}

func handleCertObjModify(ctxArg interface{}, statusFilename string,
	configArg interface{}, statusArg interface{}) {
	config := configArg.(*types.DownloaderConfig)
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectModify(%v) for %s\n",
		config.Safename, config.DownloadURL)
	handleModify(ctx, certObj, *config, *status, statusFilename)
}

func handleAppImgObjDelete(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectDelete(%v) for %s\n",
		status.Safename, status.DownloadURL)
	handleDelete(ctx, appImgObj, *status, statusFilename)
}

func handleBaseOsObjDelete(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectDelete(%v) for %s\n",
		status.Safename, status.DownloadURL)
	handleDelete(ctx, baseOsObj, *status, statusFilename)
}

func handleCertObjDelete(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.DownloaderStatus)
	ctx := ctxArg.(*downloaderContext)

	log.Printf("handleObjectDelete(%v) for %s\n",
		status.Safename, status.DownloadURL)
	handleDelete(ctx, certObj, *status, statusFilename)
}

func handleCreate(ctx *downloaderContext, objType string,
	config types.DownloaderConfig,
	statusFilename string) {

	// Start by marking with PendingAdd
	status := types.DownloaderStatus{
		Safename:       config.Safename,
		RefCount:       config.RefCount,
		DownloadURL:    config.DownloadURL,
		UseFreeUplinks: config.UseFreeUplinks,
		ImageSha256:    config.ImageSha256,
		PendingAdd:     true,
	}
	writeDownloaderStatus(&status, statusFilename)

	// Check if we have space
	kb := types.RoundupToKB(config.Size)
	if uint(kb) >= globalStatus.RemainingSpace {
		errString := fmt.Sprintf("Would exceed remaining space %d vs %d\n",
			kb, globalStatus.RemainingSpace)
		log.Println(errString)
		status.PendingAdd = false
		status.Size = 0
		status.LastErr = errString
		status.LastErrTime = time.Now()
		status.RetryCount += 1
		status.State = types.INITIAL
		writeDownloaderStatus(&status, statusFilename)
		log.Printf("handleCreate failed for %s\n", config.DownloadURL)
		return
	}

	// Update reserved space. Keep reserved until doDelete
	// XXX RefCount -> 0 should keep it reserved.
	status.ReservedSpace = uint(types.RoundupToKB(config.Size))
	globalStatus.ReservedSpace += status.ReservedSpace
	updateRemainingSpace()

	// If RefCount == 0 then we don't yet download.
	if config.RefCount == 0 {
		// XXX odd to treat as error.
		errString := fmt.Sprintf("RefCount==0; download deferred for %s\n",
			config.DownloadURL)
		log.Println(errString)
		status.PendingAdd = false
		status.Size = 0
		status.LastErr = errString
		status.LastErrTime = time.Now()
		status.RetryCount += 1
		status.State = types.INITIAL
		writeDownloaderStatus(&status, statusFilename)
		log.Printf("handleCreate deferred for %s\n", config.DownloadURL)
		return
	}

	handleSyncOp(ctx, objType, statusFilename, config, &status)
}

// Allow to cancel by setting RefCount = 0. Same as delete? RefCount 0->1
// means download. Ignore other changes?
func handleModify(ctx *downloaderContext, objType string,
	config types.DownloaderConfig, status types.DownloaderStatus,
	statusFilename string) {

	locDirname := objectDownloadDirname + "/" + objType

	if config.DownloadURL != status.DownloadURL {
		log.Printf("URL changed - not allowed %s -> %s\n",
			config.DownloadURL, status.DownloadURL)
		return
	}
	// If the sha changes, we treat it as a delete and recreate.
	// Ditto if we had a failure.
	if (status.ImageSha256 != "" && status.ImageSha256 != config.ImageSha256) ||
		status.LastErr != "" {
		reason := ""
		if status.ImageSha256 != config.ImageSha256 {
			reason = "sha256 changed"
		} else {
			reason = "recovering from previous error"
		}
		log.Printf("handleModify %s for %s\n",
			reason, config.DownloadURL)
		doDelete(statusFilename, locDirname, &status)
		handleCreate(ctx, objType, config, statusFilename)
		log.Printf("handleModify done for %s\n", config.DownloadURL)
		return
	}

	// XXX do work; look for refcnt -> 0 and delete; cancel any running
	// download
	// If RefCount from zero to non-zero then do install
	if status.RefCount == 0 && config.RefCount != 0 {

		log.Printf("handleModify installing %s\n", config.DownloadURL)
		handleCreate(ctx, objType, config, statusFilename)
		status.RefCount = config.RefCount
		status.PendingModify = false
		writeDownloaderStatus(&status, statusFilename)
	} else if status.RefCount != 0 && config.RefCount == 0 {
		log.Printf("handleModify deleting %s\n", config.DownloadURL)
		doDelete(statusFilename, locDirname, &status)
	} else {
		status.RefCount = config.RefCount
		status.PendingModify = false
		writeDownloaderStatus(&status, statusFilename)
	}
	log.Printf("handleModify done for %s\n", config.DownloadURL)
}

func doDelete(statusFilename string, locDirname string, status *types.DownloaderStatus) {

	log.Printf("doDelete(%v) for %s\n", status.Safename, status.DownloadURL)

	// XXX:FIXME, delete from verifier/verfied !!
	locFilename := locDirname + "/pending"

	if status.ImageSha256 != "" {
		locFilename = locFilename + "/" + status.ImageSha256
	}

	if _, err := os.Stat(locFilename); err == nil {
		locFilename := locFilename + "/" + status.Safename
		if _, err := os.Stat(locFilename); err == nil {
			log.Printf("Deleting %s\n", locFilename)
			// Remove file
			if err := os.Remove(locFilename); err != nil {
				log.Printf("Failed to remove %s: err %s\n",
					locFilename, err)
			}
		}
	}

	status.State = types.INITIAL
	globalStatus.UsedSpace -= uint(types.RoundupToKB(status.Size))
	status.Size = 0

	// XXX Asymmetric; handleCreate reserved on RefCount 0. We unreserve
	// going back to RefCount 0. FIXed
	updateRemainingSpace()
	writeDownloaderStatus(status, statusFilename)
}

func handleDelete(ctx *downloaderContext, objType string,
	status types.DownloaderStatus, statusFilename string) {

	locDirname := objectDownloadDirname + "/" + objType

	status.PendingDelete = true
	writeDownloaderStatus(&status, statusFilename)

	globalStatus.ReservedSpace -= status.ReservedSpace
	status.ReservedSpace = 0
	globalStatus.UsedSpace -= uint(types.RoundupToKB(status.Size))
	status.Size = 0

	updateRemainingSpace()

	writeDownloaderStatus(&status, statusFilename)

	doDelete(statusFilename, locDirname, &status)

	status.PendingDelete = false
	writeDownloaderStatus(&status, statusFilename)

	// Write out what we modified to DownloaderStatus aka delete
	if err := os.Remove(statusFilename); err != nil {
		log.Println(err)
	}
	log.Printf("handleDelete done for %s, %s\n", status.DownloadURL, locDirname)
}

// helper functions

var globalConfig types.GlobalDownloadConfig
var globalStatus types.GlobalDownloadStatus
var globalStatusFilename string

func downloaderInit() *zedUpload.DronaCtx {

	initializeDirs()

	configFilename := downloaderConfigDirname + "/global"
	statusFilename := downloaderStatusDirname + "/global"

	// now start
	globalStatusFilename = statusFilename

	// Read GlobalDownloadConfig to find MaxSpace
	// Then determine currently used space and remaining.
	cb, err := ioutil.ReadFile(configFilename)
	if err != nil {
		log.Printf("%s for %s\n", err, configFilename)
		log.Fatal(err)
	}

	if err := json.Unmarshal(cb, &globalConfig); err != nil {
		log.Printf("%s GlobalDownloadConfig file: %s\n",
			err, configFilename)
		log.Fatal(err)
	}

	log.Printf("MaxSpace %d\n", globalConfig.MaxSpace)

	globalStatus.UsedSpace = 0
	globalStatus.ReservedSpace = 0
	updateRemainingSpace()

	// XXX how do we find out when verifier cleans up duplicates etc?
	// We read objectDownloadDirname/* and determine how much space
	// is used. Place in GlobalDownloadStatus. Calculate remaining space.
	totalUsed := sizeFromDir(objectDownloadDirname)
	globalStatus.UsedSpace = uint(types.RoundupToKB(totalUsed))
	updateRemainingSpace()

	// create drona interface
	dCtx, err := zedUpload.NewDronaCtx("zdownloader", 0)

	if dCtx == nil {
		log.Printf("context create fail %s\n", err)
		log.Fatal(err)
	}

	return dCtx
}

func initializeDirs() {

	// Remove any files which didn't make it past the verifier.
	// Though verifier owns it, remove them for calculating the
	// total available space
	clearInProgressDownloadDirs(downloaderObjTypes)

	// create the object based config/status dirs
	createConfigStatusDirs(moduleName, downloaderObjTypes)

	// create the object download directories
	createDownloadDirs(downloaderObjTypes)
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

// create object download directories
func createDownloadDirs(objTypes []string) {

	workingDirTypes := []string{"pending", "verifier", "verified"}

	// now create the download dirs
	for _, objType := range objTypes {
		for _, dirType := range workingDirTypes {
			dirName := objectDownloadDirname + "/" + objType + "/" + dirType
			if _, err := os.Stat(dirName); err != nil {
				if err := os.MkdirAll(dirName, 0700); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

// clear in-progress object download directories
func clearInProgressDownloadDirs(objTypes []string) {

	inProgressDirTypes := []string{"pending", "verifier"}

	// now create the download dirs
	for _, objType := range objTypes {
		for _, dirType := range inProgressDirTypes {
			dirName := objectDownloadDirname + "/" + objType + "/" + dirType
			if _, err := os.Stat(dirName); err == nil {
				if err := os.RemoveAll(dirName); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

func sizeFromDir(dirname string) uint64 {
	var totalUsed uint64 = 0
	locations, err := ioutil.ReadDir(dirname)
	if err != nil {
		log.Fatal(err)
	}
	for _, location := range locations {
		filename := dirname + "/" + location.Name()
		log.Printf("Looking in %s\n", filename)
		if location.IsDir() {
			size := sizeFromDir(filename)
			log.Printf("Dir %s size %d\n", filename, size)
			totalUsed += size
		} else {
			log.Printf("File %s Size %d\n", filename, location.Size())
			totalUsed += uint64(location.Size())
		}
	}
	return totalUsed
}

func updateRemainingSpace() {

	globalStatus.RemainingSpace = globalConfig.MaxSpace -
		globalStatus.UsedSpace - globalStatus.ReservedSpace

	log.Printf("RemaingSpace %d, maxspace %d, usedspace %d, reserved %d\n",
		globalStatus.RemainingSpace, globalConfig.MaxSpace,
		globalStatus.UsedSpace, globalStatus.ReservedSpace)
	// Create and write
	writeGlobalStatus()
}

func writeGlobalStatus() {

	sb, err := json.Marshal(globalStatus)
	if err != nil {
		log.Fatal(err, "json Marshal GlobalDownloadStatus")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	if err = ioutil.WriteFile(globalStatusFilename, sb, 0644); err != nil {
		log.Fatal(err, globalStatusFilename)
	}
}

func writeDownloaderStatus(status *types.DownloaderStatus,
	statusFilename string) {
	b, err := json.Marshal(status)
	if err != nil {
		log.Fatal(err, "json Marshal DownloaderStatus")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(statusFilename, b, 0644)
	if err != nil {
		log.Fatal(err, statusFilename)
	}
}

func writeFile(sFilename string, dFilename string) {

	log.Printf("Writing <%s> file to <%s>\n", sFilename, dFilename)

	if _, err := os.Stat(sFilename); err == nil {
		sb, err := ioutil.ReadFile(sFilename)
		if err == nil {

			if err = ioutil.WriteFile(dFilename, sb, 0644); err != nil {
				log.Printf("Failed to write %s: err %s\n",
					dFilename, err)
			}
		} else {
			log.Printf("Failed to read %s: err %s\n",
				sFilename)
		}
	} else {
		log.Printf("Failed to stat %s: err %s\n",
			sFilename, err)
	}
}

// cloud storage interface functions/APIs

// XXX should we use --cacart? Would assume we know the root CA.
// XXX Set --limit-rate 100k
// XXX continue based on filesize with: -C -
// Note that support for --dns-interface is not compiled in
// Normally "ifname" is the source IP to be consistent with the S3 loop
func doCurl(url string, ifname string, maxsize uint64, destFilename string) error {
	cmd := "curl"
	args := []string{}
	maxsizeStr := strconv.FormatUint(maxsize, 10)
	if ifname != "" {
		args = []string{
			"-q",
			"-4", // XXX due to getting IPv6 ULAs and not IPv4
			"--insecure",
			"--retry",
			"3",
			"--silent",
			"--show-error",
			"--interface",
			ifname,
			"--max-filesize",
			maxsizeStr,
			"-o",
			destFilename,
			url,
		}
	} else {
		args = []string{
			"-q",
			"-4", // XXX due to getting IPv6 ULAs and not IPv4
			"--insecure",
			"--retry",
			"3",
			"--silent",
			"--show-error",
			"--max-filesize",
			maxsizeStr,
			"-o",
			destFilename,
			url,
		}
	}

	stdoutStderr, err := wrap.Command(cmd, args...).CombinedOutput()

	if err != nil {
		log.Println("curl failed ", err)
	} else {
		log.Printf("curl done: output <%s>\n", string(stdoutStderr))
	}
	return err
}

func doS3(ctx *downloaderContext, syncOp zedUpload.SyncOpType,
	apiKey string, password string, dpath string, maxsize uint64,
	ipSrc net.IP, filename string, locFilename string) error {
	auth := &zedUpload.AuthInput{
		AuthType: "s3",
		Uname:    apiKey,
		Password: password,
	}

	trType := zedUpload.SyncAwsTr
	// XXX:FIXME , will come as part of data store
	region := "us-west-2"

	// create Endpoint
	dEndPoint, err := ctx.dCtx.NewSyncerDest(trType, region, dpath, auth)
	if err != nil {
		log.Printf("NewSyncerDest failed: %s\n", err)
		return err
	}
	dEndPoint.WithSrcIpSelection(ipSrc)
	var respChan = make(chan *zedUpload.DronaRequest)

	log.Printf("syncOp for <%s>/<%s>\n", dpath, filename)
	// create Request
	// Round up from bytes to Mbytes
	maxMB := (maxsize + 1024*1024 - 1) / (1024 * 1024)
	req := dEndPoint.NewRequest(syncOp, filename, locFilename,
		int64(maxMB), true, respChan)
	if req == nil {
		return errors.New("NewRequest failed")
	}

	req.Post()
	resp := <-respChan
	_, err = resp.GetUpStatus()
	if resp.IsError() == false {
		return nil
	} else {
		return err
	}
}

// Drona APIs for object Download

func handleSyncOp(ctx *downloaderContext, objType string, statusFilename string,
	config types.DownloaderConfig, status *types.DownloaderStatus) {
	var err error
	var locFilename string

	var syncOp zedUpload.SyncOpType = zedUpload.SyncOpDownload

	locDirname := objectDownloadDirname + "/" + objType
	locFilename = locDirname + "/pending"

	// update status to DOWNLOAD STARTED
	status.State = types.DOWNLOAD_STARTED
	writeDownloaderStatus(status, statusFilename)

	if config.ImageSha256 != "" {
		locFilename = locFilename + "/" + config.ImageSha256
	}

	if _, err = os.Stat(locFilename); err != nil {

		if err = os.MkdirAll(locFilename, 0755); err != nil {
			log.Fatal(err)
		}
	}

	filename := types.SafenameToFilename(config.Safename)

	locFilename = locFilename + "/" + config.Safename

	log.Printf("Downloading <%s> to <%s> using %v freeuplink\n",
		config.DownloadURL, locFilename, config.UseFreeUplinks)

	var addrCount int
	if config.UseFreeUplinks {
		addrCount = types.CountLocalAddrFree(deviceNetworkStatus, "")
		log.Printf("Have %d free uplink addresses\n", addrCount)
		err = errors.New("No free IP uplink addresses for download")
	} else {
		addrCount = types.CountLocalAddrAny(deviceNetworkStatus, "")
		log.Printf("Have %d any uplink addresses\n", addrCount)
		err = errors.New("No IP uplink addresses for download")
	}
	metricsUrl := config.DownloadURL
	if config.TransportMethod == zconfig.DsType_DsS3.String() {
		// fake URL for metrics
		metricsUrl = fmt.Sprintf("S3:%s/%s", config.Dpath, filename)
	}

	// Loop through all interfaces until a success
	for addrIndex := 0; addrIndex < addrCount; addrIndex += 1 {
		var ipSrc net.IP
		if config.UseFreeUplinks {
			ipSrc, err = types.GetLocalAddrFree(deviceNetworkStatus,
				addrIndex, "")
		} else {
			// Note that GetLocalAddrAny has the free ones first
			ipSrc, err = types.GetLocalAddrAny(deviceNetworkStatus,
				addrIndex, "")
		}
		if err != nil {
			log.Printf("GetLocalAddr failed: %s\n", err)
			continue
		}
		ifname := types.GetUplinkFromAddr(deviceNetworkStatus, ipSrc)
		log.Printf("Using IP source %v if %s transport %v\n",
			ipSrc, ifname, config.TransportMethod)
		switch config.TransportMethod {
		case zconfig.DsType_DsS3.String():
			err = doS3(ctx, syncOp, config.ApiKey,
				config.Password, config.Dpath,
				config.Size, ipSrc, filename, locFilename)
			if err != nil {
				log.Printf("Source IP %s failed: %s\n",
					ipSrc.String(), err)
				// XXX don't know how much we downloaded!
				// Could have failed half-way. Using zero.
				zedcloud.ZedCloudFailure(ifname,
					metricsUrl, 1024, 0)
			} else {
				// Record how much we downloaded
				info, _ := os.Stat(locFilename)
				size := info.Size()
				zedcloud.ZedCloudSuccess(ifname,
					metricsUrl, 1024, size)
				handleSyncOpResponse(objType, config, status,
					locFilename, statusFilename, err)
				return
			}
		case zconfig.DsType_DsHttp.String():
		case zconfig.DsType_DsHttps.String():
		case "":
			err = doCurl(config.DownloadURL, ipSrc.String(),
				config.Size, locFilename)
			if err != nil {
				log.Printf("Source IP %s failed: %s\n",
					ipSrc.String(), err)
				zedcloud.ZedCloudFailure(ifname,
					metricsUrl, 1024, 0)
			} else {
				// Record how much we downloaded
				info, _ := os.Stat(locFilename)
				size := info.Size()
				zedcloud.ZedCloudSuccess(ifname,
					metricsUrl, 1024, size)
				handleSyncOpResponse(objType, config, status,
					locFilename, statusFilename, err)
				return
			}
		default:
			log.Fatal("unsupported transport method")
		}
	}
	log.Printf("All source IP addresses failed. Last %s\n", err)
	handleSyncOpResponse(objType, config, status, locFilename,
		statusFilename, err)
}

func handleSyncOpResponse(objType string, config types.DownloaderConfig,
	status *types.DownloaderStatus, locFilename string,
	statusFilename string, err error) {

	locDirname := objectDownloadDirname + "/" + objType
	if err != nil {
		// Delete file
		doDelete(statusFilename, locDirname, status)
		status.PendingAdd = false
		status.Size = 0
		status.LastErr = fmt.Sprintf("%v", err)
		status.LastErrTime = time.Now()
		status.RetryCount += 1
		status.State = types.INITIAL
		writeDownloaderStatus(status, statusFilename)
		log.Printf("handleCreate failed for %s, <%s>\n",
			status.DownloadURL, err)
		return
	}

	info, err := os.Stat(locFilename)
	if err != nil {
		// Delete file
		doDelete(statusFilename, locDirname, status)
		status.PendingAdd = false
		status.Size = 0
		status.LastErr = fmt.Sprintf("%v", err)
		status.LastErrTime = time.Now()
		status.RetryCount += 1
		status.State = types.INITIAL
		writeDownloaderStatus(status, statusFilename)
		log.Printf("handleCreate failed for %s <%s>\n", status.DownloadURL, err)
		return
	}
	status.Size = uint64(info.Size())

	globalStatus.ReservedSpace -= status.ReservedSpace
	status.ReservedSpace = 0
	globalStatus.UsedSpace += uint(types.RoundupToKB(status.Size))
	updateRemainingSpace()

	log.Printf("handleCreate successful <%s> <%s>\n",
		config.DownloadURL, locFilename)
	// We do not clear any status.RetryCount, LastErr, etc. The caller
	// should look at State == DOWNLOADED to determine it is done.

	status.ModTime = time.Now()
	status.PendingAdd = false
	status.State = types.DOWNLOADED
	writeDownloaderStatus(status, statusFilename)
}

func handleDNSModify(ctxArg interface{}, statusFilename string,
	statusArg interface{}) {
	status := statusArg.(*types.DeviceNetworkStatus)

	if statusFilename != "global" {
		log.Printf("handleDNSModify: ignoring %s\n", statusFilename)
		return
	}

	log.Printf("handleDNSModify for %s\n", statusFilename)
	deviceNetworkStatus = *status
	log.Printf("handleDNSModify %d free uplinks addresses; %d any %d\n",
		types.CountLocalAddrFree(deviceNetworkStatus, ""),
		types.CountLocalAddrAny(deviceNetworkStatus, ""))
	log.Printf("handleDNSModify done for %s\n", statusFilename)
}

func handleDNSDelete(ctxArg interface{}, statusFilename string) {
	log.Printf("handleDNSDelete for %s\n", statusFilename)

	if statusFilename != "global" {
		log.Printf("handleDNSDelete: ignoring %s\n", statusFilename)
		return
	}
	deviceNetworkStatus = types.DeviceNetworkStatus{}
	log.Printf("handleDNSDelete done for %s\n", statusFilename)
}
