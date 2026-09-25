use std::collections::VecDeque;

/// A least-recently-used cache with a fixed capacity.
pub struct LruCache {
    order: VecDeque<String>,
    capacity: usize,
}

impl LruCache {
    /// Evict the least recently used key when the cache is full.
    pub fn evict_oldest(&mut self) -> Option<String> {
        if self.order.len() >= self.capacity {
            return self.order.pop_front();
        }
        None
    }
}
