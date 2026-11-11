use std::collections::HashMap;
use std::path::Path;

use mlx_rs::{ops, ops::indexing::IndexOp, Array};
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

    pub fn forward_prefix(&self, x: &Array, rows: i32) -> Result<Array> {
        if rows <= 0 || rows > self.weight.shape()[0] {
            return Err("Linear output prefix exceeds its bounds".into());
        }
        let weight = self.weight.index((..rows, ..));
        Ok(match &self.quantization {
            Some((scales, biases, bits)) => ops::quantized_matmul(
                x,
                weight,
                scales.index((..rows, ..)),
                &biases.index((..rows, ..)),
                true,
                64,
                *bits,
            )?,
            None => x.matmul(weight.t())?,
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn output_prefix_preserves_rows_and_rejects_invalid_ranges() {
        let linear = Linear {
            weight: Array::from_slice(&[1_f32, 2., 3., 4., 5., 6.], &[3, 2]),
            quantization: None,
        };
        let input = Array::from_slice(&[2_f32, 3.], &[1, 2]);
        let prefix = linear.forward_prefix(&input, 2).unwrap();
        prefix.eval().unwrap();
        assert_eq!(prefix.as_slice::<f32>(), &[8., 18.]);
        assert!(linear.forward_prefix(&input, 0).is_err());
        assert!(linear.forward_prefix(&input, 4).is_err());
    }
}
