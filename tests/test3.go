package main

import (
	"fmt"

	"github.com/iDigitalFlame/xmt/cmd"
	"github.com/iDigitalFlame/xmt/device/devtools"
)

func test3Main() {
	if err := devtools.AdjustPrivileges("SeShutdownPrivilege", "SeUndockPrivilege"); err != nil {
		panic(err)
	}

	z := cmd.NewProcess("whoami", "/priv")
	o, err := z.CombinedOutput()
	if err != nil {
		panic(err)
	}
	fmt.Println(string(o))
}
