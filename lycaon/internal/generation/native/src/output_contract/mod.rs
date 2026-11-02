use crate::{Error, Result};
use serde::Deserialize;
use tokenizers::Tokenizer;

mod headers;
use headers::{Children, Headers};

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Contract {
    pub allowed_tools: Vec<String>,
}

pub const HOST_BYTES: u64 = 4 << 20;

impl Contract {
    pub fn compile(&self, tokenizer: &Tokenizer, structured: bool) -> Result<Envelope> {
        self.validate(structured)?;
        let mut thought = Headers::new();
        if !structured { thought.insert(&headers::encode(tokenizer, 100, "thought\n")?)?; }
        let mut tools = Headers::new();
        for name in &self.allowed_tools {
            tools.insert(&headers::encode(tokenizer, 48, &format!("call:{name}{{"))?)?;
        }
        Ok(Envelope { thought, tools, state: if structured { State::Structured } else { State::Text }, calls: 0 })
    }

    pub fn validate(&self, structured: bool) -> Result<()> {
        if self.allowed_tools.len() > 512 || (structured && !self.allowed_tools.is_empty()) {
            return Err(Error::Protocol("invalid output tool contract".into()));
        }
        let mut bytes = 0usize;
        let mut seen = std::collections::HashSet::new();
        for name in &self.allowed_tools {
            bytes = bytes.saturating_add(name.len());
            if name.is_empty() || name.len() > 256 || bytes > 65536 || name.trim() != name
                || name.contains(['{', '\n', '\r']) || !seen.insert(name)
            {
                return Err(Error::Protocol("invalid output tool name".into()));
            }
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, PartialEq)]
enum State { Text, ThoughtHeader(u32), ThoughtBody, ToolHeader(u32), ToolBody, ToolQuoted, Structured }

pub struct Envelope {
    thought: Headers,
    tools: Headers,
    state: State,
    calls: usize,
}

pub fn reserved(token: u32) -> bool {
    matches!(token, 0..=4 | 46..=52 | 98 | 100 | 101 | 105 | 106)
}

pub fn terminal(token: u32) -> bool { matches!(token, 1 | 50 | 106) }

impl Envelope {
    pub fn complete(&self) -> bool { matches!(self.state, State::Text | State::Structured) }

    fn allowed(&self, token: u32) -> bool {
        if !reserved(token) { return true; }
        match self.state {
            State::Text => terminal(token) || token == 52 || token == 100
                || (token == 48 && !self.tools.empty() && self.calls < 64),
            State::ThoughtBody => matches!(token, 52 | 101),
            State::ToolBody => matches!(token, 49 | 52),
            State::ToolQuoted => token == 52,
            State::Structured => terminal(token),
            State::ThoughtHeader(_) | State::ToolHeader(_) => false,
        }
    }

    pub fn candidates<'a>(&'a self, logits: &'a [f32]) -> Candidates<'a> {
        let header = match self.state {
            State::ThoughtHeader(node) => Some(self.thought.children(node)),
            State::ToolHeader(node) => Some(self.tools.children(node)),
            _ => None,
        };
        Candidates { envelope: self, logits, header, next: 0 }
    }

    pub fn consume(&mut self, token: u32) -> Result<()> {
        self.state = match self.state {
            State::ThoughtHeader(node) => {
                let (next, done) = self.thought.advance(node, token).ok_or_else(|| self.invalid(token))?;
                if done { State::ThoughtBody } else { State::ThoughtHeader(next) }
            }
            State::ToolHeader(node) => {
                let (next, done) = self.tools.advance(node, token).ok_or_else(|| self.invalid(token))?;
                if done { State::ToolBody } else { State::ToolHeader(next) }
            }
            state => {
                if !self.allowed(token) { return Err(self.invalid(token)); }
                match (state, token) {
                    (State::Text, 100) => State::ThoughtHeader(0),
                    (State::ThoughtBody, 101) => State::Text,
                    (State::Text, 48) => State::ToolHeader(0),
                    (State::ToolBody, 52) => State::ToolQuoted,
                    (State::ToolQuoted, 52) => State::ToolBody,
                    (State::ToolBody, 49) => { self.calls += 1; State::Text }
                    _ => state,
                }
            }
        };
        Ok(())
    }

    fn invalid(&self, token: u32) -> Error {
        Error::Protocol(format!("output token {token} is invalid in {:?}", self.state))
    }

    #[cfg(test)]
    pub(crate) fn fixture() -> Self {
        let mut thought = Headers::new();
        thought.insert(&[200, 201]).unwrap();
        let mut tools = Headers::new();
        tools.insert(&[300, 301]).unwrap();
        Self { thought, tools, state: State::Text, calls: 0 }
    }
}

#[derive(Clone)]
pub struct Candidates<'a> {
    envelope: &'a Envelope,
    logits: &'a [f32],
    header: Option<Children<'a>>,
    next: usize,
}

impl Iterator for Candidates<'_> {
    type Item = (u32, f32);
    fn next(&mut self) -> Option<Self::Item> {
        if let Some(header) = &mut self.header {
            return header.find_map(|(_, token)| self.logits.get(token as usize).map(|value| (token, *value)));
        }
        while self.next < self.logits.len() {
            let index = self.next;
            self.next += 1;
            if self.envelope.allowed(index as u32) { return Some((index as u32, self.logits[index])); }
        }
        None
    }
}

#[cfg(test)]
mod tests;
