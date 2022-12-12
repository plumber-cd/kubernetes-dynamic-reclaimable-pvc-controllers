// Package releaser is an automatic PV reclaimer.
package releaser

import (
	"context"
	"fmt"
	"sync"
	"time"

	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	AgentName = "reclaimable-pv-releaser"

	AnnotationBaseName        = AgentName + ".kubernetes.io"
	AnnotationControllerIdKey = "controller-id"
	AnnotationControllerId    = AnnotationBaseName + "/" + AnnotationControllerIdKey

	Released          = "Released"
	MessagePVReleased = "PV released successfully"

	MessageReleasePV = "error releasing PV %s: %s"
	ErrReleasePV     = "ErrReleasePV"
)

type Releaser struct {
	controller.BasicController

	SCLister storagelisters.StorageClassLister

	PVLister corelisters.PersistentVolumeLister
	PVSynced cache.InformerSynced
	PVQueue  workqueue.RateLimitingInterface

	managedSCMutex *sync.Mutex
	managedSCSet   map[string]struct{}
}

func New(
	ctx context.Context,
	kubeClientSet kubernetes.Interface,
	namespace,
	controllerId string,
) controller.Controller {
	klog.Info("Releaser starting...")

	if namespace != "" {
		klog.Warningf("Releaser can't run within a namespace as PVs are not namespaced resources - ignoring -namespace=%s and acting in the scope of the cluster", namespace)
	}

	c := controller.New(ctx, kubeClientSet, "", AgentName, controllerId)

	scInformer := c.KubeInformerFactory.Storage().V1().StorageClasses()
	pvInformer := c.KubeInformerFactory.Core().V1().PersistentVolumes()

	r := &Releaser{
		BasicController: *c,

		SCLister: scInformer.Lister(),

		PVLister: pvInformer.Lister(),
		PVSynced: pvInformer.Informer().HasSynced,
		PVQueue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "PersistentVolumes"),

		managedSCMutex: &sync.Mutex{},
		managedSCSet:   make(map[string]struct{}),
	}

	klog.V(2).Info("Setting up event handlers")

	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.Enqueue(r.PVQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			r.Requeue(r.PVQueue, old, new)
		},
		DeleteFunc: func(obj interface{}) {
			r.Dequeue(r.PVQueue, obj)
		},
	})

	return r
}

func (r *Releaser) Run(threadiness int, stopCh <-chan struct{}) error {
	return r.BasicController.Run(
		threadiness,
		stopCh,
		func(threadiness int, stopCh <-chan struct{}) error {
			klog.V(2).Info("Waiting for informer caches to sync")

			if ok := cache.WaitForCacheSync(stopCh, r.PVSynced); !ok {
				return fmt.Errorf("failed to wait for PV caches to sync")
			}

			klog.V(2).Info("Starting workers")
			for i := 0; i < threadiness; i++ {
				go wait.Until(
					r.RunWorker("pv", r.PVQueue, r.pvSyncHandler),
					time.Second,
					stopCh,
				)
			}

			return nil
		},
		func() {
			if r.PVQueue != nil {
				r.PVQueue.ShutDown()
			}
		},
	)
}

func (r *Releaser) Stop() {
	r.BasicController.Stop()
	// TODO: Any cleanup logic signaling to not to perform any write operations?
	klog.Info("Releaser stopped")
}

func (r *Releaser) pvSyncHandler(_, name string) error {
	pv, err := r.PVLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("pv '%s' in work queue no longer exists", name),
			)
			return nil
		}

		return err
	}

	sc, err := r.SCLister.Get(pv.Spec.StorageClassName)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("sc '%s' for pv '%s' in work queue didn't exist", pv.Spec.StorageClassName, name),
			)
			return nil
		}

		return err
	}

	manager, ok := sc.ObjectMeta.Annotations[AnnotationControllerId]
	if ok && manager == r.ControllerId {
		return r.pvReleaseHandler(pv)
	} else {
		klog.V(5).Infof("SC %q for PV %q is not associated with this controller ID %q, skip", pv.Spec.StorageClassName, pv.ObjectMeta.Name, r.ControllerId)
	}

	return nil
}

func (r *Releaser) pvReleaseHandler(pv *corev1.PersistentVolume) error {
	if pv.Status.Phase == corev1.VolumeAvailable {
		klog.V(6).Infof("PV %s is already '%s' - moving on", pv.ObjectMeta.Name, pv.Status.Phase)
		return nil
	}
	if pv.Status.Phase != corev1.VolumeReleased {
		klog.V(4).Infof("PV %s is '%s', can't make it '%s'", pv.ObjectMeta.Name, pv.Status.Phase, corev1.VolumeAvailable)
		return nil
	}
	if pv.Spec.ClaimRef == nil {
		klog.V(4).Infof("PV %s already had nil as claimRef - back off", pv.ObjectMeta.Name)
		return nil
	}

	pvCopy := pv.DeepCopy()
	pvCopy.Spec.ClaimRef = nil
	_, err := r.KubeClientSet.CoreV1().PersistentVolumes().Update(r.Ctx, pvCopy, metav1.UpdateOptions{})
	if err != nil {
		if errors.IsConflict(err) {
			klog.V(4).Infof("PV %s had a conflict - ignore it, it will be queued again with a new version", pv.ObjectMeta.Name)
			return nil
		}

		r.Recorder.Event(
			pvCopy,
			corev1.EventTypeWarning,
			ErrReleasePV,
			fmt.Sprintf(MessageReleasePV, pvCopy, err),
		)
		return err
	}

	r.Recorder.Event(pv, corev1.EventTypeNormal, Released, MessagePVReleased)
	return nil
}
