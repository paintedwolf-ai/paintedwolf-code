use std::collections::HashMap;
use std::path::Path;

use mlx_rs::{Array, ops};
use serde_json::Value;

use super::Result;

pub fn quantization_bits(config: &Value, name: &str) -> Result<i32> {
    let recipe = config
        .get(format!("language_model.{name}"))
        .unwrap_or(config);
    let bits = i32::try_from(recipe["bits"].as_i64().ok_or("Missing quantization bits")?)?;
    if ![4, 6, 8].contains(&bits)
        || recipe["group_size"].as_i64() != Some(64)
        || recipe["mode"].as_str() != Some("affine")
    {
        return Err("Unsupported affine quantization".into());
    }
    Ok(bits)
}

pub struct Weights {
    tensors: HashMap<String, Array>,
    quantization: Value,
}

impl Weights {
    pub fn load(path: &Path, quantization: Value) -> Result<Self> {
        let mut tensors = Array::load_safetensors(path)?;
        tensors.retain(|name, _| !name.starts_with("language_model.model.embed_tokens_per_layer."));
        Ok(Self {
            tensors,
            quantization,
        })
    }

    pub fn take(&mut self, name: &str) -> Result<Array> {
        let array = self
            .tensors
            .remove(&format!("language_model.{name}"))
            .ok_or_else(|| format!("Missing model tensor {name}"))?;
        array.eval()?;
        Ok(array)
    }

    pub fn linear(&mut self, name: &str) -> Result<Linear> {
        let weight = self.take(&format!("{name}.weight"))?;
        let quantized = self
            .tensors
            .contains_key(&format!("language_model.{name}.scales"));
        let quantization = if quantized {
            let bits = quantization_bits(&self.quantization, name)?;
            Some((
                self.take(&format!("{name}.scales"))?,
                self.take(&format!("{name}.biases"))?,
                bits,
            ))
        } else {
            None
        };
        let linear = Linear {
            weight,
            quantization,
        };
        linear.evaluate()?;
        Ok(linear)
    }
}

pub struct Linear {
    pub weight: Array,
    pub quantization: Option<(Array, Array, i32)>,
}

impl Linear {
    fn evaluate(&self) -> Result<()> {
        self.weight.eval()?;
        if let Some((scales, biases, _)) = &self.quantization {
            mlx_rs::transforms::eval([scales, biases])?;
        }
        Ok(())
    }

    pub fn forward(&self, x: &Array) -> Result<Array> {
        Ok(match &self.quantization {
            Some((scales, biases, bits)) => {
                ops::quantized_matmul(x, &self.weight, scales, biases, true, 64, *bits)?
            }
            None => x.matmul(&self.weight.t())?,
        })
    }

    pub fn embedding(&self, ids: &Array) -> Result<Array> {
        let rows = self.weight.take_axis(ids, 0)?;
        Ok(match &self.quantization {
            Some((scales, biases, bits)) => ops::dequantize(
                rows,
                scales.take_axis(ids, 0)?,
                &biases.take_axis(ids, 0)?,
                64,
                *bits,
            )?,
            None => rows,
        })
    }
}
