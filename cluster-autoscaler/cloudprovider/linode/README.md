# Cluster Autoscaler for Linode

The cluster autoscaler for Linode scales nodes in a LKE cluster.

A cluster autoscaler node group is composed of multiple LKE Node Pools of the same Linode type (e.g. g6-standard-1, g6-standard-2), each holding a *single* linode.

At every scan interval the cluster autoscaler review every LKE Node Pool and if:
* it is not among the the ones to be excluded as defined in the configuration file;
* it holds a single linode;

then it becames part of the node group holding LKE Node Pools of the linode type it has, or a node group is created with this LKE Node Pool inside if there are no node groups with that linode type at the moment.

Scaling is achieved adding LKE Node Pools to node groups, *not* increasing the size of a LKE Node Pool, that must stay 1. The reason behind this is that Linode does not provide a way to selectively delete a linode from a  LKE Node Pool and decrease the size of the pool with it.

## Configuration

The cluster autoscaler automatically select every LKE Node Pool that is part of a LKE cluster, so there is no need define the `node-group-auto-discovery`  or `nodes` flags, see [examples/cluster-autoscaler-autodiscover.yaml](examples/cluster-autoscaler-autodiscover.yaml) for an example of a kubernetes deployment.

It is mandatory to define the cloud configuration file `cloud-config`.
You can see an example of the cloud config file at [examples/cluster-autoscaler-secret.yaml](examples/cluster-autoscaler-secret.yaml), it is a an INI file with the following fields: 


| Key | Value | Mandatory | Default |
|-----|-------|-----------|---------|
| global/linode-token | Linode API Token with Read/Write permission for Kubernetes and Linodes | yes | none |
| global/lke-cluster-id | ID of the LKE cluster (numeric of the form: 12345, you can get this via linode-cli or looking at the first number of a linode in a pool, e.g. for lke15989-19461-5fec9212fad2 the lke-cluster-id is "15989") | yes | none |
| global/defaut-min-size-per-linode-type | minimum size of a node group (must be > 1) | no | 1 |
| global/defaut-max-size-per-linode-type | maximum size of a node group | no | 254 |
| global/do-not-import-pool-id | Pool id (numeric of the form: 12345) that will be excluded from the pools managed by the cluster autoscaler; can be repeated | no | none
| nodegroup \"linode_type\"/min-size" | minimum size for a specific node group | no | global/defaut-min-size-per-linode-type |
| nodegroup \"linode_type\"/max-size" | maximum size for a specific node group | no | global/defaut-min-size-per-linode-type |

Log levels of intertest for the Linode provider are:
* 1 (flag: ```--v=1```): basic logging at start;
* 2 (flag: ```--v=2```): logging of the node group composition at every scan;

## Development

Make sure you're inside the `cluster-autoscaler` path of the [autoscaler
repository](https://github.com/kubernetes/autoscaler).

Create the docker image:
```
make container
```
tag the generated docker image and push it to a registry.
