package traffic

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/rs/xid"
	"k8s.io/apimachinery/pkg/api/equality"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	"github.com/kcp-dev/logicalcluster/v2"

	workload "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kuadrant/kcp-glbc/pkg/_internal/metadata"
	"github.com/kuadrant/kcp-glbc/pkg/_internal/slice"
	v1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
	"github.com/kuadrant/kcp-glbc/pkg/dns"
	"github.com/kuadrant/kcp-glbc/pkg/dns/aws"
)

type DnsReconciler struct {
	DeleteDNS        func(ctx context.Context, accessor Interface) error
	GetDNS           func(ctx context.Context, accessor Interface) (*v1.DNSRecord, error)
	CreateDNS        func(ctx context.Context, dns *v1.DNSRecord) (*v1.DNSRecord, error)
	UpdateDNS        func(ctx context.Context, dns *v1.DNSRecord) (*v1.DNSRecord, error)
	WatchHost        func(ctx context.Context, key interface{}, host string) bool
	ForgetHost       func(key interface{}, host string)
	ListHostWatchers func(key interface{}) []dns.RecordWatcher
	Log              logr.Logger
	ManagedDomain    string
	DNSLookup        func(ctx context.Context, host string) ([]dns.HostAddress, error)
}

func (r *DnsReconciler) GetName() string {
	return "DNS Reconciler"
}

func (r *DnsReconciler) Reconcile(ctx context.Context, accessor Interface) (ReconcileStatus, error) {
	if accessor.GetDeletionTimestamp() != nil && !accessor.GetDeletionTimestamp().IsZero() {
		if err := r.DeleteDNS(ctx, accessor); err != nil && !k8errors.IsNotFound(err) {
			return ReconcileStatusStop, err
		}
		return ReconcileStatusContinue, nil
	}

	key := objectKey(accessor)
	// Attempt to retrieve the existing DNSRecord for this traffic object
	existing, err := r.GetDNS(ctx, accessor)
	// If it doesn't exist, create it
	if err != nil {
		if !k8errors.IsNotFound(err) {
			return ReconcileStatusStop, err
		}
		record, err := newDNSRecordForObject(accessor)
		if err != nil {
			return ReconcileStatusContinue, err
		}
		generatedHost := AddHostAnnotations(record, r.ManagedDomain)
		accessor.SetHCGHost(generatedHost)
		// Create the resource in the cluster
		r.Log.V(3).Info("creating DNSRecord ", "record", record.Name)
		_, err = r.CreateDNS(ctx, record)
		if err != nil {
			return ReconcileStatusContinue, err
		}
		return ReconcileStatusContinue, nil
	}
	// If it does exist, update it
	managedHost := metadata.GetAnnotation(existing, ANNOTATION_HCG_HOST)
	if managedHost == "" {
		// This covers upgrade scenario: checking traffic object for the generated host label and updating DNS record with it
		managedHost = metadata.GetAnnotation(accessor, ANNOTATION_HCG_HOST)
		if managedHost == "" {
			return ReconcileStatusStop, ErrGeneratedHostMissing
		}
		metadata.AddAnnotation(existing, ANNOTATION_HCG_HOST, managedHost)
		// ok to remove as in TMC we add as status and in none TMC we add a host directly to the object
		metadata.RemoveAnnotation(accessor, ANNOTATION_HCG_HOST)
	}
	accessor.SetHCGHost(managedHost)
	targets, err := accessor.GetDNSTargets()
	if err != nil {
		return ReconcileStatusContinue, err
	}
	var activeLBHosts []string
	for _, target := range targets {
		host := target.Value
		if target.TargetType == dns.TargetTypeHost {
			//add the host to host watcher to keep our DNS upto date
			// If it is not an IP we add it to the host watcher that triggers an update when it gets IPS
			r.WatchHost(ctx, key, host)
			activeLBHosts = append(activeLBHosts, host)
		}
	}

	// clean up any watchers no longer needed TODO(cbrookes) we may want to put this in a defer or a different routine so it always cleans up
	hostRecordWatchers := r.ListHostWatchers(key)
	for _, watcher := range hostRecordWatchers {
		if !slice.ContainsString(activeLBHosts, watcher.Host) {
			r.ForgetHost(key, watcher.Host)
		}
	}

	copyDNS := existing.DeepCopy()
	err = r.setEndpointsFromTargets(ctx, accessor, targets, copyDNS)
	if err != nil {
		return ReconcileStatusContinue, err
	}

	objMeta, err := meta.Accessor(accessor)
	if err != nil {
		return ReconcileStatusContinue, err
	}
	copyHealthAnnotations(copyDNS, objMeta)
	if !equality.Semantic.DeepEqual(copyDNS, existing) {
		if existing.Spec.Endpoints == nil && copyDNS.Spec.Endpoints != nil {
			// metric to observe the accessor admission time
			IngressObjectTimeToAdmission.Observe(copyDNS.CreationTimestamp.Time.Sub(accessor.GetCreationTimestamp().Time).Seconds())
		}
		r.Log.V(3).Info("updating DNSRecord ", "record", copyDNS.Name, "endpoints for DNSRecord", "endpoints", copyDNS.Spec.Endpoints)
		if _, err = r.UpdateDNS(ctx, copyDNS); err != nil {
			return ReconcileStatusStop, err
		}
	}
	// Once we know the DNS is creatd up and TMC is enabled for this ingress (IE status is stored in annotations) set the DNS load balancer in the ingress status.
	if accessor.TMCEnabed() {
		accessor.SetDNSLBHost(managedHost)
	}

	return ReconcileStatusContinue, nil
}

