// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

// baseosmgr orchestrates base os/certs installation
// interfaces with zedagent for configuration update
// interfaces with downloader for basos image/certs download
// interfaces with verifier for image sha/signature verfication

// baswos handles the following orchestration
//   * base os download config/status <downloader> / <baseos> / <config | status>
//   * certs download config/status   <downloader> / <certs>  / <config | status>
//   * base os verifier config/status <verifier>   / <baseos> / <config | status>
// <base os>
//   <zedagent>   <baseos> <config> --> <baseosmgr>   <baseos> <status>
//				<download>...       --> <downloader>  <baseos> <config>
//   <downloader> <baseos> <config> --> <downloader>  <baseos> <status>
//				<downloaded>...     --> <downloader>  <baseos> <status>
//								    --> <baseosmgr>   <baseos> <status>
//								    --> <verifier>    <baseos> <config>
//				<verified>  ...     --> <verifier>    <baseos> <status>
//								    --> <baseosmgr>   <baseos> <status>
// <certs>
//   <zedagent>   <certs> <config> --> <baseosmgr>   <certs> <status>
//				<download>...      --> <downloader>  <certs> <config>
//   <downloader> <certs> <config> --> <downloader>  <certs> <status>
//				<downloaded>...    --> <downloader>  <certs> <status>
//								   --> <baseosmgr>   <certs> <status>

package baseosmgr

import (
	"flag"
	"fmt"
	"github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/agentlog"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/pidfile"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"os"
	"time"
)

const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"
	certObj   = "cert.obj"
	agentName = "baseosmgr"

	configDir             = "/config"
	persistDir            = "/persist"
	objectDownloadDirname = persistDir + "/downloads"
	certificateDirname    = persistDir + "/certs"
	checkpointDirname     = persistDir + "/checkpoint"
)

// Set from Makefile
var Version = "No version specified"

type baseOsMgrContext struct {
	verifierRestarted        bool  // Information from handleVerifierRestarted
	iteration                int
	TriggerDeviceInfo        bool
	pubBaseOsStatus          *pubsub.Publication
	pubBaseOsDownloadConfig  *pubsub.Publication
	pubBaseOsVerifierConfig  *pubsub.Publication
	pubCertObjStatus         *pubsub.Publication
	pubCertObjDownloadConfig *pubsub.Publication
	subGlobalConfig          *pubsub.Subscription
	subBaseOsConfig          *pubsub.Subscription
	subCertObjConfig         *pubsub.Subscription
	subDatastoreConfig       *pubsub.Subscription
	subBaseOsDownloadStatus  *pubsub.Subscription
	subCertObjDownloadStatus *pubsub.Subscription
	subBaseOsVerifierStatus  *pubsub.Subscription
}

var debug = false
var debugOverride bool // From command line arg

