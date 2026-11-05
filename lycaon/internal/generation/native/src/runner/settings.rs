use crate::error::{Error, Result};
use serde::Serialize;
use serde_json::Value;
use std::{io::Read, path::Path};

#[derive(Serialize)]
pub struct SamplingDefaults {
    pub temperature: f32,
    pub top_p: f32,
    pub top_k: usize,
}

pub struct Settings {
    pub eos: Vec<u32>,
    pub sampling: SamplingDefaults,
}

impl Settings {
    pub fn load(directory: &Path) -> Result<Self> {
        let file = std::fs::File::open(directory.join("generation_config.json"))?;
        if !file.metadata()?.is_file() || file.metadata()?.len() > 1 << 20 {
            return Err(invalid());
        }
        let value: Value =
            serde_json::from_reader(file.take((1 << 20) + 1)).map_err(|_| invalid())?;
        Self::parse(&value)
    }

    fn parse(value: &Value) -> Result<Self> {
        let fields = value.as_object().ok_or_else(invalid)?;
        for key in fields.keys() {
            if !matches!(
                key.as_str(),
                "bos_token_id"
                    | "eos_token_id"
                    | "pad_token_id"
                    | "do_sample"
                    | "temperature"
                    | "top_p"
                    | "top_k"
                    | "transformers_version"
                    | "_from_model_config"
            ) {
                return Err(Error::Checkpoint(format!(
                    "unsupported generation setting: {key}"
                )));
            }
        }
        for (name, token) in [("bos_token_id", 2), ("pad_token_id", 0)] {
            if value
                .get(name)
                .is_some_and(|value| value.as_u64() != Some(token))
            {
                return Err(invalid());
            }
        }
        let sample = value
            .get("do_sample")
            .map_or(Some(false), Value::as_bool)
            .ok_or_else(invalid)?;
        let tokens = match &value["eos_token_id"] {
            Value::Array(values) => values.clone(),
            value => vec![value.clone()],
        };
        if tokens.is_empty() || tokens.len() > 16 {
            return Err(invalid());
        }
        let eos = tokens
            .iter()
            .map(|value| {
                value
                    .as_u64()
                    .filter(|n| *n < 262144)
                    .map(|n| n as u32)
                    .ok_or_else(invalid)
            })
            .collect::<Result<Vec<_>>>()?;
        let temperature = value
            .get("temperature")
            .map_or(Some(1.0), Value::as_f64)
            .ok_or_else(invalid)? as f32;
        let top_p = value
            .get("top_p")
            .map_or(Some(1.0), Value::as_f64)
            .ok_or_else(invalid)? as f32;
        let top_k = value
            .get("top_k")
            .map_or(Some(50), Value::as_u64)
            .ok_or_else(invalid)?;
        if !temperature.is_finite()
            || temperature < 0.0
            || sample && temperature == 0.0
            || !top_p.is_finite()
            || !(0.0 < top_p && top_p <= 1.0)
            || top_k > 262144
        {
            return Err(invalid());
        }
        Ok(Self {
            eos,
            sampling: SamplingDefaults {
                temperature: if sample { temperature } else { 0.0 },
                top_p,
                top_k: top_k as usize,
            },
        })
    }
}

fn invalid() -> Error {
    Error::Checkpoint("invalid generation configuration".into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn preserves_artifact_sampling_and_stop_tokens() {
        let settings = Settings::parse(
            &json!({"eos_token_id":[1,106,50],"do_sample":true,"temperature":1.0,"top_p":0.95,"top_k":64}),
        )
        .unwrap();
        assert_eq!(settings.eos, [1, 106, 50]);
        assert_eq!(settings.sampling.top_k, 64);
        assert_eq!(settings.sampling.top_p, 0.95);
        for value in [
            json!({"eos_token_id":null}),
            json!({"eos_token_id":1,"top_k":-1}),
            json!({"eos_token_id":1,"temperature":"1"}),
            json!({"eos_token_id":1,"top_p":0}),
            json!({"eos_token_id":1,"do_sample":"true"}),
            json!({"eos_token_id":1,"do_sample":true,"temperature":0}),
            json!({"eos_token_id":1,"repetition_penalty":1.2}),
            json!({"eos_token_id":1,"bos_token_id":3}),
        ] {
            assert!(Settings::parse(&value).is_err());
        }
    }

    #[test]
    fn greedy_configuration_does_not_silently_enable_sampling() {
        for value in [
            json!({"eos_token_id":1}),
            json!({"eos_token_id":1,"do_sample":false,"temperature":0.7}),
        ] {
            let settings = Settings::parse(&value).unwrap();
            assert_eq!(settings.sampling.temperature, 0.0);
            assert_eq!(settings.sampling.top_k, 50);
        }
    }
}
