use super::{Cache, KV};
use crate::mlx_engine::Result;
use mlx_rs::{Array, ops::indexing::*};
use std::{
    cell::RefCell,
    fs::{File, OpenOptions},
    io::{Read, Seek, SeekFrom, Write},
    os::{fd::AsRawFd, unix::fs::OpenOptionsExt},
    rc::Rc,
    sync::atomic::{AtomicU64, Ordering},
};

pub struct Checkpoint {
    file: File,
    sliding: Cache,
    globals: [bool; 4],
}

impl Checkpoint {
    pub fn save(cache: &Cache) -> Result<Self> {
        if !(1..=cache.capacity).contains(&cache.offset) {
            return Err("invalid prefix checkpoint offset".into());
        }
        let mut file = private_file()?;
        let mut globals = [false; 4];
        for (index, bank) in cache.globals.iter().enumerate() {
            let Some(bank) = bank else { continue };
            let kv = bank.borrow();
            for array in [&kv.keys, &kv.values] {
                for head in 0..2 {
                    let row = array.index((0, head, ..cache.offset, ..)).contiguous()?;
                    let values = row.try_as_slice::<half::f16>()?;
                    // F16 bytes retain their exact representation.
                    let bytes = unsafe {
                        std::slice::from_raw_parts(
                            values.as_ptr().cast::<u8>(),
                            std::mem::size_of_val(values),
                        )
                    };
                    file.write_all(bytes)?;
                }
            }
            globals[index] = true;
        }
        let mut sliding = cache.clone();
        sliding.globals.fill(None);
        Ok(Self {
            file,
            sliding,
            globals,
        })
    }

    pub fn restore(&mut self) -> Result<Cache> {
        self.file.seek(SeekFrom::Start(0))?;
        let mut cache = self.sliding.clone();
        let mut values = vec![half::f16::ZERO; cache.offset as usize * 512];
        for (index, present) in self.globals.iter().enumerate() {
            if !present {
                continue;
            }
            let mut kv = KV {
                keys: Array::zeros::<half::f16>(&[1, 2, cache.capacity, 512])?,
                values: Array::zeros::<half::f16>(&[1, 2, cache.capacity, 512])?,
            };
            for array in [&mut kv.keys, &mut kv.values] {
                for head in 0..2 {
                    // Every bit pattern is a valid F16 storage value.
                    let bytes = unsafe {
                        std::slice::from_raw_parts_mut(
                            values.as_mut_ptr().cast::<u8>(),
                            std::mem::size_of_val(values.as_slice()),
                        )
                    };
                    self.file.read_exact(bytes)?;
                    array.index_mut(
                        (0, head, ..cache.offset, ..),
                        Array::from_slice(&values, &[cache.offset, 512]),
                    );
                    array.eval()?;
                }
            }
            cache.globals[index] = Some(Rc::new(RefCell::new(kv)));
        }
        Ok(cache)
    }

    pub fn resident_bytes(&self) -> usize {
        self.sliding.bytes()
    }
}

fn private_file() -> Result<File> {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    for _ in 0..32 {
        let name = format!(
            ".nativegen-kv-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        );
        let path = std::env::temp_dir().join(name);
        let file = match OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
        {
            Ok(file) => file,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(error.into()),
        };
        std::fs::remove_file(path)?;
        // The checkpoint must not displace model pages in the file cache.
        if unsafe { libc::fcntl(file.as_raw_fd(), libc::F_NOCACHE, 1) } == -1 {
            return Err(std::io::Error::last_os_error().into());
        }
        return Ok(file);
    }
    Err("unable to allocate a private prefix checkpoint".into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::MetadataExt;

    fn source() -> Cache {
        let mut cache = Cache::new(1024).unwrap();
        for (layer, dimension) in [(0, 256), (5, 512), (23, 512)] {
            let data: Vec<_> = (0..2 * 3 * dimension)
                .map(|i| half::f16::from_f32(((i + layer as i32) % 127) as f32))
                .collect();
            let keys = Array::from_slice(&data, &[1, 2, 3, dimension]);
            let values = keys
                .add(Array::from_f32(128.0))
                .unwrap()
                .as_dtype(mlx_rs::Dtype::Float16)
                .unwrap();
            cache.update(layer, keys, values).unwrap();
        }
        cache.offset = 3;
        cache.evaluate().unwrap();
        cache
    }

    #[test]
    fn restores_exact_global_rows_and_sliding_state_without_retaining_global_arrays() {
        let source = source();
        let mut checkpoint = Checkpoint::save(&source).unwrap();
        assert_eq!(checkpoint.file.metadata().unwrap().nlink(), 0);
        assert_eq!(
            checkpoint.file.metadata().unwrap().len(),
            2 * 2 * 2 * 3 * 512 * 2
        );
        assert_eq!(checkpoint.resident_bytes(), 2 * 2 * 3 * 256 * 2);
        let restored = checkpoint.restore().unwrap();
        restored.evaluate().unwrap();
        assert_eq!(restored.offset, source.offset);
        assert_eq!(restored.capacity, source.capacity);
        assert_eq!(restored.bytes(), source.bytes());
        assert_eq!(
            restored.layers[0]
                .as_ref()
                .unwrap()
                .keys
                .try_as_slice::<half::f16>()
                .unwrap(),
            source.layers[0]
                .as_ref()
                .unwrap()
                .keys
                .try_as_slice::<half::f16>()
                .unwrap()
        );
        for index in [0, 3] {
            let bank = restored.globals[index].as_ref().unwrap().borrow();
            let original = source.globals[index].as_ref().unwrap().borrow();
            for (array, expected) in [
                (&bank.keys, &original.keys),
                (&bank.values, &original.values),
            ] {
                let saved = array.index((.., .., ..3, ..)).contiguous().unwrap();
                let expected = expected.index((.., .., ..3, ..)).contiguous().unwrap();
                assert_eq!(
                    saved.try_as_slice::<half::f16>().unwrap(),
                    expected.try_as_slice::<half::f16>().unwrap()
                );
                let empty = array.index((.., .., 3..4, ..)).contiguous().unwrap();
                assert!(
                    empty
                        .try_as_slice::<half::f16>()
                        .unwrap()
                        .iter()
                        .all(|v| *v == half::f16::ZERO)
                );
            }
        }
    }

    #[test]
    fn truncated_checkpoint_is_a_restore_error() {
        let mut checkpoint = Checkpoint::save(&source()).unwrap();
        checkpoint.file.set_len(0).unwrap();
        assert!(checkpoint.restore().is_err());
        assert!(Checkpoint::save(&Cache::new(1024).unwrap()).is_err());
    }
}
