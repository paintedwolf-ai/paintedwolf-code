//! Locating the five files a checkpoint is made of on disk.

use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

/// Everything the loader needs, resolved to concrete paths.
#[derive(Clone, Debug)]
pub struct Checkpoint {
    /// `rl_agent_config.json`: head shape, sequence budget and calibration.
    pub agent_config: PathBuf,
    /// `model.safetensors`: backbone and head in one file.
    pub weights: PathBuf,
    /// `encoder/config.json`: the ModernBERT backbone's shape.
    pub encoder_config: PathBuf,
    /// `tokenizer/tokenizer.json`.
    pub tokenizer: PathBuf,
    /// `tokenizer/tokenizer_config.json`, when the checkpoint ships one.
    pub tokenizer_config: Option<PathBuf>,
    /// How to name this checkpoint in errors.
    pub label: String,
}

const AGENT_CONFIG: &str = "rl_agent_config.json";
const WEIGHTS: &str = "model.safetensors";
const ENCODER_CONFIG: &str = "encoder/config.json";
const TOKENIZER: &str = "tokenizer/tokenizer.json";
/// Optional: a `tokenizer.json` that carries its own special tokens does not need one.
const TOKENIZER_CONFIG: &str = "tokenizer/tokenizer_config.json";

/// The files a checkpoint is made of, relative to its root.
const FILES: [&str; 5] = [AGENT_CONFIG, WEIGHTS, ENCODER_CONFIG, TOKENIZER, TOKENIZER_CONFIG];

impl Checkpoint {
    /// Resolve every file through `fetch`, which answers `Ok(None)` for a file that does not
    /// exist and `Err` for one it could not check; `missing` names the error for an absent
    /// required file.
    fn assemble(
        label: String,
        mut fetch: impl FnMut(&str) -> Result<Option<PathBuf>>,
        missing: impl Fn(&str) -> Error,
    ) -> Result<Self> {
        let mut required = |name: &str| fetch(name)?.ok_or_else(|| missing(name));
        Ok(Self {
            agent_config: required(AGENT_CONFIG)?,
            weights: required(WEIGHTS)?,
            encoder_config: required(ENCODER_CONFIG)?,
            tokenizer: required(TOKENIZER)?,
            // Only an absent optional file is skipped: a failure to fetch one is still a
            // failure, and would otherwise surface later as the wrong special-token names.
            tokenizer_config: fetch(TOKENIZER_CONFIG)?,
            label,
        })
    }

    /// Resolve a checkpoint laid out as a directory.
    pub fn from_dir(dir: impl AsRef<Path>) -> Result<Self> {
        let dir = dir.as_ref();
        if !dir.is_dir() {
            return Err(Error::Checkpoint(format!(
                "{}: not a directory. Point at a checkpoint folder.",
                dir.display()
            )));
        }
        Self::assemble(
            dir.display().to_string(),
            |name| {
                let p = dir.join(name);
                Ok(p.exists().then_some(p))
            },
            |name| {
                Error::Checkpoint(format!(
                    "{}: missing {name}. A decision checkpoint requires {}.",
                    dir.display(),
                    FILES.join(", ")
                ))
            },
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::scratch_dir;

    fn touch(dir: &Path, name: &str) {
        let p = dir.join(name);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, b"").unwrap();
    }

    #[test]
    fn a_missing_directory_is_rejected() {
        let err = Checkpoint::from_dir("/definitely/not/here").unwrap_err();
        assert!(matches!(err, Error::Checkpoint(ref m) if m.contains("not a directory")), "{err}");
    }

    #[test]
    fn the_first_missing_file_is_named() {
        let dir = scratch_dir("cp-missing");
        touch(&dir, AGENT_CONFIG);
        let err = Checkpoint::from_dir(&dir).unwrap_err();
        assert!(matches!(err, Error::Checkpoint(ref m) if m.contains(WEIGHTS)), "{err}");
    }

    #[test]
    fn the_tokenizer_config_is_optional() {
        let dir = scratch_dir("cp-complete");
        for name in &FILES[..4] {
            touch(&dir, name);
        }
        let cp = Checkpoint::from_dir(&dir).unwrap();
        assert_eq!(cp.weights, dir.join(WEIGHTS));
        assert_eq!(cp.label, dir.display().to_string());
        assert!(cp.tokenizer_config.is_none());

        touch(&dir, TOKENIZER_CONFIG);
        let cp = Checkpoint::from_dir(&dir).unwrap();
        assert_eq!(cp.tokenizer_config, Some(dir.join(TOKENIZER_CONFIG)));
    }
}
