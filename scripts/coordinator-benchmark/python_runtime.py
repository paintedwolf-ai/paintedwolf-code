"""Bootstrap the benchmark's pinned Python dependencies without changing the host."""
import fcntl
import hashlib
import os
import pathlib
import subprocess
import sys
import venv


def runtime_identity(requirements):
    base = pathlib.Path(sys._base_executable).resolve()
    fingerprint = hashlib.sha256(base.read_bytes()).hexdigest()
    return hashlib.sha256(requirements + sys.version.encode() + fingerprint.encode()).hexdigest()[:16]


def main():
    root=pathlib.Path(__file__).resolve().parents[2]
    requirements=pathlib.Path(__file__).with_name('requirements.txt')
    digest=runtime_identity(requirements.read_bytes())
    sys.path.append(str(root/'scripts'))
    from artifact_paths import bin_dir
    environment=bin_dir(root)/('coordinator-python-'+digest)
    environment.parent.mkdir(parents=True,exist_ok=True)
    python=environment/'bin/python3'
    marker=environment/'ready'
    with (environment.parent/(environment.name+'.lock')).open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        if not marker.exists():
            venv.EnvBuilder(with_pip=True,clear=True).create(environment)
            subprocess.run([str(python),'-m','pip','install','--disable-pip-version-check','-r',str(requirements)],check=True)
            marker.write_text(digest)
    env={**os.environ,'PATH':str(python.parent)+os.pathsep+os.environ.get('PATH','')}
    os.execve(python,[str(python),*sys.argv[1:]],env)


if __name__=='__main__': main()
