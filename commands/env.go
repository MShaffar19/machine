package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/docker/machine/commands/mcndirs"
	"github.com/docker/machine/libmachine"
	"github.com/docker/machine/libmachine/log"
)

const (
	envTmpl = `{{ .Prefix }}DOCKER_TLS_VERIFY{{ .Delimiter }}{{ .DockerTLSVerify }}{{ .Suffix }}{{ .Prefix }}DOCKER_HOST{{ .Delimiter }}{{ .DockerHost }}{{ .Suffix }}{{ .Prefix }}DOCKER_CERT_PATH{{ .Delimiter }}{{ .DockerCertPath }}{{ .Suffix }}{{ .Prefix }}DOCKER_MACHINE_NAME{{ .Delimiter }}{{ .MachineName }}{{ .Suffix }}{{ if .NoProxyVar }}{{ .Prefix }}{{ .NoProxyVar }}{{ .Delimiter }}{{ .NoProxyValue }}{{ .Suffix }}{{end}}{{ .UsageHint }}`
)

var (
	errImproperEnvArgs      = errors.New("Error: Expected one machine name")
	errImproperUnsetEnvArgs = errors.New("Error: Expected no machine name when the -u flag is present")
	defaultUsageHinter      UsageHintGenerator
)

func init() {
	defaultUsageHinter = &EnvUsageHintGenerator{}
}

type ShellConfig struct {
	Prefix          string
	Delimiter       string
	Suffix          string
	DockerCertPath  string
	DockerHost      string
	DockerTLSVerify string
	UsageHint       string
	MachineName     string
	NoProxyVar      string
	NoProxyValue    string
}

func cmdEnv(c CommandLine, api libmachine.API) error {
	var (
		err      error
		shellCfg *ShellConfig
	)

	// Ensure that log messages always go to stderr when this command is
	// being run (it is intended to be run in a subshell)
	log.SetOutWriter(os.Stderr)

	if c.Bool("unset") {
		shellCfg, err = shellCfgUnset(c, api)
		if err != nil {
			return err
		}
	} else {
		shellCfg, err = shellCfgSet(c, api)
		if err != nil {
			return err
		}
	}

	return executeTemplateStdout(shellCfg)
}

func shellCfgSet(c CommandLine, api libmachine.API) (*ShellConfig, error) {
	if len(c.Args()) != 1 {
		return nil, errImproperEnvArgs
	}

	host, err := api.Load(c.Args().First())
	if err != nil {
		return nil, err
	}

	dockerHost, _, err := defaultConnChecker.Check(host, c.Bool("swarm"))
	if err != nil {
		return nil, fmt.Errorf("Error checking TLS connection: %s", err)
	}

	userShell, err := getShell(c.String("shell"))
	if err != nil {
		return nil, err
	}

	shellCfg := &ShellConfig{
		DockerCertPath:  filepath.Join(mcndirs.GetMachineDir(), host.Name),
		DockerHost:      dockerHost,
		DockerTLSVerify: "1",
		UsageHint:       defaultUsageHinter.GenerateUsageHint(userShell, os.Args),
		MachineName:     host.Name,
	}

	if c.Bool("no-proxy") {
		ip, err := host.Driver.GetIP()
		if err != nil {
			return nil, fmt.Errorf("Error getting host IP: %s", err)
		}

		noProxyVar, noProxyValue := findNoProxyFromEnv()

		// add the docker host to the no_proxy list idempotently
		switch {
		case noProxyValue == "":
			noProxyValue = ip
		case strings.Contains(noProxyValue, ip):
		//ip already in no_proxy list, nothing to do
		default:
			noProxyValue = fmt.Sprintf("%s,%s", noProxyValue, ip)
		}

		shellCfg.NoProxyVar = noProxyVar
		shellCfg.NoProxyValue = noProxyValue
	}

	switch userShell {
	case "fish":
		shellCfg.Prefix = "set -gx "
		shellCfg.Suffix = "\";\n"
		shellCfg.Delimiter = " \""
	case "powershell":
		shellCfg.Prefix = "$Env:"
		shellCfg.Suffix = "\"\n"
		shellCfg.Delimiter = " = \""
	case "cmd":
		shellCfg.Prefix = "SET "
		shellCfg.Suffix = "\n"
		shellCfg.Delimiter = "="
	default:
		shellCfg.Prefix = "export "
		shellCfg.Suffix = "\"\n"
		shellCfg.Delimiter = "=\""
	}

	return shellCfg, nil
}

