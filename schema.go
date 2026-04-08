package schema

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// https://github.com/golang/go/issues/7408
//
// https://github.com/golang/go/issues/7408#issuecomment-252046876
//
// If this package were to be part of database/sql, then the API would become like:-
//
//  func (db *DB) Table(schema, table string) ([]*ColumnType, error)
//  func (db *DB) TableNames() ([][2]string, error)
//  func (db *DB) Tables() (map[[2]string][]*ColumnType, error)
//  func (db *DB) View(schema, view string) ([]*ColumnType, error)
//  func (db *DB) ViewNames() ([][2]string, error)
//  func (db *DB) Views() (map[[2]string][]*ColumnType, error)
//

//

// AnalyzeOptions controls discovery behavior for Analyze.
type AnalyzeOptions struct {
	IncludeSchemas []string
	ExcludeSchemas []string
	IncludeTables  []string
	ExcludeTables  []string
}

// AnalyzeResult is a structured discovery response.
type AnalyzeResult struct {
	Schemas []SchemaMeta
	Tables  []TableMeta
	Views   []TableMeta
	GeoInfo []GeoInfo
}

type SchemaMeta struct {
	Name string
}

type TableMeta struct {
	Schema      string
	Name        string
	IsView      bool
	Columns     []ColumnMeta
	Constraints []ConstraintMeta
	BaseTables  [][2]string
	BaseColumns []ViewColumnOrigin
}

type ViewColumnOrigin struct {
	ViewColumn string
	Schema     string
	Table      string
	Column     string
}

type ColumnMeta struct {
	Name       string
	Type       string
	Nullable   bool
	IsPrimary  bool
	IsUnique   bool
	IsReadOnly bool
	IsSpatial  bool
	IsTemporal bool
}

type ConstraintMeta struct {
	Type    string
	Columns []string
}

type GeoInfo struct {
	Schema       string
	Table        string
	Column       string
	GeometryType string
	SRID         int
	Dimension    int
	Force2D      bool
	Source       string
}

// UnknownDriverError is returned when there is no matching
// database driver type name in the driverDialect table.
//
// Errors of this kind are caused by using an unsupported
// database driver/dialect, or if/when a database driver
// developer renames the type underlying calls to db.Driver().
type UnknownDriverError struct {
	Driver string
}

// Error returns a formatted string description.
func (e UnknownDriverError) Error() string {
	return fmt.Sprintf("unknown database driver: %s", e.Driver)
}

//

// Tables returns column type metadata for all tables in the current schema.
//
// The returned map is keyed by table name tuples.
func Tables(db *sql.DB) (map[[2]string][]*sql.ColumnType, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	names, err := d.TableNames(db)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	m := make(map[[2]string][]*sql.ColumnType, len(names))
	for _, n := range names {
		ct, err := d.ColumnTypes(db, n[0], n[1])
		if err != nil {
			return nil, err
		}
		m[n] = ct
	}
	return m, nil
}

// Views returns column type metadata for all views in the current schema.
//
// The returned map is keyed by view name tuples.
func Views(db *sql.DB) (map[[2]string][]*sql.ColumnType, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	names, err := d.ViewNames(db)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	m := make(map[[2]string][]*sql.ColumnType, len(names))
	for _, n := range names {
		ct, err := d.ColumnTypes(db, n[0], n[1])
		if err != nil {
			return nil, err
		}
		m[n] = ct
	}
	return m, nil
}

// TableNames returns a list of all table names.
//
// Each name consists of a [2]string tuple: schema name, table name.
func TableNames(db *sql.DB) ([][2]string, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	return d.TableNames(db)
}

// ViewNames returns a list of all view names.
//
// Each name consists of a [2]string tuple: schema name, view name.
func ViewNames(db *sql.DB) ([][2]string, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	return d.ViewNames(db)
}

