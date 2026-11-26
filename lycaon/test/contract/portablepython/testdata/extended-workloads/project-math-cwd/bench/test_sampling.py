import unittest
from fractions import Fraction

from sampling import pass_k


class TestSampling(unittest.TestCase):
    def test_exact_values(self) -> None:
        self.assertEqual(pass_k(1, 5, 2), Fraction(0, 1))
        self.assertEqual(pass_k(5, 5, 2), Fraction(1, 1))
        self.assertEqual(pass_k(3, 5, 2), Fraction(3, 10))

    def test_invalid_inputs(self) -> None:
        for bad in [(6, 5, 2), (-1, 5, 2), (3, 5, 6), (3, -1, 1)]:
            with self.assertRaises(ValueError):
                pass_k(*bad)


if __name__ == "__main__":
    unittest.main()
