//! HEAD's trees for the index-to-HEAD comparison (WS-RT18): read through one
//! `git cat-file --batch` child, which reads objects only, never the index. A subtree
//! whose id equals the index's cache-tree id for that directory is not read: every
//! index entry under it equals HEAD's.

use std::io::{BufRead, BufReader, Read, Write};
use std::path::Path;
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use super::Refusal;
use super::reader::{Hash, Oid};

/// The largest tree object read; a directory listing past it fails the comparison.
const MAX_TREE_BYTES: usize = 16 << 20;

/// Git's mode of a tree entry that is a directory.
const MODE_TREE: u32 = 0o040000;

/// One `git cat-file --batch` child. Each request is bounded by the Git timeout: a
/// watchdog kills the child when one answer takes longer.
struct CatFile {
    child: Arc<Mutex<Child>>,
    stdin: ChildStdin,
    out: BufReader<ChildStdout>,
    /// Milliseconds since `epoch` when the pending request was sent; 0 when idle.
    pending: Arc<AtomicU64>,
    done: Arc<AtomicBool>,
    epoch: Instant,
}

impl CatFile {
    fn start(top: &Path) -> Result<CatFile, Refusal> {
        crate::indexcache::note_git_spawn();
        let mut child = Command::new("git")
            .arg("--no-optional-locks")
            .arg("-C")
            .arg(top)
            .args(["cat-file", "--batch"])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|e| Refusal::Failed(format!("git cat-file could not start: {e}")))?;
        let (Some(stdin), Some(stdout)) = (child.stdin.take(), child.stdout.take()) else {
            let _ = child.kill();
            let _ = child.wait();
            return Err(Refusal::Failed("git cat-file: no pipes".into()));
        };
        let cat = CatFile {
            child: Arc::new(Mutex::new(child)),
            stdin,
            out: BufReader::with_capacity(64 << 10, stdout),
            pending: Arc::new(AtomicU64::new(0)),
            done: Arc::new(AtomicBool::new(false)),
            epoch: Instant::now(),
        };
        let (child, pending, done, epoch) = (
            Arc::clone(&cat.child),
            Arc::clone(&cat.pending),
            Arc::clone(&cat.done),
            cat.epoch,
        );
        let limit = crate::indexcache::git_timeout().as_millis() as u64;
        std::thread::spawn(move || {
            while !done.load(Ordering::Relaxed) {
                let since = pending.load(Ordering::Relaxed);
                let now = epoch.elapsed().as_millis() as u64;
                if since != 0 && now.saturating_sub(since) >= limit {
                    if let Ok(mut c) = child.lock() {
                        let _ = c.kill();
                    }
                    return;
                }
                std::thread::sleep(Duration::from_millis(100));
            }
        });
        Ok(cat)
    }

    /// The bytes of tree `oid`.
    fn tree(&mut self, oid: &Oid) -> Result<Vec<u8>, Refusal> {
        let failed = |what: String| Refusal::Failed(format!("git cat-file: {what}"));
        let started = (self.epoch.elapsed().as_millis() as u64).max(1);
        self.pending.store(started, Ordering::Relaxed);
        writeln!(self.stdin, "{}", oid.hex())
            .and_then(|_| self.stdin.flush())
            .map_err(|e| failed(e.to_string()))?;
        let mut header = Vec::new();
        (&mut self.out)
            .take(1024)
            .read_until(b'\n', &mut header)
            .map_err(|e| failed(e.to_string()))?;
        let text = String::from_utf8_lossy(&header).trim().to_string();
        let size = match text.split(' ').collect::<Vec<_>>()[..] {
            [_, "tree", size] => size.parse::<usize>().ok(),
            _ => None,
        }
        .ok_or_else(|| failed(format!("{} is not a readable tree: {text}", oid.hex())))?;
        if size > MAX_TREE_BYTES {
            return Err(failed(format!(
                "tree {} is {size} bytes, past {MAX_TREE_BYTES}",
                oid.hex()
            )));
        }
        let mut body = vec![0u8; size + 1];
        self.out
            .read_exact(&mut body)
            .map_err(|e| failed(e.to_string()))?;
        self.pending.store(0, Ordering::Relaxed);
        body.pop();
        Ok(body)
    }
}

