//go:build e2e
// +build e2e

package e2e

import (
	"crypto/x509/pkix"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuadrant/kcp-glbc/pkg/_internal/env"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
	"github.com/kuadrant/kcp-glbc/pkg/traffic"
	. "github.com/kuadrant/kcp-glbc/test/support/ingress"
	. "github.com/kuadrant/kcp-glbc/test/support/traffic"

	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"

	"github.com/kcp-dev/logicalcluster/v2"
	corev1 "k8s.io/api/core/v1"

	kuadrantv1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	. "github.com/kuadrant/kcp-glbc/test/support"
)

// TestIngress covers the main interactions with the ingress object. Assigning a GLBC host, custom domains transformations and TLS. It uses a dummy DNS provider
func TestIngress(t *testing.T) {
	test := With(t)
	test.T().Parallel()

	// Create the test workspace
	workspace := test.NewTestWorkspace()

	// Create GLBC APIBinding in workspace
	test.CreateGLBCAPIBindings(workspace, GLBCWorkspace, GLBCExportName)
	test.CreatePlacements(workspace)

	// Create a namespace
	namespace := test.NewTestNamespace(InWorkspace(workspace), WithLabel("kuadrant.dev/cluster-type", "glbc-ingresses"))

	name := "echo"

	// Create the Deployment
	_, err := test.Client().Core().Cluster(logicalcluster.From(namespace)).AppsV1().Deployments(namespace.Name).
		Apply(test.Ctx(), DeploymentConfiguration(namespace.Name, name), ApplyOptions)
	test.Expect(err).NotTo(HaveOccurred())
	defer func() {
		test.Expect(test.Client().Core().Cluster(logicalcluster.From(namespace)).AppsV1().Deployments(namespace.Name).
			Delete(test.Ctx(), name, metav1.DeleteOptions{})).
			To(Succeed())
	}()

	// Create the Service
	_, err = test.Client().Core().Cluster(logicalcluster.From(namespace)).CoreV1().Services(namespace.Name).
		Apply(test.Ctx(), ServiceConfiguration(namespace.Name, name, map[string]string{}), ApplyOptions)
	test.Expect(err).NotTo(HaveOccurred())
	defer func() {
		test.Expect(test.Client().Core().Cluster(logicalcluster.From(namespace)).CoreV1().Services(namespace.Name).
			Delete(test.Ctx(), name, metav1.DeleteOptions{})).
			To(Succeed())
	}()

	// Create the Ingress with a custom domain set
	customHost := "ingress-test.ingresses-gblb-custom.com"
	ingConfig := IngressConfiguration(namespace.Name, name, name, customHost)
	//store the original ingress before any reconciles happen
	originalIngress, err := test.Client().Core().Cluster(logicalcluster.From(namespace)).NetworkingV1().Ingresses(namespace.Name).
		Apply(test.Ctx(), ingConfig, ApplyOptions)
	test.Expect(err).NotTo(HaveOccurred())

	defer func() {
		test.Expect(test.Client().Core().Cluster(logicalcluster.From(namespace)).NetworkingV1().Ingresses(namespace.Name).
			Delete(test.Ctx(), name, metav1.DeleteOptions{})).
			To(Succeed())
	}()

	// Create the dummy zone file configmap
	zoneConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigmapName,
			Namespace: ConfigmapNamespace,
		},
	}

	_, _ = test.Client().Core().Cluster(GLBCWorkspace).CoreV1().ConfigMaps(ConfigmapNamespace).
		Create(test.Ctx(), zoneConfigMap, metav1.CreateOptions{})

	ingress := GetIngress(test, namespace, name)
	secretName := traffic.TLSSecretName(ingress)

	// Test our annotations are as expected and that we have not modified the original spec
	test.Eventually(Ingress(test, namespace, name)).WithTimeout(TestTimeoutMedium).Should(And(
		WithTransform(Labels, And(
			HaveKey(traffic.LABEL_HAS_PENDING_HOSTS),
		)),
		// ensure the original spec has not changed
		Satisfy(OriginalSpecUnchanged(test, &originalIngress.Spec)),
	))
	test.T().Log("ingress in expected state")
	// now our ingress is in the expected state assert TLS, DNS and Transforms correct
	ingress = GetIngress(test, namespace, name)
	record := GetDNSRecord(test, namespace, name)
	hostname := record.Annotations[traffic.ANNOTATION_HCG_HOST]
	resolver := dns.ConfigMapHostResolver{}
	zoneID := os.Getenv("AWS_DNS_PUBLIC_ZONE_ID")
	test.Expect(zoneID).NotTo(BeNil())

	//TLS
	issuer := env.GetEnvString("GLBC_TLS_PROVIDER", "glbc-ca")
	fields := map[string]types.GomegaMatcher{
		"DNSNames": ConsistOf(hostname),
	}
	switch issuer {

	case "glbc-ca":
		fields["Issuer"] = Equal(pkix.Name{
			Organization: []string{"Kuadrant"},
			Names: []pkix.AttributeTypeAndValue{
				{
					Type:  []int{2, 5, 4, 10},
					Value: "Kuadrant",
				},
			},
		})

	case "le-staging":
		fields["Issuer"] = Equal(pkix.Name{
			Country:      []string{"US"},
			Organization: []string{"(STAGING) Let's Encrypt"},
			CommonName:   "(STAGING) Artificial Apricot R3",
			Names: []pkix.AttributeTypeAndValue{
				{
					Type:  []int{2, 5, 4, 6},
					Value: "US",
				},
				{
					Type:  []int{2, 5, 4, 10},
					Value: "(STAGING) Let's Encrypt",
				},
				{
					Type:  []int{2, 5, 4, 3},
					Value: "(STAGING) Artificial Apricot R3",
				},
			},
		})
		fields["Subject"] = Equal(pkix.Name{
			CommonName: hostname,
			Names: []pkix.AttributeTypeAndValue{
				{
					Type:  []int{2, 5, 4, 3},
					Value: hostname,
				},
			},
		})

	case "le-production":
		fields["Issuer"] = Equal(pkix.Name{
			Country:      []string{"US"},
			Organization: []string{"Let's Encrypt"},
			CommonName:   "Artificial Apricot R3",
			Names: []pkix.AttributeTypeAndValue{
				{
					Type:  []int{2, 5, 4, 6},
					Value: "US",
				},
				{
					Type:  []int{2, 5, 4, 10},
					Value: "Let's Encrypt",
				},
				{
					Type:  []int{2, 5, 4, 3},
					Value: "R3",
				},
			},
		})
		fields["Subject"] = Equal(pkix.Name{
			CommonName: hostname,
			Names: []pkix.AttributeTypeAndValue{
				{
					Type:  []int{2, 5, 4, 3},
					Value: hostname,
				},
			},
		})
	}
	// check our tls secret exists and is correct
	test.Eventually(Secret(test, namespace, secretName)).
		WithTimeout(TestTimeoutShort).
		Should(WithTransform(Labels, And(
			HaveKeyWithValue("kuadrant.dev/hcg.managed", "true"),
		)),
			WithTransform(Certificate, PointTo(MatchFields(IgnoreExtras, fields))))

	test.T().Log("tls secret exists and is correctly configured")

	// DNS Check a DNSRecord for the Ingress is created with the expected Spec
	test.Eventually(DNSRecord(test, namespace, name)).Should(And(
		// ensure the ingress certificate is marked as ready when the DNSrecord is created
		WithTransform(DNSRecordToCertReady(test, namespace), Equal("ready")),
		WithTransform(DNSRecordEndpoints, HaveLen(3)),
		WithTransform(Annotations, And(
			HaveKey(traffic.ANNOTATION_HCG_HOST),
		)),
		WithTransform(DNSRecordEndpoints, ContainElements(Endpoints(test, ingress, &resolver))),
		WithTransform(DNSRecordCondition(zoneID, kuadrantv1.DNSRecordFailedConditionType), MatchFieldsP(IgnoreExtras,
			Fields{
				"Status":  Equal("False"),
				"Reason":  Equal("ProviderSuccess"),
				"Message": Equal("The DNS provider succeeded in ensuring the record"),
			})),
	))
	test.T().Log("DNS is as expected")
	glbcHost := record.Annotations[traffic.ANNOTATION_HCG_HOST]

	// Test that our transforms have the expected spec and that our status is set to the generated host
	test.Eventually(Ingress(test, namespace, name)).WithTimeout(TestTimeoutShort).Should(And(
		Satisfy(OriginalSpecUnchanged(test, &originalIngress.Spec)),
		Satisfy(TransformedSpec(test, GetDefaultSpec(glbcHost, secretName, name), true, true)),
		//check that we have a LB set to our generated host
		WithTransform(LoadBalancerIngresses, HaveLen(1)),
	))

	ingress = GetIngress(test, namespace, name)
	if !LBHostEqualToGeneratedHost(ingress, record) {
		test.T().Fatalf("Generated host label on the ingress %s does not match load balancer host name", record.Annotations[traffic.ANNOTATION_HCG_HOST])
	}
	test.T().Log("transforms are in place and ingress is ready (dns load balancer is set in the status)")

	// Create a domain verification for the custom domain
	_, _ = test.Client().Kuadrant().Cluster(logicalcluster.From(ingress)).KuadrantV1().DomainVerifications().Create(test.Ctx(), &kuadrantv1.DomainVerification{
		ObjectMeta: metav1.ObjectMeta{
			Name: customHost,
		},
		Spec: kuadrantv1.DomainVerificationSpec{
			Domain: customHost,
		},
	}, metav1.CreateOptions{})
	defer func() {
		test.Expect(test.Client().Kuadrant().Cluster(logicalcluster.From(namespace)).KuadrantV1().DomainVerifications().
			Delete(test.Ctx(), customHost, metav1.DeleteOptions{})).
			To(Succeed())
	}()

	// see domain verification is not verified
	test.Eventually(DomainVerification(test, logicalcluster.From(ingress), customHost)).WithTimeout(TestTimeoutMedium).Should(And(
		WithTransform(DomainVerificationFor, Equal(customHost)),
		WithTransform(DomainVerified, Equal(false)),
		WithTransform(DomainToken, Not(Equal(""))),
	))

	// see custom host is not active in ingress
	test.Eventually(Ingress(test, namespace, name)).WithTimeout(TestTimeoutMedium).Should(And(
		Satisfy(OriginalSpecUnchanged(test, &originalIngress.Spec)),
		Satisfy(TransformedSpec(test, GetDefaultSpec(glbcHost, secretName, name), true, true)),
	))
	test.T().Log("domain not verified custom host not propigated to cluster")

	// get domainVerification in order to read required token
	dv, err := test.Client().Kuadrant().Cluster(logicalcluster.From(ingress)).KuadrantV1().DomainVerifications().Get(test.Ctx(), customHost, metav1.GetOptions{})
	test.Expect(err).NotTo(HaveOccurred())

	// set TXT record in DNS
	err = SetTXTRecord(test, customHost, dv.Status.Token)
	test.Expect(err).NotTo(HaveOccurred())

	// see domain verification is verified
	test.Eventually(DomainVerification(test, logicalcluster.From(ingress), customHost)).WithTimeout(TestTimeoutShort).Should(And(
		WithTransform(DomainVerificationFor, Equal(customHost)),
		WithTransform(DomainVerified, Equal(true)),
		WithTransform(DomainToken, Equal(dv.Status.Token)),
	))
	test.T().Log("domain is now verified")
	withCustomDomain := GetDefaultSpec(glbcHost, secretName, name)
	//bit nasty but do it this way as this is how it is appended in the reconcile and the order matters for matching
	originalIngress.Spec.Rules = append(originalIngress.Spec.Rules, withCustomDomain.Rules...)
	withCustomDomain.Rules = originalIngress.Spec.Rules
	originalIngress.Spec.TLS = append(originalIngress.Spec.TLS, withCustomDomain.TLS...)
	withCustomDomain.TLS = originalIngress.Spec.TLS

	// now we have built up our expected transformed spec check it is the same as the transformations applied to the annotations
	test.Eventually(Ingress(test, namespace, name)).WithTimeout(TestTimeoutShort).Should(And(
		Satisfy(TransformedSpec(test, withCustomDomain, true, true)),
	))

	test.T().Log("ingress is transformed correctly and in final state")
}
