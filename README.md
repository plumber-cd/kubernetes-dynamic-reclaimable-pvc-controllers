# kubernetes-dynamic-reclaimable-pvc-controllers

Automatic PV releaser.

- [kubernetes-dynamic-reclaimable-pvc-controllers](#kubernetes-dynamic-reclaimable-pvc-controllers)
  - [Features](#features)
  - [Disclaimers](#disclaimers)
  - [Why do I need this?](#why-do-i-need-this)
    - [Why `reclaimPolicy: Retain` is not enough?](#why-reclaimpolicy-retain-is-not-enough)
    - [Why not a static PVC?](#why-not-a-static-pvc)
    - [Why not StatefulSets?](#why-not-statefulsets)
  - [PV Releaser Controller](#pv-releaser-controller)
    - [Associate](#associate)
    - [Release](#release)
    - [Usage](#usage-1)
  - [Helm](#helm)

## Features

- PV Releaser
  - Deletes `claimRef` from PVs that belong to Storage Class annotated for this Releaser ID (`-controller-id`) to move their status from `Released` to `Available` **without deleting any data**.

## Disclaimers

**Releaser Controller is by design automatically makes PVs with `reclaimPolicy: Retain` available to be reclaimed by other consumers without cleaning up any data. Use it with caution - this behavior might not be desirable in most cases. Any data left on the PV after the previous consumer will be available to all the following consumers. You may want to use StatefulSets instead. This controller might be ideal for something like build cache - insensitive data by design required to be shared among different consumers. There is many use cases for this, one of them is documented in [examples/jenkins-kubernetes-plugin-with-build-cache](examples/jenkins-kubernetes-plugin-with-build-cache).**

## Why do I need this?

Essentially Releaser controller allows you to have a storage pool of reusable PVs that retain data between consumers.

The problem statement for creating it was - I needed a pool of CI/CD build caches that can be re-used by my build pods but not allowed to be used concurrently. Normally, similar functionality is performed by a StatefulSet controller, but build pods (such as Jenkins) are not managed by StatefulSet controller.

### Why `reclaimPolicy: Retain` is not enough?

When PVC that were using PV with `reclaimPolicy: Retain` is deleted - Kubernetes marks this PV `Released`. Fortunately, this will not let any other PVC to use it. I say fortunately because imagine if it did - meaning all the data on the volume could be accessed by a new consumer. This is not what `reclaimPolicy: Retain` is designed for - it only allows cluster administrators to recover the data after accidental PVC deletion. Even now deprecated `reclaimPolicy: Recycle` was performing a cleanup before making PV `Available` again. Unfortunately, this just doesn't work for something like a CI/CD build cache, where you intentionally want to reuse data from the previous consumer.

### Why not a static PVC?

One way to approach this problem statement would be just to create a static PVC with `accessModes: ["ReadWriteMany"]` - and it would work in most of the cases. But it has certain limitations - let's start with the fact `ReadWriteMany` is not supported by many storage types (example: EBS). The ones that do support it (another popular choice - EFS) most likely based on NFS which does not support an FS-level locking. Even if you got a storage type that supports both `ReadWriteMany` and FS level locking - many tools just doesn't seem to use FS level locking (or have other bugs related to concurrent cache usage). All this can lead to various race conditions failing your builds. That can be solved by making different builds to isolate themselves into a different sub-folders, but that reduces overall cache hit ratio - performance gain will be smaller and you'll have a lot of duplicated cache for commonly used transitive build dependencies.

### Why not StatefulSets?

StatefulSets are idiomatic way to reuse PVs and preserve data in Kubernetes. It works great for most of the stateful workload types - unfortunately it doesn't fit very well for CI/CD. Build pods are most likely dynamically generated in CI/CD, each pod is crafted for a specific project, with containers to bring in tools that are needed for this specific project. A simple example - one project might need a MySQL container for its unit tests while another might need a PostgreSQL container - but both are Maven projects so both need a Maven cache. You can't do this with StatefulSets where all the pods are exactly the same. Not to mention, that most of the times - using StatefulSets is just not an option (hello, Jenkins).

## PV Releaser Controller

### Associate

Releaser considers PVs associated when their Storage Class is annotated with `metadata.annotations."reclaimable-pv-releaser.kubernetes.io/controller-id"` pointing to this `-controller-id`.

### Release

Releaser watches for PVs to be released.
The following conditions must be met for a PV to be made `Available`:

- `metadata.annotations."reclaimable-pv-releaser.kubernetes.io/controller-id"` on Storage Class must be set to this Controller ID.
- `status.phase` must be `Released`.

If these conditions are met, Releaser will set `spec.claimRef` to `null`. That will make Kubernetes eventually to mark `status.phase` of this PV as `Available` - making other PVCs able to reclaim this PV.

### Usage

```
Usage of reclaimable-pv-releaser:
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
reclaimable-pv-releaser \
  -controller-id reclaimable-pvc-test \
  -lease-lock-name reclaimable-pvc-releaser-test \
  -lease-lock-namespace default
```

## Helm

You can deploy Releaser controller using Helm:

```
helm repo add plumber-cd https://plumber-cd.github.io/helm/
helm repo update
helm install releaser plumber-cd/reclaimable-pv-releaser
```

See [https://github.com/plumber-cd/helm](https://github.com/plumber-cd/helm).

Values:

- Releaser: [https://github.com/plumber-cd/helm/blob/main/charts/reclaimable-pv-releaser/values.yaml](https://github.com/plumber-cd/helm/blob/main/charts/reclaimable-pv-releaser/values.yaml)
