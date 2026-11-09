# Development and local execution

Milos currently provides an experimental Go controller for native `WorkflowRun` resources. The API group `milos.example.io` is provisional. Design documents describe the broader target; the code and generated CRD define the current input contract.

## Prerequisites and dependencies

Use Go 1.26.0 or newer (the initial implementation was tested with Go 1.26.5). Runtime dependencies are pinned in `go.mod` and `go.sum`: controller-runtime v0.24.0 and Kubernetes libraries v0.36.0. The Makefile pins controller-gen v0.21.0 independently from runtime dependencies.

```sh
go mod download
make build
make test
make vet
make generate manifests
```

`make build` produces `bin/milos-controller`. `make test` runs unit and fake-client tests with the race detector. `make generate` updates deep-copy methods; `make manifests` updates the CRD and ClusterRole. Commit generated files alongside API changes.

## Implemented boundaries

| Layer | Current responsibility |
| --- | --- |
| `api/v1alpha1` | WorkflowRun spec/status, schema defaults, immutable execution inputs, irreversible cancellation |
| `internal/workflow` | Kubernetes-independent DAG validation, deterministic readiness, parallelism, failure propagation |
| `internal/controller` | Input validation, intent/result persistence, reconciliation, cancellation, deadline and finalizer cleanup |
| `internal/execution` | Ensure, Observe, Stop and Collect lifecycle contract |
| `internal/pod` | Stable identity, owner references, execution digest and UID verification |
| `internal/backends/nativepod` | Idempotent local Pod creation, observation and deletion |
| `internal/backends/scheduledpod` | Optional Kueue, MultiKueue and Volcano lifecycle adapters |
| `internal/backends` | Configuration-based backend routing |
| `cmd/controller` | Scheme, manager, namespace scope, leader election, health checks and metrics |

Each node runs one application container named `main`, with `restartPolicy: Never` and the configured scheduler (defaulting to `default-scheduler`). A workflow supports 1–128 nodes and parallelism of 1–128. Requests and limits, including device-plugin extended resources such as `nvidia.com/gpu`, are passed through; Milos does not discover devices or choose GPU models. Only `emptyDir` and PVC volumes are currently accepted. Service account token automount is disabled on execution Pods.

Execution inputs are immutable. The default admission is `None`, placement is `Local`, and backend/contract are `native-pod`/`v1`. Optional Kueue, MultiKueue and Volcano modes are described in [scheduling integrations](scheduling-integrations.md). Unsupported combinations and nonzero `maxRetries` are rejected. A positive workflow timeout is measured from WorkflowRun creation, including waiting time; zero disables it. Node total/runtime budgets inherit workflow defaults and allow per-node overrides; see [timeouts and waiting diagnostics](timeouts.md). Queue references and worker membership are explicit immutable configuration; backend details stay outside the DAG planner.

CRD validation checks the schema, allowed enum values and immutability. The controller additionally checks DAG structure, supported volume sources, resource bounds and the 256 KiB execution definition limit. A rejected definition receives an `Accepted=False` condition without starting Pods. This prototype has no admission webhook; Kubernetes performs final validation of generated Pod fields at creation, and admission failures are currently reported through controller logs and reconcile errors. When applying native resources, use strict field validation to reject unknown fields instead of relying on default API pruning.

## Execution and recovery

1. Add the cleanup finalizer and initialize durable node state.
2. Persist a stable creation intent before submitting a Pod.
3. Observe resources using uncached API reads and verify their owner, digest and UID.
4. Persist observed node results before planning successors.
5. Recompute readiness from stored results and reserve available parallelism slots.

A Pod name derives from WorkflowRun UID, node name, attempt and dispatch generation. An ambiguous Create response is resolved by reading that same name. A known Pod UID that disappears is recorded as `ResourceMissing`; it does not trigger automatic retry. Unknown observations consume their existing slot and block dependent nodes until resolved or cancelled. Terminal node results never regress because a Pod is later removed.

