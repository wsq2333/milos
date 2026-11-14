# Optional scheduling integrations

See [planned Milos workflow extensions](milos-extensions.md) for the proposed authoring format, ResourceProfile, task requirements and durable execution decisions. A narrower [native profile path](profile-authoring.md) is implemented; the remaining extensions and profile-aware scheduler integrations are future work.

The proposed [DRA-backed profile design](resource-pools.md) reuses driver inventory/allocation and adds read-only hierarchical hardware views. Standalone ResourcePool/NodeHardware CRDs are deferred. DRA profile and topology mappings are not part of the supported combinations below.

Milos keeps DAG decisions separate from admission, cluster placement, and node scheduling. All modes implement the same `Ensure`, `Observe`, `Stop`, and `Collect` contract. The planner still consumes normalized node phases; it does not manipulate Kueue Workloads or Volcano PodGroups.

These integrations are experimental. The baseline needs only Kubernetes. Optional integrations must be enabled explicitly and their controllers, webhooks and APIs must already be installed by the cluster operator.

## Supported configuration

| Backend | Admission | Placement | Scheduler | Enable flags |
| --- | --- | --- | --- | --- |
| `native-pod` | `None` | `Local` | `default-scheduler` | None |
| `native-pod` | `None` | `Local` | `volcano` | `--enable-volcano` |
| `kueue-pod` | `Kueue` | `Local` | `default-scheduler` | `--enable-kueue` |
| `kueue-pod` | `Kueue` | `Local` | `volcano` | Both flags |
| `multikueue-pod` | `Kueue` | `MultiKueue` | `default-scheduler` | `--enable-kueue --enable-multikueue` |

MultiKueue with Volcano is rejected. Each node is one Pod, and each Volcano PodGroup has `minMember: 1`. This supports Volcano node scheduling, not multi-Pod gangs or distributed training. Kueue admission and Volcano scheduling are separate steps; two independent queue policies can both constrain progress.

External APIs are accessed through controller-runtime's unstructured client: **Kueue `kueue.x-k8s.io/v1beta2`** and **Volcano `scheduling.volcano.sh/v1beta1`**. A cluster serving only Kueue v1beta1 is unsupported. No upstream controller libraries are imported into Milos, so the Go/Kubernetes dependency versions remain unchanged. API schema compatibility does not establish scheduler or controller behavior compatibility.

## Local Kueue

Use [kueue.yaml](../config/samples/kueue.yaml), replacing `ml-local` with an operator-provided LocalQueue in the WorkflowRun namespace. It must point to a local ClusterQueue, without a MultiKueue AdmissionCheck. Start the controller with:

```sh
bin/milos-controller --namespace=ml-workflows --enable-kueue
```

Milos adds the queue label and Kueue scheduling gate at Pod creation. Kueue's Pod integration and mutating webhook must add its managed label and own finalizer, create the Workload, and remove the gate after admission. Milos never grants admission or removes Kueue finalizers. If the webhook is absent, the initial gate prevents execution and observation reports `KueueManagementMissing`.

The backend discovers Workloads by their controller owner, checks Pod UID and queue identity, and persists Workload name and UID in node status. Admission waiting, Pod execution, and placement are recorded separately. `Admitted` does not imply `Running`. Kueue eviction produces an unknown observation with `AdmissionEvicted`; a missing bound Pod is not recreated. This prototype has no application retry or automatic resubmission after preemption.

## Local Volcano

Use [volcano.yaml](../config/samples/volcano.yaml), replacing the queue name with an existing Volcano Queue. Milos creates one owned PodGroup before creating its Pod, persists the PodGroup UID, and adds `scheduling.k8s.io/group-name` to the Pod. Reconciliation does not adopt a foreign or replaced PodGroup.

For Kueue plus Volcano, use [kueue-volcano.yaml](../config/samples/kueue-volcano.yaml) and enable both integrations. Kueue owns admission; Volcano owns node scheduling. Milos deletes its PodGroup only after Pod cleanup.

## MultiKueue

Use [multikueue.yaml](../config/samples/multikueue.yaml). The LocalQueue's ClusterQueue must reference the named MultiKueue AdmissionCheck for every flavor. Its MultiKueueConfig must contain exactly the immutable `placement.workerClusters` set. Concurrent admission variants are rejected. Configure all worker readers explicitly:

```sh
bin/milos-controller --namespace=ml-workflows \
  --enable-kueue --enable-multikueue \
  --multikueue-origin=multikueue \
  --worker=worker-a=/etc/milos/workers/worker-a.kubeconfig \
  --worker=worker-b=/etc/milos/workers/worker-b.kubeconfig
```

The worker names are MultiKueueCluster resource names. The origin must match the manager's MultiKueue origin configuration. Credentials come only from operator-provided files, never WorkflowRun fields. Worker clients use direct API reads with a ten-second request timeout; they never create, patch or delete worker resources. Grant only `get` on Pods and `get,list` on Workloads in the execution namespace, using [the worker reader role](../deploy/rbac/worker-reader.yaml).

Milos creates only the manager Pod. Upstream MultiKueue creates remote Workloads and Pods, selects the worker, mirrors status and deletes remote resources. Milos checks all eligible workers directly before trusting a result, matching the execution digest, identity, origin, prebuilt Workload reference and pinned resource UIDs. Both label and annotation forms of the prebuilt reference are accepted. Manager-mirrored success alone cannot release successors. An unavailable worker, multiple executions, changed UID, missing resource, or policy drift blocks normal progression.

Cancellation first marks the manager Pod terminating, then deletes its Workload. Milos' `remote-cleanup` finalizer retains the manager identity until every configured worker reports that associated Pods and Workloads are absent. Milos then removes only its own finalizer and waits for manager resource disappearance. Worker unavailability leaves cleanup pending. Upstream Kueue finalizers are never stripped. Keep the queue routing and worker configuration available and unchanged until active runs are cleaned up; policy drift deliberately blocks remote cleanup rather than silently accepting a different worker set.

