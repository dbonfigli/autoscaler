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
	"fmt"
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
	klog "k8s.io/klog/v2"
)

// linodeCloudProvider implements CloudProvider interface.
type linodeCloudProvider struct {
	client          *linodego.Client
	config          *linodeConfig
	nodeGroups      map[string]*NodeGroup
	resourceLimiter *cloudprovider.ResourceLimiter
}

// nodeGroupConfig is the configuration if a specific node group as defined in the cloud config file.
type nodeGroupConfig struct {
	maxSize int
	minSize int
}

// linodeConfig holds the configuration if the linode provider as defined in the cloud config file.
type linodeConfig struct {
	clusterID       int
	token           string
	defaultMinSize  int
	defaultMaxSize  int
	excludedPoolIDs map[int]bool
	nodeGroupCfg  map[string]*nodeGroupConfig
}

// gcfgGlobalConfig is the gcfg representation of the global section in the config file for linode.
type gcfgGlobalConfig struct {
	clusterID       string `gcfg:"lke-cluster-id"`
	token           string `gcfg:"linode-token"`
	defaultMinSize  string `gcfg:"defaut-min-size-per-linode-type"`
	defaultMaxSize  string `gcfg:"defaut-max-size-per-linode-type"`
	excludedPoolIDs []string `gcfg:"do-not-import-pool-id"`
}

// gcfgNodeGroupConfig is the gcfg representation of the section in the cloud config file to change defaults for a node group.
type gcfgNodeGroupConfig struct {
	minSize string `gcfg:"min-size"`
	maxSize string `gcfg:"max-size"`
}

// gcfgCloudConfig is the gcfg representation of the cloud config file for linode.
type gcfgCloudConfig struct {
	global     gcfgGlobalConfig                `gcfg:"global"`
	nodeGroups map[string]*gcfgNodeGroupConfig `gcfg:"nodegroup"`
}

const (
	DefaultMinSizePerLinodeType int = 1
	DefaultMaxSizePerLinodeType int = 200
)

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

// BuildLinode builds the BuildLinode cloud provider.
func BuildLinode(
	opts config.AutoscalingOptions,
	do cloudprovider.NodeGroupDiscoveryOptions,
	rl *cloudprovider.ResourceLimiter,
) cloudprovider.CloudProvider {

	// the cloud provider automatically uses all node pools in linode.
	// This means we don't use the cloudprovider.NodeGroupDiscoveryOptions
	// flags (which can be set via '--node-group-auto-discovery' or '-nodes')

	cfg := buildCloudConfig(opts.CloudConfig)
	client := buildLinodeClient(cfg.token)
	lcp := &linodeCloudProvider {
		client: client,
		config: cfg,
		nodeGroups: make(map[string]*NodeGroup),
		resourceLimiter: rl,
	}
	err := lcp.Refresh()
	if err != nil {
		klog.Infof("Error on first import of LKE node pools: %v", err)
	}
	klog.Infof("First import of existing LKE node pools ended")
	if len(lcp.nodeGroups) == 0 {
		klog.Infof("Could not import any LKE node pool in any node group")
	} else {
		klog.Infof("imported LKE node pools:")
		for _, ng := range lcp.nodeGroups {
			klog.Infof("%s", ng.extendedDebug())
		}
	}
	return lcp
}

