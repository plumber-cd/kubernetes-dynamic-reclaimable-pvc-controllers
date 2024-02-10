
First, you will have to create a StorageClass:

```yaml
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: jenkins-maven-cache
  annotations:
    reclaimable-pv-releaser.kubernetes.io/controller-id: dynamic-reclaimable-pvc-controllers
provisioner: kubernetes.io/aws-ebs
parameters:
  type: gp2
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
---
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: jenkins-golang-cache
  annotations:
    reclaimable-pv-releaser.kubernetes.io/controller-id: dynamic-reclaimable-pvc-controllers
provisioner: kubernetes.io/aws-ebs
parameters:
  type: gp2
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
```

For maximizing cache hit ratio - you may want to have a separate SC per each cache type, i.e. Maven, Gradle, Go, etc.

Now, you can create a pod with ephemeral volumes using that reclaimable storage classes via a `podTemplate` in your Jenkinsfile:

```groovy
podTemplate(yaml: """
    apiVersion: v1
    kind: Pod
    metadata:
    spec:
      volumes:
        - name: maven-cache
          ephemeral:
            volumeClaimTemplate:
              spec:
                storageClassName: jenkins-maven-cache
                resources:
                  requests:
                    storage: 1Gi
                accessModes:
                  - ReadWriteOnce
        - name: golang-cache
          ephemeral:
            volumeClaimTemplate:
              spec:
                storageClassName: jenkins-golang-cache
                resources:
                  requests:
                    storage: 1Gi
                accessModes:
                  - ReadWriteOnce
      securityContext:
        supplementalGroups: [1000]
        fsGroup: 1000
      containers:
        - name: maven
          image: maven:3.8.1-jdk-8
          command:
            - sleep
          args:
            - 99d
          volumeMounts:
            - name: maven-cache
              mountPath: /root/.m2/repository
        - name: golang
          image: golang:1.16.5
          command:
            - sleep
          args:
            - 99d
          volumeMounts:
            - name: golang-cache
              mountPath: /go/pkg/mod
""") {
    node(POD_LABEL) {
      stage('Get a Maven project') {
        git 'https://github.com/jenkinsci/kubernetes-plugin.git'
        container('maven') {
          stage('Build a Maven project') {
            sh 'mvn -B -ntp clean install'
          }
        }
      }

      stage('Get a Golang project') {
        git url: 'https://github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers.git', branch: 'main'
        container('golang') {
          stage('Build a Go project') {
            sh 'mkdir bin && go build -o ./bin ./...'
          }
        }
      }
    }
}
```
