package ingress

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/rs/xid"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/api/networking/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	v1 "github.com/kuadrant/kcp-glbc/pkg/apis/kuadrant/v1"
)

const (
	clusterLabel = "kcp.dev/cluster"
	ownedByLabel = "kcp.dev/owned-by"

	hostGeneratedAnnotation = "kuadrant.dev/host.generated"

	manager                 = "kcp-ingress"
	cascadeCleanupFinalizer = "kcp.dev/cascade-cleanup"
)

func (c *Controller) reconcileRoot(ctx context.Context, ingress *networkingv1.Ingress) error {
	// is deleting
	if ingress.DeletionTimestamp != nil && !ingress.DeletionTimestamp.IsZero() {
		klog.Infof("deleting root ingress '%v'", ingress.Name)
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, ingress.Name))
		if err != nil {
			return err
		}
		currentLeaves, err := c.lister.List(sel)
		if err != nil {
			return err
		}
		klog.Infof("found %v leaf ingresses", len(currentLeaves))
		for _, leaf := range currentLeaves {
			err = c.kubeClient.Cluster(leaf.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Delete(ctx, leaf.Name, metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		}
		// delete DNSRecord
		err = c.dnsRecordClient.Cluster(ingress.ClusterName).KuadrantV1().DNSRecords(ingress.Namespace).Delete(ctx, ingress.Name, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		// all leaves removed, remove finalizer
		klog.Infof("'%v' ingress leaves cleaned up - removing finalizer", ingress.Name)
		removeFinalizer(ingress, cascadeCleanupFinalizer)

		c.hostsWatcher.StopWatching(ingressKey(ingress))
		for _, leaf := range currentLeaves {
			c.hostsWatcher.StopWatching(ingressKey(leaf))
		}

		return nil
	}

	AddFinalizer(ingress, cascadeCleanupFinalizer)
	if ingress.Annotations == nil || ingress.Annotations[hostGeneratedAnnotation] == "" {
		// Let's assign it a global hostname if any
		generatedHost := fmt.Sprintf("%s.%s", xid.New(), *c.domain)
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, hostGeneratedAnnotation, generatedHost)
		if err := c.patchIngress(ctx, ingress, []byte(patch)); err != nil {
			return err
		}
	}

	// Get the current leaves
	sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, ingress.Name))
	if err != nil {
		return err
	}
	currentLeaves, err := c.lister.List(sel)
	if err != nil {
		return err
	}

	// Generate the desired leaves
	desiredLeaves, err := c.desiredLeaves(ctx, ingress)
	if err != nil {
		return err
	}

	// Delete the leaves that are not desired anymore
	for _, leftover := range findNonDesiredLeaves(currentLeaves, desiredLeaves) {
		// The clean-up, i.e., removal of the DNS records, will be done in the next reconciliation cycle
		klog.Infof("Deleting non desired leaf %q", leftover.Name)
		if err := c.kubeClient.Cluster(leftover.ClusterName).NetworkingV1().Ingresses(leftover.Namespace).Delete(ctx, leftover.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
	}

	// TODO(jmprusi): ugly. fix. use indexer, etc.
	// Create and/or update the desired leaves
	for _, leaf := range desiredLeaves {
		if _, err := c.kubeClient.Cluster(leaf.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Create(ctx, leaf, metav1.CreateOptions{}); err != nil {
			if errors.IsAlreadyExists(err) {
				existingLeaf, err := c.kubeClient.Cluster(leaf.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Get(ctx, leaf.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				// Set the resourceVersion and UID to update the desired leaf.
				leaf.ResourceVersion = existingLeaf.ResourceVersion
				leaf.UID = existingLeaf.UID

				if _, err := c.kubeClient.Cluster(leaf.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Update(ctx, leaf, metav1.UpdateOptions{}); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	// Reconcile the DNSRecord for the root Ingress
	if len(ingress.Status.LoadBalancer.Ingress) > 0 {
		for _, lbs := range ingress.Status.LoadBalancer.Ingress {
			c.hostsWatcher.StartWatching(ctx, ingressKey(ingress), lbs.Hostname)
		}

		// The ingress has been admitted, let's expose the local load-balancing point to the global LB.
		record, err := c.getDNSRecord(ctx, ingress.Annotations[hostGeneratedAnnotation], ingress)
		if err != nil {
			return err
		}
		_, err = c.dnsRecordClient.Cluster(record.ClusterName).KuadrantV1().DNSRecords(record.Namespace).Create(ctx, record, metav1.CreateOptions{})
		if err != nil {
			if !errors.IsAlreadyExists(err) {
				return err
			}
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			_, err = c.dnsRecordClient.Cluster(record.ClusterName).KuadrantV1().DNSRecords(record.Namespace).Patch(ctx, record.Name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: manager, Force: pointer.Bool(true)})
			if err != nil {
				return err
			}
		}
	} else {
		err := c.dnsRecordClient.Cluster(ingress.ClusterName).KuadrantV1().DNSRecords(ingress.Namespace).Delete(ctx, ingress.Name, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

func (c *Controller) reconcileLeaf(ctx context.Context, rootName string, ingress *networkingv1.Ingress) error {
	// The leaf Ingress was updated, get others and aggregate status.
	sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, rootName))
	if err != nil {
		return err
	}
	others, err := c.lister.List(sel)
	if err != nil {
		return err
	}

	// Get the rootIngress based on the labels.
	var rootIngress *networkingv1.Ingress

	rootIf, exists, err := c.indexer.Get(&v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			ClusterName: ingress.ClusterName,
			Namespace:   ingress.Namespace,
			Name:        rootName,
		},
	})
	if err != nil {
		return err
	}

	if !exists {
		klog.Infof("deleting orphaned leaf ingress '%v' of missing root ingress '%v'", ingress.Name, rootName)
		return c.kubeClient.Cluster(ingress.ClusterName).NetworkingV1().Ingresses(ingress.Namespace).Delete(ctx, ingress.Name, metav1.DeleteOptions{})
	}

	if ingress.DeletionTimestamp != nil && !ingress.DeletionTimestamp.IsZero() {
		// The Ingress is being deleted. KCP doesn't currently cascade deletion to owned resources
		removeFinalizer(ingress, cascadeCleanupFinalizer)
	}

	// Clean the current status, and then recreate if from the other leafs.
	rootIngress = rootIf.(*networkingv1.Ingress).DeepCopy()
	rootIngress.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{}
	for _, o := range others {
		// Should the root Ingress status be updated only once the DNS record is successfully created / updated?
		rootIngress.Status.LoadBalancer.Ingress = append(rootIngress.Status.LoadBalancer.Ingress, o.Status.LoadBalancer.Ingress...)
	}

	// Update the root Ingress status with our desired LB.
	if _, err := c.kubeClient.Cluster(rootIngress.ClusterName).NetworkingV1().Ingresses(rootIngress.Namespace).UpdateStatus(ctx, rootIngress, metav1.UpdateOptions{}); err != nil {
		if errors.IsConflict(err) {
			key, err := cache.MetaNamespaceKeyFunc(ingress)
			if err != nil {
				return err
			}
			c.queue.AddRateLimited(key)
			return nil
		}
		return err
	}

	return nil
}

// TODO may want to move this to its own package in the future
func (c *Controller) getDNSRecord(ctx context.Context, hostname string, ingress *networkingv1.Ingress) (*v1.DNSRecord, error) {
	var targets []string
	for _, lbs := range ingress.Status.LoadBalancer.Ingress {
		if lbs.Hostname != "" {
			// TODO: once we are adding tests abstract to interface
			ips, err := c.hostResolver.LookupIPAddr(ctx, lbs.Hostname)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				targets = append(targets, ip.IP.String())
			}
		}
		if lbs.IP != "" {
			targets = append(targets, lbs.IP)
		}
	}

	record := &v1.DNSRecord{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.SchemeGroupVersion.String(),
			Kind:       "DNSRecord",
		},
		ObjectMeta: metav1.ObjectMeta{
			ClusterName: ingress.ClusterName,
			Namespace:   ingress.Namespace,
			Name:        ingress.Name,
		},
		Spec: v1.DNSRecordSpec{
			DNSName:    hostname,
			RecordType: "A",
			Targets:    targets,
			RecordTTL:  60,
		},
	}

	// Sets the Ingress as the owner reference
	record.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         networkingv1.SchemeGroupVersion.String(),
			Kind:               "Ingress",
			Name:               ingress.Name,
			UID:                ingress.UID,
			Controller:         pointer.Bool(true),
			BlockOwnerDeletion: pointer.Bool(true),
		},
	})

	return record, nil
}

