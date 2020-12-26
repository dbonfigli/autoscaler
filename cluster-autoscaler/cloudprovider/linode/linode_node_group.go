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
	"fmt"
	"strconv"
	"context"

	"github.com/linode/linodego"
	apiv1 "k8s.io/api/core/v1"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	lkeLabelNamespace = "lke.linode.com"
	poolIDLabel       = lkeLabelNamespace + "/pool-id"
)

// NodeGroup implements cloudprovider.NodeGroup interface. NodeGroup contains
// configuration info and functions to control a set of nodes that have the
// same capacity and set of labels.
//
// Receivers assume all fields are initialized (i.e. not nil).
//
// A node group is composed of multiple LKE pools, each with a single linode in them.
// We cannot use an LKE pool as node group since LKE does not provide a way to
// delete a specific linnode in a pool.
type NodeGroup struct {
	client        *linodego.Client
	lkePools      map[int]*linodego.LKEClusterPool //key: LKEClusterPool.ID
	poolOpts     linodego.LKEClusterPoolCreateOptions
	lkeClusterID  int
	minSize       int
	maxSize       int
	id            string
}

// MaxSize returns maximum size of the node group.
func (n *NodeGroup) MaxSize() int {
	return n.maxSize
}

// MinSize returns minimum size of the node group.
func (n *NodeGroup) MinSize() int {
	return n.minSize
}

// TargetSize returns the current target size of the node group. It is possible
// that the number of nodes in Kubernetes is different at the moment but should
// be equal to Size() once everything stabilizes (new nodes finish startup and
// registration or removed nodes are deleted completely). Implementation
// required.
func (n *NodeGroup) TargetSize() (int, error) {
	return len(n.lkePools), nil
}

// IncreaseSize increases the size of the node group. To delete a node you need
// to explicitly name it and use DeleteNode. This function should wait until
// node group size is updated. Implementation required.
func (n *NodeGroup) IncreaseSize(delta int) error {
	if delta <= 0 {
		return fmt.Errorf("delta must be positive, have: %d", delta)
	}

	currentSize := len(n.lkePools)
	targetSize := currentSize + delta
	if targetSize > n.MaxSize() {
		return fmt.Errorf("size increase is too large. current: %d desired: %d max: %d",
			currentSize, targetSize, n.MaxSize())
	}

	for i:=0; i<delta; i++ {
		err := n.addNewLKEPool()
		if err != nil {
			return err
		} 
	}

	return nil
}

// DeleteNodes deletes nodes from this node group (and also increasing the size
// of the node group with that). Error is returned either on failure or if the
// given node doesn't belong to this node group. This function should wait
// until node group size is updated. Implementation required.
func (n *NodeGroup) DeleteNodes(nodes []*apiv1.Node) error {
	for _, node := range nodes {

		poolID, err := nodeToLKEPoolID(node)
		if err != nil {
			return fmt.Errorf("cannot delete node %q with provider ID %q: %v",
				node.Name, node.Spec.ProviderID, err)
		}

		err = n.deleteLKEPool(poolID)
		if err != nil {
			return fmt.Errorf("cannot delete node %q with provider ID %q: %v",
			node.Name, node.Spec.ProviderID, err)
		}
	}

	return nil
}

// DecreaseTargetSize decreases the target size of the node group. This function
// doesn't permit to delete any existing node and can be used only to reduce the
// request for new nodes that have not been yet fulfilled. Delta should be negative.
// It is assumed that cloud provider will not delete the existing nodes when there
// is an option to just decrease the target. Implementation required.
func (n *NodeGroup) DecreaseTargetSize(delta int) error {
	if delta >= 0 {
		return fmt.Errorf("delta must be negative, have: %d", delta)
	}

	currentSize := len(n.lkePools)
	targetSize := currentSize + delta
	if targetSize < n.MinSize() {
		return fmt.Errorf("size decrease is too small. current: %d desired: %d min: %d",
			currentSize, targetSize, n.MinSize())
	}

	for id := range n.lkePools {
		err := n.deleteLKEPool(id)
		if err != nil {
			return err
		}
		delta--
		if delta <= 0 {
			break
		}
	}

	return nil
}

