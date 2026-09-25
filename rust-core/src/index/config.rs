//! Index configuration (PAR-FRESH-08) and the declared scale envelope (PAR-RT-09).
//!
//! Precedence, lowest first: built-in defaults, the `index` object of `<root>/.xmustard.json`,
//! `XMUSTARD_INDEX_*` environment variables, then command-line flags. YAML repository
//! configs are read by the Go API, which passes the same settings as flags.

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// What source-derived text the index may keep.
///
/// - `full`: chunk source text is stored in `chunk_text` (snippets without disk reads).
/// - `symbol` (default): no source text; chunk postings cover every word of the chunk
///   (identifiers, comments and literals) as contentless FTS5 terms.
/// - `none`: no source text, and the postings hold only code identifiers, which the
///   symbol and reference tables already name. Comment and literal words never reach
///   the index.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum ContentRetention {
    Full,
    #[default]
    Symbol,
    None,
}

impl ContentRetention {
    pub fn as_str(self) -> &'static str {
        match self {
            ContentRetention::Full => "full",
            ContentRetention::Symbol => "symbol",
            ContentRetention::None => "none",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s.trim().to_ascii_lowercase().as_str() {
            "full" => Some(ContentRetention::Full),
            "symbol" => Some(ContentRetention::Symbol),
            "none" => Some(ContentRetention::None),
            _ => None,
        }
    }
}

/// Declared envelope defaults. Beyond them the index is partial and says so; it never
/// grows the worker's memory to cover more.
pub const DEFAULT_MAX_FILES: usize = 10_000;
pub const DEFAULT_MAX_SYMBOLS: usize = 100_000;
pub const DEFAULT_MAX_TOTAL_BYTES: u64 = 300 << 20;
/// Files above this size are not read; generated bundles and data files dominate there,
/// and a parse tree costs several times the source size.
pub const DEFAULT_MAX_FILE_SIZE: u64 = 1 << 20;

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct IndexConfig {
    pub content_retention: ContentRetention,
    pub max_file_size: u64,
    pub allow_non_git: bool,
    pub include_untracked: bool,
    pub max_files: usize,
    pub max_symbols: usize,
    pub max_total_bytes: u64,
    /// Grammar files above this size are extracted lexically (bounded parse memory).
    pub max_parse_bytes: usize,
    /// Reuse facts from the content-addressed cache (disable to measure cold builds).
    pub use_fact_cache: bool,
    /// Store location override; default is `<git-dir>/xmustard-cache/index-v3/<scope>`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub index_dir: Option<PathBuf>,
}

impl Default for IndexConfig {
    fn default() -> Self {
        IndexConfig {
            content_retention: ContentRetention::Symbol,
            max_file_size: DEFAULT_MAX_FILE_SIZE,
            allow_non_git: false,
            include_untracked: false,
            max_files: DEFAULT_MAX_FILES,
            max_symbols: DEFAULT_MAX_SYMBOLS,
            max_total_bytes: DEFAULT_MAX_TOTAL_BYTES,
            max_parse_bytes: super::extract::DEFAULT_MAX_PARSE_BYTES,
            use_fact_cache: true,
            index_dir: None,
        }
    }
}

/// The `index` object of `.xmustard.json`; every field is optional.
#[derive(Debug, Default, Deserialize)]
struct FileIndexConfig {
    content_retention: Option<String>,
    max_file_size: Option<u64>,
    allow_non_git: Option<bool>,
    include_untracked: Option<bool>,
    max_files: Option<usize>,
    max_symbols: Option<usize>,
    max_total_bytes: Option<u64>,
    max_parse_bytes: Option<usize>,
}

#[derive(Debug, Default, Deserialize)]
struct RepoConfigFile {
    #[serde(default)]
    index: Option<FileIndexConfig>,
}

impl IndexConfig {
    /// Defaults, then `<root>/.xmustard.json`, then the environment.
    pub fn load(root: &Path) -> Result<Self, String> {
        let mut cfg = IndexConfig::default();
        let path = root.join(".xmustard.json");
        if let Ok(bytes) =
            crate::symbolgraph::read_repo_bytes_beneath_capped(root, ".xmustard.json", 1 << 20)
        {
            let file: RepoConfigFile =
                serde_json::from_slice(&bytes).map_err(|e| format!("{}: {e}", path.display()))?;
            if let Some(ix) = file.index {
                if let Some(v) = ix.content_retention {
                    cfg.content_retention = ContentRetention::parse(&v)
                        .ok_or_else(|| format!("index.content_retention: unknown value {v:?}"))?;
                }
                if let Some(v) = ix.max_file_size {
                    cfg.max_file_size = v;
                }
                if let Some(v) = ix.allow_non_git {
                    cfg.allow_non_git = v;
                }
                if let Some(v) = ix.include_untracked {
                    cfg.include_untracked = v;
                }
                if let Some(v) = ix.max_files {
                    cfg.max_files = v;
                }
                if let Some(v) = ix.max_symbols {
                    cfg.max_symbols = v;
                }
                if let Some(v) = ix.max_total_bytes {
                    cfg.max_total_bytes = v;
                }
                if let Some(v) = ix.max_parse_bytes {
                    cfg.max_parse_bytes = v;
                }
            }
        }
        cfg.apply_env(|k| std::env::var(k).ok())?;
        Ok(cfg)
    }

