package schema

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"testing"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

// Baseline discovery test against a deterministic in-memory SQLite DB.
// This validates current public behaviour (not source-code string patterns).
func TestAnalyzeDiscoveryBaselineSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE places (id INTEGER PRIMARY KEY, name TEXT NOT NULL, geom TEXT);`,
		`CREATE VIEW places_view AS SELECT id, name FROM places;`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec ddl %q: %v", stmt, err)
		}
	}

	tableNames, err := TableNames(db)
	if err != nil {
		t.Fatalf("TableNames: %v", err)
	}
	if len(tableNames) != 1 || tableNames[0][1] != "places" {
		t.Fatalf("unexpected table names: %#v", tableNames)
	}

	viewNames, err := ViewNames(db)
	if err != nil {
		t.Fatalf("ViewNames: %v", err)
	}
	if len(viewNames) != 1 || viewNames[0][1] != "places_view" {
		t.Fatalf("unexpected view names: %#v", viewNames)
	}

	tableCols, err := ColumnTypes(db, "", "places")
	if err != nil {
		t.Fatalf("ColumnTypes(table): %v", err)
	}
	if len(tableCols) != 3 {
		t.Fatalf("expected 3 table columns, got %d", len(tableCols))
	}

	viewCols, err := ColumnTypes(db, "", "places_view")
	if err != nil {
		t.Fatalf("ColumnTypes(view): %v", err)
	}
	if len(viewCols) != 2 {
		t.Fatalf("expected 2 view columns, got %d", len(viewCols))
	}

	pkCols, err := PrimaryKey(db, "", "places")
	if err != nil {
		t.Fatalf("PrimaryKey: %v", err)
	}
	if len(pkCols) != 1 || pkCols[0] != "id" {
		t.Fatalf("unexpected primary key columns: %#v", pkCols)
	}
}

func TestAnalyzeSQLiteNewFeatures(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE places (id INTEGER PRIMARY KEY, name TEXT NOT NULL, geom TEXT, created_at TIMESTAMP);`,
		`CREATE VIEW places_view AS SELECT id, name FROM places;`,
		`CREATE TABLE gpkg_geometry_columns (
			table_name TEXT NOT NULL,
			column_name TEXT NOT NULL,
			geometry_type_name TEXT NOT NULL,
			srs_id INTEGER,
			z TINYINT
		);`,
		`INSERT INTO gpkg_geometry_columns(table_name, column_name, geometry_type_name, srs_id, z)
		 VALUES ('places', 'geom', 'POINT', 4326, 0);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec ddl %q: %v", stmt, err)
		}
	}

	res, err := Analyze(db, nil)
	if err != nil {
		t.Fatalf("Analyze(nil): %v", err)
	}
	logAnalyzeSummary(t, "sqlite:new_features", res)

	if len(res.Tables) != 2 { // places + gpkg_geometry_columns
		t.Fatalf("expected 2 tables, got %d", len(res.Tables))
	}
	if len(res.Views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(res.Views))
	}

	places, ok := findTableMeta(res.Tables, "", "places")
	if !ok {
		t.Fatalf("Analyze result missing table places")
	}
	if !hasConstraint(places, "PRIMARY_KEY") {
		t.Fatalf("expected PRIMARY_KEY constraint for places")
	}
	if !hasColumnFlag(places, "geom", func(c ColumnMeta) bool { return c.IsSpatial }) {
		t.Fatalf("expected geom column to be marked spatial")
	}
	if !hasColumnFlag(places, "created_at", func(c ColumnMeta) bool { return c.IsTemporal }) {
		t.Fatalf("expected created_at column to be marked temporal")
	}

	v, ok := findTableMeta(res.Views, "", "places_view")
	if !ok {
		t.Fatalf("Analyze result missing view places_view")
	}
	if !v.IsView {
		t.Fatalf("expected places_view meta to be flagged as view")
	}
	if !allColumnsReadOnly(v) {
		t.Fatalf("expected all view columns to be read-only")
	}

	if len(res.GeoInfo) == 0 {
		t.Fatalf("expected GeoInfo entries from gpkg_geometry_columns")
	}

	filtered, err := Analyze(db, &AnalyzeOptions{IncludeTables: []string{"^places$"}})
	if err != nil {
		t.Fatalf("Analyze(filtered include): %v", err)
	}
	if len(filtered.Tables) != 1 || filtered.Tables[0].Name != "places" {
		t.Fatalf("expected only places table with include filter, got %#v", filtered.Tables)
	}
	if len(filtered.Views) != 0 {
		t.Fatalf("expected no views when IncludeTables is ^places$, got %d", len(filtered.Views))
	}

	excluded, err := Analyze(db, &AnalyzeOptions{ExcludeTables: []string{"^places$"}})
	if err != nil {
		t.Fatalf("Analyze(excluded): %v", err)
	}
	if containsObject(excluded.Tables, "", "places") {
		t.Fatalf("expected places to be excluded by ExcludeTables")
	}
}

// Optional integration smoke test against a real external DB.
//
// Usage example (Postgres):
//
//	SCHEMA_TEST_DRIVER=postgres \
//	SCHEMA_TEST_DSN="host=<HOST> port=<PORT> user=<USER> password=<PASSWORD> dbname=db_aaa_nw_nas7_2 sslmode=disable" \
//	SCHEMA_TEST_SCHEMA=public \
//	SCHEMA_TEST_OBJECT=<TABLE_OR_VIEW_NAME> \
//	SCHEMA_TEST_PK_TABLE=<TABLE_NAME> \
//	go test ./... -run TestAnalyzeDiscoveryExternalDB
//
// Optional vars:
//
//	SCHEMA_TEST_SCHEMA   schema name for object lookup
//	SCHEMA_TEST_OBJECT   table/view name for ColumnTypes
//	SCHEMA_TEST_PK_TABLE table name for PrimaryKey
func TestAnalyzeDiscoveryExternalDB(t *testing.T) {
	driverName := os.Getenv("SCHEMA_TEST_DRIVER")
	dsn := os.Getenv("SCHEMA_TEST_DSN")
	if driverName == "" || dsn == "" {
		t.Skip("set SCHEMA_TEST_DRIVER and SCHEMA_TEST_DSN to run external DB test")
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open external db: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("ping external db: %v", err)
	}

	if _, err := TableNames(db); err != nil {
		t.Fatalf("TableNames: %v", err)
	}
	if _, err := ViewNames(db); err != nil {
		t.Fatalf("ViewNames: %v", err)
	}
	if _, err := Tables(db); err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if _, err := Views(db); err != nil {
		t.Fatalf("Views: %v", err)
	}

	schemaName := os.Getenv("SCHEMA_TEST_SCHEMA")
	if object := os.Getenv("SCHEMA_TEST_OBJECT"); object != "" {
		if _, err := ColumnTypes(db, schemaName, object); err != nil {
			t.Fatalf("ColumnTypes(%s): %v", object, err)
		}
	}

	if table := os.Getenv("SCHEMA_TEST_PK_TABLE"); table != "" {
		if _, err := PrimaryKey(db, schemaName, table); err != nil {
			t.Fatalf("PrimaryKey(%s): %v", table, err)
		}
	}

	// New Analyze() checks for the current implementation.
	// Scope Analyze to the configured schema/object to avoid scanning huge DBs.
	var analyzeOpts *AnalyzeOptions
	if schemaName != "" || os.Getenv("SCHEMA_TEST_OBJECT") != "" {
		analyzeOpts = &AnalyzeOptions{}
		if schemaName != "" {
			analyzeOpts.IncludeSchemas = []string{"^" + regexp.QuoteMeta(schemaName) + "$"}
		}
		if object := os.Getenv("SCHEMA_TEST_OBJECT"); object != "" {
			analyzeOpts.IncludeTables = []string{"^" + regexp.QuoteMeta(object) + "$"}
		}
	}

	analyzeRes, err := Analyze(db, analyzeOpts)
	if err != nil {
		t.Fatalf("Analyze(%+v): %v", analyzeOpts, err)
	}
	logAnalyzeSummary(t, "external:analyze", analyzeRes)

	tableNames, err := TableNames(db)
	if err != nil {
		t.Fatalf("TableNames(recheck): %v", err)
	}
	viewNames, err := ViewNames(db)
	if err != nil {
		t.Fatalf("ViewNames(recheck): %v", err)
	}

	if analyzeOpts == nil {
		if len(analyzeRes.Tables) != len(tableNames) {
			t.Fatalf("Analyze tables mismatch: got %d, want %d", len(analyzeRes.Tables), len(tableNames))
		}
		if len(analyzeRes.Views) != len(viewNames) {
			t.Fatalf("Analyze views mismatch: got %d, want %d", len(analyzeRes.Views), len(viewNames))
		}
	}

	if object := os.Getenv("SCHEMA_TEST_OBJECT"); object != "" {
		if !containsObject(analyzeRes.Tables, schemaName, object) && !containsObject(analyzeRes.Views, schemaName, object) {
			t.Fatalf("Analyze result does not contain SCHEMA_TEST_OBJECT=%q in tables/views", object)
		}
	}

	if table := os.Getenv("SCHEMA_TEST_PK_TABLE"); table != "" {
		if !containsObject(analyzeRes.Tables, schemaName, table) {
			t.Fatalf("Analyze tables do not contain SCHEMA_TEST_PK_TABLE=%q", table)
		}
	}

	filtered, err := Analyze(db, &AnalyzeOptions{IncludeTables: []string{"^$"}})
	if err != nil {
		t.Fatalf("Analyze(filtered): %v", err)
	}
	logAnalyzeSummary(t, "external:filtered", filtered)
	if len(filtered.Tables) != 0 || len(filtered.Views) != 0 {
		t.Fatalf("expected empty Analyze result for IncludeTables=^$, got tables=%d views=%d", len(filtered.Tables), len(filtered.Views))
	}
}

func logAnalyzeSummary(t *testing.T, label string, r AnalyzeResult) {
	t.Helper()
	if os.Getenv("SCHEMA_TEST_DEBUG") != "1" {
		return
	}

	t.Logf("[%s] schemas=%d tables=%d views=%d geoinfo=%d", label, len(r.Schemas), len(r.Tables), len(r.Views), len(r.GeoInfo))
	t.Logf("[%s] tables: %s", label, firstObjectNames(r.Tables, 5))
	t.Logf("[%s] views: %s", label, firstObjectNames(r.Views, 5))
	if len(r.GeoInfo) > 0 {
		g := r.GeoInfo[0]
		t.Logf("[%s] first geoinfo: %s.%s.%s type=%s srid=%d dim=%d source=%s", label, g.Schema, g.Table, g.Column, g.GeometryType, g.SRID, g.Dimension, g.Source)
	}
}

func firstObjectNames(objects []TableMeta, limit int) string {
	if len(objects) == 0 {
		return "-"
	}
	if limit <= 0 {
		limit = 1
	}
	if len(objects) < limit {
		limit = len(objects)
	}
	out := ""
	for i := 0; i < limit; i++ {
		o := objects[i]
		name := o.Name
		if o.Schema != "" {
			name = o.Schema + "." + o.Name
		}
		if i > 0 {
			out += ", "
		}
		out += name
	}
	if len(objects) > limit {
		out += fmt.Sprintf(" (+%d more)", len(objects)-limit)
	}
	return out
}

func containsObject(objects []TableMeta, schemaName, objectName string) bool {
	for _, o := range objects {
		if o.Name != objectName {
			continue
		}
		if schemaName == "" || o.Schema == schemaName {
			return true
		}
	}
	return false
}

func findTableMeta(objects []TableMeta, schemaName, objectName string) (TableMeta, bool) {
	for _, o := range objects {
		if o.Name == objectName && o.Schema == schemaName {
			return o, true
		}
	}
	return TableMeta{}, false
}

func hasConstraint(table TableMeta, typ string) bool {
	for _, c := range table.Constraints {
		if c.Type == typ {
			return true
		}
	}
	return false
}

func hasColumnFlag(table TableMeta, column string, pred func(ColumnMeta) bool) bool {
	for _, c := range table.Columns {
		if c.Name == column && pred(c) {
			return true
		}
	}
	return false
}

func allColumnsReadOnly(table TableMeta) bool {
	if len(table.Columns) == 0 {
		return false
	}
	for _, c := range table.Columns {
		if !c.IsReadOnly {
			return false
		}
	}
	return true
}
