# Small diamond DAG

These examples run four finite, CPU-only steps: `prepare → [left, right] → finish`.
Each step sleeps for five seconds and prints START/DONE timestamps. Parallelism is two;
requests are 10m CPU and 32Mi per Pod, with limits of 100m CPU and 64Mi. No GPU, PVC,
external data or application credentials are needed.

- `argo-diamond.yaml` is runnable by an existing Argo controller with the existing
  `argo-workflow` ServiceAccount. It is a reference, not a Milos input.
- `milos-native.yaml`, `milos-volcano.yaml`, `milos-kueue.yaml` and
  `milos-kueue-volcano.yaml` are manually prepared equivalents for Milos. The importer
  is not implemented, so running these does not validate Argo import compatibility.

The examples target `argo-sim-dev` and use an image observed in the cluster's node
image inventory. Kueue cases reuse `sim-acs-argo-dev`; Volcano cases reuse `default`.
They do not modify the existing queues. Change these explicit environment choices
before running elsewhere. Use `kubectl create` because names are generated uniquely.

After an authorized controller deployment, submit each Milos variant sequentially
with explicit kubeconfig/context. Confirm Succeeded, four distinct bound Pod UIDs,
exit code zero, predecessor finish times before successor start times, and at most
two concurrent workload containers. For queued cases, verify Workload owner/UID and
admission; for Volcano cases, verify schedulerName and owned PodGroup membership.
Read only the test Pods' logs. Keep an inventory of created resource names and UIDs.
Do not delete completed runs until their retained resources have been inspected.

Argo execution is a separate baseline test and does not prove that Milos ran the DAG.

## Verified execution

All four Milos modes and the independent Argo baseline passed CPU smoke tests.
`verify-milos.py` accepts explicit `--kubeconfig`, `--context`,
`--mode` and `--report` arguments; it creates a new run and must only be invoked with
cluster test authorization.

Keep generated reports, logs, resource inventories and operational notes under
`.local/validation/` (ignored by Git). Create the report directory before passing a
path there to `--report`; do not commit environment-specific execution records.
