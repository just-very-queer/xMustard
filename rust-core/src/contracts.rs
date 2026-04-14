use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SubsystemBoundary {
    pub name: &'static str,
    pub current_owner: &'static str,
    pub target_owner: &'static str,
    pub notes: &'static str,
}

pub fn planned_boundaries() -> Vec<SubsystemBoundary> {
    vec![
        SubsystemBoundary {
            name: "scanner",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own repo scanning, signal extraction, ledger ingestion, and verdict merging.",
        },
        SubsystemBoundary {
            name: "repo_map",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own structural repo summaries and ranked path retrieval.",
        },
        SubsystemBoundary {
            name: "verification",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own verification process control and artifact parsing.",
        },
        SubsystemBoundary {
            name: "http_api",
            current_owner: "python",
            target_owner: "api-go",
            notes: "Own HTTP routing and route-by-route parity with the existing frontend contract.",
        },
    ]
}

#[cfg(test)]
mod tests {
    use super::planned_boundaries;

    #[test]
    fn boundaries_cover_core_migration_targets() {
        let boundaries = planned_boundaries();
        assert!(boundaries.iter().any(|item| item.name == "scanner"));
        assert!(boundaries.iter().any(|item| item.name == "http_api"));
    }
}
