"""Exercise the remote runner with fake systemd/probes; no network or root.

The actual untrusted diagnostic is never executed by these tests. Adapted
standalone helper checks use fake DNS and contain no upstream main routine.
"""
from contextlib import ExitStack
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / 'internal/provision/ipquality'
spec = importlib.util.spec_from_file_location('quality_runtime', ROOT / 'internal/provision/network_quality.py')
runtime = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {'pwd': SimpleNamespace(getpwnam=lambda name: SimpleNamespace(pw_uid=65534, pw_gid=65534))}):
    spec.loader.exec_module(runtime)


class QualityRuntimeTests(unittest.TestCase):
    def payload(self):
        return {'ip': '203.0.113.8', 'runId': 'a' * 24,
                'files': {'ip.sh': base64.b64encode(b'# fake diagnostic, never executed').decode()}}

    def execute(self, report_ip='203.0.113.8', result='success', expire=False, source_present=True, ansi=b'\x1b[32mIP quality\x1b[0m\n', nat_source=None, nat_exit='203.0.113.8', route_source=None):
        with tempfile.TemporaryDirectory() as folder, ExitStack() as stack:
            root = Path(folder) / 'quality'
            original_path = Path
            # The runner targets Linux; Windows treats mode 0444 as an ACL-like
            # read-only flag that prevents unlink, unlike Linux file modes.
            if os.name == 'nt':
                stack.enter_context(patch.object(original_path, 'chmod'))
            stack.enter_context(patch.object(runtime.pathlib, 'Path', side_effect=lambda value: root if str(value) == '/var/lib/hqip-quality-tmp' else original_path(value)))
            stack.enter_context(patch.object(runtime.os, 'chown', create=True))
            stack.enter_context(patch.object(runtime.shutil, 'which', side_effect=lambda name: '/usr/bin/' + name))
            calls = []

            def fake_run(command, **kwargs):
                calls.append((command, kwargs))
                if command[:5] == ['ip','-j','-4','route','get']:
                    return SimpleNamespace(returncode=0, stdout=json.dumps([{'prefsrc': route_source or nat_source or ''}]).encode())
                if command[0] == 'ip':
                    addresses = [{'addr_info': [{'local': nat_source or self.payload()['ip']}]}] if source_present else []
                    return SimpleNamespace(returncode=0, stdout=json.dumps(addresses).encode())
                if command[0] == 'curl':
                    return SimpleNamespace(returncode=0, stdout=nat_exit)
                if command[0] == 'systemd-run':
                    output = original_path(command[-1])
                    output.write_text(json.dumps({'Head': {'IP': report_ip}, 'Info': {}, 'Score': {}}))
                    if ansi is not None:
                        ansi_path = next(value.split('=', 1)[1] for value in command if value.startswith('HQIP_REPORT_ANSI='))
                        original_path(ansi_path).write_bytes(ansi)
                    return SimpleNamespace(returncode=0, stdout=b'')
                if command[:2] == ['systemctl', 'show']:
                    return SimpleNamespace(returncode=0, stdout=('inactive' if '--property=ActiveState' in command else result))
                return SimpleNamespace(returncode=0, stdout='')

            stack.enter_context(patch.object(runtime.subprocess, 'run', side_effect=fake_run))
            if expire:
                stack.enter_context(patch.object(runtime.time, 'monotonic', side_effect=[0, 316]))
            answer = runtime.run(self.payload())
            self.assertFalse(root.exists() and list(root.iterdir()), 'Transient source or report left behind')
            return answer, calls

    def test_private_original_report_is_preserved_and_optional(self):
        answer, _ = self.execute(ansi='\x1b[32m原始报告\x1b[0m\n'.encode())
        self.assertEqual(answer['ansi'], '\x1b[32m原始报告\x1b[0m\n')
        for ansi in [None, b'\xff', b'x' * (64 * 1024 + 1)]:
            answer, _ = self.execute(ansi=ansi)
            self.assertIn('report', answer)
            self.assertNotIn('ansi', answer)

    def test_fixed_unprivileged_command_bound_ip_limits_and_cleanup(self):
        answer, calls = self.execute()
        self.assertEqual(answer['report']['Head']['IP'], self.payload()['ip'])
        command, options = next(call for call in calls if call[0][0] == 'systemd-run')
        self.assertNotIn('shell', options)
        self.assertEqual(options['timeout'], 20)
        for prop in ['User=65534', 'Group=65534', 'CapabilityBoundingSet=', 'NoNewPrivileges=yes', 'ProtectHome=yes', 'ProtectSystem=strict', 'MemoryMax=192M', 'TasksMax=80', 'CPUQuota=50%', 'RuntimeMaxSec=300', 'KillMode=control-group', 'StandardOutput=null', 'StandardError=null']:
            self.assertIn('--property=' + prop, command)
        self.assertIn('-i', command)
        self.assertEqual(command[command.index('-i', command.index('bash')) + 1], self.payload()['ip'])
        for flag in ['-4', '-n', '-p', '-f', '-j']:
            self.assertIn(flag, command)
        self.assertEqual(command[command.index('/usr/bin/env') + 1], '-i')
        self.assertEqual(calls[-2][0][:2], ['systemctl', 'stop'])
        self.assertEqual(calls[-1][0][:2], ['systemctl', 'reset-failed'])

    def test_wrong_observed_ip_rejected_and_cleaned(self):
        answer, calls = self.execute(report_ip='203.0.113.9')
        self.assertEqual(answer, {'error': 'invalid'})
        self.assertIn(['systemctl', 'stop', 'hqip-quality-' + 'a' * 24], [x[0] for x in calls])

    def test_timeout_always_stops_whole_service(self):
        answer, calls = self.execute(expire=True)
        self.assertEqual(answer, {'error': 'timeout'})
        self.assertEqual(calls[-2][0][:2], ['systemctl', 'stop'])

    def test_core_resource_failure_never_reports_success(self):
        answer, _ = self.execute(result='oom-kill')
        self.assertEqual(answer, {'error': 'execution'})

    def test_unbound_source_never_launches_script(self):
        answer, calls = self.execute(source_present=False)
        self.assertEqual(answer, {'error': 'source'})
        self.assertFalse(any(c[0][0] == 'systemd-run' for c in calls))

    def test_nat_binds_owned_private_source_but_preserves_public_report_identity(self):
        answer, calls = self.execute(nat_source='10.0.186.2')
        self.assertEqual(answer['report']['Head']['IP'], self.payload()['ip'])
        command = next(c[0] for c in calls if c[0][0] == 'systemd-run')
        self.assertEqual(command[command.index('-i', command.index('bash'))+1], '10.0.186.2')
        self.assertIn('HQIP_EXPECTED_IP='+self.payload()['ip'], command)
        probe = next(c[0] for c in calls if c[0][0] == 'curl')
        self.assertEqual(probe[probe.index('--interface')+1], '10.0.186.2')
        self.assertIn('--noproxy', probe)

    def test_nat_wrong_exit_or_unowned_route_fails_before_launch(self):
        for kwargs in [{'nat_exit':'203.0.113.9'}, {'nat_exit':''}, {'route_source':'10.0.186.99'}]:
            answer, calls = self.execute(nat_source='10.0.186.2', **kwargs)
            self.assertEqual(answer, {'error':'source'})
            self.assertFalse(any(c[0][0]=='systemd-run' for c in calls))

    def test_missing_dependencies_do_not_launch_or_download(self):
        with patch.object(runtime.shutil, 'which', return_value=None), patch.object(runtime.subprocess, 'run') as run:
            self.assertEqual(runtime.run(self.payload()), {'error': 'dependencies'})
            run.assert_not_called()


