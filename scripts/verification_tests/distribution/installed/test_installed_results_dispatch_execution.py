"""The installed results entry point hands each lane flag to exactly one owner."""
import unittest
from unittest.mock import patch

from linux_release import installed_results

DISPATCH = [
    ('--bootstrap-native-fixture', 'linux_release.installed_cases.native_bootstrap.main', None),
    ('--run-cases', 'linux_release.campaign.execution.main', None),
    ('--prepare-qualification', 'linux_release.campaign.qualification_producer.main', 'prepare'),
    ('--retain-qualification', 'linux_release.campaign.qualification_producer.main', 'retain'),
    ('--execute-plan', 'linux_release.campaign.guests.main', None),
    ('--diagnose-first-launch', 'linux_release.installed_cases.desktop.diagnostic_main', None),
    ('--prepare-candidate', 'linux_release.campaign.candidate_inputs.main', None),
    ('--prepare-hosted-plan', 'linux_release.hosted.plan.main', None),
    ('--hosted-runtime', 'linux_release.hosted.installed.main', None),
]


class InstalledResultsDispatchTests(unittest.TestCase):
    def test_each_lane_flag_reaches_its_owner_with_the_remaining_arguments(self):
        for flag, owner, subcommand in DISPATCH:
            with self.subTest(flag=flag), patch(owner, return_value=7) as lane, \
                    patch.object(installed_results.sys, 'argv', ['installed_results', flag, '--alpha', 'one']):
                self.assertEqual(installed_results.main(), 7)
            expected = ['--alpha', 'one'] if subcommand is None else [subcommand, '--alpha', 'one']
            lane.assert_called_once_with(expected)

    def test_lane_flags_never_reach_the_admission_parser(self):
        for flag, owner, _ in DISPATCH:
            with self.subTest(flag=flag), patch(owner, return_value=0), \
                    patch.object(installed_results.sys, 'argv', ['installed_results', flag]), \
                    patch.object(installed_results.argparse, 'ArgumentParser') as parser:
                installed_results.main()
            parser.assert_not_called()


if __name__ == '__main__':
    unittest.main()
