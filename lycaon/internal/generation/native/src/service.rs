//! Generation service loop over length-prefixed stdio frames.

use std::io::{BufReader, BufWriter, Read, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use serde::Serialize;

use crate::cache::Cache;
use crate::decoder::Decoder;
use crate::error::{Error, Result};
use crate::protocol::{self, Event, GenerateRequest, Request, StopReason};
use crate::sampler::{self, Rng};

struct Outbound<W: Write + Send + 'static> {
    writer: Mutex<BufWriter<W>>,
    active: Mutex<Option<(String, Arc<AtomicU64>)>>,
}

impl<W: Write + Send + 'static> Outbound<W> {
    fn push<T: Serialize>(&self, e: &T) -> Result<()> {
        let body =
            serde_json::to_vec(e).map_err(|err| Error::Protocol(format!("serialize: {err}")))?;
        let mut w = self
            .writer
            .lock()
            .map_err(|_| Error::Device("writer poisoned".into()))?;
        protocol::write_frame(&mut *w, &body)
    }
}

pub struct Service {
    decoder: Decoder,
    tokenizer: Option<tokenizers::Tokenizer>,
    cache: Mutex<Cache>,
    generation: u64,
}

impl Service {
    pub fn new(decoder: Decoder, generation: u64) -> Self {
        Self::with_tokenizer(decoder, None, generation)
    }

    pub fn with_tokenizer(
        decoder: Decoder,
        tokenizer: Option<tokenizers::Tokenizer>,
        generation: u64,
    ) -> Self {
        let slot_widths: Vec<usize> = (0..decoder.cfg.source_layers)
            .map(|s| decoder.cfg.kv_width(s))
            .collect();
        let cache = Cache::with_slot_widths(slot_widths);
        Self {
            decoder,
            tokenizer,
            cache: Mutex::new(cache),
            generation,
        }
    }

    pub fn tokenize(&self, text: &str) -> Result<Vec<u32>> {
        let tok = self
            .tokenizer
            .as_ref()
            .ok_or_else(|| Error::Tokenizer("tokenizer is not loaded".into()))?;
        let enc = tok
            .encode(text, false)
            .map_err(|e| Error::Tokenizer(e.to_string()))?;
        Ok(enc.get_ids().to_vec())
    }

    pub fn detokenize(&self, tokens: &[u32]) -> Result<String> {
        let tok = self
            .tokenizer
            .as_ref()
            .ok_or_else(|| Error::Tokenizer("tokenizer is not loaded".into()))?;
        tok.decode(tokens, false)
            .map_err(|e| Error::Tokenizer(e.to_string()))
    }

    fn incremental_decode(&self, emitted: &[u32], decoded_bytes: &mut usize) -> Result<String> {
        let Some(tok) = &self.tokenizer else {
            return Ok(String::new());
        };
        let full = tok
            .decode(emitted, false)
            .map_err(|e| Error::Tokenizer(e.to_string()))?;
        let stable = if full.ends_with('\u{FFFD}') {
            full.trim_end_matches('\u{FFFD}')
        } else {
            full.as_str()
        };
        if stable.len() < *decoded_bytes || !stable.is_char_boundary(*decoded_bytes) {
            return Ok(String::new());
        }
        let delta = stable[*decoded_bytes..].to_string();
        *decoded_bytes = stable.len();
        Ok(delta)
    }