func shellCfgUnset(c CommandLine, api libmachine.API) (*ShellConfig, error) {
	if len(c.Args()) != 0 {
		return nil, errImproperUnsetEnvArgs
	}

	userShell, err := getShell(c.String("shell"))
	if err != nil {
		return nil, err
	}

	shellCfg := &ShellConfig{
		UsageHint: defaultUsageHinter.GenerateUsageHint(userShell, os.Args),
	}

	if c.Bool("no-proxy") {
		shellCfg.NoProxyVar, shellCfg.NoProxyValue = findNoProxyFromEnv()
	}

	switch userShell {
	case "fish":
		shellCfg.Prefix = "set -e "
		shellCfg.Suffix = ";\n"
		shellCfg.Delimiter = ""
	case "powershell":
		shellCfg.Prefix = `Remove-Item Env:\\`
		shellCfg.Suffix = "\n"
		shellCfg.Delimiter = ""
	case "cmd":
		shellCfg.Prefix = "SET "
		shellCfg.Suffix = "\n"
		shellCfg.Delimiter = "="
	default:
		shellCfg.Prefix = "unset "
		shellCfg.Suffix = "\n"
		shellCfg.Delimiter = ""
	}

	return shellCfg, nil
}

func executeTemplateStdout(shellCfg *ShellConfig) error {
	t := template.New("envConfig")
	tmpl, err := t.Parse(envTmpl)
	if err != nil {
		return err
	}

	return tmpl.Execute(os.Stdout, shellCfg)
}

func getShell(userShell string) (string, error) {
	if userShell != "" {
		return userShell, nil
	}
	return detectShell()
}

func findNoProxyFromEnv() (string, string) {
	// first check for an existing lower case no_proxy var
	noProxyVar := "no_proxy"
	noProxyValue := os.Getenv("no_proxy")

	// otherwise default to allcaps HTTP_PROXY
	if noProxyValue == "" {
		noProxyVar = "NO_PROXY"
		noProxyValue = os.Getenv("NO_PROXY")
	}
	return noProxyVar, noProxyValue
}

type UsageHintGenerator interface {
	GenerateUsageHint(string, []string) string
}

type EnvUsageHintGenerator struct{}

func (g *EnvUsageHintGenerator) GenerateUsageHint(userShell string, args []string) string {
	cmd := ""
	comment := "#"

	commandLine := strings.Join(args, " ")

	switch userShell {
	case "fish":
		cmd = fmt.Sprintf("eval (%s)", commandLine)
	case "powershell":
		cmd = fmt.Sprintf("%s | Invoke-Expression", commandLine)
	case "cmd":
		cmd = fmt.Sprintf("\tFOR /f \"tokens=*\" %%i IN ('%s') DO %%i", commandLine)
		comment = "REM"
	default:
		cmd = fmt.Sprintf("eval \"$(%s)\"", commandLine)
	}

	return fmt.Sprintf("%s Run this command to configure your shell: \n%s %s\n", comment, comment, cmd)
}

func detectShell() (string, error) {
	// attempt to get the SHELL env var
	shell := filepath.Base(os.Getenv("SHELL"))

	log.Debugf("shell: %s", shell)
	if shell == "" {
		// check for windows env and not bash (i.e. msysgit, etc)
		if runtime.GOOS == "windows" {
			log.Printf("On Windows, please specify either 'cmd' or 'powershell' with the --shell flag.\n\n")
		}

		return "", ErrUnknownShell
	}

	return shell, nil
}