// ColumnTypes returns the column type metadata for the given object (table or view) in the given schema.
//
// Setting schema to an empty string results in the current schema being used.
func ColumnTypes(db *sql.DB, schema, object string) ([]*sql.ColumnType, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	return d.ColumnTypes(db, schema, object)
}

// PrimaryKey returns a list of column names making up the primary
// key for the given table in the given schema.
func PrimaryKey(db *sql.DB, schema, table string) ([]string, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}
	return d.PrimaryKey(db, schema, table)
}

// Analyze discovers tables/views/columns/constraints and returns a structured response.
func Analyze(db *sql.DB, opts *AnalyzeOptions) (AnalyzeResult, error) {
	res := AnalyzeResult{}

	tables, err := TableNames(db)
	if err != nil {
		return res, err
	}
	views, err := ViewNames(db)
	if err != nil {
		return res, err
	}

	schemas := map[string]struct{}{}
	for _, t := range tables {
		if !includedSchema(t[0], opts) || !includedName(t[1], includeTables(opts), excludeTables(opts)) {
			continue
		}
		schemas[t[0]] = struct{}{}
		meta, err := loadTableMeta(db, t[0], t[1], false)
		if err != nil {
			return res, err
		}
		res.Tables = append(res.Tables, meta)
	}

	for _, v := range views {
		if !includedSchema(v[0], opts) || !includedName(v[1], includeTables(opts), excludeTables(opts)) {
			continue
		}
		schemas[v[0]] = struct{}{}
		meta, err := loadTableMeta(db, v[0], v[1], true)
		if err != nil {
			return res, err
		}
		meta.BaseTables, _ = resolveViewBaseTables(db, v[0], v[1])
		res.Views = append(res.Views, meta)
	}

	for s := range schemas {
		res.Schemas = append(res.Schemas, SchemaMeta{Name: s})
	}

	// Spatial metadata hooks per dialect (best-effort).
	d, err := getDialect(db)
	if err == nil {
		switch d.(type) {
		case postgresDialect:
			geo, gErr := fetchPostgresGeoInfo(db)
			if gErr == nil {
				res.GeoInfo = append(res.GeoInfo, filterGeoInfo(geo, opts)...)
			}
		case sqliteDialect:
			geo, gErr := fetchSqliteGeoInfo(db)
			if gErr == nil {
				res.GeoInfo = append(res.GeoInfo, filterGeoInfo(geo, opts)...)
			}
		}
	}

	return res, nil
}

