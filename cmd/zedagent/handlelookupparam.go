package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/RevH/ipinfo"
	"github.com/satori/go.uuid"
	"github.com/zededa/api/zconfig"
	"github.com/zededa/go-provision/types"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
)

const tmpDirname = "/var/tmp/zededa"

// Assumes the config files are in dirName, which is /opt/zededa/etc
// by default. The files are
//  device.cert.pem,
//  device.key.pem		Device certificate/key created before this
//  		     		client is started.
//  infra			If this file exists assume zedcontrol and do not
//  				create ACLs
//  /var/tmp/zededa/zedserverconfig Written by us; zed server EIDs
//  /var/tmp/zededa/uuid	Written by us
//
func handleLookUpParam(devConfig *zconfig.EdgeDevConfig) {

	dirName := "/opt/zededa/etc"
	zedRouterConfigbaseDir := "/var/tmp/zedrouter/config/"
	deviceCertName := dirName + "/device.cert.pem"
	deviceKeyName := dirName + "/device.key.pem"
	infraFileName := dirName + "/infra"
	zedserverConfigFileName := tmpDirname + "/zedserverconfig"
	uuidFileName := tmpDirname + "/uuid"

	//Fill DeviceDb struct with LispInfo config...
	var device = types.DeviceDb{}

	log.Printf("handleLookupParam got config %v\n", devConfig)
	lispInfo := devConfig.LispInfo
	if lispInfo == nil {
		log.Printf("handleLookupParam: missing lispInfo\n")
	}
	device.LispInstance = lispInfo.LispInstance
	device.EID = net.ParseIP(lispInfo.EID)
	device.EIDHashLen = uint8(lispInfo.EIDHashLen)
	device.EidAllocationPrefix = lispInfo.EidAllocationPrefix
	device.EidAllocationPrefixLen = int(lispInfo.EidAllocationPrefixLen)
	device.ClientAddr = lispInfo.ClientAddr
	device.LispMapServers = make([]types.LispServerInfo, len(lispInfo.LispMapServers))
	var lmsx int = 0
	for _, lms := range lispInfo.LispMapServers {

		lispServerDetail := new(types.LispServerInfo)
		lispServerDetail.NameOrIp = lms.NameOrIp
		lispServerDetail.Credential = lms.Credential
		device.LispMapServers[lmsx] = *lispServerDetail
		lmsx++
	}
	device.ZedServers.NameToEidList = make([]types.NameToEid, len(lispInfo.ZedServers))
	var zsx int = 0
	for _, zs := range lispInfo.ZedServers {

		nameToEidInfo := new(types.NameToEid)
		nameToEidInfo.HostName = zs.HostName
		nameToEidInfo.EIDs = make([]net.IP, len(zs.EID))
		var eidx int = 0
		for _, eid := range zs.EID {
			nameToEidInfo.EIDs[eidx] = net.ParseIP(eid)
			eidx++
		}
		device.ZedServers.NameToEidList[zsx] = *nameToEidInfo
		zsx++
	}

	// Load device cert
	deviceCert, err := tls.LoadX509KeyPair(deviceCertName,
		deviceKeyName)
	if err != nil {
		log.Fatal(err)
	}

	ACLPromisc := false
	if _, err := os.Stat(infraFileName); err == nil {
		fmt.Printf("Setting ACLPromisc\n")
		ACLPromisc = true
	}

	var addInfoDevice *types.AdditionalInfoDevice
	// Determine location information and use as AdditionalInfo
	if myIP, err := ipinfo.MyIP(); err == nil {
		addInfo := types.AdditionalInfoDevice{
			UnderlayIP: myIP.IP,
			Hostname:   myIP.Hostname,
			City:       myIP.City,
			Region:     myIP.Region,
			Country:    myIP.Country,
			Loc:        myIP.Loc,
			Org:        myIP.Org,
		}
		addInfoDevice = &addInfo
	}

	var devUUID uuid.UUID
	if _, err := os.Stat(uuidFileName); err != nil {
		// Create and write with initial values
		// XXX ignoring any error
		devUUID, _ = uuid.NewV4()
		b := []byte(fmt.Sprintf("%s\n", devUUID))
		err = ioutil.WriteFile(uuidFileName, b, 0644)
		if err != nil {
			log.Fatal("WriteFile", err, uuidFileName)
		}
		fmt.Printf("Created UUID %s\n", devUUID)
	} else {
		b, err := ioutil.ReadFile(uuidFileName)
		if err != nil {
			log.Fatal("ReadFile", err, uuidFileName)
		}
		uuidStr := strings.TrimSpace(string(b))
		devUUID, err = uuid.FromString(uuidStr)
		if err != nil {
			log.Fatal("uuid.FromString", err, string(b))
		}
		fmt.Printf("Read UUID %s\n", devUUID)
	}

	// If we got a StatusNotFound the EID will be zero
	if device.EID == nil {
		log.Printf("Did not receive an EID\n")
		os.Remove(zedserverConfigFileName)
		return
	}

	// XXX add Redirect support and store + retry
	// XXX try redirected once and then fall back to original; repeat
	// XXX once redirect successful, then save server and rootCert

	// Convert from IID and IPv6 EID to a string with
	// [iid]eid, where the eid uses the textual format defined in
	// RFC 5952. The iid is printed as an integer.
	sigdata := fmt.Sprintf("[%d]%s",
		device.LispInstance, device.EID.String())
	fmt.Printf("sigdata (len %d) %s\n", len(sigdata), sigdata)

	hasher := sha256.New()
	hasher.Write([]byte(sigdata))
	hash := hasher.Sum(nil)
	fmt.Printf("hash (len %d) % x\n", len(hash), hash)
	fmt.Printf("base64 hash %s\n",
		base64.StdEncoding.EncodeToString(hash))

	var signature string
	switch deviceCert.PrivateKey.(type) {
	default:
		log.Fatal("Private Key RSA type not supported")
	case *ecdsa.PrivateKey:
		key := deviceCert.PrivateKey.(*ecdsa.PrivateKey)
		r, s, err := ecdsa.Sign(rand.Reader, key, hash)
		if err != nil {
			log.Fatal("ecdsa.Sign: ", err)
		}
		fmt.Printf("r.bytes %d s.bytes %d\n", len(r.Bytes()),
			len(s.Bytes()))
		sigres := r.Bytes()
		sigres = append(sigres, s.Bytes()...)
		fmt.Printf("sigres (len %d): % x\n", len(sigres), sigres)
		signature = base64.StdEncoding.EncodeToString(sigres)
		fmt.Println("signature:", signature)
	}
	fmt.Printf("UserName %s\n", device.UserName)
	fmt.Printf("MapServers %s\n", device.LispMapServers)
	fmt.Printf("Lisp IID %d\n", device.LispInstance)
	fmt.Printf("EID %s\n", device.EID)
	fmt.Printf("EID hash length %d\n", device.EIDHashLen)

	// write zedserverconfig file with hostname to EID mappings
	f, err := os.Create(zedserverConfigFileName)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	for _, ne := range device.ZedServers.NameToEidList {
		for _, eid := range ne.EIDs {
			output := fmt.Sprintf("%-46v %s\n",
				eid, ne.HostName)
			_, err := f.WriteString(output)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
	f.Sync()

	// Determine whether NAT is in use
	if publicIP, err := addrStringToIP(device.ClientAddr); err != nil {
		log.Printf("Failed to convert %s, error %s\n",
			device.ClientAddr, err)
	} else {
		nat := !IsMyAddress(publicIP)
		fmt.Printf("NAT %v, publicIP %v\n", nat, publicIP)
	}

	// Write an AppNetworkConfig for the ZedManager application
	uv := types.UUIDandVersion{
		UUID:    devUUID,
		Version: "0",
	}
	config := types.AppNetworkConfig{
		UUIDandVersion: uv,
		DisplayName:    "zedmanager",
		IsZedmanager:   true,
	}

	olconf := make([]types.OverlayNetworkConfig, 1)
	config.OverlayNetworkList = olconf
	olconf[0].IID = device.LispInstance
	olconf[0].EID = device.EID
	olconf[0].LispSignature = signature
	olconf[0].AdditionalInfoDevice = addInfoDevice
	olconf[0].NameToEidList = device.ZedServers.NameToEidList
	lispServers := make([]types.LispServerInfo, len(device.LispMapServers))
	olconf[0].LispServers = lispServers
	for count, lispMapServer := range device.LispMapServers {
		lispServers[count].NameOrIp = lispMapServer.NameOrIp
		lispServers[count].Credential = lispMapServer.Credential
	}
	acl := make([]types.ACE, 1)
	olconf[0].ACLs = acl
	matches := make([]types.ACEMatch, 1)
	acl[0].Matches = matches
	actions := make([]types.ACEAction, 1)
	acl[0].Actions = actions
	if ACLPromisc {
		matches[0].Type = "ip"
		matches[0].Value = "::/0"
	} else {
		matches[0].Type = "eidset"
	}
	zedrouterConfigFileName := zedRouterConfigbaseDir + "" + devUUID.String() + ".json"
	writeNetworkConfig(&config, zedrouterConfigFileName)
}

func writeNetworkConfig(config *types.AppNetworkConfig,
	configFilename string) {
	fmt.Printf("Writing AppNetworkConfig to %s\n", configFilename)
	b, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal AppNetworkConfig")
	}
	err = ioutil.WriteFile(configFilename, b, 0644)
	if err != nil {
		log.Fatal(err, configFilename)
	}
}

func addrStringToIP(addrString string) (net.IP, error) {
	clientTCP, err := net.ResolveTCPAddr("tcp", addrString)
	if err != nil {
		return net.IP{}, err
	}
	return clientTCP.IP, nil
}

// IsMyAddress checks the IP address against the local IPs. Returns True if
// there is a match.
func IsMyAddress(clientIP net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok &&
			!ipnet.IP.IsLoopback() {
			if bytes.Compare(ipnet.IP, clientIP) == 0 {
				return true
			}
		}
	}
	return false
}
