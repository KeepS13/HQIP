"""Deploy only the independently signed preview using the existing CI identity."""
import argparse
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile

from flutter_channel import digest, extract_bundle, verify_bundle
from verify_flutter_public import baseline, verify_public

ROOT = Path(__file__).resolve().parents[1]
REMOTE_PYTHON = '/opt/hqip-runtime/bin/python3'


def checked_main():
    sha = os.environ['GITHUB_SHA']
    repo = os.environ['GITHUB_REPOSITORY']
    if os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        raise ValueError('Preview publication requires explicit dispatch on main')
    tip = subprocess.check_output(['gh', 'api', f'repos/{repo}/commits/main', '--jq', '.sha'], text=True).strip()
    if sha != tip:
        raise ValueError('Main changed; dispatch a new publication')
    data = json.loads(subprocess.check_output(['gh', 'api', f'repos/{repo}/actions/workflows/ci.yml/runs?head_sha={sha}&event=push&per_page=20'], text=True))
    runs = [r for r in data['workflow_runs'] if r['head_sha'] == sha and r['head_branch'] == 'main']
    if not runs or max(runs, key=lambda r: r['id'])['conclusion'] != 'success':
        raise ValueError('The current main commit must pass CI before publication')
    return sha


def reusable_source(run_id, deployment_sha):
    if not re.fullmatch(r'[0-9]+', run_id):
        raise ValueError('Invalid source build run')
    repo = os.environ['GITHUB_REPOSITORY']
    run = json.loads(subprocess.check_output(['gh', 'api', f'repos/{repo}/actions/runs/{run_id}'], text=True))
    if (run['event'] != 'workflow_dispatch' or run['head_branch'] != 'main' or
            run['path'] != '.github/workflows/flutter-preview.yml' or run['status'] != 'completed'):
        raise ValueError('Reuse requires a completed trusted main preview build')
    jobs = json.loads(subprocess.check_output(['gh', 'api', f'repos/{repo}/actions/runs/{run_id}/jobs?per_page=100'], text=True))['jobs']
    if not any(j['name'] == 'build' and j['conclusion'] == 'success' for j in jobs):
        raise ValueError('Source preview build did not pass')
    source = run['head_sha']
    comparison = json.loads(subprocess.check_output(['gh', 'api', f'repos/{repo}/compare/{source}...{deployment_sha}'], text=True))
    if comparison['status'] not in ['ahead', 'identical']:
        raise ValueError('Source build must belong to main history')
    ci = json.loads(subprocess.check_output(['gh', 'api', f'repos/{repo}/actions/workflows/ci.yml/runs?head_sha={source}&event=push&per_page=20'], text=True))['workflow_runs']
    if not any(r['head_sha'] == source and r['head_branch'] == 'main' and r['conclusion'] == 'success' for r in ci):
        raise ValueError('Source commit must have passed main CI')
    return source


def main(archive, rollback_run=None, publication_run=None):
    deployment_sha = checked_main()
    sha = reusable_source(publication_run, deployment_sha) if publication_run else deployment_sha
    host, port, user = (os.environ[n] for n in ['DEPLOY_HOST', 'DEPLOY_PORT', 'DEPLOY_USER'])
    if (host, port, user) != ('64.83.17.143', '22', 'root'):
        raise ValueError('Unexpected production target')
    public = (ROOT / 'packaging/flutter-update-public-key.txt').read_text().strip()
    run_id = os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
    if rollback_run and not re.fullmatch(r'[0-9]+-[0-9]+', rollback_run):
        raise ValueError('Invalid rollback run')
    with tempfile.TemporaryDirectory(prefix='hqip-preview-ci-') as folder:
        work = Path(folder)
        bundle = work / 'bundle'
        if not rollback_run:
            extract_bundle(archive, bundle)
            verify_bundle(bundle, public, sha)
        key, known = work / 'key', work / 'known_hosts'
        key.write_text(os.environ['DEPLOY_SSH_KEY'].strip() + '\n')
        key.chmod(0o600)
        known.write_text(os.environ['DEPLOY_KNOWN_HOSTS'].strip() + '\n')
        common = ['-i', str(key), '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                  '-o', 'UserKnownHostsFile=' + str(known), '-o', 'GlobalKnownHostsFile=/dev/null', '-o', 'UpdateHostKeys=no',
                  '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=4']
        ssh = ['ssh', *common, '-p', port, user + '@' + host]

        def remote(args):
            process = subprocess.run([*ssh, shlex.join(args)], capture_output=True, text=True)
            if process.returncode:
                print(process.stderr, file=sys.stderr)
                process.check_returncode()
            return process.stdout

        incoming = remote(['mktemp', '-d', '/tmp/hqip-preview.XXXXXXXXXX']).strip()
        if not re.fullmatch(r'/tmp/hqip-preview\.[A-Za-z0-9]{10}', incoming):
            raise ValueError('Unsafe remote staging path')
        installed = False
        try:
            sources = [ROOT / 'scripts' / name for name in ['install_flutter_preview.py', 'flutter_channel.py', 'render_domains.py']]
            if not rollback_run:
                sources.append(archive)
            subprocess.run(['scp', *common, '-P', port, *map(str, sources), user + '@' + host + ':' + incoming + '/'], check=True)
            command = [REMOTE_PYTHON, '-B', incoming + '/install_flutter_preview.py']
            options = ['--public-key', public, '--sha', sha, '--run-id', rollback_run or run_id]
            before = baseline()
            report_dir = ROOT / 'dist/flutter-preview-acceptance'
            report_dir.mkdir(parents=True, exist_ok=True)
            (report_dir / 'official-before.json').write_text(json.dumps(before, indent=2))
            origin_before = json.loads(remote([*command, 'preflight', *options]))
            if rollback_run:
                result = json.loads(remote([*command, 'rollback', *options]))
                if baseline() != before:
                    raise ValueError('Official channel changed during rollback')
            else:
                result = json.loads(remote([*command, 'activate', *options, '--archive', incoming + '/' + archive.name]))
                installed = True
                acceptance = verify_public(bundle, public, before)
                if json.loads(remote([*command, 'preflight', *options])) != origin_before:
                    raise ValueError('Control release or process changed during publication')
                result.update(acceptance)
            result.update(control=origin_before, run=run_id, sourceSHA=sha, deploymentSHA=deployment_sha,
                          sourceBuildRun=publication_run or os.environ['GITHUB_RUN_ID'])
            (report_dir / 'receipt.json').write_text(json.dumps(result, indent=2))
            (report_dir / 'official-after.json').write_text(json.dumps(baseline(), indent=2))
            print(json.dumps(result, indent=2))
            if os.environ.get('GITHUB_STEP_SUMMARY'):
                with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as f:
                    f.write(f'Flutter preview: `{sha}`; transaction `{run_id}`; phase `{result["phase"]}`.\n\nOfficial channel and control process unchanged.\n')
        except Exception:
            if installed:
                print('Acceptance failed; restoring previous preview pointer and Nginx configuration.', flush=True)
                print(remote([*command, 'rollback', *options]))
            raise
        finally:
            remote(['rm', '-rf', '--', incoming])


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--archive', type=Path, default=ROOT / 'dist/flutter-preview-publication.tar.gz')
    p.add_argument('--rollback-run')
    p.add_argument('--publication-run')
    p.add_argument('--check-only', action='store_true')
    a = p.parse_args()
    print(checked_main()) if a.check_only else main(a.archive, a.rollback_run, a.publication_run)