func loadTableMeta(db *sql.DB, schema, name string, isView bool) (TableMeta, error) {
	meta := TableMeta{Schema: schema, Name: name, IsView: isView}

	cts, err := ColumnTypes(db, schema, name)
	if err != nil {
		return meta, err
	}
	pk, err := PrimaryKey(db, schema, name)
	if err != nil {
		pk = nil
	}
	uniqueCols, err := uniqueColumns(db, schema, name)
	if err != nil {
		uniqueCols = nil
	}
	readOnlyCols, err := readOnlyColumns(db, schema, name, isView)
	if err != nil {
		readOnlyCols = nil
	}

	viewOrigins := map[string]ViewColumnOrigin{}
	baseFlags := map[[2]string]struct {
		pk        []string
		unique    []string
		readOnly  []string
		colDBType map[string]string
	}{}
	if isView {
		origins, _ := resolveViewBaseColumns(db, schema, name)
		meta.BaseColumns = origins
		for _, o := range origins {
			viewOrigins[strings.ToLower(o.ViewColumn)] = o
			key := [2]string{o.Schema, o.Table}
			if _, ok := baseFlags[key]; ok {
				continue
			}
			pk, _ := PrimaryKey(db, o.Schema, o.Table)
			uq, _ := uniqueColumns(db, o.Schema, o.Table)
			ro, _ := readOnlyColumns(db, o.Schema, o.Table, false)
			ct, _ := ColumnTypes(db, o.Schema, o.Table)
			tm := map[string]string{}
			for _, c := range ct {
				tm[strings.ToLower(c.Name())] = c.DatabaseTypeName()
			}
			baseFlags[key] = struct {
				pk        []string
				unique    []string
				readOnly  []string
				colDBType map[string]string
			}{pk: pk, unique: uq, readOnly: ro, colDBType: tm}
		}
	}

	for _, ct := range cts {
		nullable, _ := ct.Nullable()
		colName := ct.Name()
		dbType := ct.DatabaseTypeName()

		isPrimary := exists(colName, pk)
		isUniqueCol := isUnique(colName, pk, uniqueCols)
		isReadOnlyCol := exists(colName, readOnlyCols)
		isSpatialCol := isSpatial(dbType, colName)
		isTemporalCol := isTemporal(dbType, colName)

		if isView {
			if origin, ok := viewOrigins[strings.ToLower(colName)]; ok {
				if bf, ok := baseFlags[[2]string{origin.Schema, origin.Table}]; ok {
					baseCol := origin.Column
					isPrimary = exists(baseCol, bf.pk)
					isUniqueCol = isUnique(baseCol, bf.pk, bf.unique)
					isReadOnlyCol = exists(baseCol, bf.readOnly)
					if t, ok := bf.colDBType[strings.ToLower(baseCol)]; ok {
						isSpatialCol = isSpatial(t, baseCol)
						isTemporalCol = isTemporal(t, baseCol)
					}
				}
			}
		}

		meta.Columns = append(meta.Columns, ColumnMeta{
			Name:       colName,
			Type:       dbType,
			Nullable:   nullable,
			IsPrimary:  isPrimary,
			IsUnique:   isUniqueCol,
			IsReadOnly: isReadOnlyCol,
			IsSpatial:  isSpatialCol,
			IsTemporal: isTemporalCol,
		})
	}

	if len(pk) > 0 {
		meta.Constraints = append(meta.Constraints, ConstraintMeta{Type: "PRIMARY_KEY", Columns: pk})
	}

	return meta, nil
}

func includedSchema(name string, opts *AnalyzeOptions) bool {
	if opts == nil {
		return true
	}
	if len(opts.IncludeSchemas) > 0 && !includedName(name, opts.IncludeSchemas, nil) {
		return false
	}
	if len(opts.ExcludeSchemas) > 0 && !includedName(name, nil, opts.ExcludeSchemas) {
		return false
	}
	return true
}

func includeTables(opts *AnalyzeOptions) []string {
	if opts == nil {
		return nil
	}
	return opts.IncludeTables
}

func excludeTables(opts *AnalyzeOptions) []string {
	if opts == nil {
		return nil
	}
	return opts.ExcludeTables
}

func includedName(name string, includes, excludes []string) bool {
	if len(includes) > 0 {
		matched := false
		for _, p := range includes {
			if regexp.MustCompile(p).MatchString(name) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, p := range excludes {
		if regexp.MustCompile(p).MatchString(name) {
			return false
		}
	}
	return true
}

func filterGeoInfo(in []GeoInfo, opts *AnalyzeOptions) []GeoInfo {
	if opts == nil {
		return in
	}
	out := make([]GeoInfo, 0, len(in))
	for _, g := range in {
		if !includedSchema(g.Schema, opts) {
			continue
		}
		if !includedName(g.Table, includeTables(opts), excludeTables(opts)) {
			continue
		}
		out = append(out, g)
	}
	return out
}

func isUnique(column string, primaryKeyColumns, uniqueColumns []string) bool {
	if exists(column, primaryKeyColumns) {
		return true
	}
	return exists(column, uniqueColumns)
}

func uniqueColumns(db *sql.DB, schema, table string) ([]string, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}

	switch d.(type) {
	case postgresDialect:
		return fetchPostgresUniqueColumns(db, schema, table)
	case sqliteDialect:
		return fetchSqliteUniqueColumns(db, schema, table)
	default:
		return nil, nil
	}
}

func readOnlyColumns(db *sql.DB, schema, table string, isView bool) ([]string, error) {
	if isView {
		// xtraplatform resolves view columns to base columns; we currently do not do
		// that at column-level, so we avoid blanket-readonly for all view columns.
		return nil, nil
	}

	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}

	switch d.(type) {
	case postgresDialect:
		return fetchPostgresReadOnlyColumns(db, schema, table)
	case sqliteDialect:
		return fetchSqliteReadOnlyColumns(db, schema, table)
	default:
		return nil, nil
	}
}

