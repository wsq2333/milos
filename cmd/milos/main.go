// Command milos compiles authoring documents; submission is explicit via kubectl.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wsq2333/milos/internal/authoring"
	"sigs.k8s.io/yaml"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("file", "", "Milos Workflow authoring file")
	ns := flag.String("namespace", "default", "WorkflowRun namespace")
	flag.Parse()
	if *file == "" || flag.NArg() != 0 {
		return fmt.Errorf("usage: milos --file workflow.yaml --namespace default")
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
	out, err := yaml.Marshal(run)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}
