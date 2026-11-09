# Sim prod smoke deployment

The controller lives in `milos-ws` and reconciles only `argo-sim-dev`. Its lease also
lives in `argo-sim-dev`, matching the current controller's namespace flag. The
controller ServiceAccount is in `milos-ws`; namespace-scoped RoleBindings in
`argo-sim-dev` refer to it. ClusterRole names are prefixed `milos-ws-` to avoid
collisions. MultiKueue is not enabled.

Render on the designated developer machine (Python 3 and PyYAML):

```sh
python3 deploy/sim-prod/render.py --image YOUR_REGISTRY/milos@sha256:YOUR_DIGEST > /tmp/milos-sim-prod.yaml
```

Before installation, verify every named target is absent. Install the Milos CRD
separately using `kubectl create -f config/crd/bases/` only if it is absent, wait for
Established, then create the rendered manifest with the explicitly selected sim prod
kubeconfig/context. Do not overwrite existing objects. Existing `argo-sim-dev`,
Kueue, Volcano, queues, flavors and application service accounts are reused unchanged.

Run the four native WorkflowRun examples in `example/` sequentially. The Argo YAML
is an equivalent reference; Milos currently has no Argo importer. Record the exact
image digest, new resource names/UIDs, controller readiness, DAG outcome, timestamps,
Pod exits, Workload admission and PodGroup membership. An empty acceptance table is
not evidence of a successful deployment. Registry access and an image publication
must be ready before creating cluster resources.

## Verified deployment

The dedicated installation passed the CPU smoke tests in [example/](../../example/README.md).
GPU, MultiKueue and production-scale acceptance remain pending.
For a private image, pass `--image-pull-secret NAME` to the renderer after preparing
the dedicated Secret in `milos-ws`; credential values must never enter these files.

Keep generated reports, logs, resource inventories and operational notes under
`.local/validation/` (ignored by Git). Create the report directory before passing a
path there to `--report`; do not commit environment-specific execution records.
