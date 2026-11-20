import pathlib
import subprocess
import sys
import tempfile
import unittest

from testclock import check

PYTHON = '''\
import asyncio, subprocess, threading, time
from time import monotonic, sleep


def test_fixed():
    time.sleep(1)  # flagged: fixed sleep


def test_imported_sleep():
    sleep(1)  # flagged: fixed sleep


async def test_async():
    await asyncio.sleep(1)  # flagged: fixed sleep
    await asyncio.sleep(0)


def test_event_as_sleep(event):
    event.wait(2)  # flagged: fixed sleep


def test_poll(path):
    while not path.exists():
        time.sleep(.01)


def test_closure_in_loop():
    for _ in range(3):
        def later():
            time.sleep(1)  # flagged: fixed sleep


def test_deadline(path):
    deadline = monotonic() + 5
    while not path.exists():
        if monotonic() > deadline:  # flagged: clock comparison
            raise AssertionError(path)


def test_elapsed(self):
    started = time.monotonic()
    self.assertLess(time.monotonic() - started, 2)  # flagged: clock assertion


def test_waits(self, process, thread, future, event):
    process.wait(timeout=5)  # flagged: deadline
    thread.join(5)  # flagged: deadline
    future.result(timeout=5)  # flagged: deadline
    self.assertTrue(event.wait(5))  # flagged: deadline
    subprocess.run(["true"], timeout=5)  # flagged: deadline
    process.wait()
    process.wait(timeout=None)
    ",".join(["a"])


def test_expected_expiry(self, process):
    with self.assertRaises(subprocess.TimeoutExpired):
        process.communicate(timeout=.1)
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired:
        process.kill()


def test_pinned(self, mock):
    mock.wait.assert_called_once_with(timeout=90)
    self.assertEqual(product(timeout=.1), "expired")
'''

TYPESCRIPT = '''\
import { expect, it, vi } from "vitest";

it("sleeps", async () => {
  await new Promise((resolve) => setTimeout(resolve, 50)); // flagged: fixed sleep
  await new Promise((resolve) => setTimeout(resolve, 0));
  const started = Date.now();
  expect(Date.now() - started).toBeLessThan(100); // flagged: clock assertion
  expect(performance.now() - started).toBeCloseTo(0, -3); // flagged: clock assertion
  expect(Date.now() - started).toBeGreaterThanOrEqual(0);
  expect(items.length).toBeGreaterThan(0);
});

it("polls", async () => {
  while (!ready()) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
});

// A comment naming setTimeout(resolve, 50) is not code.
const text = "await page.waitForTimeout(100)";

async function respond(route) {
  // injected latency: the reader pages while this response is in flight
  await new Promise((resolve) => setTimeout(resolve, 150));
  await route.fulfill({ body: "{}" });
}

it("races", async () => {
  await Promise.race([work(), new Promise((resolve) => setTimeout(resolve, 5000))]); // flagged: race
  const deadline = Date.now() + 1000; // flagged: deadline
});
'''

FAKE_TIMERS = '''\
import { it, vi } from "vitest";

it("advances fake time", async () => {
  vi.useFakeTimers();
  const pending = new Promise((resolve) => setTimeout(resolve, 50));
  await vi.advanceTimersByTimeAsync(50);
  await pending;
});
'''

RUST = '''\
use std::time::{Duration, Instant};

pub fn product() {
    std::thread::sleep(Duration::from_millis(5));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fixed() {
        std::thread::sleep(Duration::from_millis(50)); // flagged: fixed sleep
        let started = Instant::now();
        assert!(started.elapsed() < Duration::from_secs(1)); // flagged: clock comparison
    }

    #[test]
    fn polls() {
        while !ready() {
            std::thread::sleep(Duration::from_millis(5));
        }
        while !entries().any(|entry| {
            entry.is_partial()
        }) {
            std::thread::sleep(Duration::from_millis(5));
        }
        let text = "thread::sleep(Duration::from_secs(1))";
    }

    #[test]
    fn deadlines(receiver: Receiver<()>) {
        let deadline = Instant::now() + Duration::from_secs(120); // flagged: deadline
        receiver.recv_timeout(Duration::from_secs(5)).unwrap(); // flagged: deadline
        receiver.recv().unwrap();
    }

    async fn settle() {
        tokio::time::sleep(Duration::from_millis(40)).await;
    }

    #[tokio::test(start_paused = true)]
    async fn paused_helper() {
        settle().await;
    }
}

pub fn after_the_tests() {
    std::thread::sleep(Duration::from_millis(5));
}
'''

