# Peer registry

`PEER_REGISTRY.json` records curated source liveness and canonical repository URLs for the projects in the parity research. Refresh it manually when checking for upstream moves or new commits:

```sh
python3 scripts/check_peer_registry.py --write
```

To check whether the checked-in registry is current without changing it, run:

```sh
python3 scripts/check_peer_registry.py --check
```

The checker updates canonical URLs after redirects and refreshes last-commit dates, but never changes the curated `source_present` or `superseded_by` fields. `--check` fails only when the repository identity or canonical URL has drifted; commit-date drift is reported without failing.

The GitHub Actions workflow is manual-only (`workflow_dispatch`); it does not run on a schedule.
