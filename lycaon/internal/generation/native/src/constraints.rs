use crate::error::{Error, Result};
use llguidance::{
    Matcher, ParserFactory,
    api::TopLevelGrammar,
    toktrie::{ApproximateTokEnv, TokEnv, TokRxInfo, TokTrie},
};
use serde_json::Value;
use std::{io::Read, path::Path, sync::Arc};

pub struct Grammars(ParserFactory);

fn invalid(error: impl std::fmt::Display) -> Error {
    Error::Protocol(format!("structured output: {error}"))
}

impl Grammars {
    pub fn load(directory: &Path) -> Result<Self> {
        let file = std::fs::File::open(directory.join("tokenizer.json"))?;
        if !file.metadata()?.is_file() || file.metadata()?.len() > 128 << 20 {
            return Err(Error::Checkpoint("tokenizer exceeds its bound".into()));
        }
        let json: Value = serde_json::from_reader(file.take((128 << 20) + 1)).map_err(invalid)?;
        let words = llguidance::token_bytes_from_tokenizer_json(&json).map_err(invalid)?;
        if words.len() != 262144 || words.iter().any(|word| word.len() > 4096) {
            return Err(Error::Checkpoint("invalid grammar vocabulary".into()));
        }
        let info = TokRxInfo {
            vocab_size: 262144,
            tok_eos: 1,
            tok_bos: Some(2),
            tok_pad: Some(0),
            tok_unk: None,
            tok_end_of_turn: Some(106),
        };
        let env: TokEnv = Arc::new(ApproximateTokEnv::new(TokTrie::from(&info, &words)));
        Self::from_environment(env)
    }

    fn from_environment(env: TokEnv) -> Result<Self> {
        let mut factory = ParserFactory::new_simple(&env).map_err(invalid)?;
        factory.quiet();
        let limits = factory.limits_mut();
        limits.max_lexer_states = 20000;
        limits.max_grammar_size = 50000;
        limits.verbose_errors = false;
        Ok(Self(factory))
    }

    pub fn create(&self, schema: &Value) -> Result<Matcher> {
        if serde_json::to_vec(schema).map_err(invalid)?.len() > 256 << 10 {
            return Err(Error::Capacity("output schema exceeds its bound".into()));
        }
        let parser = self
            .0
            .create_parser(TopLevelGrammar::from_json_schema(schema.clone()))
            .map_err(invalid)?;
        let mut matcher = Matcher::new(Ok(parser));
        if !matcher.grammar_warnings().is_empty() {
            return Err(invalid("schema contains unsupported constraints"));
        }
        Ok(matcher)
    }
}

pub fn mask_logits(matcher: &mut Matcher, logits: &[f32]) -> Result<Vec<f32>> {
    let mask = matcher.compute_mask().map_err(invalid)?;
    Ok(logits
        .iter()
        .enumerate()
        .map(|(id, value)| {
            if mask.is_allowed(id as u32) {
                *value
            } else {
                f32::NEG_INFINITY
            }
        })
        .collect())
}

pub fn complete(matcher: &mut Matcher) -> Result<bool> {
    Ok(matcher.is_stopped() && matcher.is_accepting().map_err(invalid)?)
}

pub fn consume(matcher: &mut Matcher, token: u32) -> Result<()> {
    matcher.consume_token(token).map_err(invalid)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn masks_invalid_bytes_and_accepts_only_complete_schema_values() {
        let env = ApproximateTokEnv::single_byte_env();
        let eos = env.eos_token();
        let grammars = Grammars::from_environment(env).unwrap();
        let mut matcher = grammars.create(&serde_json::json!({"type":"object","properties":{"answer":{"const":4}},"required":["answer"],"additionalProperties":false})).unwrap();
        assert!(!matcher.compute_mask().unwrap().is_allowed(b'x' as u32));
        assert!(!matcher.compute_mask().unwrap().is_allowed(eos));
        for byte in br#"{"answer":4}"# {
            assert!(matcher.compute_mask().unwrap().is_allowed(*byte as u32));
            consume(&mut matcher, *byte as u32).unwrap();
        }
        assert!(matcher.is_accepting().unwrap());
        assert!(complete(&mut matcher).unwrap());
    }
}
