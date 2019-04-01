// +build e2e

package service

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	zevenet "github.com/alvaroaleman/zevenet-lb-go"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func TestE2E(t *testing.T) {
	zevenetProxyCommand := os.Getenv("ZEVENET_PROXY_COMMAND")
	if zevenetProxyCommand == "" {
		t.Fatalf("Environment var ZEVENET_PROXY_COMMAND must be set!")
	}

	// TODO: Move config parsing into dedicated package
	// that gets called from both the cmd and the tests
	zevenetURL := os.Getenv("ZEVENET_URL")
	if zevenetURL == "" {
		t.Fatalf("Environment var ZEVENET_URL must be set!")
	}

	zevenetToken := os.Getenv("ZEVENET_TOKEN")
	if zevenetToken == "" {
		t.Fatalf("Environment var ZEVENET_TOKEN must be set!")
	}

	parentInterfaceName := os.Getenv("ZEVENET_PARENT_INTERFACE_NAME")
	if parentInterfaceName == "" {
		t.Logf("Env var ZEVENET_PARENT_INTERFACE_NAME is unset, defaulting to eth0")
		parentInterfaceName = "eth0"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go executeCommand(ctx, t, zevenetProxyCommand)

	insecureTLSTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: insecureTLSTransport, Timeout: 1 * time.Second}
	for try := 0; try < 10; try++ {
		t.Logf("Attempt %v/9: Trying to connect to %s", try, zevenetURL)
		if _, err := client.Get(zevenetURL); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
		if try == 9 {
			t.Fatalf("failed to connect to %s!", zevenetURL)
		}
	}

	session, err := zevenet.Connect(zevenetURL, zevenetToken, nil)
	if err != nil {
		t.Fatalf("Failed to create Zevenet session: %v", err)
	}

	Config = &ZevenetConfiguration{
		ParentInterface: parentInterfaceName,
		ZAPISession:     session,
	}

	mgr, err := manager.New(cfg, manager.Options{})
	if err != nil {
		t.Fatalf("error getting manager: %v", err)
	}
	c = mgr.GetClient()

	recFn, _ := SetupTestReconcile(newReconciler(mgr))
	if err := add(mgr, recFn); err != nil {
		t.Fatalf("failed to add reconcile func to manager: %v", err)
	}

	stopMgr, mgrStopped := StartTestManager(mgr, t)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	service := &corev1.Service{}
	service.Name = "test-service"
	service.Namespace = "default"
	service.Spec.Type = corev1.ServiceTypeLoadBalancer
	service.Spec.LoadBalancerIP = "192.168.32.3"
	service.Spec.Ports = []corev1.ServicePort{{Port: 443}}

	if err := c.Create(ctx, service); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{
		Name:      service.Name,
		Namespace: service.Namespace,
	}, service); err != nil {
		t.Fatalf("failed to get service after creating it: %v", err)
	}

	//TODO: Verify service got finalizer
	//TODO: Verify VIP and farm gets created
	//TODO: Create a node, verify farm gets an endpoint
	//TODO: Delete service, verify, VIP and farm gets removed
	//TODO: Verify finalizer gets removed after service deletion
}

func executeCommand(ctx context.Context, t *testing.T, command string) {
	splitCommand := strings.Split(command, " ")
	if len(splitCommand) < 1 {
		t.Fatalf("Environment var ZEVENET_PROXY_COMMAND must not be empy!")
	}
	var args []string
	for _, arg := range splitCommand[1:] {
		args = append(args, arg)
	}
	cmd := exec.CommandContext(ctx, splitCommand[0], args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.Canceled {
			return
		}
		t.Fatalf("Error executing command %s with args %v: err=%v out=%s", splitCommand[0], args, err, string(out))
	}
}