func newDNSRecordForObject(obj runtime.Object) (*v1.DNSRecord, error) {
	objMeta, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	record := &v1.DNSRecord{}

	record.TypeMeta = metav1.TypeMeta{
		APIVersion: v1.SchemeGroupVersion.String(),
		Kind:       "DNSRecord",
	}
	objGroupVersion := schema.GroupVersion{Group: obj.GetObjectKind().GroupVersionKind().Group, Version: obj.GetObjectKind().GroupVersionKind().Version}
	// Sets the Ingress as the owner reference
	record.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         objGroupVersion.String(),
			Kind:               obj.GetObjectKind().GroupVersionKind().Kind,
			Name:               objMeta.GetName(),
			UID:                objMeta.GetUID(),
			Controller:         pointer.Bool(true),
			BlockOwnerDeletion: pointer.Bool(true),
		},
	})
	record.ObjectMeta = metav1.ObjectMeta{
		Annotations: map[string]string{
			logicalcluster.AnnotationKey: logicalcluster.From(objMeta).String(),
		},
		Name:      objMeta.GetName(),
		Namespace: objMeta.GetNamespace(),
	}
	if _, ok := record.Annotations[ANNOTATION_TRAFFIC_KEY]; !ok {
		if record.Annotations == nil {
			record.Annotations = map[string]string{}
		}
		record.Annotations[ANNOTATION_TRAFFIC_KEY] = string(objectKey(obj))
	}

	copyHealthAnnotations(record, objMeta)
	return record, nil

}

func copyHealthAnnotations(dnsRecord *v1.DNSRecord, objectMeta metav1.Object) {
	metadata.CopyAnnotationsPredicate(objectMeta, dnsRecord, metadata.KeyPredicate(func(key string) bool {
		return strings.HasPrefix(key, ANNOTATION_HEALTH_CHECK_PREFIX)
	}))
}

// GeoContinentIPs continent code -> array of ips
type GeoContinentIPs = map[string][]string

// GeoContinents array of codes
type GeoContinents = []string

