# kubernetes-dynamic-reclaimable-pvc-controllers

Dynamic PVC provisioner for pods and jobs requesting it via annotations. Automatic PV releaser.

## Features

- PVC Provisioner
  - Dynamically create PVC for Pods or Jobs requesting it via the annotations.
  - Upon Pod or Job deletion - also deletes its PVCs that were created by this controller.
- PV Releaser
  - Associates PVs claimed by PVCs that were created by Provisioner with Releaser.
  - Deletes `claimRef` from PVs associated with Releaser to move their status from `Released` to `Available` **without cleaning up any data**.
- Provisioner and Releaser are two separate controllers under one roof, and they can be deployed separately.
  - You can use Provisioner alone for something like Jenkins Kubernetes plugin that doesn't allow PVC creation on its own and automate PVC provisioning from the pod requests. It will not make it automatically reclaimable.
  - You can use Releaser alone - provided you associate either your PVCs or PVs with it by yourself. That will set PVCs able to automatically reclaim PVs with whatever data left in it from previous consumer.

Pod and Job are interchangeable in this context - whenever it says Pod it means it can also be a Job.

## Disclaimers

**Provisioner Controller ignores RBAC. If the user creating the Pod/Job didn't had permissions to create PVC - it will still be created as long as Provisioner has access to do it.**

**Releaser Controller is by design automatically makes PVs with `reclaimPolicy: Retain` available to be reclaimed by other consumers without cleaning up any data. Use this with caution - this behavior might not be desirable in most cases. Any data left on the PV after the previous consumer will be available to all the following consumers. You may want to use StatefulSets instead. This controller might be ideal for something like build cache - insensitive data by design required to be shared among different consumers. There is many use cases for this, one of them is documented in [examples/jenkins-kubernetes-plugin-with-build-cache](examples/jenkins-kubernetes-plugin-with-build-cache).**

## PVC Provisioner Controller

### Provision

Pods/Jobs can request PVC to be automatically created for it via the annotation:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pod-with-dynamic-reclaimable-pvc
  annotations:
    dynamic-pvc-provisioner.kubernetes.io/<volumeName>/enabled: true
    dynamic-pvc-provisioner.kubernetes.io/<volumeName>/pvc: |
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
The following conditions must be met:

- `dynamic-pvc-provisioner.kubernetes.io/<volumeName>/enabled` must be `true`.
- `dynamic-pvc-provisioner.kubernetes.io/<volumeName>/pvc` must be set to a valid yaml or json representing a single `PersistentVolumeClaim` object.
- `spec.volumes[].name` with that name must exist and have `spec.volumes[].persistentVolumeClaim` on it.
- `spec.volumes[].persistentVolumeClaim.claimName` must not already exist.

If all these conditions are met - this controller will automatically create a PVC as defined in `dynamic-pvc-provisioner.kubernetes.io/<volumeName>/pvc`.

Provisioner will apply following modifications to the `dynamic-pvc-provisioner.kubernetes.io/<volumeName>/pvc` before creating an actual PVC:

- Original `metadata.name` will be ignored and set to `spec.volumes[].persistentVolumeClaim.claimName` from matching `spec.volumes[].name` to `<volumeName>`. `metadata.name` not required to be set in `dynamic-pvc-provisioner.kubernetes.io/<volumeName>/pvc`.
- `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/claimed-by"` will be set to refer to the current pod.
- `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` will be set to refer to the current Controller ID.

### Cleanup

Provisioner periodically searches for PVCs to be deleted.
Provisioner also listens for pods deletions with `dynamic-pvc-provisioner.kubernetes.io/*` annotations and that triggers cleanup routine ahead of the schedule.
The following conditions must be met for a PVC to be deleted:

- `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` indicates it is managed by this controller.
- `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/claimed-by"` refer to a non-existing object.

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

Releaser periodically searches for PVs to be released.
Releaser also listens for PVCs deletions with `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` set to this Controller ID and that triggers release routine ahead of the schedule.
The following conditions must be met for a PV to be made `Available`:

- `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` must be set to this Controller ID.
- `spec.claimRef` must be pointing to a non-existent PVC.
- `status.phase` must be `Released`.

If these conditions are met, Releaser will set `spec.claimRef` to `null`. That will make Kubernetes eventually to mark `status.phase` of this PV as `Available` - making other PVCs able to reclaim this PV. Releaser will also delete `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` to remove association - the next PVC might be managed by something else.
