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
	"fmt"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var c client.Client

var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: "default"}}

const timeout = time.Second * 5

func TestReconcile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "default"}}
	instance.Spec.Type = corev1.ServiceTypeLoadBalancer

	// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
	// channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{})
	if err != nil {
		t.Fatalf("error getting manager: %v", err)
	}
	c = mgr.GetClient()

	recFn, requests := SetupTestReconcile(newReconciler(mgr))
	if err := add(mgr, recFn); err != nil {
		t.Fatalf("failed to add reconcile func to manager: %v", err)
	}

	stopMgr, mgrStopped := StartTestManager(mgr, t)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	// Create the Service object and expect the Reconcile
	err = c.Create(context.TODO(), instance)
	// The instance object may not be a valid object because it might be missing some required fields.
	// Please modify the instance object by adding required fields and then remove the following if statement.
	if apierrors.IsInvalid(err) {
		t.Logf("failed to create object, got an invalid object error: %v", err)
		return
	}
	t.Fatalf("error creating service: %v", err)
	defer c.Delete(context.Background(), instance)
	if err := waitForExpectedReconcileRequest(requests, expectedRequest); err != nil {
		t.Fatalf("failed waiting for reconcile after creating service: %v", err)
		t.Fatal(err)
	}

	// Create a node object and expect the Reconcile
	node := &corev1.Node{}
	node.Name = "testnode"
	if err := c.Create(ctx, node); err != nil {
		t.Fatalf("failed to create node: %v", err)
	}
	if err := waitForExpectedReconcileRequest(requests, expectedRequest); err != nil {
		t.Fatalf("failed to wait for reconcile after creating node: %v", err)
	}

	// Update the node objects spec and expect no Reconcile as the Reconcile should
	// only be triggered by changes to .Status.Conditions
	if err := updateNode(c, node, func(n *corev1.Node) { n.Spec.PodCIDR = "172.25.0.0/24" }); err != nil {
		t.Fatalf("failed to update node with podCIDR: %v", err)
	}
	err = waitForExpectedReconcileRequest(requests, expectedRequest)
	if err == nil || err.Error() != "timed out waiting to receive a request" {
		t.Fatalf("did not get expected error='timed out waiting to receive a request' but instead got err=%v", err)
	}
	err = nil

	// Update the node objects conditions and expect a Reconcile
	if err := updateNode(c, node, func(n *corev1.Node) { n.Status.Conditions = []corev1.NodeCondition{corev1.NodeCondition{}} }); err != nil {
		t.Fatalf("failed to update node with a condition: %v", err)
	}
	if err := waitForExpectedReconcileRequest(requests, expectedRequest); err != nil {
		t.Fatalf("failed to wait for a reconcile after adding a condition to the node: %v", err)
	}
}

func updateNode(c client.Client, node *corev1.Node, modifyFunc func(*corev1.Node)) error {
	// We have to store these as the node object may be nil later on
	name := node.Name
	namespace := node.Namespace

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, node); err != nil {
			return err
		}
		modifyFunc(node)
		if err := c.Update(context.Background(), node); err != nil {
			return err
		}
		return nil
	})
	return err
}

func waitForExpectedReconcileRequest(c chan reconcile.Request, expected reconcile.Request) error {
	select {
	case received := <-c:
		if reflect.DeepEqual(received, expected) {
			return nil
		}
		return fmt.Errorf("received request does not match expected request")
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timed out waiting to receive a request")
	}
}
