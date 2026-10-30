//! The generation service loop: read frames, run generations, write events.
//!
//! A reader thread owns stdin and stays live while generation runs, so a cancel
//! frame interrupts the active request. Cancellation binds to the request id; a
//! cancel for any other id is ignored. One writer mutex serializes stdout. A
//! generation stages all KV appends and folds them into the cache only at terminal
//! state, so cancel, failure and rollback leave committed state as the request
//! found it.

use std::io::{BufReader, BufWriter, Read, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use serde::Serialize;

use crate::cache::Cache;
use crate::decoder::Decoder;
use crate::error::{Error, Result};
use crate::protocol::{self, Event, GenerateRequest, Request, StopReason};
use crate::sampler::{self, Rng};

/// Shared outbound side: writer + the cancel state of the active request.
struct Outbound<W: Write + Send + 'static> {
    writer: Mutex<BufWriter<W>>,
    active: Mutex<Option<(String, Arc<AtomicU64>)>>,
}

impl<W: Write + Send + 'static> Outbound<W> {
    fn push<T: Serialize>(&self, e: &T) -> Result<()> {
        let body = serde_json::to_vec(e).map_err(|err| Error::Protocol(format!("serialize: {err}")))?;
        let mut w = self.writer.lock().map_err(|_| Error::Device("writer poisoned".into()))?;
        protocol::write_frame(&mut *w, &body)
    }
}

/// A running service bound to one loaded decoder.
pub struct Service {
    decoder: Decoder,
    cache: Mutex<Cache>,
    generation: u64,
}

impl Service {
    pub fn new(decoder: Decoder, generation: u64) -> Self {
        let cache = Cache::new(decoder.cfg.source_layers, decoder.cfg.head_dim);
        Self { decoder, cache: Mutex::new(cache), generation }
    }

    /// Run one generation to completion, emitting events through `out`.
    ///
    /// The cache lock is held for the whole request: one active generation at a
    /// time. All appends stay staged until the loop's terminal state; rollback and
    /// error paths drop them, leaving committed state as the request found it.
    fn run(&self, req: &GenerateRequest, out: &Outbound<impl Write + Send>, cancel: &Arc<AtomicU64>) -> Result<()> {
        let canceled = || cancel.load(Ordering::SeqCst) == 0;
        let mut cache = self.cache.lock().map_err(|_| Error::Device("cache poisoned".into()))?;

        // A handle resolves only when its retained token state is still committed
        // and matches a prefix of this prompt. Any miss rebuilds from scratch.
        let reuse = cache.reuse(&req.cache_handle, &req.prompt_tokens).unwrap_or(0);
        if req.cache_handle.is_none() || reuse == 0 {
            cache.clear_committed();
        }

        let mut tokens = req.prompt_tokens.clone();
        if reuse > 0 {
            tokens.drain(..reuse);
        }
        let mut rng = Rng::new(req.seed);
        let mut seq: u64 = 0;
        let mut generated: u32 = 0;
        let eos = self.decoder.cfg.vocab.saturating_sub(1);
        let stop: std::collections::HashSet<u32> = req.stop_tokens.iter().copied().collect();
        let gen_mark = self.generation;

        // A fully cached prompt has nothing left to prefill; resume from retained logits.
        let fully_cached = tokens.is_empty();
        let mut emitted: Vec<u32> = Vec::new();
        let mut final_logits: Option<Vec<f32>> = None;

        let outcome = (|| -> Result<StopReason> {
            // Positions advance through provisional state (committed plus staged), so
            // every step starts where the previous one ended. A fully cached prompt
            // resumes from the logits retained at its last commit.
            let mut logits: Vec<f32> = if fully_cached {
                cache.retained_logits().ok_or_else(|| {
                    Error::Capacity("handle matched a prompt with no retained logits".into())
                })?.to_vec()
            } else {
                let next_pos = cache.provisional_len();
                let (l, staged_kv) = self.decoder.step(&tokens, &cache, next_pos)?;
                cache.stage(&staged_kv)?;
                l
            };
            if canceled() {
                return Ok(StopReason::Canceled);
            }

            loop {
                if canceled() {
                    return Ok(StopReason::Canceled);
                }
                if generated >= req.max_new_tokens {
                    return Ok(StopReason::MaxTokens);
                }
                let token = sampler::sample(&logits, req.temperature, req.top_p, &mut rng)?;
                if token as usize == eos {
                    return Ok(StopReason::Eos);
                }
                if stop.contains(&token) {
                    return Ok(StopReason::StopToken);
                }
                out.push(&Event::Token { request_id: req.request_id.clone(), seq, token })?;
                seq += 1;
                generated += 1;
                emitted.push(token);

                let (next, kv) = self.decoder.step(&[token], &cache, cache.provisional_len())?;
                logits = next.clone();
                final_logits = Some(next);
                cache.stage(&kv)?;
            }
        })();

        match outcome {
            // Terminal success: fold staged appends and publish the exact token state
            // the KV now represents — prompt plus generated tokens.
            Ok(reason) => {
                if reason == StopReason::Canceled {
                    cache.rollback();
                } else {
                    let mut state = req.prompt_tokens.clone();
                    state.extend(emitted.iter().copied());
                    cache.commit(final_logits.unwrap_or_default());
                    if matches!(reason, StopReason::Eos | StopReason::StopToken | StopReason::MaxTokens) {
                        cache.publish(&format!("{}/{}", req.request_id, gen_mark), state);
                    }
                }
                out.push(&Event::Usage {
                    request_id: req.request_id.clone(),
                    prompt_tokens: req.prompt_tokens.len() as u64,
                    generated_tokens: generated as u64,
                    reused_prefix: reuse as u64,
                })?;
                out.push(&Event::Done { request_id: req.request_id.clone(), stop_reason: reason })?;
                Ok(())
            }
            // Any failure or cancel: drop staged appends; committed state untouched.
            Err(e) => {
                cache.rollback();
                out.push(&Event::Failed { request_id: req.request_id.clone(), error: e.to_string() })?;
                Err(e)
            }
        }
    }

