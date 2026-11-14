# Native profile authoring

The first resource-aware authoring slice is implemented. `cmd/milos` compiles a Milos-owned Argo-style document to a native `WorkflowRun`; the controller resolves namespaced `ResourceProfile` objects and persists constraints before dispatch. This is not an Argo controller integration or standard Argo importer.

## Usage

Build with `make build`. Review and install the CRDs and controller using the [deployment guide](../deploy/README.md). The ResourceProfile CRD is included by `config/crd` and `deploy/crds`; the controller receives read-only profile access. Profile creation/update permissions belong to platform administrators, not ordinary workflow submitters. All profiles in the execution namespace are available to that namespace's workflow submitters in this first version. Use namespace boundaries for profile access; per-queue authorization is future work.

The following are operator instructions, not actions performed by compilation:

```sh
# First adapt the example labels to verified hardware and device-plugin settings.
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE apply -f examples/milos/resourceprofile.yaml

# Local compilation performs no cluster access or writes.
bin/milos --file examples/milos/profile-workflow.yaml --namespace YOUR_NAMESPACE > /tmp/workflowrun.yaml

# Inspect the compiled definition before explicitly submitting it.
kubectl --context YOUR_CONTEXT create --validate=strict -f /tmp/workflowrun.yaml
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE get workflowrun hardware-example -o yaml
```

Use a new explicit workflow name for each execution. The compiler does not install a Workflow CRD; applying its authoring input directly is unsupported. The sample container is a placement smoke test, not a GPU performance benchmark. No hardware labels or exclusive device configuration are created automatically.

## Discover available profiles

Catalog commands read published profiles in one namespace using your Kubernetes credentials. They do not submit work, modify profiles, inspect nodes or report free capacity.

```sh
bin/milos profiles list --namespace YOUR_NAMESPACE --context YOUR_CONTEXT
bin/milos profiles describe h20-exclusive-v1 --namespace YOUR_NAMESPACE --context YOUR_CONTEXT
# Optional explicit credentials file:
bin/milos profiles list --namespace YOUR_NAMESPACE --kubeconfig /path/to/config --context YOUR_CONTEXT
```

`list` shows names, descriptions and resource bounds in name order. `describe NAME` shows the description, intended workload types, resource bounds, compatibility guidance, enforced selectors/tolerations, and current execution support. Choose the name for `milos.requirements.profileRef.name`; requested quantities still belong in the container template.

Namespace defaults to `default`, independent of the kubeconfig's namespace. Context defaults to the kubeconfig's current context; standard kubeconfig loading rules apply when `--kubeconfig` is omitted. Flags follow the action (and profile name for `describe`). Reads have a 15-second timeout. Missing profiles, missing CRDs, credential errors and denied access return errors; an empty authorized namespace reports no published profiles.

Catalog entries are not capacity reservations, compatibility certifications or guarantees of successful scheduling. Workload types are guidance, not an allowlist. Compatibility requirements are operator-authored statements; the controller does not inspect images or automatically enforce them. Missing catalog information is displayed as unknown/not provided, never as compatible. Actual selectors and resource bounds remain enforced by the existing execution path.

### Operator-maintained catalog metadata

Add optional metadata when publishing a new profile:

```yaml
spec:
  catalog:
    description: "Exclusive H20 configuration for single-Pod GPU workloads."
    workloadTypes: [training, evaluation]
    compatibilityRequirements:
      - "Use a CUDA-enabled image validated against the deployed NVIDIA driver."
      - "The application's peak device memory must fit the selected GPU."
  # Keep the profile's enforced nodeSelector and optional resourceBounds here.
```

The complete [sample profile](../examples/milos/resourceprofile.yaml) includes both guidance and constraints. Description is limited to 2,048 characters; workload types to 16 entries of 128 characters; compatibility requirements to 16 entries of 512 characters. Catalog fields are optional, so older profiles continue to work and remain listed. The entire spec is immutable: adding or changing catalog text on an existing profile requires publishing a new versioned name (and updating future submissions). Existing execution snapshots remain unchanged. Upgrade both CRDs before publishing catalog fields because resolved snapshots embed the profile spec.

### Read-only user permissions

Users need `list` on ResourceProfiles for the list command and `get` for detail. The controller's existing `get` permission is unchanged; catalog queries run as the caller. An administrator can apply the optional [reader Role](../examples/milos/catalog-reader-role.yaml) in the intended namespace and bind it to approved users/groups:

```sh
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE apply -f examples/milos/catalog-reader-role.yaml
kubectl --context YOUR_CONTEXT -n YOUR_NAMESPACE create rolebinding milos-profile-readers \
  --role=milos-profile-reader --group=YOUR_APPROVED_GROUP
```

No binding is installed automatically. These permissions grant neither profile writes nor workflow submission. Kubernetes RBAC controls visibility; no cluster-wide catalog, service-account impersonation or per-queue access inference is introduced.

## Supported authoring contract

