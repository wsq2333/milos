# Argo YAML input contract

Status: design draft for `argo-pod-dag-v1`; no parser has been implemented. This document defines the Milos import format. **It does not claim full compatibility with any Argo version.**

## Relationship to the planned Milos authoring format

This contract applies only to standard `argoproj.io/v1alpha1` input. The separately proposed [Milos-owned Workflow format](milos-extensions.md) uses `milos.example.io/v1alpha1` and isolates custom fields under task/workflow `milos` blocks. Both compile into WorkflowRun, but only a narrower Milos-owned compiler is implemented; see [its supported contract](profile-authoring.md). The standard Argo parser remains planned. Do not widen this standard-Argo allowlist to accept Milos extensions implicitly. The Milos format inherits the initial DAG/container restrictions and explicitly adds its supported extension schema; unknown or future fields are rejected.

## Document and entrypoint

Input must be a single YAML document with `apiVersion: argoproj.io/v1alpha1` and `kind: Workflow`. All templates must be defined in `spec.templates` in that document. One template is the entrypoint DAG; all other invoked templates contain a single container. WorkflowTemplate, templateRef, nested DAGs, and Kubernetes Lists are not supported initially.

The importer produces a Milos WorkflowRun, not an `argoproj.io/Workflow`. The source file is not a manifest that can be applied directly to Milos with kubectl.

## Initial field allowlist

| Location | Allowed fields and constraints |
| --- | --- |
| Root | apiVersion, kind, metadata, spec; runtime status is rejected |
| metadata | Exactly one of name or generateName; namespace, labels, annotations |
| spec | entrypoint, templates, arguments.parameters, parallelism, serviceAccountName, schedulerName, volumes, imagePullSecrets |
| Entrypoint template | name, dag; no template inputs or Pod configuration inheritance |
| dag | tasks, failFast; failFast must explicitly be false |
| dag.tasks[] | name, template, dependencies, arguments.parameters |
| Container template | name, inputs.parameters, container |
| Workflow parameter | name, value; every value must be a string |
| Template input parameter | name, optional string default |
| Task parameter | name, value; the name must match an input of the target template |
| container | image, command, args, env, resources, volumeMounts, workingDir, imagePullPolicy |
| env[] | name and exactly one of value or valueFrom |
| env.valueFrom | secretKeyRef/configMapKeyRef with name, key, optional; other sources are rejected initially |
| resources | requests, limits; resource names are constrained by deployment policy, and values are validated as Kubernetes quantities |
| volumes[] | name; either emptyDir with medium/sizeLimit, or persistentVolumeClaim with claimName/readOnly |
| volumeMounts[] | name, mountPath, readOnly; additional fields such as subPath are not accepted initially |
| imagePullSecrets[] | name |

Reject unknown fields at every level, including nested Kubernetes configuration. Pods have exactly one application container named `main` and `restartPolicy: Never`; arbitrary PodSpec fields are not passed through. Fields such as nodeSelector, affinity, tolerations, and securityContext may be added after their policies and validation are defined. Kubernetes support alone does not make them part of this contract.

Metadata labels and annotations belong to WorkflowRun and are not automatically copied to every Pod. Milos generates identity and scheduling fields; users cannot override them. Initially, schedulerName must be omitted or explicitly set to `default-scheduler`, and the imported value is fixed to `default-scheduler`. Queue, Volcano, and MultiKueue execution configurations are rejected. Future configurations will be enabled only for implemented and validated execution backends, with the resolved execution snapshot persisted at submission. See the [execution abstraction](execution-abstraction.md).

## Parameter evaluation

The first version accepts only simple references of the form `{{workflow.parameters.NAME}}` and `{{inputs.parameters.NAME}}`. NAME follows a restricted identifier grammar. Whitespace variants, expressions, functions, indexing, and nested references are not supported.

1. Bind workflow parameters: explicit submission overrides take precedence over YAML values. Overrides cannot introduce undeclared parameters.
2. Task arguments may reference bound workflow parameters or contain string literals.
3. Bind template inputs: task arguments take precedence over input defaults. Reject missing required inputs.
4. Render container command/args elements and env.value. These fields may reference workflow or input parameters.

Substitution applies only to these string leaves. Do not substitute into the whole YAML document, reparse substituted strings, or recursively interpret template syntax inside parameter values. Images, resource quantities, volumes, task names, and scheduling configuration remain static initially. Unknown references fail before WorkflowRun creation.

This is a restricted interpolation grammar, not an implementation of Argo expressions. Parameters embedded in shell commands follow the application's shell semantics; the example uses direct argv passing.

## DAG and failure rules

Validate that the entrypoint exists, template and task names are unique, dependencies exist, the graph is nonempty, and there are no self-dependencies or cycles. Multiple roots are allowed. Dependencies express control order without requiring fabricated files or output parameters. Every task participates in the workflow's final outcome.

Require explicit `dag.failFast: false`. Argo stops scheduling new tasks after failure by default, whereas Milos initially allows independent branches to continue. Omitting the field must not silently change the meaning: omitted or true values are rejected rather than assigned a different default. [Argo DAG failure policy](https://argo-workflows.readthedocs.io/en/latest/walk-through/dag/)

A task becomes eligible only when all predecessors succeed. A predecessor's final failure propagates Skipped to its descendants while independent branches may continue. An unsuccessful attempt that is still eligible for an application retry does not cause descendants to be skipped.

Cancellation, timeouts, and retries come from Milos executionPolicy; they do not claim to implement the full Argo policies. Fields outside the allowlist, including `retryStrategy`, `activeDeadlineSeconds`, and `shutdown`, must produce errors directing users to the Milos submission policy.

## Unsupported capabilities

Reject `steps`, nested template calls, `depends`, `when`, `continueOn`, `withItems`, `withParam`, `withSequence`, `outputs`, artifacts, script, resource, HTTP, containerSet, sidecars, initContainers, daemon, hooks, onExit, suspend, synchronization, podSpecPatch, templateDefaults, workflowTemplateRef, and templateRef.

Argo stdout results, file-based parameter collection, and `tasks.*.outputs.*` are not supported. Shared storage is an application-level convention. Milos does not transfer files or treat a registered path as proof of data replication.

## Validation errors and input limits

Errors must include a stable code, field path, and reason; source line numbers may also be provided. For example:

```text
UnsupportedField at spec.templates[1].outputs:
argo-pod-dag-v1 does not collect output parameters or artifacts

UnsupportedSemantics at spec.templates[0].dag.failFast:
explicit false is required by argo-pod-dag-v1
```

The proposed parser rejects duplicate YAML keys, multiple documents, aliases/anchors, merge keys, and unexpected tags. Bound input size, nesting depth, parameter length, and node count. After parsing, validate fields, the DAG, parameters, Pod configuration, scheduling policy, and final object size. Submit only after every check passes.

Direct creation of an internal WorkflowRun must receive equivalent validation. Adding fields requires updates to the compatibility profile, allowlist, error fixtures, and semantic comparison tests against a pinned Argo version. `argoproj.io/v1alpha1` is not a substitute for a concrete Argo behavior version.

## Acceptance examples

[diamond.yaml](../examples/diamond.yaml) should import successfully. Making left exit with a nonzero code should verify that finish is skipped while right completes. Reject variants that omit failFast, add outputs, reference missing parameters, introduce cycles or self-dependencies, duplicate task names, or reference nonexistent volumes from volumeMounts.

Use a pinned Argo version to compare parameter binding, dependencies, and failure outcomes within the allowlist. Argo accepting a file does not imply that Milos must accept it. The current example has only undergone document-level checks and has not run through either engine.
