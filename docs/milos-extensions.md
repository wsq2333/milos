# Planned Milos workflow extensions

Status: long-term design. A narrower native-Pod profile slice is implemented; see [the actual schema, usage and acceptance boundary](profile-authoring.md). Standard Argo import and the additional extensions below remain planned. The schemas below are proposals, not the implemented API; `milos.example.io` remains a placeholder API group.

The [resource pools and hardware-aware placement design](resource-pools.md) defines the planned ResourcePool/NodeHardware layer and its H1–H5 roadmap. It is the detailed reference for hierarchical membership, discovery, topology ownership and pool-backed profiles.

## Authoring and execution boundaries

Use a Milos-owned `Workflow` document with familiar Argo DAG, template and container syntax. Put every custom authoring field under `milos`: task extensions under `dag.tasks[].milos`, workflow extensions under `spec.milos`. This is structural separation, not compatibility with the Argo controller. Standard Argo documents retain the separate strict [Argo subset contract](argo-subset.md); they must not accept these custom fields silently.

The authoring document is compiled into the existing `WorkflowRun`. A separate installed Workflow CRD/controller is not required for the first compiler: it can accept a document at submission. Persistence, resubmission and lifecycle of authoring objects require a separate decision before exposing them through the Kubernetes API. No CLI command or API endpoint is specified yet.

```mermaid
flowchart TD
    A["Standard Argo subset"] --> C["Validate and compile"]
    B["Milos Workflow<br/>Argo-style syntax + optional milos blocks"] --> C
    C --> W["WorkflowRun.spec<br/>Immutable DAG and normalized task requirements"]
    P["Platform ResourceProfiles and priority mappings"] --> R["Resolve and validate requirements"]
    HWP["Planned ResourcePool / NodeHardware and provider topology"] --> R
    W --> R
    R --> S["Persist resolved task snapshots"]
    W --> D["DAG Planner: dependencies"]
    D --> X["Reconciler and submission policy"]
    S --> X
    X --> I["Persist execution decision and identity"]
    I --> E["ExecutionBackend: Ensure / Observe / Stop / Collect"]
    E --> K["Kueue: admission, quota and flavor assignment"]
    K --> V["Selected local path<br/>TAS + default-scheduler or Volcano"]
    V --> H["Device allocation and execution"]
    H -. facts .-> O["WorkflowRun.status: allocation, results and timeline"]
    K -. admission facts .-> O
    O --> X
```

