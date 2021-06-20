// Package releaser is an automatic PV reclaimer.
package releaser

import (
	"context"
	"fmt"
	"time"

	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	"github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers/provisioner"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	AgentName = "reclaimable-pv-releaser"

	LabelBaseName     = AgentName + ".kubernetes.io"
	LabelManagedByKey = "managed-by"
	LabelManagedBy    = AgentName + "/" + LabelManagedByKey

	Associated          = "Associated"
	MessagePVAssociated = "PV associated successfully"

	Released          = "Released"
	MessagePVReleased = "PV released successfully"

	MessageAssociatePV = "error associating PV %s: %s"
	ErrAssociatePV     = "ErrAssociatePV"

	MessageReleasePV = "error releasing PV %s: %s"
	ErrReleasePV     = "ErrReleasePV"
)

type Releaser struct {
	controller.BasicController

	DisableAutomaticAssociation bool

	PVLister corelisters.PersistentVolumeLister
	PVSynced cache.InformerSynced
	PVQueue  workqueue.RateLimitingInterface
}

func New(
	ctx context.Context,
	kubeClientSet kubernetes.Interface,
	namespace,
	controllerId string,
	disableAutomaticAssociation bool,
) controller.Controller {
	klog.Info("Releaser starting...")

	if namespace != "" {
		klog.Warningf("Releaser can't run within a namespace as PVs are not namespaced resources - ignoring -namespace=%s and acting in the scope of the cluster", namespace)
	}

	if disableAutomaticAssociation {
		klog.Warningf("Automatic PV association is disabled - make sure you label PV manually with '%s: %s' label", LabelManagedBy, controllerId)
	}

	c := controller.New(ctx, kubeClientSet, "", AgentName, controllerId)

	pvInformer := c.KubeInformerFactory.Core().V1().PersistentVolumes()

	r := &Releaser{
		BasicController:             *c,
		DisableAutomaticAssociation: disableAutomaticAssociation,
		PVLister:                    pvInformer.Lister(),
		PVSynced:                    pvInformer.Informer().HasSynced,
		PVQueue:                     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "PersistentVolumes"),
	}

	klog.V(2).Info("Setting up event handlers")
	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.Enqueue(r.PVQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			r.Enqueue(r.PVQueue, new)
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
				return fmt.Errorf("failed to wait for caches to sync")
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

	manager, ok := pv.ObjectMeta.Labels[LabelManagedBy]
	if ok {
		if manager == r.ControllerId {
			err = r.pvReleaseHandler(pv)
			if err != nil {
				return err
			}
		}
		klog.V(5).Infof("PV %s is managed by '%s', not me '%s', skip", pv.ObjectMeta.Name, manager, r.ControllerId)
	} else if !r.DisableAutomaticAssociation {
		err = r.pvAssociateHandler(pv)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *Releaser) pvAssociateHandler(pv *corev1.PersistentVolume) error {
	if pv.Spec.ClaimRef == nil {
		klog.V(5).Infof("PV %s had no claim ref, skip", pv.ObjectMeta.Name)
		return nil
	}

	pvc, err := r.KubeClientSet.CoreV1().PersistentVolumeClaims(pv.Spec.ClaimRef.Namespace).
		Get(r.Ctx, pv.Spec.ClaimRef.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(5).Infof("PV %s had claim ref to the void, skip", pv.ObjectMeta.Name)
			return nil
		}

		return err
	}

	pvcOwner, ok := pvc.ObjectMeta.Labels[provisioner.LabelManagedBy]
	if !ok {
		klog.V(5).Infof("PVC has no manager, skip PV %s", pvc.ObjectMeta.Name, pv.ObjectMeta.Name)
		return nil
	} else if pvcOwner != r.ControllerId {
		klog.V(5).Infof("PVC %s is managed by '%s', not me '%s', skip PV %s", pvc.ObjectMeta.Name, pvcOwner, r.ControllerId, pv.ObjectMeta.Name)
		return nil
	}

	klog.V(4).Infof("PV %s is matched for association based on PVC %s", pv.ObjectMeta.Name, pvc.ObjectMeta.Name)

	pvCopy := pv.DeepCopy()
	pvCopy.ObjectMeta.Labels[LabelManagedBy] = r.ControllerId
	_, err = r.KubeClientSet.CoreV1().PersistentVolumes().Update(r.Ctx, pvCopy, metav1.UpdateOptions{})
	if err != nil {
		if errors.HasStatusCause(err, metav1.CauseTypeResourceVersionTooLarge) {
			return nil
		}

		r.Recorder.Event(
			pvCopy,
			corev1.EventTypeWarning,
			ErrAssociatePV,
			fmt.Sprintf(MessageAssociatePV, pvCopy, err),
		)
		return err
	}

	r.Recorder.Event(pv, corev1.EventTypeNormal, Associated, MessagePVAssociated)
	return nil
}

func (r *Releaser) pvReleaseHandler(pv *corev1.PersistentVolume) error {
	if pv.Status.Phase != corev1.VolumeReleased {
		klog.V(4).Infof("PV %s is '%s', can't make it reclaimable", pv.ObjectMeta.Name, pv.Status.Phase)
		return nil
	}

	pvCopy := pv.DeepCopy()
	pvCopy.Spec.ClaimRef = nil
	delete(pvCopy.ObjectMeta.Labels, LabelManagedBy)
	_, err := r.KubeClientSet.CoreV1().PersistentVolumes().Update(r.Ctx, pvCopy, metav1.UpdateOptions{})
	if err != nil {
		if errors.HasStatusCause(err, metav1.CauseTypeResourceVersionTooLarge) {
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
