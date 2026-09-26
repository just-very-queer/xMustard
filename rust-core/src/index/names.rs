//! Interned, sorted string heaps of a graph segment and the name index built on them.
//!
//! A heap is an offsets section (`n + 1` u32) and a bytes section. File paths and
//! symbol names are both stored sorted, so a string's id is its rank and a lookup is a
//! binary search that reads `O(log n)` offsets and strings through the segment's
//! storage. The name index maps a name to the symbols that declare it (`NamePost`, in
//! path then declaration order) and a file to its symbols (`FileSyms`).

use super::csr::{Sec, Segment};

/// One string heap: an offsets section and its bytes.
#[derive(Clone, Copy)]
struct Heap {
    off: Sec,
    bytes: Sec,
}

const PATHS: Heap = Heap {
    off: Sec::PathOff,
    bytes: Sec::PathHeap,
};
const NAMES: Heap = Heap {
    off: Sec::NameOff,
    bytes: Sec::NameHeap,
};

impl Heap {
    fn get(self, seg: &Segment, i: u32) -> Result<String, String> {
        let (start, n) = seg.span(self.off, i)?;
        let raw = seg.bytes(self.bytes, start, n)?;
        String::from_utf8(raw)
            .map_err(|_| format!("{}: string {i} is not UTF-8", seg.path.display()))
    }

    /// Strings `first..first + n`, reading their bytes in one range.
    fn range(self, seg: &Segment, first: u32, n: usize) -> Result<Vec<String>, String> {
        if n == 0 {
            return Ok(Vec::new());
        }
        let off = seg.rows::<u32>(self.off, first as u64, n + 1)?;
        let base = off[0];
        let raw = seg.bytes(self.bytes, base as u64, (off[n] - base) as usize)?;
        off.windows(2)
            .map(|w| {
                String::from_utf8(raw[(w[0] - base) as usize..(w[1] - base) as usize].to_vec())
                    .map_err(|_| format!("{}: heap string is not UTF-8", seg.path.display()))
            })
            .collect()
    }

    /// The id of `key` in a heap of `count` sorted strings.
    fn find(self, seg: &Segment, count: u32, key: &str) -> Result<Option<u32>, String> {
        let (mut lo, mut hi) = (0u32, count);
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            match self.get(seg, mid)?.as_str().cmp(key) {
                std::cmp::Ordering::Less => lo = mid + 1,
                std::cmp::Ordering::Greater => hi = mid,
                std::cmp::Ordering::Equal => return Ok(Some(mid)),
            }
        }
        Ok(None)
    }
}

/// Called with each name and its symbols.
pub type NameVisitor<'a> = dyn FnMut(&str, &[u32]) -> Result<(), String> + 'a;

/// Rows per batch when a whole section is walked.
const BATCH: u32 = 2048;

impl Segment {
    pub fn file_path(&self, f: u32) -> Result<String, String> {
        PATHS.get(self, f)
    }

    /// The file id of a repository-relative path.
    pub fn file_id(&self, path: &str) -> Result<Option<u32>, String> {
        PATHS.find(self, self.counts().files, path)
    }

    pub fn name(&self, n: u32) -> Result<String, String> {
        NAMES.get(self, n)
    }

    pub fn name_id(&self, name: &str) -> Result<Option<u32>, String> {
        NAMES.find(self, self.counts().names, name)
    }

    /// Symbols declaring name `n`, in path then declaration order.
    pub fn symbols_named(&self, n: u32) -> Result<Vec<u32>, String> {
        let (start, len) = self.span(Sec::NamePostOff, n)?;
        self.rows(Sec::NamePost, start, len)
    }

    /// The whole path heap: its bytes and the `files + 1` offsets into them.
    pub fn path_heap(&self) -> Result<(String, Vec<u32>), String> {
        let n = self.counts().files as usize;
        let off = self.rows::<u32>(PATHS.off, 0, n + 1)?;
        let raw = self.bytes(PATHS.bytes, 0, off[n] as usize)?;
        let heap = String::from_utf8(raw)
            .map_err(|_| format!("{}: path heap is not UTF-8", self.path.display()))?;
        let ordered = off.windows(2).all(|w| w[0] <= w[1]);
        if !ordered || off.iter().any(|o| !heap.is_char_boundary(*o as usize)) {
            return Err(format!(
                "{}: path offsets split a character",
                self.path.display()
            ));
        }
        Ok((heap, off))
    }

    /// Visit every name with its symbols (path, then declaration order), name by name
    /// in sorted order. Names, postings and symbol rows are read in batches.
    pub fn for_each_name(&self, f: &mut NameVisitor<'_>) -> Result<(), String> {
        let count = self.counts().names;
        let mut first = 0;
        while first < count {
            let n = BATCH.min(count - first);
            let names = NAMES.range(self, first, n as usize)?;
            let offs = self.rows::<u32>(Sec::NamePostOff, first as u64, n as usize + 1)?;
            let post = self.rows::<u32>(
                Sec::NamePost,
                offs[0] as u64,
                (offs[n as usize] - offs[0]) as usize,
            )?;
            for (i, name) in names.iter().enumerate() {
                let (a, b) = (
                    (offs[i] - offs[0]) as usize,
                    (offs[i + 1] - offs[0]) as usize,
                );
                f(name, &post[a..b])?;
            }
            first += n;
        }
        Ok(())
    }
}
