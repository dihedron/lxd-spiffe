# Validation evidence

Each folder holds the evidence of one lab run, named after the run's start
time (UTC) and its session.

## Plugin rename (Oct 23, 2026)

The plugins were renamed from `lxd_instance` to `lxd_iid` before their first
release. The runs recorded before the rename were made under the old name: on
Oct 23, 2026 the evidence of those runs was rewritten to the new name (only
the name changed, in SPIFFE IDs, selector types, log messages and plugin
configuration). These are the rewritten runs:

- `20261023T124401Z-e2e-1-summary`
- `20261023T124613Z-e2e-2-auto-summary`
- `20261023T124614Z-e2e-2-summary`
- `20261023T124825Z-e2e-3-summary`
- `20261023T125059Z-e2e-4-summary`
- `20261023T125144Z-e2e-5-summary`
- `20261023T125538Z-e2e-5-summary`

Runs from later on were made under `lxd_iid`.
