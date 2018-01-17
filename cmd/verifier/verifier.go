// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// Process input changes from a config directory containing json encoded files
// with VerifyImageConfig and compare against VerifyImageStatus in the status
// dir.
// Move the file from downloads/pending/<claimedsha>/<safename> to
// to downloads/verifier/<claimedsha>/<safename> and make RO, then attempt to
// verify sum.
// Once sum is verified, move to downloads/verified/<sha>/<filename> where
// the filename is the last part of the URL (after the last '/')
// Note that different URLs for same file will download to the same <sha>
// directory. We delete duplicates assuming the file content will be the same.

package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"strings"
	"time"
)

// Keeping status in /var/run to be clean after a crash/reboot
const (
	appImgObj = "appImg.obj"
	baseOsObj = "baseOs.obj"

	moduleName            = "verifier"
	zedBaseDirname        = "/var/tmp"
	zedRunDirname         = "/var/run"
	baseDirname           = zedBaseDirname + "/" + moduleName
	runDirname            = zedRunDirname + "/" + moduleName
	configDirname         = baseDirname + "/config"
	statusDirname         = runDirname + "/status"
	objectDownloadDirname = "/var/tmp/zedmanager/downloads"

	certsDirname     = "/var/tmp/zedmanager/certs"
	rootCertDirname  = "/opt/zededa/etc"
	rootCertFileName = rootCertDirname + "/root-certificate.pem"

	// If this file is present we don't delete verified files in handleDelete
	preserveFilename = configDirname + "/preserve"

	appImgConfigDirname = baseDirname + "/" + appImgObj + "/config"
	appImgStatusDirname = runDirname + "/" + appImgObj + "/status"

	baseOsConfigDirname = baseDirname + "/" + baseOsObj + "/config"
	baseOsStatusDirname = runDirname + "/" + baseOsObj + "/status"
)

// Set from Makefile
var Version = "No version specified"

func main() {

	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	versionPtr := flag.Bool("v", false, "Version")
	flag.Parse()
	if *versionPtr {
		log.Printf("%s: %s\n", os.Args[0], Version)
		return
	}

	log.Printf("Starting verifier\n")
	watch.CleanupRestarted("verifier")

	handleInit()

	// Report to zedmanager that init is done
	watch.SignalRestarted("verifier")

	appImgChanges := make(chan string)
	baseOsChanges := make(chan string)

	go watch.WatchConfigStatusAllowInitialConfig(appImgConfigDirname,
		appImgStatusDirname, appImgChanges)

	go watch.WatchConfigStatusAllowInitialConfig(baseOsConfigDirname,
		baseOsStatusDirname, baseOsChanges)

	for {

		select {
		case change := <-appImgChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					appImgConfigDirname,
					appImgStatusDirname,
					&types.VerifyImageConfig{},
					&types.VerifyImageStatus{},
					handleCreate,
					handleModify,
					handleDelete, nil)
				continue
			}
		case change := <-baseOsChanges:
			{
				go watch.HandleConfigStatusEvent(change,
					baseOsConfigDirname,
					baseOsStatusDirname,
					&types.VerifyImageConfig{},
					&types.VerifyImageStatus{},
					handleCreate,
					handleModify,
					handleDelete, nil)
				continue
			}
		}
	}
}

// Determine which files we have already verified and set status for them
func handleInit() {

	initializeDirs()

	handleInitWorkinProgresObjects()

	handleInitVerifiedObjects()

	handleInitMarkedDeletePendingObjects()
}

func handleCreate(statusFilename string, configArg interface{}) {
	var config *types.VerifyImageConfig

	switch configArg.(type) {
	default:
		log.Fatal("handleCreate: Can only handle VerifyImageConfig")
	case *types.VerifyImageConfig:
		config = configArg.(*types.VerifyImageConfig)
	}

	if isOk := isStatusFilePresent(config, statusFilename); isOk != true {
		return
	}

	log.Printf("handleCreate(%v) for %s\n",
		config.Safename, config.DownloadURL)

	// Start by marking with PendingAdd
	status := types.VerifyImageStatus{
		Safename:    config.Safename,
		ImageSha256: config.ImageSha256,
		PendingAdd:  true,
		State:       types.DOWNLOADED,
		ObjType:     config.ObjType,
		RefCount:    config.RefCount,
	}
	writeVerifyObjectStatus(&status, statusFilename)

	if err := moveObjectFromPendingToVerifier(config, &status); err != true {
		return
	}

	if err := verifyObjectSha(config, &status, statusFilename); err != true {
		return
	}

	if err := moveObjectFromVerifierToVerified(config, &status); err == true {
		status.PendingAdd = false
		status.State = types.DELIVERED
		writeVerifyObjectStatus(&status, statusFilename)
	}

	log.Printf("handleCreate done for %s\n", config.DownloadURL)
}

