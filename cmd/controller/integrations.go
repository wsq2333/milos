package main

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type workerFlags []string

func (w *workerFlags) String() string         { return strings.Join(*w, ",") }
func (w *workerFlags) Set(value string) error { *w = append(*w, value); return nil }

type integrationOptions struct {
	kueue, volcano, multikueue bool
	origin                     string
	workers                    workerFlags
}

func workerReaders(options integrationOptions, scheme *runtime.Scheme) (map[string]client.Reader, error) {
	if options.multikueue && !options.kueue {
		return nil, fmt.Errorf("--enable-multikueue requires --enable-kueue")
	}
	if !options.multikueue && len(options.workers) > 0 {
		return nil, fmt.Errorf("--worker requires --enable-multikueue")
	}
	readers := map[string]client.Reader{}
	for _, entry := range options.workers {
		name, path, ok := strings.Cut(entry, "=")
		if !ok || name == "" || path == "" || readers[name] != nil {
			return nil, fmt.Errorf("--worker must be a unique MultiKueueClusterName=kubeconfig-path")
		}
		// Credentials are selected by the operator, never by a WorkflowRun.
		config, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("load worker %q: %w", name, err)
		}
		config.Timeout = 10 * time.Second
		c, err := client.New(config, client.Options{Scheme: scheme})
		if err != nil {
			return nil, err
		}
		readers[name] = c
	}
	if options.multikueue && (len(readers) == 0 || options.origin == "") {
		return nil, fmt.Errorf("MultiKueue requires worker readers and a nonempty origin")
	}
	return readers, nil
}
