package diff

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tysen/pgschema/ir"
)

// generateCreateTypesSQL generates CREATE TYPE statements
func generateCreateTypesSQL(types []*ir.Type, targetSchema string, collector *diffCollector) {
	// Sort types topologically to handle dependencies (e.g., composite types referencing enum types)
	sortedTypes := topologicallySortTypes(types)

	for _, typeObj := range sortedTypes {
		sql := generateTypeSQL(typeObj, targetSchema)

		// Determine DiffType based on type kind

		// Create context for this statement
		var diffType DiffType
		if typeObj.Kind == ir.TypeKindDomain {
			diffType = DiffTypeDomain
		} else {
			diffType = DiffTypeType
		}

		context := &diffContext{
			Type:                diffType,
			Operation:           DiffOperationCreate,
			Path:                fmt.Sprintf("%s.%s", typeObj.Schema, typeObj.Name),
			Source:              typeObj,
			CanRunInTransaction: true,
		}

		collector.collect(context, sql)
	}
}

// generateModifyTypesSQL generates ALTER TYPE / ALTER DOMAIN statements and
// DROP+CREATE cycles for types requiring recreate (composite shape change or
// enum non-additive change).
//
// Recreates are emitted as one combined block via generateTypeRecreateBlock —
// they share dependent objects (table columns, views) and need a globally-
// consistent topological order, so per-diff emission would duplicate or
// mis-order operations. The typeCtx, colCtx, and viewCtx must already be
// computed by findDependentObjectsForRecreatedTypes. They may be nil/empty
// when no recreates are present.
func generateModifyTypesSQL(
	diffs []*typeDiff,
	targetSchema string,
	collector *diffCollector,
	typeCtx *dependentTypesContext,
	colCtx *dependentColumnsContext,
	viewCtx *dependentViewsContext,
) {
	for _, diff := range diffs {
		switch diff.Old.Kind {
		case ir.TypeKindEnum:
			// ENUM types: ALTER TYPE ADD VALUE for the additive case;
			// otherwise the recreate block emits the DDL.
			if diff.New.Kind == ir.TypeKindEnum && isEnumAddOnly(diff.Old, diff.New) {
				alterStatements := generateAlterTypeEnumStatements(diff.Old, diff.New, targetSchema)
				for _, stmt := range alterStatements {
					context := &diffContext{
						Type:                DiffTypeType,
						Operation:           DiffOperationAlter,
						Path:                fmt.Sprintf("%s.%s", diff.New.Schema, diff.New.Name),
						Source:              diff,
						CanRunInTransaction: true,
					}
					collector.collect(context, stmt)
				}
			}
		case ir.TypeKindDomain:
			// Domain types can be modified with ALTER DOMAIN
			if diff.New.Kind == ir.TypeKindDomain {
				alterStatements := generateAlterDomainStatements(diff.Old, diff.New, targetSchema)
				for _, stmt := range alterStatements {
					context := &diffContext{
						Type:                DiffTypeDomain,
						Operation:           DiffOperationAlter,
						Path:                fmt.Sprintf("%s.%s", diff.New.Schema, diff.New.Name),
						Source:              diff,
						CanRunInTransaction: true,
					}
					collector.collect(context, stmt)
				}
			}
			// Composite type changes are emitted by the cascade block below,
			// not in this loop.
		}
	}

	// Emit one combined DROP+CREATE block covering every type in the
	// recreate closure (changed composites/enums + transitively-dependent
	// composites + domains over closure types).
	if !typeCtx.IsEmpty() {
		generateTypeRecreateBlock(typeCtx, colCtx, viewCtx, targetSchema, collector)
	}
}

