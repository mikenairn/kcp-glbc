package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/strings/slices"

	workload "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kuadrant/kcp-glbc/pkg/_internal/metadata"
	"github.com/kuadrant/kcp-glbc/pkg/_internal/slice"
	v1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
)

func NewIngress(i *networkingv1.Ingress) *Ingress {
	return &Ingress{Ingress: i}
}

type Ingress struct {
	*networkingv1.Ingress
	generatedHost string
}

func (a *Ingress) SetDNSLBHost(host string) {
	a.Ingress.Status.LoadBalancer = corev1.LoadBalancerStatus{
		Ingress: []corev1.LoadBalancerIngress{
			{
				Hostname: host,
			},
		},
	}

}

func (a *Ingress) SetHCGHost(s string) {
	a.generatedHost = s
}

func (a *Ingress) GetSyncTargets() []string {
	return getSyncTargets(a.Ingress)
}

// TMCEnabed this is a very temporary solution to allow us to work with both advanced and none advanced scheduling clusters. IT SHOULD BE REMOVED ASAP
func (a *Ingress) TMCEnabed() bool {
	// check the annotations for status
	if tmcEnabled(a) {
		return true
	}
	enabled := true
	//once the status gets set to something other than the glbc provided host we are sure it is not advanced scheduling
	if len(a.Status.LoadBalancer.Ingress) == 1 {
		if a.Status.LoadBalancer.Ingress[0].Hostname != "" {
			host := a.GetHCGHost()
			enabled = a.Status.LoadBalancer.Ingress[0].Hostname == host
		} else {
			enabled = a.Status.LoadBalancer.Ingress[0].IP == ""
		}
	}
	return enabled
}

func (a *Ingress) GetKind() string {
	return "Ingress"
}

func (a *Ingress) GetHosts() []string {
	var hosts []string
	for _, rule := range a.Spec.Rules {
		if !slices.Contains(hosts, rule.Host) {
			hosts = append(hosts, rule.Host)
		}
	}

	return hosts
}

func (a *Ingress) GetHCGHost() string {
	return a.generatedHost
}

func (a *Ingress) AddTLS(host string, secret *corev1.Secret) {
	for i, tls := range a.Spec.TLS {
		if slice.ContainsString(tls.Hosts, host) {
			a.Spec.TLS[i] = networkingv1.IngressTLS{
				Hosts:      []string{host},
				SecretName: secret.Name,
			}
			return
		}
	}
	a.Spec.TLS = append(a.Spec.TLS, networkingv1.IngressTLS{
		Hosts:      []string{host},
		SecretName: secret.GetName(),
	})
}

func (a *Ingress) RemoveTLS(hosts []string) {
	for _, removeHost := range hosts {
		for i, tls := range a.Spec.TLS {
			tlsHosts := tls.Hosts
			for j, host := range tls.Hosts {
				if host == removeHost {
					tlsHosts = append(hosts[:j], hosts[j+1:]...)
				}
			}
			// if there are no hosts remaining remove the entry for TLS
			if len(tlsHosts) == 0 {
				a.Spec.TLS = append(a.Spec.TLS[:i], a.Spec.TLS[i+1:]...)
			}
		}
	}
}

// GetDNSTargets will return the LB hosts and or IPs from the the Ingress object associated with the cluster they came from
func (a *Ingress) GetDNSTargets() ([]dns.Target, error) {
	statuses, err := a.getStatuses()
	if err != nil {
		return nil, err
	}
	dnsTargets := []dns.Target{}
	for cluster, status := range statuses {
		targetAttributes, err := getTargetAttributes()
		if err != nil {
			return nil, err
		}
		for _, lb := range status.LoadBalancer.Ingress {
			dnsTarget := dns.Target{TargetAttributes: *targetAttributes}
			dnsTarget.Cluster = cluster.String()
			if lb.IP != "" {
				dnsTarget.TargetType = dns.TargetTypeIP
				dnsTarget.Value = lb.IP
			}
			if lb.Hostname != "" {
				dnsTarget.TargetType = dns.TargetTypeHost
				dnsTarget.Value = lb.Hostname

			}
			dnsTargets = append(dnsTargets, dnsTarget)
		}
	}
	return dnsTargets, nil
}

func (a *Ingress) GetSpec() interface{} {
	return a.Spec
}

func (a *Ingress) Transform(old Interface) error {
	oldIngress := old.(*Ingress)
	patches := []patch{}
	if a.Spec.Rules != nil && !equality.Semantic.DeepEqual(a.Spec.Rules, oldIngress.Spec.Rules) {
		rulesPatch := patch{
			OP:    "replace",
			Path:  "/rules",
			Value: a.Spec.Rules,
		}
		patches = append(patches, rulesPatch)
	}
	if a.Spec.TLS != nil && !equality.Semantic.DeepEqual(a.Spec.TLS, oldIngress.Spec.TLS) {
		tlsPatch := patch{
			OP:    "replace",
			Path:  "/tls",
			Value: a.Spec.TLS,
		}
		patches = append(patches, tlsPatch)
	}
	if err := applyTransformPatches(patches, a); err != nil {
		return err
	}
	// ensure we don't modify the actual spec (TODO TMC once transforms are default remove this check)
	if a.TMCEnabed() {
		// we always assume tmc is enabled and do this until we are sure it is not enabled. We will be sure before the DNS is actually published as the check is based on status
		oldSpec, ok := old.GetSpec().(networkingv1.IngressSpec)
		if !ok {
			return fmt.Errorf("expected the spec to be an ingress spec %v", old.GetSpec())
		}
		a.Spec = oldSpec
	}
	return nil

}

