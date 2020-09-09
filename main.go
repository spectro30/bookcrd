package main

import (
	"flag"

	"github.com/appscode/go/signals"
	clientset "github.com/spectro30/bookcrd/client/clientset/versioned"
	clusterInformer "github.com/spectro30/bookcrd/client/informers/externalversions"

	"time"

	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

var (
	kubeconfig string
	masterUrl  string
)

func main() {
	flag.Parse()
	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	//get the kubeconfig
	config, err := clientcmd.BuildConfigFromFlags(masterUrl, kubeconfig)
	if err != nil {
		klog.Fatalf("Build config error. the error details : ", err.Error())
	}
	k8sClientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("error in getting k8sClient , error details : ", err.Error())
	}
	clusterClientSet, err := clientset.NewForConfig(config)
	if err != nil {
		klog.Fatalf("error in getting clientset , error details : ", err.Error())
	}
	kubeInformerFactory := k8sInformers.NewSharedInformerFactory(k8sClientset, time.Minute*1)

	clusterInformerFactory := clusterInformer.NewSharedInformerFactory(clusterClientSet, time.Minute*1)

	kubeInformerFactory.Start(stopCh)
	clusterInformerFactory.Start(stopCh)

	klog.Info("sdkfjllkxcjvkl")
	controller := newController(k8sClientset, clusterClientSet,
		kubeInformerFactory.Apps().V1().Deployments(),
		kubeInformerFactory.Core().V1().Pods(),
		clusterInformerFactory.Cluster().V1alpha1().Clusters())
	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(stopCh)
	clusterInformerFactory.Start(stopCh)

	if err = controller.Run(2, stopCh); err != nil {
		klog.Fatalf("Error running controller: %s", err.Error())
	}
}

func init() {

	flag.StringVar(&kubeconfig, "kubeconfig", "", "set the kubeconfig file path via flag command line")
	flag.StringVar(&masterUrl, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
}