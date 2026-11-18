use super::{Activation, Linear, Result, Weights, norm, scale};
use mlx_rs::{Array, ops, ops::indexing::*};

pub(super) struct Experts {
    router: Linear,
    scale: Array,
    expert_scale: Array,
    gate: Linear,
    up: Linear,
    down: Linear,
    input_norm: Array,
    output_norm: Array,
    dense_norm: Array,
}

impl Experts {
    pub(super) fn load(w: &mut Weights, layer: &str) -> Result<Self> {
        let experts = format!("{layer}.experts.switch_glu");
        Ok(Self {
            router: w.linear(&format!("{layer}.router.proj"))?,
            scale: w.take(&format!("{layer}.router.scale"))?,
            expert_scale: w.take(&format!("{layer}.router.per_expert_scale"))?,
            gate: w.linear(&format!("{experts}.gate_proj"))?,
            up: w.linear(&format!("{experts}.up_proj"))?,
            down: w.linear(&format!("{experts}.down_proj"))?,
            input_norm: w.take(&format!("{layer}.pre_feedforward_layernorm_2.weight"))?,
            output_norm: w.take(&format!("{layer}.post_feedforward_layernorm_2.weight"))?,
            dense_norm: w.take(&format!("{layer}.post_feedforward_layernorm_1.weight"))?,
        })
    }

    pub(super) fn forward(
        &self,
        x: &Array,
        dense: &Array,
        activation: &Activation,
    ) -> Result<Array> {
        let tokens = x.shape()[1];
        let hidden = x.shape()[2];
        let router_input = norm(x, &scale(&self.scale, 1.0 / (hidden as f32).sqrt())?)?;
        let scores = self
            .router
            .forward(&router_input)?
            .reshape(&[tokens, 128])?;
        let indices = ops::argpartition_axis(&scores, -8, -1)?
            .index((.., -8..))
            .contiguous()?;
        let probabilities = ops::softmax_axis(scores.take_along_axis(&indices, -1)?, -1, false)?
            .multiply(self.expert_scale.take_axis(&indices, 0)?)?;
        let input = norm(x, &self.input_norm)?.reshape(&[tokens, 1, 1, hidden])?;
        let gate = self
            .gate
            .gather(&input, &indices)?
            .reshape(&[1, tokens * 8, 704])?;
        let up = self
            .up
            .gather(&input, &indices)?
            .reshape(&[1, tokens * 8, 704])?;
        let product = activation
            .product(&gate, &up)?
            .reshape(&[tokens, 8, 1, 704])?;
        let routed = self
            .down
            .gather(&product, &indices)?
            .reshape(&[tokens, 8, hidden])?
            .multiply(probabilities.reshape(&[tokens, 8, 1])?)?
            .sum_axis(1, false)?
            .reshape(&[1, tokens, hidden])?;
        Ok(norm(dense, &self.dense_norm)?.add(norm(&routed, &self.output_norm)?)?)
    }
}

impl Linear {
    fn gather(&self, x: &Array, indices: &Array) -> Result<Array> {
        Ok(match &self.quantization {
            Some((scales, biases, bits)) => ops::gather_qmm(
                x,
                &self.weight,
                scales,
                biases,
                None,
                indices,
                true,
                64,
                *bits,
                false,
            )?,
            None => ops::gather_mm(
                x,
                self.weight.transpose_axes(&[0, 2, 1])?,
                None,
                indices,
                false,
            )?,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn selected_experts_multiply_each_token_by_its_own_weights() {
        let a = ops::broadcast_to(
            Array::from_slice(&[1_f32, 3.], &[2, 1, 1, 1]),
            &[2, 1, 1, 64],
        )
        .unwrap();
        let indices = Array::from_slice(&[0_i32, 2, 1, 2], &[2, 2]);
        let weights =
            ops::broadcast_to(Array::from_slice(&[1_f32, 2., 3.], &[3, 1, 1]), &[3, 1, 64])
                .unwrap();
        let (packed, scales, biases) = ops::quantize(&weights, 64, 4).unwrap();
        for linear in [
            Linear {
                weight: weights,
                quantization: None,
            },
            Linear {
                weight: packed,
                quantization: Some((scales, biases, 4)),
            },
        ] {
            let actual = linear.gather(&a, &indices).unwrap().contiguous().unwrap();
            assert_eq!(actual.shape(), &[2, 2, 1, 1]);
            assert_eq!(
                actual.try_as_slice::<f32>().unwrap(),
                &[64., 192., 384., 576.]
            );
        }
    }
}