    fn apply_env(&mut self, get: impl Fn(&str) -> Option<String>) -> Result<(), String> {
        if let Some(v) = get("XMUSTARD_INDEX_CONTENT_RETENTION") {
            self.content_retention = ContentRetention::parse(&v)
                .ok_or_else(|| format!("XMUSTARD_INDEX_CONTENT_RETENTION: unknown value {v:?}"))?;
        }
        if let Some(v) = get("XMUSTARD_INDEX_MAX_FILE_SIZE") {
            self.max_file_size = parse_num(&v, "XMUSTARD_INDEX_MAX_FILE_SIZE")?;
        }
        if let Some(v) = get("XMUSTARD_INDEX_ALLOW_NON_GIT") {
            self.allow_non_git = parse_bool(&v);
        }
        if let Some(v) = get("XMUSTARD_INDEX_DIR").filter(|v| !v.is_empty()) {
            self.index_dir = Some(PathBuf::from(v));
        }
        Ok(())
    }

    /// Apply one `--flag value` pair; returns false for an unknown flag.
    pub fn apply_flag(&mut self, flag: &str, value: Option<&str>) -> Result<bool, String> {
        let need = || value.ok_or_else(|| format!("{flag} needs a value"));
        match flag {
            "--content-retention" => {
                let v = need()?;
                self.content_retention = ContentRetention::parse(v)
                    .ok_or_else(|| format!("--content-retention: unknown value {v:?}"))?;
            }
            "--max-file-size" => self.max_file_size = parse_num(need()?, flag)?,
            "--max-files" => self.max_files = parse_num(need()?, flag)?,
            "--max-symbols" => self.max_symbols = parse_num(need()?, flag)?,
            "--max-total-bytes" => self.max_total_bytes = parse_num(need()?, flag)?,
            "--max-parse-bytes" => self.max_parse_bytes = parse_num(need()?, flag)?,
            "--index-dir" => self.index_dir = Some(PathBuf::from(need()?)),
            _ => return Ok(false),
        }
        Ok(true)
    }

    /// Boolean switches (no value).
    pub fn apply_switch(&mut self, flag: &str) -> bool {
        match flag {
            "--allow-non-git" => self.allow_non_git = true,
            "--include-untracked" => self.include_untracked = true,
            "--no-cache" => self.use_fact_cache = false,
            _ => return false,
        }
        true
    }

    /// Settings that change which files are indexed or how they are read. Stored in meta;
    /// a change forces a full rebuild so the index never mixes two eligibility rules.
    pub fn eligibility_fingerprint(&self) -> String {
        format!(
            "max_file_size={};max_parse_bytes={};allow_non_git={};include_untracked={};max_files={};max_symbols={};max_total_bytes={}",
            self.max_file_size,
            self.max_parse_bytes,
            self.allow_non_git,
            self.include_untracked,
            self.max_files,
            self.max_symbols,
            self.max_total_bytes
        )
    }
}

fn parse_num<T: std::str::FromStr>(v: &str, what: &str) -> Result<T, String> {
    v.trim()
        .parse()
        .map_err(|_| format!("{what}: not a number: {v:?}"))
}

fn parse_bool(v: &str) -> bool {
    matches!(
        v.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn precedence_file_then_env_then_flags() {
        let dir = tempfile::TempDir::new().unwrap();
        std::fs::write(
            dir.path().join(".xmustard.json"),
            r#"{"description":"x","index":{"content_retention":"none","max_files":7}}"#,
        )
        .unwrap();
        let mut cfg = IndexConfig::load(dir.path()).unwrap();
        // the process env may carry XMUSTARD_INDEX_* in CI; only assert file values
        // that the env override below does not touch.
        assert_eq!(cfg.max_files, 7);
        cfg.apply_env(|k| (k == "XMUSTARD_INDEX_CONTENT_RETENTION").then(|| "full".to_string()))
            .unwrap();
        assert_eq!(cfg.content_retention, ContentRetention::Full);
        assert!(
            cfg.apply_flag("--content-retention", Some("symbol"))
                .unwrap()
        );
        assert_eq!(cfg.content_retention, ContentRetention::Symbol);
        assert!(!cfg.apply_flag("--bogus", Some("1")).unwrap());
        assert!(cfg.apply_flag("--max-files", Some("x")).is_err());
    }

    #[test]
    fn bad_retention_is_an_error() {
        let dir = tempfile::TempDir::new().unwrap();
        std::fs::write(
            dir.path().join(".xmustard.json"),
            r#"{"index":{"content_retention":"everything"}}"#,
        )
        .unwrap();
        assert!(IndexConfig::load(dir.path()).is_err());
    }
}
