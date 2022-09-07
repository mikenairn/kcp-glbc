## Local Development

The following describes how to deploy and test the GLBC running on a kind cluster created as part of the local development setup.

### Terminal 1

#### Run KCP

Run the `local-setup` script to create the test kind clusters, configure KCP workspaces and start a local KCP process.

```shell
$ make local-setup
```

### Terminal 2

Target the admin kcp kubeconfig:

```shell
export KUBECONFIG=.kcp/admin.kubeconfig
```

#### Check local setup state

##### Local deploy config

If this is the first time running the `make local-setup` command it will generate a set of local configuration files which
can be used to modify the configuration of GLBC.

```shell
$ tree -I '*.yaml|*.template' config/deploy/local/
config/deploy/local/
├── aws-credentials.env
└── controller-config.env
```

These files will not be committed and can be modified as required by you.

##### Workspaces

**root:kuadrant**

```shell
$ ./bin/kubectl-kcp ws root:kuadrant
Current workspace is "root:kuadrant" (type "root:universal").
$ kubectl get apiexports
NAME                  AGE
cert-manager-stable   15m
glbc-root-kuadrant         12m
kubernetes            15m
$ kubectl get apibindings
NAME                  AGE
apiresource.kcp.dev   16m
cert-manager          15m
glbc                  13m
kubernetes            15m
scheduling.kcp.dev    16m
tenancy.kcp.dev       16m
workload.kcp.dev      16m
$ kubectl get deployments -A
NAMESPACE      NAME           READY   UP-TO-DATE   AVAILABLE   AGE
cert-manager   cert-manager   0/1     0            0           13m
$ kubectl get synctargets -o wide
NAME            LOCATION        READY   SYNCED API RESOURCES   KEY                                      AGE
glbc            glbc            True                           832Kocbfr9pZCD62hs7bt3No2aylrt9lYwTJYa   16m
kcp-cluster-1   kcp-cluster-1   True                           aSmDOEWMWf6IEqrPjoUAOgn0XNeFUa05deJjLB   14m
kcp-cluster-2   kcp-cluster-2   True                           9o7VjBmvhoDPLl1txc7N6EaXcWyU5oxorBOXdp   14m
$ kubectl get negotiatedapiresources
NAME
deployments.v1.apps
ingresses.v1.networking.k8s.io
services.v1.core
```

#### Deploy GLBC

```shell
$ ./bin/kubectl-kcp ws root:kuadrant
````

Deploy the GLBC into the `kcp-glbc` workspace.

```shell
$ KUBECONFIG=.kcp/admin.kubeconfig ./utils/deploy.sh
```

Check the deployment:
```shell
$ kubectl get deployments -A
NAMESPACE      NAME                          READY   UP-TO-DATE   AVAILABLE   AGE
cert-manager   cert-manager                  0/1     0            0           17m
kcp-glbc       kcp-glbc-controller-manager   0/1     0            0           76s
```

It's not currently possible to check the logs via KCP, but you can view them by accessing the deployment on the kind cluster directly: 

```shell
$ namespace=$(kubectl --context kind-kcp-cluster-glbc-control get deployments --all-namespaces | grep -e kcp-glbc-controller-manager | awk '{print $1 }')
$ kubectl --kubeconfig ~/.kube/config --context kind-kcp-cluster-glbc-control logs -f deployments/kcp-glbc-controller-manager -n $namespace               
2022-06-20T09:59:47.622Z INFO runtime/proc.go:255 Creating TLS certificate provider {"issuer": "glbc-ca"}
....
```
N.B. Make sure you run this command using the correct kubeconfig and context i.e. Not the KCP one.

### Terminal 3

Test the deployment using the sample service.

```shell
$ export KUBECONFIG=.kcp/admin.kubeconfig
````

Move into your home workspace
```shell
$ ./bin/kubectl-kcp ws
Current workspace is "root:users:zu:yc:kcp-admin".
Note: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.
```

Create kubernetes and glbc APIBindings
```shell
$ kubectl apply -f config/deploy/local/kcp-glbc/apiexports/root-kuadrant-kubernetes-apibinding.yaml
apibinding.apis.kcp.dev/kubernetes created
$ kubectl apply -f config/deploy/local/kcp-glbc/apiexports/root-kuadrant-glbc-apibinding.yaml 
apibinding.apis.kcp.dev/glbc created
```

Deploy the echo service

```shell
$ kubectl apply -f samples/echo-service/echo.yaml
service/httpecho-both created
deployment.apps/echo-deployment created
ingress.networking.k8s.io/ingress-nondomain created
```

Check the deployment:
```shell
$ kubectl get deployments -A
NAMESPACE   NAME              READY   UP-TO-DATE   AVAILABLE   AGE
default     echo-deployment   0/1     0            0           31s
```

Check what cluster was selected for the deployment:

```shell
$ kubectl get deployment echo-deployment -n default -o json | jq .metadata.labels
{
  "claimed.internal.apis.kcp.dev/23927525fc377edc1b0d643c368ef3e53": "23927525fc377edc1b0d643c368ef3e53f085ab6362ce2e608fc0552",
  "state.workload.kcp.dev/aSmDOEWMWf6IEqrPjoUAOgn0XNeFUa05deJjLB": "Sync"
}
$ ./bin/kubectl-kcp ws root:kuadrant
Current workspace is "root:kuadrant" (type "root:universal").
$ kubectl get synctargets -o wide
NAME            LOCATION        READY   SYNCED API RESOURCES   KEY                                      AGE
glbc            glbc            True                           832Kocbfr9pZCD62hs7bt3No2aylrt9lYwTJYa   36m
kcp-cluster-1   kcp-cluster-1   True                           aSmDOEWMWf6IEqrPjoUAOgn0XNeFUa05deJjLB   34m
kcp-cluster-2   kcp-cluster-2   True                           9o7VjBmvhoDPLl1txc7N6EaXcWyU5oxorBOXdp   34m
```

Use the "state.workload.kcp.dev/${SYNCTARGET KEY}" label to figure out what synctarget is being used. kcp-cluster-1 in this case.

Check the logs:

```shell
$ kubectl --kubeconfig ~/.kube/config --context kind-kcp-cluster-1 get deployments --all-namespaces | grep echo
kcp-yj9ujet4lw2h                    echo-deployment                     1/1     1            1           6m4s
```

```shell
$ kubectl --kubeconfig ~/.kube/config --context kind-kcp-cluster-1 logs -f deployments/echo-deployment -n kcp-yj9ujet4lw2h
Echo server listening on port 8080.
```
N.B. Make sure you run this command using the correct kubeconfig i.e., Not the KCP one, and set the context to the selected cluster.
