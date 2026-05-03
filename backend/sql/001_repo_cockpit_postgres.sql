-- xMustard repo cockpit PostgreSQL foundation
--
-- This schema is the first persistent-store anchor for the repo cockpit direction.
-- It does not replace the current JSON artifact files yet. It defines the first
-- durable tables we expect to grow into as xMustard becomes a standalone repo
-- intelligence and agent grounding tool.

create schema if not exists {{schema}};

create table if not exists {{schema}}.workspaces (
    workspace_id text primary key,
    name text not null,
    root_path text not null,
    latest_scan_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists {{schema}}.workspace_snapshots (
    snapshot_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    scanner_version integer not null,
    summary_json jsonb not null default '{}'::jsonb,
    generated_at timestamptz not null default now()
);

create table if not exists {{schema}}.files (
    file_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    path text not null,
    role text not null default 'source',
    language text,
    size_bytes bigint,
    content_hash text,
    first_indexed_at timestamptz not null default now(),
    last_indexed_at timestamptz not null default now(),
    unique (workspace_id, path)
);

create table if not exists {{schema}}.symbols (
    symbol_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    file_id bigint references {{schema}}.files(file_id) on delete cascade,
    path text not null,
    symbol text not null,
    kind text not null,
    language text,
    line_start integer,
    line_end integer,
    enclosing_scope text,
    signature_text text,
    symbol_text text,
    indexed_at timestamptz not null default now()
);

create table if not exists {{schema}}.file_symbol_summaries (
    summary_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    file_id bigint references {{schema}}.files(file_id) on delete cascade,
    path text not null,
    language text,
    parser_language text,
    symbol_source text not null default 'heuristic',
    symbol_count integer not null default 0,
    summary_json jsonb not null default '{}'::jsonb,
    indexed_at timestamptz not null default now(),
    unique (workspace_id, path)
);

create table if not exists {{schema}}.symbol_edges (
    edge_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    from_symbol_id bigint references {{schema}}.symbols(symbol_id) on delete cascade,
    to_symbol_id bigint references {{schema}}.symbols(symbol_id) on delete cascade,
    edge_kind text not null,
    weight numeric(10, 4) not null default 1.0,
    created_at timestamptz not null default now()
);

create table if not exists {{schema}}.activity_events (
    activity_id text primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    entity_type text not null,
    entity_id text not null,
    action text not null,
    summary text not null,
    actor_json jsonb not null,
    issue_id text,
    run_id text,
    details_json jsonb not null default '{}'::jsonb,
    created_at timestamptz not null
);

create table if not exists {{schema}}.run_records (
    run_id text primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    issue_id text not null,
    runtime text not null,
    model text not null,
    status text not null,
    title text not null,
    prompt text not null,
    command_preview text,
    worktree_json jsonb,
    guidance_paths_json jsonb not null default '[]'::jsonb,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz not null default now()
);

create table if not exists {{schema}}.semantic_queries (
    query_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    issue_id text,
    run_id text references {{schema}}.run_records(run_id) on delete set null,
    source text not null default 'issue_context',
    reason text,
    pattern text not null,
    language text,
    path_glob text,
    engine text not null default 'ast_grep',
    match_count integer not null default 0,
    truncated boolean not null default false,
    error text,
    executed_at timestamptz not null default now()
);

create table if not exists {{schema}}.semantic_matches (
    match_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    query_id bigint references {{schema}}.semantic_queries(query_id) on delete cascade,
    file_id bigint references {{schema}}.files(file_id) on delete set null,
    symbol_id bigint references {{schema}}.symbols(symbol_id) on delete set null,
    path text not null,
    language text,
    line_start integer,
    line_end integer,
    column_start integer,
    column_end integer,
    matched_text text not null,
    context_lines text,
    meta_variables_json jsonb not null default '[]'::jsonb,
    reason text,
    score integer not null default 0,
    matched_at timestamptz not null default now()
);

create table if not exists {{schema}}.semantic_index_runs (
    index_run_id text primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    surface text not null default 'cli',
    strategy text not null default 'key_files',
    index_fingerprint text not null,
    head_sha text,
    dirty_files integer not null default 0,
    worktree_dirty boolean not null default false,
    selected_paths_json jsonb not null default '[]'::jsonb,
    selected_path_details_json jsonb not null default '[]'::jsonb,
    materialized_paths_json jsonb not null default '[]'::jsonb,
    file_rows integer not null default 0,
    symbol_rows integer not null default 0,
    summary_rows integer not null default 0,
    postgres_schema text not null default 'xmustard',
    tree_sitter_available boolean not null default false,
    ast_grep_available boolean not null default false,
    created_at timestamptz not null default now()
);

create table if not exists {{schema}}.run_plans (
    plan_id bigserial primary key,
    run_id text not null references {{schema}}.run_records(run_id) on delete cascade,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    issue_id text not null,
    phase text not null,
    version integer not null default 1,
    ownership_mode text not null default 'agent',
    owner_label text,
    plan_json jsonb not null default '{}'::jsonb,
    attached_files_json jsonb not null default '[]'::jsonb,
    branch text,
    head_sha text,
    dirty_paths_json jsonb not null default '[]'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (run_id)
);

create table if not exists {{schema}}.run_plan_revisions (
    revision_id bigserial primary key,
    plan_id bigint not null references {{schema}}.run_plans(plan_id) on delete cascade,
    run_id text not null references {{schema}}.run_records(run_id) on delete cascade,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    version integer not null,
    phase text not null,
    ownership_mode text not null,
    owner_label text,
    summary text,
    feedback text,
    branch text,
    head_sha text,
    dirty_paths_json jsonb not null default '[]'::jsonb,
    attached_files_json jsonb not null default '[]'::jsonb,
    revision_json jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now()
);

create table if not exists {{schema}}.verification_profiles (
    profile_id text primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    name text not null,
    description text not null default '',
    test_command text not null,
    coverage_command text,
    coverage_report_path text,
    coverage_format text not null default 'unknown',
    max_runtime_seconds integer not null default 30,
    retry_count integer not null default 1,
    source_paths_json jsonb not null default '[]'::jsonb,
    checklist_items_json jsonb not null default '[]'::jsonb,
    built_in boolean not null default false,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists {{schema}}.verification_runs (
    verification_run_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    profile_id text not null references {{schema}}.verification_profiles(profile_id) on delete cascade,
    run_id text references {{schema}}.run_records(run_id) on delete set null,
    issue_id text,
    success boolean not null,
    attempt_count integer not null default 1,
    confidence numeric(10, 4),
    result_json jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now()
);

create table if not exists {{schema}}.issue_artifacts (
    artifact_id text primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    issue_id text not null,
    artifact_type text not null,
    title text not null,
    payload_json jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists {{schema}}.diagnostics (
    diagnostic_id bigserial primary key,
    workspace_id text not null references {{schema}}.workspaces(workspace_id) on delete cascade,
    path text not null,
    source text not null,
    severity text not null,
    code text,
    message text not null,
    line_start integer,
    line_end integer,
    symbol_id bigint references {{schema}}.symbols(symbol_id) on delete set null,
    observed_at timestamptz not null default now()
);

create index if not exists workspaces_root_path_idx on {{schema}}.workspaces(root_path);
create index if not exists workspace_snapshots_workspace_id_idx on {{schema}}.workspace_snapshots(workspace_id, generated_at desc);
create index if not exists files_workspace_path_idx on {{schema}}.files(workspace_id, path);
create index if not exists symbols_workspace_path_idx on {{schema}}.symbols(workspace_id, path);
create index if not exists symbols_workspace_symbol_idx on {{schema}}.symbols(workspace_id, symbol);
create index if not exists file_symbol_summaries_workspace_path_idx on {{schema}}.file_symbol_summaries(workspace_id, path);
create index if not exists symbol_edges_workspace_idx on {{schema}}.symbol_edges(workspace_id, edge_kind);
create index if not exists semantic_queries_workspace_idx on {{schema}}.semantic_queries(workspace_id, executed_at desc);
create index if not exists semantic_matches_workspace_path_idx on {{schema}}.semantic_matches(workspace_id, path, matched_at desc);
create index if not exists semantic_matches_query_idx on {{schema}}.semantic_matches(query_id);
create index if not exists semantic_index_runs_workspace_surface_idx on {{schema}}.semantic_index_runs(workspace_id, surface, created_at desc);
create index if not exists activity_events_workspace_idx on {{schema}}.activity_events(workspace_id, created_at desc);
create index if not exists run_records_workspace_idx on {{schema}}.run_records(workspace_id, created_at desc);
create index if not exists run_plans_workspace_idx on {{schema}}.run_plans(workspace_id, issue_id, updated_at desc);
create index if not exists run_plan_revisions_plan_idx on {{schema}}.run_plan_revisions(plan_id, version desc);
create index if not exists verification_runs_workspace_idx on {{schema}}.verification_runs(workspace_id, created_at desc);
create index if not exists diagnostics_workspace_path_idx on {{schema}}.diagnostics(workspace_id, path, observed_at desc);

-- Text search foundation. BM25-capable ranking can be layered on top of this Postgres
-- text-search substrate using the search stack we adopt next.
alter table {{schema}}.files add column if not exists search_document tsvector;
alter table {{schema}}.symbols add column if not exists search_document tsvector;
alter table {{schema}}.semantic_matches add column if not exists search_document tsvector;
alter table {{schema}}.run_plans add column if not exists search_document tsvector;
alter table {{schema}}.issue_artifacts add column if not exists search_document tsvector;

create index if not exists files_search_document_idx on {{schema}}.files using gin(search_document);
create index if not exists symbols_search_document_idx on {{schema}}.symbols using gin(search_document);
create index if not exists semantic_matches_search_document_idx on {{schema}}.semantic_matches using gin(search_document);
create index if not exists run_plans_search_document_idx on {{schema}}.run_plans using gin(search_document);
create index if not exists issue_artifacts_search_document_idx on {{schema}}.issue_artifacts using gin(search_document);
