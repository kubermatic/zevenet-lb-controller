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

	zevenet "github.com/alvaroaleman/zevenet-lb-go"
	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const cleanupFinalizer = "zevenet-controller.kubermatic.io/cleanup"

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
	return &ReconcileService{Client: mgr.GetClient(), scheme: mgr.GetScheme(), recorder: mgr.GetRecorder("zevenet-service-controller")}
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

	nodeToServiceMapFn := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			serviceList := &corev1.ServiceList{}
			if err := mgr.GetClient().List(context.Background(), &client.ListOptions{}, serviceList); err != nil {
				glog.Errorf("failed to list services: %v", err)
				return nil
			}
			var reconcileRequests []reconcile.Request
			for _, service := range serviceList.Items {
				if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
					continue
				}
				reconcileRequests = append(reconcileRequests, reconcile.Request{NamespacedName: types.NamespacedName{
					Name:      service.Name,
					Namespace: service.Namespace,
				},
				})
			}
			return reconcileRequests
		})
	nodePredicateFunc := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode := e.ObjectOld.(*corev1.Node)
			newNode := e.ObjectNew.(*corev1.Node)
			if len(oldNode.Status.Conditions) != len(newNode.Status.Conditions) {
				return true
			}
			for _, oldNodeCondition := range oldNode.Status.Conditions {
				for _, newNodeCondition := range newNode.Status.Conditions {
					if oldNodeCondition.Type == newNodeCondition.Type && oldNodeCondition.Status == newNodeCondition.Status {
						continue
					}
				}
				return true
			}
			// Do not req if both new and old node have the same conditions regarding Type and Status
			// We do not care about lastHeartbeatTime and lastTransitionTime
			return false
		},
		DeleteFunc: func(_ event.DeleteEvent) bool { return true },
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestsFromMapFunc{
		ToRequests: nodeToServiceMapFn},
		nodePredicateFunc,
	); err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileService{}

// ReconcileService reconciles a Service object
type ReconcileService struct {
	client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileService) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fetch the Service instance
	service := &corev1.Service{}
	err := r.Get(ctx, request.NamespacedName, service)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}
	result, err := r.actualReconcile(ctx, service)
	if err != nil {
		r.recorder.Eventf(service, corev1.EventTypeWarning, "Error", "%v", err)
	}
	return result, err
}

// We wrap this in oder to be easely able to emit on event on errors
func (r *ReconcileService) actualReconcile(ctx context.Context, service *corev1.Service) (reconcile.Result, error) {
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		glog.V(4).Infof("Skipping service %s/%s as its not of type Loadbalancer", service.Namespace, service.Name)
		return reconcile.Result{}, nil
	}

	virtIntName := fmt.Sprintf("%s:%s", Config.ParentInterface, service.Spec.LoadBalancerIP)
	virtIntName = strings.Replace(virtIntName, ".", "", -1)
	farmName := fmt.Sprintf("kube-%s-%s-%s", service.Namespace, service.Name, service.Spec.LoadBalancerIP)
	farmName = strings.Replace(farmName, ".", "-", -1)

	if service.DeletionTimestamp != nil {
		_, err := Config.ZAPISession.DeleteFarm(farmName)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to delete farm %s: %v", farmName, err)
		}
		_, err = Config.ZAPISession.DeleteVirtInt(virtIntName)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to delete virtual interface %s: %v", virtIntName, err)
		}
		finalizerSet := sets.NewString(service.Finalizers...)
		if finalizerSet.Has(cleanupFinalizer) {
			finalizerSet.Delete(cleanupFinalizer)
			service.Finalizers = finalizerSet.List()
			if err := r.Client.Update(ctx, service); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to update service after removing cleanup finalizer: %v", err)
			}
		}
		return reconcile.Result{}, nil
	}

	if service.Spec.LoadBalancerIP == "" {
		glog.V(4).Infof("Failed to reconcile service %s/%s: No LoadbalancerIP configured", service.Namespace, service.Name)
		return reconcile.Result{Requeue: true}, fmt.Errorf("Service %s/%s has no LoadbalancerIP configured", service.Namespace, service.Name)
	}

	// TODO: This is a limitation of the zevent go lib we use, the actual api allows
	// multiple ports
	if len(service.Spec.Ports) != 1 {
		return reconcile.Result{}, fmt.Errorf("this controller currently only supports services withe xactly one port")
	}

	if !sets.NewString(service.Finalizers...).Has(cleanupFinalizer) {
		service.Finalizers = append(service.Finalizers, cleanupFinalizer)
		if err := r.Update(ctx, service); err != nil {
			return reconcile.Result{Requeue: true}, fmt.Errorf("failed to add finalizer: %v", err)
		}
	}

	if err := r.ensureVirtInt(virtIntName, service); err != nil {
		glog.V(4).Infof("failed to ensure virtual interface: %v", err)
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to ensure virtual interface: %v", err)
	}

	if err := r.ensureFarm(farmName, service, int(service.Spec.Ports[0].Port)); err != nil {
		glog.V(4).Infof("failed to ensure farm %s: %v", farmName, err)
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to ensure farm %s: %v", farmName, err)
	}

	nodeList := &corev1.NodeList{}
	// TODO: Find out how to use a lister with Kubebuilder
	if err := r.List(context.Background(), nil, nodeList); err != nil {
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to list nodes: %v", err)
	}

	var desiredBackends []zevenet.BackendDetails
	for _, node := range nodeList.Items {
		if !isNodeReady(node) {
			continue
		}
		for _, nodeAddress := range node.Status.Addresses {
			if nodeAddress.Type == corev1.NodeExternalIP || nodeAddress.Type == corev1.NodeInternalIP {
				desiredBackends = append(desiredBackends, zevenet.BackendDetails{
					IPAddress: nodeAddress.Address,
					Port:      int(service.Spec.Ports[0].NodePort),
				})
			}
		}
	}
	if err := r.ensureBackends(farmName, desiredBackends, service); err != nil {
		glog.V(4).Infof("Failed to ensure backends for farm %s: %v", farmName, err)
		return reconcile.Result{Requeue: true}, fmt.Errorf("failed to ensure backends for farm %s: %v", farmName, err)
	}

	service.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: service.Spec.LoadBalancerIP}}
	if err := r.Status().Update(ctx, service); err != nil {
		return reconcile.Result{Requeue: true}, fmt.Errorf("fauked to set service status: %v", err)
	}

	return reconcile.Result{}, nil
}

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *ReconcileService) ensureFarm(name string, service *corev1.Service, virtualPort int) error {
	farm, err := Config.ZAPISession.GetFarm(name)
	// Unfortunatelly there doesn't seem to be an easy check for a 404
	// The underlying lib has one that didn't apply in my tests, so I assume
	// it depends on the Zevenet version - We are optimistic here and just try
	// to create on err
	if err != nil {
		glog.V(4).Infof("Error when getting farm %s: %v", name, err)
	}
	virtualIP := service.Spec.LoadBalancerIP

	if err == nil && farm != nil && !farm.IsHTTP() && farm.VirtualIP == virtualIP && farm.VirtualPort == virtualPort {
		return nil
	}

	// The underlying lib does not support upting non-http farms yet so we have to delete and
	// re-create it
	// TODO: patch the zevenet lib to allow updating non-http farms
	if farm != nil {
		if _, err := Config.ZAPISession.DeleteFarm(name); err != nil {
			return fmt.Errorf("failed to delete farm %s: %v", name, err)
		}
		r.recorder.Eventf(service, corev1.EventTypeNormal, "Created", "Successfully deleted farm %s", name)
	}

	_, err = Config.ZAPISession.CreateFarmAsL4xNat(name, virtualIP, virtualPort)
	if err != nil {
		return fmt.Errorf("failed to create farm %s: %v", name, err)
	}
	r.recorder.Eventf(service, corev1.EventTypeNormal, "Created", "Successfully created farm %s", name)

	return nil
}

