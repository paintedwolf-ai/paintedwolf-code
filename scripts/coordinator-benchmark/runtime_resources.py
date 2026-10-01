"""Release fixture outputs through their application owner."""
import subprocess
from application_environment import application_environment


def release_external_resource(capture, source):
    subprocess.run([str(source / '.bin/lycaon-debug'), 'eval', 'tool-usage',
                    '--release-resources', str(capture)], check=True,
                   env=application_environment())
