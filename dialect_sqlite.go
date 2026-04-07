package schema

import (
	"database/sql"
)

const sqliteGeoInfo = `
	SELECT
		'' AS table_schema,
		table_name,
		column_name,
		geometry_type_name,
		COALESCE(srs_id, 0),
		COALESCE(z, 0)
	FROM
		gpkg_geometry_columns
	ORDER BY
		table_name,
		column_name
`

// TODO(js) Can we see tables in an attached database? How are their names handled? See https://sqlite.org/lang_naming.html

const sqliteAllColumns = `SELECT * FROM %s LIMIT 0`

const sqliteTableNamesWithSchema = `
	SELECT
		"" AS schema,
		name 
	FROM
		sqlite_master
	WHERE
		type = 'table'
	ORDER BY
		name
`

const sqliteViewNamesWithSchema = `
	SELECT
		"" AS schema,
		name
	FROM
		sqlite_master
	WHERE
		type = 'view'
	ORDER BY
		name
`

const sqlitePrimaryKey = `
	SELECT
		name
	FROM
		pragma_table_info(?)
	WHERE
		pk > 0
	ORDER BY
		pk
`

type sqliteDialect struct{}

func (sqliteDialect) escapeIdent(ident string) string {
	// "tablename"
	return escapeWithDoubleQuotes(ident)
}

func (d sqliteDialect) ColumnTypes(db *sql.DB, schema, name string) ([]*sql.ColumnType, error) {
	return fetchColumnTypes(db, sqliteAllColumns, schema, name, d.escapeIdent)
}

func (sqliteDialect) PrimaryKey(db *sql.DB, schema, name string) ([]string, error) {
	// if schema == "" {
	// 	return fetchNames(db, sqlitePrimaryKey, "", name)
	// }
	return fetchNames(db, sqlitePrimaryKey, "", name)
}

func (sqliteDialect) TableNames(db *sql.DB) ([][2]string, error) {
	return fetchObjectNames(db, sqliteTableNamesWithSchema)
}

func (sqliteDialect) ViewNames(db *sql.DB) ([][2]string, error) {
	return fetchObjectNames(db, sqliteViewNamesWithSchema)
}

func fetchSqliteGeoInfo(db *sql.DB) ([]GeoInfo, error) {
	rows, err := db.Query(sqliteGeoInfo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GeoInfo
	for rows.Next() {
		var schema, table, column, geomType string
		var srid, z int
		if err := rows.Scan(&schema, &table, &column, &geomType, &srid, &z); err != nil {
			return nil, err
		}
		dim := 2
		if z > 0 {
			dim = 3
		}
		out = append(out, GeoInfo{
			Schema:       schema,
			Table:        table,
			Column:       column,
			GeometryType: geomType,
			SRID:         srid,
			Dimension:    dim,
			Force2D:      dim <= 2,
			Source:       "gpkg_geometry_columns",
		})
	}

	return out, nil
}
