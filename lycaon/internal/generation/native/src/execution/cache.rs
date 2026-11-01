use super::{Backend, Executor, Result};

impl<B: Backend> Executor<B> {
    pub(super) fn resume_prefix(&mut self, tokens: &[u32], allow_complete: bool) -> Result<usize> {
        let eligible = |prefix: &[u32]| {
            !prefix.is_empty()
                && (prefix.len() < tokens.len() || (allow_complete && prefix.len() == tokens.len()))
                && tokens.starts_with(prefix)
        };
        if eligible(&self.evaluated) {
            return Ok(self.evaluated.len());
        }
        self.evaluated.clear();
        if eligible(&self.prefix) {
            if self.backend.restore_prefix()? {
                self.evaluated.clone_from(&self.prefix);
                return Ok(self.prefix.len());
            }
            self.prefix.clear();
        }
        self.backend.reset()?;
        Ok(0)
    }
}