func exists(value string, list []string) bool {
	for _, v := range list {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}

func isSpatial(databaseType, columnName string) bool {
	t := strings.ToLower(databaseType + " " + columnName)
	return strings.Contains(t, "geom") || strings.Contains(t, "geometry") || strings.Contains(t, "geography")
}

func isTemporal(databaseType, columnName string) bool {
	t := strings.ToLower(databaseType + " " + columnName)
	return strings.Contains(t, "date") || strings.Contains(t, "time") || strings.Contains(t, "timestamp")
}

// resolveViewBaseTables attempts to resolve base tables used by a view.
func resolveViewBaseTables(db *sql.DB, schema, view string) ([][2]string, error) {
	d, err := getDialect(db)
	if err != nil {
		return nil, err
	}

	switch d.(type) {
	case postgresDialect:
		const postgresViewBaseTables = `
			SELECT table_schema, table_name
			FROM information_schema.view_table_usage
			WHERE view_schema = $1 AND view_name = $2
			ORDER BY table_schema, table_name`
		rows, qErr := db.Query(postgresViewBaseTables, schema, view)
		if qErr != nil {
			return nil, qErr
		}
		defer rows.Close()
		var out [][2]string
		for rows.Next() {
			var s, n string
			if scanErr := rows.Scan(&s, &n); scanErr != nil {
				return nil, scanErr
			}
			out = append(out, [2]string{s, n})
		}
		return out, nil
	case sqliteDialect:
		const sqliteViewSql = `SELECT sql FROM sqlite_master WHERE type='view' AND name = ?`
		var sqlText string
		if qErr := db.QueryRow(sqliteViewSql, view).Scan(&sqlText); qErr != nil {
			return nil, qErr
		}
		re := regexp.MustCompile(`(?i)\bfrom\s+([a-zA-Z0-9_\."]+)`)
		m := re.FindStringSubmatch(sqlText)
		if len(m) < 2 {
			return nil, nil
		}
		tbl := strings.Trim(m[1], `"`)
		return [][2]string{{"", tbl}}, nil
	default:
		return nil, nil
	}
}

func resolveViewBaseColumns(db *sql.DB, schema, view string) ([]ViewColumnOrigin, error) {
	def, err := fetchViewDefinition(db, schema, view)
	if err != nil {
		return nil, err
	}
	return parseViewColumnOrigins(def)
}

func fetchViewDefinition(db *sql.DB, schema, view string) (string, error) {
	d, err := getDialect(db)
	if err != nil {
		return "", err
	}

	switch d.(type) {
	case postgresDialect:
		q := `SELECT pg_get_viewdef(format('%I.%I', COALESCE(NULLIF($1,''), current_schema()), $2)::regclass, true)`
		var def string
		if err := db.QueryRow(q, schema, view).Scan(&def); err != nil {
			return "", err
		}
		return def, nil
	case sqliteDialect:
		q := `SELECT sql FROM sqlite_master WHERE type='view' AND name = ?`
		var def string
		if err := db.QueryRow(q, view).Scan(&def); err != nil {
			return "", err
		}
		return def, nil
	default:
		return "", nil
	}
}

func parseViewColumnOrigins(def string) ([]ViewColumnOrigin, error) {
	m := regexp.MustCompile(`(?is)\bselect\b(.*?)\bfrom\b\s+([a-zA-Z0-9_\."` + "`" + `]+)`).FindStringSubmatch(def)
	if len(m) < 3 {
		return nil, nil
	}

	selectPart := m[1]
	fromIdent := strings.TrimSpace(m[2])
	fromSchema, fromTable := parseQualifiedIdentifier(fromIdent)

	items := splitSelectList(selectPart)
	out := make([]ViewColumnOrigin, 0, len(items))
	for _, item := range items {
		expr, alias := splitExprAndAlias(item)
		baseCol := parseSimpleColumnExpr(expr)
		if baseCol == "" {
			continue
		}
		viewCol := alias
		if viewCol == "" {
			viewCol = baseCol
		}
		out = append(out, ViewColumnOrigin{
			ViewColumn: viewCol,
			Schema:     fromSchema,
			Table:      fromTable,
			Column:     baseCol,
		})
	}
	return out, nil
}

func parseQualifiedIdentifier(in string) (string, string) {
	parts := strings.Split(strings.TrimSpace(in), ".")
	if len(parts) == 1 {
		return "", unquoteIdent(parts[0])
	}
	return unquoteIdent(parts[len(parts)-2]), unquoteIdent(parts[len(parts)-1])
}

func splitSelectList(s string) []string {
	var out []string
	start, depth := 0, 0
	inQuote := false
	for i, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote && depth > 0 {
				depth--
			}
		case ',':
			if !inQuote && depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func splitExprAndAlias(item string) (expr, alias string) {
	m := regexp.MustCompile(`(?is)^(.*?)\s+as\s+([a-zA-Z0-9_"` + "`" + `]+)$`).FindStringSubmatch(strings.TrimSpace(item))
	if len(m) == 3 {
		return strings.TrimSpace(m[1]), unquoteIdent(m[2])
	}
	return strings.TrimSpace(item), ""
}

func parseSimpleColumnExpr(expr string) string {
	e := strings.TrimSpace(expr)
	if strings.ContainsAny(e, "()") {
		return ""
	}
	parts := strings.Split(e, ".")
	last := parts[len(parts)-1]
	if last == "*" {
		return ""
	}
	return unquoteIdent(last)
}

func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		return s[1 : len(s)-1]
	}
	return s
}

