# Deploy Milos

This directory contains reviewable deployment configuration. **Shared-cluster deployments require explicit user authorization.** See the [sim prod deployment configuration](sim-prod/README.md) for the dedicated smoke-test setup. Rendering manifests and running isolated code tests do not install anything in a shared cluster.

The default deployment reconciles WorkflowRuns in the `milos` namespace and uses the native Pod backend. Experimental overlays enable Kueue, Volcano or MultiKueue. They do not install or reconfigure those upstream components, provision GPUs, create queues, or replicate worker storage.

## Layout

```text
deploy/
├── Dockerfile              # Controller image build, from the repository root
├── crds/                   # Separate WorkflowRun and ResourceProfile CRD installation
├── base/                   # Namespace, Deployment and generated controller RBAC
├── rbac/                   # Optional manager permissions and worker reader Role
└── overlays/
    ├── native/
    ├── kueue/
    ├── volcano/
    ├── kueue-volcano/
    └── multikueue/
```

Generated API schemas and roles remain in `config/` as the single source of truth. Kustomize references those directories rather than copying generated files. Run `make generate manifests` after API/RBAC marker changes. The base installs optional ClusterRole definitions but grants their permissions only when the relevant overlay includes bindings.

## Prerequisites and compatibility

- A dedicated namespace, explicit kubeconfig/context, and permission to install the Milos CRD and RBAC.
- An image registry reachable by the execution cluster. The `ghcr.io/wsq2333/milos:dev` reference is a placeholder, not a published release.
- The baseline uses standard Pods, scheduling gates where applicable, and CRD CEL validation. The code uses Kubernetes libraries v0.36.0. Isolated API tests target Kubernetes 1.36; broader cluster compatibility is not certified.
- Kueue modes require `kueue.x-k8s.io/v1beta2`, Pod integration, a working mutating webhook, and operator-created LocalQueues/ClusterQueues. Make sure Kueue manages the selected namespace. Do not edit an existing shared Kueue configuration without separate authorization.
- Volcano modes require `scheduling.volcano.sh/v1beta1`, a running `volcano` scheduler and an operator-created Volcano Queue. Each workflow node gets a single-Pod PodGroup; distributed gangs are unsupported.
- MultiKueue requires the manager and workers to be configured upstream already, including Pod integration, matching namespaces and LocalQueues, admission checks, and worker credentials. It supports only `default-scheduler` in this release.

See [the integration contract](../docs/scheduling-integrations.md) for the complete configuration matrix and failure semantics.

## Build an image

Run from the repository root on the designated development machine:

```sh
docker buildx build --platform linux/amd64 \
  -f deploy/Dockerfile -t YOUR_REGISTRY/milos:YOUR_TAG --load .
```

Publishing is a separate operator action. After authorization:

```sh
docker push YOUR_REGISTRY/milos:YOUR_TAG
```

Change the selected overlay's `images` entry to your repository and preferably an immutable digest:

```yaml
images:
  - name: ghcr.io/wsq2333/milos
    newName: YOUR_REGISTRY/milos
    digest: sha256:YOUR_IMAGE_DIGEST
```

Remove `newTag` when using `digest`. Base build images are version/tag references; resolve and pin their digests in a release build. A private registry may require a controller Deployment `imagePullSecrets` entry. Workflow execution Pods use the independent `spec.definition.imagePullSecrets` setting.

## Render before deployment

These commands are local rendering only and do not contact a Kubernetes API:

```sh
kubectl kustomize deploy/crds > /tmp/milos-crds.yaml
kubectl kustomize deploy/overlays/native > /tmp/milos-native.yaml
kubectl kustomize deploy/overlays/kueue > /tmp/milos-kueue.yaml
kubectl kustomize deploy/overlays/volcano > /tmp/milos-volcano.yaml
kubectl kustomize deploy/overlays/kueue-volcano > /tmp/milos-kueue-volcano.yaml
kubectl kustomize deploy/overlays/multikueue > /tmp/milos-multikueue.yaml
```

Select one overlay. Every overlay retains the native path, while the corresponding flags allow additional execution configurations. Do not apply several overlays as separate controllers: they share the same Deployment and lease.

The namespace defaults to `milos`. To change it, update `namespace` in the base and selected overlay and the Namespace resource name. Verify rendered Deployment, ServiceAccount, RoleBindings and ClusterRoleBinding subject namespaces all agree. A controller handles exactly one namespace. Deploying multiple independent controller installations also requires unique cluster-scoped role/binding names; the supplied manifests are for one installation per cluster.

The Deployment runs as non-root, uses a read-only root filesystem, enables leader election and exposes health checks on port 8081. Metrics are disabled by default. No Service or ingress is required for reconciliation. `/readyz` is a process readiness check; it does not prove that external schedulers, queue policies or worker credentials are usable. The controller ServiceAccount can execute Pods and must not be exposed to untrusted workflow authors without separate authorization controls.

## MultiKueue worker access

The example overlay expects two workers, `worker-a` and `worker-b`. Update its `--worker` arguments to match the MultiKueueCluster names and set the origin to the upstream manager's value. The immutable WorkflowRun worker list must exactly match its MultiKueueConfig cluster list. Keep that routing stable until existing runs are cleaned up.

Use a dedicated read-only identity per worker. [worker-reader.yaml](rbac/worker-reader.yaml) grants `get` on Pods and `get,list` on Workloads in one namespace. Bind the Role to the identity in that worker's kubeconfig. Milos never needs worker write access. Do not reuse the upstream MultiKueue write credential merely for convenience.

