// +build darwin

package device

import (
	"fmt"
	"os/exec"
	"os/user"
	"strings"
)

const (
	// OS is the local machine's Operating System type.
	OS = Mac

	// Shell is the default machine specific command shell.
	Shell = "/bin/bash"
	// Newline is the machine specific newline character.
	Newline = "\n"
)

var (
	// ShellArgs is the default machine specific command shell arguments to run commands.
	ShellArgs = []string{"-c"}
)

func isElevated() bool {
	if a, err := user.Current(); err == nil && a.Uid == "0" {
		return true
	}
	return false
}
func getVersion() string {
	var b, n, v string
	if o, err := exec.Command(Shell, append(ShellArgs, "sw_vers")...).CombinedOutput(); err == nil {
		m := make(map[string]string)
		for _, v := range strings.Split(string(o), Newline) {
			if i := strings.Split(v, ":"); len(i) == 2 {
				m[strings.ToUpper(i[0])] = strings.Replace(i[1], "\"", "", -1)
			}
		}
		n, _ = m["PRODUCTNAME"]
		b, _ = m["BUILDVERSION"]
		v, _ = m["PRODUCTVERSION"]
	}
	if len(v) == 0 {
		if o, err := exec.Command("uname", "-r").CombinedOutput(); err == nil {
			v = strings.Replace(string(o), Newline, "", -1)
		}
	}
	switch {
	case len(n) == 0 && len(b) == 0 && len(v) == 0:
		return "MacOS (?)"
	case len(n) == 0 && len(b) > 0 && len(v) > 0:
		return fmt.Sprintf("MacOS (%s, %s)", v, b)
	case len(n) == 0 && len(b) == 0 && len(v) > 0:
		return fmt.Sprintf("MacOS (%s)", v)
	case len(n) == 0 && len(b) > 0 && len(v) == 0:
		return fmt.Sprintf("MacOS (%s)", b)
	case len(n) > 0 && len(b) > 0 && len(v) > 0:
		return fmt.Sprintf("%s (%s, %s)", n, v, b)
	case len(n) > 0 && len(b) == 0 && len(v) > 0:
		return fmt.Sprintf("%s (%s)", n, v)
	case len(n) > 0 && len(b) > 0 && len(v) == 0:
		return fmt.Sprintf("%s (%s)", n, b)
	}
	return "MacOS (?)"
}

// Registry attempts to open a registry value or key, value pair on Windows devices. Returns err if the system is
// not a Windows device or an error occurred during the open.
func Registry(_, _ string) (*RegistryFile, error) {
	return nil, ErrNoRegistry
}