use crate::error::{Error, Result};
use tokenizers::{Tokenizer, tokenizer::step_decode_stream};

#[derive(Default)]
pub struct StreamingText {
    pending: Vec<u32>,
    prefix: String,
    prefix_index: usize,
    tokens: Vec<u32>,
    emitted: String,
}

impl StreamingText {
    pub fn step(&mut self, tokenizer: &Tokenizer, token: u32) -> Result<String> {
        if self.tokens.len() >= 4096 || token >= 262144 {
            return Err(Error::Capacity("decoded output exceeds its bounds".into()));
        }
        self.tokens.push(token);
        let text = step_decode_stream(
            tokenizer,
            vec![token],
            false,
            &mut self.pending,
            &mut self.prefix,
            &mut self.prefix_index,
        )
        .map_err(|e| Error::Tokenizer(e.to_string()))?
        .unwrap_or_default();
        if self.emitted.len() + text.len() > 4 << 20 {
            return Err(Error::Capacity("decoded output exceeds its bounds".into()));
        }
        self.emitted.push_str(&text);
        Ok(text)
    }

    pub fn finish(self, tokenizer: &Tokenizer) -> Result<String> {
        let full = tokenizer
            .decode(&self.tokens, false)
            .map_err(|e| Error::Tokenizer(e.to_string()))?;
        if full.len() > 4 << 20 {
            return Err(Error::Capacity("decoded output exceeds its bounds".into()));
        }
        full.strip_prefix(&self.emitted)
            .map(str::to_owned)
            .ok_or_else(|| Error::Tokenizer("decoded output changed its emitted prefix".into()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokenizers::{decoders::byte_fallback::ByteFallback, models::bpe::BPE};

    fn tokenizer() -> Tokenizer {
        let model = BPE::builder()
            .vocab_and_merges(
                [
                    ("<0xC3>".to_string(), 0),
                    ("<0xA9>".to_string(), 1),
                    ("!".to_string(), 2),
                ]
                .into_iter()
                .collect::<tokenizers::models::bpe::Vocab>(),
                vec![],
            )
            .byte_fallback(true)
            .build()
            .unwrap();
        let mut tokenizer = Tokenizer::new(model);
        tokenizer.with_decoder(Some(ByteFallback::default()));
        tokenizer
    }

    #[test]
    fn split_utf8_is_emitted_once_when_complete() {
        let tokenizer = tokenizer();
        let mut stream = StreamingText::default();
        assert_eq!(stream.step(&tokenizer, 0).unwrap(), "");
        assert_eq!(stream.step(&tokenizer, 1).unwrap(), "é");
        assert_eq!(stream.step(&tokenizer, 2).unwrap(), "!");
        assert_eq!(stream.finish(&tokenizer).unwrap(), "");
    }

    #[test]
    fn terminal_incomplete_bytes_follow_full_tokenizer_decoding() {
        let tokenizer = tokenizer();
        let mut stream = StreamingText::default();
        assert_eq!(stream.step(&tokenizer, 0).unwrap(), "");
        assert_eq!(
            stream.finish(&tokenizer).unwrap(),
            tokenizer.decode(&[0], false).unwrap()
        );
    }
}
