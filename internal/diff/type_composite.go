package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tysen/pgschema/ir"
)

// dependentColumn pairs a table with a column whose data type is a composite
// being recreated. The column has to be dropped before the composite DROP and
// re-added (with the new shape) after the composite CREATE.
type dependentColumn struct {
	Table  *ir.Table
	Column *ir.Column
	// CompositeKey is the schema.name of the composite type whose recreate
	// pulled this column in. Used by the warning text.
	CompositeKey string
}

// dependentColumnsContext tracks table columns that have to be dropped and
// re-added around a composite type recreation.
//
// Unlike the per-composite mapping used internally by findDependentObjects-
// ForRecreatedTypes, the public surface (GetAll) returns a flat, deduplicated
// list. The composite-recreate emitter operates on the closure as a single
// block — there is no per-composite emission — so a flat list is the right
// shape.
type dependentColumnsContext struct {
	all []dependentColumn
}

func newDependentColumnsContext() *dependentColumnsContext {
	return &dependentColumnsContext{}
}

// GetAll returns all dependent columns across the recreate closure, sorted
// for deterministic output (schema, table, column).
func (ctx *dependentColumnsContext) GetAll() []dependentColumn {
	if ctx == nil {
		return nil
	}
	return ctx.all
}

// dependentTypesContext tracks the full closure of composite types that need
// DROP+CREATE because they themselves change OR because they have an
// attribute typed as a recreating composite (transitively).
type dependentTypesContext struct {
	// types holds the closure, in dependency order: types that depend on
	// nothing else in the closure first, types that depend on others last.
	// CREATE order. DROP runs the slice in reverse.
	types []*ir.Type
	// changed is the set of type keys (schema.name) whose own shape is
	// changing. Used by the warning text to distinguish "shape change" from
	// "cascade-only".
	changed map[string]struct{}
	// changedDiffs maps changed type keys to the underlying typeDiff (for
	// reorder detection in warnings).
	changedDiffs map[string]*typeDiff
}

func newDependentTypesContext() *dependentTypesContext {
	return &dependentTypesContext{
		changed:      make(map[string]struct{}),
		changedDiffs: make(map[string]*typeDiff),
	}
}

// IsEmpty reports whether the closure is empty (no composite recreate).
func (ctx *dependentTypesContext) IsEmpty() bool {
	return ctx == nil || len(ctx.types) == 0
}

