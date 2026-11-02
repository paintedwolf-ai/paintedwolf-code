use super::*;
use crate::execution::Sampling;
use crate::sampler::Rng;
use tokenizers::{AddedToken, decoders::fuse::Fuse, models::bpe::BPE};

fn tokenizer() -> Tokenizer {
    let mut vocab: tokenizers::models::bpe::Vocab = (0u8..=127)
        .map(|byte| ((byte as char).to_string(), 200 + u32::from(byte))).collect();
    vocab.insert("<|channel>".into(), 100);
    vocab.insert("<|tool_call>".into(), 48);
    let mut tokenizer = Tokenizer::new(BPE::builder().vocab_and_merges(vocab, vec![]).build().unwrap());
    tokenizer.with_decoder(Some(Fuse::new()));
    tokenizer.add_special_tokens(&[AddedToken::from("<|channel>", true), AddedToken::from("<|tool_call>", true)]);
    tokenizer
}

fn sampling(envelope: Envelope) -> Sampling {
    Sampling { temperature: 0.0, top_p: 1.0, top_k: 0, rng: Rng::new(4), constraint: None, envelope }
}

#[test]
fn every_state_excludes_unmatched_controls_and_open_envelope_eos() {
    let cases = [
        (vec![], vec![1, 48, 50, 52, 100, 106]),
        (vec![100], vec![200]),
        (vec![100, 200], vec![201]),
        (vec![100, 200, 201], vec![52, 101]),
        (vec![48], vec![300]),
        (vec![48, 300], vec![301]),
        (vec![48, 300, 301], vec![49, 52]),
        (vec![48, 300, 301, 52], vec![52]),
    ];
    let controls = [0, 1, 2, 3, 4, 46, 47, 48, 49, 50, 51, 52, 98, 100, 101, 105, 106];
    for (prefix, allowed) in cases {
        let mut envelope = Envelope::fixture();
        for token in prefix { envelope.consume(token).unwrap(); }
        let logits = vec![0.0; 512];
        let actual: Vec<_> = envelope.candidates(&logits).map(|(token, _)| token).collect();
        for token in controls { assert_eq!(actual.contains(&token), allowed.contains(&token), "{:?}: {token}", envelope.state); }
        for token in &allowed { assert!(actual.contains(token)); }
        if matches!(envelope.state, State::ThoughtHeader(_) | State::ToolHeader(_)) {
            assert_eq!(actual, allowed);
        } else { assert!(actual.contains(&400)); }
    }
}

#[test]
fn sampling_masks_orphan_close_without_an_extra_forward_or_dense_mask() {
    let mut sampling = sampling(Envelope::fixture());
    let mut logits = vec![f32::NEG_INFINITY; 512];
    logits[101] = 100.0;
    logits[400] = 2.0;
    assert_eq!(sampling.sample(&logits).unwrap(), 400);
    for token in [100, 200, 201, 101, 48, 300, 301, 52, 400, 52, 49] {
        logits.fill(f32::NEG_INFINITY);
        logits[token] = 1.0;
        assert_eq!(sampling.sample(&logits).unwrap(), token as u32);
    }
    assert!(sampling.envelope.complete());
    logits.fill(f32::NEG_INFINITY);
    logits[101] = 1.0;
    assert!(sampling.sample(&logits).is_err());
}

#[test]
fn schema_mask_and_envelope_support_intersect() {
    let env = llguidance::toktrie::ApproximateTokEnv::single_byte_env();
    let grammars = crate::constraints::Grammars::from_environment(env).unwrap();
    let mut envelope = Envelope::fixture();
    envelope.state = State::Structured;
    let mut sampling = sampling(envelope);
    sampling.constraint = Some(grammars.create(&serde_json::json!({"type":"object"})).unwrap());
    let mut logits = vec![0.0; 256];
    logits[100] = 1000.0;
    logits[usize::from(b'{')] = 2.0;
    assert_eq!(sampling.sample(&logits).unwrap(), u32::from(b'{'));
}

#[test]
fn contracts_preserve_exact_names_and_canonical_header_bytes() {
    let tokenizer = tokenizer();
    let contract = Contract { allowed_tools: vec!["read".into(), "read_file".into()] };
    let mut envelope = contract.compile(&tokenizer, false).unwrap();
    envelope.consume(100).unwrap();
    let tokens = headers::encode(&tokenizer, 100, "thought\n").unwrap();
    for token in tokens { envelope.consume(token).unwrap(); }
    envelope.consume(101).unwrap();
    envelope.consume(48).unwrap();
    let tokens = headers::encode(&tokenizer, 48, "call:read_file{").unwrap();
    for token in tokens { envelope.consume(token).unwrap(); }
    assert_eq!(envelope.state, State::ToolBody);
    assert!(contract.compile(&tokenizer, true).is_err());
    let plain = Contract::default().compile(&tokenizer, false).unwrap();
    assert!(!plain.allowed(48));
    assert!(plain.allowed(52));
    assert!(plain.allowed(100));
}

#[test]
fn contract_bounds_and_call_limits_are_enforced() {
    for names in [vec!["".into()], vec!["read".into(), "read".into()], vec![" read".into()], vec!["r{e".into()], vec!["r\ne".into()], vec!["x".repeat(257)], vec!["x".into(); 513]] {
        assert!(Contract { allowed_tools: names }.validate(false).is_err());
    }
    let names = (0..512).map(|index| format!("{index}{}", "x".repeat(200))).collect();
    assert!(Contract { allowed_tools: names }.validate(false).is_err());
    let mut envelope = Envelope::fixture();
    for _ in 0..64 {
        for token in [48, 300, 301, 49] { envelope.consume(token).unwrap(); }
    }
    assert!(!envelope.allowed(48));
    assert!(envelope.allowed(100));
    assert!(Envelope::fixture().allowed(48));
    assert!(serde_json::from_str::<Contract>("{}").is_err());
    assert!(serde_json::from_str::<Contract>(r#"{"allowed_tools":[],"other":true}"#).is_err());
}
