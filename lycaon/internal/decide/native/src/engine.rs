//! The inference runtime: one resident backbone, one head per method.

use std::collections::HashMap;
use std::path::{Path, PathBuf};

use candle_core::{DType, Device};
use indexmap::IndexMap;
use serde::Serialize;
use serde_json::Value;

use crate::calibration::{Calibration, confidence_from_probs, round4, softmax};
use crate::checkpoint::Checkpoint;
use crate::config::{AgentConfig, load_encoder_config};
use crate::device::{DeviceChoice, Selected};
use crate::error::{Error, Result};
use crate::heads;
use crate::model::{DecisionModel, Head};
use crate::pyjson;
use crate::question::{QType, Question};
use crate::sequence::{Encoder, Item};

/// Tensor prefixes required before checkpoint loading.
const REQUIRED_PREFIXES: [&str; 4] = ["encoder.", "type_emb.", "scorer.", "act_head."];

/// Default batch size, bounding padding across candidate rows.
pub const DEFAULT_MAX_ROWS: usize = 32;

/// The heads a shared backbone can serve, by the names requests use.
pub const HEAD_NAMES: [&str; 5] = [HEAD_TURN_LOAD, HEAD_GUIDE_LOAD, HEAD_UNIT_RANK, HEAD_CODE_RANK, HEAD_WEB_RANK];
/// Turn decisions: which units a request needs and the turn kind.
pub const HEAD_TURN_LOAD: &str = "turn-load";
/// Guide decisions: which instruction units a request needs, when a separate head answers them.
pub const HEAD_GUIDE_LOAD: &str = "guide-load";
/// Unit ranking: skill cards and loadable tool schemas against a request.
pub const HEAD_UNIT_RANK: &str = "unit-rank";
/// Code ranking: files, definitions, symbols, search hits.
pub const HEAD_CODE_RANK: &str = "code-rank";
/// Web ranking: fetched pages and search snippets.
pub const HEAD_WEB_RANK: &str = "web-rank";

/// Ranking text shared with the trainers under `scripts/bialy/`.
const RANK_RUBRIC: &str = include_str!("../../../../../scripts/bialy/rank-rubric.json");

#[derive(serde::Deserialize)]
struct RankRubric {
    instructions: String,
    levels: Vec<String>,
}

/// The question every candidate is scored on.
pub fn rank_question() -> Question {
    let rubric: RankRubric =
        serde_json::from_str(RANK_RUBRIC).expect("scripts/bialy/rank-rubric.json is valid");
    let mut q = Question::score(rubric.instructions);
    for level in rubric.levels {
        q = q.level(level);
    }
    q.into()
}

/// The state text a candidate is scored from.
pub fn rank_state(task: &str, candidate: &str) -> String {
    format!("Task: {task}\n\nCandidate:\n{candidate}")
}

/// How to load an engine.
#[derive(Clone, Debug, Default)]
pub struct EngineOptions {
    /// The checkpoint directory.
    pub model_dir: PathBuf,
    /// How the host names the checkpoint in receipts; the encoder id when empty.
    pub model_id: String,
    pub device: DeviceChoice,
    /// Head files by name (`turn-load`, `unit-rank`, `code-rank`, `web-rank`).
    pub heads: Vec<(String, PathBuf)>,
    /// Rows per forward pass; `0` means [`DEFAULT_MAX_ROWS`].
    pub max_rows: usize,
    /// Force a precision: `f16` or `f32`. Empty picks half on an accelerator, single on the CPU.
    pub dtype: String,
    /// Token budget for a question head (instructions plus every option); `0` keeps the
    /// checkpoint's own. A multi question over dozens of options needs more than the
    /// checkpoint's default, and the state gets what remains of `max_len`.
    pub head_max_len: usize,
    /// MLX's Metal library, when the runtime is MLX and the file is not beside the executable.
    pub metallib: Option<PathBuf>,
}

/// What the handshake reports about the loaded engine.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct Identity {
    pub name: String,
    pub model: String,
    pub device: String,
    /// The loaded heads as `name=label` pairs; empty when only the checkpoint's own head
    /// loaded.
    pub head: String,
    /// The precision the model runs at: `f16` or `f32`.
    pub dtype: String,
}

