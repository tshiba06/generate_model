package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gertd/go-pluralize"
	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
)

var schema2ConfigCmd = &cobra.Command{
	Use:   "schema2config",
	Short: "schemaからconfigを作成",
	Long:  "schemaからconfigファイルを作成します",
	Run:   runSchema2Config,
}

func init() {
	rootCmd.AddCommand(schema2ConfigCmd)
}

type Model struct {
	Name         string
	Fields       []Column
	Associations []Association
}

type Config struct {
	Models []Model
}

type Table struct {
	Fields       []Column
	Associations []Association
}

type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type Association struct {
	Name           string
	Type           string
	ConstraintType string `json:"-"`
	// constrainType UniqueとForeignの両方を持っているものはNameとTypeだと2つになる
	// しかもその場合はhasOneに相当するのでどうするか
}

const (
	associateHasOne    = "hasOne"
	associateHasMany   = "hasMany"
	associateBelongsTo = "belongsTo"
)

func runSchema2Config(cmd *cobra.Command, args []string) {
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	user := os.Getenv("DB_USER")
	password := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_NAME")

	stmt := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, password, dbName)
	db, err := sql.Open("postgres", stmt)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT T.table_name, C.column_name, C.is_nullable, C.data_type
		FROM INFORMATION_SCHEMA.tables T
		LEFT JOIN INFORMATION_SCHEMA.columns C
		ON T.table_name = C.table_name
		WHERE T.table_schema = 'public';
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	tables := make(map[string][]Column, 0)
	for rows.Next() {
		var (
			tableName  string
			columnName string
			isNullable string
			dataType   string
			columnType string
		)
		rows.Scan(&tableName, &columnName, &isNullable, &dataType)
		if isNullable == "YES" {
			switch dataType {
			case "integer", "smallint":
				columnType = "null.Int"
			case "real", "numeric", "double precision":
				columnType = "null.Float"
			case "text":
				columnType = "null.String"
			case "boolean":
				columnType = "null.Bool"
			case "jsonb":
				columnType = "*datatypes.JSON"
			case "date", "time with time zone", "timestamp with time zone":
				columnType = "null.Time"
			case "ARRAY":
				columnType = "pq.Int32Array" // 暫定的
			default:
				fmt.Printf("%s は新しい型です", dataType)
			}
		} else {
			switch dataType {
			case "integer", "smallint":
				columnType = "int"
			case "real", "numeric", "double precision":
				columnType = "float64"
			case "text":
				columnType = "string"
			case "boolean":
				columnType = "bool"
			case "date", "timestamp with time zone":
				columnType = "time.Time"
			case "time with time zone":
				columnType = "datatypes.Time"
			case "ARRAY":
				columnType = "pq.Int32Array" // 暫定的
			default:
				fmt.Printf("%s は新しい型です", dataType)
			}
		}
		if columns, ok := tables[tableName]; ok {
			columns = append(columns, Column{
				Name: columnName,
				Type: columnType,
			})
			tables[tableName] = columns
		} else {
			tables[tableName] = []Column{
				{
					Name: columnName,
					Type: columnType,
				},
			}
		}
	}

	associations := make(map[string][]Association)

	belongsAssociationRows, err := db.Query(`
		SELECT R."constraint_name", K."table_name"
		FROM information_schema.referential_constraints as R
		LEFT JOIN information_schema.key_column_usage as K
		ON R.unique_constraint_schema = K."constraint_schema" AND R.unique_constraint_name = K."constraint_name"
		WHERE R."constraint_schema" = 'public';
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer belongsAssociationRows.Close()

	belongsTables := make(map[string]string, 0)
	for belongsAssociationRows.Next() {
		var (
			constraintName string
			belongsTable   string
		)
		belongsAssociationRows.Scan(&constraintName, &belongsTable)
		if _, ok := belongsTables[constraintName]; !ok {
			belongsTables[constraintName] = belongsTable
		}
	}

	belongsToRows, err := db.Query(`
		SELECT DISTINCT
			T."table_name",
			K."column_name",
			T."constraint_name",
			T."constraint_type"
		FROM information_schema.key_column_usage as K
		LEFT JOIN information_schema.table_constraints as T
		ON T."constraint_schema" = K."constraint_schema" AND T."constraint_name" = K."constraint_name"
		WHERE K."constraint_schema" = 'public' AND T.constraint_type <> 'PRIMARY KEY';
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer belongsToRows.Close()

	for belongsToRows.Next() {
		var (
			tableName      string
			columnName     string
			constraintName string
			constraintType string
		)
		belongsToRows.Scan(&tableName, &columnName, &constraintName, &constraintType)

		if bv, ok := belongsTables[constraintName]; ok {
			newAssociation := Association{
				Name:           bv, // TODO 加工が必要 item_state_idとかが入っているはずなのでItemStateとかになってないといけない
				Type:           associateBelongsTo,
				ConstraintType: constraintType,
			}
			if v, ok := associations[tableName]; ok {

				isExist := false
				for _, a := range v {
					if a.Name == newAssociation.Name {
						isExist = true
					}
				}
				if !isExist {
					v = append(v, newAssociation)
				}

				associations[tableName] = v
			} else {
				var name string
				if bv, ok := belongsTables[constraintName]; ok {
					name = bv
					associations[tableName] = []Association{
						{
							Name:           name, // TODO: 加工が必要 item_state_idとかが入っているはずなのでItemStateとかになってないといけない
							Type:           associateBelongsTo,
							ConstraintType: constraintType,
						},
					}
				}
			}
		}
	}

	plu := pluralize.NewClient()

	hasOneOrHasManyRows, err := db.Query(`
		SELECT DISTINCT
			C."table_name" as parent,
			K."table_name" as children,
			K."column_name" as children_foreign_key,
			T."constraint_type"
		FROM information_schema.constraint_column_usage as C
		LEFT JOIN information_schema.key_column_usage as K
		ON C."constraint_schema" = K."constraint_schema" AND C."constraint_name" = K."constraint_name"
		LEFT JOIN information_schema.table_constraints as T
		ON C."constraint_schema" = T."constraint_schema" AND C."constraint_name" = T."constraint_name"
		where C.table_schema = 'public' AND C."table_name" <> K."table_name";
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer hasOneOrHasManyRows.Close()

	for hasOneOrHasManyRows.Next() {
		var (
			parent             string
			children           string
			childrenForeignKey string
			constraintType     string
		)
		hasOneOrHasManyRows.Scan(&parent, &children, &childrenForeignKey, &constraintType)

		if columns, ok := tables[parent]; ok {
			for _, c := range columns {
				if plu.Singular(c.Name) == plu.Singular(children) {
					fmt.Println(children)
					continue
				}
			}
		}

		newAssociation := Association{
			Name:           children,
			Type:           associateHasMany, // 一旦全てhasManyとして扱う
			ConstraintType: constraintType,
		}

		if v, ok := associations[parent]; ok {
			for k, a := range v {
				if a.Name != newAssociation.Name {
					v = append(v, newAssociation)
					break
				}
				if a.Name == newAssociation.Name && a.ConstraintType != newAssociation.ConstraintType {
					v[k].Type = associateHasOne
					break
				}
			}
			associations[parent] = v
		} else {
			associations[parent] = []Association{
				newAssociation,
			}
		}
	}

	models := make([]Model, 0)
	for tk, tv := range tables {
		for ak, av := range associations {
			if tk != ak {
				continue
			}
			models = append(models, Model{
				Name:         tk,
				Fields:       tv,
				Associations: av,
			})
		}
	}

	// 一旦jsonでつくる
	file, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("model_config.json", file, 0777); err != nil {
		log.Fatal(err)
	}

}