PAUSED = '''\
#[cfg(test)]
mod tests {
    #[tokio::test(start_paused = true)]
    async fn paused() {
        tokio::time::sleep(std::time::Duration::from_secs(60)).await;
    }
}
'''


def flagged_lines(text):
    return sorted(number for number, line in enumerate(text.splitlines(), 1) if "// flagged" in line or "# flagged" in line)


class CheckTests(unittest.TestCase):
    def findings(self, name, text):
        return check.findings_for(pathlib.Path(name), text)

    def lines(self, name, text):
        return sorted({int(finding.split(":")[1]) for finding in self.findings(name, text)})

    def test_python_timing_dependences_are_reported(self):
        self.assertEqual(self.lines("scripts/x/test_fixture.py", PYTHON), flagged_lines(PYTHON))

    def test_python_test_support_is_checked_and_production_code_is_not(self):
        self.assertEqual(self.lines("scripts/verification_tests/support.py", PYTHON), flagged_lines(PYTHON))
        self.assertEqual(self.findings("scripts/x/fixture.py", PYTHON), [])

    def test_typescript_timing_dependences_are_reported(self):
        self.assertEqual(self.lines("src/fixture.test.ts", TYPESCRIPT), flagged_lines(TYPESCRIPT))
        self.assertEqual(self.findings("src/fixture.ts", TYPESCRIPT), [])

    def test_typescript_fake_timers_are_not_real_sleeps(self):
        self.assertEqual(self.findings("src/fixture.test.ts", FAKE_TIMERS), [])
        self.assertEqual(self.lines("src/fixture.test.ts", "vi.useFakeTimers({ shouldAdvanceTime: true });\n"), [1])

    def test_typescript_test_support_directories_are_checked(self):
        self.assertEqual(self.lines("lycaon-den/e2e/helpers.ts", TYPESCRIPT), flagged_lines(TYPESCRIPT))
        self.assertEqual(self.findings("lycaon-den/e2e/types.d.ts", TYPESCRIPT), [])

    def test_rust_timing_dependences_are_reported_in_test_code_only(self):
        self.assertEqual(self.lines("src/fixture.rs", RUST), flagged_lines(RUST))
        self.assertEqual(self.findings("src/product.rs", RUST.split("#[cfg(test)]")[0]), [])

    def test_rust_out_of_line_test_modules_are_checked_whole(self):
        sleep = "fn helper() {\n    std::thread::sleep(std::time::Duration::from_secs(1));\n}\n"
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "src/watch").mkdir(parents=True)
            (root / "src/watch.rs").write_text('#[cfg(test)]\nmod checks;\n#[cfg(test)]\n#[path = "other.rs"]\nmod other;\n')
            for name in ("src/watch/checks.rs", "src/other.rs", "src/product.rs"):
                (root / name).write_text(sleep)
            paths = [pathlib.Path(p) for p in ("src/watch.rs", "src/watch/checks.rs", "src/other.rs", "src/product.rs")]
            self.assertEqual(check.scan(root, paths), ["src/other.rs:2: fixed sleep", "src/watch/checks.rs:2: fixed sleep"])

    def test_rust_paused_time_is_not_a_real_sleep(self):
        self.assertEqual(self.findings("src/fixture.rs", PAUSED), [])

    def test_a_seeded_dependence_fails_the_command(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            (root / "test_seeded.py").write_text("import time\n\n\ndef test():\n    time.sleep(1)\n")
            (root / "node_modules").mkdir()
            (root / "node_modules" / "test_vendored.py").write_text("import time\ntime.sleep(1)\n")
            subprocess.run(["git", "-C", str(root), "add", "-f", "."], check=True)
            result = subprocess.run([sys.executable, check.__file__, "--root", str(root)],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(result.stdout, "test_seeded.py:5: fixed sleep\n")
            (root / "test_seeded.py").write_text("def test():\n    pass\n")
            result = subprocess.run([sys.executable, check.__file__, "--root", str(root)],
                                    capture_output=True, text=True)
            self.assertEqual((result.returncode, result.stdout), (0, ""), result.stderr)


if __name__ == "__main__":
    unittest.main()