/// One typed answer, in the host's shape.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Answer {
    #[serde(rename = "type")]
    pub kind: QType,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub choice: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub probabilities: Option<IndexMap<String, f32>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub score: Option<f32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub distribution: Option<Vec<f32>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub noul: Option<f32>,
    pub confidence: f32,
}

/// The runtime a checkpoint is resident on: candle on any device, or MLX on Apple silicon.
enum Runtime {
    Candle(DecisionModel),
    #[cfg(feature = "mlx")]
    Mlx(crate::mlx::Model),
}

/// A head loaded for the runtime the engine runs.
enum RuntimeHead {
    Candle(Head),
    #[cfg(feature = "mlx")]
    Mlx(crate::mlx::Head),
}

impl Runtime {
    fn dtype_name(&self) -> &'static str {
        match self {
            Runtime::Candle(m) => match m.dtype() {
                DType::F16 => "f16",
                _ => "f32",
            },
            #[cfg(feature = "mlx")]
            Runtime::Mlx(m) => match m.dtype() {
                mlx_rs::Dtype::Float16 => "f16",
                _ => "f32",
            },
        }
    }

    /// Load a head file for this runtime.
    fn load_head(&self, path: &Path, label: &str) -> Result<RuntimeHead> {
        match self {
            Runtime::Candle(m) => {
                let file = heads::read(path)?;
                Ok(RuntimeHead::Candle(Head::from_tensors(
                    file.tensors,
                    m.shape(),
                    label,
                    m.dtype(),
                    m.device(),
                )?))
            }
            #[cfg(feature = "mlx")]
            Runtime::Mlx(m) => {
                Ok(RuntimeHead::Mlx(crate::mlx::Head::from_file(path, m.shape(), m.dtype())?))
            }
        }
    }

    fn forward(
        &self,
        head: &RuntimeHead,
        batch: &crate::sequence::Batch,
    ) -> Result<crate::model::Forward> {
        match (self, head) {
            (Runtime::Candle(m), RuntimeHead::Candle(h)) => m.forward(h, batch),
            #[cfg(feature = "mlx")]
            (Runtime::Mlx(m), RuntimeHead::Mlx(h)) => m.forward(h, batch),
            #[cfg(feature = "mlx")]
            _ => Err(Error::Head("a head loaded for one runtime cannot run on another".into())),
        }
    }
}

/// A loaded checkpoint with its heads, ready to answer.
pub struct Engine {
    runtime: Runtime,
    base: RuntimeHead,
    heads: HashMap<String, RuntimeHead>,
    labels: HashMap<String, String>,
    tool_encodings: HashMap<String, String>,
    encoder: Encoder,
    calibration: Calibration,
    identity: Identity,
    max_rows: usize,
}

impl std::fmt::Debug for Engine {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Engine")
            .field("identity", &self.identity)
            .field("max_rows", &self.max_rows)
            .finish()
    }
}

