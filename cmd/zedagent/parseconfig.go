package main

import (
	"encoding/json"
	"fmt"
	"github.com/satori/go.uuid"
	"github.com/zededa/api/zconfig"
	"github.com/zededa/go-provision/types"
	"io/ioutil"
	"log"
	"net"
	"strings"
)

const (
	MaxBaseOsCount = 2
)

func parseConfig(config *zconfig.EdgeDevConfig) {

	log.Println("Applying new config")
	if validateConfig(config) == true {
		parseBaseOsConfig(config)
		parseAppInstanceConfig(config)
	}
}

func validateConfig(config *zconfig.EdgeDevConfig) bool {

	//XXX:FIXME, check if any validation required
	return true
}

func parseBaseOsConfig(config *zconfig.EdgeDevConfig) {

	log.Println("Applying Base Os config")

	cfgOsList := config.GetBase()

	if len(cfgOsList) == 0 {
		return
	}

	baseOsList := make([]types.BaseOsConfig, len(cfgOsList))

	for idx, cfgOs := range cfgOsList {

		baseOs := new(types.BaseOsConfig)

		baseOs.UUIDandVersion.UUID, _ = uuid.FromString(cfgOs.Uuidandversion.Uuid)
		baseOs.UUIDandVersion.Version = cfgOs.Uuidandversion.Version

		baseOs.Activate = cfgOs.GetActivate()
		baseOs.BaseOsVersion = cfgOs.GetBaseOSVersion()

		cfgOsDetails := cfgOs.GetBaseOSDetails()
		cfgOsParamList := cfgOsDetails.GetBaseOSParams()

		for jdx, cfgOsDetail := range cfgOsParamList {
			param := new(types.OsVerParams)
			param.OSVerKey = cfgOsDetail.GetOSVerKey()
			param.OSVerValue = cfgOsDetail.GetOSVerValue()
			baseOs.OsParams[jdx] = *param
		}

		var imageCount int
		for _, drive := range cfgOs.Drives {
			if drive.Image != nil {
				imageId := drive.Image.DsId

				for _, dsEntry := range config.Datastores {
					if dsEntry.Id == imageId {
						imageCount++
						break
					}
				}
			}
		}

		if imageCount != 0 {
			baseOs.StorageConfigList = make([]types.StorageConfig, imageCount)
			parseStorageConfigList(config, baseOsObj, baseOs.StorageConfigList,
				cfgOs.Drives)
		}

		baseOsList[idx] = *baseOs

		getCertObjects(baseOs.UUIDandVersion, baseOs.ConfigSha256,
			baseOs.StorageConfigList)

		// Dump the config content
		bytes, err := json.Marshal(baseOs)

		if err == nil {
			log.Printf("New/updated BaseOs %d: %s\n", idx, bytes)
		}
	}

	if validateBaseOsConfig(baseOsList) == true {
		createBaseOsConfig(baseOsList)
	}
}

