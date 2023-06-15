package main

import (
	"fmt"
	"syncrets/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Printf("%s", err.Error())
	}
}
