package main

import (
	"context"
	"flag"
	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/releaser"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
)

func main() {
	var disableAutomaticAssociation bool
	flag.BoolVar(&disableAutomaticAssociation, "disable-automatic-association", false, "disable automatic PV association")

	var c controller.Controller
	run := func(
		ctx context.Context,
		stopCh <-chan struct{},
		config *rest.Config,
		client *clientset.Clientset,
		namespace string,
		controllerId string,
	) {
		c = releaser.New(ctx, client, namespace, controllerId, disableAutomaticAssociation)
		if err := c.Run(2, stopCh); err != nil {
			klog.Fatalf("Error running releaser: %s", err.Error())
		}
	}
	stop := func(config *rest.Config, client *clientset.Clientset) {
		if c != nil {
			c.Stop()
		}
	}
	controller.Main(run, stop)
}
