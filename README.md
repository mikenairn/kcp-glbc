# KCP Global Load Balancer

![build status badge](https://github.com/kuadrant/kcp-glbc/actions/workflows/ci.yaml/badge.svg)

The KCP Global Load Balancer Controller (GLBC) solves multi cluster ingress use cases when leveraging KCP to provide transparent multi cluster deployments. 

The main use case it solves currently is providing you with a single host that can be used to access your workload and bring traffic to the correct physical clusters. The GLBC manages the DNS for this host and provides you with a valid TLS certificate. If your workload moves/is moved or expands contracts across clusters, GLBC will ensure that the DNS for this host is correct and traffic will continue to reach your workload.

It also offers the ability to setup a health check for your workload. When this health check fails for a particular cluster, the unhealthy cluster will be removed from the DNS response.

The GLBCs main API is the kubernetes Ingress object. GLBC watches for Ingress objects and mutates them adding in the GLBC managed host and tls certificate. 

In the future we will also add the same functionality for Gateway API and OpenShift Route. 

## Getting Started

Clone the repo and run:

```bash
make local-setup
```
> NOTE: If errors are encountered during the local-setup, refer to the [Troubleshooting Installation](https://github.com/Kuadrant/kcp-glbc/blob/main/docs/troubleshooting.md) document.

This script will:

- build all the binaries
- deploy three kubernetes 1.22 clusters locally using `kind`.
- deploy and configure the ingress controllers in each cluster.
- start the KCP server.
- create KCP workspaces for glbc and user resources:
  - kcp-glbc
  - kcp-glbc-compute
  - kcp-glbc-user
  - kcp-glbc-user-compute
- add workload clusters to the *-compute workspaces
  - kcp-glbc-compute: 1x  kind cluster
  - kcp-glbc-user-compute: 2x kind clusters
- deploy glbc dependencies (cert-manager) into kcp-glbc workspace.
    

Once the script is done, copy the `Run locally:` commands from the output.
Open a new terminal, and from the root of the project, run the copied commands.

Now you can create a new ingress resource from the root of the project:

```bash 
export KUBECONFIG=.kcp/admin.kubeconfig
./bin/kubectl-kcp workspace use root:kuadrant
kubectl apply -f samples/echo-service/echo.yaml
```
N.B. It's important that you use the `.kcp/admin.kubeconfig` kube config and switch to the `root:kuadrant` workspace.

To verify the resources were created successfully, check the output of the following:

```bash
kubectl get deployment,service,ingress
```

### Add CRC cluster (optional)

With a running local setup i.e. you have successfully executed `make local-setup`, you can run the following to create and add a CRC cluster:

```bash
./utils/local-setup-add-crc-cluster.sh
```

```bash
$ kubectl get synctargets -o wide
NAME              LOCATION          READY   SYNCED API RESOURCES
kcp-cluster-1     kcp-cluster-1     True    ["deployments.apps","ingresses.networking.k8s.io","secrets","services"]
kcp-cluster-2     kcp-cluster-2     True    ["deployments.apps","ingresses.networking.k8s.io","secrets","services"]
kcp-cluster-crc   kcp-cluster-crc   True    ["deployments.apps","ingresses.networking.k8s.io","secrets","services"]
```

You must have crc installed(https://crc.dev/crc/), and have your openshift pull secret(https://cloud.redhat.com/openshift/create/local) stored locally in `~/pull-secret`. 
Please check the script comments for any version requirements.

## Development

###Interacting directly with WorkloadClusters

Prefix `kubectl` with the appropriate kubeconfig file from the tmp directory.
For example:

```bash
KUBECONFIG=./tmp/kcp-cluster-1.kubeconfig kubectl get deployments,services,ingress --all-namespaces
```

### Testing

The e2e tests can be executed locally by running the following commands:

#### Terminal 1

Start KCP and create KinD clusters
```bash
make local-setup
```

#### Terminal 2

Run the controller with the same environment as CI ([e2e](.github/workflows/e2e.yaml)):
```bash
(export $(cat ./config/deploy/local/kcp-glbc/controller-config.env.ci | xargs) && \
KUBECONFIG=./tmp/kcp.kubeconfig ./bin/kcp-glbc)
```

#### Terminal 3

Run the e2e tests:

```bash
(export $(cat ./config/deploy/local/kcp-glbc/controller-config.env.ci | xargs) && \
export KUBECONFIG="$(pwd)"/.kcp/admin.kubeconfig && \
export CLUSTERS_KUBECONFIG_DIR="$(pwd)/tmp" && \
make e2e)
```

Run the performance tests:

```bash
(export $(cat ./config/deploy/local/kcp-glbc/controller-config.env.ci | xargs) && \
export KUBECONFIG="$(pwd)"/.kcp/admin.kubeconfig &&
make performance TEST_DNSRECORD_COUNT=1 TEST_INGRESS_COUNT=1)
```

Alternatively, You can run the KCP GLBC and/or the tests from your IDE / debugger.

### Local development on a mac
MacOS does not default to allowing you to communicate with ports running in docker containers from the host machine. To bypass this limitation we can use `docker-mac-net-connect`:
```
# Install via Homebrew
$ brew install chipmk/tap/docker-mac-net-connect

# Run the service and register it to launch at boot
$ sudo brew services start chipmk/tap/docker-mac-net-connect
```

This is done automatically as part of the `make local-setup` but will require presenting a sudo password to start the service if it is not configured to autostart.

N.B. This does not remove the requirement to have the DNS records created in a valid DNS service (e.g. route53 in AWS).

