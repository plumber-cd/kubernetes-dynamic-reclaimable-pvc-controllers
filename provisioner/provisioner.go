// Package provisioner is a dynamic PVC provisioner.
package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	AgentName = "dynamic-pvc-provisioner"

	AnnotationBaseName   = AgentName + ".kubernetes.io"
	AnnotationEnabledKey = "enabled"
	AnnotationPVCKey     = "pvc"

	LabelBaseName     = AnnotationBaseName
	LabelManagedByKey = "managed-by"

	SuccessSynced    = "Synced"
	MessagePodSynced = "Pod synced successfully"

	MessageMissingPVC = "'%s' missing PVC"
	ErrMissingPVC     = "ErrMissingPVC"

	MessageInvalidPVC = "'%s' invalid PVC: %s"
	ErrInvalidPVC     = "ErrInvalidPVC"

	MessageMissingVolume = "Pod was missing volume '%s'"
	ErrMissingVolume     = "ErrMissingVolume"
)

type Provisioner struct {
	ctx context.Context

	kubeClientSet kubernetes.Interface

	controllerId string

	kubeInformerFactory kubeinformers.SharedInformerFactory

	podsLister corelisters.PodLister
	podsSynced cache.InformerSynced

	podsQueue workqueue.RateLimitingInterface

	recorder record.EventRecorder
}

func New(ctx context.Context, kubeClientSet kubernetes.Interface, namespace, controllerId string) *Provisioner {
	klog.Info("Provisioner starting...")

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
	podsInformer := kubeInformerFactory.Core().V1().Pods()

	klog.V(2).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClientSet.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: AgentName})

	provisioner := &Provisioner{
		ctx:                 ctx,
		kubeClientSet:       kubeClientSet,
		controllerId:        controllerId,
		kubeInformerFactory: kubeInformerFactory,
		podsLister:          podsInformer.Lister(),
		podsSynced:          podsInformer.Informer().HasSynced,
		podsQueue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Pods"),
		recorder:            recorder,
	}

	klog.V(2).Info("Setting up event handlers")
	podsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			provisioner.enqueue(provisioner.podsQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			provisioner.enqueue(provisioner.podsQueue, new)
		},
		// DeleteFunc: provisioner.enqueue,
	})

	return provisioner
}

func (p *Provisioner) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer p.podsQueue.ShutDown()

	klog.Infof("Starting %s controller", AgentName)

	p.kubeInformerFactory.Start(stopCh)

	klog.V(2).Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, p.podsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.V(2).Info("Starting workers")
	for i := 0; i < threadiness; i++ {
		go wait.Until(p.runPodWorker, time.Second, stopCh)
	}

	klog.Infof("Started %s controller", AgentName)
	<-stopCh
	klog.V(2).Info("Shutting down workers")

	return nil
}

func (p *Provisioner) Stop() {
	// TODO: Any cleanup logic signaling to not to perform any write operations?
	klog.Info("Provisioner stopped")
}

func (p *Provisioner) enqueue(queue workqueue.RateLimitingInterface, obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	queue.Add(key)
}

func (p *Provisioner) runPodWorker() {
	for p.processNextWorkItem("pod", p.podsQueue, p.podSyncHandler) {
	}
}

