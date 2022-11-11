package traffic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	workload "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"

	kuadrantv1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
	"github.com/kuadrant/kcp-glbc/pkg/traffic"
	testSupport "github.com/kuadrant/kcp-glbc/test/support/ingress"

	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func defaultTestIngress(hosts []string, backend string, tls []networkingv1.IngressTLS) *networkingv1.Ingress {

	rules := []networkingv1.IngressRule{}
	for _, h := range hosts {
		rules = append(rules, networkingv1.IngressRule{
			Host: h,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{Path: "/", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: backend}}},
					},
				},
			},
		})
	}

	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: networkingv1.IngressSpec{
			Rules: rules,
		},
	}

	ing.Spec.TLS = tls

	return ing
}

func TestProcessCustomHostsIngress(t *testing.T) {
	cases := []struct {
		Name                string
		OriginalIngress     func() *traffic.Ingress
		ExpectedIngress     func() *traffic.Ingress
		DomainVerifications *kuadrantv1.DomainVerificationList
		ExpectErr           bool
	}{
		{
			Name: "test unverified host removed and replaced with glbc host",
			OriginalIngress: func() *traffic.Ingress {
				ing := traffic.NewIngress(defaultTestIngress([]string{"example.com"}, "test",
					[]networkingv1.IngressTLS{
						{Hosts: []string{"example.com"}, SecretName: "test"},
						{Hosts: []string{"guid.hcg.com"}, SecretName: "guid-hcg-com"},
					}))
				ing.SetHCGHost("guid.hcg.com")
				return ing
			},
			ExpectedIngress: func() *traffic.Ingress {
				ing := traffic.NewIngress(defaultTestIngress([]string{"guid.hcg.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"guid.hcg.com"}, SecretName: "guid-hcg-com"},
				}))
				ing.SetHCGHost("guid.hcg.com")
				ing.Annotations = map[string]string{traffic.ANNOTATION_HCG_CUSTOM_HOST_REPLACED: "[example.com]"}
				ing.Labels = map[string]string{traffic.LABEL_HAS_PENDING_HOSTS: "true"}
				return ing
			},
			DomainVerifications: &kuadrantv1.DomainVerificationList{},
		},
		{
			Name: "test verfied host left in place and glbc rule appended",
			OriginalIngress: func() *traffic.Ingress {
				ing := traffic.NewIngress(defaultTestIngress([]string{"example.com"}, "test",
					[]networkingv1.IngressTLS{
						{Hosts: []string{"example.com"}, SecretName: "test"},
						{Hosts: []string{"guid.hcg.com"}, SecretName: "guid-hcg-com"},
					}))
				ing.SetHCGHost("guid.hcg.com")
				return ing
			},
			ExpectedIngress: func() *traffic.Ingress {
				ing := traffic.NewIngress(defaultTestIngress([]string{"example.com", "guid.hcg.com"}, "test",
					[]networkingv1.IngressTLS{
						{Hosts: []string{"example.com"}, SecretName: "test"},
						{Hosts: []string{"guid.hcg.com"}, SecretName: "guid-hcg-com"},
					}))
				ing.SetHCGHost("guid.hcg.com")
				return ing
			},
			DomainVerifications: &kuadrantv1.DomainVerificationList{
				Items: []kuadrantv1.DomainVerification{{
					Spec: kuadrantv1.DomainVerificationSpec{
						Domain: "example.com",
					},
					Status: kuadrantv1.DomainVerificationStatus{
						Verified: true,
					},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			ing := tc.OriginalIngress()
			err := ing.ProcessCustomHosts(context.TODO(), tc.DomainVerifications, nil, nil)
			if tc.ExpectErr && err == nil {
				t.Fatalf("expected an error for ProcessCustomHosts but got none")
			}
			if !tc.ExpectErr && err != nil {
				t.Fatalf("did not expect an error for ProcessCustomHosts but got %s ", err)
			}
			expected := tc.ExpectedIngress()
			if !equality.Semantic.DeepEqual(ing.Spec, expected.Spec) {
				t.Log("exp spec", expected.Spec)
				t.Log("got spec", ing.Spec)
				t.Fatalf("expected processi ingress to match the expected ingress ")
			}
			if !equality.Semantic.DeepEqual(ing.Annotations, expected.Annotations) {
				t.Log("exp annotations", expected.Annotations)
				t.Log("got annotations", ing.Annotations)
			}
			if !equality.Semantic.DeepEqual(ing.Labels, expected.Labels) {
				t.Log("exp labels", expected.Labels)
				t.Log("got labels", ing.Labels)
			}
		})
	}
}

func TestApplyTransformsIngress(t *testing.T) {
	cases := []struct {
		Name string
		// OriginalIngress is the ingress as the user created it
		OriginalIngress func() *networkingv1.Ingress
		// ReconciledIngress is the ingress after the controller has done its work and ready to save it
		ReconciledIngress func() *networkingv1.Ingress
		ExpectTLSDiff     bool
		ExpectRulesDiff   bool
		ExpectErr         bool
	}{
		{
			Name: "test original spec not changed post reconcile and transforms applied single host",
			OriginalIngress: func() *networkingv1.Ingress {
				return defaultTestIngress([]string{"test.com"}, "test", nil)
			},
			ReconciledIngress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"guid.example.com"}, SecretName: "glbc"},
				})
				ing.Labels = map[string]string{
					"state.workload.kcp.dev/c1": "Sync",
				}
				return ing
			},
			ExpectTLSDiff:   true,
			ExpectRulesDiff: true,
		},
		{
			Name: "test original spec not changed post reconcile and transforms applied multiple verified hosts",
			OriginalIngress: func() *networkingv1.Ingress {
				return defaultTestIngress([]string{"test.com", "api.test.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"test.com", "api.test.com"}, SecretName: "test"}})
			},
			ReconciledIngress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"test.com", "api.test.com", "guid.example.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"guid.example.com"}, SecretName: "glbc"},
					{Hosts: []string{"test.com", "api.test.com"}, SecretName: "test"},
				})
				ing.Labels = map[string]string{
					"state.workload.kcp.dev/c1": "Sync",
				}
				return ing
			},
			ExpectTLSDiff:   true,
			ExpectRulesDiff: true,
		},
		{
			Name: "test original spec not changed post reconcile if generated host in spec",
			OriginalIngress: func() *networkingv1.Ingress {
				return defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"test.com"}, SecretName: "test"}, {Hosts: []string{"guid.example.com"}, SecretName: "glbc"}})
			},
			ReconciledIngress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{
					{Hosts: []string{"guid.example.com"}, SecretName: "glbc"},
				})
				ing.Labels = map[string]string{
					"state.workload.kcp.dev/c1": "Sync",
				}
				return ing
			},
			ExpectTLSDiff: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			// take a copy before we apply transforms this will have all the expected changes to the spec
			reconciled := testCase.ReconciledIngress()
			transformedCopy := reconciled.DeepCopy()
			reconciledIng := traffic.NewIngress(reconciled)
			orignal := testCase.OriginalIngress()
			originalIng := traffic.NewIngress(orignal)
			// Apply transforms, this will reset the spec to the original once done
			err := reconciledIng.Transform(originalIng)
			if testCase.ExpectErr {
				if err == nil {
					t.Fatalf("expected an error but got none")
				}
			} else {
				if err != nil {
					t.Fatalf("did not expect an error but got %v", err)
				}
			}
			// after the transform is done, we should have the specs of the original and transformed remain the same
			if !equality.Semantic.DeepEqual(orignal.Spec, reconciled.Spec) {
				t.Fatalf("expected the spec of the orignal and transformed to have remained the same. Expected %v Got %v", orignal.Spec, reconciled.Spec)
			}
			t.Log(reconciledIng.Annotations)
			// we should now have annotations applying the transforms. Validate the transformed spec matches the transform annotations.
			if err := testSupport.ValidateTransformed(transformedCopy.Spec, reconciledIng, testCase.ExpectRulesDiff, testCase.ExpectTLSDiff); err != nil {
				t.Fatalf("transforms were invalid %s", err)
			}

		})
	}
}

