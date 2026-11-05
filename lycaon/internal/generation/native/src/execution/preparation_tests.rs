use super::super::preparation::Prepare;
use super::{executor, request};
use crate::output_contract::Envelope;

#[test]
fn preparation_is_chunked_without_sampling_and_reusable_by_generation() {
    let mut runner = executor();
    let tokens = vec![2, 5, 6, 7];
    assert_eq!(
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 1,
                prompt_tokens: tokens.clone()
            })
            .unwrap(),
        0
    );
    assert!(runner.begin(request(2), None, Envelope::fixture()).is_err());
    assert!(runner.clear_prefix().is_err());
    let first = runner.advance_preparation(1).unwrap();
    assert_eq!(first.processed_prompt_tokens, 2);
    assert!(!first.complete);
    assert!(runner.backend.prefix.is_empty());
    let done = runner.advance_preparation(1).unwrap();
    assert!(done.complete);
    assert!(!done.canceled);
    assert_eq!(runner.backend.prefix, tokens);
    assert_eq!(runner.backend.sampling_calls, 0);
    assert!(runner.advance_preparation(1).is_err());
    // Generation's checkpoint hint does not limit exact prepared-state reuse.
    assert_eq!(
        runner.begin(request(2), None, Envelope::fixture()).unwrap(),
        4
    );
    let generated = runner.advance(2).unwrap();
    assert_eq!(generated.generated_tokens, 1);
    assert_eq!(runner.backend.sampling_calls, 1);
}

#[test]
fn cancellation_preserves_the_previous_completed_checkpoint() {
    let mut runner = executor();
    runner.begin(request(1), None, Envelope::fixture()).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.cancel(1).unwrap();
    runner
        .prepare(Prepare {
            checkpoint_tokens: 0,
            request_id: 2,
            prompt_tokens: vec![2, 30, 31, 32],
        })
        .unwrap();
    runner.advance_preparation(2).unwrap();
    let canceled = runner.cancel_preparation(2).unwrap();
    assert!(canceled.canceled);
    assert!(!canceled.complete);
    assert_eq!(canceled.processed_prompt_tokens, 2);
    assert_eq!(runner.evaluated, [2, 30]);
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    assert_eq!(
        runner.begin(request(3), None, Envelope::fixture()).unwrap(),
        3
    );
}

#[test]
fn identical_preparation_performs_no_forward_work() {
    let mut runner = executor();
    for id in 1..=2 {
        let reused = runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: id,
                prompt_tokens: vec![2, 5],
            })
            .unwrap();
        assert_eq!(reused, if id == 1 { 0 } else { 2 });
        let result = runner.advance_preparation(id).unwrap();
        assert!(result.complete);
    }
    assert_eq!(runner.backend.calls, [vec![2, 5]]);
    assert_eq!(runner.backend.sampling_calls, 0);
}

#[test]
fn preparation_rejects_invalid_and_overlapping_requests_without_effects() {
    let mut runner = executor();
    for (id, tokens) in [
        (0, vec![2]),
        (1, vec![]),
        (1, vec![262144]),
        (1, vec![2; 64]),
    ] {
        assert!(
            runner
                .prepare(Prepare {
                    checkpoint_tokens: 0,
                    request_id: id,
                    prompt_tokens: tokens
                })
                .is_err()
        );
    }
    assert!(runner.backend.calls.is_empty());
    runner
        .prepare(Prepare {
            checkpoint_tokens: 0,
            request_id: 1,
            prompt_tokens: vec![2, 5],
        })
        .unwrap();
    assert!(
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 2,
                prompt_tokens: vec![2, 5]
            })
            .is_err()
    );
    assert!(runner.advance_preparation(2).is_err());
    assert!(runner.cancel_preparation(2).is_err());
    runner.cancel_preparation(1).unwrap();
    assert!(
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 1,
                prompt_tokens: vec![2]
            })
            .is_err()
    );
    runner.begin(request(2), None, Envelope::fixture()).unwrap();
    assert!(
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 3,
                prompt_tokens: vec![2]
            })
            .is_err()
    );
}

#[test]
fn failed_preparation_discards_partial_device_state() {
    let mut runner = executor();
    runner
        .prepare(Prepare {
            checkpoint_tokens: 0,
            request_id: 1,
            prompt_tokens: vec![2, 5, 6],
        })
        .unwrap();
    runner.advance_preparation(1).unwrap();
    runner.backend.fail = true;
    assert!(runner.advance_preparation(1).is_err());
    assert!(runner.evaluated.is_empty());
    assert!(runner.prefix.is_empty());
    assert!(runner.backend.tokens.is_empty());
    runner.backend.fail = false;
    assert_eq!(
        runner.begin(request(2), None, Envelope::fixture()).unwrap(),
        0
    );
}

