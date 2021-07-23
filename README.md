## What's this project?
A Proof-of-Concept to test various approaches for auto-creating & cleaning up cluster-scoped resources. 

### Notes:
- Both options described before are automated, meaning that they will occur just by virtue of installing/uninstalling
  this operator via OLM. They each use a different Custom Resource type so that they can both run at the same time 
  with clear separation between them.
- I've not done a lot to test the resiliency of either approach, but I have tested the happy-path on both to the point to 
ensure it's a repeatable experiment.

## Option 1:

The Custom Resource (Foo) is created at start-up as part of main.go prior to the controller manager full start-up. As part
of the creation process, the ClusterRole for the operator is fetched and added to the new CR as the owner via
`ownerReferences`. When the operator is uninstalled, the ClusterRole is deleted, so the delete cascades to our custom 
resource.

### Benefit:
- avoids finalizers and leans on OLM garbage-collection, which is programatically cleaner & probably more reliable as 
there's less possibility of an error causing something to get stuck in Terminating state.

### Downside:
- Since the create bit is executed before the controller manager is fully online, everything happens outside of the context of a 
reconcile loop, meaning we can't lean on the reconcile functionality to requeue requests at a set interval if the CRD 
  is not present - we would still have to determine a method of dealing with that.
  
- call a function to instantiate as a part of main.go:
```go
if err = fooReconciler.InitializeOperand(mgr); err != nil {
    setupLog.Error(err, "unable to create operand", "controller", "Foo")
}
``` 
- fetch the associated ClusterRole so that we know who will 'own' our new cluster-scope resource:
```go
opts := &client.ListOptions{
    LabelSelector: labels.SelectorFromSet(map[string]string{
        "olm.owner":      "foo-operator.v0.0.1",
        "olm.owner.kind": "ClusterServiceVersion",
    }),
}
clusterRoleList := &rbac.ClusterRoleList{}
if err := apiReader.List(context.Background(), clusterRoleList, opts); err != nil {
    r.Log.Error(err, "unable to list ClusterRoles to seek potential operand owners")
    return err
}
```
- create the new cluster-scope resource with owner defined to ensure cascade delete cleans the resource up:
```go
instance := v1alpha1.Foo{
    ObjectMeta: metav1.ObjectMeta{
        Name: "foo-cluster-instance",
        OwnerReferences: []metav1.OwnerReference{
            {
                APIVersion:         "rbac.authorization.k8s.io/v1",
                Kind:               "ClusterRole",
                UID:                clusterRoleList.Items[0].GetUID(), // doesn't really matter which 'item' we use
                Name:               clusterRoleList.Items[0].Name,
                Controller:         pointer.BoolPtr(true),
                BlockOwnerDeletion: pointer.BoolPtr(false),
            },
        },
    },
    Spec: v1alpha1.FooSpec{
        Foo: "bar",
    },
}
```
- on uninstall of the operator, OLM cleans up the ClusterRole & our resource is garbage-collected given the 
  OwnerReference we defined. 

## Option 2:


By creating a controller/reconcile around this operator's own deployment, we can add a finalizer to the
Deployment CR which ensures that we get a call-back inside our reconciler at operator removal so we
can clean up our cluster-scoped Resource (Bar).

### Benefits:
- Everything happens inside the context of a reconciler, so the controller manager is fully online & we're not really
doing anything abnormal for operators in the start-up process.
- We can use the reconcile requeue with interval to deal with the scenario where a provider operator is installed before
or in the absense of the DBaaS operator and the CRD for the resource to create is not present.

### Downside:
- Finalizers. If an error occurs, it's pretty easy to get the Deployment stuck in Terminating state and hold up the 
uninstall of the operator unless manually intervening to kill the finalizer & checking to ensure that the 
  cluster-scoped resource was really removed as intended.

