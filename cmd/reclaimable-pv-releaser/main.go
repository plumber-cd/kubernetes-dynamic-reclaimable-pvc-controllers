package main

import (
	"context"
	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	"time"
)

func main() {
	run := func(
		ctx context.Context,
		stopCh <-chan struct{},
		config *rest.Config,
		client *clientset.Clientset,
		namespace string,
		controllerId string,
	) {
		klog.Info("Releaser starting...")

		select {}
	}
	stop := func(config *rest.Config, client *clientset.Clientset) {
		klog.Info("Releaser stopping...")
		time.Sleep(60 * time.Second)
	}
	controller.Main(run, stop)
}
