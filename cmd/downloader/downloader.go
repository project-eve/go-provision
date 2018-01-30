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
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"github.com/zededa/go-provision/wrap"
	"github.com/zededa/shared/libs/zedUpload"
	"io/ioutil"
	"log"
	"net"
	"os"
	"time"
)

const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"

	moduleName            = "downloader"
	zedBaseDirname        = "/var/tmp"
	zedRunDirname         = "/var/run"
	baseDirname           = zedBaseDirname + "/" + moduleName
	runDirname            = zedRunDirname + "/" + moduleName
	certsDirname          = "/var/tmp/zedmanager/certs"
	objectDownloadDirname = "/var/tmp/zedmanager/downloads"

	downloaderConfigDirname = baseDirname + "/config"
	downloaderStatusDirname = runDirname + "/status"

	appImgConfigDirname = baseDirname + "/" + appImgObj + "/config"
	appImgStatusDirname = runDirname + "/" + appImgObj + "/status"

	baseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	baseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"

	certObjConfigDirname = baseDirname + "/" + certObj + "/config"
	certObjStatusDirname = runDirname + "/" + certObj + "/status"
	DNSDirname           = "/var/run/zedrouter/DeviceNetworkStatus"
)

// XXX remove global variables
var (
	dCtx *zedUpload.DronaCtx
)

// Set from Makefile
var Version = "No version specified"

var deviceNetworkStatus types.DeviceNetworkStatus

