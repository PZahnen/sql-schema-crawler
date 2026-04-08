package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

func fetchSqliteUniqueColumns(db *sql.DB, schema, table string) ([]string, error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	indexListSQL := fmt.Sprintf("PRAGMA index_list('%s')", sqliteQuoteLiteral(table))
	rows, err := conn.QueryContext(ctx, indexListSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexListCols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var uniqueIndexes []string
	for rows.Next() {
		idxName, isUniq, err := scanSqliteIndexListRow(rows, indexListCols)
		if err != nil {
			return nil, err
		}
		if isUniq != 1 {
			continue
		}
		uniqueIndexes = append(uniqueIndexes, idxName)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	unique := map[string]struct{}{}
	for _, idxName := range uniqueIndexes {
		indexInfoSQL := fmt.Sprintf("PRAGMA index_info('%s')", sqliteQuoteLiteral(idxName))
		idxRows, err := conn.QueryContext(ctx, indexInfoSQL)
		if err != nil {
			return nil, err
		}

		var cols []string
		for idxRows.Next() {
			var seqno, cid int
			var col string
			if err := idxRows.Scan(&seqno, &cid, &col); err != nil {
				idxRows.Close()
				return nil, err
			}
			cols = append(cols, col)
		}
		if err := idxRows.Close(); err != nil {
			return nil, err
		}

		if len(cols) == 1 {
			if cols[0] != "" {
				unique[cols[0]] = struct{}{}
			}
		}
	}

	res := make([]string, 0, len(unique))
	for col := range unique {
		res = append(res, col)
	}
	sort.Strings(res)
	return res, nil
}

func fetchSqliteReadOnlyColumns(db *sql.DB, schema, table string) ([]string, error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	q := fmt.Sprintf("PRAGMA table_xinfo('%s')", sqliteQuoteLiteral(table))
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var (
			cid       int
			name      string
			typ       string
			notnull   int
			dfltValue interface{}
			pk        int
			hidden    int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk, &hidden); err != nil {
			return nil, err
		}
		// hidden=2/3 => generated columns in SQLite.
		if hidden == 2 || hidden == 3 {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

func scanSqliteIndexListRow(rows *sql.Rows, cols []string) (string, int, error) {
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", 0, err
	}

	idxName := ""
	isUniq := 0
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "name":
			idxName = toString(vals[i])
		case "unique":
			isUniq = toInt(vals[i])
		}
	}
	return idxName, isUniq, nil
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case int32:
		return int(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		n, _ := strconv.Atoi(t)
		return n
	case []byte:
		n, _ := strconv.Atoi(string(t))
		return n
	default:
		return 0
	}
}

func sqliteQuoteLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