func parseAppInstanceConfig(config *zconfig.EdgeDevConfig) {

	var appInstance = types.AppInstanceConfig{}

	log.Println("Applying App Instance config")

	Apps := config.GetApps()

	for _, cfgApp := range Apps {

		log.Printf("New/updated app instance %v\n", cfgApp)

		appInstance.UUIDandVersion.UUID, _ = uuid.FromString(cfgApp.Uuidandversion.Uuid)
		appInstance.UUIDandVersion.Version = cfgApp.Uuidandversion.Version
		appInstance.DisplayName = cfgApp.Displayname
		appInstance.Activate = cfgApp.Activate

		appInstance.FixedResources.Kernel = cfgApp.Fixedresources.Kernel
		appInstance.FixedResources.BootLoader = cfgApp.Fixedresources.Bootloader
		appInstance.FixedResources.Ramdisk = cfgApp.Fixedresources.Ramdisk
		appInstance.FixedResources.MaxMem = int(cfgApp.Fixedresources.Maxmem)
		appInstance.FixedResources.Memory = int(cfgApp.Fixedresources.Memory)
		appInstance.FixedResources.RootDev = cfgApp.Fixedresources.Rootdev
		appInstance.FixedResources.VCpus = int(cfgApp.Fixedresources.Vcpus)

		var imageCount int
		for _, drive := range cfgApp.Drives {
			if drive.Image != nil {
				imageId := drive.Image.DsId

				for _, dsEntry := range config.Datastores {
					if dsEntry.Id == imageId {
						imageCount++
						break
					}
				}
			}
		}

		if imageCount != 0 {
			appInstance.StorageConfigList = make([]types.StorageConfig, imageCount)
			parseStorageConfigList(config, appImgObj, appInstance.StorageConfigList,
				cfgApp.Drives)
		}

		// fill the overlay/underlay config
		parseNetworkConfig(&appInstance, cfgApp, config.Networks)

		// get the certs for image sha verification
		getCertObjects(appInstance.UUIDandVersion,
			appInstance.ConfigSha256, appInstance.StorageConfigList)

		if validateAppInstanceConfig(appInstance) == true {

			// write to zedmanager config directory
			appFilename := cfgApp.Uuidandversion.Uuid

			writeAppInstanceConfig(appInstance, appFilename)
		}
	}
}

func parseStorageConfigList(config *zconfig.EdgeDevConfig, objType string,
	storageList []types.StorageConfig,
	drives []*zconfig.Drive) {

	var idx int = 0

	for _, drive := range drives {

		found := false

		image := new(types.StorageConfig)
		for _, ds := range config.Datastores {

			if drive.Image != nil &&
				drive.Image.DsId == ds.Id {

				found = true
				image.DownloadURL = ds.Fqdn + "/" + ds.Dpath + "/" + drive.Image.Name
				image.TransportMethod = ds.DType.String()
				image.ApiKey = ds.ApiKey
				image.Password = ds.Password
				image.Dpath = ds.Dpath
				break
			}
		}

		if found == false {
			continue
		}

		image.Format = strings.ToLower(drive.Image.Iformat.String())
		image.MaxSize = uint(drive.Maxsize)
		image.ReadOnly = drive.Readonly
		image.Preserve = drive.Preserve
		image.Target = strings.ToLower(drive.Target.String())
		image.Devtype = strings.ToLower(drive.Drvtype.String())
		image.ImageSignature = drive.Image.Siginfo.Signature
		image.ImageSha256 = drive.Image.Sha256
		image.ObjType = objType

		// copy the certificates
		if drive.Image.Siginfo.Signercerturl != "" {
			image.SignatureKey = drive.Image.Siginfo.Signercerturl
			image.NeedVerification = true
		}

		// XXX:FIXME certificates can be many
		// this list, currently contains the certUrls
		// should be the sha/uuid of cert filenames
		// as proper DataStore Entries

		if drive.Image.Siginfo.Intercertsurl != "" {
			image.CertificateChain = make([]string, 1)
			image.CertificateChain[0] = drive.Image.Siginfo.Intercertsurl
		}

		storageList[idx] = *image
		idx++
	}
}

func parseNetworkConfig(appInstance *types.AppInstanceConfig,
	cfgApp *zconfig.AppInstanceConfig,
	cfgNetworks []*zconfig.NetworkConfig) {

	var ulMaxIdx int = 0
	var olMaxIdx int = 0

	// count the interfaces and allocate
	for _, intfEnt := range cfgApp.Interfaces {
		for _, netEnt := range cfgNetworks {

			if intfEnt.NetworkId == netEnt.Id {

				switch strings.ToLower(netEnt.Type.String()) {
				// underlay interface
				case "v4", "v6":
					{
						ulMaxIdx++
						break
					}
				// overlay interface
				case "lisp":
					{
						olMaxIdx++
						break
					}
				}
			}
		}
	}

	if ulMaxIdx != 0 {
		appInstance.UnderlayNetworkList = make([]types.UnderlayNetworkConfig, ulMaxIdx)
		parseUnderlayNetworkConfig(appInstance, cfgApp, cfgNetworks)
	}

	if olMaxIdx != 0 {
		appInstance.OverlayNetworkList = make([]types.EIDOverlayConfig, olMaxIdx)
		parseOverlayNetworkConfig(appInstance, cfgApp, cfgNetworks)
	}
}

