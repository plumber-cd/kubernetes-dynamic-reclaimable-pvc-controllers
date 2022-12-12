// Package controller is a general setup routine.
// It will establish k8s client and elect a leader.
// Then it will give a chance to the caller to configure queues and run the control loop.
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

	"time"

	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/leader"
	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/signals"

	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

var Version string

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
	var controllerId string
	var namespace string
	var leaseLockId string
	var leaseLockName string
	var leaseLockNamespace string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional, absolute path to the kubeconfig file")
	flag.StringVar(&controllerId, "controller-id", "", "this controller identity name - use the same string for both provisioner and releaser")
	flag.StringVar(&namespace, "namespace", "", "limit to a specific namespace - only for provisioner")
	flag.StringVar(&leaseLockId, "lease-lock-id", uuid.New().String(), "optional, the lease lock holder identity name")
	flag.StringVar(&leaseLockName, "lease-lock-name", "", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", "", "optional, the lease lock resource namespace; default to -namespace")
	flag.Parse()

	flag.Visit(func(f *flag.Flag) {
		klog.V(2).Infof("-%s=%s", f.Name, f.Value)
	})
	klog.V(2).Infof("Args: %s", flag.Args())

	if len(flag.Args()) == 1 && flag.Args()[0] == "version" {
		fmt.Println(Version)
		os.Exit(0)
	}

	if controllerId == "" {
		klog.Fatal("unable to get controller id (missing controller-id flag).")
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

	stopElectCh := make(chan struct{})
	stopCh := signals.SetupSignalHandler()
	go func() {
		<-stopCh
		klog.Info("Received termination, signaling shutdown")
		close(stopElectCh)
		cancel()
	}()

	leader.Elect(&leader.Config{
		LeaseLockName:      leaseLockName,
		LeaseLockNamespace: leaseLockNamespace,
		LeaseLockId:        leaseLockId,
		Config:             config,
		Client:             client,
		Namespace:          namespace,
		ControllerId:       controllerId,
		Ctx:                ctx,
		StopCh:             stopElectCh,
		Run:                run,
		Stop:               stop,
		Cancel:             cancel,
	})
}

type Controller interface {
	Run(threadiness int, stopCh <-chan struct{}) error
	Stop()
	Enqueue(queue workqueue.RateLimitingInterface, obj interface{})
	RunWorker(
		name string,
		queue workqueue.RateLimitingInterface,
		handler func(namespace, name string) error,
	) func()
	ProcessNextWorkItem(
		name string,
		queue workqueue.RateLimitingInterface,
		handler func(namespace, name string) error,
	) bool
}

type BasicController struct {
	Ctx                 context.Context
	ControllerName      string
	ControllerId        string
	KubeClientSet       kubernetes.Interface
	Namespace           string
	KubeInformerFactory kubeinformers.SharedInformerFactory
	Recorder            record.EventRecorder
}

func New(
	ctx context.Context,
	kubeClientSet kubernetes.Interface,
	namespace,
	controllerName,
	controllerId string,
) *BasicController {
	klog.V(2).Info("Creating kube informer")
	kubeInformerOptions := make([]kubeinformers.SharedInformerOption, 0)
	if namespace != "" {
		klog.V(2).Infof("WithNamespace=%s", namespace)
		kubeInformerOptions = append(kubeInformerOptions, kubeinformers.WithNamespace(namespace))
	}
	kubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		kubeClientSet,
		time.Second*30,
		kubeInformerOptions...,
	)

	klog.V(2).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClientSet.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerName})

	controller := &BasicController{
		Ctx:                 ctx,
		ControllerName:      controllerName,
		ControllerId:        controllerId,
		KubeClientSet:       kubeClientSet,
		Namespace:           namespace,
		KubeInformerFactory: kubeInformerFactory,
		Recorder:            recorder,
	}

	return controller
}

func (c *BasicController) Run(
	threadiness int,
	stopCh <-chan struct{},
	setup func(threadiness int, stopCh <-chan struct{}) error,
	shutdown func(),
) error {
	defer utilruntime.HandleCrash()
	defer shutdown()

	klog.Infof("Starting %s controller", c.ControllerName)

	c.KubeInformerFactory.Start(stopCh)

	err := setup(threadiness, stopCh)
	if err != nil {
		return err
	}

	klog.Infof("Started %s controller", c.ControllerName)
	<-stopCh
	klog.V(2).Info("Shutting down workers")

	return nil
}

func (c *BasicController) Stop() {
	// TODO: Any cleanup logic signaling to not to perform any write operations?
	klog.Info("Controller stopped")
}

func (c *BasicController) Enqueue(queue workqueue.RateLimitingInterface, obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	queue.AddRateLimited(key)
}

func (c *BasicController) Requeue(queue workqueue.RateLimitingInterface, old interface{}, new interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(old); err != nil {
		utilruntime.HandleError(err)
		return
	}
	queue.Forget(key)
	queue.Done(key)
	c.Enqueue(queue, new)
}

func (c *BasicController) Forget(queue workqueue.RateLimitingInterface, obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	queue.Forget(key)
}

func (c *BasicController) RunWorker(
	name string,
	queue workqueue.RateLimitingInterface,
	handler func(namespace, name string) error,
) func() {
	return func() {
		for c.ProcessNextWorkItem(name, queue, handler) {
		}
	}
}

func (c *BasicController) ProcessNextWorkItem(
	name string,
	queue workqueue.RateLimitingInterface,
	handler func(namespace, name string) error,
) bool {
	obj, shutdown := queue.Get()

	if shutdown {
		klog.V(6).Infof("Object %v quit", obj)
		return false
	}

	err := func(obj interface{}) error {
		finalFunc := func() {
			queue.Done(obj)
		}
		defer func() {
			finalFunc()
		}()

		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			queue.Forget(key)
			utilruntime.HandleError(fmt.Errorf("expected string in the queue but got %v", obj))
			return nil
		}
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			queue.Forget(key)
			utilruntime.HandleError(fmt.Errorf("invalid resource %s key: %s", name, key))
			return nil
		}
		if err = handler(namespace, name); err != nil {
			if err == context.Canceled {
				klog.V(6).Info(err)
				return nil
			}
			queue.Forget(key)
			finalFunc = func() {
				queue.AddRateLimited(key) // make sure it is requeuing after the previous item was Done
			}
			return fmt.Errorf("error syncing %s '%s': %s, requeuing", name, key, err.Error())
		}
		queue.Forget(key)
		klog.V(5).Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}