Prepare self-contained kubeconfigs with embedded CA and credentials. The distroless image has no shell or external cloud authentication plugin. File references or `exec` authentication require a separately designed image/volume configuration. Refresh credentials before expiry; Milos reads these kubeconfigs at startup, so credential rotation requires a controlled controller restart.

After deployment authorization, create a Secret in the manager namespace from local files without printing their content:

```sh
kubectl --kubeconfig "$MANAGER_KUBECONFIG" --context "$MANAGER_CONTEXT" \
  -n milos create secret generic milos-worker-kubeconfigs \
  --from-file=worker-a.kubeconfig=/secure/path/worker-a.kubeconfig \
  --from-file=worker-b.kubeconfig=/secure/path/worker-b.kubeconfig
```

Create the namespace first if it does not exist. The Secret is intentionally absent from Git. The overlay mounts it read-only and will remain pending until it exists. No operator credentials, developer addresses or cluster configuration are stored in this repository.

## Installation procedure — execute only when authorized

Set the explicit target; never rely on the current context:

```sh
export MANAGER_KUBECONFIG=/secure/path/development.kubeconfig
export MANAGER_CONTEXT=YOUR_DEVELOPMENT_CONTEXT
```

Before changing anything, inspect whether the CRD, namespace, controller and role names already exist. Review the rendered resources and image digest. If a target resource is shared or already belongs to another installation, stop and resolve ownership; do not overwrite it.

For a new installation, the reviewed manifests can be created without overwriting existing objects:

```sh
kubectl --kubeconfig "$MANAGER_KUBECONFIG" --context "$MANAGER_CONTEXT" \
  create -f /tmp/milos-crds.yaml
kubectl --kubeconfig "$MANAGER_KUBECONFIG" --context "$MANAGER_CONTEXT" \
  wait --for=condition=Established --timeout=60s crd/workflowruns.milos.example.io
kubectl --kubeconfig "$MANAGER_KUBECONFIG" --context "$MANAGER_CONTEXT" \
  create -f /tmp/milos-native.yaml
```

Replace the last file with the selected overlay. For MultiKueue, prepare the Secret as described above; if the namespace already exists from preparation, omit its document from the reviewed controller manifest. `create` reports collisions instead of silently reconciling existing configuration. A failed multi-resource create can leave partial resources; inventory exactly what was created before retrying. Do not substitute a broad `apply` to resolve unknown ownership.

## Completed offline checks

On 2026-11-06, all five overlays and the CRD bundle rendered successfully on the designated Linux development machine. Rendered namespaces, RBAC subject references, integration flags and separate CRD installation were checked. Go 1.26.5 race tests, build and vet passed. Isolated Kubernetes 1.36 API tests passed with Kueue v0.20.4 and Volcano v1.14.6 CRD schemas, including manager/worker resource identity and cleanup. The opt-in live test compiled but was not run.

A dedicated sim prod installation subsequently passed CPU smoke tests with the examples in `example/`. The generic overlays above were rendered offline; MultiKueue and GPU deployment acceptance remain pending.

## Acceptance checklist — pending live execution

After authorization, validate the selected mode in an isolated namespace with explicit context:

1. Confirm Deployment availability, leader election, and absence of authorization errors in controller logs.
2. Run the [native diamond](../config/samples/diamond.yaml) and confirm dependencies, persisted success, and retained Pod identities.
3. For Kueue, use [the Kueue sample](../config/samples/kueue.yaml) with an approved LocalQueue. Confirm the initial gate, Workload ownership, admission and execution. Cancel both a queued run and a running run; confirm owned Pods and Workloads disappear.
4. For Volcano, confirm the selected Queue, PodGroup UID and membership annotation, scheduler selection, and PodGroup cleanup after the Pod. Validate the Kueue/Volcano combination separately.
5. For MultiKueue, confirm the manager Pod remains gated, the selected worker's real Pod supplies the result, and worker identities are persisted. Verify cancellation removes remote resources through upstream MultiKueue before Milos reports completion.
6. Run restart and worker-unavailability cases only in an explicitly approved isolated environment. Unknown or unavailable observations must not create a replacement execution or release a successor.

The opt-in live Go test is described in [scheduling integrations](../docs/scheduling-integrations.md). It creates cluster-scoped test queues and a ResourceFlavor in addition to namespaced resources, so it requires separate approval in shared clusters. Do not run it as part of an ordinary deployment/render check. The dedicated sim prod CPU smoke tests do not certify every overlay or deployment environment.

## Upgrade, rollback and cleanup

Keep CRD updates separate from Deployment updates. Back up existing WorkflowRun definitions/status and review schema changes, stored fields and active execution configurations before upgrading. Rolling back to an older native-only controller while optional runs exist is unsafe: it cannot interpret or clean them up. Preserve an integration-capable controller and worker access until those runs are finished and cleaned up.

For an authorized image-only upgrade, change only the selected image reference and review the diff before applying the Deployment update. Retain the previous digest for rollback. Do not recreate the CRD or overwrite Kueue/Volcano configuration as an upgrade shortcut.

For cleanup, first stop new submissions. Cancel individual active WorkflowRuns and wait for cleanup completion, then delete individual completed WorkflowRuns while the controller is still running so finalizers can clean their retained resources. For MultiKueue, verify worker Pods and Workloads are absent before removing worker access. A disconnected worker keeps cleanup pending.

Only after all owned resources are gone should an authorized operator remove the Milos Deployment and its dedicated RBAC. Never strip upstream finalizers, force-delete a namespace, or delete the CRD as a routine cleanup shortcut. Namespace/CRD removal is a separate explicitly reviewed operation because it can remove unrelated data. Keep shared queues, schedulers, webhooks and worker configuration intact.
