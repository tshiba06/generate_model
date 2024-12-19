package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"log"
	"os"
	"strings"

	pluralize "github.com/gertd/go-pluralize"
	"github.com/iancoleman/strcase"
	"github.com/spf13/cobra"
)

var config2ModelCmd = &cobra.Command{
	Use:   "config2model",
	Short: "configから構造体を自動生成",
	Long:  "configから構造体を自動生成します",
	Run:   runConfig2Model,
}

func init() {
	rootCmd.AddCommand(config2ModelCmd)
}

func runConfig2Model(cmd *cobra.Command, args []string) {
	b, err := os.ReadFile("model_config.json")
	if err != nil {
		log.Fatal(err)
	}
	var models []Model
	if err := json.Unmarshal(b, &models); err != nil {
		log.Fatal(err)
	}

	client := pluralize.NewClient()

	for _, model := range models {
		singleName := client.Singular(model.Name)
		mName := strcase.ToCamel(singleName)
		fmt.Println(mName)

		var (
			importSpecs []ast.Spec
			fields      []*ast.Field
			packages    = map[string]string{
				"time":      "\"time\"",
				"null":      "\"gopkg.in/guregu/null.v4\"",
				"datatypes": "\"gorm.io/datatypes\"",
				"pq":        "\"github.com/lib/pq\"",
			}
		)
		for pk, pv := range packages {
			for _, f := range model.Fields {
				if strings.Contains(f.Type, pk) {
					importSpecs = append(importSpecs,
						&ast.ImportSpec{
							Path: &ast.BasicLit{
								Kind:  token.STRING,
								Value: pv,
							},
						},
					)
					break
				}
			}
		}

		for _, f := range model.Fields {
			fieldName := strings.Replace(strcase.ToCamel(f.Name), "Id", "ID", 1)
			fields = append(fields, &ast.Field{
				Names: []*ast.Ident{
					{
						Name: fieldName,
						Obj: &ast.Object{
							Kind: ast.Var,
							Name: fieldName,
						},
					},
				},
				Type: &ast.Ident{
					Name: f.Type,
				},
			})
		}

		// ①itemStandardの↓のような状況が作れないのでここを修正する
		// Nameをいい感じに入れることができれば大丈夫
		// 	VerticalStandard    *Standard `gorm:"foreignKey: VerticalStandardID"`
		// HorizontalStandard  *Standard `gorm:"foreignKey: HorizontalStandardID"`

		// ②型がARRAYのものはもう少し細かくできる
		// 型の対応表だけ事前に用意するとかで良さそう
		// cliで作成すればいい

		for _, a := range model.Associations {
			isExist := false
			for _, f := range fields {
				name := client.Singular(a.Name)
				name = strcase.ToCamel(name)
				pluName := client.Plural(name)
				// 別名で持っている場合にはconfigの使用上対応していないので1つになる
				// 現状だとconfigを手動で編集するなどの工夫が必要なのでどこかのタイミングでいい感じにする
				// ここで①の状況にならないようにskipしているのでどうするか
				if name == f.Names[0].Name || pluName == f.Names[0].Name {
					isExist = true
				}
			}
			if isExist {
				continue
			}
			switch a.Type {
			case associateHasOne, associateBelongsTo:
				name := client.Singular(a.Name)
				name = strcase.ToCamel(name)

				names := []*ast.Ident{
					{
						Name: name,
						Obj: &ast.Object{
							Kind: ast.Var,
							Name: name,
						},
					},
				}
				types := &ast.Ident{
					Name: "*" + name,
				}
				fields = append(fields, &ast.Field{
					Names: names,
					Type:  types,
				})
			case associateHasMany:
				name := strcase.ToCamel(a.Name)
				siName := client.Singular(name)
				pluName := client.Plural(name)

				names := []*ast.Ident{
					{
						Name: pluName,
						Obj: &ast.Object{
							Kind: ast.Var,
							Name: pluName,
						},
					},
				}

				types := &ast.Ident{
					Name: "[]*" + siName,
				}
				fields = append(fields, &ast.Field{
					Names: names,
					Type:  types,
				})
			}
		}

		var decls []ast.Decl
		if len(importSpecs) > 0 {
			decls = append(decls,
				&ast.GenDecl{
					Tok:   token.IMPORT,
					Specs: importSpecs,
				},
			)
		}

		decls = append(decls, &ast.GenDecl{
			Tok: token.TYPE,
			Specs: []ast.Spec{
				&ast.TypeSpec{
					Name: &ast.Ident{
						Name: mName,
						Obj: &ast.Object{
							Kind: ast.Typ,
							Name: mName,
						},
					},
					Type: &ast.StructType{
						Fields: &ast.FieldList{
							List: fields,
						},
					},
					// jsonタグを追加するならここ
					// tableのfield名と同じ場合程度なら追加
				},
			},
		})

		f := &ast.File{
			Name:  ast.NewIdent("model"),
			Decls: decls,
		}
		writeFile(singleName, f)
	}
}

// ファイル書き込み
func writeFile(tableName string, f *ast.File) {
	fset := token.NewFileSet()
	target := fmt.Sprintf("./model/%s.go", tableName)
	writer, err := os.Create(target)
	if err != nil {
		log.Fatal(err)
	}
	defer writer.Close()
	buf := new(bytes.Buffer)
	_ = format.Node(buf, fset, f)
	writer.Write(buf.Bytes())
}
