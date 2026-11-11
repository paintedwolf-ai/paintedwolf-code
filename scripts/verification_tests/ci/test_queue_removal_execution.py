import unittest
from unittest.mock import patch

from ci_policy import queue_removal

RUN = {'id': 7, 'head_branch': 'gh-readonly-queue/main/pr-12-abc', 'html_url': 'https://example.test/run'}


class QueueRemovalTests(unittest.TestCase):
    def notify(self, runs, jobs):
        posted = []

        def api(path, method='GET', body=None):
            if method == 'POST':
                posted.append((path, body['body']))
                return None
            return {'workflow_runs': runs}
        with patch.object(queue_removal, 'api', api), patch.object(queue_removal, 'pages', lambda *_: jobs):
            return queue_removal.notify('o/r', 12), posted

    def test_failed_group_names_its_lanes_and_run(self):
        other = {**RUN, 'id': 8, 'head_branch': 'gh-readonly-queue/main/pr-123-def'}
        notified, posted = self.notify([other, RUN], [{'name': 'lint', 'conclusion': 'failure'},
                                                      {'name': 'build', 'conclusion': 'failure'},
                                                      {'name': 'native', 'conclusion': 'cancelled'}])
        self.assertTrue(notified)
        self.assertEqual(posted[0][0], 'repos/o/r/issues/12/comments')
        self.assertIn('`build`, `lint` failed', posted[0][1])
        self.assertIn(RUN['html_url'], posted[0][1])

    def test_removal_without_a_failed_job_posts_nothing(self):
        self.assertEqual(self.notify([RUN], [{'name': 'lint', 'conclusion': 'success'}]), (False, []))
        self.assertEqual(self.notify([], []), (False, []))


if __name__ == '__main__':
    unittest.main()
