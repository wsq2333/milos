package authoring

import (
	"os"
	"strings"
	"testing"
)

func TestCompile(t *testing.T) {
	data, err := os.ReadFile("../../examples/milos/profile-workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Compile(data, "default")
	if err != nil {
		t.Fatal(err)
	}
	if r.Spec.Definition.Nodes[0].Requirements.ProfileRef.Name != "h20-exclusive-v1" {
		t.Fatal("lost profile")
	}
	cases := map[string]string{
		"unknown":                strings.Replace(string(data), "requirements:", "requrements:", 1),
		"priority not supported": strings.Replace(string(data), "requirements:", "scheduling: {priorityClassRef: urgent}\n              requirements:", 1),
		"duplicate":              strings.Replace(string(data), "name: hardware-example", "name: hardware-example\n  name: duplicate", 1),
		"multi document":         string(data) + "\n---\n{}\n",
		"failFast":               strings.Replace(string(data), "failFast: false", "failFast: true", 1),
		"missing template":       strings.Replace(string(data), "template: evaluate", "template: missing", 1),
		"cycle":                  strings.Replace(string(data), "template: evaluate", "dependencies: [evaluate]\n            template: evaluate", 1),
		"empty profile":          strings.Replace(string(data), "name: h20-exclusive-v1", "name: ''", 1),
		"namespace":              strings.Replace(string(data), "name: hardware-example", "name: hardware-example\n  namespace: other", 1),
		"parameter":              strings.Replace(string(data), "busybox:1.37.0", "'{{workflow.parameters.image}}'", 1),
		"alias":                  strings.Replace(string(data), "name: hardware-example", "name: &name hardware-example", 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile([]byte(input), "default"); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	legacy := strings.Replace(string(data), "            milos:\n              requirements:\n                profileRef:\n                  name: h20-exclusive-v1\n", "", 1)
	r, err = Compile([]byte(legacy), "default")
	if err != nil {
		t.Fatal(err)
	}
	if r.Spec.Definition.Nodes[0].Requirements != nil {
		t.Fatal("legacy changed")
	}
}