func (r *DnsReconciler) appendTargetContinentIPs(ctx context.Context, target dns.Target, continentIPs GeoContinentIPs) (GeoContinentIPs, error) {

	host := target.Value
	ips := []string{}
	if target.TargetType == dns.TargetTypeIP {
		ips = append(ips, host)
	}
	if target.TargetType == dns.TargetTypeHost {
		// for a non ip value look up the DNS
		addr, err := r.DNSLookup(ctx, host)
		if err != nil {
			return continentIPs, fmt.Errorf("DNSLookup failed for host %s : %s", host, err)
		}
		for _, add := range addr {
			ips = append(ips, add.IP.String())
		}
	}

	if len(ips) > 0 {
		continentCode := target.TargetAttributes.Geo.ContinentCode
		if continentCode == "" {
			continentCode = dns.DEFAULT_GEO_ATTRIBUTE_CONTINENT_CODE
			r.Log.V(3).Info("no continent code found for target, using default", "continentCode", continentCode)
		}

		if continentIPs[continentCode] == nil {
			continentIPs[continentCode] = ips
		}
	}

	return continentIPs, nil
}

func (r *DnsReconciler) getContinentIPsFromTargets(ctx context.Context, accessor Interface, dnsTargets []dns.Target) (continentIPs GeoContinentIPs, continentCodes GeoContinents, err error) {
	continentIPs = GeoContinentIPs{}
	deletingContinentIPs := GeoContinentIPs{}
	for _, target := range dnsTargets {
		deleteAnnotation := workload.InternalClusterDeletionTimestampAnnotationPrefix + target.Cluster
		if metadata.HasAnnotation(accessor, deleteAnnotation) {
			deletingContinentIPs, _ = r.appendTargetContinentIPs(ctx, target, deletingContinentIPs)
			continue
		}
		continentIPs, err = r.appendTargetContinentIPs(ctx, target, continentIPs)
		if err != nil {
			return continentIPs, continentCodes, err
		}
	}

	// no non-deleting hosts have an IP yet, so continue using IPs of "losing" clusters
	if len(continentIPs) == 0 && len(deletingContinentIPs) > 0 {
		r.Log.V(3).Info("setting the dns Target to the deleting Target as no new dns targets set yet")
		continentIPs = deletingContinentIPs
	}

	continentCodes = make([]string, 0, len(continentIPs))
	for k := range continentIPs {
		continentCodes = append(continentCodes, k)
	}
	sort.Strings(continentCodes)

	return continentIPs, continentCodes, err
}

func (r *DnsReconciler) setEndpointsFromTargets(ctx context.Context, accessor Interface, dnsTargets []dns.Target, dnsRecord *v1.DNSRecord) error {
	r.Log.V(3).Info("setEndpointsFromTargets", "dnsTargets", dnsTargets)

	continentIPs, continentCodes, err := r.getContinentIPsFromTargets(ctx, accessor, dnsTargets)
	if err != nil {
		return err
	}
	r.Log.V(3).Info("setEndpointsFromTargets", "continentIPs", continentIPs, "continentCodes", continentCodes)

	currentEndpoints := make(map[string]*v1.Endpoint, len(dnsRecord.Spec.Endpoints))
	for _, endpoint := range dnsRecord.Spec.Endpoints {
		currentEndpoints[endpoint.SetID()] = endpoint
	}
	var (
		newEndpoints []*v1.Endpoint
		endpoint     *v1.Endpoint
	)
	dnsName := accessor.GetHCGHost()
	for continentCode, ips := range continentIPs {
		//Create Geo Location CNAME record for continent targeting continent specific host
		continentHost := GetContinentHost(dnsName, continentCode)
		endpoint = createOrUpdateEndpoint(dnsName, []string{continentHost}, v1.CNAMERecordType, continentHost, 60, currentEndpoints)
		endpoint.SetProviderSpecific(aws.ProviderSpecificGeolocationContinentCode, continentCode)
		newEndpoints = append(newEndpoints, endpoint)

		if continentCode == continentCodes[0] {
			//Create default Geo Location CNAME record pointing to the first known active continent targeting continent specific host
			endpoint = createOrUpdateEndpoint(dnsName, []string{continentHost}, v1.CNAMERecordType, "default", 60, currentEndpoints)
			endpoint.SetProviderSpecific(aws.ProviderSpecificGeolocationCountryCode, "*")
			newEndpoints = append(newEndpoints, endpoint)
		}

		for _, ip := range ips {
			//Create weighted A Record for each IP in this continent
			targetID := fmt.Sprintf("%s.%s", strings.ToLower(continentCode), ip)
			weight := awsEndpointWeight(len(ips))
			endpoint = createOrUpdateEndpoint(continentHost, []string{ip}, v1.ARecordType, targetID, 60, currentEndpoints)
			endpoint.ProviderSpecific = v1.ProviderSpecific{
				{
					Name:  aws.ProviderSpecificWeight,
					Value: weight,
				},
			}
			newEndpoints = append(newEndpoints, endpoint)
		}
	}

	sort.Slice(newEndpoints, func(i, j int) bool {
		return newEndpoints[i].Targets[0] < newEndpoints[j].Targets[0]
	})

	r.Log.V(3).Info("setEndpointsFromTargets", "newEndpoints", newEndpoints)
	dnsRecord.Spec.Endpoints = newEndpoints
	return nil
}

