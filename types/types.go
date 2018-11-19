// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

package types

import (
	"github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
	"strings"
	"time"
)

// Enum names from OMA-TS-LWM2M_SwMgmt-V1_0-20151201-C
// The ones starting with BOOTING are in addition to OMA and represent
// operational/activated states.
type SwState uint8

const (
	INITIAL          SwState = iota + 1
	DOWNLOAD_STARTED         // Really download in progress
	DOWNLOADED
	DELIVERED // Package integrity verified
	INSTALLED // Available to be activated
	BOOTING
	RUNNING
	HALTING // being halted
	HALTED
	RESTARTING // Restarting due to config change or zcli
	PURGING    // Purging due to config change
	MAXSTATE   //
)

func UrlToSafename(url string, sha string) string {

	var safename string

	if sha != "" {
		safename = strings.Replace(url, "/", " ", -1) + "." + sha
	} else {
		safename = strings.Replace(url, "/", " ", -1) + "." + "sha"
	}
	return safename
}

// Remove initial part up to last '/' in URL. Note that '/' was converted
// to ' ' in Safename
func SafenameToFilename(safename string) string {
	comp := strings.Split(safename, " ")
	last := comp[len(comp)-1]
	// Drop "."sha256 tail part of Safename
	i := strings.LastIndex(last, ".")
	if i == -1 {
		log.Fatal("Malformed safename with no .sha256",
			safename)
	}
	last = last[0:i]
	return last
}

func UrlToFilename(urlName string) string {
	comp := strings.Split(urlName, "/")
	last := comp[len(comp)-1]
	return last
}

// Used to retain UUID to integer maps across reboots.
// Used for appNum and bridgeNum
type UuidToNum struct {
	UUID        uuid.UUID
	Number      int
	NumType     string // For logging
	CreateTime  time.Time
	LastUseTime time.Time
	InUse       bool
}

func (info UuidToNum) Key() string {
	return info.UUID.String()
}
