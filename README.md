# kubernetes-dynamic-reclaimable-pvc-controllers

Dynamic PVC provisioner for pods requesting it via annotations. Automatic PV releaser.

- [kubernetes-dynamic-reclaimable-pvc-controllers](#kubernetes-dynamic-reclaimable-pvc-controllers)
  - [Features](#features)
  - [Disclaimers](#disclaimers)
  - [Why do I need this?](#why-do-i-need-this)
    - [Why `reclaimPolicy: Retain` is not enough?](#why-reclaimpolicy-retain-is-not-enough)
    - [Why not a static PVC?](#why-not-a-static-pvc)
    - [Why not StatefulSets?](#why-not-statefulsets)
    - [But why dynamic PVC provisioning from the pod annotations?](#but-why-dynamic-pvc-provisioning-from-the-pod-annotations)
  - [PVC Provisioner Controller](#pvc-provisioner-controller)
    - [Provision](#provision)
    - [Usage](#usage)
  - [PV Releaser Controller](#pv-releaser-controller)
    - [Associate](#associate)
    - [Release](#release)
    - [Usage](#usage-1)
  - [Helm](#helm)

## Features

- PVC Provisioner
  - Dynamically create PVC for Pods requesting it via the annotations.
  - Pod is automatically set as `ownerReferences` to the PVC - guaranteeing its deletions upon Pod deletion.
- PV Releaser
  - Automatically associates Releaser with PVs claimed by PVCs that were created by Provisioner with the same `--controller-id`.
  - Deletes `claimRef` from PVs associated with Releaser to move their status from `Released` to `Available` **without deleting any data**.
- Provisioner and Releaser are two separate controllers under one roof, and they can be deployed separately.
  - You can use Provisioner alone for something like Jenkins Kubernetes plugin that doesn't allow PVC creation on its own and automate PVC provisioning from the pod requests. Provisioner on its own will not make PVs automatically reclaimable.
  - You can use Releaser alone - provided you associate either your PVCs or PVs with it on your own. That will set PVCs able to automatically reclaim associated PVs with whatever data left in it from previous consumer.
- To make Releaser and Deployer work together - they need to have the same `--controller-id`.

## Disclaimers

**Provisioner Controller ignores RBAC. If the user creating the Pod didn't had permissions to create PVC - it will still be created as long as Provisioner has access to do it.**

**Releaser Controller is by design automatically makes PVs with `reclaimPolicy: Retain` available to be reclaimed by other consumers without cleaning up any data. Use it with caution - this behavior might not be desirable in most cases. Any data left on the PV after the previous consumer will be available to all the following consumers. You may want to use StatefulSets instead. This controller might be ideal for something like build cache - insensitive data by design required to be shared among different consumers. There is many use cases for this, one of them is documented in [examples/jenkins-kubernetes-plugin-with-build-cache](examples/jenkins-kubernetes-plugin-with-build-cache).**

## Why do I need this?

Essentially these two controllers allows you to have a storage pool of reusable PVs.

The problem statement for creating this was - I need a pool of CI/CD build caches that can be re-used by my build pods but not allowed to be used concurrently.

### Why `reclaimPolicy: Retain` is not enough?

When PVC that were using PV with `reclaimPolicy: Retain` is deleted - Kubernetes marks this PV `Released`. Fortunately, this does not let any other PVC to start using it. I say fortunately because imagine if it did - meaning all the data on the volume could be accessed by a new consumer. This is not what `reclaimPolicy: Retain` is designed for - it only allows cluster administrators to recover the data after accidental PVC deletion. Even now deprecated `reclaimPolicy: Recycle` was performing a cleanup before making PV `Available` again. Unfortunately, this just doesn't work for something like a CI/CD build cache, where you intentionally want to reuse data from the previous consumer.

### Why not a static PVC?

One way to approach this problem statement would be just create a static PVC with `accessModes: ["ReadWriteMany"]` - and it would work in most of the cases. But it has certain limitations - let's start with the fact `ReadWriteMany` is not supported by many storage types (example: EBS). The ones that do support (another popular choice - EFS) most likely based on NFS which does not support an FS-level locking. Even if you got a storage type that supports both `ReadWriteMany` and FS level locking - many tools just doesn't seem to use FS level locking (or have other bugs related to concurrent cache usage). All this can lead to various race conditions failing your builds. That can be solved by making different builds to isolate themselves into a different sub-folders, but that reduces overall efficiency - performance gain will be smaller and you'll have a lot of duplicated cache for commonly used transitive build dependencies.

### Why not StatefulSets?

StatefulSets are idiomatic way to reuse PVs and preserve data in Kubernetes. It works great for most of the stateful workload types - unfortunately it doesn't suit very well for CI/CD. Build pods are most likely dynamically generated in CI/CD, each pod is crafted for a specific project, with containers to bring in tools that are needed for this specific project. A simple example - one project might need a MySQL container for its unit tests while another might need a PostgreSQL container - but both are Maven projects so both need a Maven cache. You can't do this with StatefulSets where all the pods are exactly the same.

### But why dynamic PVC provisioning from the pod annotations?

Everything said above explains only the need in automatic PV Releaser, but what is dynamic PVC Provisioner for?

Indeed you might not need it in most of the cases, that's why PVC Provisioner and PV Releaser are two separate controllers and they could be deployed separately. And that's why PV Releaser can disable automatic PV association. If you want to craft either PV or PVC by yourself - you can do that, just don't forget to put a label on it so PV Releaser knows it needs to make it `Available`.

Some CI/CD engines like Jenkins with Kubernetes Plugin lets you only define a build pod and no additional resources. That makes PV Releaser unusable as something needs to create a PVC for the build pod. PVC Provisioner is a great workaround in this case - you can define a PVC right on the pod as annotation.

## PVC Provisioner Controller

### Provision

Pods can request PVC to be automatically created for it via the annotation:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pod-with-dynamic-reclaimable-pvc
  annotations:
    dynamic-pvc-provisioner.kubernetes.io/<volumeName>.enabled: "true"
    dynamic-pvc-provisioner.kubernetes.io/<volumeName>.pvc: |
      apiVersion: v1
      kind: PersistentVolumeClaim
      spec:
        storageClassName: reclaimable-storage-class
        resources:
          requests:
            storage: 1Gi
        accessModes:
          - ReadWriteOnce
spec:
  volumes:
    - name: <volumeName>
      persistentVolumeClaim:
        claimName: reclaimable-pvc
  containers:
    - name: web
      image: nginx
      volumeMounts:
        - name: <volumeName>
          mountPath: /data
```

Provisioner listens for pods created/updated with `dynamic-pvc-provisioner.kubernetes.io/*` annotations.
The following conditions must be met in order for provisioner to create requested PVC:

- `dynamic-pvc-provisioner.kubernetes.io/<volumeName>.enabled` must be `true`.
- `dynamic-pvc-provisioner.kubernetes.io/<volumeName>.pvc` must be set to a valid yaml or json representing a single `PersistentVolumeClaim` object.
- `spec.volumes[].name` with that name must exist and have `spec.volumes[].persistentVolumeClaim` on it.
- `spec.volumes[].persistentVolumeClaim.claimName` must not already exist.
- `status.phase` must be `Pending`

If all these conditions are met - this controller will automatically create a PVC as defined in `dynamic-pvc-provisioner.kubernetes.io/<volumeName>.pvc`.

Provisioner will apply following modifications to the `dynamic-pvc-provisioner.kubernetes.io/<volumeName>.pvc` before creating an actual PVC:

- Original `metadata.name` will be ignored and set to `spec.volumes[].persistentVolumeClaim.claimName` from matching `spec.volumes[].name` to `<volumeName>`. `metadata.name` not required to be set in `dynamic-pvc-provisioner.kubernetes.io/<volumeName>.pvc`.
- `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` will be set to refer to the current Controller ID.
- `metadata.ownerReferences` will be set referring to the current pod as an owner - guaranteeing PVC to be deleted when the pod is deleted.

### Usage

```
Usage of dynamic-pvc-provisioner:
  -add_dir_header
    	If true, adds the file directory to the header of the log messages
  -alsologtostderr
    	log to standard error as well as files
  -controller-id string
    	this controller identity name - use the same string for both provisioner and releaser
  -kubeconfig string
    	optional, absolute path to the kubeconfig file
  -lease-lock-id string
    	optional, the lease lock holder identity name (default <computed>)
  -lease-lock-name string
    	the lease lock resource name
  -lease-lock-namespace string
    	optional, the lease lock resource namespace; default to -namespace
  -log_backtrace_at value
    	when logging hits line file:N, emit a stack trace
  -log_dir string
    	If non-empty, write log files in this directory
  -log_file string
    	If non-empty, use this log file
  -log_file_max_size uint
    	Defines the maximum size a log file can grow to. Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
  -logtostderr
    	log to standard error instead of files (default true)
  -namespace string
    	limit to a specific namespace - only for provisioner
  -one_output
    	If true, only write logs to their native severity level (vs also writing to each lower severity level)
  -skip_headers
    	If true, avoid header prefixes in the log messages
  -skip_log_headers
    	If true, avoid headers when opening log files
  -stderrthreshold value
    	logs at or above this threshold go to stderr (default 2)
  -v value
    	number for the log level verbosity
  -vmodule value
    	comma-separated list of pattern=N settings for file-filtered logging
```

Example:

```
dynamic-pvc-provisioner \
  -controller-id reclaimable-pvc-test \
  -namespace default \
  -lease-lock-name reclaimable-pvc-provisioner-test
```

## PV Releaser Controller

For Releaser to be able to make PVs claimed by Provisioner `Available` after PVC is gone - Releaser and Provisioner must share the same Controller ID.

### Associate

Once `Released` - PVs doesn't have any indication that they were once associated with a PVC that had association with this Controller ID. To establish this relation - we must catch it while PVC still exists and mark it with our label. If Releaser was down the whole time PVC existed - PV could never be associated making it now orphaned and it will stay as `Released` - Releaser can't know it have to make it `Available`.

Releaser listens for PV creations/updates.
The following conditions must be met for a PV to be associated with a Releaser:

- PV doesn't already have `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` association.
- `spec.claimRef` must refer to a PVC that has `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` set to this Controller ID.
- `--disable-automatic-association` must be `false`.

To establish association Releaser will set itself to `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` on this PV.

### Release

Releaser watches for PVs to be released.
The following conditions must be met for a PV to be made `Available`:

- `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` must be set to this Controller ID.
- `status.phase` must be `Released`.

If these conditions are met, Releaser will set `spec.claimRef` to `null`. That will make Kubernetes eventually to mark `status.phase` of this PV as `Available` - making other PVCs able to reclaim this PV. Releaser will also delete `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` to remove association - the next PVC might be managed by something else.

### Usage

```
Usage of reclaimable-pv-releaser:
  -add_dir_header
    	If true, adds the file directory to the header of the log messages
  -alsologtostderr
    	log to standard error as well as files
  -controller-id string
    	this controller identity name - use the same string for both provisioner and releaser
  -disable-automatic-association
    	disable automatic PV association
  -kubeconfig string
    	optional, absolute path to the kubeconfig file
  -lease-lock-id string
    	optional, the lease lock holder identity name (default <computed>)
  -lease-lock-name string
    	the lease lock resource name
  -lease-lock-namespace string
    	optional, the lease lock resource namespace; default to -namespace
  -log_backtrace_at value
    	when logging hits line file:N, emit a stack trace
  -log_dir string
    	If non-empty, write log files in this directory
  -log_file string
    	If non-empty, use this log file
  -log_file_max_size uint
    	Defines the maximum size a log file can grow to. Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
  -logtostderr
    	log to standard error instead of files (default true)
  -namespace string
    	limit to a specific namespace - only for provisioner
  -one_output
    	If true, only write logs to their native severity level (vs also writing to each lower severity level)
  -skip_headers
    	If true, avoid header prefixes in the log messages
  -skip_log_headers
    	If true, avoid headers when opening log files
  -stderrthreshold value
    	logs at or above this threshold go to stderr (default 2)
  -v value
    	number for the log level verbosity
  -vmodule value
    	comma-separated list of pattern=N settings for file-filtered logging
```

Example:

```
reclaimable-pv-releaser \
  -controller-id reclaimable-pvc-test \
  -lease-lock-name reclaimable-pvc-releaser-test \
  -lease-lock-namespace default
```

## Helm

You can deploy both controllers using Helm:

```
helm repo add plumber-cd https://plumber-cd.github.io/helm/
helm repo update
helm install provisioner plumber-cd/dynamic-pvc-provisioner
helm install releaser plumber-cd/reclaimable-pv-releaser
```

See [https://github.com/plumber-cd/helm](https://github.com/plumber-cd/helm).

Values:

- Provisioner: [https://github.com/plumber-cd/helm/blob/main/charts/dynamic-pvc-provisioner/values.yaml](https://github.com/plumber-cd/helm/blob/main/charts/dynamic-pvc-provisioner/values.yaml)
- Releaser: [https://github.com/plumber-cd/helm/blob/main/charts/reclaimable-pv-releaser/values.yaml](https://github.com/plumber-cd/helm/blob/main/charts/reclaimable-pv-releaser/values.yaml)
