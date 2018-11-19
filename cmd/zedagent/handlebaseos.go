// Copyright (c) 2017-2018 Zededa, Inc.
// All rights reserved.

// base os event handlers

package zedagent

import (
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/types"
)

func lookupBaseOsStatus(ctx *zedagentContext, key string) *types.BaseOsStatus {
	sub := ctx.subBaseOsStatus
	st, _ := sub.Get(key)
	if st == nil {
		log.Infof("lookupBaseOsStatus(%s) not found\n", key)
		return nil
	}
	status := cast.CastBaseOsStatus(st)
	if status.Key() != key {
		log.Errorf("lookupBaseOsStatus(%s) got %s; ignored %+v\n",
			key, status.Key(), status)
		return nil
	}
	return &status
}

func handleBaseOsReboot (ctx *zedagentContext, status types.BaseOsStatus) {
	// if restart flag is set,
	// initiate the shutdown process
	if status.Reboot == true {
		shutdownAppsGlobal(ctx)
		startExecReboot()
	}
}