func TestGetDNSTargetsIngress(t *testing.T) {

	var (
		lbHostFmt  = "lb%d.example.com"
		lbIPFmt    = "53.23.2.%d"
		clusterFmt = "c%d"
	)

	var containsTarget = func(targets []dns.Target, target dns.Target) bool {
		for _, t := range targets {
			if equality.Semantic.DeepEqual(t, target) {
				return true
			}
		}
		return false
	}

	cases := []struct {
		Name      string
		Ingress   func() *networkingv1.Ingress
		ExpectErr bool
		Validate  func([]dns.Target) error
	}{
		{
			Name: "test single cluster host",
			Ingress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{{
					Hosts:      []string{"guid.example.com"},
					SecretName: "test",
				}})
				c1 := networkingv1.IngressStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								Hostname: fmt.Sprintf(lbHostFmt, 0),
							},
						},
					},
				}
				ing.Annotations = map[string]string{}
				jsonStatus, _ := json.Marshal(c1)
				ing.Annotations[workload.InternalClusterStatusAnnotationPrefix+fmt.Sprintf(clusterFmt, 0)] = string(jsonStatus)
				return ing
			},
			Validate: func(targets []dns.Target) error {
				if len(targets) != 1 {
					return fmt.Errorf("expected a single dns target but got %v", len(targets))
				}

				targetHost := fmt.Sprintf(lbHostFmt, 0)
				targetCluster := fmt.Sprintf(clusterFmt, 0)
				expectedTarget := dns.Target{
					Cluster:    targetCluster,
					TargetType: dns.TargetTypeHost,
					Value:      targetHost,
					TargetAttributes: dns.TargetAttributes{
						Geo: dns.GeoAttributes{
							Continent:     dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT,
							ContinentCode: dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE,
						},
					},
				}
				if !containsTarget(targets, expectedTarget) {
					return fmt.Errorf("dns target %v not present", expectedTarget)
				}
				return nil
			},
		},
		{
			Name: "test multiple clusters hosts",
			Ingress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{{
					Hosts:      []string{"guid.example.com"},
					SecretName: "test",
				}})
				c1 := networkingv1.IngressStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								Hostname: fmt.Sprintf(lbHostFmt, 0),
							},
						},
					},
				}
				c2 := networkingv1.IngressStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								Hostname: fmt.Sprintf(lbHostFmt, 1),
							},
						},
					},
				}
				ing.Annotations = map[string]string{}
				jsonStatus, _ := json.Marshal(c1)
				ing.Annotations[workload.InternalClusterStatusAnnotationPrefix+fmt.Sprintf(clusterFmt, 0)] = string(jsonStatus)
				jsonStatus, _ = json.Marshal(c2)
				ing.Annotations[workload.InternalClusterStatusAnnotationPrefix+fmt.Sprintf(clusterFmt, 1)] = string(jsonStatus)
				return ing
			},
			Validate: func(targets []dns.Target) error {
				if len(targets) != 2 {
					return fmt.Errorf("expected 2 dns targets but got %v", len(targets))
				}
				for i := range targets {
					targetCluster := fmt.Sprintf(clusterFmt, i)
					targetHost := fmt.Sprintf(lbHostFmt, i)
					expectedTarget := dns.Target{
						Cluster:    targetCluster,
						TargetType: dns.TargetTypeHost,
						Value:      targetHost,
						TargetAttributes: dns.TargetAttributes{
							Geo: dns.GeoAttributes{
								Continent:     dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT,
								ContinentCode: dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE,
							},
						},
					}

					if !containsTarget(targets, expectedTarget) {
						return fmt.Errorf("dns target %v not present", expectedTarget)
					}
				}
				return nil
			},
		},
		{
			Name: "test multiple clusters IPs",
			Ingress: func() *networkingv1.Ingress {
				ing := defaultTestIngress([]string{"guid.example.com"}, "test", []networkingv1.IngressTLS{{
					Hosts:      []string{"guid.example.com"},
					SecretName: "test",
				}})
				c1 := networkingv1.IngressStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								IP: fmt.Sprintf(lbIPFmt, 0),
							},
						},
					},
				}
				c2 := networkingv1.IngressStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: []v1.LoadBalancerIngress{
							{
								IP: fmt.Sprintf(lbIPFmt, 1),
							},
						},
					},
				}
				ing.Annotations = map[string]string{}
				jsonStatus, _ := json.Marshal(c1)
				ing.Annotations[workload.InternalClusterStatusAnnotationPrefix+fmt.Sprintf(clusterFmt, 0)] = string(jsonStatus)
				jsonStatus, _ = json.Marshal(c2)
				ing.Annotations[workload.InternalClusterStatusAnnotationPrefix+fmt.Sprintf(clusterFmt, 1)] = string(jsonStatus)
				return ing
			},
			Validate: func(targets []dns.Target) error {
				if len(targets) != 2 {
					return fmt.Errorf("expected a single dns target but got %v", len(targets))
				}
				for i := range targets {
					targetCluster := fmt.Sprintf(clusterFmt, i)
					targetHost := fmt.Sprintf(lbIPFmt, i)
					expectedTarget := dns.Target{
						Cluster:    targetCluster,
						TargetType: dns.TargetTypeIP,
						Value:      targetHost,
						TargetAttributes: dns.TargetAttributes{
							Geo: dns.GeoAttributes{
								Continent:     dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT,
								ContinentCode: dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE,
							},
						},
					}

					if !containsTarget(targets, expectedTarget) {
						return fmt.Errorf("dns target %v not present", expectedTarget)
					}
				}
				return nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			ti := traffic.NewIngress(tc.Ingress())
			targets, err := ti.GetDNSTargets()
			if tc.ExpectErr && err == nil {
				t.Fatalf("expected an error but got none")
			}
			if !tc.ExpectErr && err != nil {
				t.Fatalf("did not expect an error but got %s ", err)
			}
			t.Log("targets", targets)
			if err := tc.Validate(targets); err != nil {
				t.Fatalf("unable to validate dns targets %s", err)
			}
		})
	}
}
