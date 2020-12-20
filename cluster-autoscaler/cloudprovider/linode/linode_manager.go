/*
Copyright 2019 The Kubernetes Authors.

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

package linode

import (
	"io"

	"github.com/linode/linodego"
)

var (
	version = "dev"
)

type Manager struct {
	client     linodego.Client
	clusterID  int
	nodeGroups []*NodeGroup
}

func newManager(configReader io.Reader) (*Manager, error) {

	//TODO bonf

	m := &Manager{
		client:     doClient.Kubernetes,
		clusterID:  cfg.ClusterID,
		nodeGroups: make([]*NodeGroup, 0),
	}

	return m, nil
}

// Refresh refreshes the cache holding the nodegroups. This is called by the CA
// based on the `--scan-interval`. By default it's 10 seconds.
func (m *Manager) Refresh() error {
	// TODO bonf
}