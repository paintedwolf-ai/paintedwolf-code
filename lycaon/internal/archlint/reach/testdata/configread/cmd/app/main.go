package main

import (
	"fmt"
	"os"

	"example.com/configread/conf"
)

func main() {
	c, _ := conf.Load([]byte(os.Getenv("CONFIG")))
	fmt.Println(conf.Describe(c))
}
