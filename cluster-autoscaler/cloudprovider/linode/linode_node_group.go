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

// NodeGroup implements cloudprovider.NodeGroup interface. NodeGroup contains
// configuration info and functions to control a set of nodes that have the
// same capacity and set of labels.
type NodeGroup struct {
	client    *linodego.Client
	pool      linodego.LKEClusterPool
	clusterID int
	minSize   int
	maxSize   int
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
	return n.pool.Count
}

// IncreaseSize increases the size of the node group. To delete a node you need
// to explicitly name it and use DeleteNode. This function should wait until
// node group size is updated. Implementation required.
func (n *NodeGroup) IncreaseSize(delta int) error {
	if delta <= 0 {
		return fmt.Errorf("delta must be positive, have: %d", delta)
	}

	targetSize := n.pool.Count + delta

	if targetSize > n.MaxSize() {
		return fmt.Errorf("size increase is too large. current: %d desired: %d max: %d",
			n.pool.Count, targetSize, n.MaxSize())
	}

	updateOpts := n.pool.GetUpdateOptions()
	updateOpts.Count += delta
	updatedPool, err := n.client.UpdateLKEClusterPool(context.Background(), n.clusterID, n.pool.ID, updateOpts)
	if err != nil {
		return err
	}

	if updatedPool.Count != targetSize {
		return fmt.Errorf("couldn't increase size to %d (delta: %d). Current size is: %d",
			targetSize, delta, updatedPool.Count)
	}

	n.pool = updatedPool
	return nil
}

// DeleteNodes deletes nodes from this node group (and also increasing the size
// of the node group with that). Error is returned either on failure or if the
// given node doesn't belong to this node group. This function should wait
// until node group size is updated. Implementation required.
func (n *NodeGroup) DeleteNodes(nodes []*apiv1.Node) error {

	if n.pool.Linodes == nil {
		return fmt.Errorf("cannot delete nodes, node pool %q is empty", n.Id())
	}

	ctx := context.Background()
	for _, node := range nodes {

		nodeID, ok := node.Labels[" <<<< devo prendere label che identifica nodo >>>>>>> "] // oppure node.Spec.ProviderID
		if !ok {
			return fmt.Errorf("cannot delete node %q with provider ID %q on node pool %q: node ID label %q is missing",
				node.Name, node.Spec.ProviderID, n.id, nodeIDLabel)
		}

		// check if the node to delete belongs to this node group
		found := false
		for _, linode := range n.pool.Linodes {
			if strconv.Itoa(linode.InstanceID) == nodeID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("cannot delete node %q with provider ID %q on node pool %q: node not found in this pool",
				node.Name, node.Spec.ProviderID, n.id, nodeIDLabel)	
		}

		// actually delete the node
		err := n.client.DeleteInstance(ctx, nodeID)
		if err {
			return fmt.Errorf("cannot delete node %q with provider ID %q on node pool %q: error on calling API to delete node",
				node.Name, node.Spec.ProviderID, n.id, nodeIDLabel)
		}

		updatedPool, err := n.client.UpdateLKEClusterPool(context.Background(), n.clusterID, n.pool.ID, n.pool.GetUpdateOptions())
		if err != nil {
			return err
		}

		n.pool = updatedPool

		// TODO bonf api linode non mi permette di selezionare un nodo da cancellare su pool, che faccio? cancello diretto la vm?
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

	targetSize := n.pool.Count + delta
	if targetSize < n.MinSize() {
		return fmt.Errorf("size decrease is too small. current: %d desired: %d min: %d",
			n.pool.Count, targetSize, n.MinSize())
	}

	updateOpts := n.pool.GetUpdateOptions()
	updateOpts.Count += delta
	updatedPool, err := n.client.UpdateLKEClusterPool(context.Background(), n.clusterID, n.pool.ID, updateOpts)
	if err != nil {
		return err
	}

	if updatedPool.Count != targetSize {
		return fmt.Errorf("couldn't increase size to %d (delta: %d). Current size is: %d",
			targetSize, delta, updatedPool.Count)
	}

	n.pool = updatedPool
	return nil
}

// Id returns an unique identifier of the node group.
func (n *NodeGroup) Id() string {
	return strconv.Itoa(n.pool.ID) 
}

// Debug returns a string containing all information regarding this node group.
func (n *NodeGroup) Debug() string {
	return fmt.Sprintf("cluster ID: %s (min:%d max:%d)", n.Id(), n.MinSize(), n.MaxSize())
}

// Nodes returns a list of all nodes that belong to this node group.  It is
// required that Instance objects returned by this method have Id field set.
// Other fields are optional.
func (n *NodeGroup) Nodes() ([]cloudprovider.Instance, error) {

	if n.pool.Linodes == nil {
		return []cloudprovider.Instance{}, nil
	}
	
	nodes := make([]cloudprovider.Instance, len(n.pool.Linodes))
	for i, linode := range n.pool.Linodes {
		nodes[i] = cloudprovider.Instance{Id: linode.ID}
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
	return n.pool.Linodes != nil
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
	return nil, cloudprovider.ErrNotImplemented
}

// Autoprovisioned returns true if the node group is autoprovisioned. An
// autoprovisioned group was created by CA and can be deleted when scaled to 0.
func (n *NodeGroup) Autoprovisioned() bool {
	return false
}