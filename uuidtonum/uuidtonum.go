// Copyright (c) 2018 Zededa, Inc.
// All rights reserved.

package uuidtonum

import (
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
	"github.com/zededa/go-provision/cast"
	"github.com/zededa/go-provision/pubsub"
	"github.com/zededa/go-provision/types"
	"time"
)

// Update LastUseTime; set CreateTime if no entry, set InUse
// The number can be updated as part of this. Entry could already be InUse
// If mustCreate is set the entry should not exist.
func UuidToNumAllocate(pub *pubsub.Publication, uuid uuid.UUID,
	number int, mustCreate bool, numType string) {

	log.Infof("UuidToNumAllocate(%s, %d, %v)\n", uuid.String(), number,
		mustCreate)
	i, err := pub.Get(uuid.String())
	if err != nil {
		now := time.Now()
		u := types.UuidToNum{UUID: uuid,
			CreateTime:  now,
			LastUseTime: now,
			InUse:       true,
			NumType:     numType,
			Number:      number,
		}
		log.Infof("UuidToNumAllocate(%s) publishing created %v\n",
			uuid.String(), u)
		pub.Publish(u.Key(), u)
		return
	}
	u := cast.CastUuidToNum(i)
	if u.NumType != numType {
		log.Fatalf("UuidToNumAllocate(%s) wrong numType %s vs. %s\n",
			uuid.String(), u.NumType, numType)
	}
	if mustCreate {
		log.Fatalf("UuidToNumAllocate(%s) already exists %v\n",
			uuid.String(), u)
	}
	if u.Number != number {
		log.Warnf("UuidToNumAllocate(%s) number changing from %d to %d\n",
			uuid.String(), u.Number, number)
	}
	if u.InUse {
		log.Warnf("UuidToNumAllocate(%s) already InUse %v\n",
			uuid.String(), u)
	}
	u.Number = number
	u.InUse = true
	u.LastUseTime = time.Now()
	log.Infof("UuidToNumAllocate(%s) publishing updated %v\n",
		uuid.String(), u)
	if err := pub.Publish(u.Key(), u); err != nil {
		log.Fatalf("UuidToNumAllocate(%s) publish failed %v\n",
			uuid.String(), err)
	}
}

// Clear InUse
func UuidToNumFree(pub *pubsub.Publication, uuid uuid.UUID) {

	log.Infof("UuidToNumFree(%s)\n", uuid.String())
	i, err := pub.Get(uuid.String())
	if err != nil {
		log.Fatalf("UuidToNumFree(%s) does not exist\n", uuid.String())
	}
	u := cast.CastUuidToNum(i)
	u.InUse = false
	u.LastUseTime = time.Now()
	log.Infof("UuidToNumFree(%s) publishing updated %v\n",
		uuid.String(), u)
	if err := pub.Publish(u.Key(), u); err != nil {
		log.Fatalf("UuidToNumFree(%s) publish failed %v\n",
			uuid.String(), err)
	}
}

func UuidToNumDelete(pub *pubsub.Publication, uuid uuid.UUID) {

	log.Infof("UuidToNumDelete(%s)\n", uuid.String())
	_, err := pub.Get(uuid.String())
	if err != nil {
		log.Fatalf("UuidToNumDelete(%s) does not exist\n",
			uuid.String())
	}
	if err := pub.Unpublish(uuid.String()); err != nil {
		log.Fatalf("UuidToNumDelete(%s) unpublish failed %v\n",
			uuid.String(), err)
	}
}

func UuidToNumGet(pub *pubsub.Publication, uuid uuid.UUID,
	numType string) (int, error) {

	key := uuid.String()
	log.Infof("UuidToNumGet(%s, %s)\n", key, numType)
	i, err := pub.Get(key)
	if err != nil {
		return 0, err
	}
	u := cast.CastUuidToNum(i)
	if u.Key() != key {
		errStr := fmt.Sprintf("UuidToNumGet key/UUID mismatch %s vs %s; ignored %+v",
			key, u.Key(), u)
		log.Errorln(errStr)
		return 0, errors.New(errStr)
	}
	log.Infof("UuidToNumGet(%s, %s) found %v\n", key, numType, u)
	return u.Number, nil
}

func UuidToNumGetOldestUnused(pub *pubsub.Publication,
	numType string) (uuid.UUID, int, error) {

	log.Infof("UuidToNumGetOldestUnused(%s)\n", numType)

	// Will have a LastUseTime of zero
	oldest := new(types.UuidToNum)
	items := pub.GetAll()
	for key, st := range items {
		status := cast.CastUuidToNum(st)
		if status.Key() != key {
			log.Errorf("UuidToNumGetOldestUnused key/UUID mismatch %s vs %s; ignored %+v\n",
				key, status.Key(), status)
			continue
		}
		if status.NumType != numType || status.InUse {
			continue
		}
		if oldest.LastUseTime.Before(status.LastUseTime) {
			log.Infof("UuidToNumGetOldestUnused(%s) found older %v\n",
				numType, status)
			oldest = &status
		}
	}
	if oldest.LastUseTime.IsZero() {
		errStr := fmt.Sprintf("UuidToNumGetOldestUnused(%s) none found",
			numType)
		return uuid.UUID{}, 0, errors.New(errStr)
	}
	log.Infof("UuidToNumGetOldestUnused(%s) found %v\n", numType, oldest)
	return oldest.UUID, oldest.Number, nil
}