#[test]
fn unrelated_work_restores_only_an_available_matching_checkpoint() {
    for missing in [false, true] {
        let mut runner = executor();
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 1,
                prompt_tokens: vec![2, 5, 6, 7],
            })
            .unwrap();
        while !runner.advance_preparation(1).unwrap().complete {}
        let mut unrelated = request(2);
        unrelated.prompt_tokens = vec![2, 30, 31];
        unrelated.checkpoint_tokens = 0;
        runner.begin(unrelated, None, Envelope::fixture()).unwrap();
        while runner.advance(2).unwrap().stop_reason.is_none() {}
        runner.backend.missing_prefix = missing;
        assert_eq!(
            runner.begin(request(3), None, Envelope::fixture()).unwrap(),
            if missing { 0 } else { 4 }
        );
    }
}

#[test]
fn prepared_history_keeps_the_standing_checkpoint_for_changed_history() {
    let mut runner = executor();
    runner.begin(request(1), None, Envelope::fixture()).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.cancel(1).unwrap();
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    assert_eq!(
        runner
            .prepare(Prepare {
                checkpoint_tokens: 0,
                request_id: 2,
                prompt_tokens: vec![2, 5, 6, 7, 8],
            })
            .unwrap(),
        3
    );
    assert!(runner.advance_preparation(2).unwrap().complete);
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    assert_eq!(runner.evaluated, [2, 5, 6, 7, 8]);
    let mut changed = request(3);
    changed.prompt_tokens[3] = 30;
    assert_eq!(runner.begin(changed, None, Envelope::fixture()).unwrap(), 3);
    assert_eq!(runner.backend.tokens, [2, 5, 6]);
}

#[test]
fn prepared_history_reuses_the_longer_active_state() {
    let mut runner = executor();
    runner.begin(request(1), None, Envelope::fixture()).unwrap();
    runner.advance(1).unwrap();
    runner.advance(1).unwrap();
    runner.cancel(1).unwrap();
    runner
        .prepare(Prepare {
            checkpoint_tokens: 0,
            request_id: 2,
            prompt_tokens: vec![2, 5, 6, 7, 8],
        })
        .unwrap();
    assert!(runner.advance_preparation(2).unwrap().complete);
    assert_eq!(
        runner.begin(request(3), None, Envelope::fixture()).unwrap(),
        5
    );
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
}

#[test]
fn cold_preparation_captures_the_declared_standing_boundary() {
    let mut runner = executor();
    runner
        .prepare(Prepare {
            request_id: 1,
            prompt_tokens: vec![2, 5, 6, 7, 8],
            checkpoint_tokens: 3,
        })
        .unwrap();
    for expected in [2, 3, 5] {
        assert_eq!(
            runner
                .advance_preparation(1)
                .unwrap()
                .processed_prompt_tokens,
            expected
        );
    }
    assert_eq!(runner.backend.calls, [vec![2, 5], vec![6], vec![7, 8]]);
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
    let mut changed = request(2);
    changed.prompt_tokens[3] = 30;
    assert_eq!(runner.begin(changed, None, Envelope::fixture()).unwrap(), 3);
}

#[test]
fn preparation_rejects_a_checkpoint_outside_its_prefix() {
    let mut runner = executor();
    assert!(
        runner
            .prepare(Prepare {
                request_id: 1,
                prompt_tokens: vec![2, 5],
                checkpoint_tokens: 3,
            })
            .is_err()
    );
    assert!(runner.backend.calls.is_empty());
}

#[test]
fn foreground_reuses_completed_preparation_chunks_before_the_checkpoint() {
    let mut runner = executor();
    runner
        .prepare(Prepare {
            request_id: 1,
            prompt_tokens: vec![2, 5, 6, 7],
            checkpoint_tokens: 3,
        })
        .unwrap();
    runner.advance_preparation(1).unwrap();
    runner.cancel_preparation(1).unwrap();
    assert!(runner.backend.prefix.is_empty());
    assert_eq!(
        runner.begin(request(2), None, Envelope::fixture()).unwrap(),
        2
    );
    assert_eq!(runner.backend.calls, [vec![2, 5]]);
    runner.advance(2).unwrap();
    assert_eq!(runner.backend.prefix, [2, 5, 6]);
}

#[test]
fn changed_foreground_tokens_reject_partial_preparation() {
    let mut runner = executor();
    runner
        .prepare(Prepare {
            request_id: 1,
            prompt_tokens: vec![2, 30, 31, 32],
            checkpoint_tokens: 3,
        })
        .unwrap();
    runner.advance_preparation(1).unwrap();
    runner.cancel_preparation(1).unwrap();
    assert_eq!(
        runner.begin(request(2), None, Envelope::fixture()).unwrap(),
        0
    );
}