// fetchNames executes the given query with an optional name parameter,
// and returns a list of table/view/column names.
//
// The name parameter (if not "") is passed as a parameter to db.Query.
func fetchNames(db *sql.DB, query, schema, name string) ([]string, error) {
	var rows *sql.Rows
	var err error
	if len(schema) > 0 {
		rows, err = db.Query(query, schema, name)
	} else {
		rows, err = db.Query(query, name)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scan result into list of names.
	var names []string
	n := ""
	for rows.Next() {
		err = rows.Scan(&n)
		if err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, nil
}

// fetchObjectNames executes the given query
// and returns a list of table/view/column names.
func fetchObjectNames(db *sql.DB, query string) ([][2]string, error) {
	var rows *sql.Rows
	var err error
	rows, err = db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scan result into list of names.
	var names [][2]string
	s := ""
	n := ""
	for rows.Next() {
		err = rows.Scan(&s, &n)
		if err != nil {
			return nil, err
		}
		names = append(names, [2]string{s, n})
	}
	return names, nil
}

func getDialect(db *sql.DB) (dialect, error) {
	dt := fmt.Sprintf("%T", db.Driver())
	d, ok := driverDialect[dt]
	if !ok {
		return nil, UnknownDriverError{Driver: dt}
	}
	return d, nil
}

// fetchColumnTypes queries the database and returns column's type metadata
// for a single table or view.
func fetchColumnTypes(db *sql.DB, query, schema, name string, escapeIdent func(string) string) ([]*sql.ColumnType, error) {
	if schema == "" {
		query = fmt.Sprintf(query, escapeIdent(name))
	} else {
		n := fmt.Sprintf("%s.%s", escapeIdent(schema), escapeIdent(name))
		query = fmt.Sprintf(query, n)
	}
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rows.ColumnTypes()
}
