
First, create a StorageClass. You can use this [example](../basic/sc.yaml).

Now, you can do something like this in your Jenkinsfile:

```groovy
def pvcName = "some-randomly-generated-name"
podTemplate(yaml: """
    apiVersion: v1
    kind: Pod
    metadata:
      annotations:
        dynamic-pvc-provisioner.kubernetes.io/cache.enabled: "true"
        dynamic-pvc-provisioner.kubernetes.io/cache.pvc: |
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
        - name: cache
          persistentVolumeClaim:
            claimName: ${pvcName}
      securityContext:
        supplementalGroups: [1000]
        fsGroup: 1000
      containers:
        - name: busybox
          image: busybox
          command:
            - sleep
          args:
            - 99d
          volumeMounts:
            - name: cache
              mountPath: /cache
""") {
    node(POD_LABEL) {
        container('busybox') {
            sh 'ls -la /cache'
            sh "touch /cache/${BUILD_NUMBER}.txt"
        }
    }
}
```

Since provisioner was implemented as a regular controller and not admission controller and Pod resources are immutable (for the most part) - `spec.volumes[].persistentVolumeClaim.claimName` must be set before the Pod is created.

You can generate a random unique name in whichever way you want and use it for the pod template.
