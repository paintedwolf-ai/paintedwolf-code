use super::*;
use std::collections::VecDeque;

#[derive(Default)]
struct Fake {
    tokens: Vec<u32>,
    prefix: Vec<u32>,
    calls: Vec<Vec<u32>>,
    samples: VecDeque<u32>,
    sampling_calls: usize,
    fail: bool,
    missing_prefix: bool,
}

impl Backend for Fake {
    fn plan(_: &std::path::Path, context: usize, chunk: usize) -> Result<crate::resources::Plan> {
        crate::resources::Plan::e4b(3 << 30, 2 << 30, context, chunk, false)
    }
    fn forward(&mut self, tokens: &[u32], sampling: Option<&mut Sampling>) -> Result<Option<u32>> {
        self.calls.push(tokens.to_vec());
        self.tokens.extend(tokens);
        if self.fail {
            return Err(Error::Device("fixture".into()));
        }
        Ok(sampling.map(|_| {
            self.sampling_calls += 1;
            self.samples.pop_front().unwrap_or(20)
        }))
    }
    fn reset(&mut self) -> Result<()> {
        self.tokens.clear();
        Ok(())
    }
    fn save_prefix(&mut self) -> Result<()> {
        self.prefix = self.tokens.clone();
        Ok(())
    }
    fn restore_prefix(&mut self) -> Result<bool> {
        if self.missing_prefix {
            return Ok(false);
        }
        self.tokens = self.prefix.clone();
        Ok(true)
    }
    fn clear_prefix(&mut self) -> Result<()> {
        self.prefix.clear();
        Ok(())
    }
}

#[test]
fn unavailable_checkpoint_recomputes_the_complete_prompt() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    while runner.advance(1).unwrap().stop_reason.is_none() {}
    runner.backend.missing_prefix = true;
    runner.backend.calls.clear();
    assert_eq!(runner.begin(request(2), None).unwrap(), 0);
    while runner.advance(2).unwrap().stop_reason.is_none() {}
    assert_eq!(runner.backend.calls[0], [2, 5]);
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
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
        output_schema: None,
    }
}

fn executor() -> Executor<Fake> {
    Executor::new(Fake::default(), 64, 2, vec![1, 106]).unwrap()
}

#[test]
fn steps_stop_at_checkpoint_and_chunk_boundaries() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
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
    assert_eq!(runner.backend.tokens, [2, 5, 6, 7, 8, 9, 20, 20]);
}

#[test]
fn cancellation_keeps_only_the_exact_prefix_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    assert_eq!(
        runner.cancel(1).unwrap().stop_reason,
        Some(StopReason::Canceled)
    );
    assert!(runner.backend.tokens.is_empty());
    assert_eq!(runner.begin(request(2), None).unwrap(), 3);
    assert_eq!(runner.backend.tokens, [2, 5, 6]);
    assert!(runner.cancel(1).is_err());
    assert_eq!(runner.advance(2).unwrap().processed_prompt_tokens, 5);
    runner.cancel(2).unwrap();
    let mut mismatch = request(3);
    mismatch.prompt_tokens[1] = 10;
    assert_eq!(runner.begin(mismatch, None).unwrap(), 0);
    assert!(runner.backend.tokens.is_empty());
}

#[test]
fn terminal_markers_are_counted_without_being_emitted() {
    for (sample, reason) in [(106, StopReason::Eos), (30, StopReason::StopToken)] {
        let mut runner = executor();
        runner.backend.samples.push_back(sample);
        let mut request = request(1);
        request.stop_tokens = vec![30];
        runner.begin(request, None).unwrap();
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
        assert!(runner.begin(invalid, None).is_err());
    }
    assert!(runner.backend.calls.is_empty());
    runner.begin(request(1), None).unwrap();
    assert!(runner.begin(request(2), None).is_err());
    runner.cancel(1).unwrap();
    assert!(runner.begin(request(1), None).is_err());
}

#[test]
fn failed_step_discards_partial_state_and_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.backend.fail = true;
    assert!(runner.advance(1).is_err());
    assert!(runner.backend.tokens.is_empty());
    assert!(runner.backend.prefix.is_empty());
    runner.backend.fail = false;
    assert_eq!(runner.begin(request(2), None).unwrap(), 0);
}

#[test]
fn full_admitted_prefix_reuses_only_identical_tokens() {
    let mut runner = Executor::new(Fake::default(), 16384, 256, vec![1]).unwrap();
    let make = |id| {
        let mut request = request(id);
        request.prompt_tokens = vec![2; 8193];
        request.checkpoint_tokens = 8192;
        request.max_new_tokens = 1;
        request
    };
    runner.begin(make(1), None).unwrap();
    while runner.advance(1).unwrap().stop_reason.is_none() {}
    assert_eq!(runner.backend.prefix.len(), 8192);
    let calls = runner.backend.calls.len();
    let mut second = make(2);
    second.prompt_tokens[8192] = 7;
    assert_eq!(runner.begin(second, None).unwrap(), 8192);
    assert_eq!(
        runner.advance(2).unwrap().stop_reason,
        Some(StopReason::MaxTokens)
    );
    assert_eq!(runner.backend.calls.len(), calls + 1);
    let mut changed = make(3);
    changed.prompt_tokens[6000] = 8;
    assert_eq!(runner.begin(changed, None).unwrap(), 0);
}

#[test]
fn unretained_request_preserves_the_conversation_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    while runner.advance(1).unwrap().stop_reason.is_none() {}
    let mut utility = request(2);
    utility.prompt_tokens = vec![2, 12, 13];
    utility.checkpoint_tokens = 0;
    assert_eq!(runner.begin(utility, None).unwrap(), 0);
    while runner.advance(2).unwrap().stop_reason.is_none() {}
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    assert_eq!(runner.begin(request(3), None).unwrap(), 3);
    assert_eq!(runner.backend.tokens, [2, 5, 6]);
}

#[test]
fn continuation_reuses_only_tokens_already_evaluated() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    while runner.advance(1).unwrap().stop_reason.is_none() {}
    let calls = runner.backend.calls.len();
    let mut next = request(2);
    next.prompt_tokens.extend([20, 20, 20, 106, 7]);
    assert_eq!(runner.begin(next, None).unwrap(), 8);
    assert_eq!(runner.advance(2).unwrap().reused_prefix_tokens, 8);
    assert_eq!(runner.backend.calls[calls], [20, 106]);
    runner.cancel(2).unwrap();
    assert!(runner.evaluated.is_empty());
    assert_eq!(runner.begin(request(3), None).unwrap(), 3);
}

#[test]
fn changed_continuation_replays_from_the_stable_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1), None).unwrap();
    while runner.advance(1).unwrap().stop_reason.is_none() {}
    let mut next = request(2);
    next.prompt_tokens.extend([21, 20, 20, 106, 7]);
    assert_eq!(runner.begin(next, None).unwrap(), 3);
    runner.cancel(2).unwrap();
    runner.clear_prefix().unwrap();
    assert!(runner.evaluated.is_empty());
    assert!(runner.backend.tokens.is_empty());
    assert_eq!(runner.begin(request(3), None).unwrap(), 0);
}

#[path = "execution/preparation_tests.rs"]
mod preparation;