func main() {

	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	versionPtr := flag.Bool("v", false, "Version")
	flag.Parse()
	if *versionPtr {
		log.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	log.Printf("Starting downloader\n")
	watch.CleanupRestarted("downloader")

	downloaderInit()

	deviceStatusChanges := make(chan string)
	go watch.WatchStatus(DNSDirname, deviceStatusChanges)

	// First wait to have some free uplinks
	for types.CountLocalAddrFree(deviceNetworkStatus, "") == 0 {
		select {
		case change := <-deviceStatusChanges:
			watch.HandleStatusEvent(change,
				DNSDirname,
				&types.DeviceNetworkStatus{},
				handleDNSModify, handleDNSDelete,
				nil)
		}
	}
	fmt.Printf("Have %d free uplinks addresses to use\n",
		types.CountLocalAddrFree(deviceNetworkStatus, ""))

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

		case change := <-deviceStatusChanges:
			{
				watch.HandleStatusEvent(change,
					DNSDirname,
					&types.DeviceNetworkStatus{},
					handleDNSModify, handleDNSDelete,
					nil)
			}

		case change := <-appImgChanges:
			{
				watch.HandleConfigStatusEvent(change,
					appImgConfigDirname,
					appImgStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
			}

		case change := <-baseOsChanges:
			{
				watch.HandleConfigStatusEvent(change,
					baseOsConfigDirname,
					baseOsStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
			}
		case change := <-certObjChanges:
			{
				watch.HandleConfigStatusEvent(change,
					certObjConfigDirname,
					certObjStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
			}
		}
	}
}

// Object handlers
func handleObjectCreate(statusFilename string, configArg interface{}) {

	var config *types.DownloaderConfig

	switch configArg.(type) {

	default:
		log.Fatal("handleCreate: Can only handle DownloaderConfig")

	case *types.DownloaderConfig:
		config = configArg.(*types.DownloaderConfig)
	}

	handleCreate(*config, statusFilename)
}

func handleObjectModify(statusFilename string, configArg interface{},
	statusArg interface{}) {

	var config *types.DownloaderConfig
	var status *types.DownloaderStatus

	switch configArg.(type) {

	default:
		log.Fatal("handleModify: Can only handle DownloaderConfig")

	case *types.DownloaderConfig:
		config = configArg.(*types.DownloaderConfig)
	}

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle DownloaderStatus")
	case *types.DownloaderStatus:
		status = statusArg.(*types.DownloaderStatus)
	}

	handleModify(*config, *status, statusFilename)
}

func handleObjectDelete(statusFilename string, statusArg interface{}) {

	var status *types.DownloaderStatus

	switch statusArg.(type) {

	default:
		log.Fatal("Can only handle DownloaderStatus")

	case *types.DownloaderStatus:
		status = statusArg.(*types.DownloaderStatus)
	}

	handleDelete(*status, statusFilename)
}

func handleCreate(config types.DownloaderConfig, statusFilename string) {

	log.Printf("handleCreate: %s \n", config.DownloadURL)

	if isOk := validateDownloadStatusCreate(config, statusFilename); isOk != true {
		log.Printf("handleCreate: status file create aborted\n", config.DownloadURL)
		return
	}

	// Start by marking with PendingAdd
	status := types.DownloaderStatus{
		Safename:         config.Safename,
		RefCount:         config.RefCount,
		DownloadURL:      config.DownloadURL,
		UseFreeUplinks:   config.UseFreeUplinks,
		ImageSha256:      config.ImageSha256,
		FinalObjDir:      config.FinalObjDir,
		ObjType:          config.ObjType,
		NeedVerification: config.NeedVerification,
		PendingAdd:       true,
	}
	writeDownloaderStatus(&status, statusFilename)

	// Check if we have space
	if config.MaxSize >= globalStatus.RemainingSpace {
		errString := fmt.Sprintf("Would exceed remaining space %d vs %d\n",
			config.MaxSize, globalStatus.RemainingSpace)
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
	status.ReservedSpace = config.MaxSize
	globalStatus.ReservedSpace = config.MaxSize
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

	var syncOp zedUpload.SyncOpType = zedUpload.SyncOpDownload
	handleSyncOp(syncOp, statusFilename, config, &status)
}

// Allow to cancel by setting RefCount = 0. Same as delete? RefCount 0->1
// means download. Ignore other changes?
func handleModify(config types.DownloaderConfig,
	status types.DownloaderStatus, statusFilename string) {

	locDirname := objectDownloadDirname + "/" + config.ObjType
	log.Printf("handleModify(%v) for %s\n",
		config.Safename, config.DownloadURL)

	if config.DownloadURL != status.DownloadURL {
		log.Printf("URL changed - not allowed %s -> %s\n",
			config.DownloadURL, status.DownloadURL)
		return
	}
	// If the sha changes, we treat it as a delete and recreate.
	// Ditto if we had a failure.
	if (status.ImageSha256 != "" &&
		status.ImageSha256 != config.ImageSha256) ||
		status.LastErr != "" {
		reason := ""
		if status.ImageSha256 != config.ImageSha256 {
			reason = "sha256 changed"
		} else {
			reason = "recovering from previous error"
		}
		log.Printf("handleModify: %s for %s\n",
			reason, config.DownloadURL)
		doDelete(statusFilename, locDirname, &status)
		handleCreate(config, statusFilename)
		log.Printf("handleModify done for %s\n", config.DownloadURL)
		return
	}

	// XXX do work; look for refcnt -> 0 and delete; cancel any running
	// download
	// If RefCount from zero to non-zero then do install
	if status.RefCount == 0 && config.RefCount != 0 {

		log.Printf("handleModify installing %s\n", config.DownloadURL)
		handleCreate(config, statusFilename)
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

func doDelete(statusFilename string, locDirname string,
	status *types.DownloaderStatus) {

	log.Printf("doDelete(%v) for %s\n", status.Safename, status.DownloadURL)

	// Delete the installed object
	// XXX:FIXME, whether we really need to delete
	// the installed object
	if status.FinalObjDir != "" {
		locFilename := status.FinalObjDir
		if _, err := os.Stat(locFilename); err == nil {
			locFilename := locFilename + "/" +
				types.SafenameToFilename(status.Safename)
			log.Printf("Deleting %s\n", locFilename)
			if err := os.Remove(locFilename); err != nil {
				log.Printf("Failed to remove %s: err %s\n",
					locFilename, err)
			}
		}
	}

	// XXX:FIXME, delete from verifier/verified !!
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
	globalStatus.UsedSpace -= status.Size
	status.Size = 0

	// XXX Asymmetric; handleCreate reserved on RefCount 0. We unreserve
	// going back to RefCount 0. FIXed
	updateRemainingSpace()
	writeDownloaderStatus(status, statusFilename)
}

func handleDelete(status types.DownloaderStatus, statusFilename string) {

	locDirname := objectDownloadDirname + "/" + status.ObjType

	log.Printf("handleDelete(%v) for %s, %s\n",
		status.Safename, status.DownloadURL, locDirname)

	status.PendingDelete = true
	writeDownloaderStatus(&status, statusFilename)

	globalStatus.ReservedSpace = 0
	globalStatus.UsedSpace -= status.Size
	updateRemainingSpace()

	status.ReservedSpace = 0
	status.Size = 0
	writeDownloaderStatus(&status, statusFilename)

	doDelete(statusFilename, locDirname, &status)

	status.PendingDelete = false
	writeDownloaderStatus(&status, statusFilename)

	// Write out what we modified to DownloaderStatus aka delete
	if err := os.Remove(statusFilename); err != nil {
		log.Println(err)
	}
	log.Printf("handleDelete done for %s, %s\n",
		status.DownloadURL, locDirname)
}

// helper functions

var globalConfig types.GlobalDownloadConfig
var globalStatus types.GlobalDownloadStatus
var globalStatusFilename string

func downloaderInit() {

	initializeDirs()

	// now start
	configFilename := downloaderConfigDirname + "/global"
	statusFilename := downloaderStatusDirname + "/global"

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
	// We read /var/tmp/zedmanager/downloads/* and determine how much space
	// is used. Place in GlobalDownloadStatus. Calculate remaining space.
	totalUsed := sizeFromDir(objectDownloadDirname)
	globalStatus.UsedSpace = uint((totalUsed + 1023) / 1024)
	updateRemainingSpace()

	// create drona interface
	ctx, err := zedUpload.NewDronaCtx("zdownloader", 0)

	if ctx == nil {
		log.Printf("context create fail %s\n", err)
		log.Fatal(err)
	}

	dCtx = ctx
}

func initializeDirs() {

	objTypes := []string{appImgObj, baseOsObj, certObj}

	if _, err := os.Stat(certsDirname); err != nil {
		if err := os.MkdirAll(certsDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	// Remove any files which didn't make it past the verifier.
	// Though verifier owns it, remove them for calculating the
	// total available space
	types.ClearInProgressDownloadDirs(objTypes)

	// create the object based config/status dirs
	watch.CreateConfigStatusDirs(moduleName, objTypes)

	// create the object download directories
	types.CreateDownloadDirs(objTypes)
}

func sizeFromDir(dirname string) int64 {
	var totalUsed int64 = 0
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
			totalUsed += location.Size()
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

// XXX should we use --cacert? Would assume we know the root CA.
// XXX Set --limit-rate 100k
// XXX continue based on filesize with: -C -
// XXX --max-filesize <bytes> from MaxSize in DownLoaderConfig (kbytes)
// XXX --interface ...
// Note that support for --dns-interface is not compiled in
// Normally "ifname" is the source IP to be consistent with the S3 loop
func doCurl(url string, ifname string, destFilename string) error {
	cmd := "curl"
	args := []string{}
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

func doS3(syncOp zedUpload.SyncOpType,
	apiKey string, password string, dpath string, maxsize uint,
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
	dEndPoint, err := dCtx.NewSyncerDest(trType, region, dpath, auth)
	if err != nil {
		fmt.Printf("NewSyncerDest failed: %s\n", err)
		return err
	}
	dEndPoint.WithSrcIpSelection(ipSrc)
	var respChan = make(chan *zedUpload.DronaRequest)

	log.Printf("syncOp for <%s>/<%s>\n", dpath, filename)
	// create Request
	req := dEndPoint.NewRequest(syncOp, filename, locFilename,
		int64(maxsize/1024), true, respChan)
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

func handleSyncOp(syncOp zedUpload.SyncOpType,
	statusFilename string, config types.DownloaderConfig,
	status *types.DownloaderStatus) {

	var err error

	locDirname := objectDownloadDirname + "/" + config.ObjType
	locFilename := locDirname + "/pending"

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
		fmt.Printf("Have %d free uplink addresses\n", addrCount)
	} else {
		addrCount = types.CountLocalAddrAny(deviceNetworkStatus, "")
		fmt.Printf("Have %d any uplink addresses\n", addrCount)
	}

	// Loop through all interfaces until a success
	for addrIndex := 0; addrIndex < addrCount; addrIndex += 1 {
		var ipSrc net.IP
		if config.UseFreeUplinks {
			ipSrc, err = types.GetLocalAddrFree(deviceNetworkStatus,
				addrIndex, "")
		} else {
			ipSrc, err = types.GetLocalAddrAny(deviceNetworkStatus,
				addrIndex, "")
		}
		if err != nil {
			fmt.Printf("GetLocalAddr failed: %s\n", err)
			continue
		}
		fmt.Printf("Using IP source %v transport %v\n", ipSrc,
			config.TransportMethod)

		switch config.TransportMethod {
		case zconfig.DsType_DsS3.String():
			err = doS3(syncOp, config.ApiKey, config.Password, config.Dpath,
				config.MaxSize, ipSrc, filename, locFilename)
			if err != nil {
				fmt.Printf("Source IP %s failed: %s\n",
					ipSrc.String(), err)
			} else {
				handleSyncOpResponse(config, status,
					statusFilename, err)
				return
			}
		case zconfig.DsType_DsHttp.String():
		case zconfig.DsType_DsHttps.String():
		case "":
			err = doCurl(config.DownloadURL, ipSrc.String(),
				locFilename)
			if err != nil {
				fmt.Printf("Source IP %s failed: %s\n",
					ipSrc.String(), err)
			} else {
				handleSyncOpResponse(config, status,
					statusFilename, err)
				return
			}
		default:
			log.Fatal("unsupported transport method")
		}
	}
	fmt.Printf("All source IP addresses failed. Last %s\n", err)
	handleSyncOpResponse(config, status, statusFilename, err)
}

func handleSyncOpResponse(config types.DownloaderConfig,
	status *types.DownloaderStatus, statusFilename string,
	err error) {

	locDirname := objectDownloadDirname + "/" + config.ObjType

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

	locFilename := locDirname + "/pending"

	if status.ImageSha256 != "" {
		locFilename = locFilename + "/" + status.ImageSha256
	}

	locFilename = locFilename + "/" + config.Safename

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

	// firstly, reset the reserved space
	globalStatus.ReservedSpace = 0
	status.ReservedSpace = 0
	updateRemainingSpace()

	status.Size = uint((info.Size() + 1023) / 1024)

	// XXX Compare against MaxSize and reject? Already wasted the space?
	// XXX:FIXME, if config.MaxSize is not set, assumption is,
	// we will download the object anyhow, and skip size check

	if (config.MaxSize != 0) && (status.Size > config.MaxSize) {
		errString := fmt.Sprintf("Size exceeds MaxSize; %d vs. %d for %s\n",
			status.Size, config.MaxSize, status.DownloadURL)
		log.Println(errString)
		// Delete file
		doDelete(statusFilename, locDirname, status)
		status.PendingAdd = false
		status.Size = 0
		status.LastErr = errString
		status.LastErrTime = time.Now()
		status.RetryCount += 1
		status.State = types.INITIAL
		writeDownloaderStatus(status, statusFilename)
		log.Printf("handleCreate: failed for %s, <%s>\n",
			status.DownloadURL, err)
		return
	}

	// update the used space
	globalStatus.UsedSpace += status.Size
	updateRemainingSpace()

	log.Printf("handleCreate: successful <%s> <%s>\n",
		config.DownloadURL, locFilename)
	// We do not clear any status.RetryCount, LastErr, etc. The caller
	// should look at State == DOWNLOADED to determine it is done.

	status.ModTime = time.Now()
	status.PendingAdd = false
	status.State = types.DOWNLOADED
	writeDownloaderStatus(status, statusFilename)
}

func validateDownloadStatusCreate(config types.DownloaderConfig, statusFilename string) bool {

	var isOk bool = true
	if _, err := os.Stat(statusFilename); err == nil {

		log.Printf("handleCreate: %s exists\n", statusFilename)

		cb, err := ioutil.ReadFile(statusFilename)
		if err != nil {
			log.Printf("handleCreate: %s for %s\n", err, statusFilename)
			isOk = false
		} else {

			var status types.DownloaderStatus

			if err := json.Unmarshal(cb, &status); err != nil {
				log.Printf("handleCreate: %s downloadStatus file: %s\n",
					err, statusFilename)
				isOk = false
			} else {
				if status.State >= types.DOWNLOAD_STARTED {
					log.Printf("handleCreate: download status %v \n", status.State)
					isOk = false
				}
			}
		}
	}
	return isOk
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
	deviceNetworkStatus = *status
	fmt.Printf("handleDNSModify %d free uplinks addresses to use\n",
		types.CountLocalAddrFree(deviceNetworkStatus, ""))
	log.Printf("handleDNSModify done for %s\n", statusFilename)
}

func handleDNSDelete(statusFilename string) {
	log.Printf("handleDNSDelete for %s\n", statusFilename)

	if statusFilename != "global" {
		fmt.Printf("handleDNSDelete: ignoring %s\n", statusFilename)
		return
	}
	deviceNetworkStatus = types.DeviceNetworkStatus{}
	log.Printf("handleDNSDelete done for %s\n", statusFilename)
}