impl Engine {
    /// Load the checkpoint, open the device, and load every head file.
    pub fn load(opts: &EngineOptions) -> Result<Self> {
        let cp = Checkpoint::from_dir(&opts.model_dir)?;
        let config = AgentConfig::load(&cp.agent_config)?;
        let enc_cfg = load_encoder_config(&cp.encoder_config)?;
        let (tokenizer, special) =
            crate::tokenizer::load(&cp.tokenizer, cp.tokenizer_config.as_deref())?;
        let selected = opts.device.select()?;
        let (runtime, base) = match &selected {
            Selected::Candle(device) => {
                let dtype = match opts.dtype.as_str() {
                    "" => crate::model::dtype_for(device),
                    "f16" => DType::F16,
                    "f32" => DType::F32,
                    other => {
                        return Err(Error::Device(format!(
                            "unknown dtype {other:?}; choose f16 or f32"
                        )));
                    }
                };
                // Validate tensor names and layout on the CPU before device allocation.
                let weights = candle_core::safetensors::load(&cp.weights, &Device::Cpu)?;
                verify(weights.keys(), &cp.label)?;
                verify_layout(
                    weights.keys(),
                    &cp.label,
                    config.head_layers,
                    enc_cfg.num_hidden_layers,
                )?;
                let model =
                    DecisionModel::load(weights, &enc_cfg, config.head_layers, dtype, device)?;
                let base = RuntimeHead::Candle(model.base_head().clone());
                (Runtime::Candle(model), base)
            }
            #[cfg(feature = "mlx")]
            Selected::Mlx => {
                let dtype = match opts.dtype.as_str() {
                    "" | "f16" => mlx_rs::Dtype::Float16,
                    "f32" => mlx_rs::Dtype::Float32,
                    other => {
                        return Err(Error::Device(format!(
                            "unknown dtype {other:?}; choose f16 or f32"
                        )));
                    }
                };
                if let Some(path) = &opts.metallib {
                    crate::mlx::set_metallib(path)?;
                }
                let weights = mlx_rs::Array::load_safetensors(&cp.weights)
                    .map_err(|e| Error::Checkpoint(format!("{}: {e}", cp.weights.display())))?;
                verify(weights.keys(), &cp.label)?;
                verify_layout(
                    weights.keys(),
                    &cp.label,
                    config.head_layers,
                    enc_cfg.num_hidden_layers,
                )?;
                let model = crate::mlx::Model::load(
                    weights,
                    &enc_cfg,
                    config.head_layers,
                    dtype,
                    &cp.label,
                )?;
                let base = RuntimeHead::Mlx(model.base_head().clone());
                (Runtime::Mlx(model), base)
            }
        };

        let mut heads = HashMap::new();
        let mut labels = HashMap::new();
        let mut tool_encodings = HashMap::new();
        for (name, path) in &opts.heads {
            if !HEAD_NAMES.contains(&name.as_str()) {
                return Err(Error::Head(format!(
                    "{name:?} is not a head this engine serves; choose one of {HEAD_NAMES:?}"
                )));
            }
            let (label, head) = load_head(&runtime, path, &config.encoder)?;
            let meta = heads::read_metadata_of(path)?;
            tool_encodings.insert(name.clone(), meta.tool_encoding.unwrap_or_else(|| "joint".into()));
            labels.insert(name.clone(), label);
            heads.insert(name.clone(), head);
        }

        let calibration = Calibration::from_config(&config);
        let model_id =
            if opts.model_id.is_empty() { config.encoder.clone() } else { opts.model_id.clone() };
        let identity = Identity {
            name: "Bialy".to_string(),
            model: model_id,
            device: crate::device::describe(&selected),
            head: head_label(&labels),
            dtype: runtime.dtype_name().to_string(),
        };
        // State and framing tokens share the sequence budget with the head.
        let head_max_len = match opts.head_max_len {
            0 => config.head_max_len,
            n => n.min(config.max_len.saturating_sub(64)),
        };
        Ok(Self {
            runtime,
            base,
            heads,
            labels,
            tool_encodings,
            encoder: Encoder::new(tokenizer, special, config.max_len, head_max_len),
            calibration,
            identity,
            max_rows: if opts.max_rows == 0 { DEFAULT_MAX_ROWS } else { opts.max_rows },
        })
    }

    pub fn identity(&self) -> &Identity {
        &self.identity
    }

    /// The head labels that loaded, by name.
    pub fn head_labels(&self) -> &HashMap<String, String> {
        &self.labels
    }

    pub fn validate_tool_encoding(&self, head: &str, independent: bool) -> Result<()> {
        if let Some(expected) = self.tool_encodings.get(head) {
            let actual = if independent { "independent" } else { "joint" };
            if expected != actual {
                return Err(Error::Head(format!("{head} was trained for {expected} tool options, received {actual}")));
            }
        }
        Ok(())
    }

    /// The head a request named. A bare checkpoint serves its own head everywhere; once
    /// trained heads are loaded, a head that did not load is refused rather than answered
    /// by the untrained one.
    fn head_for(&self, name: &str) -> Result<&RuntimeHead> {
        if !HEAD_NAMES.contains(&name) {
            return Err(Error::Protocol(format!(
                "unknown head {name:?}; choose one of {HEAD_NAMES:?}"
            )));
        }
        if self.heads.is_empty() {
            return Ok(&self.base);
        }
        self.heads
            .get(name)
            .ok_or_else(|| Error::Head(format!("head {name:?} is not loaded; loaded: {:?}", self.loaded_heads())))
    }