func parseUnderlayNetworkConfig(appInstance *types.AppInstanceConfig,
	cfgApp *zconfig.AppInstanceConfig,
	cfgNetworks []*zconfig.NetworkConfig) {

	var ulIdx int = 0

	for _, intfEnt := range cfgApp.Interfaces {
		for _, netEnt := range cfgNetworks {

			if intfEnt.NetworkId == netEnt.Id &&
				(strings.ToLower(netEnt.Type.String()) == "v4" ||
					strings.ToLower(netEnt.Type.String()) == "v6") {

				nv4 := netEnt.GetNv4() //XXX not required now...
				if nv4 != nil {
					booValNv4 := nv4.Dhcp
					log.Println("booValNv4: ", booValNv4)
				}
				nv6 := netEnt.GetNv6() //XXX not required now...
				if nv6 != nil {
					booValNv6 := nv6.Dhcp
					log.Println("booValNv6: ", booValNv6)
				}

				ulCfg := new(types.UnderlayNetworkConfig)
				ulCfg.ACLs = make([]types.ACE, len(intfEnt.Acls))

				for aclIdx, acl := range intfEnt.Acls {

					aclCfg := new(types.ACE)
					aclCfg.Matches = make([]types.ACEMatch, len(acl.Matches))
					aclCfg.Actions = make([]types.ACEAction, len(acl.Actions))

					for matchIdx, match := range acl.Matches {
						matchCfg := new(types.ACEMatch)
						matchCfg.Type = match.Type
						matchCfg.Value = match.Value
						aclCfg.Matches[matchIdx] = *matchCfg
					}

					for actionIdx, action := range acl.Actions {
						actionCfg := new(types.ACEAction)
						actionCfg.Limit = action.Limit
						actionCfg.LimitRate = int(action.Limitrate)
						actionCfg.LimitUnit = action.Limitunit
						actionCfg.LimitBurst = int(action.Limitburst)
						// XXX:FIXME actionCfg.Drop = <TBD>
						aclCfg.Actions[actionIdx] = *actionCfg
					}
					ulCfg.ACLs[aclIdx] = *aclCfg
				}
				appInstance.UnderlayNetworkList[ulIdx] = *ulCfg
				ulIdx++
			}
		}
	}
}

