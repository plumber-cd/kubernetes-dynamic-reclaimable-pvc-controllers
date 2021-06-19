// Package controller is a general setup routine.
// It will establish k8s client and elect a leader.
// When the leader is elected - it will return control back to the loop.
package controller

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"

	"github.com/google/uuid"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// 6 - fine level tracing verbose repetitive messages
	// 5 - verbose repetitive messages (skipped resources)
	// 4 - verbose repetitive messages (managed resources)
	// 3 - reserved
	// 2 - fine level tracing
	// 1 - debug
	klog "k8s.io/klog/v2"

	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/leader"
	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/signals"
)

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		klog.V(2).Infof("Using kubeconfig %s", kubeconfig)
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		klog.V(2).Infof("Using KUBECONFIG=%s", kubeconfig)
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	if k8sPort := os.Getenv("KUBERNETES_PORT"); k8sPort != "" {
		klog.V(2).Info("Using in cluster authentication")
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	usr, err := user.Current()
	if err != nil {
		return nil, err
	}
	if usr.HomeDir == "" {
		return nil, errors.New("home directory unknown")
	}
	kubeconfig = fmt.Sprintf("%s/.kube/config", usr.HomeDir)
	klog.V(2).Infof("Using home kubeconfig %s", kubeconfig)
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func Main(
	run func(
		ctx context.Context,
		stopCh <-chan struct{},
		config *rest.Config,
		client *clientset.Clientset,
		namespace string,
		controllerId string,
	),
	stop func(
		config *rest.Config,
		client *clientset.Clientset,
	),
) {
	klog.InitFlags(nil)
	defer klog.Flush()

	var kubeconfig string
	var namespace string
	var leaseLockName string
	var leaseLockNamespace string
	var id string
	var controllerId string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&namespace, "namespace", "", "limit to a specific namespace")
	flag.StringVar(&id, "id", uuid.New().String(), "the holder identity name")
	flag.StringVar(&controllerId, "controller-id", uuid.New().String(), "the holder identity name")
	flag.StringVar(&leaseLockName, "lease-lock-name", "", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", "", "the lease lock resource namespace")
	flag.Parse()

	klog.V(2).Info(flag.Args())

	if leaseLockName == "" {
		leaseLockName = controllerId
	}
	if leaseLockNamespace == "" {
		leaseLockNamespace = namespace
	}

	config, err := buildConfig(kubeconfig)
	if err != nil {
		klog.Fatal(err)
	}
	client := clientset.NewForConfigOrDie(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopCh := signals.SetupSignalHandler()
	go func() {
		<-stopCh
		klog.Info("Received termination, signaling shutdown")
		cancel()
	}()

	leader.Elect(&leader.Config{
		LeaseLockName:      leaseLockName,
		LeaseLockNamespace: leaseLockNamespace,
		ID:                 id,
		Config:             config,
		Client:             client,
		Namespace:          namespace,
		ControllerId:       controllerId,
		Ctx:                ctx,
		StopCh:             stopCh,
		Run:                run,
		Stop:               stop,
		Cancel:             cancel,
	})
}
