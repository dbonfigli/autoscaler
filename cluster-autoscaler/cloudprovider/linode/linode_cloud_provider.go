/*
Copyright 2016 The Kubernetes Authors.

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
	"os"
	"strconv"
	"context"
	"net/http"

	"github.com/linode/linodego"
	"golang.org/x/oauth2"
	"gopkg.in/gcfg.v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/config/dynamic"
	klog "k8s.io/klog/v2"
)

// linodeCloudProvider implements CloudProvider interface.
type linodeCloudProvider struct {
	nodeGroups      []*NodeGroup
	resourceLimiter *cloudprovider.ResourceLimiter
}

// configGlobal is the global section of the config file for linode.
type configGlobal struct {
	clusterID string `gcfg:"lke-cluster-id"`
	token     string `gcfg:"linode-token"`
}

// configNodeGroupDef is the section to define a node group in the config file for linode.
type configNodeGroupDef struct {
	linodeType     string `gcfg:"linode-type"`
	importPoolIDs []string `gcfg:"import-pool-id"`
}

// cloudConfig is the cloud config file for linode.
type cloudConfig struct {
	global        configGlobal                   `gcfg:"global"`
	nodeGroupDefs map[string]*configNodeGroupDef `gcfg:"nodegroupdef"`
}

func (l *linodeCloudProvider) Name() string {
	return cloudprovider.LinodeProviderName
}

// NodeGroups returns all node groups configured for this cloud provider.
func (l *linodeCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	nodeGroups := make([]cloudprovider.NodeGroup, len(l.nodeGroups))
	i := 0
	for _, ng := range l.nodeGroups {
		nodeGroups[i] = ng
	}
	return nodeGroups
}

// NodeGroupForNode returns the node group for the given node, nil if the node
// should not be processed by cluster autoscaler, or non-nil error if such
// occurred. Must be implemented.
func (l *linodeCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	poolID, err := nodeToLKEPoolID(node)
	if err != nil {
		return nil, err
	}
	for _, nodeGroup := range l.nodeGroups {
		if nodeGroup.lkePools[poolID] != nil {
			return nodeGroup, nil
		}
	}
	return nil, nil
}

// Pricing returns pricing model for this cloud provider or error if not available.
// Implementation optional.
func (l *linodeCloudProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes get all machine types that can be requested from the cloud provider.
// Implementation optional.
func (l *linodeCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return []string{}, cloudprovider.ErrNotImplemented
}

// NewNodeGroup builds a theoretical node group based on the node definition provided. The node group is not automatically
// created on the cloud provider side. The node group is not returned by NodeGroups() until it is created.
// Implementation optional.
func (l *linodeCloudProvider) NewNodeGroup(machineType string, labels map[string]string, systemLabels map[string]string,
	taints []apiv1.Taint, extraResources map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns struct containing limits (max, min) for resources (cores, memory etc.).
func (l *linodeCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return l.resourceLimiter, nil
}

// GPULabel returns the label added to nodes with GPU resource.
func (l *linodeCloudProvider) GPULabel() string {
	return ""
}

// GetAvailableGPUTypes return all available GPU types cloud provider supports.
func (l *linodeCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return nil
}

// Cleanup cleans up open resources before the cloud provider is destroyed, i.e. go routines etc.
func (l *linodeCloudProvider) Cleanup() error {
	return nil
}

// Refresh is called before every main loop and can be used to dynamically update cloud provider state.
// In particular the list of node groups returned by NodeGroups can change as a result of CloudProvider.Refresh().
func (l *linodeCloudProvider) Refresh() error {
	return nil
}

// BuildLinode builds the BuildLinode cloud provider.
func BuildLinode(
	opts config.AutoscalingOptions,
	do cloudprovider.NodeGroupDiscoveryOptions,
	rl *cloudprovider.ResourceLimiter,
) cloudprovider.CloudProvider {

	cloudConfig := buildCloudConfig(opts.CloudConfig)
	linodeClient := buildLinodeClient(cloudConfig.global.token)
	clusterID, err := strconv.Atoi(cloudConfig.global.clusterID)
	if err != nil {
		klog.Fatalf("Failed to read configuration file %q: LKEClusterID %q is not a number, %v",
			opts.CloudConfig, cloudConfig.global.clusterID, err)

	}
	nodeGroups := buildNodeGroups(linodeClient, clusterID, cloudConfig, do.NodeGroupSpecs)
	return &linodeCloudProvider {
		nodeGroups: nodeGroups,
		resourceLimiter: rl,
	}
}

func buildCloudConfig(configFilePath string) *cloudConfig {
	if configFilePath == "" {
		klog.Fatalf("No config file provided, please specify it via the --cloud-config flag")
	}
	configFile, err := os.Open(configFilePath)
	if err != nil {
		klog.Fatalf("Could not open cloud provider configuration file %q: %v", configFilePath, err)
	}
	defer configFile.Close()
	var cfg cloudConfig
	if err := gcfg.ReadInto(&cfg, configFile); err != nil {
		klog.Fatalf("Failed to read configuration file: %q, error: %v", configFilePath, err)
	}
	// validation of global section
	if len(cfg.global.clusterID) == 0 {
		klog.Fatalf("Failed to read configuration file: %q, error: cannot read LKE cluster id config value", configFilePath)
	}
	if len(cfg.global.token) == 0 {
		klog.Fatalf("Failed to read configuration file: %q, error: cannot read linode token config value", configFilePath)
	}
	return &cfg
}

func buildLinodeClient(linodeToken string) *linodego.Client {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: linodeToken})
	oauth2Client := &http.Client{
		Transport: &oauth2.Transport{
			Source: tokenSource,
		},
	}
	client := linodego.NewClient(oauth2Client)
	client.SetDebug(true) // TODO bonf set flag here
	return &client
}

func buildNodeGroups(client *linodego.Client, clusterID int, config *cloudConfig, nodeGroupSpecs []string) []*NodeGroup {
	if len(nodeGroupSpecs) == 0 {
		klog.Fatalf("Must specify at least one node group with --nodes=<min>:<max>:<node group name>")
	}
	nodeGroups := make([]*NodeGroup, 0)
	for _, nodeGroupSpec := range nodeGroupSpecs {
		ng := buildNodeGroup(client, clusterID, config, nodeGroupSpec)
		nodeGroups = append(nodeGroups, ng)
	}
	return nodeGroups
}

func buildNodeGroup(client *linodego.Client, clusterID int, config *cloudConfig, nodeGroupSpec string) *NodeGroup {
	spec, err := dynamic.SpecFromString(nodeGroupSpec, false)
	if err != nil {
		klog.Fatalf("Could not parse node group spec %s: %v", nodeGroupSpec, err)
	}
	if err := spec.Validate(); err != nil {
		klog.Fatalf("Could not parse node group spec %s, error on validating spec: %v", nodeGroupSpec, err)
	}
	nodeGroupName, minSize, maxSize := spec.Name, spec.MinSize, spec.MaxSize
	nodeGroupCfg, ok := config.nodeGroupDefs[nodeGroupName]
	if !ok {
		klog.Fatalf("Node group name %q defined in --nodes not found in cloud config file, please define it using the [nodegroupdef <node group name>] section", spec.Name)
	}
	linodeType := nodeGroupCfg.linodeType
	if len(linodeType) == 0 {
		klog.Fatalf("Could not find linode-type field in nodegroupdef %q", nodeGroupName)
	}
	poolOpts := linodego.LKEClusterPoolCreateOptions {
		Count: 1,
		Type: linodeType,
		//Disks []LKEClusterPoolDisk `json:"disks"` ? TODO bonf
	}
	lkePools := make(map[int] linodego.LKEClusterPool)
	// try to import already existing LKE pools as defined in the cloud config file
	for _, poolIDStr := range nodeGroupCfg.importPoolIDs {
		poolID, err := strconv.Atoi(poolIDStr)
		if err != nil {
			klog.Errorf("Failed to import LKE pool %q to node group %q, cannot convert %q to int",
				poolIDStr, nodeGroupName)
			continue
		}
		lkePool, err := client.GetLKEClusterPool(context.Background(), clusterID, poolID)
		if err != nil {
			klog.Errorf("Failed to import LKE pool %d to node group %q, error on getting LKE pool from linode API: %v",
				poolID, nodeGroupName, err)
			continue
		}
		if lkePool.Count != 1 {
			klog.Errorf("Failed to import LKE pool %d to node group %q, pool size is %d but must be exactly 1 to be imported in cluster-autoscaler",
				poolID, nodeGroupName, lkePool.Count)
			continue
		}
		if lkePool.Type != poolOpts.Type {
			klog.Errorf("Failed to import LKE pool %d to node group %q, node type of pool is different from the one defined for the node group (%q vs %q)",
				poolID, nodeGroupName, lkePool.Type, poolOpts.Type)
			continue
		}
		if len(lkePools) > maxSize {
			klog.Errorf("Cannot import LKE pool %d to node group %q, node group size is already maxed out (max size: %d)",
				poolID, nodeGroupName, maxSize)
			continue
		}
		klog.Infof("LKE pool %d (in LKE Cluster %d) correctly imported to node group %q",
			poolID, clusterID, nodeGroupName)
		lkePools[poolID] = lkePool
	}
	ng := &NodeGroup {
		client: client,
		lkePools: lkePools,
		prototypePool: poolOpts,
		lkeClusterID: clusterID,
		minSize: minSize,
		maxSize: maxSize,
		id: nodeGroupName,
	}
	currentSize := len(ng.lkePools)
	if currentSize < minSize {
		klog.Infof("Current size of node group %q is less than minSize (current: %d < min: %d), adding nodes...",
			nodeGroupName, currentSize, minSize)
		delta := minSize - currentSize
		if err := ng.IncreaseSize(delta); err != nil {
			klog.Errorf("failed to create LKE pools for node group %q to reach the minSize: %v", nodeGroupName, err)
		}
	}
	return ng
}