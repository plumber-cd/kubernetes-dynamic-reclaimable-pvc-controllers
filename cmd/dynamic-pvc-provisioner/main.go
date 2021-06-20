package main

import (
	"context"
	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/provisioner"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
)

func main() {
	var c controller.Controller
	run := func(
		ctx context.Context,
		stopCh <-chan struct{},
		config *rest.Config,
		client *clientset.Clientset,
		namespace string,
		controllerId string,
	) {
		c = provisioner.New(ctx, client, namespace, controllerId)
		if err := c.Run(2, stopCh); err != nil {
			klog.Fatalf("Error running provisioner: %s", err.Error())
		}
	}
	stop := func(config *rest.Config, client *clientset.Clientset) {
		if c != nil {
			c.Stop()
		}
	}
	controller.Main(run, stop)
}