- register a new controller with manager that will reconcile this operator's deployments:
```go
if err = (&controllers.SelfDeploymentReconciler{
    Client: mgr.GetClient(),
    Log:    ctrl.Log.WithName("controllers").WithName("SelfDeployment"),
    Scheme: mgr.GetScheme(),
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller", "controller", "SelfDeployment")
    os.Exit(1)
}
```
- since the reconciler, by default, would pick up every deployment on the cluster, use Event predicates 
to ensure that we *ONLY* reconcile when the deployment for this operator is involved: 
```go
// SetupWithManager sets up the controller with the Manager.
func (r *SelfDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Deployment{}).
		WithEventFilter(r.ignoreOtherDeployments()).
		Complete(r)
}

func (r *SelfDeploymentReconciler) ignoreOtherDeployments() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return evaluatePredicateObject(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return evaluatePredicateObject(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return evaluatePredicateObject(e.ObjectNew)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return evaluatePredicateObject(e.Object)
		},
	}
}

func evaluatePredicateObject(obj client.Object) bool {
	labels := obj.GetLabels()
	if obj.GetNamespace() == "operators" { // TODO don't hardcode this operator's namespace
		if val, keyFound := labels["olm.owner.kind"]; keyFound {
			if val == "ClusterServiceVersion" {
				if val, keyFound := labels["olm.owner"]; keyFound {
					return val == "foo-operator.v0.0.1" // TODO don't hardcode this operator's version
				}
			}
		}
	}
	return false
}
```
- now that we're reconciling only our operator's deployment, we can add a finalizer:
```go
// Add a cleanup finalizer if not already present
if dep.DeletionTimestamp == nil && len(dep.Finalizers) == 0 {
    log.Info("adding finalizer to deployment")
    controllerutil.AddFinalizer(dep, "testing.foo.com/finalizer")
    if err := r.Update(ctx, dep); err != nil {
        return ctrl.Result{}, err
    }
}
```
- create our cluster-scoped Resource. This option requires no ownership:
```go
bar := &v1alpha1.Bar{
		ObjectMeta: v12.ObjectMeta{
			Name: "bar-example",
		},
        Labels: map[string]string{
            "finalized-by": dep.Name,
            "finalized-in": "foo-operator",
        },
        Spec: v1alpha1.BarSpec{
            Foo: "bar",
        },
	}
```

- when a deletion timestamp is found, the resource is being removed, so we can remove our Resource & 
  remove the finalizer from our Deployment so that it's not stuck in a Terminating state:
```go
err := r.Client.Delete(ctx, bar)
if err != nil && !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
    log.Error(err, "error deleting Bar resource")
    return ctrl.Result{}, err
}

log.Info("Bar resource deleted, removing finalizer from deployment")
// refetch Deployment to avoid updating stale resource
dep := &v1.Deployment{}
if err := r.Get(ctx, req.NamespacedName, dep); err != nil {
    if errors.IsNotFound(err) {
        // CR deleted since request queued, child objects getting GC'd, no requeue
        log.Info("deployment not found, deleted, no requeue")
        return ctrl.Result{}, nil
    }
    // error fetching deployment, requeue and try again
    log.Error(err, "Error in Get of Deployment CR for finalizer removal")
    return ctrl.Result{}, err
}
dep.Finalizers = nil
err = r.Update(ctx, dep)
if err != nil && !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
    log.Error(err, "error removing finalizer from Deployment resource")
    return ctrl.Result{}, err
}
```

## RBAC:

This project also shows how to indicate a resource is cluster-scoped (via kubebuilder label):
```go
//+kubebuilder:resource:scope=Cluster

// Bar is the Schema for the bars API
type Bar struct {
	...
}
```
I also added a ClusterRole/RoleBinding to demonstrate how to work with cluster-scoped resources that aren't 'owned' by 
the operator:
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-deployment-accessor-role
rules:
  - apiGroups:
      - apps
    resources:
      - deployments
    verbs:
      - create
      - delete
      - get
      - list
      - patch
      - update
      - watch
```
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-deployment-accessors
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-deployment-accessor-role
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: system:authenticated

```