func moveObjectFromPendingToVerifier(config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) bool {

	// Form the unique filename in /var/tmp/zedmanager/downloads/pending/
	// based on the claimed Sha256 and safename, and the same name
	// in downloads/verifier/. Form a shorter name for
	// downloads/verified/.

	downloadDirname := objectDownloadDirname + "/" + config.ObjType
	pendingDirname := downloadDirname + "/pending/" + status.ImageSha256
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256

	pendingFilename := pendingDirname + "/" + config.Safename
	verifierFilename := verifierDirname + "/" + config.Safename

	// Move to verifier directory which is RO
	// XXX should have dom0 do this and/or have RO mounts
	log.Printf("Move from %s to %s\n", pendingFilename, verifierFilename)

	if _, err := os.Stat(pendingFilename); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(verifierFilename); err == nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(verifierDirname); err == nil {
		if err := os.RemoveAll(verifierDirname); err != nil {
			log.Fatal(err)
		}
	}

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
	return true
}

func verifyObjectSha(config *types.VerifyImageConfig,
	status *types.VerifyImageStatus, statusFilename string) bool {

	downloadDirname := objectDownloadDirname + "/" + config.ObjType
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifierFilename := verifierDirname + "/" + config.Safename

	log.Printf("Verifying URL %s file %s\n",
		config.DownloadURL, verifierFilename)

	fd, err := os.Open(verifierFilename)
	if err != nil {
		cerr := fmt.Sprintf("%v", err)
		updateVerifyErrStatus(status, cerr, statusFilename)
		log.Printf("File read failed for %s, %v\n", config.DownloadURL, err)
		return false
	}
	defer fd.Close()

	// compute sha256 of the image and match it
	// with the one in config file...
	h := sha256.New()
	if _, err := io.Copy(h, fd); err != nil {
		cerr := fmt.Sprintf("%v", err)
		updateVerifyErrStatus(status, cerr, statusFilename)
		log.Printf("Sha computation failed for %s, %v\n",
			config.DownloadURL, err)
		return false
	}

	imageHash := h.Sum(nil)
	got := fmt.Sprintf("%x", h.Sum(nil))
	if got != strings.ToLower(config.ImageSha256) {
		log.Printf("computed   %s\n", got)
		log.Printf("configured %s\n", strings.ToLower(config.ImageSha256))
		cerr := fmt.Sprintf("computed %s configured %s",
			got, config.ImageSha256)
		status.PendingAdd = false
		updateVerifyErrStatus(status, cerr, statusFilename)
		log.Printf("Sha validation failed for %s\n", config.DownloadURL)
		return false
	}

	log.Printf("Sha validation successful for %s\n", config.DownloadURL)

	if cerr := verifyObjectShaSignature(status, config, imageHash); cerr != "" {
		updateVerifyErrStatus(status, cerr, statusFilename)
		log.Printf("Signature validation failed for %s, %s\n",
			config.DownloadURL, cerr)
		return false
	}
	return true
}

func moveObjectFromVerifierToVerified(config *types.VerifyImageConfig,
	status *types.VerifyImageStatus) bool {

	downloadDirname := objectDownloadDirname + "/" + config.ObjType
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifiedDirname := downloadDirname + "/verified/" + config.ImageSha256

	verifierFilename := verifierDirname + "/" + config.Safename
	verifiedFilename := verifiedDirname + "/" + config.Safename

	// Move directory from downloads/verifier to downloads/verified
	// XXX should have dom0 do this and/or have RO mounts
	filename := types.SafenameToFilename(config.Safename)
	verifiedFilename = verifiedDirname + "/" + filename
	log.Printf("Move from %s to %s\n", verifierFilename, verifiedFilename)

	if _, err := os.Stat(verifierFilename); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(verifiedFilename); err == nil {
		log.Fatal(verifiedFilename + ": file exists")
	}

	// XXX change log.Fatal to something else?
	if _, err := os.Stat(verifiedDirname); err == nil {
		// Directory exists thus we have a sha256 collision presumably
		// due to multiple safenames (i.e., URLs) for the same content.
		// Delete existing to avoid wasting space.
		locations, err := ioutil.ReadDir(verifiedDirname)
		if err != nil {
			log.Fatal(err)
		}
		for _, location := range locations {
			log.Printf("Identical sha256 (%s) for safenames %s and %s; deleting old\n",
				config.ImageSha256, location.Name(),
				config.Safename)
		}

		if err := os.RemoveAll(verifiedDirname); err != nil {
			log.Fatal(err)
		}
	}

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
	return true
}

