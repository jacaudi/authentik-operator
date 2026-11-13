//go:build e2e

/*
Copyright 2026.

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

package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/test/utils"
)

var (
	// k8s talks to the cluster API; set up in BeforeSuite.
	k8s client.Client

	dialMu  sync.Mutex
	dialMap = map[string]string{}
)

func kubectl(ctx context.Context, args ...string) (string, error) {
	return utils.Run(exec.CommandContext(ctx, "kubectl", args...))
}

func kubectlApply(ctx context.Context, manifest string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

func helm(ctx context.Context, args ...string) (string, error) {
	return utils.Run(exec.CommandContext(ctx, "helm", args...))
}

// guardContext refuses to run against anything but the dedicated kind cluster:
// the suite installs and deletes things, and a developer's kubeconfig often
// points at a real cluster.
func guardContext(ctx context.Context) error {
	out, err := kubectl(ctx, "config", "current-context")
	if err != nil {
		return err
	}
	if want := "kind-" + clusterName; strings.TrimSpace(out) != want {
		return fmt.Errorf("kubectl context is %q, refusing to run e2e against anything but %q "+
			"(set KIND_CLUSTER and KUBECONFIG)", strings.TrimSpace(out), want)
	}
	return nil
}

func newClient() (client.Client, error) { //nolint:ireturn // controller-runtime's own constructor returns the interface
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := authentikiov1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: s})
}

// clusterTransport dials Authentik's in-cluster name through the local
// port-forward, so requests carry the Host the operator itself uses.
func clusterTransport() http.RoundTripper {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialMu.Lock()
			if to, ok := dialMap[addr]; ok {
				addr = to
			}
			dialMu.Unlock()
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// portForward is a supervised kubectl port-forward: each child runs in its own
// process group and is restarted on the same local port if it exits (it dies
// when the pod it is attached to is replaced), until Stop.
type portForward struct {
	ns, target string
	remote     int
	Port       int

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
	done    chan struct{}
}

// startPortForward forwards a free local port to target ("svc/name" or
// "pod/name") in ns and waits until it accepts connections.
func startPortForward(ctx context.Context, ns, target string, remotePort int) (*portForward, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return nil, errors.New("listener has no TCP address")
	}
	if err := l.Close(); err != nil {
		return nil, err
	}
	pf := &portForward{ns: ns, target: target, remote: remotePort, Port: addr.Port, done: make(chan struct{})}
	if err := pf.spawn(ctx); err != nil {
		return nil, err
	}
	go pf.supervise(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: time.Second}
		if c, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", pf.Port)); err == nil {
			if err := c.Close(); err != nil {
				return nil, err
			}
			return pf, nil
		}
		select {
		case <-ctx.Done():
			pf.Stop()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	pf.Stop()
	return nil, fmt.Errorf("port-forward to %s/%s did not open", ns, target)
}

// spawn starts one kubectl child. It must outlive the (spec-scoped) context
// the forward was requested with; Stop ends it.
func (p *portForward) spawn(ctx context.Context) error {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "kubectl", "port-forward", "-n", p.ns,
		"--address", "127.0.0.1", p.target, fmt.Sprintf("%d:%d", p.Port, p.remote))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = os.Environ()
	out, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	// Start under the lock, after re-checking stopped, so a Stop that has
	// already run cannot be followed by a child nobody will kill.
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.cmd = cmd
	p.mu.Unlock()
	go func() { // surface forwarding errors in the Ginkgo log
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			GinkgoWriter.Printf("port-forward %s: %s\n", p.target, sc.Text())
		}
	}()
	return nil
}

func (p *portForward) supervise(ctx context.Context) {
	defer close(p.done)
	for {
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()
		_ = cmd.Wait() // exit status is irrelevant: either we stopped it or it must be restarted
		for {
			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()
			if stopped {
				return
			}
			time.Sleep(time.Second)
			if err := p.spawn(ctx); err != nil {
				GinkgoWriter.Printf("port-forward %s: restart failed: %v\n", p.target, err)
				continue
			}
			GinkgoWriter.Printf("port-forward %s: restarted\n", p.target)
			break
		}
	}
}

// Stop ends the forward for good and kills the child's whole process group,
// so no kubectl outlives the suite.
func (p *portForward) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

// waitDeployment blocks until the Deployment is fully available.
func waitDeployment(ctx context.Context, ns, name string, timeout time.Duration) error {
	_, err := kubectl(ctx, "-n", ns, "rollout", "status", "deploy/"+name, "--timeout="+timeout.String())
	return err
}

// ensureNamespace creates ns if needed, with extra labels.
func ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	ns := &corev1.Namespace{}
	ns.Name = name
	ns.Labels = labels
	if err := k8s.Create(ctx, ns); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}

// controllerLogs returns the operator's recent logs for a failed spec.
func controllerLogs(ctx context.Context) string {
	out, err := kubectl(ctx, "-n", operatorNS, "logs", "deploy/"+operatorRelease+"-controller-manager", "--tail=200")
	if err != nil {
		return "no logs: " + err.Error()
	}
	return out
}

// scrapeMetrics fetches the operator's /metrics through a port-forward, with
// the bearer token of a ServiceAccount bound to the chart's metrics-reader
// role (the endpoint is served over HTTPS with authn/authz).
func scrapeMetrics(ctx context.Context) (string, error) {
	pf, err := startPortForward(ctx, operatorNS, "svc/"+operatorRelease+"-controller-manager-metrics-service", 8443)
	if err != nil {
		return "", err
	}
	defer pf.Stop()
	tok, err := kubectl(ctx, "-n", operatorNS, "create", "token", "e2e-metrics")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://127.0.0.1:%d/metrics", pf.Port), http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(tok))
	hc := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		// controller-runtime serves a self-signed certificate.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text())
		sb.WriteByte('\n')
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics: HTTP %d: %s", resp.StatusCode, sb.String())
	}
	return sb.String(), nil
}

// reconcileTotal sums controller_runtime_reconcile_total for a controller
// (any result) from a metrics scrape.
func reconcileTotal(metrics, controller string) (float64, error) {
	var sum float64
	found := false
	for line := range strings.SplitSeq(metrics, "\n") {
		if !strings.HasPrefix(line, "controller_runtime_reconcile_total{") ||
			!strings.Contains(line, `controller="`+controller+`"`) {
			continue
		}
		f := strings.Fields(line)
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			return 0, err
		}
		sum += v
		found = true
	}
	if !found {
		return 0, errors.New("no controller_runtime_reconcile_total for " + controller)
	}
	return sum, nil
}

func deploymentReady(ctx context.Context, ns, name string) bool {
	var d appsv1.Deployment
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
		return false
	}
	return d.Status.ReadyReplicas > 0 && d.Status.ReadyReplicas == d.Status.Replicas
}
