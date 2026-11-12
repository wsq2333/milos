// Command milos compiles authoring documents and reads the ResourceProfile catalog.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/authoring"
	"github.com/wsq2333/milos/internal/catalog"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type connection struct{ kubeconfig, context string }
type readerFactory func(connection) (client.Reader, error)

func newReader(o connection) (client.Reader, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = o.kubeconfig
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: o.context}).ClientConfig()
	if err != nil {
		return nil, err
	}
	config.Timeout = 15 * time.Second
	scheme := runtime.NewScheme()
	if err = v1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(config, client.Options{Scheme: scheme})
}
func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, newReader); err != nil {
		if err == flag.ErrHelp {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func runCLI(args []string, out, errOut io.Writer, connect readerFactory) error {
	if len(args) > 0 && args[0] == "profiles" {
		return profiles(args[1:], out, errOut, connect)
	}
	flags := flag.NewFlagSet("milos", flag.ContinueOnError)
	flags.SetOutput(errOut)
	file := flags.String("file", "", "Milos Workflow authoring file")
	ns := flags.String("namespace", "default", "WorkflowRun namespace")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: milos --file workflow.yaml --namespace default; milos profiles list|describe")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if err != nil {
		return err
	}
	run, err := authoring.Compile(b, *ns)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(run)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}
func profiles(args []string, out, errOut io.Writer, connect readerFactory) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: milos profiles list [flags] | describe NAME [flags]")
	}
	action := args[0]
	args = args[1:]
	name := ""
	switch action {
	case "list":
	case "describe":
		if len(args) == 0 {
			return fmt.Errorf("describe requires a profile name")
		}
		name = args[0]
		args = args[1:]
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			return fmt.Errorf("invalid profile name %q", name)
		}
	default:
		return fmt.Errorf("unknown profiles command %q", action)
	}
	flags := flag.NewFlagSet("milos profiles "+action, flag.ContinueOnError)
	flags.SetOutput(errOut)
	ns := flags.String("namespace", "default", "Namespace to read (default: default, independent of kubeconfig namespace)")
	var o connection
	flags.StringVar(&o.kubeconfig, "kubeconfig", "", "Kubeconfig file (otherwise standard loading rules)")
	flags.StringVar(&o.context, "context", "", "Kubeconfig context (otherwise current context)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *ns == "" || len(validation.IsDNS1123Label(*ns)) > 0 {
		return fmt.Errorf("a valid nonempty namespace is required")
	}
	r, err := connect(o)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if action == "list" {
		items, err := catalog.List(ctx, r, *ns)
		if err != nil {
			return err
		}
		return catalog.WriteList(out, *ns, items)
	}
	p, err := catalog.Get(ctx, r, *ns, name)
	if err != nil {
		return err
	}
	return catalog.WriteDetail(out, p)
}
