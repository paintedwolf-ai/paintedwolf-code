use std::io::{BufReader, BufWriter, Read, Write};
use std::path::{Path, PathBuf};

use crate::{
    error::{Error, Result},
    execution::{Backend, Executor},
    protocol,
    rendering::Renderer,
    rendering_stream::StreamingText,
};
mod settings;
pub mod wire;
use wire::{Code, Command, Envelope, Output, Response};

pub struct Identity {
    pub artifact_id: String,
    pub backend: &'static str,
    pub physical_device: wire::PhysicalDevice,
}

struct Loaded<B> {
    executor: Executor<B>,
    renderer: Renderer,
    decoding: Option<StreamingText>,
}

pub struct Runner<B, F> {
    directory: PathBuf,
    identity: Identity,
    factory: F,
    loaded: Option<Loaded<B>>,
}

impl<B: Backend, F: FnMut(&Path, usize, usize) -> Result<B>> Runner<B, F> {
    pub fn new(directory: PathBuf, identity: Identity, factory: F) -> Result<Self> {
        if identity.artifact_id.len() != 64
            || !identity.artifact_id.bytes().all(|b| b.is_ascii_hexdigit())
        {
            return Err(Error::Protocol("invalid artifact identity".into()));
        }
        Ok(Self {
            directory,
            identity,
            factory,
            loaded: None,
        })
    }

    fn handle(&mut self, command: Command) -> Result<Output> {
        match command {
            Command::Hello {} => Ok(Output::Hello {
                protocol: wire::VERSION,
                backend: self.identity.backend,
                artifact_id: self.identity.artifact_id.clone(),
                physical_device: self.identity.physical_device.clone(),
            }),
            Command::Load {
                context_tokens,
                prefill_chunk,
            } => {
                if self.loaded.is_some() {
                    return Err(Error::Protocol("model is loaded".into()));
                }
                if !(2..=65536).contains(&context_tokens) || !(1..=256).contains(&prefill_chunk) {
                    return Err(Error::Capacity("invalid execution limits".into()));
                }
                let settings = settings::Settings::load(&self.directory)?;
                let renderer = Renderer::load(&self.directory)?;
                let backend = (self.factory)(&self.directory, context_tokens, prefill_chunk)?;
                let executor = Executor::new(backend, context_tokens, prefill_chunk, settings.eos)?;
                self.loaded = Some(Loaded {
                    executor,
                    renderer,
                    decoding: None,
                });
                Ok(Output::Loaded {
                    context_tokens,
                    prefill_chunk,
                    sampling: settings.sampling,
                })
            }
            Command::Unload {} | Command::Shutdown {} => {
                self.unload()?;
                Ok(if matches!(command, Command::Shutdown {}) {
                    Output::Shutdown
                } else {
                    Output::Unloaded
                })
            }
            command => {
                let loaded = self
                    .loaded
                    .as_mut()
                    .ok_or_else(|| Error::Protocol("model is not loaded".into()))?;
                match command {
                    Command::Render {
                        request,
                        max_prompt_tokens,
                    } => Ok(Output::Rendered {
                        prompt_tokens: loaded.renderer.render(&request, max_prompt_tokens)?,
                    }),
                    Command::Begin { request } => {
                        let request_id = request.request_id;
                        let reused_prefix_tokens = loaded.executor.begin(request)?;
                        loaded.decoding = Some(StreamingText::default());
                        Ok(Output::Begun {
                            request_id,
                            reused_prefix_tokens,
                        })
                    }
                    Command::Advance { request_id } => {
                        let progress = loaded.executor.advance(request_id)?;
                        let decoding = loaded
                            .decoding
                            .as_mut()
                            .ok_or_else(|| Error::Protocol("missing output decoder".into()))?;
                        let mut text = match progress.token {
                            Some(token) => decoding.step(loaded.renderer.tokenizer(), token)?,
                            None => String::new(),
                        };
                        if progress.stop_reason.is_some() {
                            text.push_str(
                                &loaded
                                    .decoding
                                    .take()
                                    .unwrap()
                                    .finish(loaded.renderer.tokenizer())?,
                            );
                        }
                        Ok(Output::Progress { progress, text })
                    }
                    Command::Cancel { request_id } => {
                        let progress = loaded.executor.cancel(request_id)?;
                        loaded.decoding = None;
                        Ok(Output::Progress {
                            progress,
                            text: String::new(),
                        })
                    }
                    Command::ClearPrefix {} => {
                        loaded.executor.clear_prefix()?;
                        Ok(Output::Cleared)
                    }
                    _ => unreachable!(),
                }
            }
        }
    }

    fn unload(&mut self) -> Result<()> {
        if let Some(loaded) = self.loaded.take() {
            loaded.executor.release()?;
        }
        Ok(())
    }

    pub fn serve(&mut self, input: impl Read, output: impl Write) -> Result<()> {
        let result = self.serve_frames(input, output);
        let released = self.unload();
        result.and(released)
    }

    fn serve_frames(&mut self, input: impl Read, output: impl Write) -> Result<()> {
        let mut input = BufReader::new(input);
        let mut output = BufWriter::new(output);
        let mut previous = 0;
        while let Some(frame) = protocol::read_frame(&mut input)? {
            let envelope: Envelope =
                serde_json::from_slice(&frame).map_err(|e| Error::Protocol(e.to_string()))?;
            if envelope.version != wire::VERSION || envelope.id <= previous {
                return Err(Error::Protocol(
                    "invalid protocol version or sequence".into(),
                ));
            }
            previous = envelope.id;
            let shutdown = matches!(envelope.command, Command::Shutdown {});
            let result = self.handle(envelope.command);
            let fatal = matches!(&result, Err(Error::Device(_) | Error::Mlx(_)));
            let response = match result {
                Ok(result) => Response::Ok {
                    id: previous,
                    result,
                },
                Err(error) => Response::Error {
                    id: previous,
                    code: code(&error),
                    message: error.to_string(),
                },
            };
            protocol::write_frame(
                &mut output,
                &serde_json::to_vec(&response).map_err(|e| Error::Protocol(e.to_string()))?,
            )?;
            if fatal || shutdown {
                break;
            }
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "tests.rs"]
mod tests;

fn code(error: &Error) -> Code {
    match error {
        Error::Capacity(_) => Code::Capacity,
        Error::Device(_) | Error::Mlx(_) => Code::Device,
        Error::Checkpoint(_) | Error::Tokenizer(_) | Error::Io(_) => Code::Artifact,
        Error::Protocol(_) => Code::InvalidRequest,
    }
}
