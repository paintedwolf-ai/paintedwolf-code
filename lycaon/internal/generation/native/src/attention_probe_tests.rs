use super::*;

#[test]
fn strided_e4b_attention_matches_reference_across_dispatch_boundary() -> Result<()> {
    let q = input(&[1, 1, 8, 512], 23)?.transpose_axes(&[0, 2, 1, 3])?;
    let k = input(&[1, 2, 4128, 512], 24)?;
    let v = input(&[1, 2, 4128, 512], 25)?;
    for length in [1, 1024, 4095, 4096, 4097, 4128] {
        let keys = k.index((.., .., ..length, ..));
        let values = v.index((.., .., ..length, ..));
        let expected = reference(&q, &keys, &values)?;
        let output = attention(&q, &keys, &values)?;
        assert_eq!(output.dtype(), Dtype::Float16);
        assert_eq!(output.shape(), &[1, 8, 1, 512]);
        let actual = output.as_dtype(Dtype::Float32)?.contiguous()?;
        mlx_rs::transforms::eval([&actual, &expected])?;
        for (&got, &want) in actual.as_slice::<f32>().iter().zip(expected.as_slice::<f32>()) {
            assert!(got.is_finite() && want.is_finite());
            assert!((got - want).abs() <= 0.005, "keys={length}: {got} != {want}");
        }
    }
    Ok(())
}

#[test]
fn causal_prefill_preserves_ragged_query_offsets() -> Result<()> {
    let k = input(&[1, 2, 4128, 512], 31)?;
    let v = input(&[1, 2, 4128, 512], 32)?;
    for count in [2, 8, 9, 31, 32, 33, 128, 256] {
        let q = input(&[1, count, 8, 512], 30)?.transpose_axes(&[0, 2, 1, 3])?;
        let keys = k.index((.., .., ..4097, ..));
        let values = v.index((.., .., ..4097, ..));
        let expected = reference(&q, &keys, &values)?;
        let actual = attention(&q, &keys, &values)?.as_dtype(Dtype::Float32)?.contiguous()?;
        mlx_rs::transforms::eval([&actual, &expected])?;
        for (&got, &want) in actual.as_slice::<f32>().iter().zip(expected.as_slice::<f32>()) {
            assert!(got.is_finite() && want.is_finite());
            assert!((got - want).abs() <= 0.005, "queries={count}: {got} != {want}");
        }
    }
    Ok(())
}