func verifyObjectShaSignature(status *types.VerifyImageStatus,
	config *types.VerifyImageConfig, imageHash []byte) string {

	// XXX:FIXME if Image Signature is absent, skip
	// mark it as verified; implicitly assuming,
	// if signature is filled in, marking this object
	//  as valid may not hold good always!!!
	if (config.ImageSignature == nil) ||
		(len(config.ImageSignature) == 0) {
		log.Printf("ImageSignature is absent for %s\n", config.Safename)
		return ""
	}

	//Read the server certificate
	//Decode it and parse it
	//And find out the puplic key and it's type
	//we will use this certificate for both cert chain verification
	//and signature verification...

	//This func literal will take care of writing status during
	//cert chain and signature verification...

	serverCertName := types.UrlToFilename(config.SignatureKey)
	serverCertificate, err := ioutil.ReadFile(certsDirname + "/" + serverCertName)
	if err != nil {
		cerr := fmt.Sprintf("unable to read the certificate %s", serverCertName)
		return cerr
	}

	block, _ := pem.Decode(serverCertificate)
	if block == nil {
		cerr := fmt.Sprintf("unable to decode certificate")
		return cerr
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		cerr := fmt.Sprintf("unable to parse certificate")
		return cerr
	}

	//Verify chain of certificates. Chain contains
	//root, server, intermediate certificates ...

	certificateNameInChain := config.CertificateChain

	//Create the set of root certificates...
	roots := x509.NewCertPool()

	//read the root cerificates from /opt/zededa/etc/...
	rootCertificate, err := ioutil.ReadFile(rootCertFileName)
	if err != nil {
		fmt.Println(err)
		cerr := fmt.Sprintf("failed to find root certificate")
		return cerr
	}

	if ok := roots.AppendCertsFromPEM(rootCertificate); !ok {
		cerr := fmt.Sprintf("failed to parse root certificate")
		return cerr
	}

	for _, certUrl := range certificateNameInChain {

		certName := types.UrlToFilename(certUrl)

		bytes, err := ioutil.ReadFile(certsDirname + "/" + certName)
		if err != nil {
			cerr := fmt.Sprintf("failed to read certificate Directory: %v", certName)
			return cerr
		}

		if ok := roots.AppendCertsFromPEM(bytes); !ok {
			cerr := fmt.Sprintf("failed to parse intermediate certificate")
			return cerr
		}
	}

	opts := x509.VerifyOptions{Roots: roots}
	if _, err := cert.Verify(opts); err != nil {
		cerr := fmt.Sprintf("failed to verify certificate chain: ")
		return cerr
	}

	log.Println("certificate options verified")

	//Read the signature from config file...
	imgSig := config.ImageSignature

	switch pub := cert.PublicKey.(type) {

	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, imageHash, imgSig)
		if err != nil {
			cerr := fmt.Sprintf("rsa image signature verification failed")
			return cerr
		}
		log.Println("VerifyPKCS1v15 successful...\n")

	case *ecdsa.PublicKey:
		log.Printf("pub is of type ecdsa: ", pub)
		imgSignature, err := base64.StdEncoding.DecodeString(string(imgSig))
		if err != nil {
			cerr := fmt.Sprintf("DecodeString failed: %v ", err)
			return cerr
		}

		log.Printf("Decoded imgSignature (len %d): % x\n",
			len(imgSignature), imgSignature)
		rbytes := imgSignature[0:32]
		sbytes := imgSignature[32:]
		log.Printf("Decoded r %d s %d\n", len(rbytes), len(sbytes))
		r := new(big.Int)
		s := new(big.Int)
		r.SetBytes(rbytes)
		s.SetBytes(sbytes)
		log.Printf("Decoded r, s: %v, %v\n", r, s)
		ok := ecdsa.Verify(pub, imageHash, r, s)
		if !ok {
			cerr := fmt.Sprintf("ecdsa image signature verification failed ")
			return cerr
		}
		log.Printf("Signature verified\n")

	default:
		cerr := fmt.Sprintf("unknown type of public key")
		return cerr
	}
	return ""
}