    /// The names of the heads that loaded, in catalog order.
    fn loaded_heads(&self) -> Vec<&str> {
        HEAD_NAMES.iter().copied().filter(|name| self.heads.contains_key(*name)).collect()
    }

    /// Run one small batch so device kernels are compiled before the first real request.
    pub fn warm(&self) -> Result<()> {
        let candidates = vec!["File: warm.go\nSymbol: Warm (function)".to_string(); 2];
        self.rank_with(&self.base, "warm", &candidates).map(|_| ())
    }

    /// Answer every question over one state in a single forward pass with the named head.
    pub fn decide(
        &self,
        head: &str,
        state: &Value,
        questions: &IndexMap<String, Question>,
    ) -> Result<IndexMap<String, Answer>> {
        if questions.is_empty() {
            return Ok(IndexMap::new());
        }
        let state_ids = self.encoder.encode_state(&pyjson::render(state))?;
        let items = questions
            .iter()
            .map(|(id, q)| self.encoder.build(&state_ids, id, q))
            .collect::<Result<Vec<_>>>()?;
        let rows = self.forward_rows(self.head_for(head)?, &items)?;
        let mut answers = IndexMap::with_capacity(questions.len());
        for ((id, q), (item, logits)) in questions.iter().zip(items.iter().zip(rows)) {
            answers.insert(id.clone(), self.decode(q.kind, item, &logits));
        }
        Ok(answers)
    }

    /// Score every candidate's relevance to `task` on the ranking rubric with the named head.
    pub fn rank(&self, head: &str, task: &str, candidates: &[String]) -> Result<Vec<f32>> {
        self.rank_with(self.head_for(head)?, task, candidates)
    }

    fn rank_with(&self, head: &RuntimeHead, task: &str, candidates: &[String]) -> Result<Vec<f32>> {
        if candidates.is_empty() {
            return Ok(Vec::new());
        }
        let question = rank_question();
        let mut items = Vec::with_capacity(candidates.len());
        for candidate in candidates {
            let state_ids = self.encoder.encode_state(&rank_state(task, candidate))?;
            items.push(self.encoder.build(&state_ids, "relevance", &question)?);
        }
        let rows = self.forward_rows(head, &items)?;
        Ok(items
            .iter()
            .zip(rows)
            .map(|(item, logits)| self.decode(QType::Score, item, &logits).score.unwrap_or(0.0))
            .collect())
    }

    /// The per-option logits of every item, forwarded in chunks of `max_rows`.
    fn forward_rows(&self, head: &RuntimeHead, items: &[Item]) -> Result<Vec<Vec<f32>>> {
        let mut rows = Vec::with_capacity(items.len());
        for chunk in items.chunks(self.max_rows) {
            let batch = self.encoder.collate(chunk);
            rows.extend(self.runtime.forward(head, &batch)?);
        }
        Ok(rows)
    }

    /// Turn one row of logits into the host's answer shape.
    fn decode(&self, kind: QType, item: &Item, logits: &[f32]) -> Answer {
        let k = item.markers.len();
        let scale = self.calibration.temperature(kind, k);
        let z: Vec<f32> = logits[..k].iter().map(|v| v / scale).collect();
        let p = softmax(&z);
        let confidence = round4(confidence_from_probs(&p, k));
        let mut answer = Answer {
            kind,
            choice: None,
            probabilities: None,
            score: None,
            distribution: None,
            noul: None,
            confidence,
        };
        match kind {
            QType::Multi => {
                // Every option is its own yes/no read from its marker; the calibrated logit
                // stands alone, so its sigmoid is the probability the option applies.
                let probs: Vec<f32> = z.iter().map(|v| 1.0 / (1.0 + (-v).exp())).collect();
                answer.probabilities = Some(
                    item.labels.iter().cloned().zip(probs.iter().map(|v| round4(*v))).collect(),
                );
                let certainty =
                    probs.iter().map(|p| p.max(1.0 - p)).sum::<f32>() / probs.len().max(1) as f32;
                answer.confidence = round4(certainty);
            }
            QType::Choice => {
                // The first of tied maxima.
                let choice = item
                    .labels
                    .iter()
                    .zip(&p)
                    .reduce(|best, cur| if cur.1 > best.1 { cur } else { best })
                    .map(|(label, _)| label.clone())
                    .unwrap_or_default();
                answer.choice = Some(choice);
                answer.probabilities =
                    Some(item.labels.iter().cloned().zip(p.iter().map(|v| round4(*v))).collect());
            }
            QType::Score => {
                let score = p.iter().enumerate().map(|(i, v)| i as f32 * v).sum::<f32>();
                answer.score = Some(round4(score));
                answer.distribution = Some(p.iter().map(|v| round4(*v)).collect());
            }
            QType::Noul => {
                let noul = p.get(1).copied().unwrap_or(0.0);
                answer.noul = Some(round4(noul));
                answer.confidence = round4(noul.max(1.0 - noul));
            }
        }
        answer
    }
}