func Run() {
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
	logf, err := agentlog.Init(agentName)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()
	if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
		log.Fatal(err)
	}

	log.Infof("Starting %s\n", agentName)

	// Tell ourselves to go ahead
	// Context to pass around

	baseOsMgrCtx := baseOsMgrContext{}

	pubBaseOsStatus, err := pubsub.Publish(agentName,
		types.BaseOsStatus{})
	if err != nil {
		log.Fatal(err)
	}
	pubBaseOsStatus.ClearRestarted()
	baseOsMgrCtx.pubBaseOsStatus = pubBaseOsStatus

	pubBaseOsDownloadConfig, err := pubsub.PublishScope(agentName,
		baseOsObj, types.DownloaderConfig{})
	if err != nil {
		log.Fatal(err)
	}
	pubBaseOsDownloadConfig.ClearRestarted()
	baseOsMgrCtx.pubBaseOsDownloadConfig = pubBaseOsDownloadConfig

	pubBaseOsVerifierConfig, err := pubsub.PublishScope(agentName,
		baseOsObj, types.VerifyImageConfig{})
	if err != nil {
		log.Fatal(err)
	}
	pubBaseOsVerifierConfig.ClearRestarted()
	baseOsMgrCtx.pubBaseOsVerifierConfig = pubBaseOsVerifierConfig

	pubCertObjStatus, err := pubsub.Publish(agentName,
		types.CertObjStatus{})
	if err != nil {
		log.Fatal(err)
	}
	pubCertObjStatus.ClearRestarted()
	baseOsMgrCtx.pubCertObjStatus = pubCertObjStatus

	pubCertObjDownloadConfig, err := pubsub.PublishScope(agentName,
		certObj, types.DownloaderConfig{})
	if err != nil {
		log.Fatal(err)
	}
	pubCertObjDownloadConfig.ClearRestarted()
	baseOsMgrCtx.pubCertObjDownloadConfig = pubCertObjDownloadConfig

	// Look for global config such as log levels
	subGlobalConfig, err := pubsub.Subscribe("", types.GlobalConfig{},
		false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subGlobalConfig.ModifyHandler = handleGlobalConfigModify
	subGlobalConfig.DeleteHandler = handleGlobalConfigDelete
	baseOsMgrCtx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	// Look for BaseOsConfig , from zedagent
	subBaseOsConfig, err := pubsub.Subscribe("zedagent",
		types.BaseOsConfig{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subBaseOsConfig.ModifyHandler = handleBaseOsConfigModify
	subBaseOsConfig.DeleteHandler = handleBaseOsConfigDelete
	baseOsMgrCtx.subBaseOsConfig = subBaseOsConfig
	subBaseOsConfig.Activate()

	// Look for DatastorConfig, from zedagent
	subDatastoreConfig, err := pubsub.Subscribe("zedagent",
		types.DatastoreConfig{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subDatastoreConfig.ModifyHandler = handleDatastoreConfigModify
	subDatastoreConfig.DeleteHandler = handleDatastoreConfigDelete
	baseOsMgrCtx.subDatastoreConfig = subDatastoreConfig
	subDatastoreConfig.Activate()

	// Look for BaseOs DownloaderStatus from downloader
	subBaseOsDownloadStatus, err := pubsub.SubscribeScope("downloader",
		baseOsObj, types.DownloaderStatus{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subBaseOsDownloadStatus.ModifyHandler = handleDownloadStatusModify
	subBaseOsDownloadStatus.DeleteHandler = handleDownloadStatusDelete
	baseOsMgrCtx.subBaseOsDownloadStatus = subBaseOsDownloadStatus
	subBaseOsDownloadStatus.Activate()

	// Look for VerifyImageStatus from verifier
	subBaseOsVerifierStatus, err := pubsub.SubscribeScope("verifier",
		baseOsObj, types.VerifyImageStatus{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subBaseOsVerifierStatus.ModifyHandler = handleVerifierStatusModify
	subBaseOsVerifierStatus.DeleteHandler = handleVerifierStatusDelete
	subBaseOsVerifierStatus.RestartHandler = handleVerifierRestarted
	baseOsMgrCtx.subBaseOsVerifierStatus = subBaseOsVerifierStatus
	subBaseOsVerifierStatus.Activate()

	// Look for CertObjConfig, from zedagent
	subCertObjConfig, err := pubsub.Subscribe("zedagent",
		types.CertObjConfig{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subCertObjConfig.ModifyHandler = handleCertObjConfigModify
	subCertObjConfig.DeleteHandler = handleCertObjConfigDelete
	baseOsMgrCtx.subCertObjConfig = subCertObjConfig
	subCertObjConfig.Activate()

	// Look for Certs DownloaderStatus from downloader
	subCertObjDownloadStatus, err := pubsub.SubscribeScope("downloader",
		certObj, types.DownloaderStatus{}, false, &baseOsMgrCtx)
	if err != nil {
		log.Fatal(err)
	}
	subCertObjDownloadStatus.ModifyHandler = handleDownloadStatusModify
	subCertObjDownloadStatus.DeleteHandler = handleDownloadStatusDelete
	baseOsMgrCtx.subCertObjDownloadStatus = subCertObjDownloadStatus
	subCertObjDownloadStatus.Activate()

	// start the forever loop for event handling
	for {
		select {
		case change := <-subGlobalConfig.C:
			subGlobalConfig.ProcessChange(change)

		case change := <-subCertObjConfig.C:
			subCertObjConfig.ProcessChange(change)

		case change := <-subBaseOsConfig.C:
			subBaseOsConfig.ProcessChange(change)

		case change := <-subDatastoreConfig.C:
			subDatastoreConfig.ProcessChange(change)

		case change := <-subBaseOsDownloadStatus.C:
			subBaseOsDownloadStatus.ProcessChange(change)

		case change := <-subBaseOsVerifierStatus.C:
			subBaseOsVerifierStatus.ProcessChange(change)

		case change := <-subCertObjDownloadStatus.C:
			subCertObjDownloadStatus.ProcessChange(change)
		}
	}
}

func handleVerifierRestarted(ctxArg interface{}, done bool) {
	ctx := ctxArg.(*baseOsMgrContext)
	log.Infof("handleVerifierRestarted(%v)\n", done)
	if done {
		ctx.verifierRestarted = true
	}
}

// Wrappers around handleBaseOsCreate/Modify/Delete

func handleBaseOsConfigModify(ctxArg interface{}, key string, configArg interface{}) {
	ctx := ctxArg.(*baseOsMgrContext)
	config := cast.CastBaseOsConfig(configArg)
	if config.Key() != key {
		log.Errorf("handleBaseOsConfigModify key/UUID mismatch %s vs %s; ignored %+v\n", key, config.Key(), config)
		return
	}
	status := lookupBaseOsStatus(ctx, key)
	if status == nil {
		handleBaseOsCreate(ctx, key, &config)
	} else {
		handleBaseOsModify(ctx, key, &config, status)
	}
	log.Infof("handleBaseOsConfigModify(%s) done\n", key)
}

func handleBaseOsConfigDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Infof("handleBaseOsConfigDelete(%s)\n", key)
	ctx := ctxArg.(*baseOsMgrContext)
	status := lookupBaseOsStatus(ctx, key)
	if status == nil {
		log.Infof("handleBaseOsConfigDelete: unknown %s\n", key)
		return
	}
	handleBaseOsDelete(ctx, key, status)
	log.Infof("handleBaseOsConfigDelete(%s) done\n", key)
}
// base os config event handlers
// base os config create event
func handleBaseOsCreate(ctxArg interface{}, key string,
	configArg interface{}) {

	config := cast.CastBaseOsConfig(configArg)
	if config.Key() != key {
		log.Errorf("handleBaseOsCreate key/UUID mismatch %s vs %s; ignored %+v\n",
			key, config.Key(), config)
		return
	}
	uuidStr := config.Key()
	ctx := ctxArg.(*baseOsMgrContext)

	log.Infof("handleBaseOsCreate for %s\n", uuidStr)
	status := types.BaseOsStatus{
		UUIDandVersion: config.UUIDandVersion,
		BaseOsVersion:  config.BaseOsVersion,
		ConfigSha256:   config.ConfigSha256,
	}

	status.StorageStatusList = make([]types.StorageStatus,
		len(config.StorageConfigList))

	for i, sc := range config.StorageConfigList {
		ss := &status.StorageStatusList[i]
		ss.Name = sc.Name
		ss.ImageSha256 = sc.ImageSha256
		ss.Target = sc.Target
	}
	handleBaseOsCreate2(ctx, config, status)
}

func handleBaseOsCreate2(ctx *baseOsMgrContext, config types.BaseOsConfig,
	status types.BaseOsStatus) {

	// Check total and activated counts
	err := validateBaseOsConfig(ctx, config)
	if err != nil {
		errStr := fmt.Sprintf("%v", err)
		log.Errorln(errStr)
		status.Error = errStr
		status.ErrorTime = time.Now()
		publishBaseOsStatus(ctx, &status)
		return
	}

	baseOsGetActivationStatus(ctx, &status)
	publishBaseOsStatus(ctx, &status)

	baseOsHandleStatusUpdate(ctx, &config, &status)
}

// base os config modify event
func handleBaseOsModify(ctxArg interface{}, key string,
	configArg interface{}, statusArg interface{}) {
	config := cast.CastBaseOsConfig(configArg)
	if config.Key() != key {
		log.Errorf("handleBaseOsModify key/UUID mismatch %s vs %s; ignored %+v\n",
			key, config.Key(), config)
		return
	}
	status := cast.CastBaseOsStatus(statusArg)
	if status.Key() != key {
		log.Errorf("handleBaseOsModify key/UUID mismatch %s vs %s; ignored %+v\n",
			key, status.Key(), status)
		return
	}
	uuidStr := config.Key()
	ctx := ctxArg.(*baseOsMgrContext)

	log.Infof("handleBaseOsModify for %s Activate %v\n",
		config.BaseOsVersion, config.Activate)
	if config.UUIDandVersion.Version == status.UUIDandVersion.Version &&
		config.Activate == status.Activated {
		log.Infof("Same version %v for %s\n",
			config.UUIDandVersion.Version, uuidStr)
		return
	}

	// Check total and activated counts
	err := validateBaseOsConfig(ctx, config)
	if err != nil {
		errStr := fmt.Sprintf("%v", err)
		log.Errorln(errStr)
		status.Error = errStr
		status.ErrorTime = time.Now()
		publishBaseOsStatus(ctx, &status)
		return
	}

	// update the version field, uuids being the same
	status.UUIDandVersion = config.UUIDandVersion
	publishBaseOsStatus(ctx, &status)

	baseOsHandleStatusUpdate(ctx, &config, &status)
}

// base os config delete event
func handleBaseOsDelete(ctxArg interface{}, key string,
	configArg interface{}) {
	status := configArg.(*types.BaseOsStatus)
	if status.Key() != key {
		log.Errorf("handleBaseOsDelete key/UUID mismatch %s vs %s; ignored %+v\n",
			key, status.Key(), status)
		return
	}
	ctx := ctxArg.(*baseOsMgrContext)

	log.Infof("handleBaseOsDelete for %s\n", status.BaseOsVersion)
	removeBaseOsConfig(ctx, status.Key())
}

// Wrappers around handleCertObjCreate/Modify/Delete

func handleCertObjConfigModify(ctxArg interface{}, key string, configArg interface{}) {
	ctx := ctxArg.(*baseOsMgrContext)
	config := cast.CastCertObjConfig(configArg)
	if config.Key() != key {
		log.Errorf("handleCertObjConfigModify key/UUID mismatch %s vs %s; ignored %+v\n", key, config.Key(), config)
		return
	}
	status := lookupCertObjStatus(ctx, key)
	if status == nil {
		handleCertObjCreate(ctx, key, &config)
	} else {
		handleCertObjModify(ctx, key, &config, status)
	}
	log.Infof("handleCertObjConfigModify(%s) done\n", key)
}

func handleCertObjConfigDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Infof("handleCertObjConfigDelete(%s)\n", key)
	ctx := ctxArg.(*baseOsMgrContext)
	status := lookupCertObjStatus(ctx, key)
	if status == nil {
		log.Infof("handleCertObjConfigDelete: unknown %s\n", key)
		return
	}
	handleCertObjDelete(ctx, key, status)
	log.Infof("handleCertObjConfigDelete(%s) done\n", key)
}

// certificate config/status event handlers
// certificate config create event
func handleCertObjCreate(ctx *baseOsMgrContext, key string, config *types.CertObjConfig) {

	log.Infof("handleCertObjCreate for %s\n", key)

	status := types.CertObjStatus{
		UUIDandVersion: config.UUIDandVersion,
		ConfigSha256:   config.ConfigSha256,
	}

	status.StorageStatusList = make([]types.StorageStatus,
		len(config.StorageConfigList))

	for i, sc := range config.StorageConfigList {
		ss := &status.StorageStatusList[i]
		ss.Name = sc.Name
		ss.ImageSha256 = sc.ImageSha256
		ss.FinalObjDir = certificateDirname
	}

	publishCertObjStatus(ctx, &status)

	certObjHandleStatusUpdate(ctx, config, &status)
}

// certificate config modify event
func handleCertObjModify(ctx *baseOsMgrContext, key string, config *types.CertObjConfig, status *types.CertObjStatus) {

	uuidStr := config.Key()
	log.Infof("handleCertObjModify for %s\n", uuidStr)

	// XXX:FIXME, do we
	if config.UUIDandVersion.Version == status.UUIDandVersion.Version {
		log.Infof("Same version %v for %s\n",
			config.UUIDandVersion.Version, key)
		return
	}

	status.UUIDandVersion = config.UUIDandVersion
	publishCertObjStatus(ctx, status)

	certObjHandleStatusUpdate(ctx, config, status)
}

// certificate config delete event
func handleCertObjDelete(ctx *baseOsMgrContext, key string,
	status *types.CertObjStatus) {

	uuidStr := status.Key()
	log.Infof("handleCertObjDelete for %s\n", uuidStr)
	removeCertObjConfig(ctx, uuidStr)
}

// base os/certs download status modify event
func handleDownloadStatusModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	status := cast.CastDownloaderStatus(statusArg)
	if status.Key() != key {
		log.Errorf("handleDownloadStatusModify key/UUID mismatch %s vs %s; ignored %+v\n",
			key, status.Key(), status)
		return
	}
	ctx := ctxArg.(*baseOsMgrContext)
	log.Infof("handleDownloadStatusModify for %s\n",
		status.Safename)
	updateDownloaderStatus(ctx, &status)
}

// base os/certs download status delete event
func handleDownloadStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	status := cast.CastDownloaderStatus(statusArg)
	log.Infof("handleDownloadStatusDelete RefCount %d Expired %v for %s\n",
		status.RefCount, status.Expired, key)
	// Nothing to do
}

// base os verifier status modify event
func handleVerifierStatusModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	status := cast.CastVerifyImageStatus(statusArg)
	if status.Key() != key {
		log.Errorf("handleVerifierStatusModify key/UUID mismatch %s vs %s; ignored %+v\n",
			key, status.Key(), status)
		return
	}
	ctx := ctxArg.(*baseOsMgrContext)
	log.Infof("handleVerifierStatusModify for %s\n", status.Safename)
	updateVerifierStatus(ctx, &status)
}

// base os verifier status delete event
func handleVerifierStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	status := cast.CastVerifyImageStatus(statusArg)
	log.Infof("handleVeriferStatusDelete RefCount %d Expired %v for %s\n",
		status.RefCount, status.Expired, key)
	// Nothing to do
}

// data store config modify event
func handleDatastoreConfigModify(ctxArg interface{}, key string,
	configArg interface{}) {

	ctx := ctxArg.(*baseOsMgrContext)
	config := cast.CastDatastoreConfig(configArg)
	checkAndRecreateBaseOs(ctx, config.UUID)
	log.Infof("handleDatastoreConfigModify for %s\n", key)
}

// data store config delete event
func handleDatastoreConfigDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Infof("handleDatastoreConfigDelete for %s\n", key)
}

// Called when a DatastoreConfig is added
// Walk all BaseOsStatus (XXX Cert?) looking for MissingDatastore, then
// check if the DatastoreId matches.
func checkAndRecreateBaseOs(ctx *baseOsMgrContext, datastore uuid.UUID) {

	log.Infof("checkAndRecreateBaseOs(%s)\n", datastore.String())
	pub := ctx.pubBaseOsStatus
	items := pub.GetAll()
	for _, st := range items {
		status := cast.CastBaseOsStatus(st)
		if !status.MissingDatastore {
			continue
		}
		log.Infof("checkAndRecreateBaseOs(%s) missing for %s\n",
			datastore.String(), status.BaseOsVersion)

		config := lookupBaseOsConfig(ctx, status.Key())
		if config == nil {
			log.Warnf("checkAndRecreatebaseOs(%s) no config for %s\n",
				datastore.String(), status.BaseOsVersion)
			continue
		}

		matched := false
		for _, ss := range config.StorageConfigList {
			if ss.DatastoreId != datastore {
				continue
			}
			log.Infof("checkAndRecreateBaseOs(%s) found ss %s for %s\n",
				datastore.String(), ss.Name,
				status.BaseOsVersion)
			matched = true
		}
		if !matched {
			continue
		}
		log.Infof("checkAndRecreateBaseOs(%s) recreating for %s\n",
			datastore.String(), status.BaseOsVersion)
		if status.Error != "" {
			log.Infof("checkAndRecreateBaseOs(%s) remove error %s for %s\n",
				datastore.String(), status.Error,
				status.BaseOsVersion)
			status.Error = ""
			status.ErrorTime = time.Time{}
		}
		handleBaseOsCreate2(ctx, *config, status)
	}
}

func appendError(allErrors string, prefix string, lasterr string) string {
	return fmt.Sprintf("%s%s: %s\n\n", allErrors, prefix, lasterr)
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*baseOsMgrContext)
	if key != "global" {
		log.Infof("handleGlobalConfigModify: ignoring %s\n", key)
		return
	}
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
        debugOverride)
	log.Infof("handleGlobalConfigModify done for %s\n", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*baseOsMgrContext)
	if key != "global" {
		log.Infof("handleGlobalConfigDelete: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigDelete for %s\n", key)
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	log.Infof("handleGlobalConfigDelete done for %s\n", key)
}
