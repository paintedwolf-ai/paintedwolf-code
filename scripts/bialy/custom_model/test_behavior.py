"""The tolerance an exported artifact is held to against its merged checkpoint."""
import unittest

from behavior import compare, served_cases


def expected(*prompts):
    return {"prompts": [{"name": name, "prompt_tokens": [2, 5], "tokens": tokens, "margins": margins}
                        for name, tokens, margins in prompts]}


class Compare(unittest.TestCase):
    REFERENCE = expected(("a", [7, 8, 9], [6.0, 0.3, 4.0]), ("b", [4, 4], [3.0, 3.0]), ("c", [1], [0.1]))

    def seen(self, *tokens):
        return [{"prompt_tokens": [2, 5], "tokens": list(item)} for item in tokens]

    def test_identical_tokens_agree_in_full(self):
        result = compare(self.REFERENCE, self.seen([7, 8, 9], [4, 4], [1]), "F16")
        self.assertEqual(result["agreed_tokens"], [3, 2, 1])
        self.assertEqual(result["departures"], [])

    def test_a_departure_at_a_narrow_margin_is_recorded_and_accepted(self):
        result = compare(self.REFERENCE, self.seen([7, 5, 9], [4, 4, 4], [1]), "F16")
        self.assertEqual(result["agreed_tokens"], [1, 2, 1])
        self.assertEqual(result["departures"], [{"prompt": "a", "step": 1, "margin": 0.3}])

    def test_a_departure_at_a_decisive_margin_is_refused(self):
        with self.assertRaisesRegex(ValueError, "beyond its tolerance"):
            compare(self.REFERENCE, self.seen([7, 8, 3], [4, 4], [1]), "Q4_K_M")
        with self.assertRaisesRegex(ValueError, "beyond its tolerance"):
            compare(self.REFERENCE, self.seen([7, 8, 9], [4], [1]), "Q4_K_M")

    def test_quantization_widens_the_tolerance(self):
        reference = expected(("a", [7, 8], [6.0, 1.5]), ("b", [4], [3.0]))
        compare(reference, self.seen([7, 3], [4]), "Q4_K_M")
        with self.assertRaisesRegex(ValueError, "beyond its tolerance"):
            compare(reference, self.seen([7, 3], [4]), "F16")

    def test_different_tokenization_is_refused(self):
        seen = self.seen([7, 8, 9], [4, 4], [1])
        seen[1]["prompt_tokens"] = [2, 2, 5]
        with self.assertRaisesRegex(ValueError, "tokenizes a rendered prompt differently: b"):
            compare(self.REFERENCE, seen, "F16")

    def test_narrow_first_tokens_cannot_excuse_every_prompt(self):
        reference = expected(("a", [7], [0.1]), ("b", [4], [0.1]))
        with self.assertRaisesRegex(ValueError, "too few first tokens"):
            compare(reference, self.seen([1], [4]), "F16")

    def test_the_reference_prompts_are_the_served_fixtures(self):
        self.assertGreaterEqual(len(served_cases()), 5)


if __name__ == "__main__":
    unittest.main()
