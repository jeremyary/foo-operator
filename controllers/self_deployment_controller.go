/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"github.com/go-logr/logr"
	"github.com/jeremyary/foo-operator/api/v1alpha1"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// SelfDeploymentReconciler reconciles the deployment for this operator
type SelfDeploymentReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;create;update;delete;watch

func (r *SelfDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("deployment-reconcile", req.NamespacedName)

	// due to predicate filtering, we'll only reconcile this operator's own deployment when it's seen the first time
	// meaning we have a reconcile entry-point on operator start-up, so now we can create a Bar resource
	// with a finalizer attached to the deployment

	dep := &v1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, dep); err != nil {
		if errors.IsNotFound(err) {
			// CR deleted since request queued, child objects getting GC'd, no requeue
			log.Info("deployment not found, deleted, no requeue")
			return ctrl.Result{}, nil
		}
		// error fetching deployment, requeue and try again
		log.Error(err, "Error in Get of Deployment CR")
		return ctrl.Result{}, err
	}

	// Add a cleanup finalizer if not already present
	if dep.DeletionTimestamp == nil && len(dep.Finalizers) == 0 {
		log.Info("adding finalizer to deployment")
		controllerutil.AddFinalizer(dep, "testing.foo.com/finalizer")
		if err := r.Update(ctx, dep); err != nil {
			return ctrl.Result{}, err
		}
	}

	bar := &v1alpha1.Bar{
		ObjectMeta: v12.ObjectMeta{
			Name: "bar-example",
		},
	}

	// no delete timestamp and we've made it this far, so create the cluster-scoped Bar resource if not present
	if dep.DeletionTimestamp.IsZero() {
		if err := r.Get(ctx, client.ObjectKeyFromObject(bar), bar); err != nil {
			if errors.IsNotFound(err) {
				bar.Labels = map[string]string{
					"finalized-by": dep.Name,
					"finalized-in": "foo-operator", // TODO don't hardcode operator's name?
				}
				bar.Spec = v1alpha1.BarSpec{
					Foo: "bar",
				}
				if err := r.Create(ctx, bar); err != nil {
					log.Error(err, "error creating Bar resource")
					return ctrl.Result{}, err
				}
			} else {
				// error fetching Bar, requeue and try again
				log.Error(err, "error fetching Bar CR")
				return ctrl.Result{}, err
			}
		}

	} else {
		// delete timestamp's present, need to clean up Bar cluster-scope resource now & remove finalizer from deployment
		log.Info("deleting Bar resource")
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
	}

	return ctrl.Result{}, nil
}

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
