package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	src := "K:/Release/vauldy_windows_amd64/data/knox-media.db"
	dst := "E:/Projects/PowerCOM/Knox/media/tools/diag_startup/tmp/copy2.db"
	if len(os.Args) > 1 {
		src = os.Args[1]
	}
	if len(os.Args) > 2 {
		dst = os.Args[2]
	}
	db, err := sql.Open("sqlite", src)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", dst)); err != nil {
		fmt.Println("vacuum:", err)
		os.Exit(1)
	}
	fmt.Println("snapshot written:", dst)
}
