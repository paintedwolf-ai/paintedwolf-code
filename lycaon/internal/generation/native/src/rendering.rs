use std::io::{Read, Write};
use std::path::Path;

use minijinja::{Environment, ErrorKind};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokenizers::Tokenizer;

use crate::error::{Error, Result};

const MAX_TEXT_BYTES: usize = 4 << 20;

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RenderRequest {
    pub messages: Vec<Value>,
    #[serde(default)]
    pub tools: Vec<Value>,
}

pub struct Renderer {
    environment: Environment<'static>,
    tokenizer: Tokenizer,
    bos: String,
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
        let template = String::from_utf8(read_bounded(
            &directory.join("chat_template.jinja"),
            1 << 20,
        )?)
        .map_err(tokenizer_error)?;
        Ok(Self {
            environment: environment(template)?,
            tokenizer,
            bos,
        })
    }

    pub fn render(&self, request: &RenderRequest, maximum_tokens: usize) -> Result<Vec<u32>> {
        if request.messages.is_empty()
            || request.messages.len() > 2048
            || request.tools.len() > 512
            || !(1..=65536).contains(&maximum_tokens)
        {
            return Err(tokenizer_error("prompt dimensions exceed their bounds"));
        }
        let context = serde_json::json!({
            "messages": request.messages, "tools": request.tools, "bos_token": self.bos,
            "add_generation_prompt": true, "enable_thinking": false, "preserve_thinking": false,
        });
        let mut output = BoundedText(Vec::new());
        self.environment
            .get_template("chat")
            .map_err(tokenizer_error)?
            .render_captured_to(context, &mut output)
            .map_err(tokenizer_error)?;
        let text = String::from_utf8(output.0).map_err(tokenizer_error)?;
        self.tokenize(&text, maximum_tokens)
    }

    pub fn tokenize(&self, text: &str, maximum_tokens: usize) -> Result<Vec<u32>> {
        if text.len() > MAX_TEXT_BYTES || !(1..=65536).contains(&maximum_tokens) {
            return Err(tokenizer_error("tokenizer input exceeds its bounds"));
        }
        let encoding = self
            .tokenizer
            .encode(text, false)
            .map_err(tokenizer_error)?;
        let ids = encoding.get_ids();
        if ids.is_empty() || ids.len() > maximum_tokens || ids.iter().any(|id| *id >= 262144) {
            return Err(Error::Capacity(
                "rendered prompt exceeds its token budget".into(),
            ));
        }
        Ok(ids.to_vec())
    }

    pub fn tokenizer(&self) -> &Tokenizer {
        &self.tokenizer
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn template_supports_python_methods_namespaces_and_nested_arguments() {
        let environment = environment("{%- set ns = namespace(s='') -%}{%- for key, value in args | dictsort -%}{%- set ns.s = ns.s + key + ':' + value.get('text').strip() + ';' -%}{%- endfor -%}{{ ns.s }}".into()).unwrap();
        let output = environment
            .get_template("chat")
            .unwrap()
            .render(serde_json::json!({
                "args": {"z": {"text": " é "}, "a": {"text": " hello "}}
            }))
            .unwrap();
        assert_eq!(output, "a:hello;z:é;");
    }

    #[test]
    fn rendering_limits_output_and_template_work() {
        let mut output = BoundedText(vec![0; MAX_TEXT_BYTES - 1]);
        assert!(output.write_all(b"xx").is_err());
        assert_eq!(output.0.len(), MAX_TEXT_BYTES - 1);
        let mut environment =
            environment("{% for i in range(1000) %}{{ i }}{% endfor %}".into()).unwrap();
        environment.set_fuel(Some(10));
        assert!(
            environment
                .get_template("chat")
                .unwrap()
                .render(())
                .is_err()
        );
    }

    #[test]
    fn template_rejection_is_an_error() {
        let environment = environment("{{ raise_exception('invalid arguments') }}".into()).unwrap();
        assert!(
            environment
                .get_template("chat")
                .unwrap()
                .render(())
                .is_err()
        );
    }
}
