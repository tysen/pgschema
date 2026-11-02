# pgschema — issues observed and improvement ideas

Notes from real-world cutovers (frustum project, stage cluster,
April 2026) where declarative migrations surfaced six distinct bugs
requiring manual intervention. The first cutover (~1300 lines of schema
diff) hit Bugs 1–4. A follow-up cutover exercised the Bug 3 fix and
uncovered the need for transitive composite-closure handling. A later
cutover with enum value removals exposed Bugs 5 and 6.

## Fork context

Branch `port-fixes-onto-upstream` (October 2026) re-ports the fork's
fixes onto upstream main at v1.13.4. The old branch
`fix-row-dependent-composite-types` (based on v1.9.0, module renamed to
`github.com/tysen/pgschema`) is kept for reference only. The port keeps
upstream's module path, so the binary is built from a checkout:

```sh
go build -o "$(go env GOPATH)/bin/pgschema" .
```

Three commits on top of upstream, each self-contained:

1. **Composite-of-table-row deferral** (upstream issue #626, open) —
   composite types with an attribute of a new table's row type are
   created after all tables, including transitive chains and domains
   over such composites. Folded into upstream's existing table-dep
   bucket (`typesWithTableDeps` in `generateCreateSQL`, which upstream
   introduced for domains in #535). Tables using a deferred type as a
   column wait for that bucket. Fixtures: `testdata/diff/dependency/
   issue_626_*`.

2. **Plan summary counts `recreate`** (Bug 6) — `internal/plan/plan.go`
   `calculateSummaryFromSteps` now counts `recreate` as a modification
   for tables and non-view types, not just (materialized) views.
   Dedupe uses upstream's `sameChangeKey` (type + path + operation +
   source object) extended to `recreate`, so the warning, DROP and
   CREATE steps of one recreated type count once while overloaded
   functions sharing a path still count separately. The regression
   guard in `cmd/migrate_integration_test.go` fails when `plan.sql`
   is non-empty but `plan.txt` says "No changes detected".

3. **Type recreate cascade** (Bugs 3 and 5; upstream issue #625, open)
   — composite shape changes and non-additive enum changes (removal,
   reorder, rename) are DROP+CREATEd as one block with everything that
   pins them. Implementation:
   - `typeNeedsRecreate` / `isEnumAddOnly` in `internal/diff/type.go`
     decide ALTER vs DROP+CREATE. Add-only enums still go through
     upstream's `generateModifyEnumsSQL`, which runs first in
     `collectMigrationSQL` with a forced commit after each ADD VALUE.
   - `computeRecreatedTypeClosure` in `internal/diff/type_recreate.go`
     expands seeds to composites nesting a closure type and domains
     over one. Shared by `findDependentObjectsForRecreatedTypes` and
     the matview pre-drop so both phases agree.
   - `generateTypeRecreateBlock` emits warning, DROP dependent views,
     DROP COLUMN, DROP TYPE/DOMAIN (reverse topo), CREATE (topo), ADD
     COLUMN, then rebuilds views via `generateRecreatedViewSQL` (the
     helper extracted from upstream's dependent-view recreation, so
     comments, matview indexes and triggers come back).
   - `generateModifyDomainsSQL` skips domains in the closure.
   - Matviews reading an affected table are pre-dropped by
     `preDropMaterializedViewsForRecreatedTypes` and recreated by the
     modify-views phase (synthetic `viewDiff{RequiresRecreate}` when
     the body is unchanged).
   - `typesEqual` takes `targetSchema` and strips the prefix from
     composite attribute types, matching `columnsEqual`.
   - `[pgschema migrate-using: <expr>]` directive **parser** is in
     `internal/diff/column_directive.go` but **not yet wired into
     emission**.
   - 16 fixture suites under `testdata/diff/alter_type/`.

Full `go test ./...` green on the port branch.

### Fixed upstream since the fork diverged

- **Bug 1** (column drop blocked by a view recreated later): upstream
  #444 pre-drops recreated regular views before destructive table
  changes (`generatePreDropRecreatedRegularViewsSQL`).
- **Bug 2** (view created before ALTER TABLE adds its column):
  upstream #414 defers new views referencing a new column on a
  modified table to after `generateModifyTablesSQL`
  (`deferredAddedViews`).
- **Bug 4** (extension object GRANT/REVOKE noise): upstream #595
  excludes extension members (`pg_depend.deptype = 'e'`) and their
  privileges from inspection. The `.pgschemaignore` workaround in
  frustum's schema dir has been removed; confirm on the next stage
  plan that no `pg_stat_statements` ops appear.

## Bugs hit during the cutovers

First cutover surfaced Bugs 1–4 against a stage schema that included:

- `team_season` reshape (5 flat columns → 1 `detail team_season_detail`
  composite column)
- Composite types `season_detail_team`, `team_detail_season` whose
  attribute lists changed
- Views `season_detail`, `team_detail`, `fan_detail`, `player_detail`,
  `fan_display` — some requiring DROP+CREATE
- Additive: `fan.display_name NOT NULL`, `fan_stat` enum gains values

Later cutover (post Bug-3 fix) surfaced Bug 5 when the user removed
three values from the `fan_stat` enum (`AntiBump`, `AntiSteal`,
`AntiInterception`) and ran plan against a stage DB that still had
them — plan reported "No changes detected" twice over: once because
removals were silently dropped (Bug 5), once because even after the
removals were detected the human summary lost the recreate (Bug 6).

### Bug 1: column drop blocked by CREATE OR REPLACE VIEW scheduled later — FIXED UPSTREAM (#444)

**Symptom:** `ERROR: cannot drop column stats of table team_season because
other objects depend on it`

**Cause:** pgschema used `CREATE OR REPLACE VIEW` when output column
names didn't change, but emitted `ALTER TABLE ... DROP COLUMN` before
the view re-create while the old view still referenced the column.

### Bug 2: view created before ALTER TABLE adds referenced column — FIXED UPSTREAM (#414)

**Symptom:** `ERROR: column team_season.detail does not exist`

**Cause:** the CREATE phase ran before the MODIFY phase, so `CREATE
VIEW season_detail` ran before `ALTER TABLE team_season ADD COLUMN
detail`.

### Bug 3: composite type shape change silently ignored — FIXED (port commit 3)

The fix scope grew during the second cutover when the initial
implementation emitted `DROP TYPE fan_stats RESTRICT;` and apply failed
because two other composites (`fan_detail_season`,
`fan_detail_game_team`) had `fan_stats` as an attribute. Extended to
compute the transitive closure of recreating composites and
DROP/CREATE them in topo order around the changing one. Found via
`pg_depend` query during the cutover.

### Bug 4: extension object GRANT/REVOKE noise — FIXED UPSTREAM (#595)

After upgrading pgschema, `plan` proposed GRANT/REVOKE ops on
`pg_stat_statements` and friends — extension objects the schema files
never mentioned. Upstream now excludes extension-owned objects from
inspection, so the `.pgschemaignore` workaround is gone.

### Bug 5: enum value removal/reorder silently ignored — FIXED (port commit 3)

**Symptom:** removing or reordering values in a `CREATE TYPE … AS ENUM (…)`
declaration produced no diff in `pgschema plan`. Mixed remove+add
showed only the additions; the removals vanished.

**Cause:** only `ALTER TYPE … ADD VALUE` was ever emitted. Postgres has
no `DROP VALUE` and no in-place reorder, so the only correct path is
DROP+CREATE with the same dependent-cascade machinery composites use.

### Bug 6: plan summary lost `recreate` operations for non-matview types — FIXED (port commit 2)

**Symptom:** `pgschema plan` printed the full DDL in `plan.sql` but the
human summary still said "No changes detected." Every composite
shape-change fixture's `plan.txt` had the same bug baked in because
the fixtures were generated by the buggy code.

**Cause:** the tables and non-table-type switches in
`calculateSummaryFromSteps` had no `recreate` case; only the
(materialized) view switches did.

## Other shortcuts visible in the code (not hit yet, but likely bites)

| Area | Current shortcut | Future biting scenario |
|------|------------------|------------------------|
| View column refs | `containsIdentifier` text match on definition | Upstream #414/#444 cover added and dropped columns; type changes on a column a view reads are still text-matched |
| Type recreate (composite shape, enum non-additive, domain-over-base) | DROP+CREATE with cascade closure | **Fixed for tables/views/matviews/domains. Function/procedure signatures are NOT in the closure — apply will fail at PG level if any exist.** |
| Function→table deps | Upstream defers functions referencing new tables (#530, #545) | Function referencing a column being added via ALTER on an existing table |
| Trigger→function deps | Topo sort at object granularity | Trigger body referencing function signature being modified |
| Default-value deps | Not deeply tracked | DEFAULT references a function/sequence being dropped or recreated |
| CHECK constraints | Treated as opaque text | CHECK references a column/function being modified |
| RLS policies | Per-new-table deferral exists | Policy USING clause references a column being dropped |
| Partial indexes | WHERE clause opaque | Partial index predicate references dropped column |
| Generated columns | Expression opaque (upstream #591 diffs the expression) | Generated column expression references column being modified |
| Extension objects | Excluded upstream (#595) | OK |
| Inheritance hierarchies | Likely flat-handled | Parent-table ALTERs that must propagate to children |
| Partitioned tables | Upstream handles attach/detach (#639) and partition indexes | Edge cases around partition-specific DDL |
| `CREATE INDEX CONCURRENTLY` | Has `CanRunInTransaction: false` flag | Mixing concurrent index creates with other ops in one plan |
| Domain→function deps | Handled (`domainsWithFunctionDepsOnly` bucket) | OK |
| Composite→table row type | Port commit 1 (transitive) | NEW tables only; MODIFIED tables still a gap |
| Composite shape change | Port commit 3 | Closure-of-composites + tables + views + matviews. **Functions, USING data preservation NOT yet covered.** |
| Enum non-additive change | Port commit 3 | Same closure machinery. **Functions, USING data preservation NOT yet covered.** |
| Plan human summary | Port commit 2; integration test guards summary against SQL pipeline | OK |

## Improvement ideas (ordered by effort × value)

### Low-hanging fruit

1. ~~**Composite shape-change handler**~~ — DONE (Bug 3).

2. ~~**Enum non-additive change handler**~~ — DONE (Bug 5).

3. ~~**Plan summary counts recreate**~~ — DONE (Bug 6).

4. **Wire the `migrate-using` directive into emission** (~1-2 days).
   Parser stub already in place
   (`internal/diff/column_directive.go`). Emission needs to:
   - Detect when a composite/enum column has a `[pgschema
     migrate-using: <expr>]` directive in its `Column.Comment`.
   - Replace the default `DROP COLUMN + ADD COLUMN` with: `ALTER TABLE
     RENAME COLUMN c TO c__pgschema_old; <type recreate>; ADD COLUMN c
     <new_type>; UPDATE t SET c = <USING expr referencing c__pgschema_old>;
     DROP COLUMN c__pgschema_old;`. Wraps automatically in transaction
     so partial failures roll back.
   - Add fixtures under `testdata/diff/alter_type/` for the composite
     and enum cases.
   - Required real-world prerequisite for any cutover where the
     column has data the user wants to preserve. Currently we only
     accept data loss with a NOT NULL warning.

5. ~~**Default ignore list for common extensions**~~ — DONE upstream
   (#595).

6. **Function/procedure signatures referencing recreated types**
   (~1 day). Extend the cascade closure in
   `computeRecreatedTypeClosure` to scan function parameter types and
   return types. Functions that signature-depend on a recreating
   composite or enum need DROP+CREATE around the type. Currently
   apply fails with a clear PG error if any exist; the warning text
   tells the user.

7. **Upstream the port.** Issues #625 and #626 are open upstream with
   no PRs; port commits 1 and 3 are shaped as fixes for them. Commit 2
   stands on its own.

### Medium effort

8. **ALTER-column → view recreate awareness** (~2-3 days).
   Upstream now covers added (#414) and dropped (#444) columns by text
   match. The remaining gap is column type changes a view reads, and
   the same text-matching weakness for functions, defaults and CHECKs.
   Parse view SELECT lists using the bundled PG parser
   (`internal/gram.y`, `scan.l`) to extract column-level references
   and store them on `ir.View`.

9. **Operation-level (not object-level) topological sort** (~1-2
   weeks). Refactor the diff phases into a flat list of atomic
   operations, each tagged with its prerequisites. Run topo sort on
   ops, not objects. Benefits:
   - Interleaves CREATE and MODIFY naturally instead of the growing
     set of deferral buckets.
   - Can detect unsatisfiable cycles at plan time.
   - Type-recreate cascade currently lives in its own ad-hoc
     ordering inside `generateTypeRecreateBlock`. Op-level topo
     sort would unify that with the rest of the pipeline.

### High effort but high ceiling

10. **Plan dry-run against shadow DB** (~1-2 weeks).
    Before returning the plan to the user, apply it to an embedded PG
    instance restored from the current target-DB state (via `pg_dump
    --schema-only | pg_restore`). If apply fails in shadow, fail the
    plan command with the shadow error. This catches *unknown unknowns*
    — bugs we haven't discovered yet. Would have caught the
    nested-composite-closure issue, the spurious record_type-recreate
    regression introduced by the typesEqual schema-prefix fix, AND
    Bug 6.

    Complements 1-9: they fix specific known issues, 10 catches what
    remains.

11. **Column-level dependency graph**, general-purpose (~1-2 months).
    The full version of what 8 starts. Parse every SQL body (views,
    functions, triggers, policies, partial index WHERE, domain CHECK,
    generated columns) for column-level references. Build a fine-grained
    dependency graph. Use it for every phase of planning.

    This is what pg_dump essentially does (over many years and many
    accumulated edge cases). Multi-month undertaking. Probably not worth
    it compared to 10.

## Architectural notes for future-you

- Diff pipeline lives in `internal/diff/diff.go`, function
  `collectMigrationSQL`. Phases, in order: `generateModifyEnumsSQL`
  (add-only ADD VALUE, forced commit after) →
  `generatePreDropMaterializedViewsSQL` (also pre-drops recreated
  regular views for #444 and matviews over recreated types) →
  release sequence owners → `generateDropSQL` → `generateCreateSQL` →
  `generateModifySQL`.
- Type-recreate cascade is computed by
  `findDependentObjectsForRecreatedTypes`
  (`internal/diff/type_recreate.go`) and emitted by
  `generateTypeRecreateBlock` from `generateModifySQL`, right after
  `generateModifyDomainsSQL`. The closure builder
  `computeRecreatedTypeClosure` is shared with the matview pre-drop so
  both phases agree on which types recreate. Seeds are
  `typeNeedsRecreate(diff)`; expansion pulls in composites whose
  attributes reference a closure member and domains whose `BaseType`
  does.
- Plan summary (`internal/plan/plan.go::calculateSummaryFromSteps`)
  groups source diffs by object kind. Every switch over operation
  strings must include `recreate`. The integration test asserts that
  a non-empty `plan.sql` rules out a "No changes detected" summary.
  Mirror this guard in any future test that golden-files a derived
  view of a plan.
- The IR is in `ir/`. `Type` has `Columns []*TypeColumn` for
  composites and `EnumValues []string` for enums. `ddlDiff` carries
  `allNewTypes`, `allNewTables`, `allNewViews` maps for cascade
  detection across the full new state.
- Embedded PG for plan validation lives in `internal/postgres/`. Plan
  already uses it to validate the desired-state SQL parses cleanly;
  extending it to dry-run the plan itself is the #10 idea.
- Deferral buckets in `generateCreateSQL` (upstream's, extended):
  - `typesWithoutDeps` — plain enums, composites, non-deferred domains
  - `domainsWithFunctionDepsOnly` — domains whose CHECK references a
    new function
  - `typesWithTableDeps` — domains and composites built (transitively)
    on a new table's row type; emitted after all other tables
  - `tablesWithoutDeps` / `tablesWithDeps` / `tablesAfterTableDomains`
    — tables referencing new functions, function-dep domains, or
    table-dep types
  - functions split by view deps, table deps and aggregate calls;
    `deferredAddedViews` (#414) and `functionsAwaitingRecreatedViews`
    (#480) spill into the modify phase
- Text matching for view-to-view dependency: `viewDependsOnView` in
  `internal/diff/view.go` uses `containsIdentifier` for word-boundary
  substring matching on the view's SQL. Good first approximation;
  precise alternative is full AST analysis (#11).
- `typesEqual` in `internal/diff/type.go` takes `targetSchema` and
  strips schema-prefixes from composite attribute types when comparing.
- `pg_depend` queries are the authoritative source of truth for what
  depends on what. Run interactively (e.g. via `just kpg`) to debug
  apply-time `cannot drop X because Y depends on it` errors. The
  cascade closure should match what `pg_depend.refobjid` returns.

### Lesson on golden-file tests (Bug 6)

Bug 6 was hidden for the entire lifetime of the composite fix because
every `plan.txt` fixture was generated by the same buggy code that
produced the bug. The integration test compared regenerated output to
itself and passed. The lesson: when you golden-file a derived view
(human summary) of a primary artifact (DDL pipeline), add a structural
invariant linking them — here, "non-empty SQL ⇒ non-empty summary".
Otherwise `-generate` silently launders bugs into the fixtures.

## Operational lessons

- `backoffLimit: 0` on the K8s Job is the right default until we know
  pgschema's concurrency story well. Flux handles retries at the
  Kustomization level.
- Immediate `plan` after `apply` should be empty. Use it as a success
  signal in automation.
- When something goes wrong in the middle of a multi-op apply, the
  remaining ops haven't run. Don't assume "apply ran" = "schema
  migrated." Always re-plan to confirm.
- For stage cutovers, CNPG's primary can be found via
  `kubectl get pods -l cnpg.io/cluster -o jsonpath='{range .items[*]}{.metadata.name}: {.metadata.labels.cnpg\.io/instanceRole}{"\n"}{end}'`.
  Manual ops via `psql -U postgres` (peer auth) on that pod.
- When a composite recreate fails apply with "cannot drop type X
  because other objects depend on it", run
  `SELECT classid::regclass, objid::regclass FROM pg_depend WHERE
  refobjid = 'public.X'::regtype AND deptype IN ('n', 'a');` to see
  what's still referencing it. Common cause: nested composite types
  with the failing type as an attribute.