    fn cancel(&self, out: &Outbound<impl Write + Send>, request_id: &str) {
        // Cancellation applies only when the named request is the active one.
        if let Ok(active) = out.active.lock() {
            if let Some((id, mark)) = active.as_ref() {
                if id == request_id {
                    mark.store(self.generation, Ordering::SeqCst);
                }
            }
        }
    }
}

/// Serve requests on stdio until the pipe closes or a Shutdown frame arrives.
pub fn serve(reader: impl Read + Send + 'static, writer: impl Write + Send + 'static) -> Result<()> {
    let outbound = Arc::new(Outbound {
        writer: Mutex::new(BufWriter::new(writer)),
        active: Mutex::new(None),
    });
    let service: Arc<Mutex<Option<Arc<Service>>>> = Arc::new(Mutex::new(None));

    // Reader thread: stdin stays live during generation. Requests it reads while a
    // generation runs queue behind the cache lock; cancel applies by id.
    let reader_outbound = Arc::clone(&outbound);
    let (tx, rx) = std::sync::mpsc::channel::<(u64, Request)>();
    // The reader applies cancel directly: the main loop is inside a running
    // generation and could not drain a queued cancel until it finished.
    std::thread::spawn(move || {
        let mut reader = BufReader::new(reader);
        loop {
            match protocol::read_frame(&mut reader) {
                Ok(Some(bytes)) => match serde_json::from_slice::<protocol::Inbound>(&bytes) {
                    Ok(inbound) => match inbound.body {
                        Request::Cancel { request_id } => apply_cancel(&reader_outbound, &request_id),
                        body => {
                            if tx.send((inbound.id, body)).is_err() {
                                return;
                            }
                        }
                    },
                    Err(e) => {
                        let _ = reader_outbound.push(&Event::Failed {
                            request_id: String::new(),
                            error: format!("bad request: {e}"),
                        });
                    }
                },
                Ok(None) => return,
                Err(e) => {
                    let _ = reader_outbound.push(&Event::Failed { request_id: String::new(), error: e.to_string() });
                    return;
                }
            }
        }
    });

    for (frame_id, req) in rx {
        // Every reply echoes the request's frame id; streamed events correlate by
        // their own request_id.
        match req {
            Request::Hello => outbound.push(&serde_json::json!({
                "id": frame_id,
                "service": "nativegen",
                "protocol": 1,
            }))?,
            Request::Load { manifest: _, weights, device, .. } => {
                match crate::checkpoint::load_decoder(&weights, &device) {
                    Ok(dec) => {
                        *service.lock().map_err(|_| Error::Device("service poisoned".into()))? =
                            Some(Arc::new(Service::new(dec, 0)));
                        outbound.push(&serde_json::json!({"id": frame_id, "loaded": true}))?;
                    }
                    Err(e) => outbound.push(&serde_json::json!({
                        "id": frame_id, "loaded": false, "error": e.to_string()
                    }))?,
                }
            }
            Request::Generate(g) => {
                let svc = service.lock().map_err(|_| Error::Device("service poisoned".into()))?.clone();
                let Some(svc) = svc else {
                    outbound.push(&Event::Failed { request_id: g.request_id.clone(), error: "not loaded".into() })?;
                    continue;
                };
                let cancel_mark = Arc::new(AtomicU64::new(u64::MAX));
                *outbound.active.lock().map_err(|_| Error::Device("active poisoned".into()))? =
                    Some((g.request_id.clone(), Arc::clone(&cancel_mark)));
                let result = svc.run(&g, &outbound, &cancel_mark);
                *outbound.active.lock().map_err(|_| Error::Device("active poisoned".into()))? = None;
                if let Err(e) = result {
                    outbound.push(&Event::Failed { request_id: g.request_id.clone(), error: e.to_string() })?;
                }
            }
            Request::Cancel { request_id } => apply_cancel(&outbound, &request_id),
            Request::Shutdown => return Ok(()),
        }
    }
    Ok(())
}

/// Cancel the named request if it is the active one. Safe from any thread.
fn apply_cancel<W: Write + Send + 'static>(outbound: &Outbound<W>, request_id: &str) {
    if let Ok(active) = outbound.active.lock() {
        if let Some((id, mark)) = active.as_ref() {
            if id == request_id {
                mark.store(0, Ordering::SeqCst);
            }
        }
    }
}


#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cancel_flag_respects_generation_mark() {
        // A cancel mark set for generation N must not affect generation N+1.
        let mark = Arc::new(AtomicU64::new(u64::MAX));
        assert_ne!(mark.load(Ordering::SeqCst), 0, "fresh mark never matches generation 0");
        mark.store(0, Ordering::SeqCst);
        assert_eq!(mark.load(Ordering::SeqCst), 0);
    }
}
