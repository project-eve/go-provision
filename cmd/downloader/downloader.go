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
// Check length against Content-Length.

// Should retrieve length somewhere first. Should that be in the catalogue?
// Content-Length is set!
// nordmark@bobo:~$ curl -I  https://cloud-images.ubuntu.com/releases/16.04/release/ubuntu-16.04-server-cloudimg-arm64-root.tar.gz
// HTTP/1.1 200 OK
// Date: Sat, 03 Jun 2017 04:28:38 GMT
// Server: Apache
// Last-Modified: Tue, 16 May 2017 15:31:53 GMT
// ETag: "b15553f-54fa5defeec40"
// Accept-Ranges: bytes
// Content-Length: 185947455
// Content-Type: application/x-gzip

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/zededa/api/zconfig"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"github.com/zededa/go-provision/wrap"
	"github.com/zededa/shared/libs/zedUpload"
	"io/ioutil"
	"log"
	"os"
	"time"
)

const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"

	baseDirname        = "/var/tmp/downloader"
	runDirname         = "/var/run/downloader"
	certsDirname       = "/var/tmp/zedmanager/certs"
	objDownloadDirname = "/var/tmp/zedmanager/downloads"

	downloaderConfigDirname = baseDirname + "/config"
	downloaderStatusDirname = runDirname + "/status"

	appImgBaseDirname   = baseDirname + "/" + appImgObj
	appImgRunDirname    = runDirname + "/" + appImgObj
	appImgConfigDirname = appImgBaseDirname + "/config"
	appImgStatusDirname = appImgRunDirname + "/status"

	baseOsBaseDirname   = baseDirname + "/" + baseOsObj
	baseOsRunDirname    = runDirname + "/" + baseOsObj
	baseOsConfigDirname = baseOsBaseDirname + "/config"
	baseOsStatusDirname = baseOsRunDirname + "/status"

	certObjBaseDirname   = baseDirname + "/" + certObj
	certObjRunDirname    = runDirname + "/" + certObj
	certObjConfigDirname = certObjBaseDirname + "/config"
	certObjStatusDirname = certObjRunDirname + "/status"

	appImgCatalogDirname  = objDownloadDirname + "/" + appImgObj
	appImgPendingDirname  = appImgCatalogDirname + "/pending"
	appImgVerifierDirname = appImgCatalogDirname + "/verifier"
	appImgVerifiedDirname = appImgCatalogDirname + "/verified"

	baseOsCatalogDirname  = objDownloadDirname + "/" + baseOsObj
	baseOsPendingDirname  = baseOsCatalogDirname + "/pending"
	baseOsVerifierDirname = baseOsCatalogDirname + "/verifier"
	baseOsVerifiedDirname = baseOsCatalogDirname + "/verified"

	certObjCatalogDirname  = objDownloadDirname + "/" + certObj
	certObjPendingDirname  = certObjCatalogDirname + "/pending"
	certObjVerifierDirname = certObjCatalogDirname + "/verifier"
	certObjVerifiedDirname = certObjCatalogDirname + "/verified"
)

// XXX remove global variables
var (
	dCtx *zedUpload.DronaCtx
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
	log.Printf("Starting downloader\n")
	watch.CleanupRestarted("downloader")

	downloaderInit()

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

		case change := <-appImgChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					appImgConfigDirname,
					appImgStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
				continue
			}
		case change := <-baseOsChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					baseOsConfigDirname,
					baseOsStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
				continue
			}
		case change := <-certObjChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					certObjConfigDirname,
					certObjStatusDirname,
					&types.DownloaderConfig{},
					&types.DownloaderStatus{},
					handleObjectCreate,
					handleObjectModify,
					handleObjectDelete, nil)
				continue
			}
		}
	}
}

