package getuserinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"

	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/reexec"
)

const (
	getUserInfoCmd = "get-user-info"
)

func init() {
	reexec.Register(getUserInfoCmd, userInfoMain)
}

func userInfoMain() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: get-user-info usernameOrGroup")
		os.Exit(1)
	}
	username := os.Args[1]
	sid, _, _, err := syscall.LookupSID("", username)
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}

	sidAsString, err := sid.String()
	if err != nil {
		fmt.Println(err)
		os.Exit(4)
	}
	ident := idtools.Identity{
		SID: sidAsString,
	}
	asJson, err := json.Marshal(ident)
	if err != nil {
		fmt.Println(err)
		os.Exit(5)
	}
	fmt.Fprintf(os.Stdout, "%s", string(asJson))
}
