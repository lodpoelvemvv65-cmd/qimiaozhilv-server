// export-items creates an offline Chinese item reference from published MySQL
// configuration. It never publishes configuration or modifies player data.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"mhqserver/internal/itemcatalog"
	"mhqserver/internal/mysqlschema"
)

func main() {
	output := flag.String("output", "../文档/29-GM物品ID中文清单.csv", "中文 CSV 输出路径")
	flag.Parse()
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		log.Fatal("无法连接 MySQL")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := itemcatalog.Load(ctx, db)
	if err != nil {
		log.Fatalf("物品清单读取失败：%v", err)
	}
	file, err := os.Create(*output)
	if err != nil {
		log.Fatal(err)
	}
	err = itemcatalog.WriteCSV(file, items)
	closeErr := file.Close()
	if err != nil {
		log.Fatal(err)
	}
	if closeErr != nil {
		log.Fatal(closeErr)
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Category]++
	}
	fmt.Printf("已导出 %d 个物品到 %s\n分类：%v\n", len(items), *output, counts)
}