func (a *Ingress) getStatuses() (map[logicalcluster.Name]networkingv1.IngressStatus, error) {
	statuses := map[logicalcluster.Name]networkingv1.IngressStatus{}
	for k, v := range a.Annotations {
		status := networkingv1.IngressStatus{}
		if !strings.Contains(k, workload.InternalClusterStatusAnnotationPrefix) {
			continue
		}
		annotationParts := strings.Split(k, "/")
		if len(annotationParts) < 2 {
			return nil, fmt.Errorf("advanced scheduling annotation malformed %s value %s", workload.InternalClusterStatusAnnotationPrefix, a.Annotations[k])
		}
		clusterName := annotationParts[1]
		err := json.Unmarshal([]byte(v), &status)
		if err != nil {
			return statuses, err
		}
		statuses[logicalcluster.New(clusterName)] = status
	}

	if !tmcEnabled(a) {
		// when tmc enabled we don't want this status
		cluster := logicalcluster.From(a)
		statuses[cluster] = a.Status
	}
	return statuses, nil
}

func (a *Ingress) ProcessCustomHosts(_ context.Context, dvs *v1.DomainVerificationList, _ CreateOrUpdateTraffic, _ DeleteTraffic) error {
	generatedHost := a.GetHCGHost()
	if generatedHost == "" && a.DeletionTimestamp == nil {
		return ErrGeneratedHostMissing
	}
	var unverifiedRules []networkingv1.IngressRule
	var verifiedRules []networkingv1.IngressRule
	replacedHosts := []string{}
	//find any rules in the spec that are for unverifiedHosts that are not verified
	for _, rule := range a.Spec.Rules {
		//ignore any rules for generated unverifiedHosts (these are recalculated later)
		if rule.Host == generatedHost {
			continue
		}

		//check against domainverification status
		if IsDomainVerified(rule.Host, dvs.Items) || rule.Host == "" {
			verifiedRules = append(verifiedRules, rule)
		} else {
			//remove rule from accessor and mark it as awaiting verification
			unverifiedRules = append(unverifiedRules, rule)
		}

		//recalculate the generatedhost rule in the spec
		generatedHostRule := *rule.DeepCopy()
		generatedHostRule.Host = generatedHost
		verifiedRules = append(verifiedRules, generatedHostRule)
	}

	if len(unverifiedRules) > 0 {
		metadata.AddLabel(a, LABEL_HAS_PENDING_HOSTS, "true")

		for _, uh := range unverifiedRules {
			replacedHosts = append(replacedHosts, uh.Host)
		}
		metadata.AddAnnotation(a, ANNOTATION_HCG_CUSTOM_HOST_REPLACED, fmt.Sprintf("%v", replacedHosts))
	} else {
		metadata.RemoveLabel(a, LABEL_HAS_PENDING_HOSTS)
		metadata.RemoveAnnotation(a, ANNOTATION_HCG_CUSTOM_HOST_REPLACED)
	}
	// nuke any pending hosts as these will be in the spec when tmc enabled
	if a.TMCEnabed() {
		delete(a.Annotations, ANNOTATION_PENDING_CUSTOM_HOSTS)
	}
	//This needs to be done before we check the pending
	a.Spec.Rules = verifiedRules
	a.RemoveTLS(replacedHosts)

	if !a.TMCEnabed() {
		//TODO(TMC remove below code when TMC is the default)

		pending := &Pending{}
		var preservedPendingRules []networkingv1.IngressRule

		//test all the rules in the pending rules annotation to see if they are verified now
		pendingRulesRaw, exists := a.GetAnnotations()[ANNOTATION_PENDING_CUSTOM_HOSTS]
		if exists {
			if pendingRulesRaw != "" {
				err := json.Unmarshal([]byte(pendingRulesRaw), pending)
				if err != nil {
					return err
				}
			}
			for _, pendingRule := range pending.Rules {
				//recalculate the generatedhost rule in the spec
				generatedHostRule := *pendingRule.DeepCopy()
				generatedHostRule.Host = generatedHost
				a.Spec.Rules = append(a.Spec.Rules, generatedHostRule)

				//check against domainverification status
				if IsDomainVerified(pendingRule.Host, dvs.Items) || pendingRule.Host == "" {
					//add the rule to the spec
					a.Spec.Rules = append(a.Spec.Rules, pendingRule)
				} else {
					preservedPendingRules = append(preservedPendingRules, pendingRule)
				}
			}
		}
		//put the new unverified rules in the list of pending rules and update the annotation
		pending.Rules = append(preservedPendingRules, unverifiedRules...)
		if len(pending.Rules) > 0 {
			metadata.AddLabel(a, LABEL_HAS_PENDING_HOSTS, "true")
			newAnnotation, err := json.Marshal(pending)
			if err != nil {
				return err
			}
			metadata.AddAnnotation(a, ANNOTATION_PENDING_CUSTOM_HOSTS, string(newAnnotation))
			return nil
		}
		metadata.RemoveLabel(a, LABEL_HAS_PENDING_HOSTS)
		metadata.RemoveAnnotation(a, ANNOTATION_PENDING_CUSTOM_HOSTS)
		return nil
	}
	return nil
}

func (a *Ingress) GetLogicalCluster() logicalcluster.Name {
	return logicalcluster.From(a)
}

func (a *Ingress) GetNamespaceName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: a.Namespace,
		Name:      a.Name,
	}
}

func (a *Ingress) String() string {
	return fmt.Sprintf("logical cluster: %v, kind: %v, namespace/name: %v", a.GetLogicalCluster(), a.GetKind(), a.GetNamespaceName())
}
