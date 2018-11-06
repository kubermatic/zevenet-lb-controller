/*

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

package service

import (
	"context"
	"fmt"
	"strings"

	zevenet "github.com/anmoel/zevenet-lb-go"
	"github.com/golang/glog"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new Service Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
// USER ACTION REQUIRED: update cmd/manager/main.go to call this core.Add(mgr) to install this Controller
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

type ZevenetConfiguration struct {
	ParentInterface string
	ZAPISession     *zevenet.ZapiSession
}

var Config *ZevenetConfiguration

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileService{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("service-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to Service
	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// TODO(user): Modify this to be the types you create
	// Uncomment watch a Deployment created by Service - change this for objects you create
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &corev1.Service{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileService{}

// ReconcileService reconciles a Service object
type ReconcileService struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Service object and makes changes based on the state read
// and what is in the Service.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  The scaffolding writes
// a Deployment as an example
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileService) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the Service instance
	service := &corev1.Service{}
	err := r.Get(context.TODO(), request.NamespacedName, service)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		glog.V(4).Infof("Skipping service %s/%s as its not of type Loadbalancer", service.Namespace, service.Name)
		return reconcile.Result{}, nil
	}

	if service.DeletionTimestamp != nil {
		//TODO: Cleanup logic
		return reconcile.Result{}, fmt.Errorf("Not implemented")
	}

	if service.Spec.LoadBalancerIP == "" {
		glog.V(4).Infof("Failed to reconcile service %s/%s: No LoadbalancerIP configured", service.Namespace, service.Name)
		return reconcile.Result{Requeue: true}, fmt.Errorf("Service %s/%s has no LoadbalancerIP configured", service.Namespace, service.Name)
	}

	virtIntName := fmt.Sprintf("%s:%s", Config.ParentInterface, service.Spec.LoadBalancerIP)
	virtIntName = strings.Replace(virtIntName, ".", "", -1)

	if err := ensureVirtInt(virtIntName, service.Spec.LoadBalancerIP); err != nil {
		glog.V(4).Infof("failed to ensure virtual interface: %v", err)
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to ensure virtual interface: %v", err)
	}

	return reconcile.Result{}, nil
}

func ensureVirtInt(name, ip string) error {
	virtInterface, err := Config.ZAPISession.GetVirtInt(name)
	// Unfortunatelly there doesn't seem to be an easy check for a 404
	// The underlying lib has one that didn't apply in my tests, so I assume
	// it depends on the Zevenet version - We are optimistic here and just try
	// to create on err
	if err != nil {
		glog.V(4).Infof("Error wehen getting virtual interface %s: %v", name, err)
	}
	if virtInterface != nil && virtInterface.IP != ip {
		_, err = Config.ZAPISession.DeleteVirtInt(name)
		if err != nil {
			return fmt.Errorf("failed to delete virtual interface %s: %v", name, err)
		}
	}

	if err != nil || (virtInterface != nil && virtInterface.IP != ip) {
		_, err := Config.ZAPISession.CreateVirtInt(name, ip)
		if err != nil {
			return fmt.Errorf("failed to create virtual interface %s: %v", name, err)
		}
	}
	return nil
}
