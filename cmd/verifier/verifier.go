// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

// Process input in the form of collections of VerifyImageConfig structs
// and publish the results as collections of VerifyImageStatus structs.
// There are several inputs and outputs based on the objType.
// Process input changes from a config directory containing json encoded files
// with VerifyImageConfig and compare against VerifyImageStatus in the status
// dir.
//
// Move the file from objectDownloadDirname/pending/<claimedsha>/<safename> to
// to objectDownloadDirname/verifier/<claimedsha>/<safename> and make RO,
// then attempt to verify sum.
// Once sum is verified, move to objectDownloadDirname/verified/<sha>/<filename>// where the filename is the last part of the URL (after the last '/')
// Note that different URLs for same file will download to the same <sha>
// directory. We delete duplicates assuming the file content will be the same.

package verifier

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/pidfile"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"strings"
	"time"
)

const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	agentName = "verifier"

	persistDir            = "/persist"
	objectDownloadDirname = persistDir + "/downloads"

	rootCertDirname    = "/config"
	rootCertFileName   = rootCertDirname + "/root-certificate.pem"
	certificateDirname = persistDir + "/certs"

	// If this file is present we don't delete verified files in handleDelete
	tmpDirname       = "/var/tmp/zededa"
	preserveFilename = tmpDirname + "/preserve"
)

// Go doesn't like this as a constant
var (
	verifierObjTypes = []string{appImgObj, baseOsObj}
)

// Set from Makefile
var Version = "No version specified"

// Any state used by handlers goes here
type verifierContext struct {
	subAppImgConfig *pubsub.Subscription
	pubAppImgStatus *pubsub.Publication
	subBaseOsConfig *pubsub.Subscription
	pubBaseOsStatus *pubsub.Publication
	subGlobalConfig *pubsub.Subscription
}

var debug = false
var debugOverride bool                                // From command line arg
var downloadGCTime = time.Duration(600) * time.Second // Unless from GlobalConfig

