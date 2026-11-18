"""The update schedule, accumulation arithmetic and LoRA confinement, without a model."""
import unittest

from train import attention_kernels, confined_adapters
from training_loop import learning_rate, updates_per_epoch


class Schedule(unittest.TestCase):
    def test_rate_warms_up_linearly_then_decays_to_its_floor(self):
        rates = [learning_rate(update, 20, 1e-4, 0.2, 0.1) for update in range(20)]
        for rate, wanted in zip(rates[:4], [2.5e-5, 5e-5, 7.5e-5, 1e-4], strict=True):
            self.assertAlmostEqual(rate, wanted)
        self.assertAlmostEqual(rates[4], 1e-4)
        self.assertTrue(all(earlier > later for earlier, later in zip(rates[4:], rates[5:])))
        self.assertAlmostEqual(rates[-1], 1e-5)

    def test_rate_without_warmup_starts_at_the_peak_and_a_floor_of_one_holds_it(self):
        self.assertAlmostEqual(learning_rate(0, 10, 2e-4, 0.0, 0.0), 2e-4)
        self.assertAlmostEqual(learning_rate(9, 10, 2e-4, 0.0, 0.0), 0.0)
        self.assertEqual({learning_rate(update, 10, 2e-4, 0.0, 1.0) for update in range(10)}, {2e-4})
        self.assertAlmostEqual(learning_rate(0, 1, 2e-4, 0.5, 0.1), 2e-4)

    def test_a_short_last_group_is_still_one_update(self):
        self.assertEqual([updates_per_epoch(rows, 8) for rows in (1, 8, 9, 64)], [1, 1, 2, 8])


class Confinement(unittest.TestCase):
    MODULES = ["model", "model.language_model", "model.language_model.layers.0.self_attn.q_proj",
               "model.vision_tower.encoder.layers.0.self_attn.q_proj"]

    def test_adapters_inside_the_language_model_are_accepted_in_order(self):
        adapted = ["model.language_model.layers.1.self_attn.q_proj", "model.language_model.layers.0.self_attn.q_proj"]
        self.assertEqual(confined_adapters(self.MODULES, adapted, 2), sorted(adapted))
        self.assertEqual(confined_adapters(["layers.0.q_proj"], ["layers.0.q_proj"], 1), ["layers.0.q_proj"])

    def test_empty_escaping_or_miscounted_adapters_are_refused(self):
        inside = "model.language_model.layers.0.self_attn.q_proj"
        for adapted, expected, reason in (([], 1, "match no module"),
                                          ([inside, "model.vision_tower.encoder.layers.0.self_attn.q_proj"], 2, "outside the language model"),
                                          ([inside], 2, "not the declared 2")):
            with self.assertRaisesRegex(ValueError, reason):
                confined_adapters(self.MODULES, adapted, expected)


class Kernels(unittest.TestCase):
    def test_cuda_training_refuses_the_full_matrix_kernel(self):
        import torch
        with attention_kernels("cpu"):
            self.assertTrue(torch.backends.cuda.math_sdp_enabled())
        with attention_kernels("cuda:0"):
            self.assertFalse(torch.backends.cuda.math_sdp_enabled())
            self.assertTrue(torch.backends.cuda.mem_efficient_sdp_enabled())
        self.assertTrue(torch.backends.cuda.math_sdp_enabled())


if __name__ == "__main__":
    unittest.main()
