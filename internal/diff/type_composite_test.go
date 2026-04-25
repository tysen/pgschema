package diff

import (
	"testing"

	"github.com/tysen/pgschema/ir"
)

// makeComposite is a tiny helper for table-driven tests.
func makeComposite(schema, name string, attrs ...string) *ir.Type {
	cols := make([]*ir.TypeColumn, 0, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		cols = append(cols, &ir.TypeColumn{
			Name:     attrs[i],
			DataType: attrs[i+1],
			Position: i/2 + 1,
		})
	}
	return &ir.Type{Schema: schema, Name: name, Kind: ir.TypeKindComposite, Columns: cols}
}

func TestColumnReferencesCompositeType(t *testing.T) {
	tests := []struct {
		dataType string
		schema   string
		typeName string
		want     bool
	}{
		{"team_season_detail", "public", "team_season_detail", true},
		{"public.team_season_detail", "public", "team_season_detail", true},
		{"team_season_detail[]", "public", "team_season_detail", true},
		{"PUBLIC.Team_Season_Detail", "public", "team_season_detail", true},
		{"team_season_summary", "public", "team_season_detail", false},
		{"", "public", "team_season_detail", false},
		{"other.team_season_detail", "public", "team_season_detail", false},
	}
	for _, tc := range tests {
		col := &ir.Column{Name: "x", DataType: tc.dataType}
		got := columnReferencesCompositeType(col, tc.schema, tc.typeName)
		if got != tc.want {
			t.Errorf("columnReferencesCompositeType(%q, %q, %q) = %v, want %v",
				tc.dataType, tc.schema, tc.typeName, got, tc.want)
		}
	}
}

func TestIsCompositeReorderOnly(t *testing.T) {
	old := makeComposite("public", "t",
		"a", "integer",
		"b", "integer",
		"c", "integer",
	)
	reordered := makeComposite("public", "t",
		"c", "integer",
		"a", "integer",
		"b", "integer",
	)
	renamed := makeComposite("public", "t",
		"a", "integer",
		"b", "integer",
		"d", "integer",
	)
	differentLen := makeComposite("public", "t",
		"a", "integer",
		"b", "integer",
	)
	if !isCompositeReorderOnly(old, reordered) {
		t.Errorf("expected reorder to be detected as reorder-only")
	}
	if isCompositeReorderOnly(old, renamed) {
		t.Errorf("expected rename to NOT be reorder-only")
	}
	if isCompositeReorderOnly(old, differentLen) {
		t.Errorf("expected length difference to NOT be reorder-only")
	}
	if isCompositeReorderOnly(nil, old) || isCompositeReorderOnly(old, nil) {
		t.Errorf("nil input should not be reorder-only")
	}
}

func TestFindDependentObjectsForRecreatedTypes_TableColumn(t *testing.T) {
	composite := makeComposite("public", "team_season_detail",
		"a", "integer",
	)
	newComposite := makeComposite("public", "team_season_detail",
		"b", "integer",
	)
	table := &ir.Table{
		Schema: "public",
		Name:   "team_season",
		Columns: []*ir.Column{
			{Name: "id", DataType: "integer"},
			{Name: "detail", DataType: "team_season_detail"},
		},
	}
	types := map[string]*ir.Type{"public.team_season_detail": newComposite}
	tables := map[string]*ir.Table{"public.team_season": table}
	views := map[string]*ir.View{}
	diffs := []*typeDiff{{Old: composite, New: newComposite}}

	typeCtx, colCtx, viewCtx := findDependentObjectsForRecreatedTypes(types, tables, views, diffs)

	if typeCtx.IsEmpty() || len(typeCtx.types) != 1 {
		t.Fatalf("expected 1 type in closure, got %d", len(typeCtx.types))
	}
	got := colCtx.GetAll()
	if len(got) != 1 {
		t.Fatalf("expected 1 dependent column, got %d", len(got))
	}
	if got[0].Column.Name != "detail" || got[0].Table.Name != "team_season" {
		t.Errorf("unexpected dependent column: %+v", got[0])
	}
	if len(viewCtx.GetDependents(compositeRecreateBlockKey)) != 0 {
		t.Errorf("expected no dependent views")
	}
}

func TestFindDependentObjectsForRecreatedTypes_ViewDependents(t *testing.T) {
	composite := makeComposite("public", "team_season_detail", "a", "integer")
	newComposite := makeComposite("public", "team_season_detail", "b", "integer")
	table := &ir.Table{
		Schema: "public",
		Name:   "team_season",
		Columns: []*ir.Column{
			{Name: "detail", DataType: "team_season_detail"},
		},
	}
	leafView := &ir.View{
		Schema:     "public",
		Name:       "season_detail",
		Definition: "SELECT detail FROM team_season",
	}
	transitiveView := &ir.View{
		Schema:     "public",
		Name:       "season_summary",
		Definition: "SELECT count(*) FROM season_detail",
	}
	types := map[string]*ir.Type{"public.team_season_detail": newComposite}
	tables := map[string]*ir.Table{"public.team_season": table}
	views := map[string]*ir.View{
		"public.season_detail":  leafView,
		"public.season_summary": transitiveView,
	}
	diffs := []*typeDiff{{Old: composite, New: newComposite}}

	_, colCtx, viewCtx := findDependentObjectsForRecreatedTypes(types, tables, views, diffs)

	if len(colCtx.GetAll()) != 1 {
		t.Errorf("expected 1 dependent column")
	}
	deps := viewCtx.GetDependents(compositeRecreateBlockKey)
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependent views (direct + transitive), got %d", len(deps))
	}
	// Topo order: leaf first, dependent of leaf second.
	if deps[0].Name != "season_detail" || deps[1].Name != "season_summary" {
		t.Errorf("unexpected topo order: %s, %s", deps[0].Name, deps[1].Name)
	}
}

