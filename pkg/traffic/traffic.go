package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	workload "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kuadrant/kcp-glbc/pkg/_internal/metadata"
	v1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
)

type ReconcileStatus int

const (
	ReconcileStatusStop ReconcileStatus = iota
	ReconcileStatusContinue

	ANNOTATION_TRAFFIC_KEY              = "kuadrant.dev/traffic-key"
	ANNOTATION_TRAFFIC_KIND             = "kuadrant.dev/traffic-kind"
	ANNOTATION_CERTIFICATE_STATE        = "kuadrant.dev/certificate-status"
	ANNOTATION_HCG_HOST                 = "kuadrant.dev/host.generated"
	ANNOTATION_HEALTH_CHECK_PREFIX      = "kuadrant.experimental/health-"
	ANNOTATION_HCG_CUSTOM_HOST_REPLACED = "kuadrant.dev/custom-hosts-status.removed"
	ANNOTATION_PENDING_CUSTOM_HOSTS     = "kuadrant.dev/pendingCustomHosts"
	LABEL_HAS_PENDING_HOSTS             = "kuadrant.dev/hasPendingCustomHosts"
	FINALIZER_CASCADE_CLEANUP           = "kuadrant.dev/cascade-cleanup"
)

type patch struct {
	OP    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

type CreateOrUpdateTraffic func(ctx context.Context, i Interface) error
type DeleteTraffic func(ctx context.Context, i Interface) error

type Interface interface {
	runtime.Object
	metav1.Object
	GetKind() string
	GetHosts() []string
	GetHCGHost() string
	SetDNSLBHost(string)
	SetHCGHost(string)
	Transform(previous Interface) error
	GetDNSTargets() ([]dns.Target, error)
	GetLogicalCluster() logicalcluster.Name
	GetNamespaceName() types.NamespacedName
	AddTLS(host string, secret *corev1.Secret)
	RemoveTLS(host []string)
	ProcessCustomHosts(context.Context, *v1.DomainVerificationList, CreateOrUpdateTraffic, DeleteTraffic) error
	GetSyncTargets() []string
	GetSpec() interface{}
	TMCEnabed() bool
}

func tmcEnabled(obj metav1.Object) bool {
	has, _ := metadata.HasAnnotationsContaining(obj, workload.InternalClusterStatusAnnotationPrefix)
	return has
}

func getSyncTargets(obj metav1.Object) []string {
	_, labels := metadata.HasLabelsContaining(obj, workload.ClusterResourceStateLabelPrefix)
	clusters := []string{}
	for l := range labels {
		labelParts := strings.Split(l, "/")
		if len(labelParts) < 2 {
			continue
		}
		clusters = append(clusters, labelParts[1])
	}
	return clusters
}

func getTargetAttributes() (*dns.TargetAttributes, error) {
	//ToDo Add implementation to retrieve sync target attributes
	return dns.NewTargetAttributes(), nil
}

type Pending struct {
	Rules []networkingv1.IngressRule `json:"rules"`
}

// IsDomainVerified will take the host and recursively remove subdomains searching for a matching domainverification
// that is verified. Until either a match is found, or the subdomains run out.
func IsDomainVerified(host string, dvs []v1.DomainVerification) bool {
	for _, dv := range dvs {
		if dv.Spec.Domain == host && dv.Status.Verified {
			return true
		}
	}
	parentHostParts := strings.SplitN(host, ".", 2)
	//we've run out of sub-domains
	if len(parentHostParts) < 2 {
		return false
	}

	//recurse up the subdomains
	return IsDomainVerified(parentHostParts[1], dvs)
}

func applyTransformPatches(patches []patch, object Interface) error {
	// reset spec diffs
	_, existingDiffs := metadata.HasAnnotationsContaining(object, workload.ClusterSpecDiffAnnotationPrefix)
	for ek := range existingDiffs {
		metadata.RemoveAnnotation(object, ek)
	}
	if len(patches) != 0 {
		d, err := json.Marshal(patches)
		if err != nil {
			return fmt.Errorf("failed to marshal ingress transform patch %s", err)
		}
		// and spec diff for any sync target
		for _, c := range object.GetSyncTargets() {
			metadata.AddAnnotation(object, workload.ClusterSpecDiffAnnotationPrefix+c, string(d))
		}
	}

	return nil
}