func buildCloudConfig(configFilePath string) *linodeConfig {

	// read the config file and get the gcfg struct
	if configFilePath == "" {
		klog.Fatalf("No config file provided, please specify it via the --cloud-config flag")
	}
	configFile, err := os.Open(configFilePath)
	if err != nil {
		klog.Fatalf("Could not open cloud provider configuration file %q, error: %v", configFilePath, err)
	}
	defer configFile.Close()
	var gcfgCloudConfig gcfgCloudConfig
	if err := gcfg.ReadInto(&gcfgCloudConfig, configFile); err != nil {
		klog.Fatalf("Failed to parse configuration file %q, error: %v", configFilePath, err)
	}

	// get the clusterID
	clusterID, err := strconv.Atoi(gcfgCloudConfig.global.clusterID)
	if err != nil {
		klog.Fatalf("Failed to parse configuration file, error: LKE Cluster ID %q is not a number, %v",
			gcfgCloudConfig.global.clusterID, err)
	}

	// get the defaultMinSize
	defaultMinSize := DefaultMinSizePerLinodeType
	if len(gcfgCloudConfig.global.defaultMinSize) != 0 {
		defaultMinSize, err = strconv.Atoi(gcfgCloudConfig.global.defaultMinSize)
		if err != nil {
			klog.Fatalf("Failed to parse configuration file %q, error: cannot convertthe defined default min size (%q) to int in the global section: %v",
				configFilePath, gcfgCloudConfig.global.defaultMinSize, err)
		}
		if defaultMinSize < 1 {
			klog.Fatalf("Failed to parse configuration file %q, error: default min size must be >= 1 (defined in the global section: %d)",
				configFilePath, defaultMinSize)
		}
	}

	// get the defaultMaxSize
	defaultMaxSize := DefaultMaxSizePerLinodeType
	if len(gcfgCloudConfig.global.defaultMaxSize) != 0 {
		defaultMaxSize, err = strconv.Atoi(gcfgCloudConfig.global.defaultMaxSize)
		if err != nil {
			klog.Fatalf("Failed to parse configuration file %q, error: cannot convert the defined default max size (%q) to int in the global section: %v",
				configFilePath, gcfgCloudConfig.global.defaultMaxSize, err)
		}
	}

	// validate the size limits
	if defaultMaxSize <= defaultMinSize {
		klog.Fatalf("Failed to parse configuration file %q, error: default min size must be strictly less than default max size (minSize: %d, maxSize %d)",
			configFilePath, defaultMinSize, defaultMaxSize)
	}

	// get the linode token
	token := gcfgCloudConfig.global.token
	if len(gcfgCloudConfig.global.token) == 0 {
		klog.Fatalf("Failed to parse configuration file %q, error: linode token not present in global section", configFilePath)
	}

	// get the list of LKE pools that must not be imported
	excludedPoolIDs := make(map[int]bool)
	for _, excludedPoolIDStr := range gcfgCloudConfig.global.excludedPoolIDs {
		excludedPoolID, err := strconv.Atoi(excludedPoolIDStr)
		if err != nil {
			klog.Fatalf("Failed to parse configuration file %q, error: cannot convert excluded pool id %q to int", configFilePath, excludedPoolIDStr)
		}
		excludedPoolIDs[excludedPoolID] = true
	}

	// get the specific configuration of a node group
	nodeGroupCfg := make(map[string]*nodeGroupConfig)
	for nodeType, gcfgNodeGroup := range gcfgCloudConfig.nodeGroups {
		minSize := defaultMinSize
		maxSize := defaultMaxSize
		minSizeStr := gcfgNodeGroup.minSize
		if len(minSizeStr) != 0 {
			nodeGroupMinSize, err := strconv.Atoi(minSizeStr)
			if err != nil {
				klog.Fatalf("Failed to parse configuration file %q, error: could not parse min size for node group %q, %v",
					configFilePath, nodeType, err)
			} else {
				minSize = nodeGroupMinSize
			}
			if minSize < 1 {
				klog.Fatalf("Failed to parse configuration file %q, error: min size must be >= 1 (value defined in the node group section %q: %d)",
					configFilePath, nodeType, minSize)
			}
		}
		maxSizeStr := gcfgNodeGroup.maxSize
		if len(maxSizeStr) != 0 {
			nodeGroupMaxSize, err := strconv.Atoi(maxSizeStr)
			if err != nil {
				klog.Fatalf("Failed to parse configuration file %q, error: could not parse max size for node group %q, %v",
					configFilePath, nodeType, err)
			} else {
				maxSize = nodeGroupMaxSize
			}
		}
		if !(minSize < maxSize) {
			klog.Fatalf("Failed to parse configuration file %q, error: min size for a node group must be strictly less than max size (value defined minSize: %d, maxSize %d)",
				configFilePath, minSize, maxSize)
		}
		ngc := &nodeGroupConfig {
			maxSize: maxSize,
			minSize: minSize,
		}
		nodeGroupCfg[nodeType] = ngc
	}

	cfg := &linodeConfig {
		clusterID: clusterID,
		token: token,
		defaultMinSize: defaultMinSize,
		defaultMaxSize: defaultMaxSize,
		excludedPoolIDs: excludedPoolIDs,
		nodeGroupCfg: nodeGroupCfg,
	}
	return cfg
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

// Refresh is called before every main loop and can be used to dynamically update cloud provider state.
// In particular the list of node groups returned by NodeGroups can change as a result of CloudProvider.Refresh().
func (l *linodeCloudProvider) Refresh() error {
	nodeGroups := make(map[string]*NodeGroup)
	lkeClusterPools, err := l.client.ListLKEClusterPools(context.Background(), l.config.clusterID, nil)
	if err != nil {
		return fmt.Errorf("Failed to get list of LKE pools from linode API: %v", err)
	}
	for pool := range lkeClusterPools {
		//skip this pool if it is among the ones to be excluded as defined in the config file
		_, found := l.config.excludedPoolIDs[pool]; if found {
			continue
		}
		//check if the nodes in the pool are more than 1, if so skip it
		if pool.Count > 1 {
			klog.Infof("The LKE pool %d has more than one node (current nodes in pool: %d), will exclude it from the node groups",
				pool.ID, pool.Count)
			continue
		}
		// add pool to the node groups map
		linodeType := pool.Type
		// if a node group for the node type of this pool already exists, add it to the related node group
		nodeGroup, found := nodeGroups[linodeType] ; if found {
			nodeGroup.addLKEPool(pool)
			//TODO better to skip it or add it anyway?
			currentSize := len(nodeGroup.lkePools)
			if currentSize > nodeGroup.maxSize {
				klog.Infof("warning: imported node pools in node group %q are > maxSize (current size: %d, max size: %d)",
					nodeGroup.id, currentSize, nodeGroup.maxSize)
			}
		} else { // else create a new node group with this pool in it
			ng := newNodeGroup(pool, l.config, l.client)
			nodeGroups[linodeType] = ng
		}	
	}
	l.nodeGroups = nodeGroups
	return nil
}

func newNodeGroup(pool *linodego.LKEClusterPool, cfg *linodeConfig, client *linodego.Client) *NodeGroup {
	// get specific min and max size for a node group, if defined in the config file
	minSize := cfg.defaultMinSize
	maxSize := cfg.defaultMaxSize
	nodeGroupCfg, found := cfg.nodeGroupCfg[pool.Type]; if found {
		minSize = nodeGroupCfg.minSize
		maxSize = nodeGroupCfg.maxSize
	}
	// create the new node group with this single LKE pool inside
	lkePools := make(map[int]*linodego.LKEClusterPool)
	lkePools[pool.ID] = pool
	poolOpts := pool.GetCreateOptions()
	ng := &NodeGroup {
		client: client,
		lkePools: lkePools,
		poolOpts: poolOpts,
		lkeClusterID: cfg.clusterID,
		minSize: minSize,
		maxSize: maxSize,
		id: pool.Type,
	}
	return ng
}