func parseOverlayNetworkConfig(appInstance *types.AppInstanceConfig,
	cfgApp *zconfig.AppInstanceConfig,
	cfgNetworks []*zconfig.NetworkConfig) {
	var olIdx int = 0

	for _, intfEnt := range cfgApp.Interfaces {
		for _, netEnt := range cfgNetworks {

			if intfEnt.NetworkId == netEnt.Id &&
				strings.ToLower(netEnt.Type.String()) == "lisp" {

				olCfg := new(types.EIDOverlayConfig)
				olCfg.ACLs = make([]types.ACE, len(intfEnt.Acls))

				for aclIdx, acl := range intfEnt.Acls {

					aclCfg := new(types.ACE)
					aclCfg.Matches = make([]types.ACEMatch, len(acl.Matches))
					aclCfg.Actions = make([]types.ACEAction, len(acl.Actions))

					for matchIdx, match := range acl.Matches {
						matchCfg := new(types.ACEMatch)
						matchCfg.Type = match.Type
						matchCfg.Value = match.Value
						aclCfg.Matches[matchIdx] = *matchCfg
					}

					for actionIdx, action := range acl.Actions {
						actionCfg := new(types.ACEAction)
						actionCfg.Limit = action.Limit
						actionCfg.LimitRate = int(action.Limitrate)
						actionCfg.LimitUnit = action.Limitunit
						actionCfg.LimitBurst = int(action.Limitburst)
						aclCfg.Actions[actionIdx] = *actionCfg
					}
					olCfg.ACLs[aclIdx] = *aclCfg
				}

				olCfg.EIDConfigDetails.EID = net.ParseIP(intfEnt.Addr)
				olCfg.EIDConfigDetails.LispSignature = intfEnt.Lispsignature
				olCfg.EIDConfigDetails.PemCert = intfEnt.Pemcert
				olCfg.EIDConfigDetails.PemPrivateKey = intfEnt.Pemprivatekey

				nlisp := netEnt.GetNlisp()

				if nlisp != nil {

					if nlisp.Eidalloc != nil {

						olCfg.EIDConfigDetails.IID = nlisp.Iid
						olCfg.EIDConfigDetails.EIDAllocation.Allocate = nlisp.Eidalloc.Allocate
						olCfg.EIDConfigDetails.EIDAllocation.ExportPrivate = nlisp.Eidalloc.Exportprivate
						olCfg.EIDConfigDetails.EIDAllocation.AllocationPrefix = nlisp.Eidalloc.Allocationprefix
						olCfg.EIDConfigDetails.EIDAllocation.AllocationPrefixLen = int(nlisp.Eidalloc.Allocationprefixlen)
					}

					if len(nlisp.Nmtoeid) != 0 {

						olCfg.NameToEidList = make([]types.NameToEid, len(nlisp.Nmtoeid))

						for nameIdx, nametoeid := range nlisp.Nmtoeid {

							nameCfg := new(types.NameToEid)
							nameCfg.HostName = nametoeid.Hostname
							nameCfg.EIDs = make([]net.IP, len(nametoeid.Eids))

							for eIdx, eid := range nametoeid.Eids {
								nameCfg.EIDs[eIdx] = net.ParseIP(eid)
							}

							olCfg.NameToEidList[nameIdx] = *nameCfg
						}
					}
				} else {
					log.Printf("No Nlisp in for %v\n", netEnt.Id)
				}

				appInstance.OverlayNetworkList[olIdx] = *olCfg
				olIdx++
			}
		}
	}
}

