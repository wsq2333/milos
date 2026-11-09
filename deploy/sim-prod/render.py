#!/usr/bin/env python3
"""Render a dedicated installation without changing existing scheduling components."""
import argparse
from pathlib import Path
import yaml

parser = argparse.ArgumentParser()
parser.add_argument('--image', required=True, help='Published controller image, preferably pinned by digest')
parser.add_argument('--image-pull-secret', help='Existing pull Secret in milos-ws')
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
control_ns, task_ns = 'milos-ws', 'argo-sim-dev'
objects = [
    {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': control_ns}},
    {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': 'milos-controller', 'namespace': control_ns}},
]
subject = [{'kind': 'ServiceAccount', 'name': 'milos-controller', 'namespace': control_ns}]
for role in yaml.safe_load_all((root / 'config/rbac/role.yaml').read_text()):
    original = role['metadata']['name']
    if original == 'milos-multikueue':
        continue
    name = original.replace('milos-', 'milos-ws-', 1)
    role['metadata']['name'] = name
    objects.append(role)
    cluster = original.endswith('-policy')
    metadata = {'name': name}
    if not cluster:
        metadata['namespace'] = task_ns
    objects.append({'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding' if cluster else 'RoleBinding',
                    'metadata': metadata, 'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': name}, 'subjects': subject})
deployment = yaml.safe_load((root / 'deploy/base/deployment.yaml').read_text())
deployment['metadata']['namespace'] = control_ns
pod = deployment['spec']['template']['spec']
if args.image_pull_secret:
    pod['imagePullSecrets'] = [{'name': args.image_pull_secret}]
container = pod['containers'][0]
container['image'] = args.image
container['args'] = ['--namespace=' + task_ns, '--leader-elect=true', '--enable-kueue', '--enable-volcano', '--metrics-bind-address=0', '--health-probe-bind-address=:8081']
objects.append(deployment)
print(yaml.safe_dump_all(objects, sort_keys=False))
