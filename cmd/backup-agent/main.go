package main

import (
	"fmt"

	"github.com/rodericliu/backup/internal/version"
)

func main() {
	fmt.Println("backup-agent", version.Version)
}
