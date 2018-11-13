// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	zevenetProxyCommand := os.Getenv("ZEVENET_PROXY_COMMAND")
	if zevenetProxyCommand == "" {
		t.Fatalf("Environment var ZEVENET_PROXY_COMMAND must be set!")
	}

	zevenetURL := os.Getenv("ZEVENET_URL")
	if zevenetURL == "" {
		t.Fatalf("Environment var ZEVENET_URL must be set!")
	}

	zevenetToken := os.Getenv("ZEVENET_TOKEN")
	if zevenetToken == "" {
		t.Fatalf("Environment var ZEVENET_TOKEN must be set!")
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
