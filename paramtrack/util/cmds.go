package util

import "fmt"

func CheckCmd(out []byte, err error) {
	if err != nil {
		fmt.Println(string(out))
		panic(err)
	}
}
