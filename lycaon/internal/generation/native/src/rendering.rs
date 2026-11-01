use std::io::{Read, Write};
use std::path::Path;

use minijinja::{Environment, ErrorKind};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokenizers::Tokenizer;

use crate::conversation::ConversationFormat;
use crate::error::{Error, Result};

const MAX_TEXT_BYTES: usize = 4 << 20;

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RenderRequest {
    pub messages: Vec<Value>,
    #[serde(default)]
    pub tools: Vec<Value>,
    #[serde(default)]
    pub checkpoint_messages: usize,
}

#[derive(Serialize)]
pub struct Rendered {
    pub prompt_tokens: Vec<u32>,
    pub checkpoint_tokens: usize,
}

#[derive(Serialize)]
pub struct Measurement {
    pub input_tokens: usize,
    #[serde(flatten)]
    pub rendered: Rendered,
}

pub struct Renderer {
    environment: Environment<'static>,
    tokenizer: Tokenizer,
    bos: String,
    conversation: ConversationFormat,
}

fn tokenizer_error(error: impl std::fmt::Display) -> Error {
    Error::Tokenizer(error.to_string())
}

fn read_bounded(path: &Path, maximum: usize) -> Result<Vec<u8>> {
    let file = std::fs::File::open(path)?;
    if !file.metadata()?.is_file() || file.metadata()?.len() > maximum as u64 {
        return Err(Error::Tokenizer("artifact exceeds its bounds".into()));
    }
    let mut bytes = Vec::new();
    file.take(maximum as u64 + 1).read_to_end(&mut bytes)?;
    if bytes.len() > maximum {
        return Err(Error::Tokenizer("artifact exceeds its bounds".into()));
    }
    Ok(bytes)
}

struct BoundedText(Vec<u8>);

impl Write for BoundedText {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        if bytes.len() > MAX_TEXT_BYTES - self.0.len() {
            return Err(std::io::Error::other("rendered prompt exceeds its bound"));
        }
        self.0.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

fn environment(template: String) -> Result<Environment<'static>> {
    let mut environment = Environment::new();
    environment.set_fuel(Some(5_000_000));
    environment.set_recursion_limit(128);
    environment.set_unknown_method_callback(minijinja_contrib::pycompat::unknown_method_callback);
    environment.add_function(
        "raise_exception",
        |message: String| -> std::result::Result<String, minijinja::Error> {
            Err(minijinja::Error::new(ErrorKind::InvalidOperation, message))
        },
    );
    environment
        .add_template_owned("chat", template)
        .map_err(tokenizer_error)?;
    Ok(environment)
}

impl Renderer {
    pub fn load(directory: &Path) -> Result<Self> {
        let config: Value = serde_json::from_slice(&read_bounded(
            &directory.join("tokenizer_config.json"),
            4 << 20,
        )?)
        .map_err(tokenizer_error)?;
        let bos = config["bos_token"]
            .as_str()
            .or_else(|| config["bos_token"]["content"].as_str())
            .ok_or_else(|| tokenizer_error("missing BOS token"))?
            .to_owned();
        let mut tokenizer =
            Tokenizer::from_bytes(read_bounded(&directory.join("tokenizer.json"), 128 << 20)?)
                .map_err(tokenizer_error)?;
        tokenizer.with_truncation(None).map_err(tokenizer_error)?;
        tokenizer.with_padding(None);
        if tokenizer.token_to_id(&bos) != Some(2) || tokenizer.get_vocab_size(true) != 262144 {
            return Err(tokenizer_error("unsupported E4B vocabulary"));
        }
        for (marker, id) in [
            ("<|tool_call>", 48),
            ("<tool_call|>", 49),
            ("<|\"|>", 52),
            ("<|channel>", 100),
            ("<channel|>", 101),
            ("<|turn>", 105),
            ("<turn|>", 106),
        ] {
            if tokenizer.token_to_id(marker) != Some(id) {
                return Err(tokenizer_error("unsupported E4B control tokens"));
            }
        }
        let template = String::from_utf8(read_bounded(
            &directory.join("chat_template.jinja"),
            1 << 20,
        )?)
        .map_err(tokenizer_error)?;
        let conversation = ConversationFormat::load(&config, &template)?;
        Ok(Self {
            environment: environment(template)?,
            tokenizer,
            bos,
            conversation,
        })
    }

