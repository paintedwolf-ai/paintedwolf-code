"""Tiny CPU numerical contracts, without downloading a model."""
import types
import unittest
import torch

from objective import dpo, scores


class Objective(unittest.TestCase):
    def test_prompt_gradient_is_zero_and_arguments_receive_gradient(self):
        logits = torch.zeros(1, 5, 8, requires_grad=True)
        model = lambda **kw: types.SimpleNamespace(logits=logits)
        total, nll = scores(model, {"input_ids":[1,2,3,4,5],"labels":[-100,-100,-100,4,5]}, "cpu")
        self.assertAlmostEqual(float(total.detach()), -2*float(torch.log(torch.tensor(8.))), places=5)
        nll.backward()
        self.assertEqual(float(logits.grad[0,:2].abs().sum()), 0)
        self.assertGreater(float(logits.grad[0,2:4].abs().sum()), 0)

    def test_dpo_freezes_reference_and_rewards_chosen(self):
        chosen, rejected = torch.tensor(-3.,requires_grad=True), torch.tensor(-4.,requires_grad=True)
        rc, rr = torch.tensor(-3.,requires_grad=True), torch.tensor(-4.,requires_grad=True)
        value = dpo(chosen,rejected,rc,rr,-chosen/3,0.1,0)
        self.assertAlmostEqual(float(value.detach()), float(torch.log(torch.tensor(2.))), places=6)
        value.backward()
        self.assertLess(float(chosen.grad),0)
        self.assertGreater(float(rejected.grad),0)
        self.assertIsNone(rc.grad)
        self.assertIsNone(rr.grad)


if __name__ == "__main__":
    unittest.main()
