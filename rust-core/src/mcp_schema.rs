//! MCP argument schemas generated from the Rust request types.
//!
//! The Go MCP bridge embeds the generated descriptors so its published schema and
//! argument validation use the same field contract.

use serde::{Deserialize, Serialize};

pub trait SchemaType {
    const JSON_TYPE: &'static str;
    const LIST: bool = false;
    const REQUIRED: bool = true;

    fn enum_values() -> Vec<String> {
        Vec::new()
    }
}

impl SchemaType for String {
    const JSON_TYPE: &'static str = "string";
}

impl SchemaType for bool {
    const JSON_TYPE: &'static str = "boolean";
}

impl SchemaType for u32 {
    const JSON_TYPE: &'static str = "integer";
}

impl SchemaType for u64 {
    const JSON_TYPE: &'static str = "integer";
}

impl SchemaType for f64 {
    const JSON_TYPE: &'static str = "number";
}

impl<T: SchemaType> SchemaType for Option<T> {
    const JSON_TYPE: &'static str = T::JSON_TYPE;
    const LIST: bool = T::LIST;
    const REQUIRED: bool = false;

    fn enum_values() -> Vec<String> {
        T::enum_values()
    }
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
pub enum StringList<T> {
    Csv(String),
    Values(Vec<T>),
}

impl<T: SchemaType> SchemaType for StringList<T> {
    const JSON_TYPE: &'static str = "string";
    const LIST: bool = true;

    fn enum_values() -> Vec<String> {
        T::enum_values()
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct ArgSchema {
    pub name: String,
    #[serde(rename = "type")]
    pub json_type: &'static str,
    #[serde(skip_serializing_if = "Vec::is_empty", rename = "enum")]
    pub enum_values: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub min: Option<i32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max: Option<i32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_len: Option<usize>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub list: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub required: bool,
    pub advanced: bool,
    pub desc: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct ToolSchema {
    pub args: Vec<ArgSchema>,
}

fn field<T: SchemaType>(
    name: &str,
    desc: &'static str,
    advanced: bool,
    min: Option<i32>,
    max: Option<i32>,
    max_len: Option<usize>,
) -> ArgSchema {
    ArgSchema {
        name: name.to_string(),
        json_type: T::JSON_TYPE,
        enum_values: T::enum_values(),
        min,
        max,
        max_len,
        list: T::LIST,
        required: T::REQUIRED,
        advanced,
        desc,
    }
}

macro_rules! mcp_args {
    ($name:ident { $($field:ident: $type:ty => ($advanced:expr, $desc:literal, $min:expr, $max:expr, $max_len:expr)),* $(,)? }) => {
        #[derive(Debug, Default, Deserialize)]
        #[serde(deny_unknown_fields)]
        pub struct $name {
            $(#[serde(default)] pub $field: $type,)*
        }

        impl $name {
            pub fn schema() -> ToolSchema {
                ToolSchema {
                    args: vec![$(field::<$type>(
                        stringify!($field), $desc, $advanced, $min, $max, $max_len,
                    )),*],
                }
            }
        }
    };
}

macro_rules! string_enum {
    ($name:ident { $($variant:ident => $value:literal),+ $(,)? }) => {
        #[derive(Debug, Clone, Copy, Deserialize)]
        pub enum $name {
            $(#[serde(rename = $value)] $variant,)+
        }

        impl SchemaType for $name {
            const JSON_TYPE: &'static str = "string";

            fn enum_values() -> Vec<String> {
                vec![$($value.to_string()),+]
            }
        }
    };
}

string_enum!(ImpactOp {
    Refs => "refs",
    Definition => "definition",
    Implementations => "implementations",
});
string_enum!(ImpactDirection {
    Upstream => "upstream",
    Downstream => "downstream",
});
string_enum!(ImpactRelation {
    Calls => "CALLS",
    Imports => "IMPORTS",
    Extends => "EXTENDS",
    Implements => "IMPLEMENTS",
    Uses => "USES",
    Accesses => "ACCESSES",
    HasMethod => "HAS_METHOD",
    HasProperty => "HAS_PROPERTY",
});
string_enum!(ImpactScope {
    Unstaged => "unstaged",
    Staged => "staged",
    All => "all",
    Workspace => "workspace",
    Compare => "compare",
    Commit => "commit",
    Range => "range",
});

mcp_args!(ImpactArgs {
    workspace_id: Option<String> => (false, "workspace id; auto-resolved if omitted", None, None, None),
    symbol: Option<String> => (false, "name, Container.name, UID or path", None, None, None),
    path: Option<String> => (false, "a repo-relative file", None, None, Some(4096)),
    from: Option<String> => (false, "trace from here", None, None, None),
    to: Option<String> => (false, "trace to here", None, None, None),
    max_depth: Option<u32> => (false, "hops (default 3; path= 4)", Some(1), Some(32), None),
    op: Option<ImpactOp> => (true, "symbol-level occurrences instead of the depth tiers: refs (every use), definition (what declares it), implementations (types implementing it)", None, None, None),
    direction: Option<ImpactDirection> => (true, "dependents (default) or dependencies", None, None, None),
    relation_types: Option<StringList<ImpactRelation>> => (true, "edge kinds (default CALLS,IMPORTS,EXTENDS,IMPLEMENTS)", None, None, Some(128)),
    min_confidence: Option<f64> => (true, "skip edges below this confidence", Some(0), Some(1), None),
    include_tests: Option<bool> => (true, "walk test files (default false)", None, None, None),
    limit: Option<u32> => (true, "items per depth tier (default 50), or per page with op=", Some(1), Some(500), None),
    offset: Option<u32> => (true, "items skipped per tier", Some(0), Some(50000), None),
    summary_only: Option<bool> => (true, "counts, risk, no items", None, None, None),
    timeout_ms: Option<u32> => (true, "walk budget (default 10000)", Some(1), Some(60000), None),
    scope: Option<ImpactScope> => (true, "symbols a diff touches, and dependents", None, None, None),
    base: Option<String> => (true, "scope=compare|range: base", None, None, Some(256)),
    head: Option<String> => (true, "scope=compare|commit|range: head", None, None, Some(256)),
    worktree: Option<String> => (true, "another checkout to diff", None, None, Some(4096)),
    cursor: Option<String> => (true, "next_cursor of the previous page", None, None, Some(64)),
    max_chars: Option<u32> => (true, "result budget (default 8000)", Some(2000), Some(65536), None),
});

pub fn generate_impact_schema() -> Result<String, serde_json::Error> {
    serde_json::to_string_pretty(&ImpactArgs::schema())
}
