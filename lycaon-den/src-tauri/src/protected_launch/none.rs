//! Protected launch is a Unix mechanism. Non-Unix targets have no launch
//! frame, broker, or selected generation: every desktop is unprotected and
//! engine layout discovery keeps the packaged/bundled behavior.

use std::path::PathBuf;

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Generation {
    pub(crate) logical_root: PathBuf,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum GenerationError {
    FrameUnavailable,
    InvalidFrame,
    IdentityMismatch,
    BrokerMismatch,
    LayoutUnavailable,
}

impl std::fmt::Display for GenerationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let code = match self {
            Self::FrameUnavailable => "frame_unavailable",
            Self::InvalidFrame => "invalid_frame",
            Self::IdentityMismatch => "identity_mismatch",
            Self::BrokerMismatch => "broker_mismatch",
            Self::LayoutUnavailable => "layout_unavailable",
        };
        write!(f, "Protected launch generation refused ({code})")
    }
}
impl std::error::Error for GenerationError {}

/// No launch selection exists off Unix, so no desktop is ever protected.
pub(crate) fn generation() -> Result<Option<Generation>, GenerationError> {
    Ok(None)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn non_unix_targets_are_never_protected() {
        assert_eq!(generation(), Ok(None));
    }
}
