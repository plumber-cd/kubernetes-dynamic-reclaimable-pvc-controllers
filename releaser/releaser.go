// Package releaser is an automatic PV reclaimer.
package releaser

import (
	"context"
	"fmt"
	"sync"
	"time"

	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

	SCAdded          = "Added"
	MessageSCAdded   = "SC tracking added"
	SCRemoved        = "Removed"
	MessageSCRemoved = "SC tracking removed"
	SCLost           = "Lost"
	MessageSCLost    = "SC tracking removed (lost)"

	Released          = "Released"
	MessagePVReleased = "PV released successfully"

	MessageReleasePV = "error releasing PV %s: %s"
	ErrReleasePV     = "ErrReleasePV"
)

type Releaser struct {
	controller.BasicController

	SCLister storagelisters.StorageClassLister
	SCSynced cache.InformerSynced
	SCQueue  workqueue.RateLimitingInterface

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
		SCSynced: scInformer.Informer().HasSynced,
		SCQueue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "StorageClasses"),

		PVLister: pvInformer.Lister(),
		PVSynced: pvInformer.Informer().HasSynced,
		PVQueue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "PersistentVolumes"),

		managedSCMutex: &sync.Mutex{},
		managedSCSet:   make(map[string]struct{}),
	}

	klog.V(2).Info("Setting up event handlers")

	scInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.Enqueue(r.SCQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			r.Requeue(r.SCQueue, old, new)
		},
		DeleteFunc: func(obj interface{}) {
			if sc, ok := obj.(*v1.StorageClass); ok {
				r.Forget(r.SCQueue, obj)
				r.removeManagedSC(sc)
				return
			}
			klog.Warningf("Received DeleteFunc on %T - skip", obj)
		},
	})

	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.Enqueue(r.PVQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			r.Requeue(r.PVQueue, old, new)
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

			if ok := cache.WaitForCacheSync(stopCh, r.SCSynced); !ok {
				return fmt.Errorf("failed to wait for SC caches to sync")
			}

			if ok := cache.WaitForCacheSync(stopCh, r.PVSynced); !ok {
				return fmt.Errorf("failed to wait for PV caches to sync")
			}

			klog.V(2).Info("Starting workers")
			go wait.Until(
				r.RunWorker("sc", r.SCQueue, r.scSyncHandler),
				time.Second,
				stopCh,
			)
			for i := 0; i < threadiness; i++ {
				go wait.Until(
					r.RunWorker("pv", r.PVQueue, r.pvSyncHandler),
					time.Second,
					stopCh,
				)
			}

			preExistedSC, err := r.SCLister.List(labels.Everything())
			if err != nil {
				// If we can't list pre-existent objects - that would be broken state
				// It is better to fail fast, this is not expected condition
				panic(err)
			}
			for _, sc := range preExistedSC {
				r.Enqueue(r.SCQueue, sc)
			}

			preExistedPV, err := r.PVLister.List(labels.Everything())
			if err != nil {
				// If we can't list pre-existent objects - that would be broken state
				// It is better to fail fast, this is not expected condition
				panic(err)
			}
			for _, pv := range preExistedPV {
				r.Enqueue(r.PVQueue, pv)
			}

			return nil
		},
		func() {
			if r.SCQueue != nil {
				r.SCQueue.ShutDown()
			}
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

func (r *Releaser) isManagedSC(name string) bool {
	r.managedSCMutex.Lock()
	defer r.managedSCMutex.Unlock()
	_, exists := r.managedSCSet[name]
	return exists
}

func (r *Releaser) addManagedSC(sc *v1.StorageClass) {
	r.managedSCMutex.Lock()
	defer r.managedSCMutex.Unlock()
	if _, exists := r.managedSCSet[sc.ObjectMeta.Name]; exists {
		return
	}
	r.managedSCSet[sc.ObjectMeta.Name] = struct{}{}
	r.Recorder.Event(sc, corev1.EventTypeNormal, SCAdded, MessageSCAdded)
}

func (r *Releaser) removeManagedSC(sc *v1.StorageClass) {
	r.managedSCMutex.Lock()
	defer r.managedSCMutex.Unlock()
	if _, exists := r.managedSCSet[sc.ObjectMeta.Name]; !exists {
		return
	}
	delete(r.managedSCSet, sc.ObjectMeta.Name)
	r.Recorder.Event(sc, corev1.EventTypeNormal, SCRemoved, MessageSCRemoved)
}

func (r *Releaser) removeMissingSC(name string) {
	r.managedSCMutex.Lock()
	defer r.managedSCMutex.Unlock()
	if _, exists := r.managedSCSet[name]; !exists {
		return
	}
	delete(r.managedSCSet, name)
	r.Recorder.Event(&corev1.ObjectReference{
		APIVersion: "storage.k8s.io/v1",
		Kind:       "StorageClass",
		Name:       name,
	}, corev1.EventTypeNormal, SCLost, MessageSCLost)
}

func (r *Releaser) scSyncHandler(_, name string) error {
	sc, err := r.SCLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("sc '%s' in work queue no longer exists", name),
			)
			r.removeMissingSC(name)
			return nil
		}

		return err
	}

	manager, ok := sc.ObjectMeta.Annotations[AnnotationControllerId]
	if ok {
		if manager == r.ControllerId {
			r.addManagedSC(sc)
			return nil
		}
		klog.V(5).Infof("SC %s is not annotated with '%s=%s', skip", sc.ObjectMeta.Name, AnnotationControllerId, r.ControllerId)
	}

	return nil
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

	if r.isManagedSC(pv.Spec.StorageClassName) {
		return r.pvReleaseHandler(pv)
	} else {
		klog.V(5).Infof("SC %q for PV %q is not associated with this controller ID %q, skip", pv.Spec.StorageClassName, pv.ObjectMeta.Name, r.ControllerId)
	}

	return nil
}

func (r *Releaser) pvReleaseHandler(pv *corev1.PersistentVolume) error {
	if pv.Status.Phase != corev1.VolumeReleased {
		klog.V(4).Infof("PV %s is '%s', can't make it '%s'", pv.ObjectMeta.Name, pv.Status.Phase, corev1.VolumeAvailable)
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
