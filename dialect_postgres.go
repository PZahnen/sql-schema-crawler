package schema

import (
	"database/sql"
)

const postgresGeoInfo = `
	SELECT
		f_table_schema,
		f_table_name,
		f_geometry_column,
		type,
		COALESCE(srid, 0),
		COALESCE(coord_dimension, 0)
	FROM
		geometry_columns
	ORDER BY
		f_table_schema,
		f_table_name,
		f_geometry_column
`

// TODO(js) Should we be filtering out system tables, like we currently do?

const postgresAllColumns = `SELECT * FROM %s LIMIT 0`

const postgresTableNamesWithSchema = `
	SELECT
		n.nspname AS table_schema,
		c.relname AS table_name
	FROM
		pg_catalog.pg_class c
	JOIN
		pg_catalog.pg_namespace n
	ON	c.relnamespace = n.oid
	WHERE
		c.relkind IN ('r', 'f', 'p') AND
		n.nspname NOT IN ('pg_catalog', 'information_schema', 'tiger', 'tiger_data', 'topology') AND
		c.relname NOT IN ('spatial_ref_sys', 'geography_columns', 'geometry_columns', 'raster_columns', 'raster_overviews')
	ORDER BY
		table_schema,
		table_name
`

const postgresViewNamesWithSchema = `
	SELECT
		table_schema,
		table_name
	FROM
		information_schema.tables
	WHERE
		table_type = 'VIEW' AND
		table_schema NOT IN ('pg_catalog', 'information_schema', 'tiger', 'tiger_data', 'topology') AND
		table_name NOT IN ('spatial_ref_sys', 'geography_columns', 'geometry_columns', 'raster_columns', 'raster_overviews')
	UNION
	SELECT
		schemaname AS table_schema,
		matviewname AS table_name
	FROM
		pg_catalog.pg_matviews
	WHERE
		schemaname NOT IN ('pg_catalog', 'information_schema', 'tiger', 'tiger_data', 'topology') AND
		matviewname NOT IN ('spatial_ref_sys', 'geography_columns', 'geometry_columns', 'raster_columns', 'raster_overviews')
	ORDER BY
		table_schema,
		table_name
`

const postgresPrimaryKey = `
	SELECT
		kcu.column_name
	FROM
		information_schema.table_constraints tco
	JOIN
		information_schema.key_column_usage kcu
	ON	kcu.constraint_name = tco.constraint_name AND
		kcu.constraint_schema = tco.constraint_schema AND
		kcu.constraint_name = tco.constraint_name
	WHERE
		tco.constraint_type = 'PRIMARY KEY' AND
		kcu.table_schema = current_schema() AND
		kcu.table_name = $1
	ORDER BY
		kcu.ordinal_position
`

const postgresPrimaryKeyWithSchema = `
	SELECT
		kcu.column_name
	FROM
		information_schema.table_constraints tco
	JOIN
		information_schema.key_column_usage kcu
	ON	kcu.constraint_name = tco.constraint_name AND
		kcu.constraint_schema = tco.constraint_schema AND
		kcu.constraint_name = tco.constraint_name
	WHERE
		tco.constraint_type = 'PRIMARY KEY' AND
		kcu.table_schema = $1 AND
		kcu.table_name = $2
	ORDER BY
		kcu.ordinal_position
`

const postgresUniqueColumns = `
	WITH single_col_constraints AS (
		SELECT
			kcu.constraint_schema,
			kcu.constraint_name
		FROM
			information_schema.key_column_usage kcu
		WHERE
			kcu.table_schema = current_schema() AND
			kcu.table_name = $1
		GROUP BY
			kcu.constraint_schema,
			kcu.constraint_name
		HAVING
			COUNT(*) = 1
	)
	SELECT
		kcu.column_name
	FROM
		information_schema.table_constraints tc
	JOIN
		information_schema.key_column_usage kcu
	ON	kcu.constraint_name = tc.constraint_name AND
		kcu.constraint_schema = tc.constraint_schema
	JOIN
		single_col_constraints scc
	ON	scc.constraint_name = tc.constraint_name AND
		scc.constraint_schema = tc.constraint_schema
	WHERE
		tc.constraint_type = 'UNIQUE' AND
		kcu.table_schema = current_schema() AND
		kcu.table_name = $1
	ORDER BY
		kcu.ordinal_position
`

