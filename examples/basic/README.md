# Basic

Basic example - testing using a local image.

1. Build

```bash
nerdctl -n k8s.io build -t kubernetes-dynamic-reclaimable-pvc-controllers:dev .
```

2. Deploy

```bash
# Create SC
kubectl apply -f ./examples/basic/sc.yaml

helm repo add plumber-cd https://plumber-cd.github.io/helm/
helm repo update
helm install provisioner plumber-cd/dynamic-pvc-provisioner -f ./examples/basic/values.yaml
helm install releaser plumber-cd/reclaimable-pv-releaser -f ./examples/basic/values.yaml

# Or - using local
helm install provisioner ../helm/charts/dynamic-pvc-provisioner -f ./examples/basic/values.yaml
helm install releaser ../helm/charts/reclaimable-pv-releaser -f ./examples/basic/values.yaml

# Check it came up
kubectl logs deployment/provisioner-dynamic-pvc-provisioner
kubectl logs deployment/releaser-reclaimable-pv-releaser
```

3. Test

```bash
# Delete SC and see it is forgotten
kubectl delete -f ./examples/basic/sc.yaml
kubectl logs deployment/releaser-reclaimable-pv-releaser
kubectl get events

# Test provisioner
kubectl apply -f ./examples/basic/sc.yaml
kubectl apply -f ./examples/basic/pod.yaml

# Check the pod came up
kubectl get pod pod-with-dynamic-reclaimable-pvc
kubectl describe pod pod-with-dynamic-reclaimable-pvc

# Check provisioner logs
kubectl logs deployment/provisioner-dynamic-pvc-provisioner

# Check PV and PVC
kubectl get pv
kubectl get pvc

# Delete the pod
kubectl delete -f ./examples/basic/pod.yaml

# Check releaser logs
kubectl logs deployment/releaser-reclaimable-pv-releaser

# check PV being released
kubectl get pvc
kubectl get pv

# Check re-claiming previously released PV
kubectl apply -f ./examples/basic/pod.yaml
kubectl describe pod pod-with-dynamic-reclaimable-pvc
kubectl logs deployment/provisioner-dynamic-pvc-provisioner
kubectl delete -f ./examples/basic/pod.yaml
kubectl logs deployment/releaser-reclaimable-pv-releaser
```

4. Cleanup

```bash
kubectl delete -f ./examples/basic/sc.yaml
helm uninstall provisioner
helm uninstall releaser
kubectl delete lease provisioner-dynamic-pvc-provisioner releaser-reclaimable-pv-releaser
```
