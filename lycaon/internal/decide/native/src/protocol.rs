//! The stdio protocol the host speaks: one JSON request per line, one JSON reply per line.
//!
//! ```text
//! {"id": 1, "method": "hello"}
//! {"id": 2, "method": "decide", "head": "turn-load", "state": {...} | "...", "questions": {qid: {...}}}
//! {"id": 3, "method": "rank", "head": "code-rank", "task": "...", "candidates": ["...", ...]}
//! ```
//!
//! `head` names the decision head; `decide` defaults to `turn-load` and `rank` to `code-rank`.
//!
//! Replies mirror the id: `{"id": 1, "engine": {...}, "heads": {...}}`,
//! `{"id": 2, "answers": {qid: {...}}}`, `{"id": 3, "scores": [...]}`, or
//! `{"id": n, "error": "..."}`. Questions arrive in the host's shape (`type`, `instructions`,
//! `options` | `levels` | `true`/`false`) and are translated to the checkpoint's.

use std::collections::{BTreeMap, HashMap};
use std::io::{BufRead, Write};

use indexmap::IndexMap;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::engine::{Answer, Engine, HEAD_CODE_RANK, HEAD_TURN_LOAD, Identity};
use crate::error::{Error, Result};
use crate::question::{QType, Question};

/// A question as the host declares it.
#[derive(Clone, Debug, Deserialize)]
pub struct HostQuestion {
    #[serde(rename = "type")]
    pub kind: QType,
    #[serde(default)]
    pub instructions: String,
    /// Encode each multi-label option in its own row, independent of the roster.
    #[serde(default)]
    pub independent: bool,
    /// Sorted, as the host encodes its option map; the order is the marker order.
    #[serde(default)]
    pub options: Option<BTreeMap<String, String>>,
    #[serde(default)]
    pub levels: Option<Vec<String>>,
    #[serde(default, rename = "true")]
    pub when_true: Option<String>,
    #[serde(default, rename = "false")]
    pub when_false: Option<String>,
}

impl HostQuestion {
    /// The checkpoint's question shape: `criteria` in the form its type renders.
    pub fn to_question(&self) -> Question {
        let criteria = match self.kind {
            QType::Choice | QType::Multi => Some(Value::Object(
                self.options
                    .iter()
                    .flatten()
                    .map(|(k, v)| (k.clone(), Value::String(v.clone())))
                    .collect::<Map<_, _>>(),
            )),
            QType::Score => Some(Value::Array(
                self.levels.iter().flatten().map(|l| Value::String(l.clone())).collect(),
            )),
            QType::Noul => {
                let mut crit = Map::new();
                if let Some(t) = self.when_true.as_ref().filter(|s| !s.is_empty()) {
                    crit.insert("true".into(), Value::String(t.clone()));
                }
                if let Some(f) = self.when_false.as_ref().filter(|s| !s.is_empty()) {
                    crit.insert("false".into(), Value::String(f.clone()));
                }
                if crit.is_empty() { None } else { Some(Value::Object(crit)) }
            }
        };
        Question {
            kind: self.kind,
            instructions: Value::String(self.instructions.clone()),
            criteria,
        }
    }
}

#[derive(Debug, Deserialize)]
pub struct Request {
    #[serde(default)]
    pub id: i64,
    #[serde(default)]
    pub method: String,
    #[serde(default)]
    pub head: String,
    #[serde(default)]
    pub state: Value,
    #[serde(default)]
    pub questions: IndexMap<String, HostQuestion>,
    #[serde(default)]
    pub task: String,
    #[serde(default)]
    pub candidates: Vec<String>,
}

