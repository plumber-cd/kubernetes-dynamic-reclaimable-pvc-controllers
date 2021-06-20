// Package provisioner is a dynamic PVC provisioner.
package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	controller "github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
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
	LabelManagedBy    = LabelBaseName + "/" + LabelManagedByKey

	PVCProvisioned        = "PVCProvisioned"
	MessagePVCProvisioned = "PVC created successfully"

	MessageMissingPVC = "'%s' missing PVC"
	ErrMissingPVC     = "ErrMissingPVC"

	MessageInvalidPVC = "'%s' invalid PVC: %s"
	ErrInvalidPVC     = "ErrInvalidPVC"

	MessageMissingVolume = "Pod was missing volume '%s'"
	ErrMissingVolume     = "ErrMissingVolume"

	MessagePVCProvisionFailed = "PVC failed to create"
	ErrPVCProvisionFailed     = "ErrPVCProvisionFailed"
)

type Provisioner struct {
	controller.BasicController

	PodsLister corelisters.PodLister
	PodsSynced cache.InformerSynced
	PodsQueue  workqueue.RateLimitingInterface
}

func New(
	ctx context.Context,
	kubeClientSet kubernetes.Interface,
	namespace,
	controllerId string,
) controller.Controller {
	klog.Info("Provisioner starting...")

	c := controller.New(ctx, kubeClientSet, namespace, AgentName, controllerId)

	podsInformer := c.KubeInformerFactory.Core().V1().Pods()

	p := &Provisioner{
		BasicController: *c,
		PodsLister:      podsInformer.Lister(),
		PodsSynced:      podsInformer.Informer().HasSynced,
		PodsQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Pods"),
	}

	klog.V(2).Info("Setting up event handlers")
	podsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			p.Enqueue(p.PodsQueue, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			p.Enqueue(p.PodsQueue, new)
		},
	})

	return p
}

func (p *Provisioner) Run(threadiness int, stopCh <-chan struct{}) error {
	return p.BasicController.Run(
		threadiness,
		stopCh,
		func(threadiness int, stopCh <-chan struct{}) error {
			klog.V(2).Info("Waiting for informer caches to sync")
			if ok := cache.WaitForCacheSync(stopCh, p.PodsSynced); !ok {
				return fmt.Errorf("failed to wait for caches to sync")
			}

			klog.V(2).Info("Starting workers")
			for i := 0; i < threadiness; i++ {
				go wait.Until(
					p.RunWorker("pod", p.PodsQueue, p.podSyncHandler),
					time.Second,
					stopCh,
				)
			}

			return nil
		},
		func() {
			if p.PodsQueue != nil {
				p.PodsQueue.ShutDown()
			}
		},
	)
}

func (p *Provisioner) Stop() {
	p.BasicController.Stop()
	// TODO: Any cleanup logic signaling to not to perform any write operations?
	klog.Info("Provisioner stopped")
}

func (p *Provisioner) podSyncHandler(namespace, name string) error {
	pod, err := p.PodsLister.Pods(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("pod '%s/%s' in work queue no longer exists", namespace, name),
			)
			return nil
		}

		return err
	}

	if pod.Status.Phase != corev1.PodPending {
		klog.V(5).Info(
			fmt.Sprintf("pod '%s/%s' is not in '%s' status, skip", namespace, name, corev1.PodPending),
		)
		return nil
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
			p.Recorder.Event(
				pod,
				corev1.EventTypeWarning,
				ErrMissingPVC,
				fmt.Sprintf(MessageMissingPVC, pvcKey),
			)
			continue
		}
		requestedVolumes[requestedVolumeName] = ""
	}

	if len(requestedVolumes) <= 0 {
		klog.V(5).Info(
			fmt.Sprintf("pod '%s/%s' did not requested any volumes, skip", namespace, name),
		)
		return nil
	}

	for _, volume := range volumes {
		_, ok := requestedVolumes[volume.Name]
		if !ok {
			klog.V(5).Info(
				fmt.Sprintf("pod '%s/%s' volume '%s' is not one of the requested, skip",
					namespace, name, volume.Name),
			)
			continue
		}

		pvc := volume.VolumeSource.PersistentVolumeClaim
		if pvc == nil {
			p.Recorder.Event(
				pod,
				corev1.EventTypeWarning,
				ErrInvalidPVC,
				fmt.Sprintf(MessageInvalidPVC, volume.Name,
					"consumer volume for requested pvc wasn't a persistentVolumeClaim type"),
			)
			continue
		}

		requestedVolumes[volume.Name] = volume.VolumeSource.PersistentVolumeClaim.ClaimName
		klog.V(4).Info(
			fmt.Sprintf("matched volume=%s to pvc=%s", volume.Name, requestedVolumes[volume.Name]),
		)
	}

	for requestedVolume, claimName := range requestedVolumes {
		if claimName == "" {
			p.Recorder.Event(
				pod,
				corev1.EventTypeWarning,
				ErrMissingVolume,
				fmt.Sprintf(MessageMissingVolume, requestedVolume),
			)
			continue
		}

		pvcYaml := annotations[fmt.Sprintf("%s/%s.%s", AnnotationBaseName, requestedVolume, AnnotationPVCKey)]
		decode := scheme.Codecs.UniversalDeserializer().Decode
		obj, _, err := decode([]byte(pvcYaml), nil, nil)
		if err != nil {
			p.Recorder.Event(
				pod,
				corev1.EventTypeWarning,
				ErrInvalidPVC,
				fmt.Sprintf(MessageInvalidPVC, requestedVolume, err),
			)
			continue
		}
		typeOk := func() bool {
			switch t := obj.(type) {
			case *corev1.PersistentVolumeClaim:
				return true
			default:
				p.Recorder.Event(
					pod,
					corev1.EventTypeWarning,
					ErrInvalidPVC,
					fmt.Sprintf(MessageInvalidPVC, requestedVolume,
						fmt.Sprintf("expected pvc, got: %s", t)),
				)
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
		pvc.ObjectMeta.Labels[fmt.Sprintf("%s/%s", LabelBaseName, LabelManagedByKey)] = p.ControllerId
		_, err = p.KubeClientSet.CoreV1().PersistentVolumeClaims(pod.ObjectMeta.Namespace).
			Create(p.Ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				continue
			}

			p.Recorder.Event(pod, corev1.EventTypeWarning, ErrPVCProvisionFailed, MessagePVCProvisionFailed)
			return err
		}
		p.Recorder.Event(pod, corev1.EventTypeNormal, PVCProvisioned, MessagePVCProvisioned)
	}

	return nil
}
