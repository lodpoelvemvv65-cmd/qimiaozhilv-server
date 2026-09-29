package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

func main() {
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis address")
	redisPassword := flag.String("redis-password", "", "Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	flag.Parse()

	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var configNames, configNodes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT config_name), COUNT(*) FROM game_config_nodes`).Scan(&configNames, &configNodes); err != nil {
		log.Fatal(err)
	}
	var revisions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_config_revisions`).Scan(&revisions); err != nil {
		log.Fatal(err)
	}
	var legacy int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('players', 'families', 'consignment_items')
		AND COLUMN_NAME IN ('skills', 'auto_skills', 'tasks', 'is_online', 'bag_json', 'worn_json',
		'starsoul_json', 'store_json', 'pet_json', 'mails_json', 'friends_json', 'signin_json',
		'mainui_json', 'item_buffs_json', 'members_json', 'requests_json', 'gems')`).Scan(&legacy); err != nil {
		log.Fatal(err)
	}
	if legacy != 0 {
		log.Fatalf("legacy JSON columns remain: %d", legacy)
	}

	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr, Password: *redisPassword, DB: *redisDB})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatal(err)
	}
	countKeys := func(pattern string) int {
		count := 0
		iter := rdb.Scan(ctx, 0, pattern, 0).Iterator()
		for iter.Next(ctx) {
			count++
		}
		if err := iter.Err(); err != nil {
			log.Fatal(err)
		}
		return count
	}
	loginKeys := countKeys("mhq:login:key:*")
	loginVouchers := countKeys("mhq:login:voucher:*")
	onlineKeys := countKeys("mhq:online:player:*")
	fmt.Printf("MySQL=%s\nconfig_names=%d config_nodes=%d config_revisions=%d\nlegacy_json_columns=%d\nRedis=ok\nlogin_keys=%d login_vouchers=%d online_keys=%d\n",
		description, configNames, configNodes, revisions, legacy, loginKeys, loginVouchers, onlineKeys)
	if strings.TrimSpace(description) == "" || configNames < 77 || configNodes == 0 {
		log.Fatal("storage verification failed: incomplete configuration import")
	}
}
