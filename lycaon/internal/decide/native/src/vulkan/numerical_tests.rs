use burn::tensor::{DType, Tensor, TensorData, module};
use candle_core::{Device as Cpu, Tensor as Reference};

fn values(count: usize, seed: u32) -> Vec<f32> {
    let mut state = seed;
    (0..count)
        .map(|_| {
            state = state.wrapping_mul(1664525).wrapping_add(1013904223);
            ((state >> 16) as f32 / 65536.0 - 0.5) * 0.25
        })
        .collect()
}

fn agrees(actual: Vec<f32>, expected: Vec<f32>, dtype: DType) {
    assert_eq!(actual.len(), expected.len());
    let tolerance = if dtype == DType::F16 { 0.003 } else { 0.0001 };
    for (index, (actual, expected)) in actual.into_iter().zip(expected).enumerate() {
        assert!(
            actual.is_finite() && (actual - expected).abs() <= tolerance * (1.0 + expected.abs()),
            "{dtype:?} at {index}: actual={actual}, expected={expected}"
        );
    }
}

#[test]
fn production_width_projection_matches_cpu() {
    let gpu = super::test_gpu();
    for columns in [768, 2304] {
        let input = values(2 * 17 * 768, 11);
        let weights = values(768 * columns, 29);
        let expected = Reference::from_vec(input.clone(), (34, 768), &Cpu::Cpu)
            .unwrap()
            .matmul(&Reference::from_vec(weights.clone(), (768, columns), &Cpu::Cpu).unwrap())
            .unwrap()
            .flatten_all()
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();
        for dtype in [DType::F32, DType::F16] {
            if !gpu.device.supports_dtype(dtype) {
                continue;
            }
            let x = Tensor::<3>::from_data(
                TensorData::new(input.clone(), [2, 17, 768]),
                (&gpu.device, DType::F32),
            ).cast(dtype);
            let w = Tensor::<3>::from_data(
                TensorData::new(weights.clone(), [1, 768, columns]),
                (&gpu.device, DType::F32),
            ).cast(dtype);
            let actual = x.matmul(w).cast(DType::F32).into_data().try_to_vec::<f32>().unwrap();
            agrees(actual, expected.clone(), dtype);
        }
    }
}

#[test]
fn masked_attention_matches_cpu() {
    let gpu = super::test_gpu();
    let shape = [2, 12, 17, 64];
    let q = values(shape.iter().product(), 41);
    let k = values(q.len(), 43);
    let v = values(q.len(), 47);
    let bias: Vec<f32> = (0..17 * 17)
        .map(|i| if i % 17 > 12 { -10000.0 } else { 0.0 })
        .collect();
    let tensor = |data: &Vec<f32>| Reference::from_vec(data.clone(), &shape, &Cpu::Cpu).unwrap();
    let keys = tensor(&k).transpose(2, 3).unwrap().contiguous().unwrap();
    let scores = (tensor(&q).matmul(&keys).unwrap() / 8.0).unwrap();
    let reference_bias = Reference::from_vec(bias.clone(), (1, 1, 17, 17), &Cpu::Cpu).unwrap();
    let scores = scores.broadcast_add(&reference_bias).unwrap();
    let expected = candle_nn::ops::softmax_last_dim(&scores)
        .unwrap()
        .matmul(&tensor(&v))
        .unwrap().flatten_all()
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();
    for dtype in [DType::F32, DType::F16] {
        if !gpu.device.supports_dtype(dtype) {
                continue;
            }
        let tensor = |data: &Vec<f32>| {
            Tensor::<4>::from_data(
                TensorData::new(data.clone(), shape),
                (&gpu.device, DType::F32),
            ).cast(dtype)
        };
        let mask = Tensor::<4>::from_data(
            TensorData::new(bias.clone(), [1, 1, 17, 17]),
            (&gpu.device, DType::F32),
        ).cast(dtype);
        let actual = module::attention(tensor(&q), tensor(&k), tensor(&v), None, Some(mask), Default::default())
            .cast(DType::F32).into_data().try_to_vec::<f32>().unwrap();
        agrees(actual, expected.clone(), dtype);
    }
}
