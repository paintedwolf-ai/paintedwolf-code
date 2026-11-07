use crate::{Error, Result};
use tokenizers::Tokenizer;

const NONE: u32 = u32::MAX;
const MAX_NODES: usize = 131072;

#[derive(Clone, Copy)]
struct Node {
    token: u32,
    child: u32,
    next: u32,
    terminal: bool,
}

pub(super) struct Headers {
    nodes: Vec<Node>,
}

impl Headers {
    pub fn new() -> Self {
        Self {
            nodes: vec![Node {
                token: 0,
                child: NONE,
                next: NONE,
                terminal: false,
            }],
        }
    }

    pub fn insert(&mut self, tokens: &[u32]) -> Result<()> {
        if tokens.is_empty() || tokens.len() > 1024 {
            return Err(Error::Protocol(
                "output header exceeds its token bound".into(),
            ));
        }
        let mut parent = 0;
        for &token in tokens {
            if super::reserved(token) || token >= 262144 {
                return Err(Error::Protocol(
                    "output header contains a control token".into(),
                ));
            }
            let mut current = self.nodes[parent].child;
            let mut previous = NONE;
            while current != NONE && self.nodes[current as usize].token < token {
                previous = current;
                current = self.nodes[current as usize].next;
            }
            if current == NONE || self.nodes[current as usize].token != token {
                if self.nodes.len() == MAX_NODES {
                    return Err(Error::Capacity(
                        "output header trie exceeds its bound".into(),
                    ));
                }
                let index = self.nodes.len() as u32;
                self.nodes.push(Node {
                    token,
                    child: NONE,
                    next: current,
                    terminal: false,
                });
                if previous == NONE {
                    self.nodes[parent].child = index;
                } else {
                    self.nodes[previous as usize].next = index;
                }
                current = index;
            }
            parent = current as usize;
        }
        self.nodes[parent].terminal = true;
        Ok(())
    }

    pub fn advance(&self, parent: u32, token: u32) -> Option<(u32, bool)> {
        self.children(parent)
            .find(|(_, candidate)| *candidate == token)
            .map(|(node, _)| (node, self.nodes[node as usize].terminal))
    }

    pub fn children(&self, parent: u32) -> Children<'_> {
        Children {
            headers: self,
            next: self.nodes[parent as usize].child,
        }
    }

    pub fn storage_bytes(&self) -> usize {
        self.nodes.capacity() * std::mem::size_of::<Node>()
    }

    pub fn empty(&self) -> bool {
        self.nodes[0].child == NONE
    }
}

#[derive(Clone)]
pub(super) struct Children<'a> {
    headers: &'a Headers,
    next: u32,
}

impl Iterator for Children<'_> {
    type Item = (u32, u32);
    fn next(&mut self) -> Option<Self::Item> {
        if self.next == NONE {
            return None;
        }
        let index = self.next;
        let node = self.headers.nodes[index as usize];
        self.next = node.next;
        Some((index, node.token))
    }
}

pub(super) fn encode(tokenizer: &Tokenizer, marker: u32, text: &str) -> Result<Vec<u32>> {
    let marker_text = tokenizer
        .id_to_token(marker)
        .ok_or_else(|| Error::Tokenizer("missing output header marker".into()))?;
    let complete = format!("{marker_text}{text}");
    let encoded = tokenizer
        .encode(complete.as_str(), false)
        .map_err(|e| Error::Tokenizer(e.to_string()))?;
    let ids = encoded.get_ids();
    let decoded = tokenizer
        .decode(ids, false)
        .map_err(|e| Error::Tokenizer(e.to_string()))?;
    if ids.first() != Some(&marker) || decoded != complete {
        return Err(Error::Tokenizer(
            "output header does not round-trip exactly".into(),
        ));
    }
    Ok(ids[1..].to_vec())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn trie_preserves_shared_prefixes_and_sorted_sparse_edges() {
        let mut tree = Headers::new();
        tree.insert(&[200, 300, 500]).unwrap();
        tree.insert(&[200, 250, 500]).unwrap();
        let (node, done) = tree.advance(0, 200).unwrap();
        assert!(!done);
        assert_eq!(
            tree.children(node)
                .map(|(_, token)| token)
                .collect::<Vec<_>>(),
            [250, 300]
        );
        let (node, done) = tree.advance(node, 250).unwrap();
        assert!(!done);
        assert!(tree.advance(node, 500).unwrap().1);
        assert!(tree.advance(node, 700).is_none());
        assert!(tree.insert(&[101]).is_err());
        assert!(std::mem::size_of::<Node>() * MAX_NODES <= 2 << 20);
    }
}
