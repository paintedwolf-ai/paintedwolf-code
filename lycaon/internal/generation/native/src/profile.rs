/// Geometry shared by artifact admission, KV storage and the generation graph.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Profile {
    pub layers: usize,
    pub hidden: usize,
    pub attention_heads: usize,
    pub sliding_heads: usize,
    pub global_heads: usize,
    pub shared_layers: usize,
    pub window: usize,
    pub intermediate: usize,
    pub experts: usize,
    pub weight_limit: u64,
}

pub const E4B: Profile = Profile {
    layers: 42,
    hidden: 2560,
    attention_heads: 8,
    sliding_heads: 2,
    global_heads: 2,
    shared_layers: 18,
    window: 512,
    intermediate: 10240,
    experts: 0,
    weight_limit: 8 << 30,
};
pub const DENSE: Profile = Profile {
    layers: 48,
    hidden: 3840,
    attention_heads: 16,
    sliding_heads: 8,
    global_heads: 1,
    shared_layers: 0,
    window: 1024,
    intermediate: 15360,
    experts: 0,
    weight_limit: 10 << 30,
};
pub const MOE: Profile = Profile {
    layers: 30,
    hidden: 2816,
    attention_heads: 16,
    sliding_heads: 8,
    global_heads: 2,
    shared_layers: 0,
    window: 1024,
    intermediate: 2112,
    experts: 128,
    weight_limit: 18 << 30,
};

impl Profile {
    pub fn from_layers(layers: usize) -> Option<Self> {
        [E4B, DENSE, MOE].into_iter().find(|p| p.layers == layers)
    }

    pub fn kv_layers(self) -> usize {
        self.layers - self.shared_layers
    }

    pub fn has_ple(self) -> bool {
        self == E4B
    }

    pub fn kv_heads(self, sliding: bool) -> usize {
        if sliding {
            self.sliding_heads
        } else {
            self.global_heads
        }
    }
}
