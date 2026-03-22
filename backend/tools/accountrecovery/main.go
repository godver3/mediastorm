package main

import (
	"log"
	"novastream/internal/accountrecovery"
	"os"
)

func main() {
	if err := accountrecovery.Run(os.Args[1:], os.Stdout, os.Getenv); err != nil {
		log.Fatal(err)
	}
}
