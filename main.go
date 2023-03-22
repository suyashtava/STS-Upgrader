package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"git.soma.salesforce.com/ajna/stsupgrader/pkg/controller"
	"git.soma.salesforce.com/ajna/stsupgrader/pkg/lease"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
)

const (
	updaterName        = "stsupgrader"
	defaultSynchPeriod = 10
)

func eventRecorder(client kubernetes.Interface, namespace string) record.EventRecorder {
	log.Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(log.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: client.CoreV1().Events(namespace)})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: updaterName})
	return recorder
}

// getKubernetesClient instantiates a Kubernetes client based on KUBECONFIG or in-cluster config.
func getKubernetesClient(kubeconfigFile string) (kubernetes.Interface, error) {
	// If kubeconfigFile is empty, try the KUBECONFIG environment variable.
	if kubeconfigFile == "" {
		f := os.Getenv("KUBECONFIG")
		if _, err := os.Stat(f); err == nil {
			// Only use if it exists
			kubeconfigFile = f
		}
	}

	// If kubeconfigFile is still empty, try the default path under <homedir>/.kube/config.
	if kubeconfigFile == "" {
		homeKubeconfigPath := path.Join(os.Getenv("HOME"), ".kube", "config")
		if _, err := os.Stat(homeKubeconfigPath); err == nil {
			kubeconfigFile = homeKubeconfigPath
		}
	}

	// Create the client config. This will try to use kubeconfigFile if non-empty, else it will fall back to
	// initializing from in-cluster config.
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigFile)
	if err != nil {
		log.Errorf("Failed to load k8s configuration. kubeconfigFile: %s. Error: %s", kubeconfigFile,
			err.Error())
		return nil, err
	}

	// generate the client based off of the config
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// main code path
func main() {
	kubeConfigPathPtr := flag.String("kubeconfig", "", "Path to kubeconfig")
	namespacePtr := flag.String("n", "default", "Namespace to operate on")
	debugPtr := flag.Bool("debug", true, "Set to true if debug logging should be enabled")
	synchTime := *flag.Int("synchperiod", defaultSynchPeriod,
		"Time period within which to re-synch the local cache of resources")
	enableLease := flag.Bool("enableLease", false, "Set to true if leasing should be enabled")

	flag.Parse()
	if *debugPtr {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	// Enable JSON logging
	log.SetFormatter(&log.JSONFormatter{})

	// get the Kubernetes client for connectivity
	client, err := getKubernetesClient(*kubeConfigPathPtr)
	if err != nil {
		log.Fatalf("Error running controller: %s", err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// lease manager
	log.Info("Creation of new LeaseManager")
	lm := lease.NewLeaseManager(log.New(), client.CoordinationV1(), []string{}, ctx, *enableLease)

	informerFactory := informers.NewSharedInformerFactoryWithOptions(client, time.Minute*time.Duration(synchTime),
		informers.WithNamespace(*namespacePtr))
	ssInformer := informerFactory.Apps().V1().StatefulSets()
	podInformer := informerFactory.Core().V1().Pods()
	controller := controller.NewStsController(*namespacePtr, client, ssInformer, podInformer,
		eventRecorder(client, *namespacePtr), lm)

	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	defer close(stopCh)

	informerFactory.Start(stopCh)
	if err := controller.Run(1, stopCh); err != nil {
		log.Fatalf("Error running controller: %s", err.Error())
	}

	// use a channel to handle OS signals to terminate and gracefully shut
	// down processing
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigTerm, syscall.SIGTERM)
	signal.Notify(sigTerm, syscall.SIGINT)
	<-sigTerm

	if lm.Enabled {
		// Attempt final clear of any remaining leases held by stsupgrader or by no one
		if err := lm.DeleteLease(updaterName, ctx); err != nil {
			fmt.Errorf("failed to clear acquired stsupgrader lease: %v", err)
		}
		if err := lm.DeleteLease("", ctx); err != nil {
			fmt.Errorf("failed to clear leases with no holder: %v", err)
		}
	}
}
