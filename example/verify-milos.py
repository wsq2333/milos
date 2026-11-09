#!/usr/bin/env python3
"""Create and verify one small Milos DAG in an explicitly selected cluster."""
import argparse
import datetime
import json
from pathlib import Path
import subprocess
import time
import yaml

parser = argparse.ArgumentParser()
parser.add_argument('--kubeconfig', required=True)
parser.add_argument('--context', required=True)
parser.add_argument('--mode', choices=['native', 'volcano', 'kueue', 'kueue-volcano'], required=True)
parser.add_argument('--report', required=True)
args = parser.parse_args()
base = ['kubectl', '--kubeconfig', args.kubeconfig, '--context', args.context, '--request-timeout=20s', '-n', 'argo-sim-dev']

def command(parts, data=None):
    p = subprocess.run(base + parts, input=data, capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError(p.stderr)
    return p.stdout

def get(kind, name):
    return json.loads(command(['get', kind, name, '-o', 'json']))

def resources(kind, selector):
    return json.loads(command(['get', kind, '-l', selector, '-o', 'json']))['items']

run = json.loads(command(['create', '-f', str(Path(__file__).with_name('milos-' + args.mode + '.yaml')), '-o', 'json']))
name, uid = run['metadata']['name'], run['metadata']['uid']
report = {'mode': args.mode, 'namespace': 'argo-sim-dev', 'name': name, 'uid': uid, 'checks': {}, 'resources': []}
report_path = Path(args.report)
def save():
    report_path.write_text(json.dumps(report, indent=2))
save()
print('CREATED ' + name, flush=True)
deadline = time.monotonic() + 360
last = None
try:
    while time.monotonic() < deadline:
        run = get('workflowruns.milos.example.io', name)
        state = run.get('status', {})
        progress = (state.get('phase'), tuple((k, v.get('phase'), v.get('reason')) for k, v in sorted(state.get('nodes', {}).items())))
        if progress != last:
            print(json.dumps({'run': name, 'progress': progress}), flush=True)
            last = progress
        if state.get('phase') in ['Succeeded', 'Failed', 'Cancelled']:
            break
        time.sleep(2)
    report['status'] = run.get('status', {})
    assert report['status'].get('phase') == 'Succeeded', report['status']
    report['checks']['workflowSucceeded'] = True
    pods = resources('pods', 'milos.example.io/workflow-uid=' + uid)
    assert len(pods) == 4, len(pods)
    by_uid = {p['metadata']['uid']: p for p in pods}
    times = {}
    def record(o):
        m = o['metadata']
        report['resources'].append({'kind': o['kind'], 'name': m['name'], 'uid': m['uid']})
    for node, status in report['status']['nodes'].items():
        pod = by_uid[status['podUID']]
        record(pod)
        assert pod['status']['phase'] == 'Succeeded'
        assert any(o['uid'] == uid and o.get('controller') for o in pod['metadata'].get('ownerReferences', []))
        terminated = next(c for c in pod['status']['containerStatuses'] if c['name'] == 'main')['state']['terminated']
        assert terminated['exitCode'] == 0
        times[node] = {'startedAt': terminated['startedAt'], 'finishedAt': terminated['finishedAt']}
        report.setdefault('logs', {})[node] = command(['logs', pod['metadata']['name'], '-c', 'main', '--tail=10'])
        if 'kueue' in args.mode:
            workload = get('workloads.kueue.x-k8s.io', status['workloadName'])
            record(workload)
            assert workload['metadata']['uid'] == status['workloadUID']
            assert workload['spec']['queueName'] == 'sim-acs-argo-dev'
            assert any(c['type'] == 'Admitted' and c['status'] == 'True' for c in workload['status']['conditions'])
        if 'volcano' in args.mode:
            assert pod['spec']['schedulerName'] == 'volcano'
            group = get('podgroups.scheduling.volcano.sh', pod['metadata']['name'])
            record(group)
            assert group['metadata']['uid'] == status['podGroupUID']
            assert pod['metadata']['annotations']['scheduling.k8s.io/group-name'] == group['metadata']['name']
    parse = lambda value: datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    for node, deps in [('left', ['prepare']), ('right', ['prepare']), ('finish', ['left', 'right'])]:
        assert all(parse(times[node]['startedAt']) >= parse(times[d]['finishedAt']) for d in deps)
    events = sorted((parse(v[k]), delta) for v in times.values() for k, delta in [('startedAt', 1), ('finishedAt', -1)])
    active = peak = 0
    for _, delta in events:
        active += delta
        peak = max(peak, active)
    assert peak <= 2
    report['checks'].update(fourUniquePods=True, zeroExitCodes=True, dependencyOrder=True, maximumConcurrentContainers=peak)
    report['times'] = times
    if 'kueue' in args.mode:
        report['checks']['workloadAdmissionAndIdentity'] = True
    if 'volcano' in args.mode:
        report['checks']['volcanoSchedulerAndPodGroupIdentity'] = True
    print('PASSED ' + name + ' ' + json.dumps(report['checks']), flush=True)
except Exception as exc:
    report['error'] = str(exc)
    raise
finally:
    save()
