import unittest
from preparation import validate_decision_controls


class PreflightControlsTests(unittest.TestCase):
    def test_each_held_worker_and_swapped_answers_require_negative_controls(self):
        labels = ['Recent','Archive']
        suite = {'cases':[{'id':'decision','setup':{'dispatches':[
            {'label':label,'stages':[{'kind':'needs_decision'}]} for label in labels]}}]}
        controls = [{'id':'decision','kind':'shortcut','control':'unanswered-worker-decision','worker':label,
                     'failed':['planned-leg-settled-'+label,'decision-answered-'+label]} for label in labels]
        controls.append({'id':'decision','kind':'shortcut','control':'swapped-worker-decisions',
                         'failed':['decision-answered-'+label for label in labels]})
        validate_decision_controls(controls,suite,{'decision'})
        for index in range(len(controls)):
            for replacement in [None,{**controls[index],'failed':[]},{**controls[index],'kind':'intended'}]:
                changed = controls[:index]+([replacement] if replacement else [])+controls[index+1:]
                with self.subTest(index=index,replacement=replacement), self.assertRaises(ValueError):
                    validate_decision_controls(changed,suite,{'decision'})