func (c *Controller) desiredLeaves(ctx context.Context, root *networkingv1.Ingress) ([]*networkingv1.Ingress, error) {
	// This will parse the ingresses and extract all the destination services,
	// then create a new ingress leaf for each of them.
	services, err := c.getServices(ctx, root)
	if err != nil {
		return nil, err
	}

	var clusters []string
	for _, service := range services {
		if service.Labels[clusterLabel] != "" {
			clusters = append(clusters, service.Labels[clusterLabel])
		} else {
			klog.Infof("Skipping service %q because it is not assigned to any cluster", service.Name)
		}

		// Trigger reconciliation of the root ingress when this service changes.
		c.tracker.add(root, service)
	}

	desiredLeaves := make([]*networkingv1.Ingress, 0, len(clusters))
	for _, cl := range clusters {
		vd := root.DeepCopy()
		// TODO: munge cluster name
		vd.Name = fmt.Sprintf("%s--%s", root.Name, cl)

		vd.Labels = map[string]string{}
		vd.Labels[clusterLabel] = cl
		vd.Labels[ownedByLabel] = root.Name

		// Cleanup finalizers
		vd.Finalizers = []string{}
		// Cleanup owner references
		vd.OwnerReferences = []metav1.OwnerReference{}
		vd.SetResourceVersion("")

		if hostname, ok := root.Annotations[hostGeneratedAnnotation]; ok {
			// Duplicate the existing rules for the global hostname
			globalRules := make([]networkingv1.IngressRule, len(vd.Spec.Rules))
			for i, rule := range vd.Spec.Rules {
				r := rule.DeepCopy()
				r.Host = hostname
				globalRules[i] = *r
			}
			vd.Spec.Rules = append(vd.Spec.Rules, globalRules...)
		}

		desiredLeaves = append(desiredLeaves, vd)
	}

	return desiredLeaves, nil
}

