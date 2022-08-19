#!/bin/bash

#
# Copyright 2022 Red Hat, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

DEPLOY_SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"
KCP_GLBC_DIR="${DEPLOY_SCRIPT_DIR}/.."
source "${DEPLOY_SCRIPT_DIR}"/.setupEnv
source "${DEPLOY_SCRIPT_DIR}"/.startUtils

#Workspace
GLBC_WORKSPACE=root:default

#Workload clusters
: ${GLBC_WORKLOAD_CLUSTER_NAME:=glbc}

: ${KCP_VERSION:="main"}
KCP_SYNCER_IMAGE="ghcr.io/kcp-dev/kcp/syncer:${KCP_VERSION}"

# GLBC Deployment
GLBC_NAMESPACE=kcp-glbc
DEPLOY_COMPONENTS=glbc,cert-manager
GLBC_KUSTOMIZATION=${KCP_GLBC_DIR}/config/deploy/local

############################################################
# Help                                                     #
############################################################
help()
{
  # Display Help
  echo "Prepares a KCP workspace and deploys GLBC and its dependant components into it."
  echo
  echo "Syntax: deploy.sh [-c|k|m|n|h|w]"
  echo "options:"
  echo "c     Components to deploy (default: ${DEPLOY_COMPONENTS})"
  echo "k     GLBC deployment kustomization directory (default: ${GLBC_KUSTOMIZATION})"
  echo "n     Namespace glbc is being deployed into (default: ${GLBC_NAMESPACE})"
  echo "h     Print this Help."
  echo "w     Workspace to deplpy glbc into (default: ${GLBC_WORKSPACE})."
  echo
}

############################################################
# print_env                                                #
############################################################
print_env()
{
   echo "Current deployment configuration"
   echo
   echo "KubeConfig:"
   echo
   echo "  KUBECONFIG:                       ${KUBECONFIG}"
   echo
   echo "Workspaces:"
   echo
   echo "  ORG_WORKSPACE:                    ${ORG_WORKSPACE}"
   echo "  GLBC_WORKSPACE:                   ${GLBC_WORKSPACE}"
   echo "  GLBC_WORKSPACE_USER:              ${GLBC_WORKSPACE_USER}"
   echo "  GLBC_WORKSPACE_COMPUTE:           ${GLBC_WORKSPACE_COMPUTE}"
   echo "  GLBC_WORKSPACE_USER_COMPUTE:      ${GLBC_WORKSPACE_USER_COMPUTE}"
   echo
   echo "Workload clusters:"
   echo
   echo "  GLBC_WORKLOAD_CLUSTER_NAME:       ${GLBC_WORKLOAD_CLUSTER_NAME}"
   echo "  GLBC_USER_WORKLOAD_CLUSTER_NAME:  ${GLBC_USER_WORKLOAD_CLUSTER_NAME}"
   echo "  KCP_SYNCER_IMAGE:                 ${KCP_SYNCER_IMAGE}"
   echo
   echo "GLBC Deployment:"
   echo
   echo "  GLBC_NAMESPACE:                   ${GLBC_NAMESPACE}"
   echo "  DEPLOY_COMPONENTS:                ${DEPLOY_COMPONENTS}"
   echo "  GLBC_KUSTOMIZATION:               ${GLBC_KUSTOMIZATION}"
   echo
   echo "Misc:"
   echo
   echo "  WAIT_WC_READY                     ${WAIT_WC_READY}"
   echo "  OUTPUT_DIR                        ${OUTPUT_DIR}"
   echo "  KUBECTL_KCP_BIN                   ${KUBECTL_KCP_BIN}"
   echo
}

create_api_binding() {
  name=$1;
  exportName=$2;
  path=$3;
  cat <<EOF | kubectl apply -f -
apiVersion: apis.kcp.dev/v1alpha1
kind: APIBinding
metadata:
  name: ${name}
spec:
  reference:
    workspace:
      path: ${path}
      exportName: ${exportName}
EOF
  kubectl wait --timeout=60s --for=condition=Ready=true apibinding $name
}

create_ns() {
  echo "Creating namespace '${1}'"
  kubectl create namespace ${1} --dry-run=client -o yaml | kubectl apply -f -
}

create_sync_target() {
  kubectl get synctargets ${GLBC_WORKLOAD_CLUSTER_NAME} || {
    echo "Creating workload cluster '${1}'"
    ${KUBECTL_KCP_BIN} workload sync ${1} --kcp-namespace kcp-syncer --syncer-image=${KCP_SYNCER_IMAGE} --resources=ingresses.networking.k8s.io,services --output-file ${OUTPUT_DIR}/${1}-syncer.yaml
    echo "Apply the following syncer config to the intended physical cluster."
    echo ""
    echo "   kubectl apply -f ${OUTPUT_DIR}/${1}-syncer.yaml"
    echo ""
  }
  kubectl annotate --overwrite synctarget ${1} featuregates.experimental.workload.kcp.dev/advancedscheduling='true'

  kubectl wait --timeout=60s --for=condition=VirtualWorkspaceURLsReady=true apiexport kubernetes

  if [[ $WAIT_WC_READY = "true" ]]; then
    echo "This script will automatically continue once the cluster is synced!"
    echo "Waiting for workload cluster ${1} to be ready ..."
    kubectl wait --timeout=300s --for=condition=Ready=true synctargets ${1}
  fi
}