class QualityAssetTests(unittest.TestCase):
    def test_pinned_assets_and_source_provenance(self):
        info = json.loads((ASSETS / 'UPSTREAM.json').read_text())
        self.assertEqual(info['license'], 'AGPL-3.0')
        self.assertEqual(len(info['commit']), 40)
        for name, digest in info['files'].items():
            self.assertEqual(hashlib.sha256((ASSETS / name).read_bytes()).hexdigest(), digest, name)
        self.assertEqual(hashlib.sha256((ASSETS / 'upstream/ip.sh').read_bytes()).hexdigest(), info['upstreamScriptSHA256'])

    def test_adaptation_has_no_remote_code_or_report_upload(self):
        script = (ASSETS / 'ip.sh').read_text(encoding='utf-8')
        for dangerous in ['eval ', 'upload.check.place', 'hits.xykt.de', '${rawgithub}', 'install_dependencies']:
            self.assertNotIn(dangerous, script)
        self.assertIn('command curl -q --interface "$iface" --ipv4', script)
        self.assertIn('--proto \'=https\' --proto-redir \'=https\'', script)
        self.assertIn('[[ "$IPV4" == "${HQIP_EXPECTED_IP:-$iface}" ]] || ERRORcode=7', script)
        self.assertIn('smtp-not-tested', script)

    def test_browser_definition_survives_removed_installer(self):
        bash = shutil.which('bash') if os.name != 'nt' else 'C:/Program Files/Git/bin/bash.exe'
        if not bash or not Path(bash).is_file():
            self.skipTest('Bash not installed')
        script = (ASSETS / 'ip.sh').read_text(encoding='utf-8')
        # Only pure in-memory UA helper, not the upstream script entry point.
        helper = script[script.index('declare -A browsers='):script.index('adapt_locale(){')]
        result = subprocess.run([bash, '-c', helper + '\ngenerate_random_user_agent\nprintf "%s" "$UA_Browser"'], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertRegex(result.stdout, r'Mozilla/5\.0 .* (Chrome|Firefox)/[0-9]')

    def test_real_script_option_parser_distinguishes_nat_source_and_public_exit(self):
        bash = shutil.which('bash') if os.name != 'nt' else 'C:/Program Files/Git/bin/bash.exe'
        if not bash or not Path(bash).is_file():
            self.skipTest('Bash not installed')
        script = (ASSETS / 'ip.sh').read_text(encoding='utf-8')
        helper = script[script.index('get_opts(){'):script.index('show_help(){')]
        # Execute the actual parser with a fake HTTP observer: this covers both
        # its per-option and final exit checks without contacting third parties.
        setup = '''
is_valid_ipv4(){ [[ "$1" =~ ^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; }
get_ipv4(){ IPV4="$2"; IPV4work=1; }
'''
        for source, expected, observed, error in [
            ('10.0.186.2','45.205.28.141','45.205.28.141',0),
            ('10.0.186.2','45.205.28.141','45.205.28.142',7),
            ('45.205.28.141','','45.205.28.141',0),
            ('45.205.28.141','','45.205.28.142',7),
        ]:
            command = setup + helper + '\nERRORcode=0; HQIP_EXPECTED_IP="$2"; observed="$3"; get_ipv4(){ IPV4="$observed"; IPV4work=1; }; get_opts -i "$1" -4; printf "%s" "$ERRORcode"'
            result = subprocess.run([bash,'-c',command,'_',source,expected,observed],capture_output=True,text=True,timeout=5)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(result.stdout,str(error),(source,expected,observed))

    def test_dnsbl_unknown_is_not_clean_and_input_is_not_shell_code(self):
        bash = shutil.which('bash') if os.name != 'nt' else 'C:/Program Files/Git/bin/bash.exe'
        if not bash or not Path(bash).is_file():
            self.skipTest('Bash not installed')
        script = (ASSETS / 'ip.sh').read_text(encoding='utf-8')
        helper = script[script.index('check_dnsbl_parallel(){'):script.index('check_dnsbl(){')]
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'ref').mkdir()
            (root / 'bin').mkdir()
            (root / 'ref/dnsbl.list').write_text('clean.test\nblocked.test\nerror.test\nmarked.test\n$(touch injected)\n')
            dig = root / 'bin/dig'
            dig.write_text('''#!/usr/bin/env bash
[[ "$1" == -b && "$2" == "$TEST_SOURCE" ]] || exit 99
[[ "${@: -2:1}" == 8.113.0.203.* ]] || exit 97
case "${@: -2:1}" in
 *.clean.test) echo ';; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN, id: 1' ;;
 *.error.test) echo ';; ->>HEADER<<- opcode: QUERY, status: SERVFAIL, id: 1' ;;
 *.blocked.test) printf ';; status: NOERROR,\nx. 1 IN A 127.0.0.2\n' ;;
 *.marked.test) printf ';; status: NOERROR,\nx. 1 IN A 127.0.0.4\n' ;;
 *) exit 98 ;;
esac
''', newline='\n')
            dig.chmod(0o755)
            setup = '\nroot=$(cd "$1" && pwd); HQIP_REF="$root/ref"; export PATH="$root/bin:$PATH"; iface="$2"; export TEST_SOURCE="${iface:-203.0.113.8}"; check_dnsbl_parallel 203.0.113.8 50'
            for source in ['', '10.0.186.2']:
                result = subprocess.run([bash, '-c', helper + setup, '_', root.as_posix(), source], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), '4 1 1 1 1', source)
            self.assertFalse((root / 'injected').exists())


if __name__ == '__main__':
    unittest.main()
