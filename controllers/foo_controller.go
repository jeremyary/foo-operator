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
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jeremyary/foo-operator/api/v1alpha1"
)

// FooReconciler reconciles a Foo object
type FooReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=testing.foo.com,resources=foos,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=testing.foo.com,resources=foos/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=testing.foo.com,resources=foos/finalizers,verbs=update
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list

func (r *FooReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("foo", req.NamespacedName)

	// your logic here

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FooReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Foo{}).
		Complete(r)
}

func (r *FooReconciler) InitializeOperand(mgr ctrl.Manager) error {

	// controller/cache will not be ready during operator 'setup', use manager client & API Reader instead
	mgrClient := mgr.GetClient()
	apiReader := mgr.GetAPIReader()

	// fetch a CSV-owner clusterRole to make owner of our cluster-scoped resource
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"olm.owner":      "foo-operator.v0.0.1", // TODO don't hardcode the version
			"olm.owner.kind": "ClusterServiceVersion",
		}),
	}
	clusterRoleList := &rbac.ClusterRoleList{}
	if err := apiReader.List(context.Background(), clusterRoleList, opts); err != nil {
		r.Log.Error(err, "unable to list ClusterRoles to seek potential operand owners")
		return err
	}

	if len(clusterRoleList.Items) < 1 {
		r.Log.Error(errors.NewNotFound(
			schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "ClusterRole"}, "potentialOwner"),
			"could not find ClusterRole owned by CSV to inherit Foo operand")
	}

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

	instances := v1alpha1.FooList{}
	if err := apiReader.List(context.Background(), &instances); err != nil {
		r.Log.Error(err, "failed to retrieve list of Foo instances")
		return err
	}

	found := false
	for _, existing := range instances.Items {
		if existing.Name == "foo-cluster-instance" {
			// we shouldn't really ever hit this condition if our ownership has worked properly, but better to be safe
			found = true
			r.Log.Info("pre-existing Foo operand found, updating instead of creating a new one")
			if err := r.UpdateOperand(mgrClient, &existing, &instance); err != nil {
				r.Log.Error(err, "Failed to update pre-existing operand")
				return err
			}
		}
	}

	if !found {
		r.Log.Info("Target operand not found, instantiating default operand")
		if err := mgrClient.Create(context.Background(), &instance); err != nil {
			r.Log.Error(err, "failed to create new base operand")
			return err
		}
	}
	return nil
}

func (r *FooReconciler) UpdateOperand(mgrClient client.Client, from *v1alpha1.Foo, to *v1alpha1.Foo) error {
	originalName := from.Name
	originalVersion := from.ResourceVersion
	to.DeepCopyInto(from)
	from.Name = originalName
	from.ResourceVersion = originalVersion
	err := mgrClient.Update(context.Background(), from)
	if err != nil {
		return err
	}
	return nil
}
