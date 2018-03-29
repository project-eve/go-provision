// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// some common routines

package main

import (
	"fmt"
)

func initMaps() {

	initBaseOsMaps()
	initCertObjMaps()
	initDownloaderMaps()
	initVerifierMaps()
}

func appendError(allErrors string, prefix string, lasterr string) string {
	return fmt.Sprintf("%s%s: %s\n\n", allErrors, prefix, lasterr)
}

func formLookupKey(objType string, uuidStr string) string {
	return objType + "x" + uuidStr
}