    fn run(
        &self,
        req: &GenerateRequest,
        out: &Outbound<impl Write + Send>,
        cancel: &Arc<AtomicU64>,
    ) -> Result<()> {
        let canceled = || cancel.load(Ordering::SeqCst) == 0;
        let mut cache = self
            .cache
            .lock()
            .map_err(|_| Error::Device("cache poisoned".into()))?;

        let reuse = cache
            .reuse(&req.cache_handle, &req.prompt_tokens)
            .unwrap_or(0);
        if req.cache_handle.is_none() || reuse == 0 {
            cache.reset_active();
        }

        let mut tokens = req.prompt_tokens.clone();
        if reuse > 0 {
            tokens.drain(..reuse);
        }
        let mut rng = Rng::new(req.seed);
        let mut seq: u64 = 0;
        let mut generated: u32 = 0;
        let eos_set: std::collections::HashSet<u32> =
            self.decoder.cfg.eos_tokens.iter().copied().collect();
        let stop: std::collections::HashSet<u32> = req.stop_tokens.iter().copied().collect();
        let gen_mark = self.generation;

        let fully_cached = tokens.is_empty();
        let mut emitted: Vec<u32> = Vec::new();
        let mut decoded_bytes: usize = 0;
        let mut latest_logits: Vec<f32> = Vec::new();

        out.push(&Event::Accepted {
            request_id: req.request_id.clone(),
            generation: gen_mark,
        })?;

        let outcome = (|| -> Result<StopReason> {
            let mut logits: Vec<f32> = if fully_cached {
                cache
                    .retained_logits()
                    .ok_or_else(|| {
                        Error::Capacity("handle matched a prompt with no retained logits".into())
                    })?
                    .to_vec()
            } else {
                let next_pos = cache.provisional_len();
                let (l, staged_kv) = self.decoder.step(&tokens, &cache, next_pos)?;
                cache.stage(&staged_kv)?;
                l
            };
            latest_logits.clone_from(&logits);
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
                if eos_set.contains(&token) {
                    return Ok(StopReason::Eos);
                }
                if stop.contains(&token) {
                    return Ok(StopReason::StopToken);
                }
                emitted.push(token);
                let text = self.incremental_decode(&emitted, &mut decoded_bytes)?;
                out.push(&Event::Token {
                    request_id: req.request_id.clone(),
                    seq,
                    token,
                    text,
                })?;
                seq += 1;
                generated += 1;

                let (next, kv) = self.decoder.step(&[token], &cache, cache.provisional_len())?;
                logits = next;
                latest_logits.clone_from(&logits);
                cache.stage(&kv)?;
            }
        })();

        match outcome {
            Ok(reason) => {
                if reason == StopReason::Canceled {
                    cache.rollback();
                } else {
                    let mut state = req.prompt_tokens.clone();
                    state.extend(emitted.iter().copied());
                    cache.commit(latest_logits);
                    if matches!(
                        reason,
                        StopReason::Eos | StopReason::StopToken | StopReason::MaxTokens
                    ) {
                        cache.publish(&format!("{}/{}", req.request_id, gen_mark), state);
                    }
                }
                out.push(&Event::Usage {
                    request_id: req.request_id.clone(),
                    prompt_tokens: req.prompt_tokens.len() as u64,
                    generated_tokens: generated as u64,
                    reused_prefix: reuse as u64,
                })?;
                out.push(&Event::Done {
                    request_id: req.request_id.clone(),
                    stop_reason: reason,
                })?;
                Ok(())
            }
            Err(e) => {
                cache.rollback();
                out.push(&Event::Failed {
                    request_id: req.request_id.clone(),
                    error: e.to_string(),
                })?;
                Err(e)
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
    let mut next_generation: u64 = 1;

    let reader_outbound = Arc::clone(&outbound);
    let (tx, rx) = std::sync::mpsc::channel::<(u64, Request)>();
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
                        let (id, request_id) = extract_error_identity(&bytes);
                        let _ = reader_outbound.push(&serde_json::json!({
                            "id": id,
                            "kind": "failed",
                            "request_id": request_id,
                            "error": format!("bad request: {e}"),
                        }));
                    }
                },
                Ok(None) => return,
                Err(e) => {
                    let _ = reader_outbound.push(&Event::Failed {
                        request_id: String::new(),
                        error: e.to_string(),
                    });
                    return;
                }
            }
        }
    });

    for (frame_id, req) in rx {
        match req {
            Request::Hello => outbound.push(&serde_json::json!({
                "id": frame_id,
                "service": "nativegen",
                "protocol": 1,
            }))?,
            Request::Load {
                manifest: _,
                weights,
                device,
                ..
            } => match crate::checkpoint::load_decoder(&weights, &device)
                .and_then(|dec| crate::checkpoint::load_tokenizer(&weights).map(|tok| (dec, tok)))
            {
                Ok((dec, tok)) => {
                    let generation = next_generation;
                    next_generation += 1;
                    *service
                        .lock()
                        .map_err(|_| Error::Device("service poisoned".into()))? =
                        Some(Arc::new(Service::with_tokenizer(dec, Some(tok), generation)));
                    outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "loaded": true,
                        "generation": generation
                    }))?;
                }
                Err(e) => outbound.push(&serde_json::json!({
                    "id": frame_id,
                    "loaded": false,
                    "error": e.to_string()
                }))?,
            },
            Request::Tokenize { text } => {
                let svc = service
                    .lock()
                    .map_err(|_| Error::Device("service poisoned".into()))?
                    .clone();
                let Some(svc) = svc else {
                    outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "error": "not loaded"
                    }))?;
                    continue;
                };
                match svc.tokenize(&text) {
                    Ok(tokens) => outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "tokens": tokens,
                        "count": tokens.len()
                    }))?,
                    Err(e) => outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "error": e.to_string()
                    }))?,
                }
            }
            Request::Detokenize { tokens } => {
                let svc = service
                    .lock()
                    .map_err(|_| Error::Device("service poisoned".into()))?
                    .clone();
                let Some(svc) = svc else {
                    outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "error": "not loaded"
                    }))?;
                    continue;
                };
                match svc.detokenize(&tokens) {
                    Ok(text) => outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "text": text,
                    }))?,
                    Err(e) => outbound.push(&serde_json::json!({
                        "id": frame_id,
                        "error": e.to_string()
                    }))?,
                }
            }
            Request::Generate(g) => {
                let svc = service
                    .lock()
                    .map_err(|_| Error::Device("service poisoned".into()))?
                    .clone();
                let Some(svc) = svc else {
                    outbound.push(&Event::Failed {
                        request_id: g.request_id.clone(),
                        error: "not loaded".into(),
                    })?;
                    continue;
                };
                let cancel_mark = Arc::new(AtomicU64::new(u64::MAX));
                *outbound
                    .active
                    .lock()
                    .map_err(|_| Error::Device("active poisoned".into()))? =
                    Some((g.request_id.clone(), Arc::clone(&cancel_mark)));
                let _ = svc.run(&g, &outbound, &cancel_mark);
                *outbound
                    .active
                    .lock()
                    .map_err(|_| Error::Device("active poisoned".into()))? = None;
            }
            Request::Cancel { request_id } => apply_cancel(&outbound, &request_id),
            Request::Shutdown => return Ok(()),
        }
    }
    Ok(())
}