#[derive(Debug, Default, Serialize)]
pub struct Response {
    pub id: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub engine: Option<Identity>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub heads: Option<HashMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub answers: Option<IndexMap<String, Answer>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub scores: Option<Vec<f32>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Answer one request.
pub fn handle(engine: &Engine, req: &Request) -> Result<Response> {
    let mut resp = Response { id: req.id, ..Default::default() };
    match req.method.as_str() {
        "hello" => {
            resp.engine = Some(engine.identity().clone());
            resp.heads = Some(engine.head_labels().clone());
        }
        "decide" => {
            let head = if req.head.is_empty() { HEAD_TURN_LOAD } else { &req.head };
            resp.answers = Some(decide_questions(engine, head, &req.state, &req.questions)?);
        }
        "rank" => {
            let head = if req.head.is_empty() { HEAD_CODE_RANK } else { &req.head };
            resp.scores = Some(engine.rank(head, &req.task, &req.candidates)?);
        }
        other => return Err(Error::Protocol(format!("unknown method {other:?}"))),
    }
    Ok(resp)
}

fn decide_questions(
    engine: &Engine,
    head: &str,
    state: &Value,
    questions: &IndexMap<String, HostQuestion>,
) -> Result<IndexMap<String, Answer>> {
    let mut encoded = IndexMap::new();
    let mut groups = Vec::new();
    for (id, q) in questions {
        if id == "tools" {
            engine.validate_tool_encoding(head, q.independent)?;
        }
        let start = encoded.len();
        if q.independent {
            if q.kind != QType::Multi {
                return Err(Error::question(id, "independent options require a multi question"));
            }
            // Validate even an empty roster through the same question contract.
            q.to_question().labels(id)?;
            for (name, description) in q.options.iter().flatten() {
                let mut one = q.clone();
                one.options = Some([(name.clone(), description.clone())].into_iter().collect());
                encoded.insert(encoded.len().to_string(), one.to_question());
            }
        } else {
            encoded.insert(encoded.len().to_string(), q.to_question());
        }
        groups.push((id, q.independent, start..encoded.len()));
    }
    let answers = engine.decide(head, state, &encoded)?;
    let mut out = IndexMap::new();
    for (id, independent, range) in groups {
        let mut answer = answers[&range.start.to_string()].clone();
        if independent {
            let mut probabilities = IndexMap::new();
            for index in range {
                if let Some(values) = &answers[&index.to_string()].probabilities {
                    probabilities.extend(values.iter().map(|(k, v)| (k.clone(), *v)));
                }
            }
            answer.confidence = crate::calibration::round4(
                probabilities.values().map(|p| p.max(1.0 - p)).sum::<f32>()
                    / probabilities.len().max(1) as f32,
            );
            answer.probabilities = Some(probabilities);
        }
        out.insert(id.clone(), answer);
    }
    Ok(out)
}

/// Serve requests from `input` to `output` until the input closes.
///
/// Every fault is reported on the wire under the request's id; only an unwritable output ends
/// the loop early.
pub fn serve(engine: &Engine, input: impl BufRead, mut output: impl Write) -> std::io::Result<()> {
    for line in input.lines() {
        let line = line?;
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        let resp = match serde_json::from_str::<Request>(line) {
            Ok(req) => handle(engine, &req).unwrap_or_else(|e| Response {
                id: req.id,
                error: Some(e.to_string()),
                ..Default::default()
            }),
            Err(e) => Response {
                id: 0,
                error: Some(format!("unreadable request: {e}")),
                ..Default::default()
            },
        };
        serde_json::to_writer(&mut output, &resp)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::device::DeviceChoice;
    use crate::engine::EngineOptions;
    use crate::testutil::{scratch_dir, write_tiny_checkpoint};
    use serde_json::json;

    fn engine() -> Engine {
        let dir = scratch_dir("protocol");
        write_tiny_checkpoint(&dir);
        let opts = EngineOptions {
            model_dir: dir.to_path_buf(),
            model_id: "tiny/model".into(),
            device: DeviceChoice::Cpu,
            heads: Vec::new(),
            max_rows: 0,
            dtype: String::new(),
            head_max_len: 0,
            metallib: None,
        };
        let engine = Engine::load(&opts).unwrap();
        std::mem::forget(dir);
        engine
    }

    #[test]
    fn independent_options_survive_roster_growth_and_batching() {
        let engine = engine();
        let mut request: Request = serde_json::from_value(json!({
            "method": "decide", "state": "hello world",
            "questions": {"tools": {"type": "multi", "independent": true,
                "instructions": "which tool?", "options": {"command": "run a command"}}}
        })).unwrap();
        let initial = handle(&engine, &request).unwrap().answers.unwrap()["tools"]
            .probabilities.as_ref().unwrap()["command"];
        let options = request.questions["tools"].options.as_mut().unwrap();
        // Cross both the encoding budget and the inference batch boundary.
        for n in 0..71 {
            options.insert(format!("another_{n:03}"), "an unrelated option".into());
        }
        let grown = handle(&engine, &request).unwrap().answers.unwrap();
        let probabilities = grown["tools"].probabilities.as_ref().unwrap();
        assert_eq!(probabilities.len(), 72);
        assert!((probabilities["command"] - initial).abs() <= 0.0001);
        request.questions["tools"].kind = QType::Choice;
        assert!(handle(&engine, &request).is_err());
    }

    #[test]
    fn host_questions_translate_to_checkpoint_criteria() {
        let q: HostQuestion = serde_json::from_value(json!({
            "type": "choice", "instructions": "which?", "options": {"b": "second", "a": ""}
        }))
        .unwrap();
        let q = q.to_question();
        assert_eq!(q.render_options("q").unwrap(), vec!["a", "b: second"]);

        let q: HostQuestion = serde_json::from_value(json!({
            "type": "multi", "instructions": "which?", "options": {"edit": "", "run": "execute"}
        }))
        .unwrap();
        let q = q.to_question();
        assert_eq!(q.kind, QType::Multi);
        assert_eq!(q.render_options("q").unwrap(), vec!["edit", "run: execute"]);

        let q: HostQuestion = serde_json::from_value(
            json!({"type": "score", "instructions": "how?", "levels": ["lo", "hi"]}),
        )
        .unwrap();
        assert_eq!(
            q.to_question().render_options("q").unwrap(),
            vec!["level 0: lo", "level 1: hi"]
        );

        let q: HostQuestion = serde_json::from_value(
            json!({"type": "noul", "instructions": "is it?", "true": "yes it is"}),
        )
        .unwrap();
        assert_eq!(
            q.to_question().render_options("q").unwrap(),
            vec!["false: no, the statement does not hold", "true: yes it is"]
        );
        let q: HostQuestion =
            serde_json::from_value(json!({"type": "noul", "instructions": "is it?"})).unwrap();
        assert!(q.to_question().criteria.is_none());
    }

    #[test]
    fn the_loop_answers_every_method_and_reports_faults_by_id() {
        let engine = engine();
        let input = concat!(
            r#"{"id": 1, "method": "hello"}"#,
            "\n",
            "\n",
            r#"{"id": 2, "method": "decide", "state": {"b": 1, "a": "x"}, "questions": {"q": {"type": "noul", "instructions": "hello"}}}"#,
            "\n",
            r#"{"id": 3, "method": "rank", "head": "code-rank", "task": "hello", "candidates": ["world", "hello world"]}"#,
            "\n",
            r#"{"id": 4, "method": "bogus"}"#,
            "\n",
            r#"{"id": 5, "method": "decide", "state": "x", "questions": {"q": {"type": "choice", "instructions": "x"}}}"#,
            "\n",
            "not json\n",
            r#"{"id": 7, "method": "rank", "head": "bogus", "task": "hello", "candidates": ["world"]}"#,
            "\n",
        );
        let mut out = Vec::new();
        serve(&engine, input.as_bytes(), &mut out).unwrap();
        let replies: Vec<Value> = String::from_utf8(out)
            .unwrap()
            .lines()
            .map(|l| serde_json::from_str(l).unwrap())
            .collect();
        assert_eq!(replies.len(), 7);
        assert_eq!(replies[0]["id"], 1);
        assert_eq!(replies[0]["engine"]["name"], "Bialy");
        assert_eq!(replies[0]["engine"]["model"], "tiny/model");
        assert_eq!(replies[0]["heads"], json!({}));
        assert_eq!(replies[1]["id"], 2);
        assert_eq!(replies[1]["answers"]["q"]["type"], "noul");
        assert!(replies[1]["answers"]["q"]["noul"].is_number());
        assert_eq!(replies[2]["scores"].as_array().unwrap().len(), 2);
        assert!(replies[3]["error"].as_str().unwrap().contains("bogus"));
        // A choice with no options is a per-request fault, not a dead engine.
        assert_eq!(replies[4]["id"], 5);
        assert!(replies[4]["error"].as_str().unwrap().contains("criteria"));
        assert_eq!(replies[5]["id"], 0);
        assert!(replies[5]["error"].as_str().unwrap().contains("unreadable"));
        assert_eq!(replies[6]["id"], 7);
        assert!(replies[6]["error"].as_str().unwrap().contains("bogus"));
    }
}