const postgresUniqueColumnsWithSchema = `
	WITH single_col_constraints AS (
		SELECT
			kcu.constraint_schema,
			kcu.constraint_name
		FROM
			information_schema.key_column_usage kcu
		WHERE
			kcu.table_schema = $1 AND
			kcu.table_name = $2
		GROUP BY
			kcu.constraint_schema,
			kcu.constraint_name
		HAVING
			COUNT(*) = 1
	)
	SELECT
		kcu.column_name
	FROM
		information_schema.table_constraints tc
	JOIN
		information_schema.key_column_usage kcu
	ON	kcu.constraint_name = tc.constraint_name AND
		kcu.constraint_schema = tc.constraint_schema
	JOIN
		single_col_constraints scc
	ON	scc.constraint_name = tc.constraint_name AND
		scc.constraint_schema = tc.constraint_schema
	WHERE
		tc.constraint_type = 'UNIQUE' AND
		kcu.table_schema = $1 AND
		kcu.table_name = $2
	ORDER BY
		kcu.ordinal_position
`

const postgresReadOnlyColumns = `
	SELECT
		column_name
	FROM
		information_schema.columns
	WHERE
		table_schema = current_schema() AND
		table_name = $1 AND
		(is_identity = 'YES' OR is_generated <> 'NEVER')
	ORDER BY
		ordinal_position
`

const postgresReadOnlyColumnsWithSchema = `
	SELECT
		column_name
	FROM
		information_schema.columns
	WHERE
		table_schema = $1 AND
		table_name = $2 AND
		(is_identity = 'YES' OR is_generated <> 'NEVER')
	ORDER BY
		ordinal_position
`

type postgresDialect struct{}

func (postgresDialect) escapeIdent(ident string) string {
	// "tablename"
	return escapeWithDoubleQuotes(ident)
}

func (d postgresDialect) ColumnTypes(db *sql.DB, schema, name string) ([]*sql.ColumnType, error) {
	return fetchColumnTypes(db, postgresAllColumns, schema, name, d.escapeIdent)
}

func (postgresDialect) PrimaryKey(db *sql.DB, schema, name string) ([]string, error) {
	if schema == "" {
		return fetchNames(db, postgresPrimaryKey, "", name)
	}
	return fetchNames(db, postgresPrimaryKeyWithSchema, schema, name)
}

func (postgresDialect) TableNames(db *sql.DB) ([][2]string, error) {
	return fetchObjectNames(db, postgresTableNamesWithSchema)
}

func (postgresDialect) ViewNames(db *sql.DB) ([][2]string, error) {
	return fetchObjectNames(db, postgresViewNamesWithSchema)
}

func fetchPostgresGeoInfo(db *sql.DB) ([]GeoInfo, error) {
	rows, err := db.Query(postgresGeoInfo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GeoInfo
	for rows.Next() {
		var schema, table, column, geomType string
		var srid, dimension int
		if err := rows.Scan(&schema, &table, &column, &geomType, &srid, &dimension); err != nil {
			return nil, err
		}
		out = append(out, GeoInfo{
			Schema:       schema,
			Table:        table,
			Column:       column,
			GeometryType: geomType,
			SRID:         srid,
			Dimension:    dimension,
			Force2D:      dimension <= 2,
			Source:       "geometry_columns",
		})
	}

	return out, nil
}

func fetchPostgresUniqueColumns(db *sql.DB, schema, name string) ([]string, error) {
	if schema == "" {
		return fetchNames(db, postgresUniqueColumns, "", name)
	}
	return fetchNames(db, postgresUniqueColumnsWithSchema, schema, name)
}

func fetchPostgresReadOnlyColumns(db *sql.DB, schema, name string) ([]string, error) {
	if schema == "" {
		return fetchNames(db, postgresReadOnlyColumns, "", name)
	}
	return fetchNames(db, postgresReadOnlyColumnsWithSchema, schema, name)
}