/// Load one head file, refusing one trained on another backbone.
fn load_head(runtime: &Runtime, path: &Path, backbone: &str) -> Result<(String, RuntimeHead)> {
    let meta = heads::read_metadata_of(path)?;
    if meta.backbone != backbone {
        return Err(Error::Head(format!(
            "{}: trained on {}, but this checkpoint's encoder is {backbone}",
            path.display(),
            meta.backbone
        )));
    }
    let head = runtime.load_head(path, &path.display().to_string())?;
    Ok((meta.label, head))
}

/// One string for the handshake: `name=label` pairs in catalog order.
fn head_label(labels: &HashMap<String, String>) -> String {
    HEAD_NAMES
        .iter()
        .filter_map(|name| labels.get(*name).map(|label| format!("{name}={label}")))
        .collect::<Vec<_>>()
        .join(",")
}

/// Required tensor prefixes identify a supported decision checkpoint.
fn verify<'a>(keys: impl Iterator<Item = &'a String> + Clone, label: &str) -> Result<()> {
    for prefix in REQUIRED_PREFIXES {
        if !keys.clone().any(|k| k.starts_with(prefix)) {
            return Err(Error::Checkpoint(format!(
                "{label}: model.safetensors has no {prefix}* parameters, so it is not a supported \
                 decision checkpoint (expected a ModernBERT encoder plus the typed decision head)."
            )));
        }
    }
    Ok(())
}

/// Top-level tensors a checkpoint may hold besides [`REQUIRED_PREFIXES`] and `head.layers.*`:
/// the training-time temperature parameter, which inference reads from the config instead.
const OPTIONAL_KEYS: [&str; 1] = ["temperature"];