// isEnumAddOnly reports whether transforming oldType into newType is
// representable as a sequence of ALTER TYPE ADD VALUE statements only.
// True iff every value in old is present in new in the same relative order
// — i.e. removing values, reordering, or renaming all return false and route
// through the recreate path.
//
// An empty old enum returns false: the existing ADD VALUE emitter cannot
// build a valid BEFORE/AFTER clause without an existing anchor value.
// Postgres rejects empty enums in practice, but the IR permits them.
func isEnumAddOnly(oldType, newType *ir.Type) bool {
	if oldType == nil || newType == nil {
		return false
	}
	if len(oldType.EnumValues) == 0 {
		return false
	}
	oldSet := make(map[string]struct{}, len(oldType.EnumValues))
	for _, v := range oldType.EnumValues {
		oldSet[v] = struct{}{}
	}
	filtered := make([]string, 0, len(oldType.EnumValues))
	for _, v := range newType.EnumValues {
		if _, ok := oldSet[v]; ok {
			filtered = append(filtered, v)
		}
	}
	return slices.Equal(filtered, oldType.EnumValues)
}

// typeNeedsRecreate reports whether a typeDiff requires DROP+CREATE rather
// than in-place ALTER. True for composite shape change and enum non-additive
// change. Domains are always handled by generateAlterDomainStatements; their
// recreates only happen transitively when their base type recreates, which
// is decided inside the closure expansion, not here.
func typeNeedsRecreate(diff *typeDiff) bool {
	if diff == nil || diff.Old == nil || diff.New == nil {
		return false
	}
	if diff.Old.Kind != diff.New.Kind {
		return false
	}
	switch diff.Old.Kind {
	case ir.TypeKindComposite:
		return true
	case ir.TypeKindEnum:
		return !isEnumAddOnly(diff.Old, diff.New)
	}
	return false
}

// generateDropTypesSQL generates DROP TYPE statements
func generateDropTypesSQL(types []*ir.Type, targetSchema string, collector *diffCollector) {
	// Sort types by name for consistent ordering
	sortedTypes := make([]*ir.Type, len(types))
	copy(sortedTypes, types)
	sort.Slice(sortedTypes, func(i, j int) bool {
		return sortedTypes[i].Name < sortedTypes[j].Name
	})

	for _, typeObj := range sortedTypes {
		typeName := qualifyEntityName(typeObj.Schema, typeObj.Name, targetSchema)

		var sql string
		if typeObj.Kind == ir.TypeKindDomain {
			sql = fmt.Sprintf("DROP DOMAIN IF EXISTS %s RESTRICT;", typeName)
		} else {
			sql = fmt.Sprintf("DROP TYPE IF EXISTS %s RESTRICT;", typeName)
		}

		// Create context for this statement
		var diffType DiffType
		if typeObj.Kind == ir.TypeKindDomain {
			diffType = DiffTypeDomain
		} else {
			diffType = DiffTypeType
		}

		context := &diffContext{
			Type:                diffType,
			Operation:           DiffOperationDrop,
			Path:                fmt.Sprintf("%s.%s", typeObj.Schema, typeObj.Name),
			Source:              typeObj,
			CanRunInTransaction: true,
		}

		collector.collect(context, sql)
	}
}

// generateAlterTypeEnumStatements generates ALTER TYPE ADD VALUE statements for enum changes
func generateAlterTypeEnumStatements(oldType, newType *ir.Type, targetSchema string) []string {
	var statements []string

	// Create a map of old enum values for quick lookup
	oldValues := make(map[string]int)
	for i, value := range oldType.EnumValues {
		oldValues[value] = i
	}

	// Find new values and their positions
	typeName := qualifyEntityName(newType.Schema, newType.Name, targetSchema)

	for i, newValue := range newType.EnumValues {
		if _, exists := oldValues[newValue]; !exists {
			// This is a new value, generate ALTER TYPE ADD VALUE statement
			var stmt string
			if i == 0 {
				// Add at the beginning
				stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE '%s' BEFORE '%s';", typeName, newValue, newType.EnumValues[1])
			} else if i == len(newType.EnumValues)-1 {
				// Add at the end
				stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE '%s' AFTER '%s';", typeName, newValue, newType.EnumValues[i-1])
			} else {
				// Add in the middle
				stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE '%s' AFTER '%s';", typeName, newValue, newType.EnumValues[i-1])
			}
			statements = append(statements, stmt)
		}
	}

	return statements
}