func TestFindDependentObjectsForRecreatedTypes_NoMatviewsInViewCtx(t *testing.T) {
	composite := makeComposite("public", "team_season_detail", "a", "integer")
	newComposite := makeComposite("public", "team_season_detail", "b", "integer")
	table := &ir.Table{
		Schema: "public",
		Name:   "team_season",
		Columns: []*ir.Column{
			{Name: "detail", DataType: "team_season_detail"},
		},
	}
	matview := &ir.View{
		Schema:       "public",
		Name:         "season_detail_mv",
		Materialized: true,
		Definition:   "SELECT detail FROM team_season",
	}
	types := map[string]*ir.Type{"public.team_season_detail": newComposite}
	tables := map[string]*ir.Table{"public.team_season": table}
	views := map[string]*ir.View{
		"public.season_detail_mv": matview,
	}
	diffs := []*typeDiff{{Old: composite, New: newComposite}}

	_, _, viewCtx := findDependentObjectsForRecreatedTypes(types, tables, views, diffs)
	if len(viewCtx.GetDependents(compositeRecreateBlockKey)) != 0 {
		t.Errorf("matviews should be filtered from the view-cascade context (handled in pre-drop instead)")
	}
}

// TestFindDependentObjectsForRecreatedTypes_NestedClosure verifies the
// composite-of-composite recursion: when fan_stats changes shape, types that
// have fan_stats as an attribute (like fan_detail_season) must also be
// pulled into the recreate closure even though their own shape didn't
// change.
func TestFindDependentObjectsForRecreatedTypes_NestedClosure(t *testing.T) {
	oldFanStats := makeComposite("public", "fan_stats", "goals", "real")
	newFanStats := makeComposite("public", "fan_stats", "goals", "real", "assists", "real")
	// fan_detail_season nests fan_stats — it must be recreated when fan_stats
	// is recreated, even though its own shape is unchanged.
	fanDetailSeason := makeComposite("public", "fan_detail_season",
		"season_id", "integer",
		"stats", "fan_stats",
	)
	// transitively, anything nesting fan_detail_season also gets pulled in
	fanDetailGameTeam := makeComposite("public", "fan_detail_game_team",
		"detail", "fan_detail_season",
	)
	// unrelated composite — must NOT be in the closure
	unrelated := makeComposite("public", "unrelated", "x", "integer")

	types := map[string]*ir.Type{
		"public.fan_stats":             newFanStats,
		"public.fan_detail_season":     fanDetailSeason,
		"public.fan_detail_game_team":  fanDetailGameTeam,
		"public.unrelated":             unrelated,
	}
	diffs := []*typeDiff{{Old: oldFanStats, New: newFanStats}}

	typeCtx, _, _ := findDependentObjectsForRecreatedTypes(types, nil, nil, diffs)

	if len(typeCtx.types) != 3 {
		t.Fatalf("expected 3 types in closure (fan_stats + 2 nested), got %d", len(typeCtx.types))
	}

	got := make(map[string]bool)
	for _, tt := range typeCtx.types {
		got[tt.Schema+"."+tt.Name] = true
	}
	for _, want := range []string{"public.fan_stats", "public.fan_detail_season", "public.fan_detail_game_team"} {
		if !got[want] {
			t.Errorf("missing %s from closure", want)
		}
	}
	if got["public.unrelated"] {
		t.Errorf("unrelated should not be in closure")
	}

	// The changed set is just fan_stats — the others are cascade-only.
	if _, ok := typeCtx.changed["public.fan_stats"]; !ok {
		t.Errorf("fan_stats should be in changed set")
	}
	if _, ok := typeCtx.changed["public.fan_detail_season"]; ok {
		t.Errorf("fan_detail_season should NOT be in changed set (cascade-only)")
	}

	// CREATE order: fan_stats must come before fan_detail_season, which must
	// come before fan_detail_game_team.
	pos := make(map[string]int)
	for i, tt := range typeCtx.types {
		pos[tt.Schema+"."+tt.Name] = i
	}
	if pos["public.fan_stats"] >= pos["public.fan_detail_season"] {
		t.Errorf("fan_stats should be created before fan_detail_season; got positions %d vs %d",
			pos["public.fan_stats"], pos["public.fan_detail_season"])
	}
	if pos["public.fan_detail_season"] >= pos["public.fan_detail_game_team"] {
		t.Errorf("fan_detail_season should be created before fan_detail_game_team; got positions %d vs %d",
			pos["public.fan_detail_season"], pos["public.fan_detail_game_team"])
	}
}
