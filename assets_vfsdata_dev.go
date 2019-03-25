// +build dev

package blockexplorer

import (
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
)

// Assets contains project assets.
var Assets http.FileSystem

// var Assets http.FileSystem = http.Dir("./webfiles")

func init() {
	var pwd string
	{
		pc := make([]uintptr, 10) // at least 1 entry needed
		runtime.Callers(1, pc)
		f := runtime.FuncForPC(pc[0])
		pwd, _ = f.FileLine(pc[0])

		path := strings.Split(pwd, "/")
		pwd = strings.Join(path[:len(path)-1], "/")
	}
	fmt.Println(pwd + "/webfiles")

	Assets = http.Dir(pwd + "/webfiles")
	_, err := Assets.Open(pwd + "/webfiles/pages/index.html")
	log.Println(err)
}