func (r *ReconcileService) ensureBackends(farmName string, desiredBackends []zevenet.BackendDetails, service *corev1.Service) error {
	farm, err := Config.ZAPISession.GetFarm(farmName)
	if err != nil {
		return fmt.Errorf("failed to get farm %s: %v", farmName, err)
	}

	var backendsToDelete, backendsToCreate []zevenet.BackendDetails
	for _, existingBackend := range farm.Backends {
		if !isBackendInBackendsList(existingBackend, desiredBackends) {
			backendsToDelete = append(backendsToDelete, existingBackend)
		}
	}

	for _, desiredBackend := range desiredBackends {
		if !isBackendInBackendsList(desiredBackend, farm.Backends) {
			backendsToCreate = append(backendsToCreate, desiredBackend)
		}
	}

	for _, backendToCreate := range backendsToCreate {
		if err := Config.ZAPISession.CreateL4xNatBackend(farmName, backendToCreate.IPAddress, backendToCreate.Port); err != nil {
			return fmt.Errorf("failed to create backend for farm %s: %v", farmName, err)
		}
		r.recorder.Eventf(service, corev1.EventTypeNormal, "CreatedBackend", "Successfully created backend %s", backendToCreate.String())
	}

	for _, backendToDelete := range backendsToDelete {
		if err := Config.ZAPISession.DeleteL4xNatBackend(farmName, backendToDelete.ID); err != nil {
			return fmt.Errorf("faild to delete backend for farm %s: %v", farmName, err)
		}
		r.recorder.Eventf(service, corev1.EventTypeNormal, "DeletedBackend", "Successfully deleted backend %s", backendToDelete.String())
	}

	return nil
}

func isBackendInBackendsList(backend zevenet.BackendDetails, backendList []zevenet.BackendDetails) bool {

	for _, backendListItem := range backendList {
		// We intentionally conpare IPAddress and Port only as these are the only settings
		// we can set on create
		if backendListItem.IPAddress == backend.IPAddress && backendListItem.Port == backend.Port {
			return true
		}
	}

	return false
}

func (r *ReconcileService) ensureVirtInt(name string, service *corev1.Service) error {
	virtInterface, err := Config.ZAPISession.GetVirtInt(name)
	// Unfortunatelly there doesn't seem to be an easy check for a 404
	// The underlying lib has one that didn't apply in my tests, so I assume
	// it depends on the Zevenet version - We are optimistic here and just try
	// to create on err
	if err != nil {
		glog.V(4).Infof("Error when getting virtual interface %s: %v", name, err)
	}
	if virtInterface != nil && virtInterface.IP != service.Spec.LoadBalancerIP {
		_, err = Config.ZAPISession.DeleteVirtInt(name)
		if err != nil {
			return fmt.Errorf("failed to delete virtual interface %s: %v", name, err)
		}
		r.recorder.Eventf(service, corev1.EventTypeNormal, "Created", "successfully deleted virtual interface %s", name)
	}

	if err != nil || (virtInterface != nil && virtInterface.IP != service.Spec.LoadBalancerIP) {
		_, err := Config.ZAPISession.CreateVirtInt(name, service.Spec.LoadBalancerIP)
		if err != nil {
			return fmt.Errorf("failed to create virtual interface %s: %v", name, err)
		}
		r.recorder.Eventf(service, corev1.EventTypeNormal, "Created", "successfully created virtual interface %s", name)
	}
	return nil
}