Finished Pods are retained while the WorkflowRun remains active or completed. `Collect` requires acknowledgement of a persisted terminal result, but automated result garbage collection is not yet called by the reconciler. Cancellation and WorkflowRun deletion use `Stop`; deletion waits for ordinary Pod API disappearance before releasing the workflow finalizer. Completed workflows retain their outcome if a later cancellation request arrives; delete the WorkflowRun to clean up retained Pods.

Cancellation is cooperative. A create request already in flight may race with cancellation and is cleaned up on subsequent reconciliation. Pod disappearance is not proof that a process on an unreachable node has stopped. The prototype does not guarantee exactly-once application side effects, protect against arbitrary external deletion before a Pod UID is committed, or provide distributed execution fencing.

## Run locally against a development cluster

The following commands modify the selected kubeconfig cluster. Use a development namespace and context. CRD installation requires cluster-level permission; the running controller is scoped to one namespace.

```sh
kubectl apply --server-side -f config/crd/bases/
make run NAMESPACE=default
```

Run the next commands in another terminal:

```sh
kubectl apply --validate=strict -n default -f config/samples/diamond.yaml
kubectl get workflowruns,pods -n default
kubectl get workflowrun diamond -n default -o yaml
kubectl patch workflowrun diamond -n default --type=merge \
  -p '{"spec":{"desiredState":"Cancelled"}}'
kubectl delete workflowrun diamond -n default
```

The sample uses finite BusyBox commands to demonstrate dependencies; it is not an ML benchmark. `examples/diamond.yaml` is a separate Argo input example for the future importer and cannot be submitted to this controller.

The local process uses kubeconfig credentials. `config/rbac/` contains a generated ClusterRole, ServiceAccount and namespace-scoped RoleBinding for a future in-cluster deployment. The binding and ServiceAccount default to `default`; adjust both when deploying elsewhere. Applying them does not change the credentials of a locally running process. A Deployment, Dockerfile and optional Kustomize overlays are available under [deploy/](../deploy/README.md). They have not been deployed; image publication and Helm packaging remain pending.

The controller enables leader election by default using a lease in the selected namespace. Leave it enabled when running multiple replicas. Metrics and health endpoints bind to `127.0.0.1:8080` and `127.0.0.1:8081`; configure the flags explicitly when packaging the controller in a container.

## API integration tests

```sh
make test-integration
```

This starts an isolated API server and etcd using envtest, never the current kubeconfig. Kubernetes 1.36.0 binaries are downloaded into the operating system temporary directory under `milos-envtest` unless `KUBEBUILDER_ASSETS` points to existing binaries. Network access and permission to start local processes/listeners are required.

Tests cover CRD installation, immutable inputs, actual status-subresource writes and conflicts, and native Pod reconciliation against an API server. Unit tests cover deterministic planning, independent failure branches, parallelism reservations, create recovery, UID conflicts, missing resources, guarded collection, cancellation before handle persistence, workflow deadlines, and finalizer cleanup.

Envtest has no scheduler, kubelet, garbage collector, or workload containers. Test code supplies Pod terminal states. Real container execution, scheduling behavior, unreachable-node handling, GPU resources, RBAC deployment and multi-controller failover still require cluster E2E validation.

## Deferred features

Argo parsing and parameter binding, `milosctl`, application retries, automatic Pod result collection, Helm packaging, real scheduler acceptance of the optional integrations, resource profiling and ML allocation policies are not implemented. Do not add placeholder success implementations for these capabilities. Keep backend lifecycle logic separate from workflow decisions as they are introduced.

## Remote development preference

Prefer the user-designated SSH developer environment for testing. Use an isolated temporary checkout and explicit kubeconfig/context. Existing shared cluster resources must remain unchanged. The optional live test and its required cluster-scoped setup are documented in [scheduling integrations](scheduling-integrations.md); envtest requires no shared cluster changes. Keep environment addresses and credentials outside the public repository.