func writeAppInstanceConfig(appInstance types.AppInstanceConfig,
	appFilename string) {

	log.Printf("Writing app instance UUID %s\n", appFilename)
	bytes, err := json.Marshal(appInstance)
	if err != nil {
		log.Fatal(err, "json Marshal AppInstanceConfig")
	}
	configFilename := zedmanagerConfigDirname + "/" + appFilename + ".json"
	err = ioutil.WriteFile(configFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func writeBaseOsConfig(baseOsConfig types.BaseOsConfig,
	configFilename string) {

	bytes, err := json.Marshal(baseOsConfig)

	if err != nil {
		log.Fatal(err, "json Marshal BaseOsConfig")
	}

	log.Printf("Writing baseOs config UUID %s, %s\n", configFilename, bytes)

	err = ioutil.WriteFile(configFilename, bytes, 0644)

	if err != nil {
		log.Fatal(err)
	}
}

func writeBaseOsStatus(baseOsStatus *types.BaseOsStatus,
	statusFilename string) {

	log.Printf("Writing baseOs status UUID %s\n", statusFilename)

	bytes, err := json.Marshal(baseOsStatus)
	if err != nil {
		log.Fatal(err, "json Marshal BaseOsStatus")
	}

	err = ioutil.WriteFile(statusFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func getCertObjects(uuidAndVersion types.UUIDandVersion,
	sha256 string, drives []types.StorageConfig) {

	var cidx int = 0
	for _, image := range drives {
		if image.SignatureKey != "" {
			cidx++
		}
		for _, certUrl := range image.CertificateChain {
			if certUrl != "" {
				cidx++
			}
		}
	}

	if cidx == 0 {
		return
	}

	var config = &types.CertObjConfig{}
	var certConfigFilename = uuidAndVersion.UUID.String()
	var configFilename = fmt.Sprintf("%s/%s.json",
		zedagentCertObjConfigDirname, certConfigFilename)

	config.UUIDandVersion = uuidAndVersion
	config.ConfigSha256 = sha256
	config.StorageConfigList = make([]types.StorageConfig, cidx)

	cidx = 0
	for _, image := range drives {

		if image.SignatureKey != "" {
			getCertObjConfig(*config, image, image.SignatureKey, cidx)
			cidx++
		}

		for _, certUrl := range image.CertificateChain {
			if certUrl != "" {
				getCertObjConfig(*config, image, certUrl, cidx)
				cidx++
			}
		}
	}

	writeCertObjConfig(*config, configFilename)
}

func getCertObjConfig(config types.CertObjConfig,
	image types.StorageConfig, certUrl string, idx int) {

	if certUrl == "" {
		return
	}

	//var safename = types.UrlToSafename(certUrl, "")

	// XXX:FIXME dpath/key/pwd from image storage
	// should be coming from Drive
	// also the sha for the cert should be set
	// for now shortcutting it to only downloader
	var drive = &types.StorageConfig{
		DownloadURL:      certUrl,
		MaxSize:          image.MaxSize,
		TransportMethod:  image.TransportMethod,
		Dpath:            "zededa-cert-repo",
		ApiKey:           image.ApiKey,
		Password:         image.Password,
		ImageSha256:      "",
		ObjType:          certObj,
		NeedVerification: false,
		FinalObjDir:      certsDirname,
	}
	config.StorageConfigList[idx] = *drive
}

func validateBaseOsConfig(baseOsList []types.BaseOsConfig) bool {

	var osCount, activateCount int

	// not more than max base os count(2)
	if len(baseOsList) > MaxBaseOsCount {
		return false
	}

	//count base os instance activate count
	for _, baseOsInstance := range baseOsList {

		osCount++
		if baseOsInstance.Activate == true {
			activateCount++
		}
	}

	// can not be more than one activate as true
	if osCount != 0 {
		if activateCount != 1 {
			return false
		}
	}

	// check if the Sha is same, for different names
	for idx, baseOsConfig0 := range baseOsList {

		for bidx, baseOsConfig1 := range baseOsList {

			if idx <= bidx {
				continue
			}
			// compare the drives, for same Sha
			for _, drive0 := range baseOsConfig0.StorageConfigList {
				for _, drive1 := range baseOsConfig1.StorageConfigList {
					// if sha is same for URLs
					if drive0.ImageSha256 == drive1.ImageSha256 &&
						drive0.DownloadURL != drive1.DownloadURL {
						return false
					}

				}
			}
		}
	}
	return true
}

func createBaseOsConfig(baseOsList []types.BaseOsConfig) {

	for _, baseOsInstance := range baseOsList {

		baseOsFilename := baseOsInstance.UUIDandVersion.UUID.String()
		configFilename := zedagentBaseOsConfigDirname + "/" + baseOsFilename + ".json"
		writeBaseOsConfig(baseOsInstance, configFilename)
	}
}

func validateAppInstanceConfig(appInstance types.AppInstanceConfig) bool {
	return true
}

func writeCertObjConfig(config types.CertObjConfig, configFilename string) {

	bytes, err := json.Marshal(config)
	if err != nil {
		log.Fatal(err, "json Marshal certObjConfig")
	}

	log.Printf("Writing CA config %s, %s\n", configFilename, bytes)

	err = ioutil.WriteFile(configFilename, bytes, 0644)
	if err != nil {
		log.Fatal(err)
	}
}
