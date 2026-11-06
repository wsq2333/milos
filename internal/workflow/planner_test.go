package workflow

import (
	"reflect"
	"testing"
)

var diamond = []Node{{Name: "finish", Dependencies: []string{"left", "right"}}, {Name: "right", Dependencies: []string{"prepare"}}, {Name: "prepare"}, {Name: "left", Dependencies: []string{"prepare"}}}

func TestPlannerDiamond(t *testing.T) {
	cases := []struct {
		name           string
		states         map[string]State
		parallelism    int
		ready, skipped []string
		phase          Phase
	}{
		{"initial", nil, 2, []string{"prepare"}, nil, Running},
		{"parallel", map[string]State{"prepare": {Phase: Succeeded}}, 2, []string{"left", "right"}, nil, Running},
		{"reserved consumes capacity", map[string]State{"prepare": {Phase: Succeeded}, "left": {Phase: Waiting, Reserved: true}}, 1, nil, nil, Running},
		{"failure leaves independent branch", map[string]State{"prepare": {Phase: Succeeded}, "left": {Phase: Failed}}, 2, []string{"right"}, []string{"finish"}, Running},
		{"failure propagation", map[string]State{"prepare": {Phase: Failed}}, 2, nil, []string{"left", "right", "finish"}, Failed},
		{"join", map[string]State{"prepare": {Phase: Succeeded}, "left": {Phase: Succeeded}, "right": {Phase: Succeeded}}, 2, []string{"finish"}, nil, Running},
		{"success", map[string]State{"prepare": {Phase: Succeeded}, "left": {Phase: Succeeded}, "right": {Phase: Succeeded}, "finish": {Phase: Succeeded}}, 2, nil, nil, Succeeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Compute(diamond, tc.states, tc.parallelism)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Ready, tc.ready) || !reflect.DeepEqual(p.Skipped, tc.skipped) || p.Phase != tc.phase {
				t.Fatalf("unexpected plan: %+v", p)
			}
		})
	}
}

func TestRejectInvalidDAG(t *testing.T) {
	cases := [][]Node{nil, {{Name: "a"}, {Name: "a"}}, {{Name: "a", Dependencies: []string{"missing"}}}, {{Name: "a", Dependencies: []string{"a"}}}, {{Name: "a", Dependencies: []string{"b", "b"}}, {Name: "b"}}, {{Name: "a", Dependencies: []string{"b"}}, {Name: "b", Dependencies: []string{"a"}}}}
	for _, nodes := range cases {
		if _, err := Validate(nodes); err == nil {
			t.Fatalf("accepted invalid DAG: %+v", nodes)
		}
	}
	if _, err := Compute(diamond, nil, 0); err == nil {
		t.Fatal("accepted zero parallelism")
	}
	if _, err := Compute(diamond, map[string]State{"prepare": {Phase: "unknown"}}, 1); err == nil {
		t.Fatal("accepted unknown state")
	}
}
