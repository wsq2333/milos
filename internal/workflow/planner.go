// Package workflow implements deterministic DAG decisions without Kubernetes dependencies.
package workflow

import (
	"fmt"
	"sort"
)

type Phase string

const (
	Waiting    Phase = "Waiting"
	Submitted  Phase = "Submitted"
	Running    Phase = "Running"
	Succeeded  Phase = "Succeeded"
	Failed     Phase = "Failed"
	Skipped    Phase = "Skipped"
	Cancelling Phase = "Cancelling"
	Cancelled  Phase = "Cancelled"
)

type Node struct {
	Name         string
	Dependencies []string
}

type State struct {
	Phase    Phase
	Reserved bool
}

type Plan struct {
	Ready   []string
	Skipped []string
	Phase   Phase
}

func Terminal(p Phase) bool {
	return p == Succeeded || p == Failed || p == Skipped || p == Cancelled
}

// Validate returns a stable topological ordering and rejects malformed DAGs.
func Validate(nodes []Node) ([]string, error) {
	if len(nodes) == 0 || len(nodes) > 128 {
		return nil, fmt.Errorf("DAG must contain between 1 and 128 nodes")
	}
	byName := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		if n.Name == "" {
			return nil, fmt.Errorf("node name is required")
		}
		if _, found := byName[n.Name]; found {
			return nil, fmt.Errorf("duplicate node %q", n.Name)
		}
		byName[n.Name] = n
	}
	indegree := make(map[string]int, len(nodes))
	children := make(map[string][]string)
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, d := range n.Dependencies {
			if _, ok := byName[d]; !ok {
				return nil, fmt.Errorf("node %q references missing dependency %q", n.Name, d)
			}
			if d == n.Name || seen[d] {
				return nil, fmt.Errorf("invalid or duplicate dependency %q on %q", d, n.Name)
			}
			seen[d] = true
			children[d] = append(children[d], n.Name)
		}
		indegree[n.Name] = len(n.Dependencies)
	}
	var order []string
	for len(order) < len(nodes) {
		var ready []string
		for name, degree := range indegree {
			if degree == 0 {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("DAG contains a cycle")
		}
		sort.Strings(ready)
		for _, name := range ready {
			order = append(order, name)
			delete(indegree, name)
			for _, child := range children[name] {
				indegree[child]--
			}
		}
	}
	return order, nil
}

// Compute evaluates only committed results. Reserved creation intents consume slots.
func Compute(nodes []Node, states map[string]State, parallelism int) (Plan, error) {
	order, err := Validate(nodes)
	if err != nil {
		return Plan{}, err
	}
	if parallelism < 1 || parallelism > 128 {
		return Plan{}, fmt.Errorf("parallelism must be between 1 and 128")
	}
	byName := make(map[string]Node, len(nodes))
	phases := make(map[string]Phase, len(nodes))
	active := 0
	for _, n := range nodes {
		byName[n.Name] = n
		s := states[n.Name]
		if s.Phase == "" {
			s.Phase = Waiting
		}
		switch s.Phase {
		case Waiting, Submitted, Running, Succeeded, Failed, Skipped, Cancelling, Cancelled:
		default:
			return Plan{}, fmt.Errorf("unknown phase %q for %q", s.Phase, n.Name)
		}
		phases[n.Name] = s.Phase
		if !Terminal(s.Phase) && (s.Reserved || s.Phase != Waiting) {
			active++
		}
	}
	plan := Plan{Phase: Running}
	for _, name := range order {
		if phases[name] != Waiting || states[name].Reserved {
			continue
		}
		blocked, ready := false, true
		for _, d := range byName[name].Dependencies {
			if phases[d] == Failed || phases[d] == Skipped || phases[d] == Cancelled {
				blocked = true
			}
			if phases[d] != Succeeded {
				ready = false
			}
		}
		if blocked {
			phases[name] = Skipped
			plan.Skipped = append(plan.Skipped, name)
		} else if ready && active < parallelism {
			plan.Ready = append(plan.Ready, name)
			active++
		}
	}
	allTerminal, anyFailure := true, false
	for _, p := range phases {
		allTerminal = allTerminal && Terminal(p)
		anyFailure = anyFailure || p != Succeeded
	}
	if allTerminal {
		plan.Phase = Succeeded
		if anyFailure {
			plan.Phase = Failed
		}
	}
	return plan, nil
}
