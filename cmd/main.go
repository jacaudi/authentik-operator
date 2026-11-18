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

// The authentik-operator manager runs the controllers that reconcile Authentik
// objects from the authentik.io custom resources.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
	"github.com/jacaudi/authentik-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

// Set at build time with -ldflags -X (go-standards §9.2); the Dockerfile
// passes them as build args.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(authentikiov1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	o := registerFlags()
	flag.Parse()
	if err := run(o); err != nil {
		setupLog.Error(err, "Manager failed")
		os.Exit(1)
	}
}

// options holds the command-line flags.
type options struct {
	metricsAddr, metricsCertPath, metricsCertName, metricsCertKey string
	webhookCertPath, webhookCertName, webhookCertKey              string
	webhookPort                                                   int
	probeAddr                                                     string
	enableLeaderElection, secureMetrics, enableHTTP2              bool
	defaultConn                                                   connection.DefaultOptions
	adoptionNamespaces, identityNamespaces                        string
	zap                                                           zap.Options
}

// registerFlags defines the flags on flag.CommandLine; main parses them.
func registerFlags() *options {
	o := &options{zap: zap.Options{Development: true}}
	flag.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&o.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&o.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&o.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&o.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&o.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.IntVar(&o.webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	flag.StringVar(&o.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&o.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&o.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&o.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&o.defaultConn.URL, "authentik-url", os.Getenv(connection.EnvURL),
		"Default Authentik base URL for objects without spec.authentikRef (env "+connection.EnvURL+"). "+
			"Empty disables the default connection.")
	flag.StringVar(&o.defaultConn.ExternalURL, "authentik-external-url", os.Getenv(connection.EnvExternalURL),
		"Browser-facing base URL of the default Authentik, used in rendered credential Secrets "+
			"(env "+connection.EnvExternalURL+"). Defaults to --authentik-url.")
	flag.StringVar(&o.defaultConn.TokenFile, "authentik-token-file", "",
		"File holding the default connection's API token. If unset, the "+connection.EnvToken+" env var is used. "+
			"Read once at startup: rotating the default token requires a restart.")
	flag.StringVar(&o.defaultConn.CAFile, "authentik-ca-file", "",
		"Optional PEM CA bundle for the default connection, trusted in addition to system roots.")
	flag.BoolVar(&o.defaultConn.InsecureSkipVerify, "authentik-insecure-skip-verify", false,
		"Disable TLS verification for the default connection. Testing only.")
	flag.StringVar(&o.adoptionNamespaces, "default-connection-adoption-namespace-selector", "",
		"Label selector (e.g. kubernetes.io/metadata.name=team-a) for the namespaces whose resources may use "+
			"managementPolicy Adopt, AdoptOrCreate or ReadOnly on the default connection. Empty denies every "+
			"namespace. Resources with spec.authentikRef are not affected.")
	flag.StringVar(&o.identityNamespaces, "default-connection-identity-namespace-selector", "",
		"Label selector for the namespaces whose AuthentikGroup and AuthentikUser resources the operator "+
			"reconciles on the default connection, under every managementPolicy. Empty denies every namespace. "+
			"Resources with spec.authentikRef are not affected.")
	o.zap.BindFlags(flag.CommandLine)
	return o
}

// run builds the manager, registers the controllers and runs until a
// termination signal.
func run(o *options) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&o.zap)))
	setupLog.Info("Starting manager", "version", version, "commit", commit, "date", date)

	var tlsOpts []func(*tls.Config)

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !o.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
		Port:    o.webhookPort,
	}

	if o.webhookCertPath != "" {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", o.webhookCertPath, "webhook-cert-name", o.webhookCertName, "webhook-cert-key", o.webhookCertKey)

		webhookServerOptions.CertDir = o.webhookCertPath
		webhookServerOptions.CertName = o.webhookCertName
		webhookServerOptions.KeyName = o.webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   o.metricsAddr,
		SecureServing: o.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if o.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if o.metricsCertPath != "" {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", o.metricsCertPath, "metrics-cert-name", o.metricsCertName, "metrics-cert-key", o.metricsCertKey)

		metricsServerOptions.CertDir = o.metricsCertPath
		metricsServerOptions.CertName = o.metricsCertName
		metricsServerOptions.KeyName = o.metricsCertKey
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: o.probeAddr,
		LeaderElection:         o.enableLeaderElection,
		LeaderElectionID:       "a7f80c08.authentik.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	defaultCfg, err := connection.DefaultConfig(o.defaultConn, os.Getenv)
	if err != nil {
		return fmt.Errorf("default Authentik connection: %w", err)
	}
	if defaultCfg != nil {
		setupLog.Info("Configured default Authentik connection", "url", defaultCfg.URL)
	} else {
		setupLog.Info("No default Authentik connection; objects must set spec.authentikRef")
	}
	connections := &connection.Resolver{Reader: mgr.GetAPIReader(), Default: defaultCfg,
		DefaultExternalURL: o.defaultConn.ExternalURL}

	adoptionNamespaces, err := controller.ParseNamespaceSelector(o.adoptionNamespaces)
	if err != nil {
		return fmt.Errorf("--default-connection-adoption-namespace-selector: %w", err)
	}
	identityNamespaces, err := controller.ParseNamespaceSelector(o.identityNamespaces)
	if err != nil {
		return fmt.Errorf("--default-connection-identity-namespace-selector: %w", err)
	}
	if err := setupControllers(mgr, connections, adoptionNamespaces, identityNamespaces); err != nil {
		return err
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("set up ready check: %w", err)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("run manager: %w", err)
	}
	return nil
}

// setupControllers registers every reconciler with the manager.
func setupControllers(mgr ctrl.Manager, connections *connection.Resolver,
	adoptionNamespaces, identityNamespaces labels.Selector) error {
	controllers := []struct {
		name       string
		reconciler interface{ SetupWithManager(ctrl.Manager) error }
	}{
		{"authentikdeployment", &controller.AuthentikDeploymentReconciler{
			Client:      mgr.GetClient(),
			Scheme:      mgr.GetScheme(),
			Connections: connections,
		}},
		{"authentikapplication", &controller.AuthentikApplicationReconciler{
			Client:                    mgr.GetClient(),
			Scheme:                    mgr.GetScheme(),
			Connections:               connections,
			Recorder:                  mgr.GetEventRecorder("authentikapplication"),
			APIReader:                 mgr.GetAPIReader(),
			AdoptionNamespaceSelector: adoptionNamespaces,
		}},
		{"authentikuser", &controller.AuthentikUserReconciler{
			Client:                    mgr.GetClient(),
			Scheme:                    mgr.GetScheme(),
			Connections:               connections,
			Recorder:                  mgr.GetEventRecorder("authentikuser"),
			APIReader:                 mgr.GetAPIReader(),
			AdoptionNamespaceSelector: adoptionNamespaces,
			IdentityNamespaceSelector: identityNamespaces,
		}},
		{"authentikgroup", &controller.AuthentikGroupReconciler{
			Client:                    mgr.GetClient(),
			Scheme:                    mgr.GetScheme(),
			Connections:               connections,
			Recorder:                  mgr.GetEventRecorder("authentikgroup"),
			APIReader:                 mgr.GetAPIReader(),
			AdoptionNamespaceSelector: adoptionNamespaces,
			IdentityNamespaceSelector: identityNamespaces,
		}},
		{"authentikoauthprovider", &controller.AuthentikOAuthProviderReconciler{
			Client:                    mgr.GetClient(),
			Scheme:                    mgr.GetScheme(),
			Connections:               connections,
			Recorder:                  mgr.GetEventRecorder("authentikoauthprovider"),
			APIReader:                 mgr.GetAPIReader(),
			AdoptionNamespaceSelector: adoptionNamespaces,
		}},
		{"authentikcredentialstemplate", &controller.AuthentikCredentialsTemplateReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}},
	}
	for _, c := range controllers {
		if err := c.reconciler.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("create controller %s: %w", c.name, err)
		}
	}
	return nil
}
