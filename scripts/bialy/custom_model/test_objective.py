"""Tiny CPU numerical contracts, without downloading a model."""
import types
import unittest
import torch
import torch.nn.functional as F

from objective import dpo, scores


class Objective(unittest.TestCase):
    def test_prompt_gradient_is_zero_and_arguments_receive_gradient(self):
        logits = torch.zeros(1, 5, 8, requires_grad=True)
        model = lambda **kw: types.SimpleNamespace(logits=logits[:, kw["logits_to_keep"]])
        total, nll = scores(model, {"input_ids":[1,2,3,4,5],"labels":[-100,-100,-100,4,5]}, "cpu")
        self.assertAlmostEqual(float(total.detach()), -2*float(torch.log(torch.tensor(8.))), places=5)
        nll.backward()
        self.assertEqual(float(logits.grad[0,:2].abs().sum()), 0)
        self.assertGreater(float(logits.grad[0,2:4].abs().sum()), 0)
        self.assertEqual(float(logits.grad[0,4].abs().sum()), 0)

    def test_a_model_returning_every_position_is_refused(self):
        logits = torch.zeros(1, 5, 8)
        with self.assertRaisesRegex(ValueError, "logits_to_keep"):
            scores(lambda **kw: types.SimpleNamespace(logits=logits), {"input_ids":[1,2,3,4,5],"labels":[-100,-100,4,5,6]}, "cpu")
        with self.assertRaisesRegex(ValueError, "no completion"):
            scores(lambda **kw: types.SimpleNamespace(logits=logits), {"input_ids":[1,2,3],"labels":[-100,-100,-100]}, "cpu")

    def test_kept_positions_match_the_full_sequence_likelihood(self):
        from transformers import LlamaConfig, LlamaForCausalLM
        torch.manual_seed(0)
        model = LlamaForCausalLM(LlamaConfig(vocab_size=40, hidden_size=16, intermediate_size=32, num_hidden_layers=2,
                                             num_attention_heads=2, num_key_value_heads=2, max_position_embeddings=64)).eval()
        ids = torch.randint(1, 40, (1, 12))
        labels = ids.clone()
        labels[0, :7] = -100
        with torch.no_grad():
            full = model(input_ids=ids, attention_mask=torch.ones_like(ids)).logits[:, :-1].float()
            expected = F.log_softmax(full, dim=-1).gather(-1, labels[:, 1:].clamp_min(0).unsqueeze(-1)).squeeze(-1)
            expected = expected[labels[:, 1:] != -100].sum()
            total, nll = scores(model, {"input_ids": ids[0].tolist(), "labels": labels[0].tolist()}, "cpu")
        torch.testing.assert_close(total, expected, rtol=1e-5, atol=1e-5)
        torch.testing.assert_close(nll, -expected / 5, rtol=1e-5, atol=1e-5)

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
