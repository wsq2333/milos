# Node timeouts and waiting diagnostics

Milos supports a workflow deadline, node total timeouts and node runtime timeouts.
These are wall-clock limits for finite DAG nodes, not CPU usage or tenant budgets.
The first release does not implement separate queue, scheduling or startup limits,
automatic retries, or application-progress/heartbeat detection.

## Configuration

```yaml
spec:
  executionPolicy:
    parallelism: 2
    maxRetries: 0
    workflowTimeoutSeconds: 3600
    nodeTimeouts:
      totalSeconds: 1800
      runSeconds: 1200
  definition:
    nodes:
      - name: train
        timeouts:
          runSeconds: 600
        container:
          image: YOUR_TRAINING_IMAGE
```

This fragment must be combined with a complete WorkflowRun. See the runnable
[timeout example](../config/samples/node-timeouts.yaml), which intentionally times
out one node, skips its successor and allows an independent node to complete.

- `executionPolicy.workflowTimeoutSeconds` is measured from WorkflowRun creation.
  It includes dependency waits, queueing, execution and controller downtime.
- `executionPolicy.nodeTimeouts` supplies defaults. Each node's `timeouts` overrides
  individual fields. An absent field inherits its default; explicit `0` disables
  that limit. If neither level supplies a value, the limit is disabled.
- `totalSeconds` starts at `status.nodes.<name>.intentRecordedAt`, committed together
  with the execution reservation before creating resources. It includes creation,
  admission, dispatch, scheduling, startup and execution. Waiting for dependencies
  or an available Milos parallelism slot does not consume this budget.
- `runSeconds` starts at the identity-verified `main` container's `startedAt`, not
  Pod creation, admission, Pod phase Running, or readiness. For MultiKueue, only
  the worker Pod supplies this evidence. A terminated container's timestamps also
  work when Milos never observed its Running state. Missing timing evidence leaves
  the runtime timer unarmed; use a total timeout to bound such cases.
- Values are integer seconds in `0..604800`. Defaults and overrides are immutable
  execution inputs. A timeout cannot be extended after submission.

The earlier enabled node deadline wins; a tie uses `NodeDeadlineExceeded` rather
than `RunDeadlineExceeded`. The workflow-wide stop path takes precedence if its
deadline or cancellation is observed before a node result is committed.
Active runs use a five-second fallback poll plus Pod/WorkflowRun events. API delays,
controller outages and cleanup can extend the time until execution actually stops;
these limits are not hard real-time process termination guarantees. Runtime budgets
assume synchronized controller and worker clocks.

## Durable stop and completion semantics

The controller persists a node's `stopReason`, `stopRequestedAt`, phase `Cancelling`
and stage `Stopping` before calling `ExecutionBackend.Stop`. Cleanup uses the same
resource identities and UID checks as workflow cancellation. Only after confirmed
cleanup does the node become `Failed`, with `failureClass: Timeout`. Descendants
are skipped; independent branches continue. A stopping node keeps its parallelism
slot until cleanup completes. No automatic retry or replacement execution occurs.

Node timeout decisions survive restarts. A later success observation cannot reverse
a persisted stop decision. If a fresh verified terminal observation arrives before
a stop decision, a container finish timestamp at or before the node deadline is
accepted even when the controller polls late. A finish after the deadline triggers
timeout cleanup. When a terminal observation lacks a finish timestamp, an already
expired node budget wins. Already committed terminal results never regress.

Observation errors do not disable known deadlines. Cleanup errors or an unreachable
worker retain `Cancelling` and the cleanup diagnostic; they never imply successful
cleanup. Milos does not strip upstream finalizers. Pod API disappearance is not proof
that a process on an unreachable machine has stopped.

New optional spec fields are omitted when unset, preserving existing execution
hashes. Older stored execution intents without `intentRecordedAt` use WorkflowRun
creation time as a conservative fallback. Existing immutable runs cannot acquire
new timeout settings through an update. Upgrade the CRD before the controller to
preserve the new policy and status fields.

## Waiting visibility

Planner `phase` remains stable; `stage` provides a more specific observation:

| Stage | Meaning |
| --- | --- |
| `WaitingForDependencies` | A predecessor has not succeeded |
| `WaitingForParallelism` | Dependencies succeeded; no Milos execution slot is available |
| `Creating` | Execution is reserved; backend resources are being established |
| `WaitingForAdmission` | Admission or scheduling gates still block scheduling |
| `WaitingForWorker` | MultiKueue has not yielded a verifiable worker execution |
| `WaitingForScheduling` | Pod is not bound to a node |
| `WaitingForStartup` | Pod is bound; main container start is not observed |
| `Running` | Main container start is observed |
| `ObservationUnavailable` | Missing resources, inaccessible APIs or unverifiable state |
| `Stopping` | A stop decision is persisted; cleanup is pending |
| `Finished` | A node result or skip is committed |

`reason` retains structured diagnostics such as `Unschedulable`,
`ImagePullBackOff`, `WaitingForAdmission`, `AdmissionEvicted` or `WorkerUnavailable`.
`message` contains up to 1024 characters of diagnostic context, not application
logs. `stageSince` records the first persisted local observation of a stage, not
an authoritative scheduler transition time. Repeated observations do not reset it.
Inspect these fields with an explicitly selected cluster and namespace:

```sh
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE get workflowrun YOUR_RUN \
  -o jsonpath='{.status.nodes}'
```

No waiting stage by itself causes failure. A Pod can wait indefinitely when both
workflow and relevant node limits are disabled. In particular, `AdmissionEvicted`
does not imply automatic re-admission or Milos retry. Kueue remains responsible for
admission, Volcano for placement, and Milos for workflow timeout/cleanup policy.

## Validation scope

Unit tests use an injected clock and reconstruct the controller between reconciles.
They cover budget inheritance and disabling, dependency/queue exclusion, main
container timestamps, boundary races, status conflicts, cleanup slot retention,
independent branches, observation outages and remote timing sources. Isolated API
tests exercise the generated CRD, immutable policies, zero-versus-absent values,
status persistence and the runtime timeout cleanup path. They do not run a real
scheduler, kubelet or MultiKueue controller. Live acceptance of these new features
remains pending explicit deployment authorization.
