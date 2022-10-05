/*
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

package support

import (
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"

	"github.com/kcp-dev/logicalcluster/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kuadrant/kcp-glbc/pkg/util/env"
)

const (
	TestTimeoutShort  = 1 * time.Minute
	TestTimeoutMedium = 5 * time.Minute
	TestTimeoutLong   = 10 * time.Minute

	workloadClusterKubeConfigDir = "CLUSTERS_KUBECONFIG_DIR"
	testWorkspaceName            = "TEST_WORKSPACE"
	glbcWorkspaceName            = "GLBC_WORKSPACE"
	glbcExportName               = "GLBC_EXPORT"

	maxNameLength          = 63
	randomLength           = 5
	MaxGeneratedNameLength = maxNameLength - randomLength
)

var (
	TestOrganization = getEnvLogicalClusterName(testWorkspaceName, tenancyv1alpha1.RootCluster.Join("kuadrant"))
	GLBCWorkspace    = getEnvLogicalClusterName(glbcWorkspaceName, TestOrganization)
	GLBCExportName   = env.GetEnvString(glbcExportName, "glbc-root-kuadrant")

	ApplyOptions = metav1.ApplyOptions{FieldManager: "kcp-glbc-e2e", Force: true}
)

func getEnvLogicalClusterName(key string, fallback logicalcluster.Name) logicalcluster.Name {
	value, found := os.LookupEnv(key)
	if !found {
		return fallback
	}
	return logicalcluster.New(value)
}

// GenerateName Borrowed from https://github.com/kubernetes/apiserver/blob/v0.25.2/pkg/storage/names/generate.go
func GenerateName(base string) string {
	if len(base) > MaxGeneratedNameLength {
		base = base[:MaxGeneratedNameLength]
	}
	return fmt.Sprintf("%s%s", base, utilrand.String(randomLength))
}