The only input GVK is `milos.example.io/v1alpha1, Workflow`. Require explicit metadata.name, optional matching namespace, one entrypoint DAG, container templates, task names/template references/dependencies, and explicit `failFast: false`. Optional spec fields are parallelism, volumes, serviceAccountName and imagePullSecrets. Container fields use the existing Milos ContainerSpec allowlist. The initial compiler rejects parameters/interpolation, generateName, metadata labels/annotations, nested DAGs and standard Argo input; these remain planned rather than silently approximated.

The only custom authoring field currently accepted is:

```yaml
milos:
  requirements:
    profileRef:
      name: h20-exclusive-v1
```

Task type, scheduling priority, duration estimates, workflow extensions and distributed fields remain rejected. Unknown fields, duplicate YAML keys, multiple documents, anchors/aliases, custom tags, excessive nesting, conflicting namespaces, missing templates, cycles and invalid resource bounds fail validation. Input is limited to 256 KiB and DAGs to 128 nodes. Resource references are static.

Quantities remain in `container.resources` in both authoring and the native NodeSpec. This intentionally uses one existing resource source instead of adding a duplicate requirements.resources field. The resolver snapshots normalized resources, including requests defaulted from limits. Profile-enabled extended resources require equal integer requests and limits. Legacy nodes retain the original request-construction path.

## Implemented ResourceProfile schema

Use the concrete sample [resourceprofile.yaml](../examples/milos/resourceprofile.yaml). The schema is deliberately narrower than the long-term design:

- `spec.catalog`: optional bounded description, workload types and advisory compatibility requirements.
- `spec.nodeSelector`: 1–32 exact, validated label pairs.
- `spec.tolerations`: up to 16 explicit-key Equal/Exists scheduling tolerations; no NoExecute or timed eviction behavior.
- `spec.resourceBounds`: per-resource minimum and maximum effective requests; limits cannot exceed maximum bounds. Bounds validate quantities, not device capacity or quota.
- Spec is immutable through CRD validation. Publish a new versioned name to change constraints.

The profile does not claim to discover GPU memory, prove exclusivity, validate a communication path or reserve capacity. Platform operators must verify selector labels and device allocation configuration. No discovery/status controller is included. An absent matching node produces normal scheduling waits rather than fallback.

## Durable execution

```mermaid
flowchart LR
    A["Milos Workflow file"] --> C["Strict local compiler"]
    C --> W["Explicitly created WorkflowRun"]
    P["Namespaced ResourceProfile"] --> R["Controller resolution"]
    W --> R
    R --> S["Persist all ResolvedTasks"]
    S --> I["Persist node intent and ExecutionDecision"]
    I --> B["Existing native backend"]
    B --> K["Constrained Pod / default-scheduler"]
    K --> O["Existing status and lifecycle observation"]
    O --> W
```

`status.resolvedTasks` pins profile name, UID, generation, compiler version, constraints and normalized resources with a digest. Every required profile resolves before the workflow is accepted; one missing profile blocks all execution. The controller polls invalid active workflows, so publishing a missing profile can unblock them.

`status.nodes[name].decision` binds the snapshot to the definition digest and existing WorkflowUID/NodeID/Attempt/DispatchGeneration identity. It is saved with intent before Pod creation. Subsequent reconciles verify snapshot and decision hashes and reconstruct Pod requests from persisted data, without reading the live profile again. Profile deletion/recreation cannot alter an accepted snapshot or block cancellation. Status write access remains trusted controller authority, not a security boundary provided by hashes.

Existing workflow/node timeout and cancellation behavior remains intact. DefinitionDigest and the legacy execution digest are unchanged for nodes without requirements. Profiles are restricted to local native-pod/default-scheduler for now; Kueue/Volcano profile mapping and observed hardware verification are future work. Current Pod identity checks and ordinary Pod status establish execution facts, not physical GPU identity.

## Validation

Validation completed with `make build`, `make test` (race detector), `make vet`, and `make test-integration`.

Catalog CLI tests cover namespace isolation, stable listing, legacy/empty results, guidance display, credential options, authorization failures, and offline compiler compatibility. The isolated API test also verifies catalog schema limits, immutable metadata and readback.

Unit tests cover strict compilation, missing profiles, bounds, normalization, corrupt snapshots and legacy omission. The isolated API test creates a profile and compiled WorkflowRun, verifies profile immutability, deletes the profile after resolution, reconstructs the reconciler for every step, checks Pod selectors/GPU requests and stable UID, then confirms cancellation. The test does not run a scheduler, kubelet or GPU container; physical hardware placement requires separate live acceptance.

See [planned extensions](milos-extensions.md) for later capabilities and Kueue/Volcano mappings.

## Planned DRA profiles

The [DRA-backed profile design](resource-pools.md) proposes an explicit allocation provider and an approved ResourceClaimTemplate reference. It is not supported by the current compiler or controller. Existing profiles keep their selectors, container resource quantities and native/default behavior; they do not require DRA. Future DRA profiles take device quantities from the approved template and reject duplicate extended-resource requests for those devices.