// Object handlers
func handleObjectCreate(statusFilename string, configArg interface{}) {

	var config *types.DownloaderConfig

	switch configArg.(type) {

	default:
		log.Fatal("Can only handle DownloaderConfig")

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
		log.Fatal("Can only handle DownloaderConfig")

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

	var syncOp zedUpload.SyncOpType = zedUpload.SyncOpDownload

	// Start by marking with PendingAdd
	status := types.DownloaderStatus{
		Safename:         config.Safename,
		RefCount:         config.RefCount,
		DownloadURL:      config.DownloadURL,
		ImageSha256:      config.ImageSha256,
		DownloadObjDir:   config.DownloadObjDir,
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
	status.ReservedSpace += config.MaxSize
	globalStatus.ReservedSpace += config.MaxSize
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

	handleSyncOp(syncOp, statusFilename, config, &status)
}

// Allow to cancel by setting RefCount = 0. Same as delete? RefCount 0->1
// means download. Ignore other changes?
func handleModify(config types.DownloaderConfig,
	status types.DownloaderStatus, statusFilename string) {

	locDirname := config.DownloadObjDir
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

	locDirname := status.DownloadObjDir

	log.Printf("handleDelete(%v) for %s, %s\n",
		status.Safename, status.DownloadURL, locDirname)

	status.PendingDelete = true
	writeDownloaderStatus(&status, statusFilename)

	globalStatus.ReservedSpace -= status.ReservedSpace
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

	dirs := []string{
		baseDirname,
		runDirname,

		downloaderConfigDirname,
		downloaderStatusDirname,

		appImgBaseDirname,
		appImgRunDirname,
		appImgConfigDirname,
		appImgStatusDirname,

		certObjBaseDirname,
		certObjRunDirname,
		certObjConfigDirname,
		certObjStatusDirname,

		baseOsBaseDirname,
		baseOsRunDirname,
		baseOsConfigDirname,
		baseOsStatusDirname,

		objDownloadDirname,

		appImgCatalogDirname,
		appImgPendingDirname,
		appImgVerifierDirname,
		appImgVerifiedDirname,

		baseOsCatalogDirname,
		baseOsPendingDirname,
		baseOsVerifierDirname,
		baseOsVerifiedDirname,

		certObjCatalogDirname,
		certObjPendingDirname,
		certObjVerifierDirname,
		certObjVerifiedDirname,

		certsDirname,
	}

	workInProgressDirs := []string{
		appImgPendingDirname,
		appImgVerifierDirname,
		baseOsPendingDirname,
		baseOsVerifierDirname,
		certObjPendingDirname,
		certObjVerifierDirname,
	}

	// Remove any files which didn't make it past the verifier.
	// Though verifier owns it, remove them for calculating the
	// total available space
	for _, dir := range workInProgressDirs {
		if _, err := os.Stat(dir); err == nil {
			if err := os.Remove(dir); err != nil {
				log.Fatal(err)
			}
		}
	}

	// now create the dirs
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			if err := os.MkdirAll(dir, 0700); err != nil {
				log.Fatal(err)
			}
		}
	}

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
	totalUsed := sizeFromDir(objDownloadDirname)
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
			fmt.Printf("Dir %s size %d\n", filename, size)
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

// XXX temporary options since and wierd free.fr dns behavior with AAAA and A.
// Added  -4 --no-check-certificate
// XXX switch to curl? with --limit-rate 100k -C -
// XXX should we use --cacart? Would assume we know the root CA.
func doWget(url string, destFilename string) error {

	cmd := "wget"
	args := []string{
		"-q",
		"-c",
		"-4", // XXX due to getting IPv6 ULAs and not IPv4
		"--no-check-certificate",
		"--tries=3",
		"-O",
		destFilename,
		url,
	}

	stdoutStderr, err := wrap.Command(cmd, args...).CombinedOutput()

	if err != nil {
		log.Println("wget failed ", err)
	} else {
		log.Printf("wget done: output %s\n", string(stdoutStderr))
	}
	return err
}

// Drona APIs for object Download

func handleSyncOp(syncOp zedUpload.SyncOpType,
	statusFilename string, config types.DownloaderConfig,
	status *types.DownloaderStatus) {

	var err error
	var locFilename string

	locDirname := appImgCatalogDirname

	if config.DownloadObjDir != "" {
		locDirname = config.DownloadObjDir
	}
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

	log.Printf("Downloading <%s> to <%s>\n", config.DownloadURL, locFilename)

	switch config.TransportMethod {
	case zconfig.DsType_DsS3.String():
		{

			auth := &zedUpload.AuthInput{AuthType: "s3",
				Uname:    config.ApiKey,
				Password: config.Password}

			trType := zedUpload.SyncAwsTr
			// XXX:FIXME , will come as part of data store
			region := "us-west-2"

			// create Endpoint
			dEndPoint, err := dCtx.NewSyncerDest(trType, region,
				config.Dpath, auth)

			if err == nil && dEndPoint != nil {
				var respChan = make(chan *zedUpload.DronaRequest)

				log.Printf("syncOp for <%s>/<%s>\n", config.Dpath, filename)

				// create Request
				req := dEndPoint.NewRequest(syncOp, filename, locFilename,
					int64(config.MaxSize/1024), true, respChan)

				if req != nil {
					req.Post()
					select {
					case resp := <-respChan:
						_, err = resp.GetUpStatus()

						if resp.IsError() == false {
							err = nil
						}
					}
				}
			}
		}
		handleSyncOpResponse(config, status, statusFilename, err)
		break

	case zconfig.DsType_DsHttp.String():
	case zconfig.DsType_DsHttps.String():
	case "":
		err = doWget(config.DownloadURL, locFilename)
		handleSyncOpResponse(config, status, statusFilename, err)
	default:
		log.Fatal("unsupported transport method")
	}
}

func handleSyncOpResponse(config types.DownloaderConfig,
	status *types.DownloaderStatus, statusFilename string,
	err error) {

	locDirname := appImgCatalogDirname
	if config.DownloadObjDir != "" {
		locDirname = config.DownloadObjDir
	}

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
	globalStatus.ReservedSpace -= config.MaxSize
	status.ReservedSpace -= config.MaxSize

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