func handleModify(statusFilename string, configArg interface{},
	statusArg interface{}) {
	var config *types.VerifyImageConfig
	var status *types.VerifyImageStatus

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageConfig")
	case *types.VerifyImageConfig:
		config = configArg.(*types.VerifyImageConfig)
	}

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageStatus")
	case *types.VerifyImageStatus:
		status = statusArg.(*types.VerifyImageStatus)
	}

	log.Printf("handleModify(%v) for %s\n",
		config.Safename, config.DownloadURL)

	// Note no comparison on version

	// Always update RefCount
	status.RefCount = config.RefCount

	if status.RefCount == 0 {
		status.PendingModify = true
		writeVerifyObjectStatus(status, statusFilename)
		doDelete(status)
		status.PendingModify = false
		status.State = 0 // XXX INITIAL implies failure
		writeVerifyObjectStatus(status, statusFilename)
		log.Printf("handleModify done for %s\n", config.DownloadURL)
		return
	}

	// If identical we do nothing. Otherwise we do a delete and create.
	if config.Safename == status.Safename &&
		config.ImageSha256 == status.ImageSha256 {
		log.Printf("handleModify: no change for %s\n",
			config.DownloadURL)
		return
	}

	// try deleting
	status.PendingModify = true
	writeVerifyObjectStatus(status, statusFilename)
	handleDelete(statusFilename, status)

	// create again
	handleCreate(statusFilename, config)
	status.PendingModify = false
	writeVerifyObjectStatus(status, statusFilename)
	log.Printf("handleModify done for %s\n", config.DownloadURL)
}

func handleDelete(statusFilename string, statusArg interface{}) {

	var status *types.VerifyImageStatus

	switch statusArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageStatus")
	case *types.VerifyImageStatus:
		status = statusArg.(*types.VerifyImageStatus)
	}
	log.Printf("handleDelete(%v)\n", status.Safename)

	doDelete(status)

	// Write out what we modified to VerifyImageStatus aka delete
	if err := os.Remove(statusFilename); err != nil {
		log.Println(err)
	}
	log.Printf("handleDelete done for %s\n", status.Safename)
}

