package main
import (
    "database/sql"
    "fmt"
    "log"
    _ "github.com/lib/pq"
)
func main() {
    db, err := sql.Open("postgres", "host=127.0.0.1 port=15432 user=postgres password=postgres dbname=redstoneapi_preview sslmode=disable")
    if err != nil { log.Fatal(err) }
    defer db.Close()
    
    rows, err := db.Query("SELECT id, owner_user_id, name, platform, visibility, status FROM redstone_account_share_rooms LIMIT 10")
    if err != nil { log.Fatal(err) }
    defer rows.Close()
    
    count := 0
    for rows.Next() {
        var id int64
        var ownerID, name, platform, visibility, status string
        if err := rows.Scan(&id, &ownerID, &name, &platform, &visibility, &status); err != nil {
            log.Fatal(err)
        }
        fmt.Printf("ID: %d, Owner: %s, Name: %s, Platform: %s, Visibility: %s, Status: %s\n", id, ownerID, name, platform, visibility, status)
        count++
    }
    if count == 0 {
        fmt.Println("数据库中没有房间记录")
    }
}
