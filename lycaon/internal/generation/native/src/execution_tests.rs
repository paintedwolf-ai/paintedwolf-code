use super::*;
use std::collections::VecDeque;

#[derive(Default)]
struct Fake {
    tokens: Vec<u32>,
    prefix: Vec<u32>,
    calls: Vec<Vec<u32>>,
    samples: VecDeque<u32>,
    fail: bool,
}

impl Backend for Fake {
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>> {
        self.calls.push(tokens.to_vec());
        self.tokens.extend(tokens);
        if self.fail {
            return Err(Error::Device("fixture".into()));
        }
        Ok(sampling.map(|_| self.samples.pop_front().unwrap_or(20)))
    }
    fn reset(&mut self) -> Result<()> {
        self.tokens.clear();
        Ok(())
    }
    fn save_prefix(&mut self) -> Result<()> {
        self.prefix = self.tokens.clone();
        Ok(())
    }
    fn restore_prefix(&mut self) -> Result<()> {
        self.tokens = self.prefix.clone();
        Ok(())
    }
    fn clear_prefix(&mut self) -> Result<()> {
        self.prefix.clear();
        Ok(())
    }
}

fn request(id: u64) -> Begin {
    Begin {
        request_id: id,
        prompt_tokens: vec![2, 5, 6, 7, 8, 9],
        max_new_tokens: 3,
        temperature: 0.0,
        top_p: 1.0,
        top_k: 64,
        seed: 42,
        checkpoint_tokens: 3,
        stop_tokens: vec![],
    }
}

fn executor() -> Executor<Fake> {
    Executor::new(Fake::default(), 64, 2, vec![1, 106]).unwrap()
}

#[test]
fn steps_stop_at_checkpoint_and_chunk_boundaries() {
    let mut runner = executor();
    runner.begin(request(1)).unwrap();
    for position in [2, 3, 5, 6] {
        assert_eq!(runner.advance(1).unwrap().processed_prompt_tokens, position);
    }
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    assert_eq!(
        runner.backend.calls,
        [vec![2, 5], vec![6], vec![7, 8], vec![9]]
    );
    assert_eq!(runner.advance(1).unwrap().generated_tokens, 2);
    let done = runner.advance(1).unwrap();
    assert_eq!(done.stop_reason, Some(StopReason::MaxTokens));
    assert_eq!(done.token, Some(20));
    assert!(runner.advance(1).is_err());
    assert!(runner.backend.tokens.is_empty());
}

#[test]
fn cancellation_keeps_only_the_exact_prefix_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1)).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    assert_eq!(
        runner.cancel(1).unwrap().stop_reason,
        Some(StopReason::Canceled)
    );
    assert!(runner.backend.tokens.is_empty());
    assert_eq!(runner.begin(request(2)).unwrap(), 3);
    assert_eq!(runner.backend.tokens, [2, 5, 6]);
    assert!(runner.cancel(1).is_err());
    assert_eq!(runner.advance(2).unwrap().processed_prompt_tokens, 5);
    runner.cancel(2).unwrap();
    let mut mismatch = request(3);
    mismatch.prompt_tokens[1] = 10;
    assert_eq!(runner.begin(mismatch).unwrap(), 0);
    assert!(runner.backend.tokens.is_empty());
}

#[test]
fn terminal_markers_are_counted_without_being_emitted() {
    for (sample, reason) in [(106, StopReason::Eos), (30, StopReason::StopToken)] {
        let mut runner = executor();
        runner.backend.samples.push_back(sample);
        let mut request = request(1);
        request.stop_tokens = vec![30];
        runner.begin(request).unwrap();
        for _ in 0..3 {
            assert!(runner.advance(1).unwrap().stop_reason.is_none());
        }
        let done = runner.advance(1).unwrap();
        assert_eq!(done.stop_reason, Some(reason));
        assert_eq!(done.generated_tokens, 1);
        assert!(done.token.is_none());
    }
}

#[test]
fn invalid_or_stale_requests_do_not_touch_the_backend() {
    let mut runner = executor();
    for mutate in [
        |r: &mut Begin| r.request_id = 0,
        |r: &mut Begin| r.max_new_tokens = usize::MAX,
        |r: &mut Begin| r.temperature = f32::NAN,
        |r: &mut Begin| r.top_p = 0.0,
        |r: &mut Begin| r.prompt_tokens[0] = 262144,
        |r: &mut Begin| r.checkpoint_tokens = r.prompt_tokens.len(),
    ] {
        let mut invalid = request(1);
        mutate(&mut invalid);
        assert!(runner.begin(invalid).is_err());
    }
    assert!(runner.backend.calls.is_empty());
    runner.begin(request(1)).unwrap();
    assert!(runner.begin(request(2)).is_err());
    runner.cancel(1).unwrap();
    assert!(runner.begin(request(1)).is_err());
}

#[test]
fn failed_step_discards_partial_state_and_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1)).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.backend.fail = true;
    assert!(runner.advance(1).is_err());
    assert!(runner.backend.tokens.is_empty());
    assert!(runner.backend.prefix.is_empty());
    runner.backend.fail = false;
    assert_eq!(runner.begin(request(2)).unwrap(), 0);
}
