package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tysen/pgschema/ir"
)

// dependentColumn pairs a table with a column whose data type is a type
// being recreated (composite, enum, or domain over a recreated type). The
// column has to be dropped before the type DROP and re-added (with the new
// shape) after the type CREATE.
type dependentColumn struct {
	Table  *ir.Table
	Column *ir.Column
	// TypeKey is the schema.name of the type whose recreate pulled this
	// column in. Used by the warning text.
	TypeKey string
}

// dependentColumnsContext tracks table columns that have to be dropped and
// re-added around a type recreation.
//
// Unlike the per-type mapping used internally by findDependentObjects-
// ForRecreatedTypes, the public surface (GetAll) returns a flat, deduplicated
// list. The recreate emitter operates on the closure as a single block —
// there is no per-type emission — so a flat list is the right shape.
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

// dependentTypesContext tracks the full closure of types that need
// DROP+CREATE: composites whose shape changed, enums whose values changed
// non-additively, composites that nest a recreating type as an attribute
// (transitively), and domains whose base type is a recreating type.
type dependentTypesContext struct {
	// types holds the closure, in dependency order: types that depend on
	// nothing else in the closure first, types that depend on others last.
	// CREATE order. DROP runs the slice in reverse.
	types []*ir.Type
	// changed is the set of type keys (schema.name) whose own definition
	// is changing (composite shape change or enum non-additive change).
	// Used by the warning text to distinguish "self change" from
	// "cascade-only".
	changed map[string]struct{}
	// changedDiffs maps changed type keys to the underlying typeDiff (for
	// reorder/value-removal detection in warnings).
	changedDiffs map[string]*typeDiff
}

func newDependentTypesContext() *dependentTypesContext {
	return &dependentTypesContext{
		changed:      make(map[string]struct{}),
		changedDiffs: make(map[string]*typeDiff),
	}
}

// IsEmpty reports whether the closure is empty (no recreate needed).
func (ctx *dependentTypesContext) IsEmpty() bool {
	return ctx == nil || len(ctx.types) == 0
}

// computeRecreatedTypeClosure builds the full set of types that must be
// DROP+CREATEd for a given set of typeDiffs. The closure includes:
//
//   - Each typeDiff that satisfies typeNeedsRecreate (composite shape change
//     or enum non-additive change). These are the "seeds".
//   - The transitive closure of composites whose attributes reference any
//     type already in the closure (e.g., `region_data AS (..., stats
//     point_data, ...)` is recreated when `point_data` is recreated, even
//     though `region_data`'s attribute list itself is unchanged).
//   - Any domain whose base type names a closure member. Required because
//     `typesEqual` compares `BaseType` as a string and sees no change when
//     the underlying enum/composite is recreated, so the domain would
//     otherwise be missed.
//
// Returns the unsorted closure keyed by "schema.name" plus a parallel map
// of seed-only keys (used to populate dependentTypesContext.changed).
func computeRecreatedTypeClosure(
	allNewTypes map[string]*ir.Type,
	modifiedTypes []*typeDiff,
) (closure map[string]*ir.Type, seeds map[string]*typeDiff) {
	closure = make(map[string]*ir.Type)
	seeds = make(map[string]*typeDiff)

	for _, td := range modifiedTypes {
		if !typeNeedsRecreate(td) {
			continue
		}
		key := td.New.Schema + "." + td.New.Name
		closure[key] = td.New
		seeds[key] = td
	}
	if len(closure) == 0 {
		return closure, seeds
	}

	// Expand to fixed point. Two parallel rules:
	//   - composite whose attribute references any closure member
	//   - domain whose base type names a closure member
	for {
		grew := false
		for key, typeObj := range allNewTypes {
			if typeObj == nil {
				continue
			}
			if _, ok := closure[key]; ok {
				continue
			}
			switch typeObj.Kind {
			case ir.TypeKindComposite:
				if compositeReferencesAnyInClosure(typeObj, closure) {
					closure[key] = typeObj
					grew = true
				}
			case ir.TypeKindDomain:
				if domainBaseTypeInClosure(typeObj, closure) {
					closure[key] = typeObj
					grew = true
				}
			}
		}
		if !grew {
			break
		}
	}

	return closure, seeds
}

