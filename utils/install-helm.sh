#!/bin/bash

# Install a version of helm.
# https://helm.sh/docs/intro/install/

set -euo pipefail

if [ ! -f "$1" ]; then
  TMP_DIR=$(mktemp -d)
  cd $TMP_DIR

  echo "Downloading Helm install script"
  curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3
  chmod +x get_helm.sh

  mkdir -p "$(dirname $1)"
  HELM_INSTALL_DIR="$(dirname $1)" DESIRED_VERSION=$2 USE_SUDO=false ./get_helm.sh

  rm -rf $TMP_DIR
fi
