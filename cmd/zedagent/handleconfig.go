// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/zededa/api/zconfig"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	MaxReaderSmall      = 1 << 16 // 64k
	MaxReaderMaxDefault = MaxReaderSmall
	MaxReaderMedium     = 1 << 19 // 512k
	MaxReaderHuge       = 1 << 21 // two megabytes
	configTickTimeout   = 1       // in minutes
)

var configApi string = "api/v1/edgedevice/config"
var statusApi string = "api/v1/edgedevice/info"
var metricsApi string = "api/v1/edgedevice/metrics"

// XXX remove global variables
var activeVersion string
var configUrl string
var deviceId string
var metricsUrl string
var statusUrl string

var serverFilename string = "/opt/zededa/etc/server"

var dirName string = "/opt/zededa/etc"
var deviceCertName string = dirName + "/device.cert.pem"
var deviceKeyName string = dirName + "/device.key.pem"
var rootCertName string = dirName + "/root-certificate.pem"

// XXX remove global variables
var deviceCert tls.Certificate
var cloudClient *http.Client

func getCloudUrls() {

	// get the server name
	bytes, err := ioutil.ReadFile(serverFilename)
	if err != nil {
		log.Fatal(err)
	}
	strTrim := strings.TrimSpace(string(bytes))
	serverName := strings.Split(strTrim, ":")[0]

	configUrl = serverName + "/" + configApi
	statusUrl = serverName + "/" + statusApi
	metricsUrl = serverName + "/" + metricsApi

	deviceCert, err = tls.LoadX509KeyPair(deviceCertName, deviceKeyName)
	if err != nil {
		log.Fatal(err)
	}
	// Load CA cert
	caCert, err := ioutil.ReadFile(rootCertName)
	if err != nil {
		log.Fatal(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{deviceCert},
		ServerName:   serverName,
		RootCAs:      caCertPool,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		// TLS 1.2 because we can
		MinVersion: tls.VersionTLS12,
	}
	tlsConfig.BuildNameToCertificate()

	log.Printf("Connecting to %s\n", serverName)

	transport := &http.Transport{TLSClientConfig: tlsConfig}
	cloudClient = &http.Client{Transport: transport}
}

// got a trigger for new config. check the present version and compare
// if this is a new version, initiate update
//  compare the old version config with the new one
// delete if some thing is not present in the old config
// for the new config create entries in the zMgerConfig Dir
// for each of the above buckets

func configTimerTask() {

	log.Println("starting config fetch timer task")
	getLatestConfig(nil, configUrl)

	ticker := time.NewTicker(time.Minute * configTickTimeout)

	for t := range ticker.C {
		log.Println(t)
		getLatestConfig(nil, configUrl)
	}
}

func getLatestConfig(deviceCert []byte, configUrl string) {

	log.Printf("config-url: %s\n", configUrl)
	resp, err := cloudClient.Get("https://" + configUrl)

	if err != nil {
		log.Printf("URL get fail: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if err := validateConfigMessage(resp); err != nil {
		log.Println("validateConfigMessage: ", err)
		return
	}
	config, err := readDeviceConfigProtoMessage(resp)
	if err != nil {
		log.Println("readDeviceConfigProtoMessage: ", err)
		return
	}
	publishDeviceConfig(config)
}

func validateConfigMessage(r *http.Response) error {

	var ctTypeStr = "Content-Type"
	var ctTypeProtoStr = "application/x-proto-binary"

	switch r.StatusCode {
	case http.StatusOK:
		log.Printf("validateConfigMessage StatusOK\n")
	default:
		log.Printf("validateConfigMessage statuscode %d %s\n",
			r.StatusCode, http.StatusText(r.StatusCode))
		log.Printf("received response %v\n", r)
		return fmt.Errorf("http status %d %s",
			r.StatusCode, http.StatusText(r.StatusCode))
	}
	ct := r.Header.Get(ctTypeStr)
	if ct == "" {
		return fmt.Errorf("No content-type")
	}
	mimeType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return fmt.Errorf("Get Content-type error")
	}
	switch mimeType {
	case ctTypeProtoStr:
		return nil
	default:
		return fmt.Errorf("Content-type %s not supported",
			mimeType)
	}
}

func readDeviceConfigProtoMessage(r *http.Response) (*zconfig.EdgeDevConfig, error) {

	var config = &zconfig.EdgeDevConfig{}

	bytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	log.Printf("parsing proto %d bytes\n", len(bytes))
	err = proto.Unmarshal(bytes, config)
	if err != nil {
		log.Println("Unmarshalling failed: %v", err)
		return nil, err
	}
	return config, nil
}

func publishDeviceConfig(config *zconfig.EdgeDevConfig) {

	log.Printf("Publishing config %v\n", config)

	// if they match return
	var devId = &zconfig.UUIDandVersion{}

	devId = config.GetId()
	if devId != nil {
		// store the device id
		deviceId = devId.Uuid
		if devId.Version == activeVersion {
			log.Printf("Same version, skipping:%v\n", config.Id.Version)
			return
		}
		activeVersion = devId.Version
	}

	handleLookUpParam(config)

	// delete old app configs, if any
	checkCurrentAppFiles(config)

	// delete old baseOs configs, if any
	checkCurrentBaseOsFiles(config)

	// handle any new baseOs/App config
	parseConfig(config)
}

func checkCurrentBaseOsFiles(config *zconfig.EdgeDevConfig) {

	// get the current set of baseOs files
	curBaseOsFilenames, err := ioutil.ReadDir(zedagentBaseOsConfigDirname)

	if err != nil {
		log.Printf("read dir %s fail, err: %v\n", zedagentBaseOsConfigDirname, err)
		curBaseOsFilenames = nil
	}

	baseOses := config.GetBase()
	// delete any baseOs config which is not present in the new set
	for _, curBaseOs := range curBaseOsFilenames {
		curBaseOsFilename := curBaseOs.Name()

		// file type json
		if strings.HasSuffix(curBaseOsFilename, ".json") {
			found := false
			for _, baseOs := range baseOses {
				baseOsFilename := baseOs.Uuidandversion.Uuid + ".json"
				if baseOsFilename == curBaseOsFilename {
					found = true
					break
				}
			}
			// baseOS instance not found, delete
			if !found {
				log.Printf("Remove baseOs config %s\n", curBaseOsFilename)
				err := os.Remove(zedagentBaseOsConfigDirname + "/" + curBaseOsFilename)
				if err != nil {
					log.Println("Old config: ", err)
				}
			}
		}
	}
}

func checkCurrentAppFiles(config *zconfig.EdgeDevConfig) {

	// get the current set of App files
	curAppFilenames, err := ioutil.ReadDir(zedmanagerConfigDirname)

	if err != nil {
		log.Printf("read dir %s fail, err: %v\n", zedmanagerConfigDirname, err)
		curAppFilenames = nil
	}

	Apps := config.GetApps()
	// delete any app instances which are not present in the new set
	for _, curApp := range curAppFilenames {
		curAppFilename := curApp.Name()

		// file type json
		if strings.HasSuffix(curAppFilename, ".json") {
			found := false
			for _, app := range Apps {
				appFilename := app.Uuidandversion.Uuid + ".json"
				if appFilename == curAppFilename {
					found = true
					break
				}
			}
			// app instance not found, delete
			if !found {
				log.Printf("Remove app config %s\n", curAppFilename)
				err := os.Remove(zedmanagerConfigDirname + "/" + curAppFilename)
				if err != nil {
					log.Println("Old config: ", err)
				}
			}
		}
	}
}
