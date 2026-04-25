package diff

import "regexp"

// migrateUsingPattern matches a leading "[pgschema migrate-using: <expr>]"
// marker in a column comment. The marker is intended to opt the column into
// data-preserving migration when its composite type's shape changes (handled
// by a follow-up patch — this file is the parser stub so the protocol is
// pinned down before any code starts emitting it).
//
// Pattern notes:
//   - Anchored at the start so trailing user prose is preserved verbatim.
//   - Square brackets are not valid in unquoted Postgres identifiers, which
//     keeps the marker unambiguous and survives `pg_dump`/`COMMENT ON COLUMN`
//     round-trips.
//   - The expression body uses a non-greedy capture so a `]` that legally
//     appears inside the user's USING expression (e.g. an array subscript)
//     would terminate parsing early. We accept that limitation; users with
//     bracketed expressions can wrap them in `array_position`/`unnest` etc.
//     This is a stub — when we wire the directive into emission, we'll
//     revisit if a real-world expression needs richer escaping.
var migrateUsingPattern = regexp.MustCompile(`(?s)^\s*\[pgschema\s+migrate-using:\s*(.+?)\]\s*`)

// extractMigrateUsing reads a "[pgschema migrate-using: <expr>]" directive
// from the head of a column comment. Returns the USING expression, the
// remainder of the comment with the directive stripped, and whether the
// directive was present.
//
// The `cleanComment` return value is what `columnsEqual` should compare
// against, so a column carrying a directive does not appear to have a
// changed comment on every diff run.
func extractMigrateUsing(comment string) (using, cleanComment string, ok bool) {
	if comment == "" {
		return "", "", false
	}
	m := migrateUsingPattern.FindStringSubmatchIndex(comment)
	if m == nil {
		return "", comment, false
	}
	using = comment[m[2]:m[3]]
	cleanComment = comment[m[1]:]
	return using, cleanComment, true
}

// stripMigrateUsing returns comment with any leading directive marker
// removed. Convenience wrapper for callers (such as `columnsEqual`) that
// only care about the cleaned comment, not the directive.
func stripMigrateUsing(comment string) string {
	_, clean, _ := extractMigrateUsing(comment)
	return clean
}