fn extract_error_identity(bytes: &[u8]) -> (u64, String) {
    let Ok(val) = serde_json::from_slice::<serde_json::Value>(bytes) else {
        return (0, String::new());
    };
    let id = val.get("id").and_then(|v| v.as_u64()).unwrap_or(0);
    let request_id = val
        .get("request_id")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    (id, request_id)
}

fn apply_cancel<W: Write + Send + 'static>(outbound: &Outbound<W>, request_id: &str) {
    if let Ok(active) = outbound.active.lock()
        && let Some((id, mark)) = active.as_ref()
        && id == request_id
    {
        mark.store(0, Ordering::SeqCst);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::decoder::{Config, Decoder, Weights};
    use std::collections::HashMap;
    use std::io::Cursor;

    fn tiny_service() -> Service {
        let cfg = Config {
            hidden: 4,
            heads: 2,
            kv_heads: 1,
            head_dim: 2,
            full_head_dim: 2,
            source_layers: 1,
            total_layers: 1,
            rope_theta: 10_000.0,
            full_rope_theta: 10_000.0,
            partial_rotary_factor: 1.0,
            vocab: 5,
            eps: 1e-5,
            sliding_window: 8,
            full_layers_skip_rope: true,
            attention_scale: 1.0,
            normalize_v: true,
            ple_layers: 0,
            eos_tokens: vec![4],
        };
        let mut tensors = HashMap::new();
        tensors.insert("model.embed_tokens.weight".into(), vec![0.1; 5 * 4]);
        tensors.insert("model.norm.weight".into(), vec![1.0; 4]);
        let p = "model.layers.0";
        tensors.insert(format!("{p}.input_layernorm.weight"), vec![1.0; 4]);
        tensors.insert(format!("{p}.self_attn.q_proj.weight"), vec![0.1; 4 * 4]);
        tensors.insert(format!("{p}.self_attn.k_proj.weight"), vec![0.1; 2 * 4]);
        tensors.insert(format!("{p}.self_attn.v_proj.weight"), vec![0.1; 2 * 4]);
        tensors.insert(format!("{p}.self_attn.o_proj.weight"), vec![0.1; 4 * 4]);
        tensors.insert(format!("{p}.self_attn.q_norm.weight"), vec![1.0; 2]);
        tensors.insert(format!("{p}.self_attn.k_norm.weight"), vec![1.0; 2]);
        tensors.insert(format!("{p}.pre_feedforward_layernorm.weight"), vec![1.0; 4]);
        tensors.insert(format!("{p}.mlp.gate_proj.weight"), vec![0.1; 8 * 4]);
        tensors.insert(format!("{p}.mlp.up_proj.weight"), vec![0.1; 8 * 4]);
        tensors.insert(format!("{p}.mlp.down_proj.weight"), vec![0.1; 4 * 8]);
        let dec = Decoder::load(cfg, Weights { tensors }).unwrap();
        Service::new(dec, 1)
    }

    #[test]
    fn cancel_flag_respects_generation_mark() {
        let mark = Arc::new(AtomicU64::new(u64::MAX));
        assert_ne!(mark.load(Ordering::SeqCst), 0);
        mark.store(0, Ordering::SeqCst);
        assert_eq!(mark.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn run_generates_commits_and_reuses_exact_prefix() {
        let svc = tiny_service();
        let buf = Arc::new(Mutex::new(Vec::<u8>::new()));
        struct SharedVec(Arc<Mutex<Vec<u8>>>);
        impl Write for SharedVec {
            fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
                self.0.lock().unwrap().extend_from_slice(data);
                Ok(data.len())
            }
            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }

        let out = Outbound {
            writer: Mutex::new(BufWriter::new(SharedVec(Arc::clone(&buf)))),
            active: Mutex::new(None),
        };
        let cancel = Arc::new(AtomicU64::new(u64::MAX));
        let req1 = GenerateRequest {
            request_id: "r1".into(),
            prompt_tokens: vec![1, 2],
            max_new_tokens: 2,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: None,
        };
        svc.run(&req1, &out, &cancel).unwrap();

        let req2 = GenerateRequest {
            request_id: "r2".into(),
            prompt_tokens: vec![1, 2, 0, 0, 3],
            max_new_tokens: 1,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: Some("r1/1".into()),
        };
        svc.run(&req2, &out, &cancel).unwrap();

        let raw = buf.lock().unwrap().clone();
        let mut cursor = Cursor::new(raw);
        let mut usages = Vec::new();
        while let Some(frame) = protocol::read_frame(&mut cursor).unwrap() {
            let ev: Event = serde_json::from_slice(&frame).unwrap();
            if let Event::Usage {
                request_id,
                reused_prefix,
                ..
            } = ev
            {
                usages.push((request_id, reused_prefix));
            }
        }
        assert_eq!(usages, vec![("r1".into(), 0), ("r2".into(), 4)]);
    }

    #[test]
    fn reusing_older_handle_after_extension_matches_direct_reuse() {
        let svc = tiny_service();
        let buf = Arc::new(Mutex::new(Vec::<u8>::new()));
        struct SharedVec(Arc<Mutex<Vec<u8>>>);
        impl Write for SharedVec {
            fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
                self.0.lock().unwrap().extend_from_slice(data);
                Ok(data.len())
            }
            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }

        let out = Outbound {
            writer: Mutex::new(BufWriter::new(SharedVec(Arc::clone(&buf)))),
            active: Mutex::new(None),
        };
        let cancel = Arc::new(AtomicU64::new(u64::MAX));

        // 1. Publish handle r1/1 for prompt [1, 2] + 1 generated token (total len 3).
        let req_a = GenerateRequest {
            request_id: "r1".into(),
            prompt_tokens: vec![1, 2],
            max_new_tokens: 1,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: None,
        };
        svc.run(&req_a, &out, &cancel).unwrap();

        // 2. Reuse r1/1 directly to establish expected tokens and usage.
        let req_direct = GenerateRequest {
            request_id: "direct".into(),
            prompt_tokens: vec![1, 2, 0, 3],
            max_new_tokens: 2,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: Some("r1/1".into()),
        };
        svc.run(&req_direct, &out, &cancel).unwrap();

        // 3. Extend further and publish r2/1 so the active cache state moves past r1/1.
        let req_b = GenerateRequest {
            request_id: "r2".into(),
            prompt_tokens: vec![1, 2, 0, 3, 2, 1],
            max_new_tokens: 2,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: Some("r1/1".into()),
        };
        svc.run(&req_b, &out, &cancel).unwrap();

        // 4. Reuse the older handle r1/1 again after r2/1 was published.
        let req_reused_a = GenerateRequest {
            request_id: "after_b".into(),
            prompt_tokens: vec![1, 2, 0, 3],
            max_new_tokens: 2,
            seed: 7,
            temperature: 0.0,
            top_p: 1.0,
            stop_tokens: vec![],
            cache_handle: Some("r1/1".into()),
        };
        svc.run(&req_reused_a, &out, &cancel).unwrap();

        let raw = buf.lock().unwrap().clone();
        let mut cursor = Cursor::new(raw);
        let mut direct_tokens = Vec::new();
        let mut after_b_tokens = Vec::new();
        let mut after_b_reused = 0;
        while let Some(frame) = protocol::read_frame(&mut cursor).unwrap() {
            let ev: Event = serde_json::from_slice(&frame).unwrap();
            match ev {
                Event::Token {
                    request_id, token, ..
                } if request_id == "direct" => direct_tokens.push(token),
                Event::Token {
                    request_id, token, ..
                } if request_id == "after_b" => after_b_tokens.push(token),
                Event::Usage {
                    request_id,
                    reused_prefix,
                    ..
                } if request_id == "after_b" => after_b_reused = reused_prefix,
                _ => {}
            }
        }
        assert_eq!(after_b_reused, 3);
        assert_eq!(direct_tokens, after_b_tokens);
    }

    #[test]
    fn tokenize_without_loaded_tokenizer_fails() {
        let svc = tiny_service();
        assert!(svc.tokenize("hello").is_err());
    }

    #[test]
    fn extract_error_identity_preserves_request_fields() {
        let raw = br#"{"id":42,"method":"generate","request_id":"req-42"}"#;
        assert_eq!(extract_error_identity(raw), (42, "req-42".into()));
    }

    fn write_tiny_checkpoint_files(dir: &std::path::Path, include_tokenizer: bool) {
        let cfg = serde_json::json!({
            "hidden_size": 4,
            "num_attention_heads": 2,
            "num_key_value_heads": 1,
            "head_dim": 2,
            "full_head_dim": 2,
            "num_hidden_layers": 1,
            "rope_theta": 10000.0,
            "vocab_size": 5,
            "rms_norm_eps": 1e-5,
            "sliding_window": 8,
            "eos_token_id": 4
        });
        std::fs::write(dir.join("config.json"), cfg.to_string()).unwrap();

        let specs: &[(&str, &[usize])] = &[
            ("model.embed_tokens.weight", &[5, 4]),
            ("model.norm.weight", &[4]),
            ("model.layers.0.input_layernorm.weight", &[4]),
            ("model.layers.0.self_attn.q_proj.weight", &[4, 4]),
            ("model.layers.0.self_attn.k_proj.weight", &[2, 4]),
            ("model.layers.0.self_attn.v_proj.weight", &[2, 4]),
            ("model.layers.0.self_attn.o_proj.weight", &[4, 4]),
            ("model.layers.0.self_attn.q_norm.weight", &[2]),
            ("model.layers.0.self_attn.k_norm.weight", &[2]),
            ("model.layers.0.pre_feedforward_layernorm.weight", &[4]),
            ("model.layers.0.mlp.gate_proj.weight", &[8, 4]),
            ("model.layers.0.mlp.up_proj.weight", &[8, 4]),
            ("model.layers.0.mlp.down_proj.weight", &[4, 8]),
        ];
        let mut header = serde_json::Map::new();
        let mut data = Vec::<u8>::new();
        for (name, shape) in specs {
            let count: usize = shape.iter().product();
            let start = data.len();
            for _ in 0..count {
                data.extend_from_slice(&0.1f32.to_le_bytes());
            }
            let end = data.len();
            header.insert(
                (*name).to_string(),
                serde_json::json!({
                    "dtype": "F32",
                    "shape": shape,
                    "data_offsets": [start, end],
                }),
            );
        }
        let header_bytes = serde_json::to_vec(&header).unwrap();
        let mut st = Vec::new();
        st.extend_from_slice(&(header_bytes.len() as u64).to_le_bytes());
        st.extend_from_slice(&header_bytes);
        st.extend_from_slice(&data);
        std::fs::write(dir.join("model.safetensors"), st).unwrap();

        if include_tokenizer {
            let tok_json = serde_json::json!({
                "version": "1.0",
                "truncation": null,
                "padding": null,
                "added_tokens": [],
                "normalizer": null,
                "pre_tokenizer": {
                    "type": "Whitespace"
                },
                "post_processor": null,
                "decoder": null,
                "model": {
                    "type": "WordLevel",
                    "vocab": {
                        "<unk>": 0,
                        "hello": 1,
                        "world": 2,
                        "test": 3,
                        "<eos>": 4
                    },
                    "unk_token": "<unk>"
                }
            });
            std::fs::write(dir.join("tokenizer.json"), tok_json.to_string()).unwrap();
        }
    }

    #[test]
    fn serve_rejects_load_without_tokenizer_and_decodes_with_tokenizer() {
        let missing_dir = std::env::temp_dir().join(format!(
            "nativegen-missing-tok-{}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&missing_dir);
        std::fs::create_dir_all(&missing_dir).unwrap();
        write_tiny_checkpoint_files(&missing_dir, false);

        let valid_dir = std::env::temp_dir().join(format!(
            "nativegen-valid-tok-{}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&valid_dir);
        std::fs::create_dir_all(&valid_dir).unwrap();
        write_tiny_checkpoint_files(&valid_dir, true);

        let mut req_buf = Vec::new();
        let push_req = |buf: &mut Vec<u8>, v: serde_json::Value| {
            let bytes = serde_json::to_vec(&v).unwrap();
            protocol::write_frame(buf, &bytes).unwrap();
        };
        push_req(
            &mut req_buf,
            serde_json::json!({
                "id": 1,
                "method": "load",
                "manifest": "",
                "weights": missing_dir.to_string_lossy(),
                "device": "cpu"
            }),
        );
        push_req(
            &mut req_buf,
            serde_json::json!({
                "id": 2,
                "method": "load",
                "manifest": "",
                "weights": valid_dir.to_string_lossy(),
                "device": "cpu"
            }),
        );
        push_req(
            &mut req_buf,
            serde_json::json!({
                "id": 3,
                "method": "tokenize",
                "text": "hello world"
            }),
        );
        push_req(
            &mut req_buf,
            serde_json::json!({
                "id": 4,
                "method": "detokenize",
                "tokens": [1, 2]
            }),
        );
        push_req(
            &mut req_buf,
            serde_json::json!({
                "id": 5,
                "method": "shutdown"
            }),
        );

        let resp_buf = Arc::new(Mutex::new(Vec::<u8>::new()));
        struct SharedOut(Arc<Mutex<Vec<u8>>>);
        impl Write for SharedOut {
            fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
                self.0.lock().unwrap().extend_from_slice(data);
                Ok(data.len())
            }
            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }
        serve(Cursor::new(req_buf), SharedOut(Arc::clone(&resp_buf))).unwrap();

        let _ = std::fs::remove_dir_all(&missing_dir);
        let _ = std::fs::remove_dir_all(&valid_dir);

        let raw = resp_buf.lock().unwrap().clone();
        let mut cursor = Cursor::new(raw);
        let mut frames = Vec::new();
        while let Some(frame) = protocol::read_frame(&mut cursor).unwrap() {
            frames.push(serde_json::from_slice::<serde_json::Value>(&frame).unwrap());
        }
        assert_eq!(frames.len(), 4);
        assert_eq!(frames[0]["id"], 1);
        assert_eq!(frames[0]["loaded"], false);
        assert!(frames[0]["error"].as_str().unwrap_or("").contains("tokenizer.json"));

        assert_eq!(frames[1]["id"], 2);
        assert_eq!(frames[1]["loaded"], true);

        assert_eq!(frames[2]["id"], 3);
        assert_eq!(frames[2]["tokens"], serde_json::json!([1, 2]));

        assert_eq!(frames[3]["id"], 4);
        assert_eq!(frames[3]["text"], "hello world");
    }
}
