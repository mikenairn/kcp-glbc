package main

import (
	"flag"
	"time"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	envoyserver "knative.dev/net-kourier/pkg/envoy/server"

	kuadrantv1 "github.com/kuadrant/kcp-glbc/pkg/client/kuadrant/clientset/versioned"
	"github.com/kuadrant/kcp-glbc/pkg/client/kuadrant/informers/externalversions"
	"github.com/kuadrant/kcp-glbc/pkg/reconciler/dns"
	"github.com/kuadrant/kcp-glbc/pkg/reconciler/ingress"
)

const (
	numThreads   = 2
	resyncPeriod = 10 * time.Hour
)

var kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig")
var kubecontext = flag.String("context", "", "Context to use in the Kubeconfig file, instead of the current context")

var domain = flag.String("domain", "hcpapps.net", "The domain to use to expose ingresses")
var customHosts = flag.Bool("custom-hosts", true, "Flag to enable hosts to be custom")

var dnsProvider = flag.String("dns-provider", "aws", "The DNS provider being used [aws, fake]")

var envoyEnableXDS = flag.Bool("envoyxds", false, "Start an Envoy control plane")
var envoyXDSPort = flag.Uint("envoyxds-port", 18000, "Envoy control plane port")
var envoyListenPort = flag.Uint("envoy-listener-port", 80, "Envoy default listener port")

func main() {
	flag.Parse()

	var overrides clientcmd.ConfigOverrides
	if *kubecontext != "" {
		overrides.CurrentContext = *kubecontext
	}

	r, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig},
		&overrides).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}

	ctx := genericapiserver.SetupSignalContext()

	kubeClient, err := kubernetes.NewClusterForConfig(r)
	if err != nil {
		klog.Fatal(err)
	}
	kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient.Cluster("*"), resyncPeriod)

	dnsRecordClient, err := kuadrantv1.NewClusterForConfig(r)
	if err != nil {
		klog.Fatal(err)
	}
	kuadrantInformerFactory := externalversions.NewSharedInformerFactory(dnsRecordClient.Cluster("*"), resyncPeriod)

	controllerConfig := &ingress.ControllerConfig{
		KubeClient:            kubeClient,
		DnsRecordClient:       dnsRecordClient,
		SharedInformerFactory: kubeInformerFactory,
		Domain:                domain,
	}
	if *envoyEnableXDS {
		controllerConfig.EnvoyXDS = envoyserver.NewXdsServer(*envoyXDSPort, nil)
		controllerConfig.EnvoyListenPort = envoyListenPort
	}
	ingressController := ingress.NewController(controllerConfig)

	dnsRecordController, err := dns.NewController(&dns.ControllerConfig{
		DnsRecordClient:       dnsRecordClient,
		SharedInformerFactory: kuadrantInformerFactory,
		DNSProvider:           dnsProvider,
	})
	if err != nil {
		klog.Fatal(err)
	}

	kubeInformerFactory.Start(ctx.Done())
	kubeInformerFactory.WaitForCacheSync(ctx.Done())

	kuadrantInformerFactory.Start(ctx.Done())
	kuadrantInformerFactory.WaitForCacheSync(ctx.Done())

	go func() {
		ingressController.Start(ctx, numThreads)
	}()

	go func() {
		dnsRecordController.Start(ctx, numThreads)
	}()

	<-ctx.Done()
}