/// Refuse a checkpoint whose tensors do not match its config.
fn verify_layout<'a>(
    keys: impl Iterator<Item = &'a String> + Clone,
    label: &str,
    head_layers: usize,
    encoder_layers: usize,
) -> Result<()> {
    for (prefix, expected, source) in [
        ("head.layers.", head_layers, "head_layers in rl_agent_config.json"),
        ("encoder.layers.", encoder_layers, "num_hidden_layers in encoder/config.json"),
    ] {
        let found = crate::model::layer_indices(keys.clone(), prefix);
        if found != (0..expected).collect::<Vec<_>>() {
            return Err(Error::Checkpoint(format!(
                "{label}: {source} says {expected} layers, but model.safetensors has {prefix}* \
                 layers {found:?}"
            )));
        }
    }
    let known = |k: &str| {
        OPTIONAL_KEYS.contains(&k)
            || k.starts_with("head.layers.")
            || REQUIRED_PREFIXES.iter().any(|p| k.starts_with(p))
    };
    let mut unexpected: Vec<_> = keys.filter(|k| !known(k)).collect();
    if !unexpected.is_empty() {
        unexpected.sort();
        return Err(Error::Checkpoint(format!(
            "{label}: model.safetensors holds tensors no part of the model reads: {unexpected:?}"
        )));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::{scratch_dir, write_tiny_checkpoint, write_tiny_head};
    use candle_core::{DType, Tensor};

    fn weights(keys: &[&str]) -> HashMap<String, Tensor> {
        keys.iter()
            .map(|k| (k.to_string(), Tensor::zeros(1, DType::F32, &Device::Cpu).unwrap()))
            .collect()
    }

    fn tiny_engine() -> (Engine, crate::testutil::ScratchDir) {
        let dir = scratch_dir("engine");
        write_tiny_checkpoint(&dir);
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            model_id: "tiny/model".into(),
            device: DeviceChoice::Cpu,
            heads: Vec::new(),
            max_rows: 2,
            dtype: String::new(),
            head_max_len: 0,
            metallib: None,
        };
        (Engine::load(&opts).unwrap(), dir)
    }

    #[test]
    fn a_file_without_the_decision_head_is_refused_at_load() {
        let complete = [
            "encoder.embeddings.weight",
            "type_emb.weight",
            "scorer.1.weight",
            "act_head.0.weight",
        ];
        assert!(verify(weights(&complete).keys(), "cp").is_ok());
        let err = verify(weights(&complete[..3]).keys(), "cp").unwrap_err();
        assert!(
            matches!(err, Error::Checkpoint(ref m) if m.starts_with("cp:") && m.contains("act_head.")),
            "{err}"
        );
    }

    #[test]
    fn layers_the_config_does_not_describe_are_refused() {
        let keys = [
            "encoder.layers.0.attn.Wqkv.weight",
            "encoder.layers.1.attn.Wqkv.weight",
            "head.layers.0.linear1.weight",
            "head.layers.1.linear1.weight",
            "head.layers.2.linear1.weight",
            "type_emb.weight",
            "temperature",
        ];
        assert!(verify_layout(weights(&keys).keys(), "cp", 3, 2).is_ok());
        let err = verify_layout(weights(&keys).keys(), "cp", 2, 2).unwrap_err();
        assert!(matches!(err, Error::Checkpoint(ref m) if m.contains("head_layers")), "{err}");
        let mut extra = keys.to_vec();
        extra.push("pooler.weight");
        let err = verify_layout(weights(&extra).keys(), "cp", 3, 2).unwrap_err();
        assert!(matches!(err, Error::Checkpoint(ref m) if m.contains("pooler.weight")), "{err}");
    }

    #[test]
    fn the_engine_answers_every_primitive_and_ranks_in_chunks() {
        let (engine, _dir) = tiny_engine();
        assert_eq!(engine.identity().head, "");
        assert_eq!(engine.identity().model, "tiny/model");
        assert_eq!(engine.identity().dtype, "f32");

        let questions: IndexMap<String, Question> = [
            (
                "pick".to_string(),
                Question::choice("pick one").bare_option("a").bare_option("b").into(),
            ),
            (
                "level".to_string(),
                Question::score("pick one").level("a").level("b").level("c").into(),
            ),
            ("yes".to_string(), Question::noul("the statement holds").into()),
        ]
        .into_iter()
        .collect();
        let out = engine
            .decide(HEAD_TURN_LOAD, &Value::String("hello world".into()), &questions)
            .unwrap();
        assert_eq!(out.keys().collect::<Vec<_>>(), ["pick", "level", "yes"]);
        let multi: IndexMap<String, Question> = [(
            "tools".to_string(),
            Question::multi("which?").bare_option("a").bare_option("b").bare_option("c").into(),
        )]
        .into_iter()
        .collect();
        let multi_out =
            engine.decide(HEAD_TURN_LOAD, &Value::String("hello world".into()), &multi).unwrap();
        let tools = &multi_out["tools"];
        assert_eq!(tools.kind, QType::Multi);
        let probs = tools.probabilities.as_ref().unwrap();
        assert_eq!(probs.len(), 3);
        assert!(probs.values().all(|p| (0.0..=1.0).contains(p)));
        assert!(tools.choice.is_none());
        assert!((0.5..=1.0).contains(&tools.confidence));
        let pick = &out["pick"];
        assert!(["a", "b"].contains(&pick.choice.as_deref().unwrap()));
        assert_eq!(pick.probabilities.as_ref().unwrap().len(), 2);
        let level = &out["level"];
        assert!((0.0..=2.0).contains(&level.score.unwrap()));
        assert_eq!(level.distribution.as_ref().unwrap().len(), 3);
        let yes = &out["yes"];
        assert!((0.0..=1.0).contains(&yes.noul.unwrap()));
        assert!(yes.confidence >= 0.5);

        // Five candidates over max_rows=2 forward in three chunks and come back in order.
        let candidates: Vec<String> = (0..5).map(|i| format!("hello world {i}")).collect();
        let scores = engine.rank(HEAD_CODE_RANK, "pick one", &candidates).unwrap();
        assert_eq!(scores.len(), 5);
        assert!(scores.iter().all(|s| (0.0..=4.0).contains(s)));
        assert!(engine.decide(HEAD_TURN_LOAD, &Value::Null, &IndexMap::new()).unwrap().is_empty());
        assert!(engine.rank(HEAD_CODE_RANK, "x", &[]).unwrap().is_empty());
        let err = engine.rank("bogus", "x", &candidates).unwrap_err();
        assert!(matches!(err, Error::Protocol(ref m) if m.contains("bogus")), "{err}");
    }

    #[test]
    fn a_head_file_serves_its_method_and_the_base_head_serves_the_rest() {
        let dir = scratch_dir("engine-heads");
        write_tiny_checkpoint(&dir);
        let rank = write_tiny_head(&dir, "rank-head", "tiny");
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            model_id: String::new(),
            device: DeviceChoice::Cpu,
            heads: vec![(HEAD_CODE_RANK.to_string(), rank)],
            max_rows: 0,
            dtype: String::new(),
            head_max_len: 0,
            metallib: None,
        };
        let engine = Engine::load(&opts).unwrap();
        assert_eq!(engine.identity().head, "code-rank=rank-head");
        assert!(engine.validate_tool_encoding(HEAD_CODE_RANK, false).is_ok());
        assert!(engine.validate_tool_encoding(HEAD_CODE_RANK, true).is_err());
        assert_eq!(engine.identity().model, "tiny");
        assert_eq!(engine.head_labels().get(HEAD_CODE_RANK).map(String::as_str), Some("rank-head"));
        assert!(engine.head_labels().get(HEAD_TURN_LOAD).is_none());
        let candidates = ["hello world".to_string()];
        // The loaded head answers; a head that did not load is refused rather than
        // answered by the checkpoint's own untrained head.
        assert_eq!(engine.rank(HEAD_CODE_RANK, "pick", &candidates).unwrap().len(), 1);
        let refused = engine.rank(HEAD_WEB_RANK, "pick", &candidates).unwrap_err().to_string();
        assert!(refused.contains("not loaded"), "{refused}");
        // Warming never depends on which heads loaded.
        engine.warm().unwrap();
    }

    #[test]
    fn a_head_from_another_backbone_is_refused() {
        let dir = scratch_dir("engine-wrong-head");
        write_tiny_checkpoint(&dir);
        let other = write_tiny_head(&dir, "other", "answerdotai/ModernBERT-large");
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            model_id: String::new(),
            device: DeviceChoice::Cpu,
            heads: vec![(HEAD_TURN_LOAD.to_string(), other)],
            max_rows: 0,
            dtype: String::new(),
            head_max_len: 0,
            metallib: None,
        };
        let err = match Engine::load(&opts) {
            Ok(_) => panic!("a head from another backbone loaded"),
            Err(e) => e,
        };
        assert!(
            matches!(err, Error::Head(ref m) if m.contains("trained on answerdotai/ModernBERT-large")),
            "{err}"
        );
    }

    #[test]
    fn the_handshake_label_lists_loaded_heads_in_catalog_order() {
        let two: HashMap<String, String> = [(HEAD_CODE_RANK, "cr-r3"), (HEAD_TURN_LOAD, "tl-r1")]
            .into_iter()
            .map(|(a, b)| (a.into(), b.into()))
            .collect();
        assert_eq!(head_label(&two), "turn-load=tl-r1,code-rank=cr-r3");
        assert_eq!(head_label(&HashMap::new()), "");
        let unknown = EngineOptions {
            heads: vec![("bogus".to_string(), PathBuf::from("/x"))],
            ..Default::default()
        };
        let err = Engine::load(&unknown).unwrap_err();
        assert!(
            matches!(err, Error::Head(ref m) | Error::Checkpoint(ref m) if !m.is_empty()),
            "{err}"
        );
    }
}