// findDependentObjectsForRecreatedTypes computes the full cascade for any
// types being recreated (composite shape change or enum non-additive change).
// The cascade includes:
//
//   - The transitive type closure (see computeRecreatedTypeClosure).
//   - All table columns whose type is any type in the closure.
//   - All regular views that reference any table holding such a column or
//     mention any type in the closure by name. Includes transitive
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

	closure, seeds := computeRecreatedTypeClosure(allNewTypes, modifiedTypes)
	if len(closure) == 0 {
		return typeCtx, colCtx, viewCtx
	}
	for key, td := range seeds {
		typeCtx.changed[key] = struct{}{}
		typeCtx.changedDiffs[key] = td
	}

	// Topologically sort the closure for CREATE order. topologicallySortTypes
	// orders referenced-before-referencing for both composites (via attribute
	// references) and domains (via base type).
	allInClosure := make([]*ir.Type, 0, len(closure))
	for _, t := range closure {
		allInClosure = append(allInClosure, t)
	}
	typeCtx.types = topologicallySortTypes(allInClosure)

	// Collect table columns whose data type matches any type in the
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
				if columnReferencesType(col, ct.Schema, ct.Name) {
					colKey := tk + "." + col.Name
					if seenCol[colKey] {
						continue
					}
					seenCol[colKey] = true
					colCtx.all = append(colCtx.all, dependentColumn{
						Table:   table,
						Column:  col,
						TypeKey: ck,
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
		// flat list, not per-type.
		viewCtx.dependents[typeRecreateBlockKey] = sorted
	}

	return typeCtx, colCtx, viewCtx
}

// typeRecreateBlockKey is the sentinel key the type-recreate emitter uses to
// store and retrieve dependent views. Picked to be unambiguously not a real
// schema.name.
const typeRecreateBlockKey = "__pgschema_type_recreate_block__"

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

// domainBaseTypeInClosure reports whether typeObj is a domain whose base type
// names a closure member. typesEqual compares BaseType as a string and sees
// no change when the underlying composite/enum is recreated, so the closure
// expansion has to pull domains in explicitly.
func domainBaseTypeInClosure(typeObj *ir.Type, closure map[string]*ir.Type) bool {
	if typeObj == nil || typeObj.Kind != ir.TypeKindDomain {
		return false
	}
	for _, ct := range closure {
		if dataTypeMatches(typeObj.BaseType, ct.Schema, ct.Name) {
			return true
		}
	}
	return false
}

// dataTypeMatches reports whether a column DataType string refers to the
// named type. Lifted from columnReferencesType so the composite closure
// expansion doesn't need to wrap attributes in an ir.Column.
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

// columnReferencesType reports whether a column's declared data type is the
// named type (composite, enum, or domain). Wraps dataTypeMatches.
func columnReferencesType(col *ir.Column, typeSchema, typeName string) bool {
	if col == nil {
		return false
	}
	return dataTypeMatches(col.DataType, typeSchema, typeName)
}

// generateTypeRecreateBlock emits SQL for the entire type-recreate closure
// (composites, enums, domains over recreated types). The order is:
//
//  1. WARNING comment summarizing destructive consequences.
//  2. DROP each dependent regular view (reverse topo order).
//  3. DROP each dependent table column.
//  4. DROP each type in reverse topo order (dependents go first).
//     Domains use DROP DOMAIN; everything else uses DROP TYPE.
//  5. CREATE each type in topo order (referenced-first).
//  6. ADD COLUMN to each table that lost a column in step 3.
//  7. CREATE each dependent regular view (topo order).
//
// Materialized views that depend on the closure are pre-dropped in
// generatePreDropMaterializedViewsSQL and recreated via the existing matview
// modify pipeline.
//
// Called once per migration, regardless of how many types are in the closure.
func generateTypeRecreateBlock(
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
	dependentViews := viewCtx.GetDependents(typeRecreateBlockKey)

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
	warning := buildTypeRecreateBlockWarning(typeCtx, dependentColumns, dependentViews)
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

	// 4. DROP each type in reverse topo (dependents first). Domains use
	//    DROP DOMAIN; composites and enums use DROP TYPE.
	for i := len(typeCtx.types) - 1; i >= 0; i-- {
		t := typeCtx.types[i]
		typeName := qualifyEntityName(t.Schema, t.Name, targetSchema)
		diffType := DiffTypeType
		dropKeyword := "TYPE"
		if t.Kind == ir.TypeKindDomain {
			diffType = DiffTypeDomain
			dropKeyword = "DOMAIN"
		}
		ctx := &diffContext{
			Type:                diffType,
			Operation:           DiffOperationRecreate,
			Path:                fmt.Sprintf("%s.%s", t.Schema, t.Name),
			Source:              primary,
			CanRunInTransaction: true,
		}
		collector.collect(ctx, fmt.Sprintf("DROP %s IF EXISTS %s RESTRICT;", dropKeyword, typeName))
	}

	// 5. CREATE each type in topo order.
	for _, t := range typeCtx.types {
		diffType := DiffTypeType
		if t.Kind == ir.TypeKindDomain {
			diffType = DiffTypeDomain
		}
		ctx := &diffContext{
			Type:                diffType,
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
// re-adding a column whose type was recreated. Restores structural
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

// buildTypeRecreateBlockWarning summarizes the destructive consequences of a
// type-recreate cascade (composite shape change, enum non-additive change,
// domain over recreated base). Returns "" if the cascade is empty.
func buildTypeRecreateBlockWarning(
	typeCtx *dependentTypesContext,
	cols []dependentColumn,
	views []*ir.View,
) string {
	if typeCtx.IsEmpty() {
		return ""
	}

	var parts []string

	// Headline. Pick wording based on the kinds present in the changed set.
	hasComposite := false
	hasEnum := false
	allCompositeReorder := true
	for _, td := range typeCtx.changedDiffs {
		switch td.New.Kind {
		case ir.TypeKindComposite:
			hasComposite = true
			if !isCompositeReorderOnly(td.Old, td.New) {
				allCompositeReorder = false
			}
		case ir.TypeKindEnum:
			hasEnum = true
			allCompositeReorder = false
		}
	}
	switch {
	case hasComposite && hasEnum:
		parts = append(parts, "WARNING: type recreate; DROP+CREATE is the only safe path for the changes in this block.")
	case hasComposite && allCompositeReorder:
		parts = append(parts, "WARNING: composite attribute reorder requires DROP+CREATE in Postgres (no in-place reorder).")
	case hasComposite:
		parts = append(parts, "WARNING: composite type shape change; DROP+CREATE is the only safe path.")
	case hasEnum:
		parts = append(parts, "WARNING: enum value removal/reorder requires DROP+CREATE in Postgres (no DROP VALUE / no reorder).")
	}

	// Per-enum: list values being removed. Apply will fail if any data row
	// holds a removed value.
	enumKeys := make([]string, 0)
	for k, td := range typeCtx.changedDiffs {
		if td.New.Kind == ir.TypeKindEnum {
			enumKeys = append(enumKeys, k)
		}
	}
	sort.Strings(enumKeys)
	for _, k := range enumKeys {
		td := typeCtx.changedDiffs[k]
		removed := enumValuesRemoved(td.Old, td.New)
		if len(removed) > 0 {
			parts = append(parts, fmt.Sprintf("enum %s drops values [%s]; any row holding one of these values will block apply (DROP TYPE … RESTRICT fails).", k, strings.Join(quoteAll(removed), ", ")))
		}
	}

	// Cascade-only types: in the closure but not seeds. Split by kind so the
	// warning text is precise.
	cascadeComposites := make([]string, 0)
	cascadeDomains := make([]string, 0)
	for _, t := range typeCtx.types {
		key := t.Schema + "." + t.Name
		if _, ok := typeCtx.changed[key]; ok {
			continue
		}
		switch t.Kind {
		case ir.TypeKindComposite:
			cascadeComposites = append(cascadeComposites, key)
		case ir.TypeKindDomain:
			cascadeDomains = append(cascadeDomains, key)
		}
	}
	if len(cascadeComposites) > 0 {
		sort.Strings(cascadeComposites)
		parts = append(parts, fmt.Sprintf("composite types %s are also being DROP+CREATEd because they nest a recreating type as an attribute.", strings.Join(cascadeComposites, ", ")))
	}
	if len(cascadeDomains) > 0 {
		sort.Strings(cascadeDomains)
		parts = append(parts, fmt.Sprintf("domains %s are also being DROP+CREATEd because their base type is being recreated.", strings.Join(cascadeDomains, ", ")))
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
		parts = append(parts, fmt.Sprintf("view %s.%s will be DROP+CREATEd; if its body references columns or fields removed by this change it will fail to recreate.", v.Schema, v.Name))
	}

	parts = append(parts, "Function/procedure signatures referencing recreated types are not detected; if any exist, apply will fail with a clear PG error.")

	return "-- " + strings.Join(parts, "\n-- ")
}

// enumValuesRemoved returns the values present in oldType but not in newType,
// preserving their relative order in oldType.
func enumValuesRemoved(oldType, newType *ir.Type) []string {
	if oldType == nil || newType == nil {
		return nil
	}
	keep := make(map[string]struct{}, len(newType.EnumValues))
	for _, v := range newType.EnumValues {
		keep[v] = struct{}{}
	}
	removed := make([]string, 0)
	for _, v := range oldType.EnumValues {
		if _, ok := keep[v]; !ok {
			removed = append(removed, v)
		}
	}
	return removed
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "'" + v + "'"
	}
	return out
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
