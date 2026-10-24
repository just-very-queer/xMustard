use serde_json::Value;

#[test]
fn impact_args_are_typed_and_reject_unknown_fields() {
    let args: xmustard_core::mcp_schema::ImpactArgs = serde_json::from_value(serde_json::json!({
        "workspace_id": "repo",
        "max_depth": 3,
        "relation_types": ["CALLS", "IMPORTS"],
    }))
    .unwrap();
    assert_eq!(args.workspace_id.as_deref(), Some("repo"));
    assert_eq!(args.max_depth, Some(3));
    assert!(matches!(
        args.relation_types,
        Some(xmustard_core::mcp_schema::StringList::Values(_))
    ));
    let csv: xmustard_core::mcp_schema::ImpactArgs = serde_json::from_value(serde_json::json!({
        "relation_types": "CALLS,IMPORTS",
    }))
    .unwrap();
    assert!(matches!(
        csv.relation_types,
        Some(xmustard_core::mcp_schema::StringList::Csv(_))
    ));
    assert!(
        serde_json::from_value::<xmustard_core::mcp_schema::ImpactArgs>(serde_json::json!({
            "relation_types": ["NOT_A_RELATION"],
        }))
        .is_err()
    );
    assert!(
        serde_json::from_value::<xmustard_core::mcp_schema::ImpactArgs>(serde_json::json!({
            "unknown": true,
        }))
        .is_err()
    );
}

#[test]
fn impact_schema_matches_the_published_generated_contract() {
    let generated: Value =
        serde_json::from_str(&xmustard_core::mcp_schema::generate_impact_schema().unwrap())
            .unwrap();
    let published: Value = serde_json::from_str(include_str!(
        "../../api-go/internal/mcpserver/impact_schema_generated.json"
    ))
    .unwrap();
    assert_eq!(generated, published);
}