The diagram shows the proposed local integration paths. Native/default remains available. The planned [TAS/MultiKueue path](resource-pools.md#kueue-tas-and-multikueue) adds worker-side topology assignment after local acceptance; MultiKueue/Volcano remains unsupported.

## Planned authoring fields

All fields below are optional extensions. Only `milos.requirements.profileRef.name` is implemented, within the native profile contract linked above. A compiler must reject every other field until its semantics and enforcement are implemented. Do not accept future fields as ignored configuration.

| Location | Field | Contract and business value | Delivery |
| --- | --- | --- | --- |
| Task `milos` | `taskType` | Reporting category: training, evaluation, simulation, or 3dgs. Does not determine priority or hardware automatically. Enables comparable JCT and efficiency reports. | Planned workload metadata |
| Task `milos.requirements` | `profileRef.name` | One validated, versioned ResourceProfile. Compiles enforced constraints; physical hardware verification remains future work. | Implemented for native/default |
| Task `milos.scheduling` | `priorityClassRef` | Platform-authorized priority mapped separately to admission and placement classes. Improves urgent-work turnaround without bypassing dependencies. | H3 |
| Task `milos.scheduling` | `estimatedDurationSeconds` | Positive advisory runtime estimate; retain source and confidence when available. Not a timeout or deadline. Enables forecasts before duration-based scheduling is introduced. | H5 |
| Workflow `spec.milos` | `execution` | Explicit supported backend/admission/placement/scheduler configuration, normalized to WorkflowRun.spec.execution. Preserves queue ownership and authorization. | H3 |
| Workflow `spec.milos` | `submissionPolicy` | Bounded outstanding work, optionally by platform resource category. Avoids CPU branches being blocked by unrelated GPU waits and limits queue pressure. Exact fields require policy design. | Later |
| Workflow `spec.milos` | `deadline` | Optional end-to-end completion target. Define absolute/relative semantics before implementation; never infer from duration estimates. Enables deadline reporting and future constrained selection. | Later |
| Task `milos.requirements` | `execution` | Typed execution shape, worker roles/counts and per-worker quantities. Requires runtime-backed group lifecycle; not a larger minMember on the existing Pod backend. Enables distributed training. | Later |
| Task `milos.requirements` | `topology` | Required/preferred zone, fabric or subgroup placement with one explicit topology owner. Reduces communication overhead. | Later |
| Task `milos.requirements` | `network` | Proposed RDMA/capability requirements; device quantities stay in container resources. Locality and link speed do not promise bandwidth reservations. | H4a |
| Task `milos.requirements` | `data` | Required storage access and locality constraints. A location is not proof of availability or replication. Prevents unusable placements and transfer overhead. | Later |
| Task `milos.requirements` | `allowedProfileRefs` | Explicit alternatives, mutually exclusive with a single profileRef; selection objective and fallback rules must be defined. Enables measured hardware selection. | Later |
| Task `milos` | `recovery` | Interruption capability, checkpoint identity/completeness, restore compatibility and bounded recovery. No preemption protection without enforced platform policy. Reduces lost work. | Later |
| Workflow `spec.milos` | `optimization` | Advisory first, then constrained time/cost selection with prediction uncertainty. No mixed-unit score or automatic training-parameter changes. | Later |
| Task `milos` | `artifacts` / `cache` | Typed outputs/checkpoints and explicit safe reuse, covering inputs, code, configuration and side effects. Reduces repeated work. Requires an artifact protocol. | Later |

Later-field names identify reserved design areas, not a finalized schema. Fractional sharing, RDMA device requests, hybrid-parallel topology, automatic migration, priority propagation and safe backfilling require separate enforcement designs and acceptance tests. Workflow urgency must not silently elevate ancestors or unrelated tasks.

## Proposed usage

This proposed example uses the fictional GPU-Large hardware and research pool from the [resource-pool scenario](resource-pools.md#example-a-shared-pool-with-incompatible-placement-choices). `example.com/gpu` is a placeholder extended resource, not a deployed device-plugin configuration.

```yaml
apiVersion: milos.example.io/v1alpha1
kind: Workflow
metadata:
  name: reconstruction
spec:
  entrypoint: pipeline
  templates:
    - name: pipeline
      dag:
        failFast: false
        tasks:
          - name: reconstruct
            template: run-3dgs
            milos:
              taskType: 3dgs
              requirements:
                profileRef:
                  name: gpu-large-exclusive-v1
              scheduling:
                priorityClassRef: background
                estimatedDurationSeconds: 7200
    - name: run-3dgs
      container:
        image: example/3dgs:v1
        resources:
          requests:
            cpu: "8"
            memory: 32Gi
          limits:
            example.com/gpu: "1"
```

Container resources specify quantities; the task profile specifies eligible hardware. Bind extensions by DAG task, not template: two tasks may reuse a template with different profiles. Standard Argo dependency, parameter and explicit `failFast: false` restrictions still apply to the initial authoring subset. Unknown fields, duplicate keys and unsupported semantics are errors. No arbitrary PodSpec passthrough or user overrides of generated identity/scheduling metadata.

At compilation, normalize resources to one internal source. For profile-enabled nodes, move authoring container resources into NodeSpec.requirements.resources and clear legacy container.resources. Reject competing internal sources. Normalize extended-resource requests/limits consistently with Kubernetes semantics before digesting. A standalone acceleratorCount shorthand is unnecessary in the initial authoring schema. Duration estimates remain distinct from existing workflow/node timeouts.

## ResourceProfile

ResourceProfile is a namespaced, platform-managed CRD with an immutable spec. Restrict use through deployment policy as well as write permissions. Administrators publish a new versioned name to change a profile. Profiles define eligibility and request bounds, never live free capacity, reservations or tenant quota. The following proposed extension retains current `nodeSelector` and `resourceBounds` names; `poolRef`, `hardware` and `integrations` remain unsupported. CPU-only profiles omit GPU requirements.

```yaml
apiVersion: milos.example.io/v1alpha1
kind: ResourceProfile
metadata:
  name: gpu-large-exclusive-v1
  namespace: ml-workflows
spec:
  poolRef:
    name: research-gpu-large
  nodeSelector:
    accelerator.platform.example/model: gpu-large
    accelerator.platform.example/allocation-mode: exclusive
  hardware:
    gpu:
      minDeviceMemory: 70Gi
      allocationMode: exclusive
  resourceBounds:
    example.com/gpu: {min: "1", max: "8"}
    cpu: {min: "1", max: "96"}
    memory: {min: 4Gi, max: 512Gi}
  integrations:
    kueue:
      compatibleFlavorRefs: [gpu-large-exclusive]
```

Values and labels are illustrative, not verified cluster inventory. Device memory and exclusivity must map to verified labels and allocation mechanisms; declaring them does not enforce them. ResourceProfile validation status means configuration was verified, not that capacity is currently available. Store validation provenance and observation time. A profile must not manufacture RDMA support, runtime compatibility or device isolation from a name. Reject requirements the chosen backend cannot enforce.

Plan ResourcePool and NodeHardware as the next inventory and membership layer, as defined in the [hardware design](resource-pools.md). ResourcePool selects discovered nodes through parent/selector intersections and deduplicates capacity summaries; NodeHardware holds sourced physical facts. A profile's optional poolRef narrows eligible resources without replacing explicit constraints. Hardware classes, physical topology, isolation and team quotas remain separate. Reuse provider topology, ResourceFlavors and existing queues; provisioning, draining and a new allocation ledger are outside the initial scope. Existing profiles without poolRef remain valid.

## Resolution and execution records

1. Validate immutable input and authorization. Resolve every profile-enabled task before allowing any workflow task to start. Missing/invalid profiles produce a field-specific error; never fall back silently.
2. Persist bounded resolved task snapshots in WorkflowRun.status.resolvedTasks. Include profile name/UID/generation/spec digest, compiler version, normalized resources, selectors, tolerations, priority mapping and allowed flavor bindings. Persist the complete compiler output, not only hashes. Enforce snapshot and total object-size limits.
3. When a node is selected for submission, persist NodeStatus.decision with the existing WorkflowUID/NodeID/Attempt/DispatchGeneration identity, resolved-task digest, execution digest, reason and decision timestamp alongside execution intent. No backend create precedes this commit.
4. Build requests from the committed snapshot. Never resolve live profiles again for an existing execution. DefinitionDigest still identifies immutable user input; ExecutionDigest additionally binds compiler output and execution policy. Existing runs retain the legacy digest path.
5. Record observed allocation separately: Workload UID, resource-to-flavor assignments, worker cluster, Pod/Node UIDs, observed hardware labels and verification timestamp. Device UUIDs require trusted device observations; Pod requests alone do not prove device identity.
6. Keep existing IntentRecordedAt, StartedAt, FinishedAt and stage diagnostics. Detailed telemetry and historical decisions belong in external storage, not unbounded CR status.

For future pool-backed profiles, also persist pool UID, transitive policy revision, bounded evidence references and the actual compiled eligibility constraints. Do not embed pool membership lists or device graphs per task. The [pool enforcement contract](resource-pools.md#resolution-enforcement-and-restart-safety) must handle mutable membership, stale observations and restart/create races before this feature ships; a mutable pool-name label or a hash alone is insufficient.

Profile deletion must not prevent observation, cancellation or cleanup. Configuration drift before resource creation blocks dispatch with a reason rather than silently recompiling. After creation, report mismatches without inventing an automatic eviction/retry protocol. Existing unknown/disconnected execution rules remain in force.

The decision explains allowed hardware and why it was selected; observed allocation records what Kueue and the scheduler actually assigned. Predictions are not allocation facts. Future retries must explicitly define when a new resolution is allowed; no running attempt is mutated in place.

## Kueue and Volcano mapping

| Source | Compiled target / authority |
| --- | --- |
| Workflow execution admission.queueRef | Pod `kueue.x-k8s.io/queue-name`; existing namespaced LocalQueue |
| Normalized resource quantities | Pod resources; Kueue Pod integration owns creation of its Workload |
| Profile node constraints | Pod selectors/tolerations; validate compatibility with ResourceFlavor nodeLabels |
| Planned pool membership | Enforceable compiled eligibility predicate; pool hierarchy does not create queue quotas or topology domains |
| Profile compatibleFlavorRefs | Validation allowlist, not a direct Kueue flavor-selection command |
| Authorized priority mapping | Kueue `kueue.x-k8s.io/priority-class` and separately Pod/PodGroup priorityClassName |
| Workflow scheduler name | Pod schedulerName; default-scheduler or volcano in supported combinations |
| Workflow Volcano queue | Owned PodGroup.spec.queue |
| Single-Pod execution | Existing owned PodGroup with minMember: 1 and Pod group annotation |
| Group minimum resources, if emitted | Derive from effective Pod requests; no independent conflicting user quantity |
| Actual admission | Read Workload.status.admission PodSet assignments, including flavor per resource |
| Planned TAS locality | ResourceFlavor.topologyName and supported Pod-template TAS annotations; explicit PodSet grouping for multiple roles |
| Planned network devices | Verified capabilities plus provider resource requests or supported DRA claims; attachment handled by network/device components |
| Planned MultiKueue TAS | Approved per-worker profile/pool/flavor bindings; worker-side topology assignment |
| Actual placement | Read identity-verified execution Pod and authorized node/device observations |

Kueue remains admission and quota authority. ResourceFlavor describes hardware labels; quota belongs to ClusterQueue resource groups. Milos does not write Workload admission status or remove Kueue gates. Validate that queue configuration plus selectors excludes incompatible flavors; reject ambiguous mappings rather than pretending compatibleFlavorRefs pins a flavor. CPU/memory/GPU may have distinct flavor assignments.

Choose one topology path: Kueue TAS computes assignment with default-scheduler initially binding Pods, or Volcano owns topology placement. Do not stack independent topology decisions. Configure queue/preemption policies consistently; priority does not bypass dependencies or prevent eviction. See [TAS/MultiKueue](resource-pools.md#kueue-tas-and-multikueue) and the [network model](resource-pools.md#network-resources) for examples and validation boundaries.

Keep requirements immutable while recording scheduler-owned assignments separately. TAS node replacement may change observed allocation; enabling it requires explicit runtime recovery, replacement identity and cleanup handling. Existing single-Pod identity checks must not silently adopt replacement resources.

The new resolver requires read permissions for approved profiles and applicable flavor/priority metadata. Node verification may require additional scoped-by-policy cluster reads; do not assume existing worker credentials include them. Multi-cluster profiles require per-cluster bindings and verified worker capabilities before enabling this path. Preserve current remote UID verification and cleanup behavior.

## Compatibility and implementation boundaries

Without a `milos` block, preserve the supported baseline resources and scheduling defaults. Existing native WorkflowRun submissions remain valid. Missing extensions do not imply a GPU model, team priority or recovery capability. A deployment may explicitly require profiles for selected queues, but that is opt-in policy.

Keep the pure DAG planner independent of profile and scheduler APIs. Add a resolver/compiler in internal/resources; extend NodeSpec and status types, request construction and backend observations. Keep Ensure/Observe/Stop/Collect. Resource-aware submission is a separate change: preserve existing parallelism semantics for old workflows and introduce explicit new policy rather than redefining the field.

For distributed workloads, use a runtime-backed execution group. One owner handles in-execution process recovery; Milos owns workflow-level decisions after confirmed outcomes. The entire DAG is never one gang. Current single-Pod lifecycle and remote cleanup contracts must not be assumed sufficient for TrainJob or JobSet.

## Delivery and acceptance

| Order | Deliverable | Acceptance evidence |
| --- | --- | --- |
| Baseline | Implemented strict Milos authoring, native profile resolution, durable decisions and profile catalog | Existing unit/API coverage; real GPU verification remains pending; standard Argo import and parameters remain separate future work |
| H1 | NodeHardware/network inventory and hierarchical ResourcePool catalog | Trusted observations, unknown connectivity, parent intersection, overlap deduplication and shape summaries |
| H2 | Pool-backed native ResourceProfile enforcement | Correct live placement, bounded snapshots, membership drift/restart validation, cancellation and unchanged legacy behavior |
| H3 | Local profile/flavor and priority mapping; TAS configuration or alternative Volcano path | Pool enforcement, topology labels/gates, no quota bypass and cancellation |
| H4a | Execution groups, local TAS and network-device allocation | PodSet grouping, required/preferred locality, verified attachment/transport and explicit recovery |
| H4b | MultiKueue with worker-side TAS | Per-worker bindings, remote topology assignment, replacement identity and outage cleanup |
| H5 | Performance profiles, resource-aware submission and permitted alternative selection | Mixed training/evaluation/simulation/3DGS evaluation; uncertainty, equivalent-quality JCT/cost and fairness; bounded submissions and no starvation |

The native profile vertical slice is implemented. Next establish trustworthy inventory and pool membership, then validate a narrow pool-backed native path before automatic selection. See the [H1–H5 acceptance plan](resource-pools.md#delivery-roadmap-and-acceptance) for business value and required evidence. Artifact/data reuse requires its own correctness protocol; these hardware stages do not implicitly implement it. Scheduler integrations must validate real admission and placement rather than merely generated YAML.

## References

- [Kueue ResourceFlavor](https://kueue.sigs.k8s.io/v0.20/docs/concepts/resource_flavor/)
- [Kueue workload priority](https://kueue.sigs.k8s.io/v0.20/docs/concepts/workload_priority_class/)
- [Volcano PodGroup](https://volcano.sh/docs/concepts/podgroup/)
- [Execution abstraction](execution-abstraction.md)
- [Scheduling integration boundaries](scheduling-integrations.md)
