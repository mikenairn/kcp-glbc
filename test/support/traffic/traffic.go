package traffic

import (
	"fmt"
	"sort"

	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"

	kuadrantv1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
	"github.com/kuadrant/kcp-glbc/test/support"

	"github.com/kuadrant/kcp-glbc/pkg/traffic"
)

func Endpoints(t support.Test, ingress traffic.Interface, res dns.HostResolver) []types.GomegaMatcher {
	host := ingress.GetAnnotations()[traffic.ANNOTATION_HCG_HOST]
	targets, err := ingress.GetDNSTargets()
	t.Expect(err).NotTo(gomega.HaveOccurred())
	matchers := []types.GomegaMatcher{}
	continentHosts := map[string]string{}

	if len(targets) == 0 {
		return matchers
	}

	// Add A Record matchers, one per target, dnsName is a continent specific host, value is an IP
	for _, target := range targets {
		continentCode := target.TargetAttributes.Geo.ContinentCode
		continentHost := traffic.GetContinentHost(host, continentCode)
		continentHosts[continentCode] = continentHost
		matchers = append(matchers, support.MatchFieldsP(IgnoreExtras,
			Fields{
				"DNSName":          Equal(continentHost),
				"Targets":          ConsistOf(target.Value),
				"RecordType":       Equal("A"),
				"RecordTTL":        Equal(kuadrantv1.TTL(60)),
				"SetIdentifier":    Equal(fmt.Sprintf("%s.%s", continentCode, target.Value)),
				"ProviderSpecific": ConsistOf(kuadrantv1.ProviderSpecific{{Name: "aws/weight", Value: "120"}}),
			}))
	}

	// Add CNAME Record matchers, one per continent host, dnsName is the host, value is a continent specific host
	for continentCode, continentHost := range continentHosts {
		matchers = append(matchers, support.MatchFieldsP(IgnoreExtras,
			Fields{
				"DNSName":          Equal(host),
				"Targets":          ConsistOf(continentHost),
				"RecordType":       Equal("CNAME"),
				"RecordTTL":        Equal(kuadrantv1.TTL(60)),
				"SetIdentifier":    Equal(continentHost),
				"ProviderSpecific": ConsistOf(kuadrantv1.ProviderSpecific{{Name: "aws/geolocation-continent-code", Value: continentCode}}),
			}))
	}

	continentCodes := make([]string, 0, len(continentHosts))
	for k := range continentHosts {
		continentCodes = append(continentCodes, k)
	}
	sort.Strings(continentCodes)
	defaultContinentHost := continentHosts[continentCodes[0]]

	// Add default CNAME Record matcher, one per dns record, dnsName is the host, value is the first continent specific host in the list
	matchers = append(matchers, support.MatchFieldsP(IgnoreExtras,
		Fields{
			"DNSName":          Equal(host),
			"Targets":          ConsistOf(defaultContinentHost),
			"RecordType":       Equal("CNAME"),
			"RecordTTL":        Equal(kuadrantv1.TTL(60)),
			"SetIdentifier":    Equal("default"),
			"ProviderSpecific": ConsistOf(kuadrantv1.ProviderSpecific{{Name: "aws/geolocation-country-code", Value: "*"}}),
		}))
	return matchers
}
