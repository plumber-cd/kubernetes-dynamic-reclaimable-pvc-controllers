# Basic

Basic example - testing using a local image.

1. Build

```bash
docker build -t kubernetes-dynamic-reclaimable-pvc-controllers:dev .
```

2. Deploy

```bash
helm repo add plumber-cd https://plumber-cd.github.io/helm/
helm repo update
helm install provisioner plumber-cd/dynamic-pvc-provisioner -f ./examples/basic/values.yaml
helm install releaser plumber-cd/reclaimable-pv-releaser -f ./examples/basic/values.yaml
```

3. Test

```bash
kubectl apply -f ./examples/basic/sc.yaml

kubectl apply -f ./examples/basic/pod.yaml
kubectl delete -f ./examples/basic/pod.yaml

# check PVC being released

kubectl apply -f ./examples/basic/pod.yaml
kubectl delete -f ./examples/basic/pod.yaml
```

4. Cleanup

```bash
kubectl delete -f ./examples/basic/sc.yaml
helm uninstall provisioner
helm uninstall releaser
```