impl Drop for CatFile {
    fn drop(&mut self) {
        self.done.store(true, Ordering::Relaxed);
        if let Ok(mut c) = self.child.lock() {
            let _ = c.kill();
            let _ = c.wait();
        }
    }
}

/// What HEAD holds next, in index order.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum HeadItem {
    /// A blob, symlink or gitlink.
    File { path: Vec<u8>, mode: u32, oid: Oid },
    /// Every path under `prefix` (`""` or `dir/`) is as the index has it.
    Same { prefix: Vec<u8> },
}

struct Frame {
    /// `""` or `dir/`.
    prefix: Vec<u8>,
    data: Vec<u8>,
    at: usize,
}

/// HEAD's paths in index order, depth first, a tree read only when reached.
pub(crate) struct HeadWalk<'a> {
    top: &'a Path,
    hash: Hash,
    cat: Option<CatFile>,
    stack: Vec<Frame>,
    cache_tree: &'a [(Vec<u8>, Oid)],
    root: Option<Oid>,
    peeked: Option<HeadItem>,
}

impl<'a> HeadWalk<'a> {
    /// The walk of HEAD's tree `root` (None when HEAD is unborn).
    pub(crate) fn new(
        top: &'a Path,
        hash: Hash,
        root: Option<Oid>,
        cache_tree: &'a [(Vec<u8>, Oid)],
    ) -> Self {
        HeadWalk {
            top,
            hash,
            cat: None,
            stack: Vec::new(),
            cache_tree,
            root,
            peeked: None,
        }
    }

    fn same_as_index(&self, dir: &[u8], oid: &Oid) -> bool {
        self.cache_tree
            .binary_search_by(|(p, _)| p.as_slice().cmp(dir))
            .is_ok_and(|i| self.cache_tree[i].1 == *oid)
    }

    fn read_tree(&mut self, oid: &Oid) -> Result<Vec<u8>, Refusal> {
        if self.cat.is_none() {
            self.cat = Some(CatFile::start(self.top)?);
        }
        match self.cat.as_mut() {
            Some(cat) => cat.tree(oid),
            None => Err(Refusal::Failed("git cat-file".into())),
        }
    }

    pub(crate) fn peek(&mut self) -> Result<Option<&HeadItem>, Refusal> {
        if self.peeked.is_none() {
            self.peeked = self.fill()?;
        }
        Ok(self.peeked.as_ref())
    }

    pub(crate) fn next(&mut self) -> Result<Option<HeadItem>, Refusal> {
        self.peek()?;
        Ok(self.peeked.take())
    }

    fn fill(&mut self) -> Result<Option<HeadItem>, Refusal> {
        if let Some(root) = self.root.take() {
            if self.same_as_index(b"", &root) {
                return Ok(Some(HeadItem::Same { prefix: Vec::new() }));
            }
            let data = self.read_tree(&root)?;
            self.stack.push(Frame {
                prefix: Vec::new(),
                data,
                at: 0,
            });
        }
        let h = self.hash.oid_len();
        let malformed = || Refusal::Failed("git cat-file: malformed tree".into());
        loop {
            let Some(frame) = self.stack.last_mut() else {
                return Ok(None);
            };
            if frame.at >= frame.data.len() {
                self.stack.pop();
                continue;
            }
            let rest = &frame.data[frame.at..];
            let sp = rest.iter().position(|c| *c == b' ').ok_or_else(malformed)?;
            let nul = rest.iter().position(|c| *c == 0).ok_or_else(malformed)?;
            let oid_bytes = rest.get(nul + 1..nul + 1 + h).ok_or_else(malformed)?;
            let mode = std::str::from_utf8(&rest[..sp])
                .ok()
                .and_then(|m| u32::from_str_radix(m, 8).ok())
                .ok_or_else(malformed)?;
            let path = [frame.prefix.as_slice(), &rest[sp + 1..nul]].concat();
            let oid = Oid::from_bytes(oid_bytes);
            frame.at += nul + 1 + h;
            if mode != MODE_TREE {
                return Ok(Some(HeadItem::File { path, mode, oid }));
            }
            let mut prefix = path;
            if self.same_as_index(&prefix, &oid) {
                prefix.push(b'/');
                return Ok(Some(HeadItem::Same { prefix }));
            }
            let data = self.read_tree(&oid)?;
            prefix.push(b'/');
            self.stack.push(Frame {
                prefix,
                data,
                at: 0,
            });
        }
    }
}
