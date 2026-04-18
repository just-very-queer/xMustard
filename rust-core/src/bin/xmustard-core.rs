use std::env;
use std::fs;
use std::path::PathBuf;

fn main() {
    let mut args = env::args().skip(1);
    let Some(command) = args.next() else {
        eprintln!("usage: xmustard-core <command> [args]");
        std::process::exit(2);
    };

    match command.as_str() {
        "scan-signals" => {
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core scan-signals <root_path>");
                std::process::exit(2);
            };
            let root_path = PathBuf::from(root);
            match xmustard_core::scanner::scan_repo_signals(&root_path) {
                Ok(signals) => {
                    println!(
                        "{}",
                        serde_json::to_string(&signals).expect("scanner result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("scan-signals failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "build-repo-map" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core build-repo-map <workspace_id> <root_path>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core build-repo-map <workspace_id> <root_path>");
                std::process::exit(2);
            };
            let root_path = PathBuf::from(root);
            match xmustard_core::repomap::build_repo_map(&root_path, &workspace_id) {
                Ok(summary) => {
                    println!(
                        "{}",
                        serde_json::to_string(&summary).expect("repo-map result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("build-repo-map failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "parse-coverage-lcov" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core parse-coverage-lcov <workspace_id> <report_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let Some(report_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core parse-coverage-lcov <workspace_id> <report_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            match xmustard_core::verification::parse_lcov_file(
                &PathBuf::from(&report_path),
                &workspace_id,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result).expect("coverage result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("parse-coverage-lcov failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "parse-coverage" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core parse-coverage <workspace_id> <report_path> [run_id] [issue_id]");
                std::process::exit(2);
            };
            let Some(report_path) = args.next() else {
                eprintln!("usage: xmustard-core parse-coverage <workspace_id> <report_path> [run_id] [issue_id]");
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            match xmustard_core::verification::parse_coverage_file(
                &PathBuf::from(&report_path),
                &workspace_id,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result).expect("coverage result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("parse-coverage failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "run-verification-command" => {
            let Some(workspace_root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let Some(timeout_seconds) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let Some(command_text) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let timeout_seconds = timeout_seconds.parse::<u64>().unwrap_or(30);
            match xmustard_core::verification::run_verification_command(
                &PathBuf::from(&workspace_root),
                &command_text,
                timeout_seconds,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("verification command result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("run-verification-command failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "run-verification-profile" => {
            let Some(workspace_root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-profile <workspace_root> <profile_json_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let Some(profile_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-profile <workspace_root> <profile_json_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            let profile_content = match fs::read_to_string(&profile_json_path) {
                Ok(content) => content,
                Err(err) => {
                    eprintln!("run-verification-profile failed to read profile: {err}");
                    std::process::exit(1);
                }
            };
            let profile = match serde_json::from_str::<xmustard_core::verification::RustVerificationProfileInput>(&profile_content) {
                Ok(profile) => profile,
                Err(err) => {
                    eprintln!("run-verification-profile failed to decode profile: {err}");
                    std::process::exit(1);
                }
            };
            match xmustard_core::verification::run_verification_profile(
                &PathBuf::from(&workspace_root),
                &profile,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("verification profile result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("run-verification-profile failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "describe-architecture" => {
            println!(
                "{}",
                serde_json::to_string(&xmustard_core::contracts::no_python_architecture_contract())
                    .expect("architecture contract should serialize")
            );
        }
        _ => {
            eprintln!("unknown command: {command}");
            std::process::exit(2);
        }
    }
}