func hashString(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return fmt.Sprint(h.Sum32())
}

func generateStatusHost(domain *string, ingress *networkingv1.Ingress) string {
	// TODO(jmprusi): using "contains" is a bad idea as it could be abused by crafting a malicious hostname, but for a PoC it should be good enough?
	allRulesAreDomain := true
	for _, rule := range ingress.Spec.Rules {
		if !strings.Contains(rule.Host, *domain) {
			allRulesAreDomain = false
			break
		}
	}

	// TODO(jmprusi): Hardcoded to the first one...
	if allRulesAreDomain {
		return ingress.Spec.Rules[0].Host
	}

	return hashString(ingress.Name+ingress.Namespace+ingress.ClusterName) + "." + *domain
}

// getServices will parse the ingress object and return a list of the services.
func (c *Controller) getServices(ctx context.Context, ingress *networkingv1.Ingress) ([]*corev1.Service, error) {
	var services []*corev1.Service
	for _, rule := range ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			svc, err := c.kubeClient.Cluster(ingress.ClusterName).CoreV1().Services(ingress.Namespace).Get(ctx, path.Backend.Service.Name, metav1.GetOptions{})
			// TODO(jmprusi): If one of the services doesn't exist, we invalidate all the other ones.. review this.
			if err != nil {
				return nil, err
			}
			services = append(services, svc)
		}
	}
	return services, nil
}

func (c *Controller) patchIngress(ctx context.Context, ingress *networkingv1.Ingress, data []byte) error {
	i, err := c.kubeClient.Cluster(ingress.ClusterName).NetworkingV1().Ingresses(ingress.Namespace).
		Patch(ctx, ingress.Name, types.MergePatchType, data, metav1.PatchOptions{FieldManager: manager})
	if err != nil {
		return err
	}
	ingress = i
	return nil
}

func findNonDesiredLeaves(current, desired []*networkingv1.Ingress) []*networkingv1.Ingress {
	var missing []*networkingv1.Ingress

	for _, c := range current {
		found := false
		for _, d := range desired {
			if c.Name == d.Name {
				found = true
			}
		}
		if !found {
			missing = append(missing, c)
		}
	}

	return missing
}

func getRootName(ingress *networkingv1.Ingress) (rootName string, isLeaf bool) {
	if ingress.Labels != nil {
		rootName, isLeaf = ingress.Labels[ownedByLabel]
	}

	return
}

func AddFinalizer(ingress *networkingv1.Ingress, finalizer string) {
	for _, v := range ingress.Finalizers {
		if v == finalizer {
			return
		}
	}
	ingress.Finalizers = append(ingress.Finalizers, finalizer)
}

func removeFinalizer(ingress *networkingv1.Ingress, finalizer string) {
	for i, v := range ingress.Finalizers {
		if v == finalizer {
			ingress.Finalizers[i] = ingress.Finalizers[len(ingress.Finalizers)-1]
			ingress.Finalizers = ingress.Finalizers[:len(ingress.Finalizers)-1]
			return
		}
	}
}

func ingressKey(ingress *networkingv1.Ingress) interface{} {
	key, _ := cache.MetaNamespaceKeyFunc(ingress)
	return cache.ExplicitKey(key)
}