// findDependentObjectsForRecreatedTypes computes the full cascade for any
// composite types whose shape is changing. The cascade includes:
//
//   - The transitive closure of composite types that need DROP+CREATE because
//     they themselves change shape OR have an attribute typed as a recreating
//     composite (e.g., `fan_detail_season AS (..., stats fan_stats, ...)` has
//     to be recreated when `fan_stats` is recreated, even though
//     `fan_detail_season`'s attribute list itself didn't change).
//   - All table columns whose type is any composite in the closure.
//   - All regular views that reference any table holding such a column or
//     mention any composite in the closure by name. Includes transitive
//     view-on-view dependencies.
//
// Materialized views are NOT included in viewCtx — they're handled via the
// pre-drop phase and the existing matview modify pipeline.
//
// allNewTypes/allNewTables/allNewViews are keyed by "schema.name".
func findDependentObjectsForRecreatedTypes(
	allNewTypes map[string]*ir.Type,
	allNewTables map[string]*ir.Table,
	allNewViews map[string]*ir.View,
	modifiedTypes []*typeDiff,
) (*dependentTypesContext, *dependentColumnsContext, *dependentViewsContext) {
	typeCtx := newDependentTypesContext()
	colCtx := newDependentColumnsContext()
	viewCtx := newDependentViewsContext()

	// Seed the closure with composites whose shape is changing.
	closure := make(map[string]*ir.Type)
	for _, td := range modifiedTypes {
		if td.Old == nil || td.New == nil {
			continue
		}
		if td.Old.Kind != ir.TypeKindComposite || td.New.Kind != ir.TypeKindComposite {
			continue
		}
		key := td.New.Schema + "." + td.New.Name
		closure[key] = td.New
		typeCtx.changed[key] = struct{}{}
		typeCtx.changedDiffs[key] = td
	}
	if len(closure) == 0 {
		return typeCtx, colCtx, viewCtx
	}

	// Expand: any composite (in the new state) whose attributes reference a
	// type already in the closure must also be recreated. Iterate to fixed
	// point — composite-of-composite-of-composite chains converge in O(depth)
	// passes.
	for {
		grew := false
		for key, typeObj := range allNewTypes {
			if typeObj == nil || typeObj.Kind != ir.TypeKindComposite {
				continue
			}
			if _, ok := closure[key]; ok {
				continue
			}
			if compositeReferencesAnyInClosure(typeObj, closure) {
				closure[key] = typeObj
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	// Topologically sort the closure for CREATE order. typeReferencesType
	// (used by topologicallySortTypes) returns true when typeA's attributes
	// reference typeB, so that helper sorts referenced-before-referencing.
	allInClosure := make([]*ir.Type, 0, len(closure))
	for _, t := range closure {
		allInClosure = append(allInClosure, t)
	}
	typeCtx.types = topologicallySortTypes(allInClosure)

	// Collect table columns whose data type matches any composite in the
	// closure. Iterate in deterministic order.
	tableKeys := make([]string, 0, len(allNewTables))
	for k := range allNewTables {
		tableKeys = append(tableKeys, k)
	}
	sort.Strings(tableKeys)
	seenCol := make(map[string]bool)
	for _, tk := range tableKeys {
		table := allNewTables[tk]
		if table == nil {
			continue
		}
		for _, col := range table.Columns {
			for ck, ct := range closure {
				if columnReferencesCompositeType(col, ct.Schema, ct.Name) {
					colKey := tk + "." + col.Name
					if seenCol[colKey] {
						continue
					}
					seenCol[colKey] = true
					colCtx.all = append(colCtx.all, dependentColumn{
						Table:        table,
						Column:       col,
						CompositeKey: ck,
					})
					break
				}
			}
		}
	}
	sort.Slice(colCtx.all, func(i, j int) bool {
		a, b := colCtx.all[i], colCtx.all[j]
		if a.Table.Schema != b.Table.Schema {
			return a.Table.Schema < b.Table.Schema
		}
		if a.Table.Name != b.Table.Name {
			return a.Table.Name < b.Table.Name
		}
		return a.Column.Name < b.Column.Name
	})

	// Collect dependent regular views: views that mention any composite in
	// the closure by name, or that query any table with a column in the
	// closure. Then expand transitively.
	dependentTableKeys := make(map[string]struct{})
	for _, dc := range colCtx.all {
		dependentTableKeys[dc.Table.Schema+"."+dc.Table.Name] = struct{}{}
	}

	direct := make([]*ir.View, 0)
	seenView := make(map[string]bool)
	viewKeys := make([]string, 0, len(allNewViews))
	for k := range allNewViews {
		viewKeys = append(viewKeys, k)
	}
	sort.Strings(viewKeys)
	for _, vk := range viewKeys {
		view := allNewViews[vk]
		if view == nil || view.Materialized {
			continue
		}
		if seenView[vk] {
			continue
		}
		matched := false
		for _, ct := range closure {
			if viewDependsOnView(view, ct.Name) || viewDependsOnView(view, ct.Schema+"."+ct.Name) {
				matched = true
				break
			}
		}
		if !matched {
			for tk := range dependentTableKeys {
				schemaName := strings.SplitN(tk, ".", 2)
				if len(schemaName) != 2 {
					continue
				}
				if viewDependsOnTable(view, schemaName[0], schemaName[1]) {
					matched = true
					break
				}
			}
		}
		if matched {
			direct = append(direct, view)
			seenView[vk] = true
		}
	}

	if len(direct) > 0 {
		all := findTransitiveDependents(direct, allNewViews, nil)
		nonMatview := make([]*ir.View, 0, len(all))
		for _, v := range all {
			if v == nil || v.Materialized {
				continue
			}
			nonMatview = append(nonMatview, v)
		}
		sorted := topologicallySortViews(nonMatview)
		// Store under a single sentinel key — the emitter operates on the
		// flat list, not per-composite.
		viewCtx.dependents[compositeRecreateBlockKey] = sorted
	}

	return typeCtx, colCtx, viewCtx
}

// compositeRecreateBlockKey is the sentinel key the composite-recreate
// emitter uses to store and retrieve dependent views. Picked to be
// unambiguously not a real schema.name.
const compositeRecreateBlockKey = "__pgschema_composite_recreate_block__"

// compositeReferencesAnyInClosure reports whether any of typeObj's composite
// attributes reference a type already marked for recreate.
func compositeReferencesAnyInClosure(typeObj *ir.Type, closure map[string]*ir.Type) bool {
	for _, attr := range typeObj.Columns {
		for _, ct := range closure {
			if dataTypeMatches(attr.DataType, ct.Schema, ct.Name) {
				return true
			}
		}
	}
	return false
}

// dataTypeMatches reports whether a column DataType string refers to the
// named type. Lifted from columnReferencesCompositeType so the composite
// closure expansion doesn't need to wrap attributes in an ir.Column.
func dataTypeMatches(dt, typeSchema, typeName string) bool {
	dt = strings.TrimSpace(dt)
	if dt == "" {
		return false
	}
	dt = strings.TrimSuffix(dt, "[]")
	dt = strings.TrimSpace(dt)
	if dt == typeSchema+"."+typeName || dt == typeName {
		return true
	}
	dtLower := strings.ToLower(dt)
	if dtLower == strings.ToLower(typeSchema+"."+typeName) || dtLower == strings.ToLower(typeName) {
		return true
	}
	return false
}

// columnReferencesCompositeType reports whether a column's declared data type
// is the named composite. Public alias for dataTypeMatches scoped to columns.
func columnReferencesCompositeType(col *ir.Column, typeSchema, typeName string) bool {
	if col == nil {
		return false
	}
	return dataTypeMatches(col.DataType, typeSchema, typeName)
}

// generateCompositeRecreateBlock emits SQL for the entire composite-recreate
// closure. The order is:
//
//  1. WARNING comment summarizing destructive consequences.
//  2. DROP each dependent regular view (reverse topo order).
//  3. DROP each dependent table column.
//  4. DROP each composite in reverse topo order (dependents go first).
//  5. CREATE each composite in topo order (referenced-first).
//  6. ADD COLUMN to each table that lost a column in step 3.
//  7. CREATE each dependent regular view (topo order).
//
// Materialized views that depend on the closure are pre-dropped in
// generatePreDropMaterializedViewsSQL and recreated via the existing matview
// modify pipeline.
//
// Called once per migration, regardless of how many composites are in the
// closure.
func generateCompositeRecreateBlock(
	typeCtx *dependentTypesContext,
	colCtx *dependentColumnsContext,
	viewCtx *dependentViewsContext,
	targetSchema string,
	collector *diffCollector,
) {
	if typeCtx.IsEmpty() {
		return
	}

	dependentColumns := colCtx.GetAll()
	dependentViews := viewCtx.GetDependents(compositeRecreateBlockKey)

	// Use the first changed composite's diff as the warning's "source"
	// reference — needed for the DiffSource interface contract. Pick
	// deterministically.
	var primary *typeDiff
	primaryKey := ""
	for k := range typeCtx.changedDiffs {
		if primaryKey == "" || k < primaryKey {
			primaryKey = k
		}
	}
	if primaryKey != "" {
		primary = typeCtx.changedDiffs[primaryKey]
	}

	path := primaryKey
	if primary != nil {
		path = fmt.Sprintf("%s.%s", primary.New.Schema, primary.New.Name)
	}

	// 1. Warning banner.
	warning := buildCompositeRecreateBlockWarning(typeCtx, dependentColumns, dependentViews)
	if warning != "" {
		warningCtx := &diffContext{
			Type:                DiffTypeType,
			Operation:           DiffOperationRecreate,
			Path:                path,
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(warningCtx, warning)
	}

	// 2. Drop dependent views (reverse topo).
	for i := len(dependentViews) - 1; i >= 0; i-- {
		v := dependentViews[i]
		viewName := qualifyEntityName(v.Schema, v.Name, targetSchema)
		ctx := &diffContext{
			Type:                DiffTypeView,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s", v.Schema, v.Name),
			Source:              v,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, fmt.Sprintf("DROP VIEW IF EXISTS %s RESTRICT;", viewName))
	}

	// 3. Drop dependent table columns.
	for _, dc := range dependentColumns {
		tableName := getTableNameWithSchema(dc.Table.Schema, dc.Table.Name, targetSchema)
		ctx := &diffContext{
			Type:                DiffTypeTable,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s.%s", dc.Table.Schema, dc.Table.Name, dc.Column.Name),
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tableName, ir.QuoteIdentifier(dc.Column.Name)))
	}

	// 4. DROP TYPEs in reverse topo (dependents first).
	for i := len(typeCtx.types) - 1; i >= 0; i-- {
		t := typeCtx.types[i]
		typeName := qualifyEntityName(t.Schema, t.Name, targetSchema)
		ctx := &diffContext{
			Type:                DiffTypeType,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s", t.Schema, t.Name),
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, fmt.Sprintf("DROP TYPE IF EXISTS %s RESTRICT;", typeName))
	}

	// 5. CREATE TYPEs in topo order.
	for _, t := range typeCtx.types {
		ctx := &diffContext{
			Type:                DiffTypeType,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s", t.Schema, t.Name),
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, generateTypeSQL(t, targetSchema))
	}

	// 6. Re-add table columns. NOT NULL is restored unconditionally — see
	// buildAddColumnSQL.
	for _, dc := range dependentColumns {
		tableName := getTableNameWithSchema(dc.Table.Schema, dc.Table.Name, targetSchema)
		columnSQL := buildAddColumnSQL(dc, targetSchema)
		ctx := &diffContext{
			Type:                DiffTypeTable,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s.%s", dc.Table.Schema, dc.Table.Name, dc.Column.Name),
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", tableName, columnSQL))
	}

	// 7. Recreate dependent views in topo order.
	for _, v := range dependentViews {
		ctx := &diffContext{
			Type:                DiffTypeView,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s", v.Schema, v.Name),
			Source:              v,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, generateViewSQL(v, targetSchema))
	}
}

// buildAddColumnSQL produces the column body for an ADD COLUMN statement
// re-adding a column whose composite type was recreated. Restores structural
// attributes from the new IR. NOT NULL is restored unconditionally — adding
// it to a non-empty table without a default fails at apply, and the
// transaction rolls back, which is the right outcome for a tool that exists
// to surface schema mismatches. The pre-emitted warning tells users to add a
// DEFAULT or use the migrate-using directive when their tables have data.
func buildAddColumnSQL(dc dependentColumn, targetSchema string) string {
	col := dc.Column
	dataType := stripSchemaPrefix(col.DataType, targetSchema)
	parts := []string{ir.QuoteIdentifier(col.Name), dataType}

	if col.DefaultValue != nil && *col.DefaultValue != "" {
		parts = append(parts, "DEFAULT "+*col.DefaultValue)
	}

	if !col.IsNullable {
		parts = append(parts, "NOT NULL")
	}

	return strings.Join(parts, " ")
}

// buildCompositeRecreateBlockWarning summarizes the destructive consequences
// of a composite-recreate cascade. Returns "" if the cascade is empty.
func buildCompositeRecreateBlockWarning(
	typeCtx *dependentTypesContext,
	cols []dependentColumn,
	views []*ir.View,
) string {
	if typeCtx.IsEmpty() {
		return ""
	}

	var parts []string

	// Decide between "shape change" and "reorder only" based on the changed
	// composites. If any changed composite has any non-reorder difference, we
	// describe the block as a shape change. Otherwise, all changed composites
	// are reorder-only.
	allReorder := true
	for _, td := range typeCtx.changedDiffs {
		if !isCompositeReorderOnly(td.Old, td.New) {
			allReorder = false
			break
		}
	}
	if allReorder {
		parts = append(parts, "WARNING: composite attribute reorder requires DROP+CREATE in Postgres (no in-place reorder).")
	} else {
		parts = append(parts, "WARNING: composite type shape change; DROP+CREATE is the only safe path.")
	}

	// Call out cascade-only types (recreated because they nest a changing
	// composite, not because their own shape changed).
	cascadeOnly := make([]string, 0)
	for _, t := range typeCtx.types {
		key := t.Schema + "." + t.Name
		if _, ok := typeCtx.changed[key]; !ok {
			cascadeOnly = append(cascadeOnly, key)
		}
	}
	if len(cascadeOnly) > 0 {
		sort.Strings(cascadeOnly)
		parts = append(parts, fmt.Sprintf("composite types %s are also being DROP+CREATEd because they nest a changing composite as an attribute.", strings.Join(cascadeOnly, ", ")))
	}

	for _, dc := range cols {
		colDesc := fmt.Sprintf("%s.%s.%s", dc.Table.Schema, dc.Table.Name, dc.Column.Name)
		if !dc.Column.IsNullable && (dc.Column.DefaultValue == nil || *dc.Column.DefaultValue == "") {
			parts = append(parts, fmt.Sprintf("column %s is being dropped+re-added with NOT NULL but no DEFAULT; on a non-empty table this will fail at apply. Add a DEFAULT or use a migrate-using directive if data exists.", colDesc))
		} else {
			parts = append(parts, fmt.Sprintf("column %s data will be lost", colDesc))
		}
	}

	for _, v := range views {
		parts = append(parts, fmt.Sprintf("view %s.%s will be DROP+CREATEd; any view body that destructures composite fields removed in this change will fail to recreate.", v.Schema, v.Name))
	}

	parts = append(parts, "Function/procedure signatures referencing recreated composites are not detected; if any exist, apply will fail with a clear PG error.")

	return "-- " + strings.Join(parts, "\n-- ")
}

// isCompositeReorderOnly reports whether the only difference between two
// composite shapes is attribute ordering — i.e. the (name, type) sets match.
func isCompositeReorderOnly(oldType, newType *ir.Type) bool {
	if oldType == nil || newType == nil {
		return false
	}
	if len(oldType.Columns) != len(newType.Columns) {
		return false
	}
	oldByName := make(map[string]string, len(oldType.Columns))
	for _, col := range oldType.Columns {
		oldByName[col.Name] = col.DataType
	}
	for _, col := range newType.Columns {
		t, ok := oldByName[col.Name]
		if !ok || t != col.DataType {
			return false
		}
	}
	return true
}