This is cooperative cleanup, not distributed fencing. A fresh absence check cannot prove that an in-flight remote create cannot arrive later, or that a process on a partitioned node has stopped. Upstream cleanup and application-level idempotency remain necessary. Automatic redispatch adoption and cross-cluster exactly-once side effects are not provided. Namespaces, service accounts, images, PVCs and data must already be usable on every eligible worker; Milos does not replicate storage or credentials.

## Planned TAS and network integration

This is future work, not an addition to the supported configuration table. First validate DRA-backed native profiles and claim lifecycle. Then add Kueue DRA quota/TAS feasibility with default-scheduler, local execution groups and MultiKueue worker-side TAS/DRA. Volcano topology remains an alternative requiring its own allocation compatibility tests. Upstream support does not establish Milos runtime, identity or cleanup compatibility.

Network locality uses provider topology; RDMA/NIC allocation uses approved DRA template requests and required attachment components. Existing device-plugin profiles remain an explicit compatibility path, never a silent fallback. Link speed and same-rack placement do not guarantee bandwidth. See the [TAS/MultiKueue diagrams and examples](resource-pools.md#kueue-tas-and-multikueue), [network resource model](resource-pools.md#network-resources) and [H3–H4b acceptance plan](resource-pools.md#delivery-roadmap-and-acceptance).

The initial DRA integration uses per-Pod ResourceClaimTemplates with exact counts. Validate Kueue feature gates and unsupported claim features. On workers, administrators provision compatible DeviceClasses/templates; MultiKueue does not synchronize them. Add authorized claim observations and cleanup verification without writing allocation status or removing driver protection finalizers. See [Kueue DRA limitations](https://kueue.sigs.k8s.io/v0.20/docs/concepts/dynamic_resource_allocation/).

## Persistence and permissions

WorkflowRun status stores the Pod, Workload and PodGroup identities, selected worker identities, normalized admission/placement, observation source and failure class. These fields survive controller restarts. Queue choice and worker membership are immutable spec fields. Existing native WorkflowRuns retain their defaults.

`config/rbac/role.yaml` contains generated roles without granting them automatically. Optional [Kueue bindings](../deploy/rbac/kueue/bindings.yaml) and [Volcano bindings](../deploy/rbac/volcano/bindings.yaml) grant namespaced mutations through RoleBindings and cluster policy reads through separate ClusterRoleBindings. [MultiKueue bindings](../deploy/rbac/multikueue/bindings.yaml) additionally grant manager Pod patching for the Milos finalizer. Adjust every namespace before applying; the examples use `default`. Worker credentials require separate read-only permissions. A kubeconfig-driven local process uses its kubeconfig permissions, not these ServiceAccount bindings.

## Validation

Run unit tests with `make test`. Tests cover creation recovery, admission waiting, eviction, missing webhooks, owned PodGroups, remote identity, stale manager success, worker disconnection, and cleanup after manager loss.

`make test-integration` always runs the native API contract test. To additionally validate external schemas and manager/worker identity handling against two isolated API servers, provide upstream CRD directories:

```sh
MILOS_KUEUE_CRDS=/path/to/kueue/config/components/crd/bases \
MILOS_VOLCANO_CRDS=/path/to/volcano-crds \
make test-integration
```

Use a directory containing only Volcano PodGroup and Queue CRDs. These tests simulate upstream controller effects; they do not run the Kueue or Volcano controllers or workload containers. Reference schemas are Kueue v0.20.4 and the PodGroup/Queue CRDs extracted from the Volcano v1.14.6 `installer/volcano-development.yaml` release manifest. The `volcano-sh/apis` repository artifacts are example objects, not CRD definitions.

An opt-in live test is available for native execution, Kueue completion, queued cancellation and running cancellation:

```sh
MILOS_TEST_KUBECONFIG=/path/to/development.kubeconfig \
MILOS_TEST_CONTEXT=development \
MILOS_TEST_IMAGE=your-registry/busybox:tag \
go test -tags=live -v -count=1 -timeout=15m ./tests/live
```

It requires the Milos CRD and a functioning Kueue Pod integration. It creates a unique namespace, a ResourceFlavor, two ClusterQueues restricted to that namespace, LocalQueues and tiny CPU-only runs. It does not edit existing resources or join shared cohorts. It deletes only its own test runs and queues, using UID preconditions; the empty namespace is retained. Cluster-scoped test resource creation requires explicit authorization in a shared environment. Prefer the user-designated SSH development environment for tests, with explicit kubeconfig/context and an isolated source directory.

Small CPU DAGs passed on a real sim prod deployment on 2026-11-09 for native/default, native/Volcano, Kueue/default and Kueue/Volcano, including combined-path cancellation and scoped Pod creation permissions. Reusable smoke-test manifests and the verification script are in [example/](../example/README.md). Real MultiKueue dispatch/cancellation, worker outages, GPU behavior and production-scale acceptance remain unverified.

## Upstream references

- [Kueue plain Pods](https://kueue.sigs.k8s.io/v0.20/docs/tasks/run/plain_pods/)
- [MultiKueue plain Pods](https://kueue.sigs.k8s.io/v0.20/docs/tasks/run/multikueue/plain_pods/)
- [Kueue v0.20.4 Pod adapter](https://github.com/kubernetes-sigs/kueue/blob/v0.20.4/pkg/controller/jobs/pod/pod_multikueue_adapter.go)
- [Volcano v1.14.6 scheduling API](https://github.com/volcano-sh/apis/tree/v1.14.6/pkg/apis/scheduling/v1beta1)