deploy_cert_manager() {
  echo "Deploying Cert Manager"
  create_ns "cert-manager"
  kubectl apply -f ${KCP_GLBC_DIR}/config/cert-manager/cert-manager.yaml
  echo "Waiting for Cert Manager deployments to be ready..."
  kubectl -n cert-manager wait --timeout=300s --for=condition=Available deployments --all
}

deploy_glbc() {
  echo "Creating GLBC namespace"
  create_ns ${GLBC_NAMESPACE}

  echo "Creating issuer"
  #ToDo This shouldn't be forcing us to set a value for the KUBECONFIG env var
  go run ${DEPLOY_SCRIPT_DIR}/certman-issuer/ --glbc-kubeconfig ${KUBECONFIG} --issuer-namespace ${GLBC_NAMESPACE}

  echo "Deploying GLBC"
  ${KUSTOMIZE_BIN} build ${GLBC_KUSTOMIZATION} | kubectl apply -f -
  echo "Waiting for GLBC deployments to be ready..."
  kubectl -n ${GLBC_NAMESPACE} wait --timeout=300s --for=condition=Available deployments --all
}

############################################################
# Script Start                                             #
############################################################

while getopts "c:k:n:hw:W:" arg; do
  case "${arg}" in
    c)
      DEPLOY_COMPONENTS=${OPTARG}
      ;;
    k)
      GLBC_KUSTOMIZATION=${OPTARG}
      ;;
    n)
      GLBC_NAMESPACE=${OPTARG}
      ;;
    h)
      help
      exit 0
      ;;
    w)
      GLBC_WORKSPACE=${OPTARG}
      ;;
    *)
      help
      exit 1
      ;;
  esac
done
shift $((OPTIND-1))

# Misc
# Wait for workload clusters to be ready before continuing
: ${WAIT_WC_READY:="false"}
# Directory to output any generated files to i.e *syncer.yaml
: ${OUTPUT_DIR:=${GLBC_KUSTOMIZATION}}

set -e pipefail

## Check we are targeting a kcp instance
${KUBECTL_KCP_BIN} workspace . > /dev/null || (echo "You must be targeting a KCP API Server, check your current KUBECONIFG and context before continuing!" && exit 1)

print_env
echo "Continuing in 10 seconds, Ctrl+C to stop ..."
sleep 10

${KUBECTL_KCP_BIN} workspace use ${GLBC_WORKSPACE}

############################################################
# Create glbc sync target                                  #
############################################################

## Create GLBC workload cluster
kubectl create namespace kcp-syncer --dry-run=client -o yaml | kubectl apply -f -
create_sync_target ${GLBC_WORKLOAD_CLUSTER_NAME}

## Add location
kubectl apply -f ${KCP_GLBC_DIR}/utils/kcp-contrib/location.yaml

############################################################
# Register APIs                             #
############################################################

## Bind to compute APIs
create_api_binding "kubernetes" "kubernetes" "${GLBC_WORKSPACE}"

## Register the Pod API (required by cert-manager)
kubectl apply -f ${KCP_GLBC_DIR}/utils/kcp-contrib/crds/pods.yaml

## Register GLBC APIs
kubectl apply -f ${KCP_GLBC_DIR}/utils/kcp-contrib/apiresourceschema.yaml

## Register CertManager APIs
kubectl apply -f ${KCP_GLBC_DIR}/config/cert-manager/certificates-apiresourceschema.yaml
kubectl apply -f ${KCP_GLBC_DIR}/config/cert-manager/cert-manager-apiexport.yaml
create_api_binding "cert-manager" "cert-manager-stable" "${GLBC_WORKSPACE}"

############################################################
# Setup GLBC APIExport                                     #
############################################################

if ${DEPLOY_SCRIPT_DIR}/create_glbc_api_export.sh -w "${GLBC_WORKSPACE}" -W "${GLBC_WORKSPACE}" -n "glbc"; then
  echo "GLBC APIExport created successfully for ${ORG_WORKSPACE}:${GLBC_WORKSPACE_USER} workspace!"
else
  echo "GLBC APIExport could not be created!"
  # If the GLBC APIExport can't be created, we shouldn't continue to try and deploy anything!
  exit 0
fi

############################################################
# Deploy GLBC Components                                   #
############################################################

## Deploy components
if [[ $DEPLOY_COMPONENTS =~ "cert-manager" ]]; then
  deploy_cert_manager
fi

if [[ $DEPLOY_COMPONENTS =~ "glbc" ]]; then
  deploy_glbc

  echo ""
  echo "GLBC is now running."
  echo ""
  echo "Try deploying the sample service:"
  echo ""
  echo "     ${KUBECTL_KCP_BIN} workspace use ${GLBC_WORKSPACE_USER}"
  echo "     kubectl apply -f samples/echo-service/echo.yaml"
  echo ""
fi
