package main

import (
	"flag"
	"fmt"
	"os"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/backends/scheduledpod"
	"github.com/wsq2333/milos/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var namespace, metricsAddr, healthAddr string
	var leaderElection bool
	var integrations integrationOptions
	flag.BoolVar(&integrations.kueue, "enable-kueue", false, "Enable Kueue v1beta2 admission integration")
	flag.BoolVar(&integrations.volcano, "enable-volcano", false, "Enable Volcano v1beta1 single-Pod scheduling")
	flag.BoolVar(&integrations.multikueue, "enable-multikueue", false, "Enable MultiKueue with worker-side observation")
	flag.StringVar(&integrations.origin, "multikueue-origin", "multikueue", "Origin label configured on the MultiKueue manager")
	flag.Var(&integrations.workers, "worker", "Read-only worker access as MultiKueueClusterName=kubeconfig-path (repeatable)")
	flag.StringVar(&namespace, "namespace", "default", "Namespace to reconcile and use for leader election")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "127.0.0.1:8080", "Metrics listener (0 disables it)")
	flag.StringVar(&healthAddr, "health-probe-bind-address", "127.0.0.1:8081", "Health probe listener")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Use a lease to elect one active controller")
	logOptions := zap.Options{}
	logOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOptions)))
	if err := run(namespace, metricsAddr, healthAddr, leaderElection, integrations); err != nil {
		ctrl.Log.Error(err, "controller stopped")
		os.Exit(1)
	}
}

func run(namespace, metricsAddr, healthAddr string, leaderElection bool, integrations integrationOptions) error {
	if namespace == "" {
		return fmt.Errorf("namespace must not be empty")
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := milosv1.AddToScheme(scheme); err != nil {
		return err
	}
	workers, err := workerReaders(integrations, scheme)
	if err != nil {
		return err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:  scheme,
		Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
		Metrics: metricsserver.Options{BindAddress: metricsAddr}, HealthProbeBindAddress: healthAddr,
		LeaderElection: leaderElection, LeaderElectionNamespace: namespace, LeaderElectionID: "milos-controller.milos.example.io",
	})
	if err != nil {
		return err
	}
	backend := &backends.Router{
		Native: &nativepod.Backend{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()},
		Scheduled: &scheduledpod.Backend{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(),
			EnableKueue: integrations.kueue, EnableVolcano: integrations.volcano,
			EnableMultiKueue: integrations.multikueue, Workers: workers, Origin: integrations.origin,
		},
	}

	reconciler := &controller.WorkflowRunReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Backend: backend}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
