"""Exercise release classification without network, registry, or GitHub writes."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / 'scripts/publish-release.sh'
MOCK_TOOL = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['MOCK_CALLS'], 'a') as log:
    log.write(json.dumps([tool, *args]) + '\n')
if tool == 'curl':
    pathlib.Path(args[args.index('--output') + 1]).write_bytes(b'mock-download')
elif tool == 'tar':
    target = pathlib.Path(args[args.index('-C') + 1]) / 'linux-amd64/helm'
    target.parent.mkdir(parents=True)
    target.write_text(pathlib.Path(sys.argv[0]).read_text()); target.chmod(0o755)
elif tool == 'helm':
    if args[0] == 'package':
        version = args[args.index('--version') + 1]
        pathlib.Path('dist', 'agents-' + version + '.tgz').write_bytes(b'mock-chart')
    elif args[:2] == ['registry', 'login']:
        sys.stdin.read()  # Consume the dummy token without logging it.
    elif args[0] != 'push':
        sys.exit('unexpected helm operation')
elif tool == 'sha256sum':
    if args[0] not in ('-c', '--check'):
        for name in args:
            print(hashlib.sha256(pathlib.Path(name).read_bytes()).hexdigest() + '  ' + name)
elif tool == 'gh':
    if args[:2] == ['release', 'view']:
        sys.exit(0 if os.environ.get('MOCK_EXISTING_RELEASE') == 'true' else 1)
    if args[:2] not in (['release', 'create'], ['release', 'upload']):
        sys.exit('unexpected GitHub operation')
else:
    sys.exit('unexpected tool')
'''


@unittest.skipUnless(shutil.which('bash'), 'release script requires bash')
class PublishReleaseTest(unittest.TestCase):
    def run_publish(self, tag, existing=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / 'bin'; binary.mkdir()
            for name in ('curl', 'tar', 'helm', 'sha256sum', 'gh'):
                path = binary / name
                path.write_text(MOCK_TOOL); path.chmod(0o755)
            calls = root / 'calls.jsonl'
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ.get('PATH', ''),
                       RELEASE_TAG=tag, GH_TOKEN='test-only-token', GITHUB_ACTOR='test-actor',
                       MOCK_CALLS=str(calls), MOCK_EXISTING_RELEASE=str(existing).lower())
            subprocess.run(['bash', str(SCRIPT)], cwd=root, env=env, check=True, capture_output=True, text=True)
            return [json.loads(line) for line in calls.read_text().splitlines()], (root / 'dist/IMAGES').read_text()

    def test_prerelease_is_not_latest_and_uses_matching_chart_and_images(self):
        for tag in ('v0.1.0-rc.1', 'v1.2.3-beta.2'):
            with self.subTest(tag=tag):
                calls, images = self.run_publish(tag)
                create = next(call for call in calls if call[:3] == ['gh', 'release', 'create'])
                self.assertIn('--prerelease', create)
                self.assertIn('--latest=false', create)
                self.assertIn('dist/agents-' + tag[1:] + '.tgz', create)
                package = next(call for call in calls if call[:2] == ['helm', 'package'])
                self.assertEqual(package[package.index('--version') + 1], tag[1:])
                self.assertEqual(package[package.index('--app-version') + 1], tag)
                self.assertEqual(images.splitlines(), [f'ghcr.io/bit-precise/agents-{component}:{tag}'
                                                       for component in ('control', 'edge', 'console')])

    def test_stable_release_keeps_default_classification(self):
        calls, _ = self.run_publish('v0.1.0')
        create = next(call for call in calls if call[:3] == ['gh', 'release', 'create'])
        self.assertNotIn('--prerelease', create)
        self.assertFalse(any(argument.startswith('--latest') for argument in create))

    def test_existing_release_still_only_updates_assets(self):
        calls, _ = self.run_publish('v0.1.0-rc.1', existing=True)
        self.assertFalse(any(call[:3] == ['gh', 'release', 'create'] for call in calls))
        upload = next(call for call in calls if call[:3] == ['gh', 'release', 'upload'])
        self.assertIn('--clobber', upload)


if __name__ == '__main__':
    unittest.main()
