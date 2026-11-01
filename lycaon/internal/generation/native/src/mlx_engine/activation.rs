use mlx_rs::{Array, error::Exception, nn, transforms::compile::compile};
use std::cell::RefCell;

type Kernel = Box<dyn FnMut(&Array) -> std::result::Result<Array, Exception>>;

pub struct Activation(RefCell<Kernel>);

impl Activation {
    pub fn new() -> Self {
        let operation = |input: &Array| nn::gelu_approximate(input)?.as_dtype(input.dtype());
        Self(RefCell::new(Box::new(compile(operation, true))))
    }

    pub fn apply(&self, input: &Array) -> super::Result<Array> {
        Ok(self.0.borrow_mut()(input)?)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mlx_rs::Dtype;

    #[test]
    fn retained_activation_preserves_half_precision_and_reference_values() {
        let activation = Activation::new();
        for values in [vec![-3.0_f32, -1.0, 0.0, 1.0, 3.0], vec![0.5]] {
            let input = Array::from_slice(&values, &[values.len() as i32])
                .as_dtype(Dtype::Float16)
                .unwrap();
            let output = activation.apply(&input).unwrap();
            assert_eq!(output.dtype(), Dtype::Float16);
            let output = output.as_dtype(Dtype::Float32).unwrap();
            output.eval().unwrap();
            for (&got, &x) in output.as_slice::<f32>().iter().zip(&values) {
                let want = 0.5
                    * x
                    * (1.0
                        + (std::f32::consts::FRAC_2_PI.sqrt() * (x + 0.044715 * x.powi(3))).tanh());
                assert!((got - want).abs() < 0.002, "{got} != {want}");
            }
        }
    }
}