    pub fn render(&self, request: &RenderRequest, maximum_tokens: usize) -> Result<Rendered> {
        if !(1..=65536).contains(&maximum_tokens) {
            return Err(Error::Protocol("token budget exceeds its bound".into()));
        }
        let measured = self.measure(request)?;
        if measured.input_tokens > maximum_tokens {
            return Err(Error::Capacity(
                "rendered prompt exceeds its token budget".into(),
            ));
        }
        Ok(measured.rendered)
    }

    pub fn measure(&self, request: &RenderRequest) -> Result<Measurement> {
        if request.messages.is_empty()
            || request.messages.len() > 2048
            || request.tools.len() > 512
            || request.checkpoint_messages > request.messages.len()
        {
            return Err(Error::Protocol(
                "prompt dimensions exceed their bounds".into(),
            ));
        }
        self.conversation.validate(&request.messages)?;
        let prompt_tokens = self.encode(&self.render_text(request, true)?)?;
        let input_tokens = prompt_tokens.len();
        if input_tokens > 65536 {
            return Ok(Measurement {
                input_tokens,
                rendered: Rendered {
                    prompt_tokens: Vec::new(),
                    checkpoint_tokens: 0,
                },
            });
        }
        let mut checkpoint_tokens = 0;
        if request.checkpoint_messages > 0 {
            let prefix = RenderRequest {
                messages: request.messages[..request.checkpoint_messages].to_vec(),
                tools: request.tools.clone(),
                checkpoint_messages: 0,
            };
            if let Ok(tokens) = self
                .render_text(&prefix, false)
                .and_then(|text| self.tokenize(&text, 65536))
            {
                let count = tokens.len().min(prompt_tokens.len().saturating_sub(1));
                if prompt_tokens.starts_with(&tokens[..count]) {
                    checkpoint_tokens = count;
                }
            }
        }
        Ok(Measurement {
            input_tokens,
            rendered: Rendered {
                prompt_tokens,
                checkpoint_tokens,
            },
        })
    }

    fn render_text(&self, request: &RenderRequest, generation_prompt: bool) -> Result<String> {
        let context = serde_json::json!({
            "messages": request.messages, "tools": request.tools, "bos_token": self.bos,
            "add_generation_prompt": generation_prompt, "enable_thinking": false, "preserve_thinking": false,
        });
        let mut output = BoundedText(Vec::new());
        self.environment
            .get_template("chat")
            .map_err(tokenizer_error)?
            .render_captured_to(context, &mut output)
            .map_err(|error| Error::Protocol(error.to_string()))?;
        String::from_utf8(output.0).map_err(tokenizer_error)
    }

    pub fn tokenize(&self, text: &str, maximum_tokens: usize) -> Result<Vec<u32>> {
        if !(1..=65536).contains(&maximum_tokens) {
            return Err(Error::Protocol("token budget exceeds its bound".into()));
        }
        let ids = self.encode(text)?;
        if ids.len() > maximum_tokens {
            return Err(Error::Capacity(
                "rendered prompt exceeds its token budget".into(),
            ));
        }
        Ok(ids)
    }

    fn encode(&self, text: &str) -> Result<Vec<u32>> {
        if text.len() > MAX_TEXT_BYTES {
            return Err(Error::Protocol("tokenizer input exceeds its bounds".into()));
        }
        let encoding = self
            .tokenizer
            .encode(text, false)
            .map_err(tokenizer_error)?;
        let ids = encoding.get_ids();
        if ids.is_empty() || ids.iter().any(|id| *id >= 262144) {
            return Err(Error::Tokenizer("invalid rendered token sequence".into()));
        }
        Ok(ids.to_vec())
    }

    pub fn tokenizer(&self) -> &Tokenizer {
        &self.tokenizer
    }
}

#[cfg(test)]
#[path = "rendering_tests.rs"]
mod tests;