func Run() {
	handlersInit()
	logf, err := agentlog.Init(agentName)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()

	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	flag.Parse()
	debug = *debugPtr
	debugOverride = debug
	if debugOverride {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
		log.Fatal(err)
	}
	log.Infof("Starting %s\n", agentName)

	// create the directories
	initializeDirs()

	// Any state needed by handler functions
	ctx := verifierContext{}

	// Set up our publications before the subscriptions so ctx is set
	pubAppImgStatus, err := pubsub.PublishScope(agentName, appImgObj,
		types.VerifyImageStatus{})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubAppImgStatus = pubAppImgStatus
	pubAppImgStatus.ClearRestarted()

	pubBaseOsStatus, err := pubsub.PublishScope(agentName, baseOsObj,
		types.VerifyImageStatus{})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubBaseOsStatus = pubBaseOsStatus
	pubBaseOsStatus.ClearRestarted()

	// Look for global config such as log levels
	subGlobalConfig, err := pubsub.Subscribe("", types.GlobalConfig{},
		false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subGlobalConfig.ModifyHandler = handleGlobalConfigModify
	subGlobalConfig.DeleteHandler = handleGlobalConfigDelete
	ctx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	subAppImgConfig, err := pubsub.SubscribeScope("zedmanager",
		appImgObj, types.VerifyImageConfig{}, false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subAppImgConfig.ModifyHandler = handleAppImgModify
	subAppImgConfig.DeleteHandler = handleAppImgDelete
	ctx.subAppImgConfig = subAppImgConfig
	subAppImgConfig.Activate()

	subBaseOsConfig, err := pubsub.SubscribeScope("baseosmgr",
		baseOsObj, types.VerifyImageConfig{}, false, &ctx)
	if err != nil {
		log.Fatal(err)
	}
	subBaseOsConfig.ModifyHandler = handleBaseOsModify
	subBaseOsConfig.DeleteHandler = handleBaseOsDelete
	ctx.subBaseOsConfig = subBaseOsConfig
	subBaseOsConfig.Activate()

	handleInit(&ctx)

	// Report to zedmanager that init is done
	pubAppImgStatus.SignalRestarted()
	pubBaseOsStatus.SignalRestarted()

	// We will cleanup zero RefCount objects after a while
	// We run timer 10 times more often than the limit on LastUse
	gc := time.NewTicker(downloadGCTime / 10)

	for {
		select {
		case change := <-subGlobalConfig.C:
			subGlobalConfig.ProcessChange(change)

		case change := <-subAppImgConfig.C:
			subAppImgConfig.ProcessChange(change)

		case change := <-subBaseOsConfig.C:
			subBaseOsConfig.ProcessChange(change)

		case <-gc.C:
			gcVerifiedObjects(&ctx)
		}
	}
}

func handleInit(ctx *verifierContext) {

	log.Infoln("handleInit")

	// mark all status file to PendingDelete
	handleInitWorkinProgressObjects(ctx)

	// recreate status files for verified objects
	handleInitVerifiedObjects(ctx)

	// delete status files marked PendingDelete
	handleInitMarkedDeletePendingObjects(ctx)

	log.Infoln("handleInit done")
}

func initializeDirs() {
	// first the certs directory
	if _, err := os.Stat(certificateDirname); err != nil {
		log.Debugf("Create %s\n", certificateDirname)
		if err := os.MkdirAll(certificateDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	// Remove any files which didn't make it past the verifier.
	// useful for calculating total available space in
	// downloader context
	// XXX when does downloader calculate space?
	clearInProgressDownloadDirs(verifierObjTypes)

	// create the object download directories
	createDownloadDirs(verifierObjTypes)
}

// Mark all existing Status as PendingDelete.
// If they correspond to verified files then in the handleInitVerifiedObjects
// function PendingDelete will be reset. Finally, in
// handleInitMarkedDeletePendingObjects we will delete anything which still
// has PendingDelete set.
func handleInitWorkinProgressObjects(ctx *verifierContext) {

	publications := []*pubsub.Publication{
		ctx.pubAppImgStatus,
		ctx.pubBaseOsStatus,
	}
	for _, pub := range publications {
		items := pub.GetAll()
		for key, st := range items {
			status := cast.CastVerifyImageStatus(st)
			if status.Key() != key {
				log.Errorf("handleInitWorkin key/UUID mismatch %s vs %s; ignored %+v\n",
					key, status.Key(), status)
				continue
			}
			log.Debugf("Marking with PendingDelete: %s\n", key)
			status.PendingDelete = true
			publishVerifyImageStatus(ctx, &status)
		}
	}
}

// recreate status files for verified objects
func handleInitVerifiedObjects(ctx *verifierContext) {
	for _, objType := range verifierObjTypes {

		verifiedDirname := objectDownloadDirname + "/" + objType + "/verified"
		if _, err := os.Stat(verifiedDirname); err == nil {
			populateInitialStatusFromVerified(ctx, objType,
				verifiedDirname, "")
		}
	}
}

// recursive scanning for verified objects,
// to recreate the status files
func populateInitialStatusFromVerified(ctx *verifierContext,
	objType string, objDirname string, parentDirname string) {

	log.Infof("populateInitialStatusFromVerified(%s, %s)\n", objDirname,
		parentDirname)

	locations, err := ioutil.ReadDir(objDirname)

	if err != nil {
		log.Fatal(err)
	}

	for _, location := range locations {

		filename := objDirname + "/" + location.Name()

		if location.IsDir() {
			log.Debugf("populateInitialStatusFromVerified: Looking in %s\n", filename)
			if _, err := os.Stat(filename); err == nil {
				populateInitialStatusFromVerified(ctx,
					objType, filename, location.Name())
			}
		} else {
			info, _ := os.Stat(filename)
			log.Debugf("populateInitialStatusFromVerified: Processing %d Mbytes %s \n",
				info.Size()/(1024*1024), filename)

			sha := parentDirname
			// We don't know the URL; Pick a name which is unique
			safename := location.Name() + "." + sha

			status := types.VerifyImageStatus{
				Safename:    safename,
				ObjType:     objType,
				ImageSha256: sha,
				State:       types.DELIVERED,
				Size:        info.Size(),
				RefCount:    0,
				LastUse:     time.Now(),
			}

			// We re-verify the sha on reboot/restart
			// XXX what about signature? Do we have the certs?
			imageHash, err := computeShaFile(filename)
			if err != nil {
				log.Errorf("computeShaFile %s failed %s\n",
					filename, err)
				doDelete(&status)
				continue
			}

			got := fmt.Sprintf("%x", imageHash)
			if got != strings.ToLower(sha) {
				log.Errorf("computed   %s\n", got)
				log.Errorf("configured %s\n",
					strings.ToLower(sha))
				doDelete(&status)
				continue
			}

			// Passed sha verification
			publishVerifyImageStatus(ctx, &status)
		}
	}
}

// remove the status files marked as pending delete
func handleInitMarkedDeletePendingObjects(ctx *verifierContext) {
	publications := []*pubsub.Publication{
		ctx.pubAppImgStatus,
		ctx.pubBaseOsStatus,
	}
	for _, pub := range publications {
		items := pub.GetAll()
		for key, st := range items {
			status := cast.CastVerifyImageStatus(st)
			if status.Key() != key {
				log.Errorf("handleInitMarked key/UUID mismatch %s vs %s; ignored %+v\n",
					key, status.Key(), status)
				continue
			}
			if status.PendingDelete {
				log.Infof("still PendingDelete; delete %s\n",
					key)
				unpublishVerifyImageStatus(ctx, &status)
			}
		}
	}
}

// Create the object download directories we own
func createDownloadDirs(objTypes []string) {

	workingDirTypes := []string{"verifier", "verified"}

	// now create the download dirs
	for _, objType := range objTypes {
		for _, dirType := range workingDirTypes {
			dirName := objectDownloadDirname + "/" + objType + "/" + dirType
			if _, err := os.Stat(dirName); err != nil {
				log.Debugf("Create %s\n", dirName)
				if err := os.MkdirAll(dirName, 0700); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

// clear in-progress object download directories
func clearInProgressDownloadDirs(objTypes []string) {

	inProgressDirTypes := []string{"verifier"}

	// Now remove the in-progress dirs
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

// If an object has a zero RefCount and dropped to zero more than
// downloadGCTime ago, then we delete the Status. That will result in the
// user (zedmanager or baseosmgr) deleting the Config, unless a RefCount
// increase is underway.
// XXX Note that this runs concurrently with the handler.
func gcVerifiedObjects(ctx *verifierContext) {
	log.Debugf("gcVerifiedObjects()\n")
	publications := []*pubsub.Publication{
		ctx.pubAppImgStatus,
		ctx.pubBaseOsStatus,
	}
	for _, pub := range publications {
		items := pub.GetAll()
		for key, st := range items {
			status := cast.CastVerifyImageStatus(st)
			if status.Key() != key {
				log.Errorf("gcVerifiedObjects key/UUID mismatch %s vs %s; ignored %+v\n",
					key, status.Key(), status)
				continue
			}
			if status.RefCount != 0 {
				log.Debugf("gcVerifiedObjects: skipping RefCount %d: %s\n",
					status.RefCount, key)
				continue
			}
			timePassed := time.Since(status.LastUse)
			if timePassed < downloadGCTime {
				log.Debugf("gcverifiedObjects: skipping recently used %s remains %d seconds\n",
					key,
					(timePassed-downloadGCTime)/time.Second)
				continue
			}
			log.Infof("gcVerifiedObjects: expiring status for %s; LastUse %v now %v\n",
				key, status.LastUse, time.Now())
			status.Expired = true
			publishVerifyImageStatus(ctx, &status)
		}
	}
}

func updateVerifyErrStatus(ctx *verifierContext,
	status *types.VerifyImageStatus, lastErr string) {

	status.LastErr = lastErr
	status.LastErrTime = time.Now()
	status.PendingAdd = false
	publishVerifyImageStatus(ctx, status)
}

func publishVerifyImageStatus(ctx *verifierContext,
	status *types.VerifyImageStatus) {

	log.Debugf("publishVerifyImageStatus(%s, %s)\n",
		status.ObjType, status.Safename)

	pub := verifierPublication(ctx, status.ObjType)
	key := status.Key()
	pub.Publish(key, status)
}

func unpublishVerifyImageStatus(ctx *verifierContext,
	status *types.VerifyImageStatus) {

	log.Debugf("publishVerifyImageStatus(%s, %s)\n",
		status.ObjType, status.Safename)

	pub := verifierPublication(ctx, status.ObjType)
	key := status.Key()
	st, _ := pub.Get(key)
	if st == nil {
		log.Errorf("unpublishVerifyImageStatus(%s) not found\n", key)
		return
	}
	pub.Unpublish(key)
}

func verifierPublication(ctx *verifierContext, objType string) *pubsub.Publication {
	var pub *pubsub.Publication
	switch objType {
	case appImgObj:
		pub = ctx.pubAppImgStatus
	case baseOsObj:
		pub = ctx.pubBaseOsStatus
	default:
		log.Fatalf("verifierPublication: Unknown ObjType %s\n",
			objType)
	}
	return pub
}

// Wrappers to add objType for create. The Delete wrappers are merely
// for function name consistency
func handleAppImgModify(ctxArg interface{}, key string,
	configArg interface{}) {

	handleVerifyImageModify(ctxArg, appImgObj, key, configArg)
}

func handleAppImgDelete(ctxArg interface{}, key string, configArg interface{}) {
	handleVerifyImageDelete(ctxArg, key, configArg)
}

func handleBaseOsModify(ctxArg interface{}, key string,
	configArg interface{}) {

	handleVerifyImageModify(ctxArg, baseOsObj, key, configArg)
}

func handleBaseOsDelete(ctxArg interface{}, key string, configArg interface{}) {
	handleVerifyImageDelete(ctxArg, key, configArg)
}

// Callers must be careful to publish any changes to VerifyImageStatus
func lookupVerifyImageStatus(ctx *verifierContext, objType string,
	key string) *types.VerifyImageStatus {

	pub := verifierPublication(ctx, objType)
	st, _ := pub.Get(key)
	if st == nil {
		log.Infof("lookupVerifyImageStatus(%s) not found\n", key)
		return nil
	}
	status := cast.CastVerifyImageStatus(st)
	if status.Key() != key {
		log.Errorf("lookupVerifyImageStatus(%s) got %s; ignored %+v\n",
			key, status.Key(), status)
		return nil
	}
	return &status
}

// We have one goroutine per provisioned domU object.
// Channel is used to send config (new and updates)
// Channel is closed when the object is deleted
// The go-routine owns writing status for the object
// The key in the map is the objects Key()
type handlers map[string]chan<- interface{}

var handlerMap handlers

func handlersInit() {
	handlerMap = make(handlers)
}

// Wrappers around handleCreate, handleModify, and handleDelete

// Determine whether it is an create or modify
func handleVerifyImageModify(ctxArg interface{}, objType string,
	key string, configArg interface{}) {

	log.Infof("handleVerifyImageModify(%s)\n", key)
	ctx := ctxArg.(*verifierContext)
	config := cast.CastVerifyImageConfig(configArg)
	if config.Key() != key {
		log.Errorf("handleVerifyImageModify key/UUID mismatch %s vs %s; ignored %+v\n",
			key, config.Key(), config)
		return
	}
	// Do we have a channel/goroutine?
	h, ok := handlerMap[config.Key()]
	if !ok {
		h1 := make(chan interface{})
		handlerMap[config.Key()] = h1
		go runHandler(ctx, objType, key, h1)
		h = h1
	}
	log.Debugf("Sending config to handler\n")
	h <- configArg
	log.Infof("handleVerifyImageModify(%s) done\n", key)
}

func handleVerifyImageDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Infof("handleVerifyImageDelete(%s)\n", key)
	// Do we have a channel/goroutine?
	h, ok := handlerMap[key]
	if ok {
		log.Debugf("Closing channel\n")
		close(h)
		delete(handlerMap, key)
	} else {
		log.Debugf("handleVerifyImageDelete: unknown %s\n", key)
		return
	}
	log.Infof("handleVerifyImageDelete(%s) done\n", key)
}

// Server for each domU
func runHandler(ctx *verifierContext, objType string, key string,
	c <-chan interface{}) {

	log.Infof("runHandler starting\n")

	closed := false
	for !closed {
		select {
		case configArg, ok := <-c:
			if ok {
				config := cast.CastVerifyImageConfig(configArg)
				status := lookupVerifyImageStatus(ctx,
					objType, key)
				if status == nil {
					handleCreate(ctx, objType, &config)
				} else {
					handleModify(ctx, &config, status)
				}
			} else {
				// Closed
				status := lookupVerifyImageStatus(ctx,
					objType, key)
				if status != nil {
					handleDelete(ctx, status)
				}
				closed = true
			}
		}
	}
	log.Infof("runHandler(%s) DONE\n", key)
}

func handleCreate(ctx *verifierContext, objType string,
	config *types.VerifyImageConfig) {

	log.Infof("handleCreate(%v) objType %s for %s\n",
		config.Safename, objType, config.Name)
	if objType == "" {
		log.Fatalf("handleCreate: No ObjType for %s\n",
			config.Safename)
	}

	status := types.VerifyImageStatus{
		Safename:    config.Safename,
		ObjType:     objType,
		ImageSha256: config.ImageSha256,
		PendingAdd:  true,
		State:       types.DOWNLOADED,
		RefCount:    config.RefCount,
		LastUse:     time.Now(),
	}
	publishVerifyImageStatus(ctx, &status)

	ok, size := markObjectAsVerifying(ctx, config, &status)
	if !ok {
		log.Errorf("handleCreate fail for %s\n", config.Name)
		return
	}
	status.Size = size
	publishVerifyImageStatus(ctx, &status)

	if !verifyObjectSha(ctx, config, &status) {
		log.Errorf("handleCreate fail for %s\n", config.Name)
		return
	}
	publishVerifyImageStatus(ctx, &status)

	markObjectAsVerified(ctx, config, &status)
	status.PendingAdd = false
	status.State = types.DELIVERED
	publishVerifyImageStatus(ctx, &status)
	log.Infof("handleCreate done for %s\n", config.Name)
}

// Returns ok, size of object
func markObjectAsVerifying(ctx *verifierContext,
	config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) (bool, int64) {

	// Form the unique filename in
	// objectDownloadDirname/<objType>/pending/
	// based on the claimed Sha256 and safename, and the same name
	// in objectDownloadDirname/<objType>/verifier/. Form a shorter name for
	// objectDownloadDirname/<objType/>verified/.

	objType := status.ObjType
	downloadDirname := objectDownloadDirname + "/" + objType
	pendingDirname := downloadDirname + "/pending/" + status.ImageSha256
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256

	pendingFilename := pendingDirname + "/" + config.Safename
	verifierFilename := verifierDirname + "/" + config.Safename

	// Move to verifier directory which is RO
	// XXX should have dom0 do this and/or have RO mounts
	log.Infof("Move from %s to %s\n", pendingFilename, verifierFilename)

	info, err := os.Stat(pendingFilename)
	if err != nil {
		// XXX hits sometimes; attempting to verify before download
		// is complete?
		log.Errorf("markObjectAsVerifying failed %s\n", err)
		cerr := fmt.Sprintf("%v", err)
		updateVerifyErrStatus(ctx, status, cerr)
		log.Errorf("handleCreate failed for %s\n", config.Name)
		return false, 0
	}

	if _, err := os.Stat(verifierFilename); err == nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(verifierDirname); err == nil {
		if err := os.RemoveAll(verifierDirname); err != nil {
			log.Fatal(err)
		}
	}
	log.Debugf("Create %s\n", verifierDirname)
	if err := os.MkdirAll(verifierDirname, 0700); err != nil {
		log.Fatal(err)
	}

	if err := os.Rename(pendingFilename, verifierFilename); err != nil {
		log.Fatal(err)
	}

	if err := os.Chmod(verifierDirname, 0500); err != nil {
		log.Fatal(err)
	}

	if err := os.Chmod(verifierFilename, 0400); err != nil {
		log.Fatal(err)
	}

	// Clean up empty directory
	if err := os.RemoveAll(pendingDirname); err != nil {
		log.Fatal(err)
	}
	return true, info.Size()
}

func verifyObjectSha(ctx *verifierContext, config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) bool {

	objType := status.ObjType
	downloadDirname := objectDownloadDirname + "/" + objType
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifierFilename := verifierDirname + "/" + config.Safename

	log.Infof("Verifying URL %s file %s\n",
		config.Name, verifierFilename)

	imageHash, err := computeShaFile(verifierFilename)
	if err != nil {
		cerr := fmt.Sprintf("%v", err)
		updateVerifyErrStatus(ctx, status, cerr)
		log.Errorf("verifyObjectSha %s failed %s\n",
			config.Name, cerr)
		return false
	}

	got := fmt.Sprintf("%x", imageHash)
	if got != strings.ToLower(config.ImageSha256) {
		log.Errorf("computed   %s\n", got)
		log.Errorf("configured %s\n", strings.ToLower(config.ImageSha256))
		cerr := fmt.Sprintf("computed %s configured %s",
			got, config.ImageSha256)
		status.PendingAdd = false
		updateVerifyErrStatus(ctx, status, cerr)
		log.Errorf("verifyObjectSha %s failed %s\n",
			config.Name, cerr)
		return false
	}

	log.Infof("Sha validation successful for %s\n", config.Name)

	if cerr := verifyObjectShaSignature(status, config, imageHash); cerr != "" {
		updateVerifyErrStatus(ctx, status, cerr)
		log.Errorf("Signature validation failed for %s, %s\n",
			config.Name, cerr)
		return false
	}
	return true
}

func computeShaFile(filename string) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func verifyObjectShaSignature(status *types.VerifyImageStatus, config *types.VerifyImageConfig, imageHash []byte) string {

	// XXX:FIXME if Image Signature is absent, skip
	// mark it as verified; implicitly assuming,
	// if signature is filled in, marking this object
	//  as valid may not hold good always!!!
	if (config.ImageSignature == nil) ||
		(len(config.ImageSignature) == 0) {
		log.Infof("No signature to verify for %s\n",
			config.Name)
		return ""
	}

	log.Infof("Validating %s using cert %s sha %s\n",
		config.Name, config.SignatureKey,
		config.ImageSha256)

	//Read the server certificate
	//Decode it and parse it
	//And find out the puplic key and it's type
	//we will use this certificate for both cert chain verification
	//and signature verification...

	//This func literal will take care of writing status during
	//cert chain and signature verification...

	serverCertName := types.UrlToFilename(config.SignatureKey)
	serverCertificate, err := ioutil.ReadFile(certificateDirname + "/" + serverCertName)
	if err != nil {
		cerr := fmt.Sprintf("unable to read the certificate %s: %s", serverCertName, err)
		return cerr
	}

	block, _ := pem.Decode(serverCertificate)
	if block == nil {
		cerr := fmt.Sprintf("unable to decode server certificate")
		return cerr
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		cerr := fmt.Sprintf("unable to parse certificate: %s", err)
		return cerr
	}

	//Verify chain of certificates. Chain contains
	//root, server, intermediate certificates ...

	certificateNameInChain := config.CertificateChain

	//Create the set of root certificates...
	roots := x509.NewCertPool()

	// Read the root cerificates from /config
	rootCertificate, err := ioutil.ReadFile(rootCertFileName)
	if err != nil {
		log.Errorln(err)
		cerr := fmt.Sprintf("failed to find root certificate: %s", err)
		return cerr
	}

	if ok := roots.AppendCertsFromPEM(rootCertificate); !ok {
		cerr := fmt.Sprintf("failed to parse root certificate")
		return cerr
	}

	for _, certUrl := range certificateNameInChain {

		certName := types.UrlToFilename(certUrl)

		bytes, err := ioutil.ReadFile(certificateDirname + "/" + certName)
		if err != nil {
			cerr := fmt.Sprintf("failed to read certificate Directory %s: %s",
				certName, err)
			return cerr
		}

		if ok := roots.AppendCertsFromPEM(bytes); !ok {
			cerr := fmt.Sprintf("failed to parse intermediate certificate")
			return cerr
		}
	}

	opts := x509.VerifyOptions{Roots: roots}
	if _, err := cert.Verify(opts); err != nil {
		cerr := fmt.Sprintf("failed to verify certificate chain: %s",
			err)
		return cerr
	}

	log.Infof("certificate options verified for %s\n", config.Name)

	//Read the signature from config file...
	imgSig := config.ImageSignature

	switch pub := cert.PublicKey.(type) {

	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, imageHash, imgSig)
		if err != nil {
			cerr := fmt.Sprintf("rsa image signature verification failed: %s", err)
			return cerr
		}
		log.Infof("VerifyPKCS1v15 successful for %s\n",
			config.Name)
	case *ecdsa.PublicKey:
		imgSignature, err := base64.StdEncoding.DecodeString(string(imgSig))
		if err != nil {
			cerr := fmt.Sprintf("DecodeString failed: %v ", err)
			return cerr
		}

		log.Debugf("Decoded imgSignature (len %d): % x\n",
			len(imgSignature), imgSignature)
		rbytes := imgSignature[0:32]
		sbytes := imgSignature[32:]
		log.Debugf("Decoded r %d s %d\n", len(rbytes), len(sbytes))
		r := new(big.Int)
		s := new(big.Int)
		r.SetBytes(rbytes)
		s.SetBytes(sbytes)
		log.Debugf("Decoded r, s: %v, %v\n", r, s)
		ok := ecdsa.Verify(pub, imageHash, r, s)
		if !ok {
			cerr := fmt.Sprintf("ecdsa image signature verification failed")
			return cerr
		}
		log.Infof("ecdsa Verify successful for %s\n",
			config.Name)
	default:
		cerr := fmt.Sprintf("unknown type of public key")
		return cerr
	}
	return ""
}

func markObjectAsVerified(ctx *verifierContext, config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) {

	objType := status.ObjType
	downloadDirname := objectDownloadDirname + "/" + objType
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifiedDirname := downloadDirname + "/verified/" + config.ImageSha256

	verifierFilename := verifierDirname + "/" + config.Safename
	verifiedFilename := verifiedDirname + "/" + config.Safename

	// Move directory from objectDownloadDirname/verifier to
	// objectDownloadDirname/verified
	// XXX should have dom0 do this and/or have RO mounts
	filename := types.SafenameToFilename(config.Safename)
	verifiedFilename = verifiedDirname + "/" + filename
	log.Infof("Move from %s to %s\n", verifierFilename, verifiedFilename)

	if _, err := os.Stat(verifierFilename); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(verifiedFilename); err == nil {
		log.Warn(verifiedFilename + ": file exists")
		if err := os.RemoveAll(verifiedFilename); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(verifiedDirname); err == nil {
		// Directory exists thus we have a sha256 collision presumably
		// due to multiple safenames (i.e., URLs) for the same content.
		// Delete existing to avoid wasting space.
		locations, err := ioutil.ReadDir(verifiedDirname)
		if err != nil {
			log.Fatal(err)
		}
		for _, location := range locations {
			log.Debugf("Identical sha256 (%s) for safenames %s and %s; deleting old\n",
				config.ImageSha256, location.Name(),
				config.Safename)
		}
		if err := os.RemoveAll(verifiedDirname); err != nil {
			log.Fatal(err)
		}
	}

	log.Errorf("Create %s\n", verifiedDirname)
	if err := os.MkdirAll(verifiedDirname, 0700); err != nil {
		log.Fatal(err)
	}

	if err := os.Rename(verifierFilename, verifiedFilename); err != nil {
		log.Fatal(err)
	}

	if err := os.Chmod(verifiedDirname, 0500); err != nil {
		log.Fatal(err)
	}

	// Clean up empty directory
	if err := os.RemoveAll(verifierDirname); err != nil {
		log.Fatal(err)
	}
}

func handleModify(ctx *verifierContext, config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) {

	// Note no comparison on version
	changed := false

	log.Infof("handleModify(%v) objType %s for %s\n",
		status.Safename, status.ObjType, config.Name)

	if status.ObjType == "" {
		log.Fatalf("handleModify: No ObjType for %s\n",
			status.Safename)
	}

	// Always update RefCount
	if status.RefCount != config.RefCount {
		log.Infof("handleModify RefCount change %s from %d to %d Expired %v\n",
			config.Name, status.RefCount, config.RefCount,
			status.Expired)
		status.RefCount = config.RefCount
		status.Expired = false
		changed = true
	}

	if status.RefCount == 0 {
		// GC timer will clean up by marking status Expired
		// and some point in time.
		// Then user (zedmanager/baseosmgr) will delete config.
		status.PendingModify = true
		status.LastUse = time.Now()
		status.PendingModify = false
		publishVerifyImageStatus(ctx, status)
		log.Infof("handleModify done for %s\n", config.Name)
		return
	}

	// If identical we do nothing. Otherwise we do a delete and create.
	if config.Safename == status.Safename &&
		config.ImageSha256 == status.ImageSha256 {
		if changed {
			publishVerifyImageStatus(ctx, status)
		}
		log.Infof("handleModify: no (other) change for %s\n",
			config.Name)
		return
	}

	status.PendingModify = true
	publishVerifyImageStatus(ctx, status)
	handleDelete(ctx, status)
	handleCreate(ctx, status.ObjType, config)
	status.PendingModify = false
	publishVerifyImageStatus(ctx, status)
	log.Infof("handleModify done for %s\n", config.Name)
}

func handleDelete(ctx *verifierContext, status *types.VerifyImageStatus) {

	log.Infof("handleDelete(%v) objType %s refcount %d lastUse %v Expired %v\n",
		status.Safename, status.ObjType, status.RefCount,
		status.LastUse, status.Expired)

	if status.ObjType == "" {
		log.Fatalf("handleDelete: No ObjType for %s\n",
			status.Safename)
	}

	doDelete(status)

	unpublishVerifyImageStatus(ctx, status)
	log.Infof("handleDelete done for %s\n", status.Safename)
}

// Remove the file from any of the three directories
// Only if it verified (state DELIVERED) do we delete the final. Needed
// to avoid deleting a different verified file with same sha as this claimed
// to have
func doDelete(status *types.VerifyImageStatus) {
	log.Infof("doDelete(%v)\n", status.Safename)

	objType := status.ObjType
	downloadDirname := objectDownloadDirname + "/" + objType
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifiedDirname := downloadDirname + "/verified/" + status.ImageSha256

	_, err := os.Stat(verifierDirname)
	if err == nil {
		log.Infof("doDelete removing verifier %s\n", verifierDirname)
		if err := os.RemoveAll(verifierDirname); err != nil {
			log.Fatal(err)
		}
	}
	_, err = os.Stat(verifiedDirname)
	if err == nil && status.State == types.DELIVERED {
		if _, err := os.Stat(preserveFilename); err != nil {
			log.Infof("doDelete removing %s\n", verifiedDirname)
			if err := os.RemoveAll(verifiedDirname); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Infof("doDelete preserving %s\n", verifiedDirname)
		}
	}
	log.Infof("doDelete(%v) done\n", status.Safename)
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*verifierContext)
	if key != "global" {
		log.Infof("handleGlobalConfigModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigModify for %s\n", key)
	var gcp *types.GlobalConfig
	debug, gcp = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	if gcp != nil && gcp.DownloadGCTime != 0 {
		downloadGCTime = time.Duration(gcp.DownloadGCTime) * time.Second
	}
	log.Infof("handleGlobalConfigModify done for %s\n", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*verifierContext)
	if key != "global" {
		log.Infof("handleGlobalConfigDelete: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigDelete for %s\n", key)
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	log.Infof("handleGlobalConfigDelete done for %s\n", key)
}