// generateAlterDomainStatements generates ALTER DOMAIN statements for domain changes
func generateAlterDomainStatements(oldDomain, newDomain *ir.Type, targetSchema string) []string {
	var statements []string
	domainName := qualifyEntityName(newDomain.Schema, newDomain.Name, targetSchema)

	// Check if default value changed
	if oldDomain.Default != newDomain.Default {
		if newDomain.Default == "" {
			statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s DROP DEFAULT;", domainName))
		} else {
			statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s SET DEFAULT %s;", domainName, newDomain.Default))
		}
	}

	// Check if NOT NULL changed
	if oldDomain.NotNull != newDomain.NotNull {
		if newDomain.NotNull {
			statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s SET NOT NULL;", domainName))
		} else {
			statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s DROP NOT NULL;", domainName))
		}
	}

	// Check constraints changes
	// Create maps for easier comparison
	oldConstraints := make(map[string]*ir.DomainConstraint)
	for _, c := range oldDomain.Constraints {
		key := c.Name
		if key == "" {
			key = c.Definition
		}
		oldConstraints[key] = c
	}

	newConstraints := make(map[string]*ir.DomainConstraint)
	for _, c := range newDomain.Constraints {
		key := c.Name
		if key == "" {
			key = c.Definition
		}
		newConstraints[key] = c
	}

	// Drop removed constraints
	for key, oldConstraint := range oldConstraints {
		if newConstraint, exists := newConstraints[key]; !exists {
			// Constraint was removed
			if oldConstraint.Name != "" {
				statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s DROP CONSTRAINT %s;", domainName, ir.QuoteIdentifier(oldConstraint.Name)))
			}
			// Note: unnamed constraints cannot be dropped individually
		} else if oldConstraint.Name != "" && oldConstraint.Definition != newConstraint.Definition {
			// Constraint exists but definition changed - need to drop and recreate
			statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s DROP CONSTRAINT %s;", domainName, ir.QuoteIdentifier(oldConstraint.Name)))
		}
	}

	// Add new constraints
	for key, newConstraint := range newConstraints {
		oldConstraint, exists := oldConstraints[key]
		if !exists || (exists && oldConstraint.Definition != newConstraint.Definition) {
			// Either new constraint or definition changed
			constraintDef := newConstraint.Definition
			if newConstraint.Name != "" {
				statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s ADD CONSTRAINT %s %s;", domainName, ir.QuoteIdentifier(newConstraint.Name), constraintDef))
			} else {
				statements = append(statements, fmt.Sprintf("ALTER DOMAIN %s ADD %s;", domainName, constraintDef))
			}
		}
	}

	return statements
}

