# Milos architecture and execution protocol

See [planned Milos workflow extensions](milos-extensions.md) for the proposed authoring format, ResourceProfile, task requirements and durable execution decisions. A narrower [native profile path](profile-authoring.md) is implemented; the remaining extensions and profile-aware scheduler integrations are future work.

See [DRA-backed profiles and hardware-aware placement](resource-pools.md) for the planned device allocation path, hierarchical catalog views and H1–H5 roadmap. DRA integration is not implemented; standalone ResourcePool/NodeHardware CRDs are deferred.

The design includes [Kueue TAS and worker-side TAS with MultiKueue](resource-pools.md#kueue-tas-and-multikueue), with Volcano topology as an alternative. The [network model](resource-pools.md#network-resources) separates connectivity, device allocation and performance; locality does not guarantee bandwidth.

Status: target design with a native Pod core and experimental optional scheduling adapters. See [development status](development.md) and [scheduling integrations](scheduling-integrations.md) for the implemented contract and validation boundaries. Interfaces and examples below also include planned capabilities.

Milos targets machine learning training and inference on heterogeneous CPU/GPU Kubernetes clusters, with the long-term goal of improving workflow scheduling and resource allocation. This document defines the initial execution core supporting that goal. API fields, scale limits, and failure-handling details still require prototype validation.

The default path uses native Pods with default-scheduler in one cluster. Experimental Kueue, MultiKueue and Volcano adapters are optional dependencies; planned retry and fencing semantics below are not all implemented. See the [execution abstraction](execution-abstraction.md) for interfaces, configuration, and staged delivery.

## 1. Architecture decisions

| Decision | Rationale and boundary |
| --- | --- |
| A dedicated DAG planner | Level-triggered convergence; static Pod DAGs only initially |
| Argo YAML as input | Reuse the authoring format without promising complete execution semantics or Argo CLI/UI compatibility |
| A dedicated WorkflowRun CRD | Separate resources from Argo Workflow and avoid competing controllers |
| Active state in the Kubernetes API | PostgreSQL-backed business admission, audit transactions, and shared business-table contracts are outside scope |
| One Pod per node attempt | Avoid overlapping Job, Argo, and Milos retry mechanisms |
| Finalize from Pod status | No output-parameter collection, executor, result bridge, or application callbacks in the first release |
| Separate DAG progression, admission, and node scheduling | Preserve responsibility boundaries across Milos, Kueue/MultiKueue, and Volcano |
| Planned DRA-backed profiles | ResourceProfile references approved device templates; DRA owns inventory/allocation; pool hierarchy is a read-only view |

PostgreSQL may later provide archival storage and queries; it is not an execution dependency. Do not store logs, metrics, or unbounded history in WorkflowRun. Build a small execution core first, then choose extensions based on ML workloads and measured scale.

## 2. State authority and write ownership

| Object or data | Authoritative location | Writer |
| --- | --- | --- |
| Immutable definition, parameters, and scheduling snapshot | WorkflowRun.spec | Importer at creation; API validation prevents mutation |
| Cancellation intent | WorkflowRun.spec.desiredState | Authorized user or CLI; one-way Running → Cancelled |
| Node results, current attempt, and workflow state | WorkflowRun.status | Milos controller |
| Actual execution facts | Pod.status in the execution cluster | Kubernetes; Milos reads only |
| Manager projection of remote facts | Manager Pod.status | MultiKueue; Milos reads only |
| Admission and dispatch | Workload and other Kueue objects | Kueue/MultiKueue |
| Logs and application files | External logging or storage systems | Applications and supporting components |

WorkflowRun does not mirror the entire Pod. Store only the results, resource identities, timestamps, concise reasons, and cleanup progress needed for progression. Manager and worker Pods have different UIDs and must not be confused. A cross-cluster ownerReference is not a remote cleanup protocol.

The planned hardware catalog reads Nodes, driver-owned ResourceSlices and trusted supplemental facts. DeviceClass/ResourceClaimTemplate policy stays platform-owned; Kubernetes owns claim creation and allocation. ResourceProfiles remain the user-facing contract. Hierarchical views do not allocate devices or create quotas. Persist bounded claim request content and policy references in WorkflowRun, then record allocation separately. The [DRA lifecycle contract](resource-pools.md#resolution-enforcement-and-restart-safety) covers policy immutability, claim identity, restart and cleanup.

## 3. Object model

Use a namespaced `WorkflowRun` with a separate status subresource. The API group `milos.example.io` is a placeholder that must be replaced with the project's chosen domain before release. This is a conceptual structure, not an installable CRD:

```yaml
apiVersion: milos.example.io/v1alpha1
kind: WorkflowRun
metadata:
  name: diamond
spec:
  definition: {}                # Normalized DAG, bound templates, and parameters
  definitionDigest: "sha256:..."
  compilerVersion: "..."
  compatibilityProfile: "argo-pod-dag-v1"
  execution:                   # Immutable snapshot; the only initial combination
    backend: native-pod
    contractVersion: v1
    admission:
      provider: None
    placement:
      provider: Local
    scheduler:
      name: default-scheduler
  executionPolicy:
    maxRetries: 0              # Retries in addition to the initial attempt
    retryBackoffSeconds: 10
    workflowTimeoutSeconds: 3600
    parallelism: 4
  desiredState: Running
status:
  observedGeneration: 1
  phase: Running
  nodes:
    prepare:
      phase: Submitted
      attempt: 0
      dispatchGeneration: 0
      executionIntentRecorded: true
      executionHandle:
        backend: native-pod
        contractVersion: v1
        resources:
          - clusterID: "local-cluster-id"
            apiVersion: v1
            kind: Pod
            namespace: default
            name: "milos-..."
            uid: "..."
            role: ExecutionPod
      observation: Available
      cleanup: Pending
  conditions: []
```

The `definition`, scheduling and policy snapshots, and compiler metadata are immutable after creation; only cancellation intent may change. Enforce validation through a structural CRD schema, CEL, or a webhook. Direct WorkflowRun creation must receive full DAG and field validation, rather than relying only on the CLI.

`observedGeneration` identifies the processed spec version, not the freshness of remote state. Original YAML may be retained as optional provenance, but execution depends only on the normalized definition. Avoid duplicating large source YAML documents inside the CR initially.

Node IDs derive from task identity, not template names. Multiple tasks can invoke one template. The first version requires only one entrypoint scope; extend identity when nesting is introduced.

## 4. Node and workflow semantics

Node phases are `Waiting`, `Submitted`, `Running`, `Succeeded`, `Failed`, `Skipped`, `Cancelling`, and `Cancelled`.

- `Waiting`: dependencies are incomplete, or the persisted `nextRetryAt` for an application retry has not arrived.
- `Submitted`: a Pod is associated with the node; it may be waiting for quota, a node, or the first execution observation.
- `Running`: the Pod was last confirmed running; observation availability is tracked separately.
- `Succeeded`: the current execution Pod succeeded and the result is persisted. Dependencies recognize only this committed state.
- `Failed`: failure is confirmed and no further application retries are allowed.
- `Skipped`: at least one predecessor finally failed or was skipped because of a dependency failure.
- `Cancelled`: the task never ran, or execution cleanup has reached its confirmation criteria.

Observations may skip intermediate phases. A first read may already show Succeeded without a prior observation of Running. Pending does not indicate an application failure. Interpret Pod phases and container diagnostics according to the [Kubernetes Pod lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/).

Proposed workflow phases are `Pending`, `Running`, `Cancelling`, `Succeeded`, `Failed`, and `Cancelled`. A timeout ends as `Failed/DeadlineExceeded`, using `Cancelling` while stopping execution. Success requires all nodes to succeed. On failure, independent branches continue and descendants blocked by failed dependencies are skipped; the workflow is finalized as failed after all branches converge.

When observation is uncertain, retain the last confirmed phase and set `ObservationAvailable=False` with reasons such as `RemoteUnavailable` or `ResourceMissing`. Do not invent a terminal outcome. Unknown causes do not automatically consume application retries.

### Application retries

Attempts start at 0, and Pods use `restartPolicy: Never`. Retry only failures with matching identity that are confirmed to result from a nonzero application-container exit and are not part of eviction or cancellation. Other infrastructure failures enter diagnosis or recovery first. Suspend automatic re-execution if the policy cannot classify the situation safely.

Persist the old attempt result, the next retry time, and the new execution intent first. Confirm the old execution has stopped and that no remote execution remains unaccounted for before creating the next attempt. The initial policy uses fixed backoff and bounded retries; it does not interpret Argo `retryStrategy`. To bound CR size, initially allow at most 3 retries and retain bounded attempt summaries.

Quota eviction and redispatch after worker disconnection use a separate protocol from application retries. `dispatchGeneration` is reserved for execution replacement explicitly initiated by Milos; it does not claim to identify every internal MultiKueue dispatch. If upstream cannot provide sufficient dispatch identity, the first multi-cluster prototype must not enable Milos-driven automatic infrastructure redispatch.

## 5. Level-triggered reconciliation

The planner is a pure function. It does not access Kubernetes, read an implicit clock, or use randomness:

```go
func Plan(spec WorkflowSpec, state ObservedState, now time.Time) PlanResult
```

`PlanResult` describes desired state changes, actions with preconditions, and the next check time. It is not a transaction log. Recompute it each cycle rather than replaying an outdated in-memory action list.

Proposed order:

1. Read WorkflowRun and obtain normalized facts through ExecutionBackend.Observe. Initially, read associated Pods only; future backends may also read Workload and cluster connectivity state.
2. Prioritize deletionTimestamp, cancellation intent, or timeout. Persist the WorkflowRun cleanup finalizer before creating any Pod.
3. Validate facts against Workflow UID, node ID, attempt, Pod UID, and definition digest. Events from old executions may only inform cleanup of their resources.
4. Persist node results confirmed in this cycle. On write conflicts, reread and recompute rather than continuing the previous plan.
5. Compute eligible nodes from committed node state, select them in a stable order, and respect the parallelism budget.
6. Persist execution intent, including attempt, stable name, and budget reservation. Call Create only after this write succeeds.
7. Call ExecutionBackend.Ensure. The native backend uses EnsurePod with a stable name; if creation has an uncertain outcome, query that name instead of generating a new one.
8. Persist the actual UID, then reconcile again for remaining actions and cleanup.

The `parallelism` budget includes reserved creation intents and Submitted, Running, and Cancelling nodes, preventing unbounded accumulation of Pods waiting for quota. Nodes waiting for an application retry do not occupy a slot once the previous execution is confirmed stopped. Release a failed creation intent's slot only after establishing that no actual Pod exists.

Initially, watch only WorkflowRun and owned Pods. Watch mapped Workloads or other resources only when the corresponding backend is enabled. Recover list/watch on startup and use a jittered `RequeueAfter` for timeouts and missed notifications on active workflows. Each cycle need not scan the entire cluster.

Use resourceVersion conditional updates, leader election, and stable identities together for concurrency control. Rereading before an action reduces cancellation races, but WorkflowRun updates and Pod creation have no cross-object transaction. A Pod created concurrently with cancellation may briefly appear and must be detected and removed in the next cycle. Cancellation does not promise to stop all creation instantaneously.

## 6. Idempotency and crash windows

The proposed execution key is `(Workflow UID, node ID, attempt, dispatchGeneration)`. Derive Pod names from a deterministic hash of this key plus a short prefix within Kubernetes naming limits. Labels and annotations record ownership and the normalized definition digest.

On `AlreadyExists`, verify the owner, execution key, digest, and any recorded UID. A mismatch produces `IdentityConflict`: do not adopt or delete the object, or generate a random replacement name. The digest covers Milos-generated input, not a byte-for-byte comparison of the entire Pod spec after webhook mutation. Validate mutations that affect execution semantics separately.

| Interruption point | Recovery |
| --- | --- |
| Intent persisted, Pod not yet created | Create using the same execution key |
| Create succeeded, UID not persisted | Get the same Pod name, verify ownership, and record its UID |
| Create timed out with an uncertain result | Query the same name; wait if the query is unavailable |
| Pod succeeded, node result not persisted | Retain the terminal Pod and record the result after recovery; do not release successors early |
| Node result persisted, successor not created | Recompute eligible nodes from persisted results |
| Cleanup completed, cleanup status not persisted | Confirm absence again, then record completion |
| An old Pod event arrives late | Ignore it for the current attempt if execution identity does not match |

A Pod disappearing after its UID was recorded is not equivalent to never having been created. Set `ResourceMissing` and let the selected backend investigate. Future queue and multi-cluster backends must also inspect Workload and dispatch information. Without reliable execution evidence, retain an unknown observation rather than recreating the same execution key.

Terminal state never regresses. Retry only nodes that have not been finalized as failed, and keep previous attempt results separately. Once a workflow's terminal state is committed, editing its spec cannot reopen execution; another submission must produce a new Workflow UID.

## 7. Future integrations: Kueue, MultiKueue, and Volcano

The initial NativePodBackend neither creates nor watches these components' objects and requires none of their CRDs, permissions, or installation settings. Introduce separate execution backends and scheduling policies as needed.

### Single cluster

Milos maps queue selection to the Pod label `kueue.x-k8s.io/queue-name`. Platform deployment enables Kueue Pod integration and its webhook. Milos does not create a competing Workload or write admission conditions. [Plain Pod integration](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)

Validate queue names, schedulerName, and allowed resource capabilities against deployment configuration, then snapshot them at submission. No additional ExecutionProfile CRD is needed initially. An enabled admission path must fail closed under its deployment contract; a disabled integration or ineffective webhook must not silently turn it into a plain Pod that bypasses quota.

Start Volcano validation with individual Pods, without creating Volcano Jobs or enabling another application-retry mechanism. Multi-Pod gangs require a separate design for PodGroup, admission sets, and lifecycle. Upstream examples also distinguish schedulerName, PodGroup, and Queue settings. [Volcano tutorial](https://volcano.sh/docs/gettingstarted/tutorials/)

For the planned TAS path, Kueue computes topology assignments and default-scheduler initially binds within those constraints. Validate DRA claim lifecycle, explicit execution groups and network-device attachment locally before adding MultiKueue worker-side TAS/DRA. Worker DeviceClasses and claim templates must be provisioned and verified separately. Persist requirements separately from observed assignments; scheduler-driven replacement needs an explicit runtime/identity recovery contract. These are planned capabilities, not extensions to the currently supported single-Pod contract.

### Cross-cluster status path

```text
worker kubelet → worker Pod.status
             → MultiKueue
             → manager Pod.status
             → Milos validates and persists the node result
             → successors become eligible
```

The reviewed v0.20 documentation describes automatic scheduling gates on manager plain Pods and synchronization of remote status. [Official documentation](https://kueue.sigs.k8s.io/v0.20/docs/tasks/run/multikueue/plain_pods/)

The corresponding v0.20.0 adapter synchronizes phase and container statuses, preserves the manager SchedulingGated condition while the remote Pod is not scheduled, and skips synchronization for a terminating manager Pod. Do not interpret every manager condition as a direct worker fact. [Pinned source](https://github.com/kubernetes-sigs/kueue/blob/v0.20.0/pkg/controller/jobs/pod/pod_multikueue_adapter.go)

An unchanged resourceVersion does not establish disconnection, and a successful manager API read does not prove worker connectivity. Assess freshness using available MultiKueueCluster, AdmissionCheck, and Workload information. Report unknown when reliable evidence is unavailable.

### Multi-cluster release criteria

Verify these protocols through source review and E2E tests against pinned versions, rather than inferring them from component feature lists:

| Question | Required evidence |
| --- | --- |
| Can a manager execute the application container? | Manager Pods remain under the correct scheduling gates in successful and faulty configurations |
| Is the ownerReference compatible with plain Pod integration? | Joint tests of Milos ownership, labels, and Kueue framework detection |
| Are terminal results retained? | Other garbage collectors do not remove manager Pods/Workloads before Milos persists results |
| What happens after worker loss or redispatch? | Whether upstream can recreate remote Pods, how old and new executions are identified, and how double retries are prevented |
| When is cancellation complete? | Persistent evidence of remote cleanup and retention of pending cleanup while disconnected |
| Which Pod metadata is copied? | Propagation of custom labels, annotations, and finalizers without leaving workers waiting for a nonexistent Milos controller |
| Does Volcano execution work? | Correct worker scheduling configuration and any required single-Pod auxiliary objects |

If upstream objects cannot prove remote cleanup, separately design and review a minimal persistent cleanup receipt or a restricted read-only worker verifier. Keep multi-cluster support experimental until this is resolved. Do not begin with a full worker agent or invent upstream acknowledgement fields.

Preconfigure worker Secrets, ConfigMaps, ServiceAccounts, image-pull permissions, and PVCs correctly. Same-name PVCs do not guarantee identical data. The initial design does not automatically transfer outputs between nodes or replicate dependency resources. Application Pods do not receive manager-cluster credentials.

## 8. Cancellation, timeouts, and cleanup

Node total/runtime budgets, waiting diagnostics and node-scoped timeout cleanup are implemented as described in [timeouts](timeouts.md). Separate stage timeouts and retries remain future work.

Persist cancellation as `desiredState=Cancelled`; it cannot revert to Running. Workflow deletion enters the same cleanup path through a Milos finalizer. Modify only Milos-owned finalizers and do not remove Kueue finalizers.

1. Persist the stop reason and stop scheduling new nodes or application retries. Mark unstarted nodes Cancelled while preserving completed results.
2. Set in-flight nodes to Cancelling and call ExecutionBackend.Stop. The native backend deletes using the bound Pod UID to avoid removing a same-name replacement.
3. Preserve resource mappings and continue checking local and remote cleanup. A manager Pod returning NotFound is not proof of remote cleanup.
4. Once validated cleanup criteria are met, set `CleanupComplete=True`, finalize cancellation or timeout, and remove the Workflow finalizer.

Initially, use a Workflow finalizer and terminal-resource retention policy. Avoid adding a custom Pod finalizer that MultiKueue might copy without a corresponding worker controller. Prototype metadata propagation and cleanup before deciding whether Pod result-protection finalizers are needed.

Calculate workflow deadlines from creation time, including dependency waits, queueing, execution, and retries. A timeout starts the stop protocol; it does not establish that remote containers stopped at that instant. If a worker is disconnected, retain Cancelling and pending cleanup rather than dropping the finalizer because of the timeout.

For cancellation/success races, never change an already committed workflow terminal state. Otherwise, prioritize stop intent observed and successfully persisted during reconciliation, while retaining committed node results. If delayed events obscure completion order, do not infer a remote timeline from local arrival order.

Call ExecutionBackend.Collect for naturally completed Pods only after their results are persisted. Configure other collectors to avoid deleting terminal evidence prematurely. If forced external deletion loses a result, report unknown and require investigation rather than assuming success. Expose retention, cleanup failures, and resource leaks through metrics.

## 9. Modules, deployment, and stack

| Module | Inputs and outputs | Outside its responsibility |
| --- | --- | --- |
| argoyaml | Source YAML → validated immutable definition | Kubernetes submission and runtime state |
| workflow/model, validate | Domain model, cycle detection, parameter and dependency validation | SDK calls |
| workflow/planner | Definition, persisted state, observations, time → next plan | API writes and implicit clocks |
| controller | Reads, planning, execution, status CAS, and requeueing | Application parameter expression engine |
| execution | Lifecycle contract, configuration resolution, and normalized observation model | DAG progression or dynamic plugin loading |
| backends/nativepod | Ensure/Observe/Stop/Collect for local Pods | Workloads and cross-cluster dispatch |
| pod | Construction, execution keys, UID validation, and fact normalization | Autonomous retry decisions |
| Future backends/scheduling | Kueue/MultiKueue lifecycle and Volcano scheduling adaptation | Reimplementing upstream schedulers or coupling them to the planner |
| observability | Metrics, structured logs, and Conditions | Treating logs as the execution ledger |

Use Go, controller-runtime/client-go for APIs and controllers, controller-gen for CRD/RBAC generation, and Helm for deployment. The compiler uses strict YAML parsing and explicit field allowlists, rather than decoding full Argo types and ignoring unsupported fields.

Initially, deploy only Milos and its WorkflowRun CRD into one Kubernetes cluster with default-scheduler. Permissions cover WorkflowRun/status, Pod lifecycle, and necessary native objects such as leader-election Leases. Application Pods do not need Kubernetes API tokens by default. Add Kueue permissions and watches only for enabled backends. Manager/worker deployments come with the multi-cluster stage, with credentials managed by MultiKueue.

The first release focuses on DAG validation, dependency evaluation, execution identity, cleanup, and failure recovery. Future ML resource policies should extend explicit inputs and execution configuration while preserving the separation of DAG progression, resource admission, cluster selection, and node scheduling.

## 10. Scale limits and observability

Proposed initial guardrails are 128 nodes, a compiled definition no larger than 256 KiB, a total WorkflowRun design budget of 768 KiB, and at most 3 application retries. These are prototype budgets, not official Kubernetes limits or measured capacities. Reserve room for worst-case status growth, bound diagnostic text, and test serialization at maximum load.

Keep node and execution history bounded in status. Checking only submission size is insufficient: running out of room to persist a terminal result would break recovery. Evaluate separate NodeExecution CRs, controller sharding, and external archival storage when measurements show a scale bottleneck.

Measure reconciliation duration, ready-to-submit latency, admission wait time, status conflicts, identity conflicts, unavailable observations, and cleanup delays. Put Workflow/Pod UIDs in logs, not unbounded Prometheus labels. Write Conditions only when their meaning changes to avoid unnecessary API writes during periodic checks.

## 11. Delivery and acceptance

| Stage | Deliverable | Acceptance criteria |
| --- | --- | --- |
| M0 Contract | Schema draft, strict import, static DAG validation | Reject unknown fields, duplicate keys, cycles, and invalid references; deterministic example compilation |
| M1 Core | WorkflowRun, planner, NativePodBackend + default-scheduler | Run without third-party CRDs; correct diamond completion and failure propagation |
| M2 Lifecycle | Retries, timeouts, cancellation, and result retention | Fault injection, status conflicts, completion/cancellation races, and UID deletion preconditions |
| M3 Local extensions | Kueue + default-scheduler, then single-cluster Volcano | Validate admission and node scheduling independently without changing the DAG planner |
| M4 Multiple clusters | MultiKueue + default-scheduler, then Volcano combinations | Remote execution, delayed status, disconnection, late results, and confirmed cancellation cleanup |
| M5 Release | Pinned dependencies and capacity measurements | Reproducible compatibility, permissions, retention, and recovery verification |

The resource-management workstream follows [H1–H5](resource-pools.md#delivery-roadmap-and-acceptance): read-only Node/ResourceSlice views, DRA-backed native profiles, Kueue DRA/TAS mappings, local execution groups and network claims (H4a), MultiKueue worker-side TAS/DRA (H4b), then measured optimization. Volcano topology remains an alternative path. These stages do not mark lifecycle or multi-cluster acceptance as complete.

Planner tests cover dependencies, failure propagation, retry budgets, determinism, and non-regressing state. envtest covers schema, status conflicts, ownership, UIDs, and related API behavior. It does not run kubelet or a real scheduler and cannot replace multi-cluster E2E tests.

Required failure cases include crashes after successful Create, Create timeouts, status conflicts, restarts before terminal-result persistence, stale controller caches, delayed events from old executions, externally deleted Pods, worker disconnection during cancellation, recovery after cleanup failure, and parallelism boundaries.

## 12. Versions and open questions

Kueue v0.20 documentation and the v0.20.0 Pod adapter were reviewed as research references, not as a validated release baseline. Initially pin Kubernetes, Go, controller-runtime/client-go, and the Argo semantic reference version. Pin Kueue/MultiKueue and Volcano versions and image digests when their respective extensions are implemented. No cluster tests have been run.

The repository is `github.com/wsq2333/milos`, which also establishes the planned Go module path. Before initial implementation, choose the final API group, real workload YAML samples, and capacity budget. Resolve integration questions before enabling the corresponding backend; they do not block the native Pod core:

1. Do MultiKueue plain Pod recovery, cancellation, and garbage collection provide sufficient identity and cleanup evidence?
2. How can manager terminal results be protected without blocking remote cleanup, and is a minimal additional object needed?
3. Does the selected Volcano configuration support the single-Pod path directly, and how does preemption interact with Kueue?
4. What are the final API group and initial workload YAML samples?
5. Do the 128-node limit and status budget cover the initial workloads?

These questions constrain subsequent implementation without preventing progress on the single-cluster static DAG core.
