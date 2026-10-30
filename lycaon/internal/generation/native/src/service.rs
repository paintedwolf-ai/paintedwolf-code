//! The generation service loop: read frames, run generations, write events.
//!
//! One writer serializes events. Cancellation is a flag checked between decode
//! steps; a canceled run rolls its staged KV back and reports the stop reason.

use std::io::{BufReader, BufWriter, Read, Write};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use serde::Serialize;

use crate::cache::Cache;
use crate::decoder::Decoder;
use crate::error::{Error, Result};
use crate::protocol::{self, Event, GenerateRequest, Request, StopReason};
use crate::sampler::{self, Rng};

/// A running service bound to one loaded decoder.
pub struct Service {
    decoder: Decoder,
    cache: Mutex<Cache>,
    canceled: Arc<AtomicBool>,
    generation: u64,
}

impl Service {
    pub fn new(decoder: Decoder, generation: u64) -> Self {
        let cache = Cache::new(decoder.cfg.source_layers, decoder.cfg.head_dim);
        Self { decoder, cache: Mutex::new(cache), canceled: Arc::new(AtomicBool::new(false)), generation }
    }

    /// Run one generation to completion, emitting events through `emit`.
    pub fn generate(&self, req: &GenerateRequest, mut emit: impl FnMut(&Event)) -> Result<()> {
        let mut cache = self.cache.lock().map_err(|_| Error::Device("cache poisoned".into()))?;
        self.canceled.store(false, Ordering::SeqCst);

        let mut tokens = req.prompt_tokens.clone();
        let reused = cache.reuse(&req.cache_handle).unwrap_or(0).min(tokens.len().saturating_sub(1));
        if reused > 0 {
            tokens.drain(..reused); // prefix is already in the cache
        }
        let mut rng = Rng::new(req.seed);
        let mut seq: u64 = 0;
        let mut generated: u32 = 0;

        // Prefill: run the un-cached prompt tail in one step, staging its kv.
        let next_pos = cache.committed_len();
        let (mut logits, staged_kv) = self.decoder.step(&tokens, &cache, next_pos)?;
        cache.stage(&staged_kv)?;
        cache.commit();

        let eos = self.decoder.cfg.vocab - 1; // last id is EOS in Gemma
        let stop: std::collections::HashSet<u32> = req.stop_tokens.iter().copied().collect();

        let stop_reason = loop {
            if self.canceled.load(Ordering::SeqCst) {
                break StopReason::Canceled;
            }
            if generated >= req.max_new_tokens {
                break StopReason::MaxTokens;
            }
            let token = sampler::sample(&logits, req.temperature, req.top_p, &mut rng)?;
            if token as usize == eos {
                break StopReason::Eos;
            }
            if stop.contains(&token) {
                break StopReason::StopToken;
            }
            emit(&Event::Token { request_id: req.request_id.clone(), seq, token });
            seq += 1;
            generated += 1;

            let (next, kv) = self.decoder.step(&[token], &cache, cache.committed_len())?;
            logits = next;
            cache.stage(&kv)?;
            cache.commit();
        };

        let handle = format!("{}-{}", req.request_id, self.generation);
        cache.publish(&handle);
        cache.evict_stale();
        emit(&Event::Usage {
            request_id: req.request_id.clone(),
            prompt_tokens: req.prompt_tokens.len() as u64,
            generated_tokens: generated as u64,
            reused_prefix: reused as u64,
        });
        emit(&Event::Done { request_id: req.request_id.clone(), stop_reason });
        Ok(())
    }

    pub fn cancel(&self) {
        self.canceled.store(true, Ordering::SeqCst);
    }
}

/// Serve requests on stdio until the pipe closes or a Shutdown frame arrives.
pub fn serve(reader: impl Read, writer: impl Write) -> Result<()> {
    let mut reader = BufReader::new(reader);
    let mut writer = BufWriter::new(writer);
    let service: Arc<Mutex<Option<Arc<Service>>>> = Arc::new(Mutex::new(None));

    loop {
        let frame = protocol::read_frame(&mut reader)?;
        let Some(bytes) = frame else { return Ok(()) };
        let req: Request = serde_json::from_slice(&bytes)
            .map_err(|e| Error::Protocol(format!("bad request: {e}")))?;
        match req {
            Request::Hello => reply(&mut writer, serde_json::json!({
                "service": "nativegen",
                "protocol": 1,
            }))?,
            Request::Load { manifest: _, weights, device, .. } => {
                let dec = crate::checkpoint::load_decoder(&weights, &device)?;
                *service.lock().map_err(|_| Error::Device("service poisoned".into()))? =
                    Some(Arc::new(Service::new(dec, 0)));
                reply(&mut writer, serde_json::json!({"loaded": true}))?;
            }
            Request::Generate(g) => {
                let svc = service.lock().map_err(|_| Error::Device("service poisoned".into()))?.clone();
                let Some(svc) = svc else {
                    reply(&mut writer, &Event::Failed { request_id: g.request_id.clone(), error: "not loaded".into() })?;
                    continue;
                };
                // Emit frames through a serialized writer closure.
                let w = Arc::new(Mutex::new(()));
                let result = {
                    let writer_ref = Arc::clone(&w);
                    let mut sink = FrameSink { writer: &mut writer, lock: &writer_ref };
                    svc.generate(&g, |e| sink.push(e))
                };
                let _ = w;
                result?;
            }
            Request::Cancel { .. } => {
                if let Some(svc) = service.lock().map_err(|_| Error::Device("service poisoned".into()))?.as_ref() {
                    svc.cancel();
                }
            }
            Request::Shutdown => return Ok(()),
        }
    }
}

struct FrameSink<'a, W: Write> {
    writer: &'a mut W,
    #[allow(dead_code)]
    lock: &'a Mutex<()>,
}

impl<W: Write> FrameSink<'_, W> {
    fn push(&mut self, e: &Event) {
        let Ok(body) = serde_json::to_vec(e) else { return };
        let _ = protocol::write_frame(self.writer, &body);
    }
}

fn reply<W: Write>(w: &mut W, body: impl Serialize) -> Result<()> {
    let body = serde_json::to_vec(&body).map_err(|e| Error::Protocol(format!("serialize: {e}")))?;
    protocol::write_frame(w, &body)
}