func (p *Provisioner) processNextWorkItem(
	name string,
	queue workqueue.RateLimitingInterface,
	handler func(namespace, name string) error,
) bool {
	obj, shutdown := queue.Get()

	if shutdown {
		klog.V(5).Infof("Object %#v quit", obj)
		return false
	}

	err := func(obj interface{}) error {
		defer queue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			queue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in the queue but got %#v", obj))
			return nil
		}
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			queue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("invalid resource %s key: %s", name, key))
			return nil
		}
		if err = handler(namespace, name); err != nil {
			queue.AddRateLimited(key)
			return fmt.Errorf("error syncing %s '%s': %s, requeuing", name, key, err.Error())
		}
		queue.Forget(obj)
		klog.V(5).Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (p *Provisioner) podSyncHandler(namespace, name string) error {
	pod, err := p.podsLister.Pods(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("pod '%s/%s' in work queue no longer exists", namespace, name))
			return nil
		}

		return err
	}

	annotations := pod.ObjectMeta.Annotations
	volumes := pod.Spec.Volumes

	// key: volumeName, value: claimName or "" if not found on the pod
	requestedVolumes := map[string]string{}
	for key, value := range annotations {
		keyParts := strings.Split(key, "/")
		klog.V(6).Info(fmt.Sprintf("keyParts: %s", keyParts))
		if len(keyParts) != 2 || keyParts[0] != AnnotationBaseName {
			continue
		}
		keySubParts := strings.Split(keyParts[1], ".")
		klog.V(6).Info(fmt.Sprintf("keySubParts: %s", keySubParts))
		if len(keySubParts) != 2 || keySubParts[1] != AnnotationEnabledKey {
			continue
		}
		requestedVolumeName := keySubParts[0]

		enabled, err := strconv.ParseBool(value)
		if err != nil || !enabled {
			klog.V(5).Info(fmt.Sprintf("'%s: %v', skip", key, value))
			continue
		}

		pvcKey := fmt.Sprintf("%s/%s.%s", AnnotationBaseName, requestedVolumeName, AnnotationPVCKey)
		if _, ok := annotations[pvcKey]; !ok {
			p.recorder.Event(pod, corev1.EventTypeWarning, ErrMissingPVC, fmt.Sprintf(MessageMissingPVC, pvcKey))
			continue
		}
		requestedVolumes[requestedVolumeName] = ""
	}

	if len(requestedVolumes) <= 0 {
		klog.V(5).Info(fmt.Sprintf("pod '%s/%s' did not requested any volumes, skip", namespace, name))
		return nil
	}

	for _, volume := range volumes {
		_, ok := requestedVolumes[volume.Name]
		if !ok {
			klog.V(5).Info(fmt.Sprintf("pod '%s/%s' volume '%s' is not one of the requested, skip", namespace, name, volume.Name))
			continue
		}

		pvc := volume.VolumeSource.PersistentVolumeClaim
		if pvc == nil {
			p.recorder.Event(pod, corev1.EventTypeWarning, ErrInvalidPVC, fmt.Sprintf(MessageInvalidPVC, volume.Name, "consumer volume for requested pvc wasn't a persistentVolumeClaim type"))
			continue
		}

		requestedVolumes[volume.Name] = volume.VolumeSource.PersistentVolumeClaim.ClaimName
		klog.V(4).Info(fmt.Sprintf("matched volume=%s to pvc=%s", volume.Name, requestedVolumes[volume.Name]))
	}

	updated := false
	for requestedVolume, claimName := range requestedVolumes {
		if claimName == "" {
			p.recorder.Event(pod, corev1.EventTypeWarning, ErrMissingVolume, fmt.Sprintf(MessageMissingVolume, requestedVolume))
			continue
		}

		pvcYaml := annotations[fmt.Sprintf("%s/%s.%s", AnnotationBaseName, requestedVolume, AnnotationPVCKey)]
		decode := scheme.Codecs.UniversalDeserializer().Decode
		obj, _, err := decode([]byte(pvcYaml), nil, nil)
		if err != nil {
			p.recorder.Event(pod, corev1.EventTypeWarning, ErrInvalidPVC, fmt.Sprintf(MessageInvalidPVC, requestedVolume, err))
			continue
		}
		typeOk := func() bool {
			switch t := obj.(type) {
			case *corev1.PersistentVolumeClaim:
				return true
			default:
				p.recorder.Event(pod, corev1.EventTypeWarning, ErrInvalidPVC, fmt.Sprintf(MessageInvalidPVC, requestedVolume, fmt.Sprintf("expected pvc, got: %s", t)))
				return false
			}
		}()
		if !typeOk {
			continue
		}
		pvc := obj.(*corev1.PersistentVolumeClaim)

		pvc.ObjectMeta.Name = claimName
		pvc.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(pod, corev1.SchemeGroupVersion.WithKind("Pod")),
		}
		if pvc.ObjectMeta.Labels == nil {
			pvc.ObjectMeta.Labels = map[string]string{}
		}
		pvc.ObjectMeta.Labels[fmt.Sprintf("%s/%s", LabelBaseName, LabelManagedByKey)] = p.controllerId
		_, err = p.kubeClientSet.CoreV1().PersistentVolumeClaims(pod.ObjectMeta.Namespace).
			Create(p.ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				continue
			}

			return err
		}
		updated = true
	}

	if updated {
		p.recorder.Event(pod, corev1.EventTypeNormal, SuccessSynced, MessagePodSynced)
	}
	return nil
}