// GetContinentHost returns a continent specific host for the given host and continent code.
//
// i.e. host=x.y.com, continentCode=NA = x.na.y.com
//
// If the inputs are invalid the unmodified host is returned.
func GetContinentHost(host, continentCode string) string {
	p := strings.Split(host, ".")
	if len(p) <= 2 || continentCode == "" {
		return host
	}
	first := p[0]
	copy(p, p[1:])
	p = p[:len(p)-1]
	return fmt.Sprintf("%s.%s.%s", first, strings.ToLower(continentCode), strings.Join(p, "."))
}

func createOrUpdateEndpoint(dnsName string, targets v1.Targets, recordType v1.DNSRecordType, setIdentifier string,
	recordTTL v1.TTL, currentEndpoints map[string]*v1.Endpoint) (endpoint *v1.Endpoint) {
	ok := false
	if endpoint, ok = currentEndpoints[setIdentifier]; !ok {
		endpoint = &v1.Endpoint{
			SetIdentifier: setIdentifier,
		}
	}
	endpoint.DNSName = dnsName
	endpoint.RecordType = string(recordType)
	endpoint.Targets = targets
	endpoint.RecordTTL = recordTTL
	return endpoint
}

// awsEndpointWeight returns the weight Value for a single AWS record in a set of records where the traffic is split
// evenly between a number of clusters/ingresses, each splitting traffic evenly to a number of IPs (numIPs)
//
// Divides the number of IPs by a known weight allowance for a cluster/ingress, note that this means:
// * Will always return 1 after a certain number of ips is reached, 60 in the current case (maxWeight / 2)
// * Will return values that don't add up to the total maxWeight when the number of ingresses is not divisible by numIPs
//
// The aws weight value must be an integer between 0 and 255.
// https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-values-weighted.html#rrsets-values-weighted-weight
func awsEndpointWeight(numIPs int) string {
	maxWeight := 120
	if numIPs > maxWeight {
		numIPs = maxWeight
	}
	return strconv.Itoa(maxWeight / numIPs)
}

// AddHostAnnotations adds generated host annotation to a provided DNS Record CR
func AddHostAnnotations(record metav1.Object, host string) string {
	if !metadata.HasAnnotation(record, ANNOTATION_HCG_HOST) {
		// Let's assign it a global hostname if any
		generatedHost := fmt.Sprintf("%s.%s", xid.New(), host)
		metadata.AddAnnotation(record, ANNOTATION_HCG_HOST, generatedHost)
		//we need this host set and saved on the accessor before we go any further so force an update
		// if this is not saved we end up with a new host and the certificate can have the wrong host
		return generatedHost
	}
	return ""
}

func objectKey(obj runtime.Object) cache.ExplicitKey {
	key, _ := cache.MetaNamespaceKeyFunc(obj)
	return cache.ExplicitKey(key)
}
