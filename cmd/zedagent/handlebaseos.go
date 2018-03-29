// Copyright (c) 2017 Zededa, Inc.
// All rights reserved.

// base os event handlers

package main

import (
	"github.com/zededa/go-provision/types"
	"log"
	"reflect"
)

// zedagent publishes these config/status files
// and also the consumer
var baseOsConfigMap map[string]types.BaseOsConfig
var baseOsStatusMap map[string]types.BaseOsStatus

func initBaseOsMaps() {

	if baseOsConfigMap == nil {
		log.Printf("create baseOsConfig map\n")
		baseOsConfigMap = make(map[string]types.BaseOsConfig)
	}

	if baseOsStatusMap == nil {
		log.Printf("create baseOsStatus map\n")
		baseOsStatusMap = make(map[string]types.BaseOsStatus)
	}
}

func baseOsConfigGet(uuidStr string) *types.BaseOsConfig {
	config, ok := baseOsConfigMap[uuidStr]
	if !ok {
		log.Printf("%s, baseOs config is absent\n", uuidStr)
		return nil
	}
	return &config
}

func baseOsConfigSet(uuidStr string, config *types.BaseOsConfig) {
	baseOsConfigMap[uuidStr] = *config
}

func baseOsConfigDelete(uuidStr string) bool {
	log.Printf("%s, baseOs config delete\n", uuidStr)
	if config := baseOsConfigGet(uuidStr); config != nil {
		delete(baseOsConfigMap, uuidStr)
		return true
	}
	return false
}

func baseOsStatusGet(uuidStr string) *types.BaseOsStatus {
	status, ok := baseOsStatusMap[uuidStr]
	if !ok {
		log.Printf("%s, baseOs status is absent\n", uuidStr)
		return nil
	}
	return &status
}

func baseOsStatusSet(uuidStr string, status *types.BaseOsStatus) {
	baseOsStatusMap[uuidStr] = *status
}

func baseOsStatusDelete(uuidStr string) bool {
	if status := baseOsStatusGet(uuidStr); status != nil {
		log.Printf("baseOsStatusDelete(%v) for %s\n",
			status.BaseOsVersion, uuidStr)
		delete(baseOsStatusMap, uuidStr)
		return true
	}
	return false
}

func addOrUpdateBaseOsStatus(uuidStr string, status *types.BaseOsStatus) {

	changed := false

	if m := baseOsStatusGet(uuidStr); m != nil {
		if !reflect.DeepEqual(m, status) {
			log.Printf("addOrUpdateBaseOsStatus(%v) for %s, Status change\n",
				status.BaseOsVersion, uuidStr)
			changed = true
		} else {
			log.Printf("addOrUpdateBaseOsStatus(%v) for %s, No change\n",
				status.BaseOsVersion, uuidStr)
		}
	} else {
		log.Printf("addOrUpdateBaseOsStatus(%v) for %s, Config add\n",
			status.BaseOsVersion, uuidStr)
		changed = true
	}

	if changed {
		baseOsStatusSet(uuidStr, status)
	}
}

func removeBaseOsStatus(uuidStr string) {
	baseOsStatusDelete(uuidStr)
}