// Id returns an unique identifier of the node group.
func (n *NodeGroup) Id() string {
	return n.id
}

// Debug returns a string containing all information regarding this node group.
func (n *NodeGroup) Debug() string {
	return fmt.Sprintf("node group ID: %q (min:%d max:%d)", n.Id(), n.MinSize(), n.MaxSize())
}

// extendendDebug returns a string containing detailed information regarding this node group.
func (n *NodeGroup) extendedDebug() string {
	return fmt.Sprintf("node group debug info: %+v", n)
}

// Nodes returns a list of all nodes that belong to this node group. It is
// required that Instance objects returned by this method have Id field set.
// Other fields are optional.
func (n *NodeGroup) Nodes() ([]cloudprovider.Instance, error) {
	
	nodes := make([]cloudprovider.Instance, len(n.lkePools))
	i := 0
	for id := range n.lkePools {
		nodes[i] = cloudprovider.Instance{Id: strconv.Itoa(id)}
		i++
	}

	return nodes, nil
}

// TemplateNodeInfo returns a schedulerframework.NodeInfo structure of an empty
// (as if just started) node. This will be used in scale-up simulations to
// predict what would a new node look like if a node group was expanded. The
// returned NodeInfo is expected to have a fully populated Node object, with
// all of the labels, capacity and allocatable information as well as all pods
// that are started on the node by default, using manifest (most likely only
// kube-proxy). Implementation optional.
func (n *NodeGroup) TemplateNodeInfo() (*schedulerframework.NodeInfo, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// Exist checks if the node group really exists on the cloud provider side.
// Allows to tell the theoretical node group from the real one. Implementation
// required.
func (n *NodeGroup) Exist() bool {
	return true
}

// Create creates the node group on the cloud provider side. Implementation
// optional.
func (n *NodeGroup) Create() (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// Delete deletes the node group on the cloud provider side.  This will be
// executed only for autoprovisioned node groups, once their size drops to 0.
// Implementation optional.
func (n *NodeGroup) Delete() error {
	return cloudprovider.ErrNotImplemented
}

// Autoprovisioned returns true if the node group is autoprovisioned. An
// autoprovisioned group was created by CA and can be deleted when scaled to 0.
func (n *NodeGroup) Autoprovisioned() bool {
	return false
}

func (n *NodeGroup) addNewLKEPool() error {
	ctx := context.Background()
	newPool, err := n.client.CreateLKEClusterPool(ctx, n.lkeClusterID, n.poolOpts)
	if err != nil {
		return fmt.Errorf("error on creating new LKE pool for LKE clusterID: %d", n.lkeClusterID)
	}
	n.lkePools[newPool.ID] = newPool
	return nil
}

func (n *NodeGroup) addLKEPool(pool *linodego.LKEClusterPool) {
	n.lkePools[pool.ID] = pool
}

func (n *NodeGroup) deleteLKEPool(id int) error {
	_, ok := n.lkePools[id]
	if !ok {
		return fmt.Errorf("cannot delete LKE pool %d, this pool is not one we are managing", id)
	}
	ctx := context.Background()
	err := n.client.DeleteLKEClusterPool(ctx, n.lkeClusterID, id)
	if err != nil {
		return fmt.Errorf("error on deleting LKE pool %d, Linode API said: %v", id, err)
	}
	delete(n.lkePools, id)
	return nil
}

// nodeToLKEPoolID return the LKE pool of an apiv1.Node node, or error if it cannot find one
func nodeToLKEPoolID(node *apiv1.Node) (int, error) {
	poolIDstring, ok := node.Labels[poolIDLabel]
	if !ok {
		return 0, fmt.Errorf("cannot find LKE pool for node %q with provider ID %q: LKE pool ID label %q is missing",
			node.Name, node.Spec.ProviderID, poolIDLabel)
	}
	poolID, err := strconv.Atoi(poolIDstring)
	if err != nil {
		return 0, fmt.Errorf("cannot find LKE pool for node %q with provider ID %q: cannot convert LKE pool ID label to int, %v",
		node.Name, node.Spec.ProviderID, poolIDstring)
	}
	return poolID, nil
}