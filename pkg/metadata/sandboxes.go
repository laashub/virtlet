/*
Copyright 2017 Mirantis

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boltdb/bolt"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/Mirantis/virtlet/pkg/metadata/types"
)

var (
	sandboxKeyPrefix  = []byte("sandboxes/")
	sandboxDataBucket = []byte("data")
)

func sandboxKey(sandboxID string) []byte {
	return append(sandboxKeyPrefix, []byte(sandboxID)...)
}

type podSandboxMeta struct {
	client *boltClient
	id     string
}

// GetID returns ID of the pod sandbox managed by this object
func (m podSandboxMeta) GetID() string {
	return m.id
}

// Retrieve loads from DB and returns pod sandbox data bound to the object
func (m podSandboxMeta) Retrieve() (*types.PodSandboxInfo, error) {
	if m.GetID() == "" {
		return nil, errors.New("Pod sandbox ID cannot be empty")
	}
	var psi *types.PodSandboxInfo
	err := m.client.db.View(func(tx *bolt.Tx) error {
		bucket, err := getSandboxBucket(tx, m.GetID(), false, false)
		if err != nil {
			return err
		}
		return retrieveSandboxFromDB(bucket, &psi)
	})
	if err == nil && psi != nil {
		psi.PodID = m.GetID()
	}
	return psi, err
}

// Save allows to create/modify/delete pod sandbox instance bound to the object.
// Supplied handler gets current PodSandboxInfo value (nil if doesn't exist) and returns new structure
// value to be saved or nil to delete. If error value is returned from the handler, the transaction is
// rolled back and returned error becomes the result of the function
func (m podSandboxMeta) Save(updater func(*types.PodSandboxInfo) (*types.PodSandboxInfo, error)) error {
	if m.GetID() == "" {
		return errors.New("Pod sandbox ID cannot be empty")
	}
	return m.client.db.Update(func(tx *bolt.Tx) error {
		key := sandboxKey(m.GetID())
		var current *types.PodSandboxInfo
		bucket, err := getSandboxBucket(tx, m.GetID(), true, false)
		if err != nil {
			return err
		}
		if err := retrieveSandboxFromDB(bucket, &current); err != nil {
			return err
		}
		newData, err := updater(current)
		if err != nil {
			return err
		}

		if newData == nil {
			return tx.DeleteBucket(key)
		}
		return saveSandboxToDB(bucket, newData)
	})
}

// PodSandbox returns interface instance which manages pod sandbox with given ID
func (b *boltClient) PodSandbox(podID string) PodSandboxMetadata {
	return &podSandboxMeta{id: podID, client: b}
}

// ListPodSandboxes returns list of pod sandboxes that match given filter
func (b *boltClient) ListPodSandboxes(filter *types.PodSandboxFilter) ([]PodSandboxMetadata, error) {
	var result []PodSandboxMetadata
	err := b.db.View(func(tx *bolt.Tx) error {
		c := tx.Cursor()
		for k, _ := c.Seek(sandboxKeyPrefix); k != nil && bytes.HasPrefix(k, sandboxKeyPrefix); k, _ = c.Next() {
			psm := podSandboxMeta{client: b, id: string(k[len(sandboxKeyPrefix):])}
			fv, err := filterPodSandboxMeta(&psm, filter)
			if err != nil {
				return err
			}
			if fv {
				result = append(result, psm)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func getSandboxBucket(tx *bolt.Tx, podID string, create, optional bool) (*bolt.Bucket, error) {
	key := sandboxKey(podID)
	if create {
		bucket, err := tx.CreateBucketIfNotExists(key)
		if err != nil {
			return nil, err
		}
		return bucket, nil
	}
	bucket := tx.Bucket(key)
	if bucket == nil && !optional {
		return nil, fmt.Errorf("pod sandbox %q does not exist", podID)
	}
	return bucket, nil
}

func retrieveSandboxFromDB(bucket *bolt.Bucket, psi **types.PodSandboxInfo) error {
	data := bucket.Get(sandboxDataBucket)
	if data == nil {
		return nil
	}
	return json.Unmarshal(data, psi)
}

func saveSandboxToDB(bucket *bolt.Bucket, psi *types.PodSandboxInfo) error {
	data, err := json.Marshal(psi)
	if err != nil {
		return err
	}

	return bucket.Put(sandboxDataBucket, data)
}

func filterPodSandboxMeta(psm PodSandboxMetadata, filter *types.PodSandboxFilter) (bool, error) {
	if filter == nil {
		return true, nil
	}

	if filter.Id != "" && psm.GetID() != filter.Id {
		return false, nil
	}

	psi, err := psm.Retrieve()
	if err != nil {
		return false, err
	}
	if psi == nil {
		return false, fmt.Errorf("no data found for pod id %q", psm.GetID())
	}

	if filter.State != nil && psi.State != *filter.State {
		return false, nil
	}

	sel := fields.SelectorFromSet(filter.LabelSelector)
	if !sel.Matches(fields.Set(psi.Config.Labels)) {
		return false, nil
	}

	return true, nil
}