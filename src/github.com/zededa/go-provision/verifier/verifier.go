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

// XXX TBD add a signature on the checksum. Verify against root CA.

// XXX TBD separately add support for verifying the signatures on the meta-data (the AIC)

package main

import (
	"crypto/sha256"
	"crypto/x509"
	"crypto"
	"crypto/rsa"
	"crypto/ecdsa"
	"crypto/dsa"
	"encoding/pem"
	"math/big"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/zededa/go-provision/types"
	"github.com/zededa/go-provision/watch"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"
)

var imgCatalogDirname string

func main() {
	log.Printf("Starting verifier\n")

	watch.CleanupRestarted("verifier")

	// Keeping status in /var/run to be clean after a crash/reboot
	baseDirname := "/var/tmp/verifier"
	runDirname := "/var/run/verifier"
	configDirname := baseDirname + "/config"
	statusDirname := runDirname + "/status"
	imgCatalogDirname = "/var/tmp/zedmanager/downloads"

	pendingDirname := imgCatalogDirname + "/pending"
	verifierDirname := imgCatalogDirname + "/verifier"
	verifiedDirname := imgCatalogDirname + "/verified"

	if _, err := os.Stat(baseDirname); err != nil {
		if err := os.MkdirAll(baseDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(configDirname); err != nil {
		if err := os.MkdirAll(configDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(runDirname); err != nil {
		if err := os.MkdirAll(runDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(statusDirname); err != nil {
		if err := os.MkdirAll(statusDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	// Don't remove directory since there is a watch on it
	locations, err := ioutil.ReadDir(statusDirname)
	if err != nil {
		log.Fatal(err)
	}
	for _, location := range locations {
		filename := statusDirname + "/" + location.Name()
		if err := os.RemoveAll(filename); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(imgCatalogDirname); err != nil {
		if err := os.MkdirAll(imgCatalogDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	if _, err := os.Stat(pendingDirname); err != nil {
		if err := os.MkdirAll(pendingDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	// Remove any files which didn't make it past the verifier
	if err := os.RemoveAll(verifierDirname); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(verifierDirname); err != nil {
		if err := os.MkdirAll(verifierDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(verifiedDirname); err != nil {
		if err := os.MkdirAll(verifiedDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}

	// Creates statusDir entries for already verified files
	handleInit(verifiedDirname, statusDirname, "")

	fileChanges := make(chan string)
	go watch.WatchConfigStatusAllowInitialConfig(configDirname,
		statusDirname, fileChanges)
	for {
		change := <-fileChanges
		watch.HandleConfigStatusEvent(change,
			configDirname, statusDirname,
			&types.VerifyImageConfig{},
			&types.VerifyImageStatus{},
			handleCreate, handleModify, handleDelete, nil)
	}
}

// Determine which files we have already verified and set status for them
func handleInit(verifiedDirname string, statusDirname string,
     parentDirname string) {
	fmt.Printf("handleInit(%s, %s, %s)\n",
		verifiedDirname, statusDirname,	parentDirname)
	locations, err := ioutil.ReadDir(verifiedDirname)
	if err != nil {
		log.Fatal(err)
	}
	for _, location := range locations {
		filename := verifiedDirname + "/" + location.Name()
		fmt.Printf("handleInit: Looking in %s\n", filename)
		if location.IsDir() {
			handleInit(filename, statusDirname, location.Name())
		} else {
			status := types.VerifyImageStatus{
				Safename:	location.Name(),
				ImageSha256:	parentDirname,
				State:		types.DELIVERED,
			}
			writeVerifyImageStatus(&status,
				statusDirname + "/" + location.Name() + ".json")
		}
	}
	fmt.Printf("handleInit done for %s, %s, %s\n",
		verifiedDirname, statusDirname,	parentDirname)
	// Report to zedmanager that init is done
	watch.SignalRestarted("verifier")
}

func writeVerifyImageStatus(status *types.VerifyImageStatus,
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

func handleCreate(statusFilename string, configArg interface{}) {
	var config *types.VerifyImageConfig

	switch configArg.(type) {
	default:
		log.Fatal("Can only handle VerifyImageConfig")
	case *types.VerifyImageConfig:
		config = configArg.(*types.VerifyImageConfig)
	}
	log.Printf("handleCreate(%v) for %s\n",
		config.Safename, config.DownloadURL)
	// Start by marking with PendingAdd
	status := types.VerifyImageStatus{
		Safename:	config.Safename,
		ImageSha256:	config.ImageSha256,
		PendingAdd:     true,
		State:		types.DOWNLOADED,
		RefCount:	config.RefCount,
	}
	writeVerifyImageStatus(&status, statusFilename)

	// Form the unique filename in /var/tmp/zedmanager/downloads/pending/
	// based on the claimed Sha256 and safename, and the same name
	// in downloads/verifier/
	// Move to verifier directory which is RO
	// XXX should have dom0 do this and/or have RO mounts
	srcDirname := imgCatalogDirname + "/pending/" + config.ImageSha256
	srcFilename := srcDirname + "/" + config.Safename
	destDirname := imgCatalogDirname + "/verifier/" + config.ImageSha256
	destFilename := destDirname + "/" + config.Safename
	fmt.Printf("Move from %s to %s\n", srcFilename, destFilename)
	if _, err := os.Stat(destDirname); err != nil {
		if err := os.MkdirAll(destDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.Rename(srcFilename, destFilename); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(destDirname, 0500); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(destFilename, 0400); err != nil {
		log.Fatal(err)
	}
	log.Printf("Verifying URL %s file %s\n",
		config.DownloadURL, destFilename)

	f, err := os.Open(destFilename)
	if err != nil {
		status.LastErr = fmt.Sprintf("%v", err)
		status.LastErrTime = time.Now()
		status.State = types.INITIAL
		writeVerifyImageStatus(&status, statusFilename)
		log.Printf("handleCreate failed for %s\n", config.DownloadURL)
		return
	}
	defer f.Close()


	//copmpute sha256 of the image and match it 
	//with the one in config file...
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		status.LastErr = fmt.Sprintf("%v", err)
		status.LastErrTime = time.Now()
		status.State = types.INITIAL
		writeVerifyImageStatus(&status, statusFilename)
		log.Printf("handleCreate failed for %s\n", config.DownloadURL)
		return
	}

	imageHash := h.Sum(nil)
	got := fmt.Sprintf("%x", h.Sum(nil))
	if got != config.ImageSha256 {
		fmt.Printf("got      %s\n", got)
		fmt.Printf("expected %s\n", config.ImageSha256)
		status.LastErr = fmt.Sprintf("got %s expected %s",
			got, config.ImageSha256)
		status.LastErrTime = time.Now()
		status.PendingAdd = false
		status.State = types.INITIAL
		writeVerifyImageStatus(&status, statusFilename)
		log.Printf("handleCreate failed for %s\n", config.DownloadURL)
		return
	}

	//Read the server certificate
        //Decode it and parse it
        //And find out the puplic key and it's type
	//we will use this certificate for both cert chain verification 
        //and signature verification...

	baseCertDirname := "/var/tmp/zedmanager"
	certificateDirname := baseCertDirname+"/cert"
	rootCertDirname := "/opt/zededa/etc"
	rootCertFileName := rootCertDirname+"/root-certificate.pem"

	//This func literal will take care of writing status during 
	//cert chain and signature verification...
	UpdateStatusWhileVerifyingSignature := func (lastErr string){
		status.LastErr = lastErr
		status.LastErrTime = time.Now()
		status.PendingAdd = false
		status.State = types.INITIAL
		writeVerifyImageStatus(&status, statusFilename)
		log.Printf("handleCreate failed for %s\n", config.DownloadURL)
    }

	serverCertName := config.SignatureKey
        serverCertificate, err := ioutil.ReadFile(certificateDirname+"/"+serverCertName)
        if err != nil {
		readCertFailErr := fmt.Sprintf("unable to read the certificate")
		log.Println(readCertFailErr)
		UpdateStatusWhileVerifyingSignature(readCertFailErr)
		fmt.Println(err)
		return
        }
        block, _ := pem.Decode(serverCertificate)
        if block == nil {
		decodeFailedErr := fmt.Sprintf("unable to decode certificate")
		log.Println(decodeFailedErr)
		UpdateStatusWhileVerifyingSignature(decodeFailedErr)
		return
        }
        cert, err := x509.ParseCertificate(block.Bytes)
        if err != nil {
		parseFailedErr := fmt.Sprintf("unable to parse certificate")
		log.Println(parseFailedErr)
		UpdateStatusWhileVerifyingSignature(parseFailedErr)
		return
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
		unableToFindRootCertErr := fmt.Sprintf("failed to find root certificate")
		log.Println(unableToFindRootCertErr)
		UpdateStatusWhileVerifyingSignature(unableToFindRootCertErr)
		return
	}
	ok := roots.AppendCertsFromPEM(rootCertificate)
	if !ok {
		rootParseFailedErr := fmt.Sprintf("failed to parse root certificate")
		log.Println(rootParseFailedErr)
		UpdateStatusWhileVerifyingSignature(rootParseFailedErr)
		return
	}

        length := len(certificateNameInChain)
	for c := 0 ; c < length; c++ { 

		certNameFromChain, err := ioutil.ReadFile(certificateDirname+"/"+certificateNameInChain[c])
		if err != nil {
			fmt.Println(err)
		}
		
		ok := roots.AppendCertsFromPEM(certNameFromChain)
		if !ok {
			intermediateCertParseFailedErr := fmt.Sprintf("failed to parse intermediate certificate")
			log.Println(intermediateCertParseFailedErr)
			UpdateStatusWhileVerifyingSignature(intermediateCertParseFailedErr)
			return
		}
	}
	opts := x509.VerifyOptions{
                Roots:   roots,
        }
        if _, err := cert.Verify(opts); err != nil {
		certChainVerificationErr := fmt.Sprintf("failed to verify certificate chain: ")
		log.Println(certChainVerificationErr)
		UpdateStatusWhileVerifyingSignature(certChainVerificationErr)
		return
        }else {
                log.Println("certificate verified")
        }

        //Read the signature from config file...
        imgSig := config.ImageSignature
        if err != nil {
                log.Println(err)
		signatureNotFoundErr := fmt.Sprintf("image signature not found")
		UpdateStatusWhileVerifyingSignature(signatureNotFoundErr)
		return
        }

        switch pub := cert.PublicKey.(type) {
        case *rsa.PublicKey:

                err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, imageHash, imgSig)
                if err != nil {
			log.Fatalf("VerifyPKCS1v15 failed...")
			rsaSignatureVerificationFiledErr := fmt.Sprintf("rsa image signature verification failed")
			UpdateStatusWhileVerifyingSignature(rsaSignatureVerificationFiledErr)
			return

                } else {
                        log.Printf("VerifyPKCS1v15 successful...")
                }

        case *dsa.PublicKey:

                log.Printf("pub is of type DSA: ",pub)

        case *ecdsa.PublicKey:

                log.Printf("pub is of type ecdsa: ",pub)
                imgSignature, err := base64.StdEncoding.DecodeString(string(imgSig))
                if err != nil {
                        fmt.Println("DecodeString: ", err)
                }
                log.Printf("Decoded imgSignature (len %d): % x\n", len(imgSignature), imgSignature)
                rbytes := imgSignature[0:32]
                sbytes := imgSignature[32:]
                fmt.Printf("Decoded r %d s %d\n", len(rbytes), len(sbytes))
                r := new(big.Int)
                s := new(big.Int)
                r.SetBytes(rbytes)
                s.SetBytes(sbytes)
                log.Printf("Decoded r, s: %v, %v\n", r, s)
		ok := ecdsa.Verify(pub, imageHash, r, s)
		if !ok {
			log.Printf("ecdsa.Verify failed")
			ecdsaSignatureVerificationFiledErr := fmt.Sprintf("ecdsa image signature verification failed ")
			UpdateStatusWhileVerifyingSignature(ecdsaSignatureVerificationFiledErr)
			return
		}
		log.Printf("Signature verified\n")

        default:
		unknownPublicKeyTypeErr := fmt.Sprintf("unknown type of public key")
		log.Println(unknownPublicKeyTypeErr)
		UpdateStatusWhileVerifyingSignature(unknownPublicKeyTypeErr)
                return
        }

	// Move directory from downloads/verifier to downloads/verified
	// XXX should have dom0 do this and/or have RO mounts
	finalDirname := imgCatalogDirname + "/verified/" + config.ImageSha256
	// Drop URL all but last part of URL. Note that '/' was converted
	// to '_' in Safename
	comp := strings.Split(config.Safename, "_")
	last := comp[len(comp)-1]
	// Drop "."sha256 tail part of Safename
	i := strings.LastIndex(last, ".")
	if i == -1 {
		log.Fatal("Malformed safename with no .sha256",
			config.Safename)
	}
	last = last[0:i]
	finalFilename := finalDirname + "/" + last
	fmt.Printf("Move from %s to %s\n", destFilename, finalFilename)
	// XXX change log.Fatal to something else?
	if _, err := os.Stat(finalDirname); err == nil {
		// Directory exists thus we have a sha256 collision presumably
		// due to multiple safenames (i.e., URLs) for the same content.
		// Delete existing to avoid wasting space.
		locations, err := ioutil.ReadDir(finalDirname)
		if err != nil {
			log.Fatal(err)
		}
		for _, location := range locations {
			log.Printf("Identical sha256 (%s) for safenames %s and %s; deleting old\n",
				config.ImageSha256, location.Name(),
				config.Safename)
		}

		if err := os.RemoveAll(finalDirname); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.MkdirAll(finalDirname, 0700); err != nil {
		log.Fatal( err)
	}
	if err := os.Rename(destFilename, finalFilename); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(finalDirname, 0500); err != nil {
		log.Fatal(err)
	}


	status.PendingAdd = false
	status.State = types.DELIVERED
	writeVerifyImageStatus(&status, statusFilename)
	log.Printf("handleCreate done for %s\n", config.DownloadURL)
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

	// If identical we do nothing. Otherwise we do a delete and create.
	if config.Safename == status.Safename &&
	   config.ImageSha256 == status.ImageSha256 {
		log.Printf("handleModify: no change for %s\n",
			config.DownloadURL)
		return
	}

	status.PendingModify = true
	writeVerifyImageStatus(status, statusFilename)
	handleDelete(statusFilename, status)
	handleCreate(statusFilename, config)
	status.PendingModify = false
	writeVerifyImageStatus(status, statusFilename)
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

	// Write out what we modified to VerifyImageStatus aka delete
	if err := os.Remove(statusFilename); err != nil {
		log.Println(err)
	}
	log.Printf("handleDelete done for %s\n", status.Safename)
}