// generateTypeSQL generates CREATE TYPE statement
func generateTypeSQL(typeObj *ir.Type, targetSchema string) string {
	// Only include type name without schema if it's in the target schema
	typeName := qualifyEntityName(typeObj.Schema, typeObj.Name, targetSchema)

	switch typeObj.Kind {
	case ir.TypeKindEnum:
		if len(typeObj.EnumValues) == 0 {
			return fmt.Sprintf("CREATE TYPE %s AS ENUM ();", typeName)
		}

		// Use multi-line format for better readability
		var lines []string
		lines = append(lines, fmt.Sprintf("CREATE TYPE %s AS ENUM (", typeName))
		for i, value := range typeObj.EnumValues {
			if i == len(typeObj.EnumValues)-1 {
				// Last value, no comma
				lines = append(lines, fmt.Sprintf("    '%s'", value))
			} else {
				// Not last value, add comma
				lines = append(lines, fmt.Sprintf("    '%s',", value))
			}
		}
		lines = append(lines, ");")
		return strings.Join(lines, "\n")
	case ir.TypeKindComposite:
		var attributes []string
		for _, attr := range typeObj.Columns {
			// Strip schema prefix from data type if it matches the target schema
			dataType := stripSchemaPrefix(attr.DataType, targetSchema)
			attributes = append(attributes, fmt.Sprintf("%s %s", attr.Name, dataType))
		}
		return fmt.Sprintf("CREATE TYPE %s AS (%s);", typeName, strings.Join(attributes, ", "))
	case ir.TypeKindDomain:
		// Use multi-line format for better readability if there are constraints
		hasConstraints := len(typeObj.Constraints) > 0 || typeObj.NotNull || typeObj.Default != ""

		if !hasConstraints {
			return fmt.Sprintf("CREATE DOMAIN %s AS %s;", typeName, typeObj.BaseType)
		}

		// Multi-line format
		lines := []string{fmt.Sprintf("CREATE DOMAIN %s AS %s", typeName, typeObj.BaseType)}

		if typeObj.Default != "" {
			lines = append(lines, fmt.Sprintf("  DEFAULT %s", typeObj.Default))
		}
		if typeObj.NotNull {
			lines = append(lines, "  NOT NULL")
		}

		// Add domain constraints (CHECK constraints)
		// Normalize VALUE to uppercase for consistency
		for _, constraint := range typeObj.Constraints {
			constraintDef := constraint.Definition
			if constraint.Name != "" {
				lines = append(lines, fmt.Sprintf("  CONSTRAINT %s %s", ir.QuoteIdentifier(constraint.Name), constraintDef))
			} else {
				lines = append(lines, fmt.Sprintf("  %s", constraintDef))
			}
		}

		return strings.Join(lines, "\n") + ";"
	default:
		return fmt.Sprintf("CREATE TYPE %s;", typeName)
	}
}

// typesEqual compares two types for equality. targetSchema lets us normalize
// schema-qualified data type names the same way columnsEqual does: composite
// attribute types coming from a target-DB inspection often arrive unqualified
// while the desired-state IR (built from a temporary schema and then
// rewritten to the target schema) arrives qualified. Without normalization
// the two would always look different.
func typesEqual(old, new *ir.Type, targetSchema string) bool {
	if old.Schema != new.Schema {
		return false
	}
	if old.Name != new.Name {
		return false
	}
	if old.Kind != new.Kind {
		return false
	}

	switch old.Kind {
	case ir.TypeKindEnum:
		// For ENUM types, compare values
		if len(old.EnumValues) != len(new.EnumValues) {
			return false
		}
		for i, value := range old.EnumValues {
			if value != new.EnumValues[i] {
				return false
			}
		}

	case ir.TypeKindComposite:
		// For composite types, compare columns positionally so that
		// reordering attributes is reported as a change. Postgres has no
		// in-place reorder for composite attributes, so a reorder is still a
		// real DDL operation (DROP+CREATE) even though it is conceptually a
		// no-op from the application's perspective.
		if len(old.Columns) != len(new.Columns) {
			return false
		}
		for i, col := range old.Columns {
			newCol := new.Columns[i]
			if col.Name != newCol.Name {
				return false
			}
			if stripSchemaPrefix(col.DataType, targetSchema) != stripSchemaPrefix(newCol.DataType, targetSchema) {
				return false
			}
		}

	case ir.TypeKindDomain:
		// For domain types, compare base type and constraints
		if old.BaseType != new.BaseType {
			return false
		}
		if old.NotNull != new.NotNull {
			return false
		}
		if old.Default != new.Default {
			return false
		}
		if len(old.Constraints) != len(new.Constraints) {
			return false
		}
		for i, constraint := range old.Constraints {
			newConstraint := new.Constraints[i]
			if constraint.Name != newConstraint.Name || constraint.Definition != newConstraint.Definition {
				return false
			}
		}
	}

	return true
}
