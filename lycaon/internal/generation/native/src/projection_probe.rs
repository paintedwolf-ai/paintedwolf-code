use mlx_rs::{Array, Dtype, memory, ops, random, transforms::eval};
use nativegen::mlx_engine::Result;
use serde_json::{Value, json};
use std::{collections::HashMap, path::Path, time::Instant};

struct Weight {
    packed: Array,
    scales: Array,
    biases: Array,
    bits: i32,
}

impl Weight {
    fn load(tensors: &mut HashMap<String, Array>, config: &Value, name: &str) -> Result<Self> {
        let prefix = format!("language_model.model.layers.0.{name}");
        let recipe = config["quantization"].get(&prefix).unwrap_or(&config["quantization"]);
        let mut take = |suffix| tensors.remove(&format!("{prefix}.{suffix}")).ok_or("missing projection weight");
        let weight = Self { packed: take("weight")?, scales: take("scales")?, biases: take("biases")?,
            bits: recipe["bits"].as_i64().ok_or("missing projection precision")? as i32 };
        eval([&weight.packed, &weight.scales, &weight.biases])?;
        Ok(weight)
    }

    fn quantized(&self, x: &Array) -> Result<Array> {
        Ok(ops::quantized_matmul(x, &self.packed, &self.scales, &self.biases, true, 64, self.bits)?)
    }

    fn dense(&self) -> Result<Array> {
        Ok(ops::dequantize(&self.packed, &self.scales, &self.biases, 64, self.bits)?)
    }

    fn joined(&self, other: &Self) -> Result<Self> {
        if self.bits != other.bits { return Err("joint projection requires equal precision".into()); }
        let weight = Self {
            packed: ops::concatenate(&[&self.packed, &other.packed], 0)?,
            scales: ops::concatenate(&[&self.scales, &other.scales], 0)?,
            biases: ops::concatenate(&[&self.biases, &other.biases], 0)?, bits: self.bits,
        };
        eval([&weight.packed, &weight.scales, &weight.biases])?;
        Ok(weight)
    }
}

fn measure(name: &str, count: i32, mode: &str, operation: impl Fn() -> Result<Array>) -> Result<()> {
    for trial in 0..8 {
        memory::reset_peak_memory()?;
        let start = Instant::now();
        let output = operation()?;
        output.eval()?;
        let seconds = start.elapsed().as_secs_f64();
        println!("{}", json!({"projection":name,"queries":count,"mode":mode,"trial":trial,
            "warmup":trial==0,"seconds":seconds,"peak_bytes":memory::peak_memory()?,
            "output_dtype":format!("{:?}", output.dtype())}));
    }
    Ok(())
}

pub fn run(directory: &Path) -> Result<()> {
    memory::set_cache_limit(128 << 20)?;
    let config: Value = serde_json::from_slice(&std::fs::read(directory.join("config.json"))?)?;
    let mut tensors = Array::load_safetensors(directory.join("model.safetensors"))?;
    let names = ["self_attn.q_proj", "mlp.gate_proj", "mlp.up_proj", "mlp.down_proj"];
    let weights = names.iter().map(|name| Weight::load(&mut tensors, &config, name)).collect::<Result<Vec<_>>>()?;
    drop(tensors);
    let joint = weights[1].joined(&weights[2])?;
    for (name, weight) in names.iter().zip(&weights) {
        for count in [1, 32, 128, 256] {
            let width = weight.packed.shape()[1] * 32 / weight.bits;
            let x = random::normal::<f32>(&[count, width], None, Some(0.25), &random::key(42)?)?.as_dtype(Dtype::Float16)?;
            x.eval()?;
            measure(name, count, "quantized", || weight.quantized(&x))?;
            measure(name, count, "dequantize_then_matmul", || Ok(x.matmul(&weight.dense()?.t())?))?;
            let dense = weight.dense()?;
            dense.eval()?;
            measure(name, count, "retained_dense", || Ok(x.matmul(&dense.t())?))?;
            if *name == "mlp.gate_proj" {
                measure("mlp.gate_up", count, "separate", || Ok(ops::concatenate(&[&weights[1].quantized(&x)?, &weights[2].quantized(&x)?], -1)?))?;
                measure("mlp.gate_up", count, "joint", || joint.quantized(&x))?;
            }
        }
        memory::clear_cache()?;
    }
    Ok(())
}