// Remove the file from any of the three directories
// Only if it verified (state DELIVERED) do we detete the final. Needed
// to avoid deleting a different verified file with same sha as this claimed
// to have
func doDelete(status *types.VerifyImageStatus) {

	log.Printf("doDelete(%v)\n", status.Safename)

	downloadDirname := objectDownloadDirname + "/" + status.ObjType
	pendingDirname := downloadDirname + "/pending/" + status.ImageSha256
	verifierDirname := downloadDirname + "/verifier/" + status.ImageSha256
	verifiedDirname := downloadDirname + "/verified/" + status.ImageSha256

	// cleanup work in progress files, if any
	if _, err := os.Stat(pendingDirname); err == nil {
		log.Printf("doDelete removing %s\n", pendingDirname)
		if err := os.RemoveAll(pendingDirname); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(verifierDirname); err == nil {
		log.Printf("doDelete removing %s\n", verifierDirname)
		if err := os.RemoveAll(verifierDirname); err != nil {
			log.Fatal(err)
		}
	}

	_, err := os.Stat(verifiedDirname)
	if err == nil && status.State == types.DELIVERED {

		if _, err := os.Stat(preserveFilename); err == nil {
			log.Printf("doDelete preserving %s\n", verifiedDirname)
		} else {
			log.Printf("doDelete removing %s\n", verifiedDirname)
			if err := os.RemoveAll(verifiedDirname); err != nil {
				log.Fatal(err)
			}
		}
	}
	log.Printf("doDelete(%v) done\n", status.Safename)
}

func initializeDirs() {

	objTypes := []string{appImgObj, baseOsObj}

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

	// create the object download directoris
	types.CreateDownloadDirs(objTypes)
}

func handleInitWorkinProgresObjects() {

	objTypes := []string{appImgObj, baseOsObj}

	for _, objType := range objTypes {
		statusDirname := runDirname + "/" + objType + "/status"
		if _, err := os.Stat(statusDirname); err == nil {

			// Don't remove directory since there is a watch on it
			locations, err := ioutil.ReadDir(statusDirname)
			if err != nil {
				log.Fatal(err)
			}
			// Mark as PendingDelete and later purge such entries
			for _, location := range locations {

				if !strings.HasSuffix(location.Name(), ".json") {
					continue
				}
				status := types.VerifyImageStatus{}
				statusFile := statusDirname + "/" + location.Name()
				cb, err := ioutil.ReadFile(statusFile)
				if err != nil {
					log.Printf("%s for %s\n", err, statusFile)
					continue
				}
				if err := json.Unmarshal(cb, &status); err != nil {
					log.Printf("%s file: %s\n",
						err, statusFile)
					continue
				}
				status.PendingDelete = true
				writeVerifyObjectStatus(&status, statusFile)
			}
		}
	}
}

func handleInitMarkedDeletePendingObjects() {

	objTypes := []string{appImgObj, baseOsObj}

	for _, objType := range objTypes {
		statusDirname := runDirname + "/" + objType + "/status"
		if _, err := os.Stat(statusDirname); err == nil {

			// Don't remove directory since there is a watch on it
			locations, err := ioutil.ReadDir(statusDirname)
			if err != nil {
				log.Fatal(err)
			}

			// Delete any still marked as PendingDelete
			for _, location := range locations {

				if !strings.HasSuffix(location.Name(), ".json") {
					continue
				}
				status := types.VerifyImageStatus{}
				statusFile := statusDirname + "/" + location.Name()
				cb, err := ioutil.ReadFile(statusFile)
				if err != nil {
					log.Printf("%s for %s\n", err, statusFile)
					continue
				}
				if err := json.Unmarshal(cb, &status); err != nil {
					log.Printf("%s file: %s\n",
						err, statusFile)
					continue
				}
				if status.PendingDelete {
					log.Printf("still PendingDelete; delete %s\n",
						statusFile)
					if err := os.RemoveAll(statusFile); err != nil {
						log.Fatal(err)
					}
				}
			}
		}
	}
}

// revalidate/recreate status files for verified objects
func handleInitVerifiedObjects() {

	var objTypes = []string{
		appImgObj,
		baseOsObj,
	}

	for _, objType := range objTypes {

		statusDirname := runDirname + "/" + objType + "/status"
		verifiedDirname := objectDownloadDirname + "/" + objType + "/verified"

		if _, err := os.Stat(verifiedDirname); err == nil {
			handleVerifiedObjectsExtended(objType, verifiedDirname,
				statusDirname, "")
		}
	}
}

func handleVerifiedObjectsExtended(objType string, objDirname string,
	statusDirname string, parentDirname string) {

	log.Printf("handleInit(%s, %s, %s)\n", objDirname,
		statusDirname, parentDirname)

	locations, err := ioutil.ReadDir(objDirname)

	if err != nil {
		log.Fatal(err)
	}

	for _, location := range locations {

		filename := objDirname + "/" + location.Name()

		if location.IsDir() {

			log.Printf("handleInit: Looking in %s\n", filename)
			if _, err := os.Stat(filename); err == nil {
				handleVerifiedObjectsExtended(objType, filename,
					statusDirname, location.Name())
			}

		} else {

			log.Printf("handleInit: processing %s\n", filename)
			safename := location.Name()

			// XXX should really re-verify the image on reboot/restart
			// We don't know the URL; Pick a name which is unique
			sha := parentDirname
			if sha != "" {
				safename += "." + sha
			}

			status := types.VerifyImageStatus{
				Safename:    safename,
				ImageSha256: sha,
				ObjType:     objType,
				State:       types.DELIVERED,
			}

			writeVerifyObjectStatus(&status,
				statusDirname+"/"+safename+".json")
		}
	}
}

func updateVerifyErrStatus(status *types.VerifyImageStatus,
	lastErr string, statusFilename string) {
	status.LastErr = lastErr
	status.LastErrTime = time.Now()
	status.PendingAdd = false
	status.State = types.INITIAL
	writeVerifyObjectStatus(status, statusFilename)
}

func writeVerifyObjectStatus(status *types.VerifyImageStatus,
	statusFilename string) {
	b, err := json.Marshal(status)
	if err != nil {
		log.Fatal(err, "json Marshal VerifyImageStatus")
	}
	// We assume a /var/run path hence we don't need to worry about
	// partial writes/empty files due to a kernel crash.
	err = ioutil.WriteFile(statusFilename, b, 0644)
	if err != nil {
		log.Fatal(err, statusFilename)
	}
}

func isStatusFilePresent(config *types.VerifyImageConfig, statusFilename string) bool {

	var isOk bool = true

	if _, err := os.Stat(statusFilename); err == nil {

		log.Printf("handleCreate: status file %s exists\n", config.Safename)

		cb, err := ioutil.ReadFile(statusFilename)
		if err != nil {
			log.Printf("handleCreate: %s for %s\n", err, statusFilename)
			isOk = false
		} else {

			var status types.VerifyImageStatus

			if err := json.Unmarshal(cb, &status); err != nil {
				log.Printf("handleCreate: %s downloadStatus file: %s\n",
					err, statusFilename)
				isOk = false
			} else {
				if status.State >= types.DOWNLOADED {
					log.Printf("handleCreate: download status %v\n",
						status.State)
					isOk = false
				}
			}
		}
	}
	return isOk
}
