use super::*;
use mlx_rs::{nn, ops::indexing::*, transforms::compile::compile};

fn reference(gate: &Array, up: &Array) -> Array {
    let operation = |input: &Array| nn::gelu_approximate(input)?.as_dtype(input.dtype());
    compile(operation, true)(gate).unwrap().multiply(up).unwrap()
}

fn assert_product(activation: &Activation, gate: &Array, up: &Array) {
    let actual = activation.product(gate, up).unwrap();
    assert_eq!(actual.shape(), gate.shape());
    assert_eq!(actual.dtype(), Dtype::Float16);
    assert_eq!(actual.nbytes(), gate.nbytes());
    let expected = reference(gate, up).as_dtype(Dtype::Float32).unwrap();
    let actual = actual.as_dtype(Dtype::Float32).unwrap();
    mlx_rs::transforms::eval([&actual, &expected]).unwrap();
    for (index, (&got, &want)) in actual
        .as_slice::<f32>()
        .iter()
        .zip(expected.as_slice::<f32>())
        .enumerate()
    {
        if want.is_finite() {
            assert!((got - want).abs() <= 0.00002, "element {index}: {got} != {want}");
        } else {
            assert_eq!(got, want, "element {index}");
        }
    }
}

#[test]
fn product_preserves_every_finite_half_input() {
    let activation = Activation::new().unwrap();
    let values: Vec<_> = (0..=u16::MAX)
        .map(half::f16::from_bits)
        .filter(|value| value.is_finite())
        .collect();
    assert_eq!(values.len(), 248 * 256);
    let gate = Array::from_slice(&values, &[1, 248, 256]);
    for multiplier in [0.5, -1.0, 2.0] {
        let up = Array::from_slice(
            &vec![half::f16::from_f32(multiplier); values.len()],
            gate.shape(),
        );
        assert_product(&activation, &gate, &up);
    }
}

#[test]
fn product_handles_prefill_decode_and_strided_ple_rows() {
    let activation = Activation::new().unwrap();
    for rows in [1, 3, 128, 256] {
        for width in [256, 10240] {
            let values: Vec<_> = (0..rows * width)
                .map(|i| half::f16::from_f32(((i * 37 % 1201) - 600) as f32 / 100.0))
                .collect();
            let gate = Array::from_slice(&values, &[1, rows, width]);
            let up = Array::from_slice(&values, &[1, width, rows])
                .transpose_axes(&[0, 2, 1])
                .unwrap();
            assert_product(&activation, &gate, &up);
        }
        let values: Vec<_> = (0..rows * 42 * 256)
            .map(|i| half::f16::from_f32((i % 101) as f32 / 100.0))
            .collect();
        let storage = Array::from_slice(&values, &[1, rows, 42, 256]);
        let gate = storage.index((.., .., 5, ..));
        let up = storage.index((.., .., 17, ..));
        assert_product(&activation, &gate, &up);
    }
}

#[test]
fn product_rejects_unsupported_shapes_and_precision() {
    let activation = Activation::new().unwrap();
    for shape in [
        vec![1, 256],
        vec![2, 1, 256],
        vec![1, 0, 256],
        vec![1, 257, 256],
        vec![1, 1, 255],
    ] {
        let input = Array::zeros::<half::f16>(&shape).unwrap();
        assert!(activation.product(&input, &input).is_err());
    }
    let half = Array::zeros::<half::f16>(&[1, 1, 256]).unwrap();
    let float = Array::zeros::<f32>(&[1, 1, 256]).unwrap();
    let other = Array::zeros::<half::f16>(&[1, 3, 256]).unwrap();
    assert!(activation.product(&half, &float).is_err());
    assert!(activation.product(&float, &half).is_err());
    assert!(activation.product(&half, &other).is_err());
}

#[test]
fn shader_compilation_errors_keep_the_underlying_diagnostic() {
    let broken = Activation::create(
        c"e4b_invalid_activation_fixture",
        c"out[thread_position_in_grid.x] = unknown_activation_fixture_value;",
    )
    .unwrap();
    let input = Array::ones::<half::f16>(&[1, 1, 256]).unwrap();
    let output = broken.product(&input, &input).unwrap();
    let error = output.eval().unwrap_err();
    assert!(
        error.what().contains("unknown_activation_fixture_value"),
        "{error}"
    );
}

#[test]
fn dispatch_rejects_unallocated_handles_before_use() {
    let empty = ProductDispatch {
        config: sys::mlx_fast_metal_kernel_config {
            ctx: std::ptr::null_mut(),
        },
        inputs: sys::mlx_vector_array {
            ctx: std::ptr::null_mut(),
        },
        outputs: sys::mlx_vector_array {
            ctx: std::ptr::null_mut(),
        },
    };
    assert!(empty.validate().is_err());
}
