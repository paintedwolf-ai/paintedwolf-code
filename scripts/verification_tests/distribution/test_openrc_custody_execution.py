import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]


class OpenRCCustodyExecutionTest(unittest.TestCase):
    def service_fixture(self, root):
        tools = root / "tools"
        tools.mkdir()
        owners = root / "owners.json"
        owners.write_text("{}")
        scripts = {
            "getent": '''#!/usr/bin/env python3
import os, sys
if sys.argv[1] == 'passwd':
 print('paintedwolf-vault:x:12345:12345:service:/var/empty:'+os.environ.get('FIXTURE_LOGIN_SHELL','/usr/bin/nologin'))
else: print('paintedwolf-vault:x:12345:')
''',
            "stat": '''#!/usr/bin/env python3
import json, os, sys
value=json.load(open(os.environ['FIXTURE_OWNERS'])).get(sys.argv[3], {})
st=os.stat(sys.argv[3])
print(value.get(sys.argv[2], st.st_uid if sys.argv[2]=='%u' else st.st_gid))
''',
            "install": '''#!/usr/bin/env python3
import json, os, pathlib, sys
args=sys.argv[1:]; path=pathlib.Path(args[-1]); path.mkdir(parents=True); path.chmod(int(args[args.index('-m')+1],8))
file=pathlib.Path(os.environ['FIXTURE_OWNERS']); owners=json.loads(file.read_text()); owners[str(path)]={'%u':int(args[args.index('-o')+1]),'%g':int(args[args.index('-g')+1])}; file.write_text(json.dumps(owners))
''',
            "setpriv": "#!/bin/sh\nexit 0\n",
            "useradd": "#!/bin/sh\nexit 99\n",
            "groupadd": "#!/bin/sh\nexit 99\n",
        }
        for name, content in scripts.items():
            path = tools / name
            path.write_text(content)
            path.chmod(0o755)
        engine = root / "engine"
        engine.write_text("#!/bin/sh\nexit 0\n")
        engine.chmod(0o755)
        service = (ROOT / "packaging/linux/openrc/paintedwolf-vault-identity").read_text()
        for name in scripts:
            service = service.replace("/usr/bin/" + name, str(tools / name))
        service = service.replace("/usr/bin/chmod", shutil.which("chmod"))
        service = service.replace("/var/lib/paintedwolf-vault-openrc", str(root / "state"))
        service = service.replace("/run/painted-wolf-code", str(root / "runtime"))
        service = service.replace("/opt/painted-wolf-code/bin/pw-vault-identity", str(engine))
        file = root / "service.sh"
        file.write_text(service + '\neerror() { printf "%s\\n" "$*" >&2; }\nstart_pre\n')
        return file, dict(os.environ, FIXTURE_OWNERS=str(owners)), owners

    def test_private_state_survives_restart_and_wrong_ownership_is_not_reassigned(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            file, env, owners = self.service_fixture(root)
            for _ in range(2):
                result = subprocess.run(["sh", str(file)], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "state").stat().st_mode & 0o777, 0o700)
            self.assertEqual((root / "runtime").stat().st_mode & 0o777, 0o755)
            identity = root / "state/identity"
            identity.write_text("preserve durable credentials")
            values = json.loads(owners.read_text())
            values[str(root / "state")]["%u"] = 54321
            owners.write_text(json.dumps(values))
            before = owners.read_text()
            result = subprocess.run(["sh", str(file)], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Refusing to reassign", result.stderr)
            self.assertEqual(identity.read_text(), "preserve durable credentials")
            self.assertEqual(owners.read_text(), before)

    def test_existing_login_account_cannot_become_the_custodian(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            file, env, _ = self.service_fixture(root)
            env["FIXTURE_LOGIN_SHELL"] = "/bin/sh"
            result = subprocess.run(["sh", str(file)], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root / "state").exists())

    def test_package_hooks_restart_active_openrc_without_reenabling_a_disabled_service(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "tools"
            tools.mkdir()
            (root / "openrc").mkdir()
            calls = root / "calls"
            for name in ("rc-service", "rc-update"):
                file = tools / name
                file.write_text('#!/bin/sh\nprintf "%s %s\\n" "' + name + '" "$*" >> "$FIXTURE_CALLS"\nif [ "$2" = status ]; then exit "$FIXTURE_ACTIVE"; fi\n')
                file.chmod(0o755)
            hook = (ROOT / "packaging/linux/arch/painted-wolf-code.install").read_text()
            hook = hook.replace("/run/systemd/system", str(root / "systemd"))
            hook = hook.replace("/run/openrc", str(root / "openrc"))
            file = root / "hook.sh"
            file.write_text(hook + '\n"$1"\n')
            env = dict(os.environ, PATH=str(tools), FIXTURE_CALLS=str(calls), FIXTURE_ACTIVE="0")
            result = subprocess.run([shutil.which("sh"), str(file), "post_upgrade"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("rc-service paintedwolf-vault-identity restart", calls.read_text())
            self.assertNotIn("rc-update", calls.read_text())
            calls.write_text("")
            env["FIXTURE_ACTIVE"] = "1"
            subprocess.run([shutil.which("sh"), str(file), "post_upgrade"], env=env, check=True, capture_output=True)
            self.assertNotIn("restart", calls.read_text())

    def test_openrc_fragments_are_only_shipped_with_pacman(self):
        spec = importlib.util.spec_from_file_location("custody_package_config", ROOT / "scripts/linux-package-config.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        path = "/etc/init.d/paintedwolf-vault-identity"
        self.assertIn(path, module.optional_files(ROOT, "pacman"))
        self.assertNotIn(path, module.optional_files(ROOT, "deb"))
        self.assertNotIn(path, module.optional_files(ROOT, "rpm"))
