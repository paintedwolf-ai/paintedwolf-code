use std::collections::HashMap;

const MAX_DELIVERED: usize = 256;

#[derive(Default)]
pub(super) struct Delivered {
    entries: HashMap<u32, Entry>,
}

struct Entry {
    session_id: Option<String>,
    activation_token: Option<String>,
}

impl Delivered {
    pub(super) fn replacement(&self, session: Option<&str>) -> u32 {
        session
            .and_then(|session| {
                self.entries.iter().find_map(|(id, entry)| {
                    (entry.session_id.as_deref() == Some(session)).then_some(*id)
                })
            })
            .unwrap_or(0)
    }

    pub(super) fn admit(&self, replacement: u32) -> Result<(), String> {
        if replacement == 0 && self.entries.len() >= MAX_DELIVERED {
            return Err("The desktop notification limit has been reached".into());
        }
        Ok(())
    }

    pub(super) fn record(
        &mut self,
        id: u32,
        replacement: u32,
        session: Option<String>,
    ) -> Result<(), String> {
        if id == 0 || (replacement != 0 && id != replacement) {
            return Err("The desktop notification service returned an invalid identifier".into());
        }
        self.entries.insert(
            id,
            Entry {
                session_id: session,
                activation_token: None,
            },
        );
        Ok(())
    }

    pub(super) fn cancel(&mut self, sessions: &[String]) -> Vec<u32> {
        let ids: Vec<_> = self
            .entries
            .iter()
            .filter_map(|(id, entry)| {
                entry
                    .session_id
                    .as_ref()
                    .filter(|session| sessions.contains(session))
                    .map(|_| *id)
            })
            .collect();
        for id in &ids {
            self.entries.remove(id);
        }
        ids
    }

    pub(super) fn closed(&mut self, id: u32) {
        self.entries.remove(&id);
    }

    pub(super) fn token(&mut self, id: u32, token: String) {
        if token.len() <= 4096 && !token.contains(['\0', '\r', '\n']) {
            if let Some(entry) = self.entries.get_mut(&id) {
                entry.activation_token = Some(token);
            }
        }
    }

    pub(super) fn activate(
        &mut self,
        id: u32,
        action: &str,
    ) -> Option<(Option<String>, Option<String>)> {
        if action != "default" {
            return None;
        }
        self.entries
            .remove(&id)
            .map(|entry| (entry.session_id, entry.activation_token))
    }

    pub(super) fn clear(&mut self) {
        self.entries.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn replacement_and_activation_belong_to_the_delivered_chat() {
        let mut delivered = Delivered::default();
        delivered.record(7, 0, Some("chat-a".into())).unwrap();
        delivered.record(8, 0, Some("chat-b".into())).unwrap();
        assert_eq!(delivered.replacement(Some("chat-a")), 7);
        assert_eq!(delivered.replacement(None), 0);
        delivered.token(7, "activation-a".into());
        assert!(delivered.activate(7, "foreign").is_none());
        assert!(delivered.activate(9, "default").is_none());
        assert_eq!(
            delivered.activate(7, "default"),
            Some((Some("chat-a".into()), Some("activation-a".into())))
        );
        assert!(delivered.activate(7, "default").is_none());
        assert_eq!(delivered.replacement(Some("chat-b")), 8);
    }

    #[test]
    fn canceled_closed_and_disconnected_notifications_cannot_activate() {
        let mut delivered = Delivered::default();
        for id in 1..=3 {
            delivered.record(id, 0, Some(format!("chat-{id}"))).unwrap();
        }
        assert_eq!(delivered.cancel(&["chat-1".into()]), vec![1]);
        delivered.closed(2);
        assert!(delivered.activate(1, "default").is_none());
        assert!(delivered.activate(2, "default").is_none());
        delivered.clear();
        assert!(delivered.activate(3, "default").is_none());
    }

    #[test]
    fn delivery_bounds_and_server_identity_are_checked() {
        let mut delivered = Delivered::default();
        assert!(delivered.record(0, 0, None).is_err());
        assert!(delivered.record(4, 3, None).is_err());
        for id in 1..=MAX_DELIVERED as u32 {
            delivered.record(id, 0, None).unwrap();
        }
        assert!(delivered.admit(0).is_err());
        assert!(delivered.admit(1).is_ok());
        delivered.token(1, "invalid\nactivation".into());
        assert_eq!(delivered.activate(1, "default"), Some((None, None)));
        assert!(delivered.admit(0).is_ok());
    }
}
