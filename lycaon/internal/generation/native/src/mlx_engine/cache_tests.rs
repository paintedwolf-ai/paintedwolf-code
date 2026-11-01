use super::*;

#[test]
fn retained_prefix_is_unchanged_by_active_cache_extension() {
    let mut active = Cache::new(1024).unwrap();
    let keys = Array::ones::<f32>(&[1, 2, 3, 512])
        .unwrap()
        .as_dtype(Dtype::Float16)
        .unwrap();
    active.update(5, keys.clone(), keys).unwrap();
    active.offset = 3;
    active.evaluate().unwrap();
    let prefix = active.clone();
    let next = Array::ones::<f32>(&[1, 2, 2, 512])
        .unwrap()
        .multiply(Array::from_f32(2.0))
        .unwrap()
        .as_dtype(Dtype::Float16)
        .unwrap();
    active.update(5, next.clone(), next).unwrap();
    active.offset = 5;
    active.evaluate().unwrap();
    let old = prefix.globals[0]
        .as_ref()
        .unwrap()
        .borrow()
        .keys
        .index((.., .., ..3, ..))
        .as_dtype(Dtype::Float32)
        .unwrap();
    let new = active.globals[0]
        .as_ref()
        .unwrap()
        .borrow()
        .keys
        .index((.., .., 3..5, ..))
        .as_dtype(Dtype::Float32)
        .unwrap();
    mlx_rs::transforms::eval([&old, &new]).unwrap();
    assert!(old.as_slice::<f32>().iter().all(|v| *v == 1.0));
    assert_eq!(active.resident_bytes_with(&prefix), active.bytes());
    assert!(new.as_slice::<f32>().iter().all(|v| *v == 2.0));
    assert_eq!(prefix.offset, 3);
}

#[test]
fn rejects_precision_drift_and_malformed_cache_shapes() {
    let mut cache = Cache::new(1024).unwrap();
    for shape in [&[1, 2, 1, 256][..], &[1][..]] {
        let keys = Array::zeros::<f32>(shape).unwrap();
        assert!(cache.update(0, keys.clone(), keys).is_err());
    }
    assert!(cache.get(24, 1).is_err());
    assert_eq!(cache.bytes(), 0);
}

#[test]
fn unrelated_requests_do_not_overwrite_a_retained_global_bank() {
    let mut active = Cache::new(1024).unwrap();
    let first = Array::ones::<half::f16>(&[1, 2, 3, 512]).unwrap();
    active.update(5, first.clone(), first).unwrap();
    active.offset = 3;
    active.evaluate().unwrap();
    let prefix = active.clone();
    active.clear();
    let other = Array::zeros::<half::f16>(&[1, 2, 3, 512]).unwrap();
    active.update(5, other.clone(), other).unwrap();
    active.offset = 3;
    active.evaluate().unwrap();
    assert_eq!(
        active.resident_bytes_with(&prefix),
        active.bytes() + prefix.bytes()
    );
    let saved = prefix.globals[0]
        .as_ref()
        .unwrap()
        .borrow()
        .keys
        .index((.., .., ..3, ..))
        .as_dtype(Dtype::Float32)
        .unwrap();
    assert!(saved.as_slice::<f32>().iter().all(|v| *v == 1.0));
}

#[test]
fn global_capacity_is_reserved_once_and_bounds_updates() {
    let mut cache = Cache::new(1024).unwrap();
    for count in [1, 256] {
        let keys = Array::ones::<f32>(&[1, 2, count, 512])
            .unwrap()
            .as_dtype(Dtype::Float16)
            .unwrap();
        let kv = cache.update(5, keys.clone(), keys).unwrap();
        assert_eq!(kv.keys.shape()[2], cache.offset + count);
        cache.offset += count;
        cache.evaluate().unwrap();
        assert_eq!(cache.bytes(), 2 * 2 * 1024 * 512 * 2);
    }
    assert_eq!(cache.bytes(), 2 * 2 * 1024 * 512 * 2);
    cache.offset = 1024;
    let keys = Array::zeros::<f32>(&[1, 2, 1, 512]).unwrap();
    assert!(cache.update(5, keys.clone(), keys).is_err());
}

#[test]
fn clear_preserves_the_admitted_capacity() {
    for capacity in [0, 1, 65537, usize::MAX] {
        assert!(Cache::new(capacity).is_err());
    }
    let mut cache = Cache::new(2).unwrap();
    let keys = Array::ones::<half::f16>(&[1, 2, 2, 512]).unwrap();
    cache.update(5, keys.clone(), keys).unwrap();
    cache.offset = 2;
    cache.evaluate().unwrap();
    cache.clear();
    assert_eq!(cache.offset, 0);
    assert_eq!(cache.bytes(), 0);
    let keys = Array::ones::<half::f16>(&[1, 2, 3, 512]).unwrap();
    assert!(cache.update(5, keys.clone(), keys).is_err());
    let keys = Array::ones::<half::f16>(&[1, 2, 2, 512]).unwrap();
    cache.update(5, keys.clone(), keys).unwrap();
    cache.evaluate().unwrap();
    assert_eq!(cache.bytes(), 2 * 2 * 2 * 512 * 2);
}

#[test]
fn sliding_state_retains_only_the_window_and_current_chunk() {
    let mut cache = Cache::new(1024).unwrap();
    for _ in 0..4 {
        let keys = Array::ones::<f32>(&[1, 2, 256, 256])
            .unwrap()
            .as_dtype(Dtype::Float16)
            .unwrap();
        let kv = cache.update(0, keys.clone(), keys).unwrap();
        assert_eq!(kv.keys.shape()[2], cache.offset.min(511) + 256);
        cache.offset += 256;
        cache.evaluate().unwrap();
    }
    assert_eq!(cache.bytes(), 2 * 2 * 767 * 256 * 2);
}
