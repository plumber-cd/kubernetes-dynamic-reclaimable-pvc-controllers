// Package leader is an election mechanism as explained in https://github.com/kubernetes/client-go/blob/master/examples/leader-election/main.go
package leader

import (
	"context"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	klog "k8s.io/klog/v2"
)

type Config struct {
	LeaseLockName      string
	LeaseLockNamespace string
	ID                 string
	StopCh             <-chan struct{}
	Ctx                context.Context
	Config             *rest.Config
	Client             *clientset.Clientset
	Namespace          string
	ControllerId       string
	Run                func(
		ctx context.Context,
		stopCh <-chan struct{},
		config *rest.Config,
		client *clientset.Clientset,
		namespace string,
		controllerId string,
	)
	Stop   func(config *rest.Config, client *clientset.Clientset)
	Cancel context.CancelFunc
}

func Elect(config *Config) {
	if config.LeaseLockName == "" {
		klog.Fatal("unable to get lease lock resource name (missing lease-lock-name flag).")
	}
	if config.LeaseLockNamespace == "" {
		klog.Fatal("unable to get lease lock resource namespace (missing lease-lock-namespace flag).")
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      config.LeaseLockName,
			Namespace: config.LeaseLockNamespace,
		},
		Client: config.Client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: config.ID,
		},
	}

	leaderelection.RunOrDie(config.Ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second,
		RenewDeadline:   15 * time.Second,
		RetryPeriod:     5 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				config.Run(ctx, config.StopCh, config.Config, config.Client, config.Namespace, config.ControllerId)
			},
			OnStoppedLeading: func() {
				klog.Infof("leader lost: %s", config.ID)
				config.Stop(config.Config, config.Client)
				os.Exit(0)
			},
			OnNewLeader: func(identity string) {
				if identity == config.ID {
					klog.Infof("I am the leader now: %s", config.ID)
					return
				}
				klog.V(2).Infof("new leader elected: %s", identity)
			},
		},
	})
}
