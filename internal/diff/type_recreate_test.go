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

func TestColumnReferencesType(t *testing.T) {
	tests := []struct {
		dataType string
		schema   string
		typeName string
		want     bool
	}{
		{"widget_metrics", "public", "widget_metrics", true},
		{"public.widget_metrics", "public", "widget_metrics", true},
		{"widget_metrics[]", "public", "widget_metrics", true},
		{"PUBLIC.Widget_Metrics", "public", "widget_metrics", true},
		{"team_widget_overview", "public", "widget_metrics", false},
		{"", "public", "widget_metrics", false},
		{"other.widget_metrics", "public", "widget_metrics", false},
	}
	for _, tc := range tests {
		col := &ir.Column{Name: "x", DataType: tc.dataType}
		got := columnReferencesType(col, tc.schema, tc.typeName)
		if got != tc.want {
			t.Errorf("columnReferencesType(%q, %q, %q) = %v, want %v",
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
	composite := makeComposite("public", "widget_metrics",
		"a", "integer",
	)
	newComposite := makeComposite("public", "widget_metrics",
		"b", "integer",
	)
	table := &ir.Table{
		Schema: "public",
		Name:   "widgets",
		Columns: []*ir.Column{
			{Name: "id", DataType: "integer"},
			{Name: "detail", DataType: "widget_metrics"},
		},
	}
	types := map[string]*ir.Type{"public.widget_metrics": newComposite}
	tables := map[string]*ir.Table{"public.widgets": table}
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
	if got[0].Column.Name != "detail" || got[0].Table.Name != "widgets" {
		t.Errorf("unexpected dependent column: %+v", got[0])
	}
	if len(viewCtx.GetDependents(typeRecreateBlockKey)) != 0 {
		t.Errorf("expected no dependent views")
	}
}

func TestFindDependentObjectsForRecreatedTypes_ViewDependents(t *testing.T) {
	composite := makeComposite("public", "widget_metrics", "a", "integer")
	newComposite := makeComposite("public", "widget_metrics", "b", "integer")
	table := &ir.Table{
		Schema: "public",
		Name:   "widgets",
		Columns: []*ir.Column{
			{Name: "detail", DataType: "widget_metrics"},
		},
	}
	leafView := &ir.View{
		Schema:     "public",
		Name:       "widget_summary",
		Definition: "SELECT detail FROM widgets",
	}
	transitiveView := &ir.View{
		Schema:     "public",
		Name:       "widget_overview",
		Definition: "SELECT count(*) FROM widget_summary",
	}
	types := map[string]*ir.Type{"public.widget_metrics": newComposite}
	tables := map[string]*ir.Table{"public.widgets": table}
	views := map[string]*ir.View{
		"public.widget_summary":  leafView,
		"public.widget_overview": transitiveView,
	}
	diffs := []*typeDiff{{Old: composite, New: newComposite}}

	_, colCtx, viewCtx := findDependentObjectsForRecreatedTypes(types, tables, views, diffs)

	if len(colCtx.GetAll()) != 1 {
		t.Errorf("expected 1 dependent column")
	}
	deps := viewCtx.GetDependents(typeRecreateBlockKey)
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependent views (direct + transitive), got %d", len(deps))
	}
	// Topo order: leaf first, dependent of leaf second.
	if deps[0].Name != "widget_summary" || deps[1].Name != "widget_overview" {
		t.Errorf("unexpected topo order: %s, %s", deps[0].Name, deps[1].Name)
	}
}

func TestFindDependentObjectsForRecreatedTypes_NoMatviewsInViewCtx(t *testing.T) {
	composite := makeComposite("public", "widget_metrics", "a", "integer")
	newComposite := makeComposite("public", "widget_metrics", "b", "integer")
	table := &ir.Table{
		Schema: "public",
		Name:   "widgets",
		Columns: []*ir.Column{
			{Name: "detail", DataType: "widget_metrics"},
		},
	}
	matview := &ir.View{
		Schema:       "public",
		Name:         "widget_summary_mv",
		Materialized: true,
		Definition:   "SELECT detail FROM widgets",
	}
	types := map[string]*ir.Type{"public.widget_metrics": newComposite}
	tables := map[string]*ir.Table{"public.widgets": table}
	views := map[string]*ir.View{
		"public.widget_summary_mv": matview,
	}
	diffs := []*typeDiff{{Old: composite, New: newComposite}}

	_, _, viewCtx := findDependentObjectsForRecreatedTypes(types, tables, views, diffs)
	if len(viewCtx.GetDependents(typeRecreateBlockKey)) != 0 {
		t.Errorf("matviews should be filtered from the view-cascade context (handled in pre-drop instead)")
	}
}

// TestFindDependentObjectsForRecreatedTypes_NestedClosure verifies the
// composite-of-composite recursion: when point_data changes shape, types that
// have point_data as an attribute (like region_data) must also be
// pulled into the recreate closure even though their own shape didn't
// change.
func TestFindDependentObjectsForRecreatedTypes_NestedClosure(t *testing.T) {
	oldFanStats := makeComposite("public", "point_data", "score_a", "real")
	newFanStats := makeComposite("public", "point_data", "score_a", "real", "score_b", "real")
	// region_data nests point_data — it must be recreated when point_data
	// is recreated, even though its own shape is unchanged.
	fanDetailSeason := makeComposite("public", "region_data",
		"region_id", "integer",
		"stats", "point_data",
	)
	// transitively, anything nesting region_data also gets pulled in
	fanDetailGameTeam := makeComposite("public", "report_data",
		"detail", "region_data",
	)
	// unrelated composite — must NOT be in the closure
	unrelated := makeComposite("public", "unrelated", "x", "integer")

	types := map[string]*ir.Type{
		"public.point_data":             newFanStats,
		"public.region_data":     fanDetailSeason,
		"public.report_data":  fanDetailGameTeam,
		"public.unrelated":             unrelated,
	}
	diffs := []*typeDiff{{Old: oldFanStats, New: newFanStats}}

	typeCtx, _, _ := findDependentObjectsForRecreatedTypes(types, nil, nil, diffs)

	if len(typeCtx.types) != 3 {
		t.Fatalf("expected 3 types in closure (point_data + 2 nested), got %d", len(typeCtx.types))
	}

	got := make(map[string]bool)
	for _, tt := range typeCtx.types {
		got[tt.Schema+"."+tt.Name] = true
	}
	for _, want := range []string{"public.point_data", "public.region_data", "public.report_data"} {
		if !got[want] {
			t.Errorf("missing %s from closure", want)
		}
	}
	if got["public.unrelated"] {
		t.Errorf("unrelated should not be in closure")
	}

	// The changed set is just point_data — the others are cascade-only.
	if _, ok := typeCtx.changed["public.point_data"]; !ok {
		t.Errorf("point_data should be in changed set")
	}
	if _, ok := typeCtx.changed["public.region_data"]; ok {
		t.Errorf("region_data should NOT be in changed set (cascade-only)")
	}

	// CREATE order: point_data must come before region_data, which must
	// come before report_data.
	pos := make(map[string]int)
	for i, tt := range typeCtx.types {
		pos[tt.Schema+"."+tt.Name] = i
	}
	if pos["public.point_data"] >= pos["public.region_data"] {
		t.Errorf("point_data should be created before region_data; got positions %d vs %d",
			pos["public.point_data"], pos["public.region_data"])
	}
	if pos["public.region_data"] >= pos["public.report_data"] {
		t.Errorf("region_data should be created before report_data; got positions %d vs %d",
			pos["public.region_data"], pos["public.report_data"])
	}
}
